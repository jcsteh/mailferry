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
	"strings"

	"github.com/ajsap/mailferry/v2/internal/paths"
)

// WriteOAuthProfile persists an [oauth.<name>] section to the config file at
// path, atomically and comment-preserving for the rest of the file. Only
// external secret *references* are written — the profile must already carry
// keychain:/file:/env:/cmd: references, never literal secrets (Validate
// enforces this). An existing section of the same name is replaced in place.
func WriteOAuthProfile(path string, p OAuthProfile) error {
	if p.Name == "" {
		return fmt.Errorf("oauth profile needs a name")
	}
	if err := p.Validate(); err != nil {
		return err
	}
	raw := ""
	if data, err := os.ReadFile(path); err == nil {
		raw = string(data)
	} else {
		raw = TOMLTemplate
	}
	raw = removeSection(raw, "oauth."+p.Name)
	block := renderOAuthSection(p)
	if !strings.HasSuffix(raw, "\n") {
		raw += "\n"
	}
	raw += "\n" + block
	return atomicWrite(path, raw)
}

// DeleteOAuthProfile removes an [oauth.<name>] section from the config file.
// A missing section (or missing file) is not an error.
func DeleteOAuthProfile(path, name string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	updated := removeSection(string(data), "oauth."+name)
	if updated == string(data) {
		return nil // nothing to remove
	}
	return atomicWrite(path, updated)
}

// renderOAuthSection formats one profile as a TOML section (references only).
func renderOAuthSection(p OAuthProfile) string {
	var b strings.Builder
	b.WriteString("[oauth." + p.Name + "]\n")
	kv := func(k, v string) {
		if v != "" {
			b.WriteString(fmt.Sprintf("%s = %q\n", k, v))
		}
	}
	kv("provider", p.Provider)
	if p.Kind != "" && p.Kind != "delegated" {
		kv("kind", p.Kind)
	}
	kv("tenant", p.Tenant)
	kv("account", p.Account)
	kv("authorised_at", p.AuthorisedAt)
	kv("client_id", p.ClientID)
	kv("mechanism", p.Mechanism)
	if len(p.Scopes) > 0 {
		kv("scopes", strings.Join(p.Scopes, " "))
	}
	kv("token_url", p.TokenURL)
	// Secret REFERENCES only.
	kv("client_secret", p.ClientSecret)
	kv("refresh_token", p.RefreshToken)
	kv("access_token", p.AccessToken)
	kv("key_json", p.KeyJSON)
	return b.String()
}

// removeSection deletes a [section] header and its body (up to the next
// section header or EOF) from a TOML document, leaving everything else — and
// its comments — intact.
func removeSection(raw, section string) string {
	lines := strings.Split(raw, "\n")
	var out []string
	inTarget := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			name := strings.TrimSpace(t[1 : len(t)-1])
			inTarget = name == section
			if inTarget {
				continue // drop the header
			}
		}
		if inTarget {
			continue // drop body lines until the next section
		}
		out = append(out, line)
	}
	res := strings.Join(out, "\n")
	// collapse the blank-line gap the removal may leave.
	for strings.Contains(res, "\n\n\n") {
		res = strings.ReplaceAll(res, "\n\n\n", "\n\n")
	}
	return res
}

// atomicWrite writes content to path via a temp file + rename, 0600.
func atomicWrite(path, content string) error {
	if err := paths.EnsureParent(path); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mailferry-toml-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
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
