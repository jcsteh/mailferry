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

package oauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretRefEnv(t *testing.T) {
	t.Setenv("MF_SECRET", "s3cr3t")
	got, err := SecretRef("env:MF_SECRET").Resolve()
	if err != nil || got != "s3cr3t" {
		t.Fatalf("env ref: got %q err %v", got, err)
	}
	if _, err := SecretRef("env:MF_NOT_SET_XYZ").Resolve(); err == nil {
		t.Fatal("missing env var must error")
	}
}

func TestSecretRefFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tok")
	os.WriteFile(p, []byte("  file-secret\n"), 0o600)
	got, err := SecretRef("file:" + p).Resolve()
	if err != nil || got != "file-secret" {
		t.Fatalf("file ref: got %q err %v", got, err)
	}
}

func TestValidateRefRejectsLiteral(t *testing.T) {
	// A literal secret (no scheme) must be rejected so tokens never end up
	// written into configuration.
	err := ValidateRef("refresh_token", "1//0eXXXXXXliteralrefreshtoken")
	if err == nil || !strings.Contains(err.Error(), "external secret reference") {
		t.Fatalf("literal secret must be rejected, got: %v", err)
	}
	// Empty is allowed (optional secret).
	if err := ValidateRef("client_secret", ""); err != nil {
		t.Fatalf("empty ref should be allowed: %v", err)
	}
	// Recognised schemes pass without resolving.
	for _, ref := range []string{"env:X", "file:/x", "keychain:svc", "cmd:echo hi"} {
		if err := ValidateRef("f", ref); err != nil {
			t.Fatalf("scheme %q should validate: %v", ref, err)
		}
	}
}

func TestResolveRejectsUnknownScheme(t *testing.T) {
	if _, err := SecretRef("plaintextsecret").Resolve(); err == nil {
		t.Fatal("a bare literal must not resolve")
	}
}
