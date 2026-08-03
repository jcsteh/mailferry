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
	"fmt"
	"os"
	"strings"
)

// OAuthProfile is the declarative, SECRET-FREE description of one OAuth
// identity, loaded from an [oauth.<name>] section of mailferry.toml. It
// carries only external secret *references* (env:/file:/keychain:/cmd:),
// never the secrets themselves — MailFerry refuses to store client secrets
// or refresh tokens in plaintext configuration. A CSV row references a
// profile by name via an "oauth:<name>" password cell.
type OAuthProfile struct {
	Name     string
	Provider string // microsoft | google | generic
	// Kind is the authentication model: "delegated" (default, a user's own
	// mailbox via refresh token), "application" (Microsoft app-only, one app
	// → many mailboxes), or "service_account" (Google domain-wide delegation,
	// one service account → many impersonated mailboxes).
	Kind   string
	Tenant string // Microsoft tenant (common if empty)
	// Account is the signed-in identity a delegated authorisation resolved to
	// (from the provider's OIDC id_token), recorded so `auth list`/`auth show`
	// can tell an operator WHICH mailbox a profile speaks for. It is an email
	// address, not a credential, and is never used to make an authorisation
	// decision — only to label the profile.
	Account string
	// AuthorisedAt is when this profile was last successfully authorised
	// (RFC 3339), written by `auth add` / `auth refresh`. Non-secret
	// provenance: it answers "how old is this credential?" without exposing
	// anything about it. `auth test` deliberately does NOT update it —
	// informational commands stay zero-side-effect.
	AuthorisedAt string
	ClientID     string   // public application (client) id — not a secret
	Mechanism    string   // "" = provider default (XOAUTH2)
	Scopes       []string // override provider default scopes
	ClientSecret string   // external secret reference (application clients)
	RefreshToken string   // external secret reference (delegated)
	AccessToken  string   // external secret reference (delegated, no refresh)
	KeyJSON      string   // external secret reference (service_account key)
	TokenURL     string   // generic provider only
}

// secretRefFields are the profile fields that must be external references,
// never literal secrets.
func (p OAuthProfile) secretRefFields() map[string]string {
	return map[string]string{
		"client_secret": p.ClientSecret,
		"refresh_token": p.RefreshToken,
		"access_token":  p.AccessToken,
		"key_json":      p.KeyJSON,
	}
}

// knownKinds are the accepted authentication models.
var knownKinds = map[string]bool{
	"": true, "delegated": true, "application": true, "service_account": true,
}

// knownProviders maps accepted provider aliases to a canonical family.
var knownProviders = map[string]string{
	"microsoft": "microsoft", "ms": "microsoft", "office365": "microsoft",
	"o365": "microsoft", "azure": "microsoft", "entra": "microsoft",
	"google": "google", "gmail": "google", "workspace": "google", "gsuite": "google",
	"generic": "generic", "custom": "generic", "standard": "generic",
}

// acceptedSecretSchemes are the only forms a secret reference may take.
var acceptedSecretSchemes = []string{"env:", "file:", "keychain:", "cmd:"}

// LoadOAuthProfiles reads every [oauth.<name>] section from the config file
// at path. It NEVER creates the file and never resolves secrets — it only
// parses references. A missing file yields no profiles (not an error), so
// password-only deployments are unaffected.
func LoadOAuthProfiles(path string) (map[string]OAuthProfile, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]OAuthProfile{}, nil, nil
		}
		return nil, nil, err
	}
	tree, err := parseTOML(string(data))
	if err != nil {
		return nil, nil, fmt.Errorf("config %s: could not parse (%v)", path, err)
	}
	profiles := map[string]OAuthProfile{}
	var warns []string
	for section, body := range tree {
		if !strings.HasPrefix(section, "oauth.") {
			continue
		}
		name := strings.TrimPrefix(section, "oauth.")
		if name == "" {
			continue
		}
		p := OAuthProfile{Name: name}
		for key, val := range body {
			s, _ := val.(string)
			switch key {
			case "provider":
				p.Provider = s
			case "kind":
				p.Kind = s
			case "tenant":
				p.Tenant = s
			case "account":
				p.Account = s
			case "authorised_at":
				p.AuthorisedAt = s
			case "client_id":
				p.ClientID = s
			case "mechanism":
				p.Mechanism = s
			case "scopes":
				p.Scopes = splitScopes(s)
			case "client_secret":
				p.ClientSecret = s
			case "refresh_token":
				p.RefreshToken = s
			case "access_token":
				p.AccessToken = s
			case "key_json":
				p.KeyJSON = s
			case "token_url":
				p.TokenURL = s
			default:
				warns = append(warns, fmt.Sprintf(
					"oauth profile %q: unknown key %q ignored", name, key))
			}
		}
		profiles[name] = p
	}
	return profiles, warns, nil
}

