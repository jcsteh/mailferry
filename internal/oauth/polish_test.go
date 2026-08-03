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

// Tests for the operator-facing OAuth behaviour: which account was
// authorised, the headless copy/paste path, shared administrative token
// sources, and the cross-platform coherence of the secret store.
//
// No test here contacts Microsoft or Google, launches a browser, or needs a
// real credential.
package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// --- account discovery -----------------------------------------------------

// fakeIDToken builds an unsigned OIDC-shaped token carrying claims. The
// signature is irrelevant: MailFerry uses the claim as a LABEL only.
func fakeIDToken(claims map[string]string) string {
	body, _ := json.Marshal(claims)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(body) + ".sig"
}

func TestAccountFromIDToken(t *testing.T) {
	cases := []struct {
		claims map[string]string
		want   string
	}{
		{map[string]string{"email": "jeslyn@example.com"}, "jeslyn@example.com"},
		{map[string]string{"preferred_username": "andy@example.org"}, "andy@example.org"},
		{map[string]string{"upn": "ops@example.net"}, "ops@example.net"},
		{map[string]string{"sub": "0000-1111"}, ""}, // not an address: not shown
		{map[string]string{}, ""},
	}
	for _, c := range cases {
		if got := accountFromIDToken(fakeIDToken(c.claims)); got != c.want {
			t.Fatalf("claims %v -> %q, want %q", c.claims, got, c.want)
		}
	}
	// Malformed input must never panic or produce noise.
	for _, bad := range []string{"", "notatoken", "a.b", "a.!!!.c"} {
		if got := accountFromIDToken(bad); got != "" {
			t.Fatalf("malformed token %q produced %q", bad, got)
		}
	}
}

// TestInteractiveScopesRequestIdentity: the authorise URL must ask for the
// identity-only OIDC scopes (so the account can be reported) alongside the
// mail scope and offline access.
func TestInteractiveScopesRequestIdentity(t *testing.T) {
	for _, prov := range []string{"microsoft", "google"} {
		ts := authSource(t, prov, &fakeTransport{
			respond: func(string, int) (int, string) { return 200, "{}" }})
		u, err := url.Parse(ts.authorizeURL("http://127.0.0.1:1/callback", "chal", "st"))
		if err != nil {
			t.Fatal(err)
		}
		scope := u.Query().Get("scope")
		for _, want := range []string{"openid", "email"} {
			if !strings.Contains(scope, want) {
				t.Fatalf("%s authorise scope %q is missing %q", prov, scope, want)
			}
		}
		if prov == "microsoft" && !strings.Contains(scope, "offline_access") {
			t.Fatalf("microsoft scope must include offline_access: %q", scope)
		}
		if prov == "google" && u.Query().Get("access_type") != "offline" {
			t.Fatal("google must request offline access for a refresh token")
		}
		// PKCE is mandatory for a native client (RFC 8252).
		if u.Query().Get("code_challenge_method") != "S256" {
			t.Fatal("the loopback flow must use PKCE S256")
		}
	}
}

// TestAuthorisedAccountIsReported: a completed authorisation carries the
// signed-in account back to the caller for display and profile labelling.
func TestAuthorisedAccountIsReported(t *testing.T) {
	tr := &fakeTransport{respond: func(form string, n int) (int, string) {
		b, _ := json.Marshal(tokenResponse{AccessToken: "a", RefreshToken: "r",
			ExpiresIn: 3600, IDToken: fakeIDToken(map[string]string{"email": "jeslyn@example.com"})})
		return 200, string(b)
	}}
	ts := authSource(t, "microsoft", tr)
	opener := func(authURL string) error {
		u, _ := url.Parse(authURL)
		go http.Get(u.Query().Get("redirect_uri") + "?code=c&state=" + u.Query().Get("state"))
		return nil
	}
	res, err := ts.LoopbackAuthorize(context.Background(), opener, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Account != "jeslyn@example.com" {
		t.Fatalf("account not reported: %+v", res)
	}
}

// --- headless copy/paste path ---------------------------------------------

// TestManualRedirectPasteCompletesAuthorisation: the officially-supported
// loopback + PKCE flow, with the code carried back by copy and paste. This
// is what a remote migration server with no browser uses.
func TestManualRedirectPasteCompletesAuthorisation(t *testing.T) {
	tr := &fakeTransport{respond: func(form string, n int) (int, string) {
		if !strings.Contains(form, "code_verifier=") {
			t.Errorf("the pasted-code path must still send the PKCE verifier: %q", form)
		}
		b, _ := json.Marshal(tokenResponse{AccessToken: "a", RefreshToken: "pasted-refresh",
			ExpiresIn: 3600})
		return 200, string(b)
	}}
	ts := authSource(t, "google", tr)

	var authURL string
	ts.SetManualCode(func(ctx context.Context) (string, error) {
		// Emulate the operator pasting the address their browser was sent to.
		u, _ := url.Parse(authURL)
		return u.Query().Get("redirect_uri") + "?code=pasted-code&state=" +
			u.Query().Get("state"), nil
	})
	res, err := ts.LoopbackAuthorize(context.Background(), nil, func(u string) { authURL = u })
	if err != nil {
		t.Fatalf("manual paste authorisation: %v", err)
	}
	if res.RefreshToken != "pasted-refresh" {
		t.Fatalf("refresh token not captured via paste: %+v", res)
	}
}

func TestPastedRedirectParsing(t *testing.T) {
	// A bare code is accepted.
	if c, err := codeFromPastedRedirect("just-a-code", "st"); err != nil || c != "just-a-code" {
		t.Fatalf("bare code: %q %v", c, err)
	}
	// A full redirect URL is accepted and its state verified.
	full := "http://127.0.0.1:5555/callback?code=abc&state=st"
	if c, err := codeFromPastedRedirect(full, "st"); err != nil || c != "abc" {
		t.Fatalf("full URL: %q %v", c, err)
	}
	// A mismatched state is a possible CSRF and must be refused.
	if _, err := codeFromPastedRedirect(full, "other"); err == nil ||
		!strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("state mismatch must be refused, got %v", err)
	}
	// A provider error in the URL is reported as a denial, not a parse error.
	if _, err := codeFromPastedRedirect(
		"http://127.0.0.1:1/callback?error=access_denied", "st"); err == nil ||
		!strings.Contains(err.Error(), "denied") {
		t.Fatalf("denial should be reported as such, got %v", err)
	}
	// A URL with no code is refused rather than silently returning "".
	if _, err := codeFromPastedRedirect("http://127.0.0.1:1/callback?state=st", "st"); err == nil {
		t.Fatal("a URL with no code must be refused")
	}
}

