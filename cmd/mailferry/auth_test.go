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

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/ajsap/mailferry/v2/internal/config"
	"github.com/ajsap/mailferry/v2/internal/paths"
)

// TestAuthBulkProfileLifecycle exercises the non-interactive auth lifecycle
// through the real command dispatch: add an application profile and a
// service-account profile, list them, and remove one — asserting the config
// carries only references and never a literal secret.
func TestAuthBulkProfileLifecycle(t *testing.T) {
	sandbox(t)
	// The config path comes from the same per-OS resolver the command uses,
	// so this holds on macOS/Windows too — not just XDG Linux.
	cfgPath := paths.Default().ConfigFile

	// Microsoft application (app-only) profile — non-interactive.
	if rc := run([]string{"auth", "add", "microsoft365", "--name", "tenant-bulk",
		"--kind", "application", "--client-id", "app-guid", "--tenant", "example.com",
		"--client-secret", "env:MS_APP_SECRET"}); rc != 0 {
		t.Fatalf("auth add application rc=%d", rc)
	}
	// Google service-account (domain-wide delegation) profile.
	if rc := run([]string{"auth", "add", "google", "--name", "ws-bulk",
		"--kind", "service_account", "--key-json", "file:/etc/mailferry/sa.json"}); rc != 0 {
		t.Fatalf("auth add service_account rc=%d", rc)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	// Structural checks via the parser (the template carries COMMENTED
	// example sections, so substring matching would give false positives).
	profiles, _, perr := config.LoadOAuthProfiles(cfgPath)
	if perr != nil {
		t.Fatal(perr)
	}
	if p := profiles["tenant-bulk"]; p.Kind != "application" || p.ClientSecret != "env:MS_APP_SECRET" {
		t.Fatalf("tenant-bulk profile wrong: %+v", p)
	}
	if p := profiles["ws-bulk"]; p.Kind != "service_account" || p.KeyJSON != "file:/etc/mailferry/sa.json" {
		t.Fatalf("ws-bulk profile wrong: %+v", p)
	}
	// A literal private key or password must never appear in the file.
	for _, bad := range []string{"BEGIN PRIVATE KEY", "password ="} {
		if strings.Contains(string(data), bad) {
			t.Fatalf("secret-shaped content leaked into config: %q", bad)
		}
	}

	// Duplicate add is refused.
	if rc := run([]string{"auth", "add", "microsoft365", "--name", "tenant-bulk",
		"--kind", "application", "--client-id", "x", "--tenant", "t", "--client-secret", "env:S"}); rc == 0 {
		t.Fatal("adding a duplicate profile name must fail")
	}

	// list and show run cleanly.
	if rc := run([]string{"auth", "list"}); rc != 0 {
		t.Fatalf("auth list rc=%d", rc)
	}
	if rc := run([]string{"auth", "show", "tenant-bulk"}); rc != 0 {
		t.Fatalf("auth show rc=%d", rc)
	}

	// remove deletes the section, leaving the other profile.
	if rc := run([]string{"auth", "remove", "ws-bulk"}); rc != 0 {
		t.Fatalf("auth remove rc=%d", rc)
	}
	profiles, _, _ = config.LoadOAuthProfiles(cfgPath)
	if _, ok := profiles["ws-bulk"]; ok {
		t.Fatal("removed profile still present")
	}
	if _, ok := profiles["tenant-bulk"]; !ok {
		t.Fatal("remove clobbered the other profile")
	}
}

// TestAuthAddValidationRejectsBadKind proves an invalid application profile is
// refused before anything is written.
func TestAuthAddValidationRejectsBadKind(t *testing.T) {
	sandbox(t)
	// Google application is not supported for IMAP — must be rejected.
	if rc := run([]string{"auth", "add", "google", "--name", "bad",
		"--kind", "application", "--client-id", "c", "--client-secret", "env:S"}); rc == 0 {
		t.Fatal("google application profile must be rejected")
	}
}
