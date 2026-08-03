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

package engine

import (
	"fmt"
	"sync"

	"github.com/ajsap/mailferry/v2/internal/config"
	"github.com/ajsap/mailferry/v2/internal/imapx"
	"github.com/ajsap/mailferry/v2/internal/oauth"
)

// authRegs holds ONE oauth.Registry per run configuration, so every mailbox
// runner in the process shares token sources instead of minting its own.
// This matters for the MSP case: a Microsoft 365 application profile issues
// a single tenant-level token valid for every consented mailbox, and a
// 500-mailbox migration must ask Entra ID for it ONCE, not 500 times (and
// re-ask once per expiry, not 500 times). The registry itself is
// concurrency-safe and each cached source serialises its own refreshes.
//
// Keyed by the *config.Run pointer: a process has one live run configuration
// (plus, in tests, one per test), so this stays bounded.
var authRegs sync.Map // *config.Run -> *oauth.Registry

func authRegistry(cfg *config.Run) *oauth.Registry {
	if v, ok := authRegs.Load(cfg); ok {
		return v.(*oauth.Registry)
	}
	v, _ := authRegs.LoadOrStore(cfg, oauth.NewRegistry())
	return v.(*oauth.Registry)
}

// SASLAuthFor is the exported provider-agnostic bridge for commands that
// build a single client outside a MailboxRunner (e.g. dedup). Pass a
// config.Run carrying the loaded OAuthProfiles.
func SASLAuthFor(cfg *config.Run, profileName, user string) (*imapx.SASLAuth, error) {
	return saslAuthFor(cfg, profileName, user)
}

// saslAuthFor turns an OAuth profile reference into a reusable SASL
// authenticator, or nil for password authentication (empty reference). It
// is the single provider-agnostic bridge from configuration to the IMAP
// client, shared by the migration runner and the check/verify/dedup paths.
func saslAuthFor(cfg *config.Run, profileName, user string) (*imapx.SASLAuth, error) {
	if profileName == "" {
		return nil, nil // password LOGIN — the unchanged default
	}
	prof, ok := cfg.OAuthProfiles[profileName]
	if !ok {
		return nil, fmt.Errorf("oauth profile %q is not defined ([oauth.%s] in mailferry.toml)",
			profileName, profileName)
	}
	return authRegistry(cfg).SASLAuth(oauth.FromConfig(prof), user)
}

// oauthProviderOf returns the provider family an endpoint authenticated
// against ("microsoft", "google", …), or "" for password authentication.
// Used as identification evidence: a token only that provider could mint,
// accepted by this server, says whose service it is.
func oauthProviderOf(cfg *config.Run, profileName string) string {
	if profileName == "" {
		return ""
	}
	if prof, ok := cfg.OAuthProfiles[profileName]; ok {
		return prof.Provider
	}
	return ""
}

// authLabel describes how an endpoint authenticated, for the connection
// panel. It reports the CONFIGURED mechanism, which is what actually
// happened — an OAuth endpoint never silently falls back to a password.
func authLabel(cfg *config.Run, profileName string) string {
	if profileName == "" {
		return "Password"
	}
	prof, ok := cfg.OAuthProfiles[profileName]
	if !ok {
		return "OAuth"
	}
	switch prof.Provider {
	case "microsoft":
		return "OAuth (Microsoft)"
	case "google":
		return "OAuth (Google)"
	case "generic", "":
		return "OAuth"
	default:
		return "OAuth (" + prof.Provider + ")"
	}
}

// buildAuth constructs the SASL bearer-token authenticators for this
// runner's two endpoints, once, from any referenced OAuth profile. It is
// deliberately PROVIDER-AGNOSTIC: it looks a profile up by name and hands it
// to internal/oauth, which owns all Microsoft/Google/generic knowledge. The
// engine never branches on the provider — it just attaches whatever
// authenticator (or none, for password LOGIN) comes back. Source and
// destination are independent: one may be OAuth and the other a password.
func (r *MailboxRunner) buildAuth() error {
	if r.authBuilt {
		return nil
	}
	src, err := saslAuthFor(r.Cfg, r.Spec.SrcAuth, r.Spec.Src.User)
	if err != nil {
		return fmt.Errorf("source authentication: %w", err)
	}
	dst, err := saslAuthFor(r.Cfg, r.Spec.DstAuth, r.Spec.Dst.User)
	if err != nil {
		return fmt.Errorf("destination authentication: %w", err)
	}
	r.srcAuth, r.dstAuth = src, dst
	r.authBuilt = true
	return nil
}
