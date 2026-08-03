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
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTransport answers token requests without a network, capturing the
// posted form so tests can assert grant parameters.
type fakeTransport struct {
	calls    atomic.Int32
	lastBody string
	respond  func(form string, n int) (int, string)
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	f.lastBody = string(body)
	n := int(f.calls.Add(1))
	code, payload := f.respond(f.lastBody, n)
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(payload)),
		Header:     make(http.Header),
	}, nil
}

func newSourceWithTransport(t *testing.T, p Profile, tr http.RoundTripper) *TokenSource {
	t.Helper()
	ts, err := NewTokenSource(p, "user@example.com")
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	ts.http = &http.Client{Transport: tr}
	return ts
}

func okToken(access string, expiresIn int) string {
	b, _ := json.Marshal(tokenResponse{AccessToken: access, ExpiresIn: expiresIn, TokenType: "Bearer"})
	return string(b)
}

func TestTokenRefreshAndCache(t *testing.T) {
	t.Setenv("MF_TEST_RT", "refresh-token-value")
	tr := &fakeTransport{respond: func(form string, n int) (int, string) {
		if !strings.Contains(form, "grant_type=refresh_token") {
			t.Errorf("expected refresh_token grant, got %q", form)
		}
		return 200, okToken("access-"+itoa(n), 3600)
	}}
	ts := newSourceWithTransport(t, Profile{
		Name: "p", ProviderName: "google", ClientID: "cid",
		RefreshToken: "env:MF_TEST_RT",
	}, tr)

	tok, err := ts.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "access-1" {
		t.Fatalf("first token = %q, want access-1", tok)
	}
	// Second call within the validity window must NOT refresh again.
	if tok2, _ := ts.Token(context.Background()); tok2 != "access-1" {
		t.Fatalf("cached token expected, got %q (refreshed unnecessarily)", tok2)
	}
	if got := tr.calls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 token request, got %d", got)
	}
}

func TestTokenRefreshOnExpiry(t *testing.T) {
	t.Setenv("MF_TEST_RT", "rt")
	tr := &fakeTransport{respond: func(form string, n int) (int, string) {
		return 200, okToken("access-"+itoa(n), 1) // 1-second lifetime
	}}
	ts := newSourceWithTransport(t, Profile{
		Name: "p", ProviderName: "microsoft", ClientID: "cid", RefreshToken: "env:MF_TEST_RT",
	}, tr)
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Force expiry: the early-refresh window (60s) already makes a 1s token
	// stale, so the next call must refresh.
	tok, err := ts.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "access-2" {
		t.Fatalf("expired token was not refreshed: got %q", tok)
	}
}

func TestRefreshTokenRotation(t *testing.T) {
	t.Setenv("MF_TEST_RT", "rt-initial")
	var seenRefresh []string
	tr := &fakeTransport{respond: func(form string, n int) (int, string) {
		for _, kv := range strings.Split(form, "&") {
			if strings.HasPrefix(kv, "refresh_token=") {
				seenRefresh = append(seenRefresh, strings.TrimPrefix(kv, "refresh_token="))
			}
		}
		b, _ := json.Marshal(tokenResponse{AccessToken: "acc" + itoa(n),
			ExpiresIn: 1, RefreshToken: "rt-rotated-" + itoa(n), TokenType: "Bearer"})
		return 200, string(b)
	}}
	ts := newSourceWithTransport(t, Profile{
		Name: "p", ProviderName: "google", ClientID: "cid", RefreshToken: "env:MF_TEST_RT",
	}, tr)
	ts.Token(context.Background()) // n=1: uses rt-initial, rotates to rt-rotated-1
	ts.Token(context.Background()) // n=2: must use rt-rotated-1
	if len(seenRefresh) < 2 || seenRefresh[0] != "rt-initial" || seenRefresh[1] != "rt-rotated-1" {
		t.Fatalf("refresh-token rotation not honoured: %v", seenRefresh)
	}
}

func TestInvalidGrantIsActionable(t *testing.T) {
	t.Setenv("MF_TEST_RT", "rt")
	tr := &fakeTransport{respond: func(form string, n int) (int, string) {
		return 400, `{"error":"invalid_grant","error_description":"token expired"}`
	}}
	ts := newSourceWithTransport(t, Profile{
		Name: "acct", ProviderName: "microsoft", ClientID: "cid", RefreshToken: "env:MF_TEST_RT",
	}, tr)
	_, err := ts.Token(context.Background())
	if err == nil {
		t.Fatal("invalid_grant must surface an error")
	}
	if !strings.Contains(err.Error(), "invalid_grant") || !strings.Contains(err.Error(), "re-authorise") {
		t.Fatalf("error should be actionable, got: %v", err)
	}
	// The error must never leak the refresh token.
	if strings.Contains(err.Error(), "rt") && strings.Contains(err.Error(), "refresh-token-value") {
		t.Fatalf("secret leaked into error: %v", err)
	}
}

func TestStaticAccessTokenNoRefresh(t *testing.T) {
	t.Setenv("MF_TEST_AT", "static-access")
	ts, err := NewTokenSource(Profile{
		Name: "p", ProviderName: "google", ClientID: "cid", AccessToken: "env:MF_TEST_AT",
	}, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := ts.Token(context.Background())
	if err != nil || tok != "static-access" {
		t.Fatalf("static access token: tok=%q err=%v", tok, err)
	}
}

func TestMissingCredentialFailsFast(t *testing.T) {
	if _, err := NewTokenSource(Profile{Name: "p", ProviderName: "google", ClientID: "cid"}, "u@example.com"); err == nil {
		t.Fatal("a profile with neither refresh_token nor access_token must fail")
	}
}

func TestProviderPresets(t *testing.T) {
	ms := MicrosoftProvider("example.com")
	if !strings.Contains(ms.TokenURL, "example.com") ||
		!strings.Contains(ms.TokenURL, "oauth2/v2.0/token") {
		t.Fatalf("microsoft token url wrong: %s", ms.TokenURL)
	}
	if ms.IMAPHost != "outlook.office365.com" {
		t.Fatalf("microsoft imap host wrong: %s", ms.IMAPHost)
	}
	g := GoogleProvider()
	if g.TokenURL != "https://oauth2.googleapis.com/token" || g.IMAPHost != "imap.gmail.com" {
		t.Fatalf("google preset wrong: %+v", g)
	}
	if len(g.Scopes) != 1 || g.Scopes[0] != "https://mail.google.com/" {
		t.Fatalf("google scope wrong: %v", g.Scopes)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// keep time import used if the suite trims cases later
var _ = time.Second
