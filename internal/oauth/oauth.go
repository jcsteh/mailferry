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

// Package oauth is MailFerry's provider-aware OAuth 2.0 layer. It turns a
// declarative auth Profile (provider + client + external secret references)
// into a refreshing bearer-token source that the native IMAP client
// consumes through a provider-agnostic SASLAuth. All Microsoft/Google
// specifics live HERE — the migration engine and the IMAP protocol code
// never branch on the provider.
//
// Verified against official provider documentation (July 2026):
//   - Microsoft 365 / Exchange Online: SASL XOAUTH2, IMAP host
//     outlook.office365.com:993, delegated scope
//     https://outlook.office.com/IMAP.AccessAsUser.All plus offline_access,
//     token endpoint https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token,
//     device-code grant supported for headless/native clients.
//   - Google Workspace / Gmail: SASL XOAUTH2, IMAP host imap.gmail.com:993,
//     scope https://mail.google.com/, token endpoint
//     https://oauth2.googleapis.com/token.
package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ajsap/mailferry/v2/internal/imapx"
)

// Provider is a set of endpoints and default scopes for an identity
// platform. Built-in presets cover the first-class providers; "generic"
// lets a standards-compliant provider be configured explicitly.
type Provider struct {
	Name      string
	TokenURL  string
	DeviceURL string   // device-authorization endpoint ("" if unsupported)
	AuthURL   string   // authorization endpoint (interactive code flow)
	Scopes    []string // default scopes when the profile does not override
	IMAPHost  string   // documented IMAP host (informational / default)
	IMAPPort  int
	Mechanism string // default SASL mechanism (XOAUTH2 for both first-class)
}

// MicrosoftProvider returns the Microsoft identity platform preset for a
// tenant ("common", "organizations", "consumers", or a tenant GUID/domain).
func MicrosoftProvider(tenant string) Provider {
	if tenant == "" {
		tenant = "common"
	}
	base := "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0"
	return Provider{
		Name:      "microsoft",
		TokenURL:  base + "/token",
		DeviceURL: base + "/devicecode",
		AuthURL:   base + "/authorize",
		Scopes: []string{
			"https://outlook.office.com/IMAP.AccessAsUser.All",
			"offline_access",
		},
		IMAPHost:  "outlook.office365.com",
		IMAPPort:  993,
		Mechanism: imapx.MechXOAUTH2,
	}
}

// GoogleProvider returns the Google OAuth preset (Gmail / Workspace).
func GoogleProvider() Provider {
	return Provider{
		Name:      "google",
		TokenURL:  "https://oauth2.googleapis.com/token",
		DeviceURL: "https://oauth2.googleapis.com/device/code",
		AuthURL:   "https://accounts.google.com/o/oauth2/v2/auth",
		Scopes:    []string{"https://mail.google.com/"},
		IMAPHost:  "imap.gmail.com",
		IMAPPort:  993,
		Mechanism: imapx.MechXOAUTH2,
	}
}

// Kind is the authentication model a profile uses. They are deliberately
// distinct — a delegated user token authenticates ONE mailbox, whereas an
// application/service profile authenticates MANY authorised mailboxes, with
// very different consent and security implications.
type Kind string

const (
	// KindDelegated: interactive user OAuth. A refresh token (or a
	// pre-provisioned access token) authenticates the consenting user's own
	// mailbox. The default.
	KindDelegated Kind = "delegated"
	// KindApplication: Microsoft 365 client-credentials (app-only). One
	// tenant application with the IMAP.AccessAsApp permission and admin
	// consent authenticates MANY authorised mailboxes; the target mailbox is
	// the SASL login. Requires a client secret and tenant.
	KindApplication Kind = "application"
	// KindServiceAccount: Google Workspace service account with domain-wide
	// delegation. One service account, authorised by a Workspace admin,
	// impersonates MANY users; the target mailbox is the SASL login. Requires
	// the service account JSON key.
	KindServiceAccount Kind = "service_account"
)

