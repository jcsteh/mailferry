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

// Naming the mailbox to authorise, and proving afterwards that it really
// was the one that signed in.
//
// These tests exist because of a real failure: an operator whose tenant
// required admin consent signed in as the administrator to grant it, and
// single sign-on then handed the delegated refresh token to the
// administrator's mailbox instead of the user's. A profile bound to the
// wrong mailbox migrates the wrong mail.
package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestLoginHintIsSentAndSuppressesTheAccountPicker: the hint must reach the
// provider, and Microsoft's account picker must not be requested at the same
// time — select_account overrides login_hint, which would defeat the point.
func TestLoginHintIsSentAndSuppressesTheAccountPicker(t *testing.T) {
	tr := &fakeTransport{respond: func(string, int) (int, string) { return 200, "{}" }}

	for _, prov := range []string{"microsoft", "google"} {
		ts := authSource(t, prov, tr)
		ts.SetLoginHint("jeslyn@example.com")
		u, err := url.Parse(ts.authorizeURL("http://127.0.0.1:1/callback", "chal", "st"))
		if err != nil {
			t.Fatal(err)
		}
		if got := u.Query().Get("login_hint"); got != "jeslyn@example.com" {
			t.Fatalf("%s: login_hint = %q, want the named mailbox", prov, got)
		}
		if prov == "microsoft" && u.Query().Get("prompt") == "select_account" {
			t.Fatal("microsoft: select_account defeats login_hint and must not be sent with it")
		}
		if prov == "google" && u.Query().Get("prompt") != "consent" {
			t.Fatal("google still needs prompt=consent to guarantee a refresh token")
		}
	}
}

// TestNoLoginHintKeepsTheAccountPicker: without a named mailbox, Microsoft
// should still show the picker so a stale SSO session cannot silently
// choose for the operator.
func TestNoLoginHintKeepsTheAccountPicker(t *testing.T) {
	ts := authSource(t, "microsoft", &fakeTransport{
		respond: func(string, int) (int, string) { return 200, "{}" }})
	u, _ := url.Parse(ts.authorizeURL("http://127.0.0.1:1/callback", "chal", "st"))
	if u.Query().Get("prompt") != "select_account" {
		t.Fatal("without a hint the account picker must be requested")
	}
	if u.Query().Has("login_hint") {
		t.Fatal("no hint was set, so none must be sent")
	}
}

// TestAuthorisedAsDetectsTheWrongAccount is the actual safety check.
func TestAuthorisedAsDetectsTheWrongAccount(t *testing.T) {
	ts := authSource(t, "microsoft", &fakeTransport{
		respond: func(string, int) (int, string) { return 200, "{}" }})

	// No hint: nothing to contradict.
	if ok, _, _ := ts.AuthorisedAs(AuthResult{Account: "anyone@example.com"}); !ok {
		t.Fatal("without a hint any account must be accepted")
	}

	ts.SetLoginHint("jeslyn@example.com")

	// The administrator signed in instead — the case that prompted this.
	ok, want, got := ts.AuthorisedAs(AuthResult{Account: "itadmin@example.com"})
	if ok {
		t.Fatal("a different account must be reported as a mismatch")
	}
	if want != "jeslyn@example.com" || got != "itadmin@example.com" {
		t.Fatalf("mismatch must name both accounts, got want=%q got=%q", want, got)
	}

	// The right account, in whatever case the provider chose to return.
	if ok, _, _ := ts.AuthorisedAs(AuthResult{Account: "Jeslyn@Example.com"}); !ok {
		t.Fatal("account comparison must be case-insensitive")
	}

	// The provider did not say who signed in: unknown is not a mismatch,
	// because refusing on missing information would block legitimate
	// providers that return no identity claim.
	if ok, _, _ := ts.AuthorisedAs(AuthResult{Account: ""}); !ok {
		t.Fatal("an unidentified account must not be treated as a mismatch")
	}
}

// TestLoginHintEndToEnd drives the whole loopback flow with a hint and a
// provider that signs in a different user, proving the mismatch is caught
// where it matters rather than only in isolation.
func TestLoginHintEndToEnd(t *testing.T) {
	tr := &fakeTransport{respond: func(form string, n int) (int, string) {
		b, _ := json.Marshal(tokenResponse{AccessToken: "a", RefreshToken: "r", ExpiresIn: 3600,
			IDToken: fakeIDToken(map[string]string{"preferred_username": "itadmin@example.com"})})
		return 200, string(b)
	}}
	ts := authSource(t, "microsoft", tr)
	ts.SetLoginHint("jeslyn@example.com")

	var sentHint string
	opener := func(authURL string) error {
		u, _ := url.Parse(authURL)
		sentHint = u.Query().Get("login_hint")
		go http.Get(u.Query().Get("redirect_uri") + "?code=c&state=" + u.Query().Get("state"))
		return nil
	}
	res, err := ts.LoopbackAuthorize(context.Background(), opener, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sentHint != "jeslyn@example.com" {
		t.Fatalf("the hint never reached the authorise request: %q", sentHint)
	}
	ok, want, got := ts.AuthorisedAs(res)
	if ok {
		t.Fatal("signing in as somebody else must be detected")
	}
	if !strings.Contains(want+got, "@") {
		t.Fatalf("both accounts should be reportable: %q / %q", want, got)
	}
}
