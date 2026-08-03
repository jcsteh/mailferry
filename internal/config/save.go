// MailFerry — IMAP Migration & Sync
// High-Performance Native IMAP Migration Engine
//
// Copyright (C) 2026 Andy Saputra
// Author: Andy Saputra <andy@saputra.org>
// SPDX-License-Identifier: AGPL-3.0-or-later
//
// This file is part of MailFerry (https://github.com/ajsap/mailferry).
// Licensed under the GNU Affero General Public License v3.0 or later;
// see the LICENSE file for details.

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ajsap/mailferry/v2/internal/paths"
)

// SaveValues persists option values ("section.key" → value string, as the
// F9 Settings editor produces them) into the configuration file at path.
//
// Guarantees (the F9 persistence contract):
//   - every value is validated through the typed option catalogue BEFORE
//     anything is written — an invalid value aborts the whole save, so an
//     interrupted or buggy editor can never corrupt mailferry.toml;
//   - the write is atomic: a temp file in the same directory is renamed
//     over the original, so a crash mid-save leaves the old file intact;
//   - comments, ordering, unknown keys and unrelated lines are preserved:
//     an existing active "key = …" line is updated in place, a commented
//     "# key = …" line is activated in place, and a missing key is added
//     at the end of its [section] (the section is appended if absent);
//   - only catalogue options can be saved — credentials are not in the
//     catalogue, so no secret can ever travel through this path.
func SaveValues(path string, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	// Validation gate: every name must be a known option and every value
	// must satisfy its bounds on a scratch Run.
	scratch := Defaults()
	quoted := map[string]string{}
	for name, v := range values {
		o, found := FindOption(name)
		if !found {
			return fmt.Errorf("unknown setting %q", name)
		}
		if !o.Set(scratch, v) {
			return fmt.Errorf("invalid value %q for %s", v, name)
		}
		if o.Kind == KindEnum || o.Kind == KindString {
			quoted[name] = `"` + v + `"` // TOML string form
		} else {
			quoted[name] = v
		}
	}
	values = quoted

	raw := ""
	if data, err := os.ReadFile(path); err == nil {
		raw = string(data)
	} else {
		raw = TOMLTemplate // fresh file: start from the documented template
	}
	updated, err := applyValues(raw, values)
	if err != nil {
		return err
	}

	if err := paths.EnsureParent(path); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mailferry-toml-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(updated); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// applyValues performs the line-level edit described on SaveValues.
func applyValues(raw string, values map[string]string) (string, error) {
	pending := map[string]string{} // "section.key" → value, not yet placed
	for k, v := range values {
		pending[k] = v
	}
	lines := strings.Split(raw, "\n")
	sectionOf := func(line string) (string, bool) {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			return strings.TrimSpace(t[1 : len(t)-1]), true
		}
		return "", false
	}
	// keyOf recognises "key = …" and commented "# key = …" forms.
	keyOf := func(line string) (key string, commented bool, ok bool) {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			t = strings.TrimSpace(strings.TrimPrefix(t, "#"))
			commented = true
		}
		eq := strings.Index(t, "=")
		if eq <= 0 {
			return "", false, false
		}
		key = strings.TrimSpace(t[:eq])
		if key == "" || strings.ContainsAny(key, " \t[") {
			return "", false, false
		}
		return key, commented, true
	}

	section := ""
	sectionEnd := map[string]int{} // section → index AFTER its last line
	// Pass 1: replace active lines; remember section extents.
	for i, line := range lines {
		if s, isSect := sectionOf(line); isSect {
			section = s
			sectionEnd[section] = i + 1
			continue
		}
		if section == "" {
			continue
		}
		if strings.TrimSpace(line) != "" {
			sectionEnd[section] = i + 1
		}
		key, commented, ok := keyOf(line)
		if !ok {
			continue
		}
		full := section + "." + key
		v, want := pending[full]
		if !want {
			continue
		}
		if !commented {
			lines[i] = key + " = " + v
			delete(pending, full)
		}
	}
	// Pass 2: activate commented "# key = …" lines for keys still pending
	// (first commented occurrence in the right section wins).
	section = ""
	for i, line := range lines {
		if s, isSect := sectionOf(line); isSect {
			section = s
			continue
		}
		if section == "" || len(pending) == 0 {
			continue
		}
		key, commented, ok := keyOf(line)
		if !ok || !commented {
			continue
		}
		full := section + "." + key
		if v, want := pending[full]; want {
			lines[i] = key + " = " + v
			delete(pending, full)
		}
	}
	if len(pending) == 0 {
		return strings.Join(lines, "\n"), nil
	}
	// Pass 3: insert remaining keys at the end of their [section], or
	// append a new [section] at EOF — never re-open an existing section
	// header, so the file stays valid for strict TOML parsers.
	type insertion struct {
		at   int
		text string
	}
	var inserts []insertion
	newSect := map[string][]string{} // keys grouped per missing section
	names := make([]string, 0, len(pending))
	for full := range pending {
		names = append(names, full)
	}
	sort.Strings(names) // deterministic output
	for _, full := range names {
		v := pending[full]
		dot := strings.Index(full, ".")
		sect, key := full[:dot], full[dot+1:]
		if end, okS := sectionEnd[sect]; okS {
			inserts = append(inserts, insertion{end, key + " = " + v})
		} else {
			newSect[sect] = append(newSect[sect], key+" = "+v)
		}
	}
	var appendix []string
	sects := make([]string, 0, len(newSect))
	for s := range newSect {
		sects = append(sects, s)
	}
	sort.Strings(sects)
	for _, s := range sects {
		appendix = append(appendix, "", "["+s+"]")
		appendix = append(appendix, newSect[s]...)
	}
	// apply insertions bottom-up so earlier indices stay valid
	for i := 0; i < len(inserts); i++ {
		for j := i + 1; j < len(inserts); j++ {
			if inserts[j].at > inserts[i].at {
				inserts[i], inserts[j] = inserts[j], inserts[i]
			}
		}
	}
	for _, ins := range inserts {
		lines = append(lines[:ins.at], append([]string{ins.text}, lines[ins.at:]...)...)
	}
	out := strings.Join(lines, "\n")
	if len(appendix) > 0 {
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += strings.Join(appendix, "\n") + "\n"
	}
	return out, nil
}
