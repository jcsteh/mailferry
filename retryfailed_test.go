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

// The two ways to retry a failed message, and the line between them.
//
//	mailferry retry-failed          ADMINISTRATIVE — a deliberate State
//	                                Database write. Rows go back to `planned`
//	                                and the registry to RETRY_PENDING, so the
//	                                messages rejoin EVERY future run.
//
//	run CSV --retry-failed          RUNTIME — a scheduling override for this
//	                                execution only. Failed rows are admitted
//	                                to the work queue; nothing is rewritten
//	                                up front.
//
// The distinction matters because a runtime flag that silently rewrote
// persistent migration state would be a trap: the operator would expect the
// next run to behave as before, and it would not.
package mailferry_test

import (
	"database/sql"
	"testing"

	"github.com/ajsap/mailferry/v2/internal/config"
	"github.com/ajsap/mailferry/v2/internal/engine"
	"github.com/ajsap/mailferry/v2/internal/fakeimap"
	_ "modernc.org/sqlite"
)

// rejectingSource builds a mailbox where exactly one message is refused by
// the destination, so it lands in the Failed Message Registry.
func rejectingSource(t *testing.T, n int) (*fakeimap.Account, *fakeimap.Account, *harness) {
	t.Helper()
	srcA := fakeimap.NewAccount("alice", "pw1")
	for i := 0; i < n; i++ {
		marker := "FINE"
		if i == 3 {
			marker = "REJECTME"
		}
		srcA.Folder("INBOX").Add(poisonBody(i, marker), nil, "17-Jul-2026 10:00:00 +0000")
	}
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)
	h.dst.AppendReject = []byte("REJECTME")
	return srcA, dstA, h
}

// messageStates returns src_uid -> state from the messages table, so a test
// can assert what the State Database actually holds.
func messageStates(t *testing.T, dbPath string) map[uint32]string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT src_uid, state FROM messages")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[uint32]string{}
	for rows.Next() {
		var uid uint32
		var st string
		rows.Scan(&uid, &st)
		out[uid] = st
	}
	return out
}

// failCounts returns src_uid -> fail_count from the registry.
func failCounts(t *testing.T, dbPath string) map[uint32]int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT src_uid, fail_count FROM failed_messages")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[uint32]int{}
	for rows.Next() {
		var uid uint32
		var n int
		rows.Scan(&uid, &n)
		out[uid] = n
	}
	return out
}

func assertIdentity(t *testing.T, h *harness, tag string) {
	t.Helper()
	a := h.stats.Snapshot().Agg()
	if a.MsgsDone != a.Appended+a.Adopted+a.PriorDone+a.Planned {
		t.Fatalf("%s: statistics identity broken — MsgsDone=%d != %d+%d+%d+%d",
			tag, a.MsgsDone, a.Appended, a.Adopted, a.PriorDone, a.Planned)
	}
}

// oneRun is the shared single-worker configuration.
func oneRun(c *config.Run) { c.Workers = 1; c.StaleTimeout = 0 }

// --- the default: failed messages are excluded ----------------------------

// TestDefaultRunExcludesFailedMessages is the baseline the whole design
// rests on: one poison message must not stall every future run.
func TestDefaultRunExcludesFailedMessages(t *testing.T) {
	_, dstA, h := rejectingSource(t, 8)

	if counts := h.run(oneRun); counts["WARNINGS"] != 1 {
		t.Fatalf("first run: %v (%v)", counts, tail(h.logLines(), 10))
	}
	if n := len(dstA.Folder("INBOX").Msgs); n != 7 {
		t.Fatalf("healthy messages delivered: %d (want 7)", n)
	}

	// Even with the server fixed, a plain resume must NOT retry it.
	h.dst.AppendReject = nil
	before := h.dst.AppendCount.Load()
	h.run(oneRun)
	if d := h.dst.AppendCount.Load() - before; d != 0 {
		t.Fatalf("plain resume attempted %d append(s); failed messages must be excluded", d)
	}
	if st := readRegistryStatus(t, h.cfg.DBPath); st[4] != "FAILED" {
		t.Fatalf("registry status = %q, want FAILED", st[4])
	}
}

// --- runtime override -----------------------------------------------------

