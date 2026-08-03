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

// Which account did the operator just sign in as?
//
// After an interactive authorisation MailFerry reports the mailbox that was
// authorised — "✓ Authorised … Account: jeslyn@example.com" — because a
// profile that silently belongs to the wrong account is a migration
// disaster waiting to happen. Both Microsoft and Google return that identity
// in a standard OpenID Connect id_token when the interactive request asks
// for the openid/email scopes, which is what MailFerry does.
//
// IMPORTANT — this is a LABEL, never an authorisation decision. The id_token
// signature is deliberately NOT verified here: the token was received
// directly from the provider's token endpoint over an authenticated TLS
// channel in the same request MailFerry initiated, and MailFerry uses the
// claim only to print a name and record it in the profile. Nothing in the
// migration engine trusts it. (Verifying it would require fetching and
// caching provider JWKS purely to decorate one line of output.)
package oauth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// oidcClaims are the display-only identity claims MailFerry reads.
type oidcClaims struct {
	Email             string `json:"email"`
	PreferredUsername string `json:"preferred_username"`
	UPN               string `json:"upn"`
	UniqueName        string `json:"unique_name"`
}

// accountFromIDToken extracts a human-meaningful account name from an OIDC
// id_token, or "" when there is none. It never errors: a missing or
// unreadable claim simply means MailFerry does not print an account.
func accountFromIDToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c oidcClaims
	if json.Unmarshal(raw, &c) != nil {
		return ""
	}
	for _, v := range []string{c.Email, c.PreferredUsername, c.UPN, c.UniqueName} {
		if v = strings.TrimSpace(v); v != "" && strings.Contains(v, "@") {
			return v
		}
	}
	return ""
}

// oidcScopes are appended to interactive delegated authorisation requests so
// the provider returns an id_token identifying the signed-in account. They
// grant no mail access whatsoever — "openid" and "email" are identity-only.
var oidcScopes = []string{"openid", "email"}