// TestManualPasteHonoursCancellation: the paste wait must not outlive ctx.
func TestManualPasteHonoursCancellation(t *testing.T) {
	ts := authSource(t, "google", &fakeTransport{
		respond: func(string, int) (int, string) { return 200, "{}" }})
	ctx, cancel := context.WithCancel(context.Background())
	ts.SetManualCode(func(c context.Context) (string, error) {
		<-c.Done()
		return "", c.Err()
	})
	cancel()
	if _, err := ts.LoopbackAuthorize(ctx, nil, func(string) {}); err == nil {
		t.Fatal("a cancelled authorisation must return an error")
	}
}

// --- shared administrative token sources ----------------------------------

// TestRegistrySharesApplicationToken: a Microsoft application profile mints
// ONE tenant-level token for every mailbox, so hundreds of mailboxes must
// not produce hundreds of token requests.
func TestRegistrySharesApplicationToken(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	tr := &fakeTransport{respond: func(form string, n int) (int, string) {
		mu.Lock()
		requests++
		mu.Unlock()
		if !strings.Contains(form, "grant_type=client_credentials") {
			t.Errorf("application profiles must use client credentials: %q", form)
		}
		b, _ := json.Marshal(tokenResponse{AccessToken: "app-token", ExpiresIn: 3600})
		return 200, string(b)
	}}

	t.Setenv("MF_TEST_APP_SECRET", "s3cret")
	prof := Profile{Name: "tenant-bulk", ProviderName: "microsoft", Kind: KindApplication,
		Tenant: "example.com", ClientID: "cid", ClientSecret: "env:MF_TEST_APP_SECRET"}

	reg := NewRegistry()
	// Prime the shared source and give it the fake transport before any
	// worker uses it, so the concurrency under test is token minting.
	primed, err := reg.source(prof, "seed@example.com")
	if err != nil {
		t.Fatal(err)
	}
	primed.SetHTTPClient(httpClientWith(tr))

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			user := "user" + string(rune('a'+i%26)) + "@example.com"
			src, serr := reg.source(prof, user)
			if serr != nil {
				t.Error(serr)
				return
			}
			if _, terr := src.Token(context.Background()); terr != nil {
				t.Error(terr)
			}
		}(i)
	}
	wg.Wait()

	if reg.Sources() != 1 {
		t.Fatalf("an application profile must share ONE token source, got %d", reg.Sources())
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("50 mailboxes caused %d token requests; the app token must be minted once", requests)
	}
}

// TestRegistryKeepsDelegatedTokensSeparate: a delegated token IS the user,
// so sources must never be shared between mailboxes.
func TestRegistryKeepsDelegatedTokensSeparate(t *testing.T) {
	t.Setenv("MF_TEST_REFRESH", "rt")
	prof := Profile{Name: "user-prof", ProviderName: "google", ClientID: "cid",
		RefreshToken: "env:MF_TEST_REFRESH"}
	reg := NewRegistry()
	for _, u := range []string{"a@example.com", "b@example.com", "a@example.com"} {
		if _, err := reg.source(prof, u); err != nil {
			t.Fatal(err)
		}
	}
	if reg.Sources() != 2 {
		t.Fatalf("expected one source per distinct user, got %d", reg.Sources())
	}
}

