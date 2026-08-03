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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func httpClientWith(tr http.RoundTripper) *http.Client { return &http.Client{Transport: tr} }

func urlDecode(s string) (string, error) { return url.QueryUnescape(s) }

// TestClientCredentialsGrant proves the Microsoft application (app-only)
// grant posts the client-credentials form with the .default scope and that
// one profile authenticates different mailboxes via the SASL login.
func TestClientCredentialsGrant(t *testing.T) {
	t.Setenv("MF_CS", "app-secret")
	var gotForm string
	tr := &fakeTransport{respond: func(form string, n int) (int, string) {
		gotForm = form
		return 200, okToken("app-access-"+itoa(n), 3600)
	}}
	p := Profile{Name: "tenant", ProviderName: "microsoft", Kind: KindApplication,
		Tenant: "example.com", ClientID: "cid", ClientSecret: "env:MF_CS"}
	ts := newSourceForUser(t, p, "shared@example.com", tr)

	tok, err := ts.Token(context.Background())
	if err != nil || tok != "app-access-1" {
		t.Fatalf("client-credentials token: tok=%q err=%v", tok, err)
	}
	if !strings.Contains(gotForm, "grant_type=client_credentials") {
		t.Fatalf("expected client_credentials grant, got %q", gotForm)
	}
	if !strings.Contains(gotForm, "scope=https%3A%2F%2Foutlook.office365.com%2F.default") {
		t.Fatalf("expected the app-only .default scope, got %q", gotForm)
	}
	// The bound user is the SASL login — the token itself is user-independent.
	if sa := ts.SASLAuth(); sa.User != "shared@example.com" {
		t.Fatalf("SASL user = %q, want the target mailbox", sa.User)
	}
}

// TestServiceAccountJWTGrant proves the Google domain-wide-delegation grant
// signs a JWT impersonating the target user and posts a jwt-bearer assertion.
func TestServiceAccountJWTGrant(t *testing.T) {
	keyJSON := genServiceAccountKey(t, "svc@project.example")
	t.Setenv("MF_KEY", keyJSON)
	var gotForm string
	tr := &fakeTransport{respond: func(form string, n int) (int, string) {
		gotForm = form
		return 200, okToken("impersonated-access", 3600)
	}}
	p := Profile{Name: "workspace", ProviderName: "google", Kind: KindServiceAccount,
		ClientID: "unused", KeyJSON: "env:MF_KEY"}
	ts := newSourceForUser(t, p, "alice@example.com", tr)

	tok, err := ts.Token(context.Background())
	if err != nil || tok != "impersonated-access" {
		t.Fatalf("service-account token: tok=%q err=%v", tok, err)
	}
	if !strings.Contains(gotForm, "grant_type=urn") || !strings.Contains(gotForm, "jwt-bearer") {
		t.Fatalf("expected jwt-bearer grant, got %q", gotForm)
	}
	// The assertion must impersonate the target user (sub) and request mail.
	assertion := formValue(gotForm, "assertion")
	claims := decodeJWTClaims(t, assertion)
	if claims["sub"] != "alice@example.com" {
		t.Fatalf("JWT sub = %v, want the impersonated user", claims["sub"])
	}
	if claims["iss"] != "svc@project.example" {
		t.Fatalf("JWT iss = %v, want the service account", claims["iss"])
	}
	if s, _ := claims["scope"].(string); !strings.Contains(s, "mail.google.com") {
		t.Fatalf("JWT scope = %v, want the mail scope", claims["scope"])
	}
}

// TestServiceAccountPerUserImpersonation: the SAME profile impersonates
// different users — one admin profile → many mailboxes.
func TestServiceAccountPerUserImpersonation(t *testing.T) {
	keyJSON := genServiceAccountKey(t, "svc@project.example")
	t.Setenv("MF_KEY2", keyJSON)
	subs := map[string]bool{}
	tr := &fakeTransport{respond: func(form string, n int) (int, string) {
		claims := decodeJWTClaims(t, formValue(form, "assertion"))
		subs[claims["sub"].(string)] = true
		return 200, okToken("tok", 3600)
	}}
	p := Profile{Name: "ws", ProviderName: "google", Kind: KindServiceAccount, KeyJSON: "env:MF_KEY2"}
	for _, user := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		ts := newSourceForUser(t, p, user, tr)
		if _, err := ts.Token(context.Background()); err != nil {
			t.Fatalf("token for %s: %v", user, err)
		}
	}
	for _, user := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		if !subs[user] {
			t.Fatalf("profile did not impersonate %s", user)
		}
	}
}

func TestServiceAccountRequiresUser(t *testing.T) {
	keyJSON := genServiceAccountKey(t, "svc@project.example")
	t.Setenv("MF_KEY3", keyJSON)
	p := Profile{Name: "ws", ProviderName: "google", Kind: KindServiceAccount, KeyJSON: "env:MF_KEY3"}
	if _, err := NewTokenSource(p, ""); err == nil {
		t.Fatal("service_account without a target user must fail")
	}
}

// --- helpers --------------------------------------------------------------

func newSourceForUser(t *testing.T, p Profile, user string, tr *fakeTransport) *TokenSource {
	t.Helper()
	ts, err := NewTokenSource(p, user)
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	ts.SetHTTPClient(httpClientWith(tr))
	return ts
}

func genServiceAccountKey(t *testing.T, email string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
	b, _ := json.Marshal(serviceAccountKey{
		ClientEmail: email,
		PrivateKey:  string(pemBytes),
		TokenURI:    "https://oauth2.googleapis.com/token",
		Type:        "service_account",
	})
	return string(b)
}

func decodeJWTClaims(t *testing.T, jwt string) map[string]any {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion is not a 3-part JWT: %q", jwt)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("claims json: %v", err)
	}
	return m
}

func formValue(form, key string) string {
	for _, kv := range strings.Split(form, "&") {
		if strings.HasPrefix(kv, key+"=") {
			v := strings.TrimPrefix(kv, key+"=")
			dec, _ := urlDecode(v)
			return dec
		}
	}
	return ""
}
