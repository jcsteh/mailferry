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

// End-to-end proof that the Source/Destination panel reports what the
// servers actually said, and says "Unknown" when they said nothing.
package mailferry_test

import (
	"testing"

	"github.com/ajsap/mailferry/v2/internal/config"
	"github.com/ajsap/mailferry/v2/internal/fakeimap"
)

// TestServerIdentificationFromLiveConnection: what the servers reported over
// the wire is what the run records — for both sides, independently.
func TestServerIdentificationFromLiveConnection(t *testing.T) {
	srcA := maSource(6, 100)
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)
	h.src.ServerName, h.src.ServerVersion = "Dovecot", "2.3.21"
	h.dst.ServerName, h.dst.ServerVersion = "Axigen", "10.3"

	h.run(nil)

	mbs := h.stats.Snapshot().Mailboxes
	if len(mbs) == 0 {
		t.Fatal("no mailbox recorded")
	}
	mb := mbs[0]
	if mb.Src.Server != "Dovecot" || mb.Src.ServerVersion != "2.3.21" {
		t.Fatalf("source identification: %q %q", mb.Src.Server, mb.Src.ServerVersion)
	}
	if mb.Dst.Server != "Axigen" || mb.Dst.ServerVersion != "10.3" {
		t.Fatalf("destination identification: %q %q", mb.Dst.Server, mb.Dst.ServerVersion)
	}
	if got := mb.Src.ServerLabel(); got != "Dovecot 2.3.21" {
		t.Fatalf("ServerLabel = %q", got)
	}
	// Plaintext test transport: the panel must say so rather than implying
	// a secure channel.
	if mb.Src.Security != "none" || mb.Dst.Security != "none" {
		t.Fatalf("security should be reported honestly: %q / %q",
			mb.Src.Security, mb.Dst.Security)
	}
	// Password endpoints must be labelled as such.
	if mb.Src.Auth != "Password" || mb.Dst.Auth != "Password" {
		t.Fatalf("auth labels: %q / %q", mb.Src.Auth, mb.Dst.Auth)
	}
}

// TestServerIdentificationUnknownWhenSilent: a server that declines to
// identify itself is reported as Unknown — never guessed from its hostname.
func TestServerIdentificationUnknownWhenSilent(t *testing.T) {
	srcA := maSource(4, 100)
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)
	// Neither server reports a name, and no vendor-private capability is
	// advertised.
	h.run(nil)

	mb := h.stats.Snapshot().Mailboxes[0]
	if mb.Src.Server != "" || mb.Dst.Server != "" {
		t.Fatalf("a silent server must not be identified: %q / %q",
			mb.Src.Server, mb.Dst.Server)
	}
	if got := mb.Src.ServerLabel(); got != "Unknown" {
		t.Fatalf("ServerLabel = %q, want Unknown", got)
	}
}

// TestServerIdentificationOAuthLabelled: an OAuth endpoint says so, and the
// provider comes from the profile rather than from the hostname.
func TestServerIdentificationOAuthLabelled(t *testing.T) {
	srcA := maSource(4, 100)
	srcA.OAuthToken = "src-token"
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)
	h.src.ServerName = "Dovecot"

	prof := newStaticTokenProfile(t, h.dir, "src-prof", "src-token")
	prof.Provider = "microsoft"
	counts := h.runSpec(func(c *config.Run) {
		c.OAuthProfiles = map[string]config.OAuthProfile{"src-prof": prof}
	}, func(s *config.MailboxSpec) {
		s.Src.Password = ""
		s.SrcAuth = "src-prof"
	})
	if counts["SUCCESS"] != 1 {
		t.Fatalf("OAuth run: %v (%v)", counts, tail(h.logLines(), 10))
	}

	mb := h.stats.Snapshot().Mailboxes[0]
	if mb.Src.Auth != "OAuth (Microsoft)" {
		t.Fatalf("source auth label = %q", mb.Src.Auth)
	}
	if mb.Dst.Auth != "Password" {
		t.Fatalf("destination auth label = %q — the two sides are independent", mb.Dst.Auth)
	}
	// A completed Microsoft OAuth is the strongest identification evidence
	// there is, and outranks the server's own (here contradictory) self
	// report — only Microsoft's service can accept that token.
	if mb.Src.Server != "Exchange Online" {
		t.Fatalf("source server = %q, want the OAuth provider's service", mb.Src.Server)
	}
	// The password side is unaffected and still identified from its own words.
	if mb.Dst.Server != "" && mb.Dst.Server == "Exchange Online" {
		t.Fatalf("the destination was misidentified from the SOURCE's auth: %q", mb.Dst.Server)
	}
}
