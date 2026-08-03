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

// Interactive authorisation flows for `mailferry auth add` (delegated
// profiles). Two officially-supported paths, chosen per provider and per
// environment:
//
//   - Loopback authorization-code + PKCE (RFC 8252): MailFerry starts a
//     127.0.0.1 listener, opens the provider's official authorize endpoint in
//     the browser, receives the code on the loopback redirect, and exchanges
//     it for a refresh token. Both Microsoft and Google support this for
//     native/installed clients (Google's OOB flow is deprecated; loopback is
//     the current recommendation).
//
//   - Device authorisation (RFC 8628): for headless/SSH hosts. Microsoft
//     supports device code for public clients. Google's device flow does NOT
//     cover the full mail scope, so for headless Google MailFerry directs the
//     operator to authorise on another machine (loopback) or use a
//     service_account — it never emulates a flow the provider does not offer.
//
//   - Manual redirect capture: still the SAME officially-supported loopback
//     authorization-code + PKCE flow, but the authorisation code travels back
//     by copy-and-paste instead of by a local socket. The operator opens the
//     printed URL in a browser on their OWN machine; the provider redirects to
//     the loopback address, which of course cannot be reached from there, and
//     the operator pastes that failed redirect URL back into MailFerry. This
//     is how a remote migration server with no graphical browser and no port
//     forwarding authorises a Google account — nothing is emulated and no
//     token is ever copied by hand.
//
// Everything here is injectable (HTTP client, browser opener, listener) so
// the orchestration is unit-tested without a real browser, real credentials
// or a live provider.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// decodeJSON reads a JSON response body into v.
func decodeJSON(resp *http.Response, v any) error {
	return json.NewDecoder(resp.Body).Decode(v)
}

// BrowserOpener opens a URL in the operator's browser. Overridable in tests
// and replaced with a no-op in --no-browser mode.
type BrowserOpener func(url string) error

// NewAuthSource builds a token source for running an INTERACTIVE
// authorisation (auth add), where no credentials exist yet — it validates
// only the provider and client, not the (absent) refresh token. The tests
// and the auth command inject the HTTP client and browser opener.
func NewAuthSource(p Profile) (*TokenSource, error) {
	prov, err := p.Provider()
	if err != nil {
		return nil, err
	}
	if p.ClientID == "" {
		return nil, fmt.Errorf("a client_id is required to authorise an OAuth profile")
	}
	return &TokenSource{
		prof:  p,
		prov:  prov,
		http:  &http.Client{Timeout: 30 * time.Second},
		early: 60 * time.Second,
	}, nil
}

// SetHTTPClient overrides the HTTP client (tests / custom transports).
func (ts *TokenSource) SetHTTPClient(c *http.Client) { ts.http = c }

// SetManualCode enables the manual redirect-capture path alongside the
// loopback listener: fn blocks reading the redirect URL (or bare code) the
// operator pastes, and whichever arrives first — the local redirect or the
// paste — completes the authorisation. fn should honour ctx.
func (ts *TokenSource) SetManualCode(fn func(context.Context) (string, error)) {
	ts.manualCode = fn
}

// Provider exposes the resolved provider preset (endpoints, IMAP host).
func (ts *TokenSource) ResolvedProvider() Provider { return ts.prov }

// AuthResult is the outcome of an interactive authorisation: the refresh
// token to persist (via the credential store) and, when the provider returns
// one, an initial access token. Neither is ever logged. Account is the
// signed-in identity for display and for labelling the profile (see
// account.go) — it is not a credential.
type AuthResult struct {
	RefreshToken string
	AccessToken  string
	Account      string
}

// pkce returns a code verifier and its S256 challenge.
func pkce() (verifier, challenge string) {
	b := make([]byte, 32)
	rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return
}