// TestRuntimeRetryFailedIncludesThemForThisRunOnly is the headline
// behaviour: the flag admits failed messages to the queue, they migrate,
// and the registry follows through the ordinary success path.
func TestRuntimeRetryFailedIncludesThemForThisRunOnly(t *testing.T) {
	_, dstA, h := rejectingSource(t, 8)

	h.run(oneRun)
	if n := len(dstA.Folder("INBOX").Msgs); n != 7 {
		t.Fatalf("setup: %d delivered (want 7)", n)
	}

	// Server fixed; retry for this run only.
	h.dst.AppendReject = nil
	counts := h.run(func(c *config.Run) { oneRun(c); c.SkipKnownFailed = false })
	if counts["SUCCESS"] != 1 {
		t.Fatalf("retry run: %v (%v)", counts, tail(h.logLines(), 12))
	}
	if n := len(dstA.Folder("INBOX").Msgs); n != 8 {
		t.Fatalf("after --retry-failed: %d delivered (want 8)", n)
	}
	if u := uniqueBodies(dstA.Folder("INBOX")); u != 8 {
		t.Fatalf("retry created duplicates: %d unique of 8", u)
	}
	// Success flows through the normal path: registry RECOVERED, row done.
	if st := readRegistryStatus(t, h.cfg.DBPath); st[4] != "RECOVERED" {
		t.Fatalf("registry status after success = %q, want RECOVERED", st[4])
	}
	if ms := messageStates(t, h.cfg.DBPath); ms[4] != "done" {
		t.Fatalf("message state after success = %q, want done", ms[4])
	}
	assertIdentity(t, h, "runtime retry")
}

// TestRuntimeRetryFailedDoesNotRewriteStateUpFront is the contract that
// separates the flag from the command. When the message fails AGAIN, the
// State Database must look exactly as it would have without the flag —
// still FAILED, never RETRY_PENDING, never reset to planned.
func TestRuntimeRetryFailedDoesNotRewriteStateUpFront(t *testing.T) {
	_, _, h := rejectingSource(t, 8)

	h.run(oneRun)
	beforeStates := messageStates(t, h.cfg.DBPath)
	beforeCounts := failCounts(t, h.cfg.DBPath)
	if beforeStates[4] != "failed" {
		t.Fatalf("setup: message state = %q, want failed", beforeStates[4])
	}

	// Server still rejecting. Run WITH the override.
	counts := h.run(func(c *config.Run) { oneRun(c); c.SkipKnownFailed = false })
	if counts["WARNINGS"] != 1 {
		t.Fatalf("retry run should still warn: %v", counts)
	}

	st := readRegistryStatus(t, h.cfg.DBPath)
	if st[4] == "RETRY_PENDING" {
		t.Fatal("the runtime flag must NEVER set RETRY_PENDING — that is the " +
			"administrative command's job")
	}
	if st[4] != "FAILED" {
		t.Fatalf("registry status = %q, want FAILED", st[4])
	}
	if ms := messageStates(t, h.cfg.DBPath); ms[4] != "failed" {
		t.Fatalf("message state = %q, want failed (the flag must not reset to planned)", ms[4])
	}
	// A genuine re-attempt happened, so the failure count moved on.
	if after := failCounts(t, h.cfg.DBPath); after[4] <= beforeCounts[4] {
		t.Fatalf("fail_count %d -> %d: the message was not actually retried",
			beforeCounts[4], after[4])
	}
	assertIdentity(t, h, "repeat failure")
}

// TestRuntimeRetryFailedIsNotPersistent: the override applies to one
// execution. The next run without it must exclude failed messages again.
func TestRuntimeRetryFailedIsNotPersistent(t *testing.T) {
	_, _, h := rejectingSource(t, 8)
	h.run(oneRun)

	// One overridden run against a still-broken server.
	h.run(func(c *config.Run) { oneRun(c); c.SkipKnownFailed = false })

	// Now fix the server and run WITHOUT the flag: it must stay excluded,
	// proving the override did not persist anything.
	h.dst.AppendReject = nil
	before := h.dst.AppendCount.Load()
	h.run(oneRun)
	if d := h.dst.AppendCount.Load() - before; d != 0 {
		t.Fatalf("a later plain run attempted %d append(s) — the runtime override "+
			"leaked into persistent state", d)
	}
}

// --- administrative command ----------------------------------------------

