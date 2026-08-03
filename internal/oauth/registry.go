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

// Token-source sharing for bulk / MSP migrations.
//
// A Microsoft 365 application (client-credentials) profile mints ONE
// tenant-level access token that authorises EVERY mailbox the tenant has
// consented to. Without sharing, a 500-mailbox migration with 10 workers
// would ask Entra ID for 500 identical tokens and keep re-asking as they
// expire — needless load, needless throttling risk (AADSTS429), and 500
// independent expiry clocks.
//
// The Registry caches token sources by (profile, token identity):
//
//	application      one source per PROFILE   — the token is app-level, so
//	                 every mailbox shares it, and one refresh serves all
//	delegated        one source per USER      — the token IS the user
//	service_account  one source per USER      — each token impersonates one
//	                 specific mailbox (sub claim)
//
// Every cached source is already concurrency-safe (TokenSource serialises
// refreshes behind its own mutex), so N workers sharing one administrative
// source is correct by construction: the first to notice expiry refreshes,
// the rest wait briefly and reuse the new token.
package oauth

import (
	"strings"
	"sync"

	"github.com/ajsap/mailferry/v2/internal/imapx"
)

// Registry hands out shared, refreshing token sources for a set of profiles.
// The zero value is not usable — construct with NewRegistry.
type Registry struct {
	mu  sync.Mutex
	src map[string]*TokenSource
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry { return &Registry{src: map[string]*TokenSource{}} }

// tokenIdentity is the cache key suffix: what actually distinguishes one
// minted token from another for this authentication model.
func tokenIdentity(p Profile, user string) string {
	if p.kind() == KindApplication {
		return "" // app-level token: identical for every mailbox
	}
	return strings.ToLower(user)
}

// SASLAuth returns an authenticator for (profile, user), reusing the cached
// token source whenever the underlying token is the same one. Safe for
// concurrent use by every mailbox runner in the process.
func (r *Registry) SASLAuth(p Profile, user string) (*imapx.SASLAuth, error) {
	ts, err := r.source(p, user)
	if err != nil {
		return nil, err
	}
	return ts.SASLAuthFor(user), nil
}

// source resolves (and caches) the token source for (profile, user).
func (r *Registry) source(p Profile, user string) (*TokenSource, error) {
	key := p.Name + "\x1f" + string(p.kind()) + "\x1f" + tokenIdentity(p, user)
	r.mu.Lock()
	defer r.mu.Unlock()
	if ts, ok := r.src[key]; ok {
		return ts, nil
	}
	ts, err := NewTokenSource(p, user)
	if err != nil {
		return nil, err
	}
	r.src[key] = ts
	return ts, nil
}

// Sources reports how many distinct token sources are live — used by tests
// to prove an administrative profile really is shared rather than
// re-authenticated per mailbox.
func (r *Registry) Sources() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.src)
}