// Profile is the declarative, secret-free description of one OAuth identity.
// It carries only external secret *references* (see secret.go), never the
// secrets themselves.
type Profile struct {
	Name         string
	ProviderName string // "microsoft" | "google" | "generic"
	Kind         Kind   // "" = delegated (the default)
	Tenant       string // Microsoft tenant ("common" if empty)
	ClientID     string
	Mechanism    string    // "" = provider default (XOAUTH2)
	Scopes       []string  // override the provider defaults
	ClientSecret SecretRef // client secret (application / confidential clients)
	RefreshToken SecretRef // delegated: long-lived; mints access tokens
	AccessToken  SecretRef // delegated: optional pre-provisioned token (no refresh)
	KeyJSON      SecretRef // service_account: the service account JSON key
	// Generic-provider endpoints (ignored for microsoft/google).
	TokenURL string
}

// kind resolves the effective authentication model (delegated by default).
func (p Profile) kind() Kind {
	if p.Kind == "" {
		return KindDelegated
	}
	return p.Kind
}

// Provider resolves the profile's provider preset.
func (p Profile) Provider() (Provider, error) {
	switch strings.ToLower(p.ProviderName) {
	case "microsoft", "ms", "office365", "o365", "azure", "entra":
		return MicrosoftProvider(p.Tenant), nil
	case "google", "gmail", "workspace", "gsuite":
		return GoogleProvider(), nil
	case "generic", "custom", "standard":
		if p.TokenURL == "" {
			return Provider{}, fmt.Errorf("oauth profile %q: generic provider requires token_url", p.Name)
		}
		return Provider{Name: "generic", TokenURL: p.TokenURL, Mechanism: imapx.MechXOAUTH2}, nil
	default:
		return Provider{}, fmt.Errorf("oauth profile %q: unknown provider %q "+
			"(microsoft | google | generic)", p.Name, p.ProviderName)
	}
}

// mechanism resolves the SASL mechanism for the profile.
func (p Profile) mechanism(prov Provider) string {
	if p.Mechanism != "" {
		return strings.ToUpper(p.Mechanism)
	}
	if prov.Mechanism != "" {
		return prov.Mechanism
	}
	return imapx.MechXOAUTH2
}

// TokenSource mints and caches access tokens for one profile, bound to one
// target user, refreshing as they expire. It is safe for concurrent use — a
// single mailbox's source is shared across reconnects so a long migration
// reuses one cached token until it actually needs a new one, and the mutex
// serialises refreshes so concurrent workers never race or corrupt state.
// The grant used depends on the profile Kind: delegated (refresh token or
// static access token), application (client credentials), or service_account
// (signed JWT with domain-wide-delegation impersonation of the target user).
type TokenSource struct {
	prof  Profile
	prov  Provider
	user  string // target mailbox / impersonated user
	http  *http.Client
	early time.Duration // refresh this long before actual expiry

	pollOverride time.Duration // test-only device-poll interval override
	// manualCode, when set, lets the operator paste the redirect URL instead
	// of relying on the loopback listener being reachable (headless hosts).
	manualCode func(context.Context) (string, error)
	// loginHint names the account the operator intends to authorise, so a
	// stale single-sign-on session cannot quietly authorise a different one.
	loginHint string

	mu         sync.Mutex
	token      string
	expires    time.Time
	static     bool   // a pre-provisioned access token with no refresh capability
	curRefresh string // the live refresh token (rotated in memory, never persisted)
}

