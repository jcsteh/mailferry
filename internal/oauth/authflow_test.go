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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoopbackAuthorizeSuccess drives the full loopback flow with a fake
// browser that fetches the redirect (as a real browser would) and a fake
// token endpoint — no real browser, credentials or provider.
func TestLoopbackAuthorizeSuccess(t *testing.T) {
	tr := &fakeTransport{respond: func(form string, n int) (int, string) {
		if !strings.Contains(form, "grant_type=authorization_code") {
			t.Errorf("expected authorization_code exchange, got %q", form)
		}
		if !strings.Contains(form, "code_verifier=") {
			t.Errorf("PKCE verifier missing from exchange: %q", form)
		}
		b, _ := json.Marshal(tokenResponse{AccessToken: "acc", RefreshToken: "the-refresh", ExpiresIn: 3600})
		return 200, string(b)
	}}
	ts := authSource(t, "google", tr)

	// The "browser" completes the redirect with a valid code+state.
	opener := func(authURL string) error {
		u, _ := url.Parse(authURL)
		state := u.Query().Get("state")
		redirect := u.Query().Get("redirect_uri")
		go func() {
			http.Get(redirect + "?code=abc123&state=" + state)
		}()
		return nil
	}
	res, err := ts.LoopbackAuthorize(context.Background(), opener, nil)
	if err != nil {
		t.Fatalf("loopback authorize: %v", err)
	}
	if res.RefreshToken != "the-refresh" {
		t.Fatalf("refresh token not captured: %+v", res)
	}
}

// TestLoopbackBrowserOpenFailsFallsBackToURL: when the browser cannot open,
// the flow prints the URL and still completes via manual navigation.
func TestLoopbackBrowserOpenFailsFallsBackToURL(t *testing.T) {
	tr := &fakeTransport{respond: func(form string, n int) (int, string) {
		b, _ := json.Marshal(tokenResponse{AccessToken: "a", RefreshToken: "r", ExpiresIn: 3600})
		return 200, string(b)
	}}
	ts := authSource(t, "microsoft", tr)

	var printed string
	// Browser open fails; the printed URL is used to complete the redirect.
	failOpen := func(string) error { return errFakeNoBrowser }
	printURL := func(u string) {
		printed = u
		pu, _ := url.Parse(u)
		go http.Get(pu.Query().Get("redirect_uri") + "?code=z&state=" + pu.Query().Get("state"))
	}
	res, err := ts.LoopbackAuthorize(context.Background(), failOpen, printURL)
	if err != nil {
		t.Fatalf("fallback authorize: %v", err)
	}
	if printed == "" || !strings.Contains(printed, "code_challenge") {
		t.Fatalf("expected a copyable authorize URL on browser-open failure, got %q", printed)
	}
	if res.RefreshToken != "r" {
		t.Fatal("refresh token not captured on fallback path")
	}
}

// TestLoopbackStateMismatchRejected: a redirect with the wrong state is a
// possible CSRF and must be rejected.
func TestLoopbackStateMismatchRejected(t *testing.T) {
	tr := &fakeTransport{respond: func(string, int) (int, string) { return 200, "{}" }}
	ts := authSource(t, "google", tr)
	opener := func(authURL string) error {
		u, _ := url.Parse(authURL)
		go http.Get(u.Query().Get("redirect_uri") + "?code=abc&state=WRONG")
		return nil
	}
	_, err := ts.LoopbackAuthorize(context.Background(), opener, nil)
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("state mismatch must be rejected, got %v", err)
	}
}

// TestLoopbackCancellation: ctx cancellation ends the flow cleanly.
func TestLoopbackCancellation(t *testing.T) {
	tr := &fakeTransport{respond: func(string, int) (int, string) { return 200, "{}" }}
	ts := authSource(t, "google", tr)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	_, err := ts.LoopbackAuthorize(ctx, func(string) error { return nil }, nil)
	if err == nil {
		t.Fatal("cancellation should end the flow with an error")
	}
}

// TestDeviceAuthMicrosoft: the device flow starts and polls to completion.
func TestDeviceAuthMicrosoft(t *testing.T) {
	poll := 0
	tr := &fakeTransport{respond: func(form string, n int) (int, string) {
		if strings.Contains(form, "device_code=") { // token poll
			poll++
			if poll < 2 {
				return 400, `{"error":"authorization_pending"}`
			}
			b, _ := json.Marshal(tokenResponse{AccessToken: "a", RefreshToken: "dev-refresh", ExpiresIn: 3600})
			return 200, string(b)
		}
		// device-code request
		da := DeviceAuth{DeviceCode: "DEV", UserCode: "ABCD-EFGH",
			VerificationURI: "https://microsoft.com/devicelogin", Interval: 0, ExpiresIn: 300}
		b, _ := json.Marshal(da)
		return 200, string(b)
	}}
	ts := authSource(t, "microsoft", tr)
	ts.pollOverride = 5 * time.Millisecond // keep the test fast

	da, err := ts.StartDeviceAuth(context.Background())
	if err != nil {
		t.Fatalf("start device auth: %v", err)
	}
	if da.UserCode != "ABCD-EFGH" {
		t.Fatalf("user code = %q", da.UserCode)
	}
	res, err := ts.PollDeviceAuth(context.Background(), da)
	if err != nil {
		t.Fatalf("poll device auth: %v", err)
	}
	if res.RefreshToken != "dev-refresh" {
		t.Fatalf("device refresh token not captured: %+v", res)
	}
}

// TestDeviceAuthGoogleUnsupported: MailFerry must NOT emulate a device flow
// for Google (it is not supported for the mail scope).
func TestDeviceAuthGoogleUnsupported(t *testing.T) {
	tr := &fakeTransport{respond: func(string, int) (int, string) { return 200, "{}" }}
	ts := authSource(t, "google", tr)
	_, err := ts.StartDeviceAuth(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not offer device-code") {
		t.Fatalf("google device auth must be refused with guidance, got %v", err)
	}
}

// --- credential store -----------------------------------------------------

func TestCredStoreFileFallbackRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cs := NewCredStore(dir).UseFileOnly()
	ref, backend, err := cs.Store("client-a", "super-secret-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "file:") {
		t.Fatalf("file-only store should return a file: ref, got %q", ref)
	}
	if backend != BackendFile {
		t.Fatalf("file-only store should report the file backend: %q", backend)
	}
	// The reference resolves back to the secret.
	got, err := SecretRef(ref).Resolve()
	if err != nil || got != "super-secret-refresh" {
		t.Fatalf("resolve stored secret: got %q err %v", got, err)
	}
	// 0600 permissions.
	p := strings.TrimPrefix(ref, "file:")
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("stored secret perms = %v, want 0600", st.Mode().Perm())
	}
	// Delete removes it.
	if err := cs.Delete("client-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("delete should remove the secret file")
	}
	_ = filepath.Base(p)
}

// helpers

var errFakeNoBrowser = fakeErr("no browser")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

func authSource(t *testing.T, provider string, tr *fakeTransport) *TokenSource {
	t.Helper()
	ts, err := NewAuthSource(Profile{Name: "p", ProviderName: provider, ClientID: "cid"})
	if err != nil {
		t.Fatalf("NewAuthSource: %v", err)
	}
	ts.SetHTTPClient(httpClientWith(tr))
	return ts
}
