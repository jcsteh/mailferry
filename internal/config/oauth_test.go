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
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadOAuthProfiles(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "mailferry.toml", `
[migration]
parallel_mailboxes = 4

[oauth.ms-work]
provider = "microsoft"
tenant = "example.com"
client_id = "abc-123"
refresh_token = "env:MS_RT"

[oauth.gmail]
provider = "google"
client_id = "goog.apps"
client_secret = "file:/etc/x"
refresh_token = "env:G_RT"
scopes = "https://mail.google.com/"
`)
	profiles, _, err := LoadOAuthProfiles(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(profiles))
	}
	ms := profiles["ms-work"]
	if ms.Provider != "microsoft" || ms.Tenant != "example.com" ||
		ms.ClientID != "abc-123" || ms.RefreshToken != "env:MS_RT" {
		t.Fatalf("ms-work parsed wrong: %+v", ms)
	}
	if g := profiles["gmail"]; len(g.Scopes) != 1 || g.Scopes[0] != "https://mail.google.com/" {
		t.Fatalf("gmail scopes wrong: %+v", g)
	}
}

func TestLoadOAuthProfilesMissingFile(t *testing.T) {
	profiles, _, err := LoadOAuthProfiles(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || len(profiles) != 0 {
		t.Fatalf("missing file must yield no profiles and no error: %v", err)
	}
}

func TestProfileValidation(t *testing.T) {
	cases := []struct {
		name    string
		p       OAuthProfile
		wantErr string
	}{
		{"ok-microsoft", OAuthProfile{Name: "a", Provider: "microsoft", ClientID: "c", RefreshToken: "env:X"}, ""},
		{"literal-secret", OAuthProfile{Name: "a", Provider: "google", ClientID: "c",
			RefreshToken: "1//literaltoken"}, "external secret reference"},
		{"unknown-provider", OAuthProfile{Name: "a", Provider: "yahoo", ClientID: "c",
			RefreshToken: "env:X"}, "unknown provider"},
		{"generic-no-url", OAuthProfile{Name: "a", Provider: "generic", ClientID: "c",
			RefreshToken: "env:X"}, "token_url"},
		{"no-client", OAuthProfile{Name: "a", Provider: "google", RefreshToken: "env:X"}, "client_id"},
		{"no-cred", OAuthProfile{Name: "a", Provider: "google", ClientID: "c"}, "refresh_token"},
		{"bad-mechanism", OAuthProfile{Name: "a", Provider: "google", ClientID: "c",
			RefreshToken: "env:X", Mechanism: "PLAIN"}, "mechanism"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.p.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestCSVBackwardCompatAndOAuthRef proves password rows are untouched and an
// "oauth:<name>" password cell is parsed as an auth reference (with the
// password cleared), keeping the canonical 10-column format.
func TestCSVBackwardCompatAndOAuthRef(t *testing.T) {
	dir := t.TempDir()
	csv := writeFile(t, dir, "m.csv", strings.Join([]string{
		"srchost,srcport,srcsecurity,srcuser,srcpassword,dsthost,dstport,dstsecurity,dstuser,dstpassword",
		"imap.example.com,993,ssl,alice@example.com,SECRETPW,imap.example.net,993,ssl,alice@example.net,DESTPW",
		"outlook.office365.com,993,ssl,bob@example.com,oauth:ms-work,imap.gmail.com,993,ssl,bob@example.net,oauth:gmail",
	}, "\n"))
	specs, err := ParseCSV(csv)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(specs))
	}
	// Row 1: unchanged password behaviour.
	if specs[0].Src.Password != "SECRETPW" || specs[0].SrcAuth != "" ||
		specs[0].Dst.Password != "DESTPW" || specs[0].DstAuth != "" {
		t.Fatalf("password row must be unchanged: %+v", specs[0])
	}
	// Row 2: OAuth references, passwords cleared.
	if specs[1].SrcAuth != "ms-work" || specs[1].Src.Password != "" {
		t.Fatalf("src oauth ref wrong: auth=%q pw=%q", specs[1].SrcAuth, specs[1].Src.Password)
	}
	if specs[1].DstAuth != "gmail" || specs[1].Dst.Password != "" {
		t.Fatalf("dst oauth ref wrong: auth=%q pw=%q", specs[1].DstAuth, specs[1].Dst.Password)
	}
}

func TestProfileKindValidation(t *testing.T) {
	cases := []struct {
		name    string
		p       OAuthProfile
		wantErr string
	}{
		{"app-ok", OAuthProfile{Name: "a", Provider: "microsoft", Kind: "application",
			ClientID: "c", Tenant: "t", ClientSecret: "env:S"}, ""},
		{"app-needs-secret", OAuthProfile{Name: "a", Provider: "microsoft", Kind: "application",
			ClientID: "c", Tenant: "t"}, "client_secret"},
		{"app-needs-tenant", OAuthProfile{Name: "a", Provider: "microsoft", Kind: "application",
			ClientID: "c", ClientSecret: "env:S"}, "tenant"},
		{"google-app-unsupported", OAuthProfile{Name: "a", Provider: "google", Kind: "application",
			ClientID: "c", ClientSecret: "env:S"}, "service_account"},
		{"sa-ok", OAuthProfile{Name: "a", Provider: "google", Kind: "service_account",
			KeyJSON: "file:/k.json"}, ""},
		{"sa-needs-key", OAuthProfile{Name: "a", Provider: "google", Kind: "service_account"}, "key_json"},
		{"ms-sa-unsupported", OAuthProfile{Name: "a", Provider: "microsoft", Kind: "service_account",
			KeyJSON: "file:/k.json"}, "application"},
		{"unknown-kind", OAuthProfile{Name: "a", Provider: "google", Kind: "wizardry",
			ClientID: "c", RefreshToken: "env:R"}, "unknown kind"},
		{"literal-key", OAuthProfile{Name: "a", Provider: "google", Kind: "service_account",
			KeyJSON: "{\"private_key\":\"...\"}"}, "external secret reference"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.p.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestWriteAndDeleteOAuthProfile proves a profile round-trips through the
// atomic writer (references only, comments preserved) and can be removed.
func TestWriteAndDeleteOAuthProfile(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "mailferry.toml", `# MailFerry Configuration

[migration]
parallel_mailboxes = 8
`)
	prof := OAuthProfile{Name: "client-a", Provider: "microsoft", ClientID: "guid",
		Tenant: "example.com", RefreshToken: "keychain:mailferry/client-a"}
	if err := WriteOAuthProfile(p, prof); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, p)
	if !strings.Contains(out, "[oauth.client-a]") || !strings.Contains(out, `refresh_token = "keychain:mailferry/client-a"`) {
		t.Fatalf("profile section not written:\n%s", out)
	}
	if !strings.Contains(out, "parallel_mailboxes = 8") || !strings.Contains(out, "# MailFerry Configuration") {
		t.Fatalf("unrelated content/comments lost:\n%s", out)
	}
	// It loads back.
	profiles, _, err := LoadOAuthProfiles(p)
	if err != nil || profiles["client-a"].ClientID != "guid" {
		t.Fatalf("profile did not round-trip: %+v err=%v", profiles["client-a"], err)
	}
	// No literal secret ever appears.
	if strings.Contains(out, "BEGIN PRIVATE KEY") || strings.Contains(strings.ToLower(out), "password =") {
		t.Fatalf("a secret leaked into the config:\n%s", out)
	}
	// Rewriting the same name replaces (does not duplicate) the section.
	prof.ClientID = "guid2"
	WriteOAuthProfile(p, prof)
	if n := strings.Count(readFile(t, p), "[oauth.client-a]"); n != 1 {
		t.Fatalf("expected exactly one section after rewrite, got %d", n)
	}
	// Delete removes it, leaving other content intact.
	if err := DeleteOAuthProfile(p, "client-a"); err != nil {
		t.Fatal(err)
	}
	out = readFile(t, p)
	if strings.Contains(out, "[oauth.client-a]") {
		t.Fatalf("section not removed:\n%s", out)
	}
	if !strings.Contains(out, "parallel_mailboxes = 8") {
		t.Fatalf("delete clobbered unrelated content:\n%s", out)
	}
}

func TestValidateOAuthUndefinedProfile(t *testing.T) {
	specs := []MailboxSpec{{Index: 1, SrcAuth: "missing",
		Src: Endpoint{User: "a@example.com"}, Dst: Endpoint{User: "a@example.net"}}}
	err := ValidateOAuth(specs, map[string]OAuthProfile{})
	if err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("undefined profile must error, got %v", err)
	}
	// A defined-but-invalid profile is also caught.
	err = ValidateOAuth(
		[]MailboxSpec{{Index: 1, SrcAuth: "p", Src: Endpoint{User: "a@example.com"}}},
		map[string]OAuthProfile{"p": {Name: "p", Provider: "google"}}, // no client_id / cred
	)
	if err == nil || !strings.Contains(err.Error(), "client_id") {
		t.Fatalf("invalid referenced profile must error, got %v", err)
	}
}