// NewTokenSource builds a token source from a profile for a target user. It
// resolves secret *references* and validates the profile's requirements here
// (once) so a missing secret or misconfiguration fails fast with an
// actionable error before the migration starts; resolved values live only
// inside the source. For delegated profiles the user is the SASL login; for
// application/service_account profiles it is the mailbox being accessed (and,
// for service accounts, the impersonated identity).
func NewTokenSource(p Profile, user string) (*TokenSource, error) {
	prov, err := p.Provider()
	if err != nil {
		return nil, err
	}
	ts := &TokenSource{
		prof:  p,
		prov:  prov,
		user:  user,
		http:  &http.Client{Timeout: 30 * time.Second},
		early: 60 * time.Second,
	}
	switch p.kind() {
	case KindApplication:
		if secret, serr := p.ClientSecret.Resolve(); serr != nil || secret == "" {
			return nil, fmt.Errorf("oauth profile %q (application): a client_secret reference is required", p.Name)
		}
	case KindServiceAccount:
		if _, jerr := p.KeyJSON.Resolve(); jerr != nil {
			return nil, fmt.Errorf("oauth profile %q (service_account): key_json: %w", p.Name, jerr)
		}
		if user == "" {
			return nil, fmt.Errorf("oauth profile %q (service_account): a target user to impersonate is required", p.Name)
		}
	default: // delegated
		access, aerr := p.AccessToken.Resolve()
		if aerr != nil {
			return nil, fmt.Errorf("oauth profile %q: access token: %w", p.Name, aerr)
		}
		refresh, rerr := p.RefreshToken.Resolve()
		if rerr != nil {
			return nil, fmt.Errorf("oauth profile %q: refresh token: %w", p.Name, rerr)
		}
		if refresh == "" && access == "" {
			return nil, fmt.Errorf("oauth profile %q needs a refresh_token (recommended) "+
				"or a pre-provisioned access_token", p.Name)
		}
		ts.curRefresh = refresh
		if refresh == "" {
			ts.static = true
			ts.token = access
			ts.expires = time.Now().Add(55 * time.Minute)
		}
	}
	return ts, nil
}

// Mechanism reports the SASL mechanism this source authenticates with.
func (ts *TokenSource) Mechanism() string { return ts.prof.mechanism(ts.prov) }

// Expiry reports when the currently cached access token stops being valid,
// or the zero time if none has been minted yet. Diagnostics show it so an
// operator can see the token really is short-lived and really is refreshed —
// it reveals nothing about the token itself.
func (ts *TokenSource) Expiry() time.Time {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.token == "" {
		return time.Time{}
	}
	return ts.expires
}

// Static reports whether this source carries a pre-provisioned access token
// with no refresh capability — worth flagging, because such a profile stops
// working when the token expires and cannot recover by itself.
func (ts *TokenSource) Static() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.static
}

// SASLAuth adapts the source to the provider-agnostic imapx authenticator for
// this source's bound user. The engine attaches the result to each new
// connection; every connect fetches a fresh (auto-refreshed) token.
func (ts *TokenSource) SASLAuth() *imapx.SASLAuth { return ts.SASLAuthFor(ts.user) }

// SASLAuthFor adapts the source for a specific mailbox login. It exists for
// the shared administrative case: a Microsoft application profile mints ONE
// tenant-level token that authorises many mailboxes, so many authenticators —
// one per mailbox — legitimately share a single token source. For delegated
// and service-account profiles the token itself is user-specific, and the
// Registry keys its cache accordingly, so user always matches ts.user there.
func (ts *TokenSource) SASLAuthFor(user string) *imapx.SASLAuth {
	if user == "" {
		user = ts.user
	}
	return &imapx.SASLAuth{
		Mechanism: ts.Mechanism(),
		User:      user,
		TokenFn:   ts.Token,
	}
}

// Token returns a currently-valid access token, refreshing if necessary.
// It never returns the token inside an error.
func (ts *TokenSource) Token(ctx context.Context) (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.token != "" && time.Now().Before(ts.expires.Add(-ts.early)) {
		return ts.token, nil
	}
	if ts.static {
		if time.Now().Before(ts.expires) {
			return ts.token, nil
		}
		return "", fmt.Errorf("oauth profile %q: pre-provisioned access token has expired and "+
			"no refresh_token is configured — supply a refresh token for long migrations", ts.prof.Name)
	}
	switch ts.prof.kind() {
	case KindApplication:
		return ts.clientCredentials(ctx)
	case KindServiceAccount:
		return ts.jwtBearer(ctx)
	default:
		return ts.refresh(ctx)
	}
}