// randState returns an anti-forgery state value.
func randState() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// LoopbackAuthorize runs the loopback authorization-code + PKCE flow. It
// binds a 127.0.0.1 listener, invokes openBrowser with the authorize URL,
// waits for the redirect (or ctx cancellation / timeout), then exchanges the
// code for tokens. openBrowser may be nil (the URL is only printed by the
// caller in --no-browser loopback mode).
func (ts *TokenSource) LoopbackAuthorize(ctx context.Context, openBrowser BrowserOpener,
	printURL func(string)) (AuthResult, error) {
	if ts.prov.AuthURL == "" {
		return AuthResult{}, fmt.Errorf("provider %q does not support interactive authorisation", ts.prov.Name)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return AuthResult{}, fmt.Errorf("could not start the loopback listener: %w", err)
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	verifier, challenge := pkce()
	state := randState()
	authURL := ts.authorizeURL(redirect, challenge, state)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv := &http.Server{Handler: loopbackHandler(state, codeCh, errCh)}
	go srv.Serve(ln)
	defer srv.Close()

	if openBrowser != nil {
		if berr := openBrowser(authURL); berr != nil && printURL != nil {
			printURL(authURL) // browser open failed — degrade to a copyable URL
		}
	} else if printURL != nil {
		printURL(authURL)
	}

	// Manual redirect capture runs CONCURRENTLY with the listener: on a
	// headless host the listener will never fire, and on a desktop the paste
	// prompt is simply never used. Whichever completes first wins.
	manualCh := make(chan string, 1)
	if ts.manualCode != nil {
		go func() {
			v, merr := ts.manualCode(ctx)
			switch {
			case merr != nil:
				select {
				case errCh <- merr:
				default:
				}
			case strings.TrimSpace(v) != "":
				select {
				case manualCh <- v:
				default:
				}
			}
		}()
	}

	select {
	case <-ctx.Done():
		return AuthResult{}, ctx.Err()
	case err := <-errCh:
		return AuthResult{}, err
	case code := <-codeCh:
		return ts.exchangeCode(ctx, code, verifier, redirect)
	case pasted := <-manualCh:
		code, perr := codeFromPastedRedirect(pasted, state)
		if perr != nil {
			return AuthResult{}, perr
		}
		return ts.exchangeCode(ctx, code, verifier, redirect)
	}
}

// codeFromPastedRedirect extracts the authorisation code from what the
// operator pasted: the whole failed redirect URL (preferred — the state is
// then verified) or a bare code. An explicit provider error in the URL is
// surfaced as such rather than as a parse failure.
func codeFromPastedRedirect(pasted, wantState string) (string, error) {
	pasted = strings.TrimSpace(pasted)
	if !strings.Contains(pasted, "?") && !strings.Contains(pasted, "code=") {
		return pasted, nil // a bare authorisation code
	}
	raw := pasted
	if i := strings.Index(raw, "?"); i >= 0 {
		raw = raw[i+1:]
	}
	q, err := url.ParseQuery(raw)
	if err != nil {
		return "", fmt.Errorf("that does not look like the redirect URL — paste the whole "+
			"address the browser was sent to (it contains code=…): %w", err)
	}
	if e := q.Get("error"); e != "" {
		return "", fmt.Errorf("authorisation was denied (%s)", e)
	}
	if st := q.Get("state"); st != "" && st != wantState {
		return "", errors.New("authorisation state mismatch (possible CSRF) — aborted")
	}
	code := q.Get("code")
	if code == "" {
		return "", errors.New("the pasted URL contains no authorisation code")
	}
	return code, nil
}

// SetLoginHint names the account the operator intends to authorise. Both
// Microsoft and Google accept the standard OIDC `login_hint` parameter and
// pre-select that identity at the sign-in page.
//
// This exists because a browser already signed in as somebody else — a
// tenant administrator who has just granted consent, most typically — will
// otherwise be reused by single sign-on, and the operator ends up with a
// delegated token for the WRONG mailbox. The hint makes the intent explicit;
// AuthorisedAs (below) then makes it verifiable.
func (ts *TokenSource) SetLoginHint(user string) { ts.loginHint = strings.TrimSpace(user) }

// authorizeURL builds the provider authorize URL with PKCE + offline access.
func (ts *TokenSource) authorizeURL(redirect, challenge, state string) string {
	q := url.Values{}
	q.Set("client_id", ts.prof.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirect)
	q.Set("scope", ts.authScope())
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	if ts.loginHint != "" {
		q.Set("login_hint", ts.loginHint)
	}
	switch ts.prov.Name {
	case "google":
		q.Set("access_type", "offline") // Google: required to receive a refresh token
		q.Set("prompt", "consent")
	case "microsoft":
		// select_account forces the account picker, which DEFEATS a
		// login_hint — so ask for the picker only when no specific account
		// was named. Without a hint the picker is still the right default:
		// it stops a stale SSO session silently choosing for the operator.
		if ts.loginHint == "" {
			q.Set("prompt", "select_account")
		}
	}
	return ts.prov.AuthURL + "?" + q.Encode()
}

// AuthorisedAs reports whether res identifies the account the operator asked
// for. It returns ok=false ONLY when the provider told MailFerry which
// account signed in and it is a different one — an unknown account (no
// id_token claim) cannot be contradicted, so it is not treated as a mismatch.
func (ts *TokenSource) AuthorisedAs(res AuthResult) (ok bool, want, got string) {
	want, got = ts.loginHint, res.Account
	if want == "" || got == "" {
		return true, want, got
	}
	return strings.EqualFold(want, got), want, got
}

// authScope is the interactive scope: the mail scope, offline access
// (Microsoft needs offline_access explicitly; Google uses access_type), plus
// the identity-only OIDC scopes so MailFerry can report WHICH account was
// authorised. The OIDC scopes grant no mailbox access.
func (ts *TokenSource) authScope() string {
	scopes := ts.prof.Scopes
	if len(scopes) == 0 {
		scopes = ts.prov.Scopes
	}
	out := append([]string{}, scopes...)
	want := []string{}
	if ts.prov.Name == "microsoft" {
		want = append(want, "offline_access")
	}
	if ts.prov.Name == "microsoft" || ts.prov.Name == "google" {
		want = append(want, oidcScopes...)
	}
	for _, w := range want {
		if !hasScope(out, w) {
			out = append(out, w)
		}
	}
	return strings.Join(out, " ")
}

func hasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

// loopbackHandler validates state and captures the code from the redirect.
func loopbackHandler(state string, codeCh chan<- string, errCh chan<- error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			writeResult(w, "Authorisation failed: "+e+". You can close this tab.")
			errCh <- fmt.Errorf("authorisation was denied (%s)", e)
			return
		}
		if q.Get("state") != state {
			writeResult(w, "Authorisation state mismatch. You can close this tab.")
			errCh <- errors.New("authorisation state mismatch (possible CSRF) — aborted")
			return
		}
		code := q.Get("code")
		if code == "" {
			writeResult(w, "No authorisation code received. You can close this tab.")
			errCh <- errors.New("no authorisation code was returned")
			return
		}
		writeResult(w, "MailFerry authorisation complete. You can close this tab and return to the terminal.")
		codeCh <- code
	}
}