// TestSASLAuthForBindsTheMailbox: a shared administrative source still
// authenticates the SPECIFIC mailbox on each connection.
func TestSASLAuthForBindsTheMailbox(t *testing.T) {
	t.Setenv("MF_TEST_APP_SECRET2", "s")
	prof := Profile{Name: "bulk", ProviderName: "microsoft", Kind: KindApplication,
		Tenant: "t", ClientID: "c", ClientSecret: "env:MF_TEST_APP_SECRET2"}
	ts, err := NewTokenSource(prof, "first@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if a := ts.SASLAuthFor("second@example.com"); a.User != "second@example.com" {
		t.Fatalf("authenticator bound to %q, want second@example.com", a.User)
	}
	if a := ts.SASLAuthFor(""); a.User != "first@example.com" {
		t.Fatalf("an empty login should fall back to the source's user, got %q", a.User)
	}
}

// --- secret storage coherence ---------------------------------------------

// TestSecretRefSchemesAreCoherent: every scheme the credential store can
// WRITE must also be a scheme MailFerry recognises when READING. A stored
// reference a later run cannot parse is the defect this guards.
func TestSecretRefSchemesAreCoherent(t *testing.T) {
	for _, ref := range []string{"env:X", "file:/tmp/x", "keychain:mailferry/a",
		"dpapi:/tmp/x.dpapi", "cmd:echo hi"} {
		if err := ValidateRef("field", ref); err != nil {
			t.Fatalf("%q must be an accepted reference: %v", ref, err)
		}
		if !looksLikeRef(ref) {
			t.Fatalf("%q not recognised as a reference", ref)
		}
	}
	// A literal secret is still rejected outright.
	if err := ValidateRef("refresh_token", "0.AXkA-literal-token"); err == nil {
		t.Fatal("a literal secret must never be accepted in configuration")
	}
}

// TestKeychainBackendMatchesResolver: the store only chooses the keychain
// backend on platforms whose resolver can read it back.
func TestKeychainBackendMatchesResolver(t *testing.T) {
	cs := NewCredStore(t.TempDir())
	backend := cs.Preferred()
	switch runtime.GOOS {
	case "darwin", "linux":
		if backend == BackendDPAPI {
			t.Fatal("DPAPI is a Windows facility and must not be selected here")
		}
	case "windows":
		if backend == BackendKeychain {
			t.Fatal("Windows has no keychain: reference resolver")
		}
	}
	if keychainAvailable() && backend != BackendKeychain {
		t.Fatalf("a reachable keychain should be preferred, got %q", backend)
	}
	if !keychainAvailable() && !dpapiAvailable() && backend != BackendFile {
		t.Fatalf("with no OS secret service the file backend must be used, got %q", backend)
	}
}

// TestDPAPIReferenceFailsHonestlyOffWindows: a config copied from Windows
// must produce an accurate explanation, not "unrecognised reference".
func TestDPAPIReferenceFailsHonestlyOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this asserts the non-Windows behaviour")
	}
	p := t.TempDir() + "/x.dpapi"
	if err := os.WriteFile(p, []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := SecretRef("dpapi:" + p).Resolve()
	if err == nil || !strings.Contains(err.Error(), "Windows") {
		t.Fatalf("expected an explicit Windows-only explanation, got %v", err)
	}
}

// TestCredStoreDeleteIsIdempotent: `auth remove` must always be able to
// finish, whether or not a credential is actually there.
func TestCredStoreDeleteIsIdempotent(t *testing.T) {
	cs := NewCredStore(t.TempDir()).UseFileOnly()
	if err := cs.Delete("never-existed"); err != nil {
		t.Fatalf("deleting a missing credential must not fail: %v", err)
	}
	if _, _, err := cs.Store("p", "v"); err != nil {
		t.Fatal(err)
	}
	if err := cs.Delete("p"); err != nil {
		t.Fatal(err)
	}
	if err := cs.Delete("p"); err != nil {
		t.Fatalf("a second delete must be a no-op: %v", err)
	}
}

// TestTokenErrorsNeverLeakSecrets: an actionable message must never carry a
// token, a client secret or a refresh token.
func TestTokenErrorsNeverLeakSecrets(t *testing.T) {
	tr := &fakeTransport{respond: func(string, int) (int, string) {
		return 400, `{"error":"invalid_grant","error_description":"AADSTS70008: expired"}`
	}}
	t.Setenv("MF_TEST_RT", "SUPER-SECRET-REFRESH-TOKEN")
	ts, err := NewTokenSource(Profile{Name: "p", ProviderName: "microsoft",
		ClientID: "cid", RefreshToken: "env:MF_TEST_RT"}, "u@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ts.SetHTTPClient(httpClientWith(tr))
	_, err = ts.Token(context.Background())
	if err == nil {
		t.Fatal("expected the rejection to surface")
	}
	if strings.Contains(err.Error(), "SUPER-SECRET-REFRESH-TOKEN") {
		t.Fatalf("the error leaked the refresh token: %v", err)
	}
	if !strings.Contains(err.Error(), "re-authorise") {
		t.Fatalf("an expired grant must tell the operator what to do: %v", err)
	}
}