// store records a successful token response's access token and lifetime.
// Caller holds ts.mu.
func (ts *TokenSource) store(tr tokenResponse) (string, error) {
	if tr.AccessToken == "" {
		return "", fmt.Errorf("oauth profile %q: token endpoint returned no access token", ts.prof.Name)
	}
	ts.token = tr.AccessToken
	life := time.Duration(tr.ExpiresIn) * time.Second
	if life <= 0 {
		life = 55 * time.Minute
	}
	ts.expires = time.Now().Add(life)
	if tr.RefreshToken != "" {
		ts.curRefresh = tr.RefreshToken
	}
	return ts.token, nil
}

// exchange posts a token-endpoint form and parses the response. Shared by
// every grant type; never logs the form or the token.
func (ts *TokenSource) exchange(ctx context.Context, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.prov.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := ts.http.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("oauth profile %q: token endpoint unreachable: %w", ts.prof.Name, err)
	}
	defer resp.Body.Close()
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return tokenResponse{}, fmt.Errorf("oauth profile %q: token endpoint returned an unreadable response (HTTP %d)",
			ts.prof.Name, resp.StatusCode)
	}
	if tr.Error != "" || resp.StatusCode >= 400 {
		return tokenResponse{}, classifyTokenError(ts.prof.Name, tr.Error, tr.ErrorDescription)
	}
	return tr, nil
}

// tokenResponse is the subset of the RFC 6749 token response we consume.
// IDToken is the OpenID Connect identity token, used ONLY to label the
// profile with the account that signed in (see account.go).
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	IDToken          string `json:"id_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// scopeStr returns the effective scope string (profile overrides provider).
func (ts *TokenSource) scopeStr() string {
	scopes := ts.prof.Scopes
	if len(scopes) == 0 {
		scopes = ts.prov.Scopes
	}
	return strings.Join(scopes, " ")
}

// refresh performs the delegated refresh-token grant. Caller holds ts.mu.
// A rotated refresh token is kept in memory for this run only — MailFerry
// never persists it; operators renew durable tokens via their secret store.
func (ts *TokenSource) refresh(ctx context.Context) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", ts.curRefresh)
	form.Set("client_id", ts.prof.ClientID)
	if s := ts.scopeStr(); s != "" {
		form.Set("scope", s)
	}
	if secret, serr := ts.prof.ClientSecret.Resolve(); serr == nil && secret != "" {
		form.Set("client_secret", secret)
	}
	tr, err := ts.exchange(ctx, form)
	if err != nil {
		return "", err
	}
	return ts.store(tr)
}

// classifyTokenError turns an OAuth error code into an actionable message
// (never containing any token). Covers the cases operators actually hit.
func classifyTokenError(profile, code, desc string) error {
	var hint string
	switch code {
	case "invalid_grant":
		hint = "the refresh token is expired, revoked or was issued to a different client — re-authorise the account"
	case "invalid_client":
		hint = "the client_id or client_secret is wrong, or the app registration does not permit this flow"
	case "invalid_scope":
		hint = "the requested IMAP scope is not consented — grant the mailbox/IMAP scope and offline access"
	case "consent_required", "interaction_required":
		hint = "administrator or user consent is required for IMAP access"
	case "unauthorized_client":
		hint = "this client is not authorised for the refresh-token grant (check the app registration / tenant policy)"
	default:
		if code == "" {
			code = "unknown_error"
		}
		hint = "check the app registration, scopes and tenant policy"
	}
	return fmt.Errorf("oauth profile %q: token request rejected (%s) — %s", profile, code, hint)
}