func writeResult(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>MailFerry</title>"+
		"<body style='font-family:system-ui;padding:3rem;max-width:32rem'>"+
		"<h2>MailFerry</h2><p>%s</p></body>", msg)
}

// exchangeCode swaps an authorisation code for tokens (PKCE verifier).
func (ts *TokenSource) exchangeCode(ctx context.Context, code, verifier, redirect string) (AuthResult, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("client_id", ts.prof.ClientID)
	form.Set("code_verifier", verifier)
	if secret, serr := ts.prof.ClientSecret.Resolve(); serr == nil && secret != "" {
		form.Set("client_secret", secret)
	}
	tr, err := ts.exchange(ctx, form)
	if err != nil {
		return AuthResult{}, err
	}
	if tr.RefreshToken == "" {
		return AuthResult{}, fmt.Errorf("the provider returned no refresh token — " +
			"ensure offline access / consent was granted so long migrations can refresh")
	}
	return AuthResult{RefreshToken: tr.RefreshToken, AccessToken: tr.AccessToken,
		Account: accountFromIDToken(tr.IDToken)}, nil
}

// --- device authorisation (RFC 8628, Microsoft) --------------------------

// DeviceAuth is the provider's device-code response (the parts we present).
type DeviceAuth struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	// Microsoft returns verification_uri; some providers use a _complete form.
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	Message                 string `json:"message"`
}

