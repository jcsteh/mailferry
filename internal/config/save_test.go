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
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveUpdatesActiveLinePreservingComments(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "mailferry.toml", `# MailFerry Configuration

[migration]
# Mailboxes migrated concurrently.
parallel_mailboxes = 10
per_host_connections = 8
`)
	if err := SaveValues(p, map[string]string{"migration.parallel_mailboxes": "20"}); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, p)
	if !strings.Contains(out, "parallel_mailboxes = 20") {
		t.Fatalf("value not updated:\n%s", out)
	}
	if !strings.Contains(out, "# Mailboxes migrated concurrently.") {
		t.Fatalf("comment not preserved:\n%s", out)
	}
	if !strings.Contains(out, "per_host_connections = 8") {
		t.Fatalf("unrelated key lost:\n%s", out)
	}
	// The file must still load back to the saved value (round-trip).
	cfg := Defaults()
	LoadTOML(cfg, p, false)
	if cfg.Workers != 20 {
		t.Fatalf("round-trip: workers=%d, want 20", cfg.Workers)
	}
}

func TestSaveActivatesCommentedLine(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "mailferry.toml", `[recovery]
# stale_timeout_seconds = 300
`)
	if err := SaveValues(p, map[string]string{"recovery.stale_timeout_seconds": "600"}); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, p)
	if !strings.Contains(out, "stale_timeout_seconds = 600") {
		t.Fatalf("commented line not activated:\n%s", out)
	}
	if strings.Contains(out, "# stale_timeout_seconds = 300") {
		t.Fatalf("stale commented default should be replaced:\n%s", out)
	}
}

func TestSaveAddsMissingKeyInSection(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "mailferry.toml", "[logging]\nlevel = \"info\"\n")
	if err := SaveValues(p, map[string]string{"logging.keep_days": "7"}); err != nil {
		t.Fatal(err)
	}
	cfg := Defaults()
	LoadTOML(cfg, p, false)
	if cfg.LogKeepDays != 7 {
		t.Fatalf("added key not effective: keep_days=%d", cfg.LogKeepDays)
	}
}

func TestSaveEnumIsQuoted(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "mailferry.toml", "[network]\ncompress = \"auto\"\n")
	if err := SaveValues(p, map[string]string{"network.compress": "off"}); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, p)
	if !strings.Contains(out, `compress = "off"`) {
		t.Fatalf("enum value must be quoted:\n%s", out)
	}
}

// TestSaveRejectsInvalidWithoutCorruption is the crash-safety guarantee: an
// out-of-range value is refused BEFORE the file is touched.
func TestSaveRejectsInvalidWithoutCorruption(t *testing.T) {
	dir := t.TempDir()
	orig := "[migration]\nparallel_mailboxes = 10\n"
	p := writeFile(t, dir, "mailferry.toml", orig)
	err := SaveValues(p, map[string]string{"migration.parallel_mailboxes": "9999"}) // > max 100
	if err == nil {
		t.Fatal("out-of-range value must be rejected")
	}
	if got := readFile(t, p); got != orig {
		t.Fatalf("file must be UNCHANGED after a rejected save:\n%s", got)
	}
}

func TestSaveUnknownOptionRejected(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "mailferry.toml", "[migration]\n")
	if err := SaveValues(p, map[string]string{"migration.not_a_real_option": "1"}); err == nil {
		t.Fatal("unknown option must be rejected")
	}
}

func TestSavePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions not applicable on Windows")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "mailferry.toml")
	if err := SaveValues(p, map[string]string{"migration.parallel_mailboxes": "12"}); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("saved config perms = %v, want 0600", st.Mode().Perm())
	}
}

// TestNoSecretIsSavable is the credential-protection guarantee: the option
// catalogue that SaveValues writes contains NO secret-bearing field, so the
// F9 editor can never persist a password or token.
func TestNoSecretIsSavable(t *testing.T) {
	// Secret-bearing terms (precise, so display "refresh_ms" is not a match).
	banned := []string{"password", "secret", "token", "credential", "passwd", "bearer"}
	for _, o := range Catalog() {
		name := strings.ToLower(o.Name() + " " + o.Label)
		for _, b := range banned {
			if strings.Contains(name, b) {
				t.Fatalf("option %q looks secret-bearing (%q) — must not be persistable", o.Name(), b)
			}
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
