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

import "github.com/ajsap/mailferry/v2/internal/config"

// FromConfig converts a declarative config.OAuthProfile (parsed from
// mailferry.toml) into an oauth.Profile ready to build a token source. It is
// pure data mapping — the only place the two representations meet.
func FromConfig(p config.OAuthProfile) Profile {
	return Profile{
		Name:         p.Name,
		ProviderName: p.Provider,
		Kind:         Kind(p.Kind),
		Tenant:       p.Tenant,
		ClientID:     p.ClientID,
		Mechanism:    p.Mechanism,
		Scopes:       p.Scopes,
		ClientSecret: SecretRef(p.ClientSecret),
		RefreshToken: SecretRef(p.RefreshToken),
		AccessToken:  SecretRef(p.AccessToken),
		KeyJSON:      SecretRef(p.KeyJSON),
		TokenURL:     p.TokenURL,
	}
}