// StartDeviceAuth requests a device code. Supported for Microsoft public
// clients; returns an actionable error for providers without a device flow
// for the mail scope (e.g. Google).
func (ts *TokenSource) StartDeviceAuth(ctx context.Context) (DeviceAuth, error) {
	if ts.prov.DeviceURL == "" || ts.prov.Name != "microsoft" {
		return DeviceAuth{}, fmt.Errorf("provider %q does not offer device-code authorisation for IMAP — "+
			"authorise on another machine (loopback) or use a service account for headless bulk migration",
			ts.prov.Name)
	}
	form := url.Values{}
	form.Set("client_id", ts.prof.ClientID)
	form.Set("scope", ts.authScope())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.prov.DeviceURL,
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := ts.http.Do(req)
	if err != nil {
		return DeviceAuth{}, fmt.Errorf("device authorisation unreachable: %w", err)
	}
	defer resp.Body.Close()
	var da DeviceAuth
	if err := decodeJSON(resp, &da); err != nil {
		return DeviceAuth{}, fmt.Errorf("device authorisation returned an unreadable response")
	}
	if da.DeviceCode == "" {
		return DeviceAuth{}, fmt.Errorf("device authorisation returned no device code")
	}
	return da, nil
}

// PollDeviceAuth polls the token endpoint until the operator completes
// authorisation on another device, the code expires, or ctx is cancelled.
func (ts *TokenSource) PollDeviceAuth(ctx context.Context, da DeviceAuth) (AuthResult, error) {
	interval := time.Duration(da.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if ts.pollOverride > 0 {
		interval = ts.pollOverride // tests use a tiny interval
	}
	deadline := time.Now().Add(time.Duration(maxInt(da.ExpiresIn, 300)) * time.Second)
	for {
		select {
		case <-ctx.Done():
			return AuthResult{}, ctx.Err()
		case <-time.After(interval):
		}
		if time.Now().After(deadline) {
			return AuthResult{}, fmt.Errorf("device authorisation expired before it was completed")
		}
		form := url.Values{}
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
		form.Set("client_id", ts.prof.ClientID)
		form.Set("device_code", da.DeviceCode)
		tr, err := ts.exchangeDevice(ctx, form)
		if err != nil {
			return AuthResult{}, err
		}
		switch tr.Error {
		case "":
			if tr.RefreshToken == "" {
				return AuthResult{}, fmt.Errorf("device authorisation returned no refresh token")
			}
			return AuthResult{RefreshToken: tr.RefreshToken, AccessToken: tr.AccessToken,
				Account: accountFromIDToken(tr.IDToken)}, nil
		case "authorization_pending", "slow_down":
			if tr.Error == "slow_down" {
				interval += 5 * time.Second
			}
			continue
		case "authorization_declined", "expired_token", "access_denied":
			return AuthResult{}, fmt.Errorf("device authorisation was not completed (%s)", tr.Error)
		default:
			return AuthResult{}, classifyTokenError(ts.prof.Name, tr.Error, tr.ErrorDescription)
		}
	}
}

// exchangeDevice posts a device-token poll and returns the raw response
// (including pending/slow_down states, which are not hard errors).
func (ts *TokenSource) exchangeDevice(ctx context.Context, form url.Values) (tokenResponse, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.prov.TokenURL,
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := ts.http.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("device token poll unreachable: %w", err)
	}
	defer resp.Body.Close()
	var tr tokenResponse
	_ = decodeJSON(resp, &tr)
	return tr, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