// TestRetryFailedCommandIsPermanent: the command rewrites state so the
// messages rejoin future runs with no flag at all.
func TestRetryFailedCommandIsPermanent(t *testing.T) {
	_, dstA, h := rejectingSource(t, 8)
	h.run(oneRun)

	// The administrative operation — no migration involved.
	db, err := engine.OpenStateForTest(h.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	n := db.SetFailedStatus("RETRY_PENDING", "", "", 0)
	db.Close()
	if n != 1 {
		t.Fatalf("re-queued %d (want 1)", n)
	}

	// It reset persistent state immediately, with no run.
	if st := readRegistryStatus(t, h.cfg.DBPath); st[4] != "RETRY_PENDING" {
		t.Fatalf("registry status = %q, want RETRY_PENDING", st[4])
	}
	if ms := messageStates(t, h.cfg.DBPath); ms[4] != "planned" {
		t.Fatalf("message state = %q, want planned", ms[4])
	}

	// A plain run — no flag — now picks it up.
	h.dst.AppendReject = nil
	before := h.dst.AppendCount.Load()
	counts := h.run(oneRun)
	if counts["SUCCESS"] != 1 {
		t.Fatalf("run after retry-failed: %v (%v)", counts, tail(h.logLines(), 10))
	}
	if d := h.dst.AppendCount.Load() - before; d != 1 {
		t.Fatalf("appended %d (want 1 — the re-queued message)", d)
	}
	if n := len(dstA.Folder("INBOX").Msgs); n != 8 {
		t.Fatalf("delivered %d (want 8)", n)
	}
	if st := readRegistryStatus(t, h.cfg.DBPath); st[4] != "RECOVERED" {
		t.Fatalf("registry status = %q, want RECOVERED", st[4])
	}
	assertIdentity(t, h, "administrative retry")
}

// TestRetryFailedCommandScoping: --mailbox / --folder / --uid narrow the
// permanent re-queue.
func TestRetryFailedCommandScoping(t *testing.T) {
	srcA := fakeimap.NewAccount("alice", "pw1")
	for i := 0; i < 6; i++ {
		marker := "FINE"
		if i == 2 || i == 4 {
			marker = "REJECTME"
		}
		srcA.Folder("INBOX").Add(poisonBody(i, marker), nil, "17-Jul-2026 10:00:00 +0000")
	}
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)
	h.dst.AppendReject = []byte("REJECTME")
	h.run(oneRun)
	_ = dstA

	if got := len(readRegistryStatus(t, h.cfg.DBPath)); got != 2 {
		t.Fatalf("expected 2 registry entries, got %d", got)
	}
	db, err := engine.OpenStateForTest(h.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	n := db.SetFailedStatus("RETRY_PENDING", "", "", 3) // UID 3 only
	db.Close()
	if n != 1 {
		t.Fatalf("scoped re-queue affected %d rows (want 1)", n)
	}
	st := readRegistryStatus(t, h.cfg.DBPath)
	if st[3] != "RETRY_PENDING" {
		t.Fatalf("UID 3 status = %q, want RETRY_PENDING", st[3])
	}
	if st[5] != "FAILED" {
		t.Fatalf("UID 5 status = %q — scoping must leave it alone", st[5])
	}
	ms := messageStates(t, h.cfg.DBPath)
	if ms[3] != "planned" || ms[5] != "failed" {
		t.Fatalf("scoped reset wrong: uid3=%q uid5=%q", ms[3], ms[5])
	}
}

// --- the two must not be confused ----------------------------------------

// TestRuntimeAndAdministrativeRetryAreDistinct states the contract plainly:
// identical starting state, one operation each, different persistent
// outcomes.
func TestRuntimeAndAdministrativeRetryAreDistinct(t *testing.T) {
	// Runtime override, server still broken → state unchanged.
	_, _, hRun := rejectingSource(t, 6)
	hRun.run(oneRun)
	hRun.run(func(c *config.Run) { oneRun(c); c.SkipKnownFailed = false })
	runState := messageStates(t, hRun.cfg.DBPath)[4]
	runStatus := readRegistryStatus(t, hRun.cfg.DBPath)[4]

	// Administrative command, no run at all → state rewritten.
	_, _, hCmd := rejectingSource(t, 6)
	hCmd.run(oneRun)
	db, err := engine.OpenStateForTest(hCmd.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetFailedStatus("RETRY_PENDING", "", "", 0)
	db.Close()
	cmdState := messageStates(t, hCmd.cfg.DBPath)[4]
	cmdStatus := readRegistryStatus(t, hCmd.cfg.DBPath)[4]

	if runState != "failed" || runStatus != "FAILED" {
		t.Fatalf("runtime override changed persistent state: %q/%q", runState, runStatus)
	}
	if cmdState != "planned" || cmdStatus != "RETRY_PENDING" {
		t.Fatalf("administrative command did not persist: %q/%q", cmdState, cmdStatus)
	}
	if runState == cmdState {
		t.Fatal("the runtime flag and the administrative command must differ")
	}
}
