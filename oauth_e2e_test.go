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

// End-to-end OAuth migration tests: a full migration authenticating with
// SASL XOAUTH2 (fake provider token), proving the engine consumes an
// authenticated connection without any provider-specific logic, that
// password and OAuth endpoints interoperate in one run, and that the bearer
// token never leaks into the session log. Synthetic RFC 2606 data only.
package mailferry_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ajsap/mailferry/v2/internal/config"
	"github.com/ajsap/mailferry/v2/internal/engine"
	"github.com/ajsap/mailferry/v2/internal/fakeimap"
)

// oauthProfile builds a static-access-token profile whose token is read from
// an env var — no HTTP, deterministic, and exercising the secret-reference
// path (env:) end to end.
func oauthProfile(t *testing.T, name, envVar, token string) config.OAuthProfile {
	t.Helper()
	t.Setenv(envVar, token)
	return config.OAuthProfile{
		Name: name, Provider: "google", ClientID: "cid.apps",
		AccessToken: "env:" + envVar,
	}
}

func runOAuth(t *testing.T, src, dst *fakeimap.Server, spec config.MailboxSpec,
	profiles map[string]config.OAuthProfile) (map[string]int, []string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.CSVFile = "test.csv"
	cfg.DBPath = filepath.Join(dir, "migration.db")
	cfg.LogsDir = filepath.Join(dir, "logs")
	cfg.Workers = 1
	cfg.Timeout = 30
	cfg.RunID = time.Now().Format("20060102-150405.000")
	cfg.OAuthProfiles = profiles
	os.MkdirAll(cfg.LogsDir, 0o755)

	// The engine logs from several goroutines at once (a mailbox opens its
	// source and destination connections in parallel), so the collector needs
	// its own lock.
	var logMu sync.Mutex
	var log []string
	record := func(s string) {
		logMu.Lock()
		log = append(log, s)
		logMu.Unlock()
	}
	logf := func(config.MailboxSpec) func(string, ...any) {
		return func(f string, a ...any) { record(fmt.Sprintf(f, a...)) }
	}
	res, err := engine.RunMigration(context.Background(), cfg, []config.MailboxSpec{spec},
		engine.NewStats(), record, logf)
	if err != nil {
		t.Fatal(err)
	}
	logMu.Lock()
	defer logMu.Unlock()
	return res.Counts, append([]string(nil), log...)
}

// TestOAuthMigrationBothSides: source AND destination authenticate with
// XOAUTH2. Proves a full migration works over bearer-token auth.
func TestOAuthMigrationBothSides(t *testing.T) {
	srcA := buildSrc()
	srcA.OAuthToken = "src-access-token"
	dstA := fakeimap.NewAccount("bob@example.net", "unused")
	dstA.OAuthToken = "dst-access-token"

	src := fakeimap.NewServer(srcA)
	dst := fakeimap.NewServer(dstA)
	mustStart(t, src, dst)

	profiles := map[string]config.OAuthProfile{
		"srcp": oauthProfile(t, "srcp", "MF_E2E_SRC", "src-access-token"),
		"dstp": oauthProfile(t, "dstp", "MF_E2E_DST", "dst-access-token"),
	}
	spec := config.MailboxSpec{
		Index:   1,
		Src:     config.Endpoint{Host: "127.0.0.1", Port: src.Port(), Security: "none", User: srcA.User},
		Dst:     config.Endpoint{Host: "127.0.0.1", Port: dst.Port(), Security: "none", User: dstA.User},
		SrcAuth: "srcp", DstAuth: "dstp",
	}
	counts, log := runOAuth(t, src, dst, spec, profiles)
	if counts["SUCCESS"] != 1 {
		t.Fatalf("OAuth migration should succeed: %v (log tail: %v)", counts, tail(log, 8))
	}
	if got, want := dstA.TotalMsgs(), srcA.TotalMsgs(); got != want {
		t.Fatalf("messages migrated: %d != %d", got, want)
	}
	// The bearer tokens must never appear in the session log.
	joined := strings.Join(log, "\n")
	for _, tok := range []string{"src-access-token", "dst-access-token"} {
		if strings.Contains(joined, tok) {
			t.Fatalf("bearer token %q leaked into the session log", tok)
		}
	}
}

// TestOAuthMixedWithPassword: source uses password LOGIN, destination uses
// XOAUTH2 — the common MSP case of migrating from a legacy server into a
// modern-auth tenant. Proves the two mechanisms interoperate in one run.
func TestOAuthMixedWithPassword(t *testing.T) {
	srcA := buildSrc() // password auth (pw1)
	dstA := fakeimap.NewAccount("bob@example.net", "unused")
	dstA.OAuthToken = "dst-access-token"

	src := fakeimap.NewServer(srcA)
	dst := fakeimap.NewServer(dstA)
	mustStart(t, src, dst)

	profiles := map[string]config.OAuthProfile{
		"dstp": oauthProfile(t, "dstp", "MF_E2E_MIX", "dst-access-token"),
	}
	spec := config.MailboxSpec{
		Index: 1,
		Src: config.Endpoint{Host: "127.0.0.1", Port: src.Port(), Security: "none",
			User: srcA.User, Password: srcA.Password},
		Dst:     config.Endpoint{Host: "127.0.0.1", Port: dst.Port(), Security: "none", User: dstA.User},
		DstAuth: "dstp",
	}
	counts, log := runOAuth(t, src, dst, spec, profiles)
	if counts["SUCCESS"] != 1 {
		t.Fatalf("mixed auth migration should succeed: %v (log tail: %v)", counts, tail(log, 8))
	}
	if got, want := dstA.TotalMsgs(), srcA.TotalMsgs(); got != want {
		t.Fatalf("messages migrated: %d != %d", got, want)
	}
}

// TestOAuthBadTokenFailsClean: a wrong token must fail the mailbox with an
// authentication error — never silently fall back to password LOGIN.
func TestOAuthBadTokenFailsClean(t *testing.T) {
	dstA := fakeimap.NewAccount("bob@example.net", "the-real-password")
	dstA.OAuthToken = "correct-token"
	srcA := buildSrc()
	src := fakeimap.NewServer(srcA)
	dst := fakeimap.NewServer(dstA)
	mustStart(t, src, dst)

	profiles := map[string]config.OAuthProfile{
		"dstp": oauthProfile(t, "dstp", "MF_E2E_BAD", "WRONG-token"),
	}
	spec := config.MailboxSpec{
		Index: 1,
		Src: config.Endpoint{Host: "127.0.0.1", Port: src.Port(), Security: "none",
			User: srcA.User, Password: srcA.Password},
		Dst:     config.Endpoint{Host: "127.0.0.1", Port: dst.Port(), Security: "none", User: dstA.User},
		DstAuth: "dstp",
	}
	counts, _ := runOAuth(t, src, dst, spec, profiles)
	if counts["SUCCESS"] == 1 {
		t.Fatal("a wrong bearer token must NOT authenticate (no password fallback)")
	}
	if dstA.TotalMsgs() != 0 {
		t.Fatalf("no messages should have been written on auth failure, got %d", dstA.TotalMsgs())
	}
}

func mustStart(t *testing.T, servers ...*fakeimap.Server) {
	t.Helper()
	for _, s := range servers {
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Stop)
	}
}
