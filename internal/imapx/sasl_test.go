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

package imapx

import (
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ajsap/mailferry/v2/internal/fakeimap"
)

// TestXOAUTH2GoldenVectors pins the SASL XOAUTH2 encoding against the exact
// example strings published in the Microsoft and Google IMAP OAuth docs, so
// a future refactor can never silently change the on-the-wire format.
//
// NOTE ON THE DOMAIN: the Microsoft vector below uses
// "test@contoso.onmicrosoft.com", which is NOT an RFC 2606 reserved name.
// That is deliberate and is the one permitted exception to MailFerry's
// examples-only-use-reserved-namespaces rule: this string is QUOTED EVIDENCE
// from Microsoft's own documentation, and the base64 expectation encodes it.
// Substituting example.com would still test the encoder, but it would no
// longer prove MailFerry emits the bytes Microsoft publishes — which is the
// entire point of a golden vector. It is not anyone's real address.
func TestXOAUTH2GoldenVectors(t *testing.T) {
	// Microsoft Learn example.
	msWant := "dXNlcj10ZXN0QGNvbnRvc28ub25taWNyb3NvZnQuY29tAWF1dGg9QmVhcmVy" +
		"IEV3QkFBbDNCQUFVRkZwVUFvN0ozVmUwYmpMQldaV0NjbFJDM0VvQUEBAQ=="
	if got := XOAUTH2Response("test@contoso.onmicrosoft.com",
		"EwBAAl3BAAUFFpUAo7J3Ve0bjLBWZWCclRC3EoAA"); got != msWant {
		t.Fatalf("XOAUTH2 Microsoft vector mismatch:\n got %s\nwant %s", got, msWant)
	}
	// Google/Microsoft shared example (someuser@example.com / ya29...).
	googWant := "dXNlcj1zb21ldXNlckBleGFtcGxlLmNvbQFhdXRoPUJlYXJlciB5YTI5LnZG" +
		"OWRmdDRxbVRjMk52YjNSbGNrQmhkSFJoZG1semRHRXVZMjl0Q2cBAQ=="
	if got := XOAUTH2Response("someuser@example.com",
		"ya29.vF9dft4qmTc2Nvb3RlckBhdHRhdmlzdGEuY29tCg"); got != googWant {
		t.Fatalf("XOAUTH2 Google vector mismatch:\n got %s\nwant %s", got, googWant)
	}
}

// TestOAUTHBEARERFormat verifies the RFC 7628 gs2 framing.
func TestOAUTHBEARERFormat(t *testing.T) {
	got := OAUTHBEARERResponse("alice@example.com", "imap.example.com", 993, "TOKEN123")
	dec := mustDecode(t, got)
	want := "n,a=alice@example.com,\x01host=imap.example.com\x01port=993\x01auth=Bearer TOKEN123\x01\x01"
	if dec != want {
		t.Fatalf("OAUTHBEARER framing mismatch:\n got %q\nwant %q", dec, want)
	}
}

// --- end-to-end AUTHENTICATE against the fake server ----------------------

func dialAuth(t *testing.T, s *fakeimap.Server, ep Endpoint, auth *SASLAuth,
	log func(string)) (*Client, error) {
	t.Helper()
	c := NewClient(ep, 5*time.Second, true, nil, "test", log)
	c.Trace = true
	c.Auth = auth
	if err := c.Connect(); err != nil {
		return nil, err
	}
	return c, c.Login()
}

func TestSASLAuthenticateSuccess(t *testing.T) {
	acct := fakeimap.NewAccount("alice@example.com", "unused")
	acct.OAuthToken = "good-access-token"
	s := fakeimap.NewServer(acct)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	auth := &SASLAuth{Mechanism: MechXOAUTH2, User: "alice@example.com",
		TokenFn: func(context.Context) (string, error) { return "good-access-token", nil }}
	c, err := dialAuth(t, s, epFor(s, "alice@example.com"), auth, nil)
	if err != nil {
		t.Fatalf("XOAUTH2 login should succeed: %v", err)
	}
	c.Logout(2 * time.Second)
}

func TestSASLAuthenticateBadToken(t *testing.T) {
	acct := fakeimap.NewAccount("alice@example.com", "unused")
	acct.OAuthToken = "good-access-token"
	s := fakeimap.NewServer(acct)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	auth := &SASLAuth{Mechanism: MechXOAUTH2, User: "alice@example.com",
		TokenFn: func(context.Context) (string, error) { return "WRONG-token", nil }}
	_, err := dialAuth(t, s, epFor(s, "alice@example.com"), auth, nil)
	if err == nil {
		t.Fatal("a bad bearer token must fail authentication, not succeed")
	}
	if _, ok := err.(*AuthErr); !ok {
		t.Fatalf("want *AuthErr, got %T: %v", err, err)
	}
}

func TestSASLMechanismNotAdvertised(t *testing.T) {
	// Account without an OAuth token → server never advertises AUTH=XOAUTH2.
	s := fakeimap.NewServer(fakeimap.NewAccount("bob@example.com", "pw"))
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	auth := &SASLAuth{Mechanism: MechXOAUTH2, User: "bob@example.com",
		TokenFn: func(context.Context) (string, error) { return "tok", nil }}
	_, err := dialAuth(t, s, epFor(s, "bob@example.com"), auth, nil)
	if err == nil || !strings.Contains(err.Error(), "does not advertise AUTH=XOAUTH2") {
		t.Fatalf("want an actionable 'not advertised' error, got: %v", err)
	}
}

// TestSASLTokenNeverTraced is the security regression: with --trace on, the
// bearer token must NEVER appear in the protocol trace — only the redacted
// placeholder.
func TestSASLTokenNeverTraced(t *testing.T) {
	const token = "SUPER-SECRET-BEARER-abc123"
	acct := fakeimap.NewAccount("alice@example.com", "unused")
	acct.OAuthToken = token
	s := fakeimap.NewServer(acct)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	var mu sync.Mutex
	var trace strings.Builder
	log := func(line string) { mu.Lock(); trace.WriteString(line + "\n"); mu.Unlock() }
	auth := &SASLAuth{Mechanism: MechXOAUTH2, User: "alice@example.com",
		TokenFn: func(context.Context) (string, error) { return token, nil }}
	c, err := dialAuth(t, s, epFor(s, "alice@example.com"), auth, log)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	c.Logout(2 * time.Second)

	mu.Lock()
	got := trace.String()
	mu.Unlock()
	if strings.Contains(got, token) {
		t.Fatalf("bearer token LEAKED into the trace:\n%s", got)
	}
	// Also make sure the base64-encoded response never leaked verbatim.
	if strings.Contains(got, XOAUTH2Response("alice@example.com", token)) {
		t.Fatalf("encoded SASL response leaked into the trace:\n%s", got)
	}
	if !strings.Contains(got, "AUTHENTICATE XOAUTH2 ****") {
		t.Fatalf("expected redacted AUTHENTICATE line in the trace:\n%s", got)
	}
}

func epFor(s *fakeimap.Server, user string) Endpoint {
	host, port := splitAddr(s.Addr)
	return Endpoint{Host: host, Port: port, Security: "none", User: user}
}

func splitAddr(addr string) (string, int) {
	i := strings.LastIndex(addr, ":")
	host := addr[:i]
	p := 0
	for _, r := range addr[i+1:] {
		p = p*10 + int(r-'0')
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return host, p
}

func mustDecode(t *testing.T, b64 string) string {
	t.Helper()
	dec, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	return string(dec)
}
