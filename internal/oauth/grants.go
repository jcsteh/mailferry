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

// Bulk / administrative grant types for many-mailbox migrations:
//
//   - Microsoft 365 client-credentials (application permissions): one tenant
//     app with IMAP.AccessAsApp and admin consent authenticates many mailboxes.
//     The token is app-level; the target mailbox is the SASL login.
//     Verified: token endpoint /oauth2/v2.0/token, scope
//     https://outlook.office365.com/.default.
//
//   - Google Workspace domain-wide delegation (service account): a signed JWT
//     with the service account key, impersonating the target user (sub),
//     exchanged for an access token. Verified: scope https://mail.google.com/
//     (this scope is only usable via domain-wide delegation), token endpoint
//     https://oauth2.googleapis.com/token, RS256-signed assertion.
//
// Both are testable without a network: the JWT is built deterministically and
// the token exchange goes through the injectable http client.
package oauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// clientCredentials performs the Microsoft application (app-only) grant.
// Caller holds ts.mu. The access token authenticates any mailbox the tenant
// has authorised for the app; the specific mailbox is selected by the SASL
// login (ts.user), not by the token.
func (ts *TokenSource) clientCredentials(ctx context.Context) (string, error) {
	secret, err := ts.prof.ClientSecret.Resolve()
	if err != nil || secret == "" {
		return "", fmt.Errorf("oauth profile %q (application): client secret unavailable", ts.prof.Name)
	}
	// App-only uses the resource .default scope — NOT the delegated
	// per-user scopes — unless the profile explicitly overrides it.
	scope := strings.Join(ts.prof.Scopes, " ")
	if scope == "" {
		scope = "https://outlook.office365.com/.default"
	}
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", ts.prof.ClientID)
	form.Set("client_secret", secret)
	form.Set("scope", scope)
	tr, err := ts.exchange(ctx, form)
	if err != nil {
		return "", err
	}
	return ts.store(tr)
}

// serviceAccountKey is the subset of a Google service account JSON key we use.
type serviceAccountKey struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
	Type        string `json:"type"`
}

// jwtBearer performs the Google domain-wide-delegation grant: a JWT assertion
// signed with the service account key, impersonating ts.user, exchanged for
// an access token. Caller holds ts.mu.
func (ts *TokenSource) jwtBearer(ctx context.Context) (string, error) {
	raw, err := ts.prof.KeyJSON.Resolve()
	if err != nil {
		return "", fmt.Errorf("oauth profile %q (service_account): key unavailable", ts.prof.Name)
	}
	var key serviceAccountKey
	if err := json.Unmarshal([]byte(raw), &key); err != nil {
		return "", fmt.Errorf("oauth profile %q: service account key is not valid JSON", ts.prof.Name)
	}
	if key.ClientEmail == "" || key.PrivateKey == "" {
		return "", fmt.Errorf("oauth profile %q: service account key is missing client_email/private_key", ts.prof.Name)
	}
	tokenURI := key.TokenURI
	if tokenURI == "" {
		tokenURI = ts.prov.TokenURL
	}
	scope := ts.scopeStr()
	if scope == "" {
		scope = "https://mail.google.com/"
	}

	assertion, err := signServiceAccountJWT(key, ts.user, scope, tokenURI, time.Now())
	if err != nil {
		return "", fmt.Errorf("oauth profile %q: could not sign the impersonation assertion: %w", ts.prof.Name, err)
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)
	tr, err := ts.exchange(ctx, form)
	if err != nil {
		return "", err
	}
	return ts.store(tr)
}

// signServiceAccountJWT builds and RS256-signs the domain-wide-delegation
// assertion: iss = service account, sub = impersonated user, scope, aud =
// token endpoint. Exported-ish for tests via the exported helper below.
func signServiceAccountJWT(key serviceAccountKey, subject, scope, aud string, now time.Time) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iss":   key.ClientEmail,
		"sub":   subject, // impersonated user (domain-wide delegation)
		"scope": scope,
		"aud":   aud,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signingInput := b64url(hb) + "." + b64url(cb)

	priv, err := parseRSAPrivateKey(key.PrivateKey)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + b64url(sig), nil
}

// parseRSAPrivateKey parses a PEM PKCS#1 or PKCS#8 RSA private key.
func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("service account private key is not valid PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("service account private key is not a supported RSA key")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("service account private key is not an RSA key")
	}
	return rk, nil
}

func b64url(b []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(b), "=")
}