// splitScopes accepts space- or comma-separated scope lists.
func splitScopes(s string) []string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' })
	if len(f) == 0 {
		return nil
	}
	return f
}

// ValidateProfile checks one profile WITHOUT resolving any secret or
// touching the network: provider is known, required fields are present, and
// every secret field is an external reference (never a literal). Returns a
// combined, actionable error, or nil.
func (p OAuthProfile) Validate() error {
	var errs []string
	fam, ok := knownProviders[strings.ToLower(p.Provider)]
	if p.Provider == "" {
		errs = append(errs, "provider is required (microsoft | google | generic)")
	} else if !ok {
		errs = append(errs, fmt.Sprintf("unknown provider %q (microsoft | google | generic)", p.Provider))
	}
	if fam == "generic" && p.TokenURL == "" {
		errs = append(errs, "generic provider requires token_url")
	}
	kind := p.Kind
	if kind == "" {
		kind = "delegated"
	}
	if !knownKinds[p.Kind] {
		errs = append(errs, fmt.Sprintf("unknown kind %q (delegated | application | service_account)", p.Kind))
	}
	// Credential requirements differ by authentication model.
	switch kind {
	case "application":
		if p.ClientID == "" {
			errs = append(errs, "client_id is required")
		}
		if p.ClientSecret == "" {
			errs = append(errs, "application profiles require a client_secret reference")
		}
		if fam == "microsoft" && p.Tenant == "" {
			errs = append(errs, "application profiles require a tenant")
		}
		if fam == "google" {
			errs = append(errs, "google application (client-credentials) is not supported for IMAP — "+
				"use kind = \"service_account\" (domain-wide delegation) for bulk Google migrations")
		}
	case "service_account":
		if p.KeyJSON == "" {
			errs = append(errs, "service_account profiles require a key_json reference (the service account JSON key)")
		}
		if fam == "microsoft" {
			errs = append(errs, "service_account is a Google Workspace model — "+
				"use kind = \"application\" for bulk Microsoft 365 migrations")
		}
	default: // delegated
		if p.ClientID == "" {
			errs = append(errs, "client_id is required")
		}
		if p.RefreshToken == "" && p.AccessToken == "" {
			errs = append(errs, "a refresh_token reference is required (or a pre-provisioned access_token)")
		}
	}
	for field, ref := range p.secretRefFields() {
		if ref == "" {
			continue
		}
		if !isSecretRef(ref) {
			errs = append(errs, fmt.Sprintf(
				"%s must be an external secret reference (%s) — a literal secret must never "+
					"be written into configuration", field, strings.Join(acceptedSecretSchemes, " ")))
		}
	}
	if p.Mechanism != "" {
		m := strings.ToUpper(p.Mechanism)
		if m != "XOAUTH2" && m != "OAUTHBEARER" {
			errs = append(errs, fmt.Sprintf("mechanism %q is not supported (XOAUTH2 | OAUTHBEARER)", p.Mechanism))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("oauth profile %q:\n  - %s", p.Name, strings.Join(errs, "\n  - "))
}

// isSecretRef reports whether s uses one of the accepted external-secret
// schemes (kept here so config validates references without importing the
// oauth resolver).
func isSecretRef(s string) bool {
	for _, scheme := range acceptedSecretSchemes {
		if strings.HasPrefix(s, scheme) {
			return true
		}
	}
	return false
}

// ValidateOAuth checks that every OAuth reference used by the specs resolves
// to a defined, valid profile. It runs before any connection, alongside the
// whole-file CSV validation, so a misconfigured migration fails fast and
// clearly. Password-only migrations (no oauth: references) pass trivially.
func ValidateOAuth(specs []MailboxSpec, profiles map[string]OAuthProfile) error {
	var errs []string
	used := map[string]bool{}
	for _, s := range specs {
		for _, ref := range []struct{ side, name string }{
			{"source", s.SrcAuth}, {"destination", s.DstAuth},
		} {
			if ref.name == "" {
				continue
			}
			used[ref.name] = true
			if _, ok := profiles[ref.name]; !ok {
				errs = append(errs, fmt.Sprintf(
					"mailbox %d %s references oauth profile %q, which is not defined "+
						"([oauth.%s] in mailferry.toml)", s.Index, ref.side, ref.name, ref.name))
			}
		}
	}
	for name := range used {
		if p, ok := profiles[name]; ok {
			if err := p.Validate(); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("OAuth configuration problems:\n%s", strings.Join(errs, "\n"))
}
