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

// MULTIAPPEND (RFC 3502) behavioural matrix.
//
// MULTIAPPEND is an OPTIMISATION, so every test here asserts the same thing
// from a different angle: the optimisation changes how many APPEND commands
// are issued and NOTHING else. Message counts, uniqueness (no duplicates),
// flags, INTERNALDATE, per-message accounting and the statistics identity
// must be indistinguishable from the single-append path.
//
// Everything runs against the deterministic in-process fake IMAP server —
// no public server, no network, no credentials.
package mailferry_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ajsap/mailferry/v2/internal/config"
	"github.com/ajsap/mailferry/v2/internal/engine"
	"github.com/ajsap/mailferry/v2/internal/fakeimap"
)

// maSource builds a source mailbox of n uniform, individually identifiable
// messages (RFC 2606 domains only).
func maSource(n, pad int) *fakeimap.Account {
	a := fakeimap.NewAccount("alice", "pw1")
	inbox := a.Folder("INBOX")
	for i := 1; i <= n; i++ {
		var flags []string
		if i%2 == 0 {
			flags = append(flags, `\Seen`)
		}
		if i%5 == 0 {
			flags = append(flags, `\Flagged`)
		}
		inbox.Add(msgBody(i, "batch", true, pad), flags,
			fmt.Sprintf("%02d-Jun-2026 09:00:00 +0000", (i%27)+1))
	}
	return a
}

// assertIntact is the invariant every case shares: every source message
// arrived exactly once.
func assertIntact(t *testing.T, srcA, dstA *fakeimap.Account, want int) {
	t.Helper()
	if n := len(dstA.Folder("INBOX").Msgs); n != want {
		t.Fatalf("destination has %d message(s), want %d", n, want)
	}
	if u := uniqueBodies(dstA.Folder("INBOX")); u != want {
		t.Fatalf("duplicates created: %d unique of %d", u, want)
	}
	if got, wantTotal := dstA.TotalMsgs(), srcA.TotalMsgs(); got != wantTotal {
		t.Fatalf("total messages %d != source %d", got, wantTotal)
	}
}

// assertStatsIdentity re-asserts the canonical accounting identity, because
// a batch must never be counted as one message.
func assertStatsIdentity(t *testing.T, h *harness) {
	t.Helper()
	agg := h.stats.Snapshot().Agg()
	if sum := agg.Appended + agg.Adopted + agg.PriorDone + agg.Planned; agg.MsgsDone != sum {
		t.Fatalf("statistics identity broken: MsgsDone=%d != appended %d + adopted %d + "+
			"prior %d + planned %d", agg.MsgsDone, agg.Appended, agg.Adopted,
			agg.PriorDone, agg.Planned)
	}
}

// 1. MULTIAPPEND advertised and working — the default path.
func TestMultiAppendAdvertisedAndUsed(t *testing.T) {
	srcA := maSource(40, 200)
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)

	counts := h.run(func(c *config.Run) { c.Workers = 1; c.MaxConnsPerBox = 1 })
	if counts["SUCCESS"] != 1 {
		t.Fatalf("expected SUCCESS, got %v (%v)", counts, tail(h.sessions, 8))
	}
	assertIntact(t, srcA, dstA, 40)
	assertStatsIdentity(t, h)

	cmds, msgs := h.dst.AppendCmds.Load(), h.dst.AppendCount.Load()
	if msgs != 40 {
		t.Fatalf("server stored %d message(s) via APPEND, want 40", msgs)
	}
	if cmds >= msgs {
		t.Fatalf("MULTIAPPEND saved nothing: %d command(s) for %d message(s)", cmds, msgs)
	}
	if h.dst.MaxBatchSeen.Load() < 2 {
		t.Fatalf("no batch larger than one message was ever sent")
	}
	// Flags and INTERNALDATE survive batching exactly as they do unbatched.
	assertFlagsAndDatesPreserved(t, srcA, dstA)
}

// assertFlagsAndDatesPreserved matches every source message to its
// destination copy by body and compares flags and INTERNALDATE.
func assertFlagsAndDatesPreserved(t *testing.T, srcA, dstA *fakeimap.Account) {
	t.Helper()
	dst := map[string]*fakeimap.Msg{}
	for _, m := range dstA.Folder("INBOX").Msgs {
		dst[string(m.Body)] = m
	}
	for _, s := range srcA.Folder("INBOX").Msgs {
		d, ok := dst[string(s.Body)]
		if !ok {
			t.Fatalf("source UID %d never arrived", s.UID)
		}
		if d.InternalDate != s.InternalDate {
			t.Fatalf("uid %d INTERNALDATE %q != %q", s.UID, d.InternalDate, s.InternalDate)
		}
		for f := range s.Flags {
			if strings.EqualFold(f, `\Recent`) {
				continue
			}
			if !d.Flags[f] {
				t.Fatalf("uid %d lost flag %s in a batched append", s.UID, f)
			}
		}
	}
}

// 2. MULTIAPPEND not advertised — transparent fallback, never a failure.
func TestMultiAppendNotAdvertisedFallsBack(t *testing.T) {
	srcA := maSource(25, 100)
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)
	h.dst.NoMultiAppend.Store(true)

	if counts := h.run(nil); counts["SUCCESS"] != 1 {
		t.Fatalf("a destination without MULTIAPPEND must migrate normally: %v", counts)
	}
	assertIntact(t, srcA, dstA, 25)
	assertStatsIdentity(t, h)
	if n := h.dst.MultiAppendN.Load(); n != 0 {
		t.Fatalf("issued %d batched append(s) to a server that never advertised MULTIAPPEND", n)
	}
	if c := h.dst.AppendCmds.Load(); c != 25 {
		t.Fatalf("expected one command per message, got %d", c)
	}
}

// 3. Explicitly disabled — every message gets its own APPEND.
func TestMultiAppendExplicitlyDisabled(t *testing.T) {
	srcA := maSource(20, 100)
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)

	h.run(func(c *config.Run) { c.MultiAppend = "off" })
	assertIntact(t, srcA, dstA, 20)
	if n := h.dst.MultiAppendN.Load(); n != 0 {
		t.Fatalf("multiappend=off still batched %d command(s)", n)
	}
	if c := h.dst.AppendCmds.Load(); c != 20 {
		t.Fatalf("multiappend=off: %d command(s) for 20 messages", c)
	}
}

// 4. Explicit batch size is honoured exactly.
func TestMultiAppendExplicitBatchSize(t *testing.T) {
	srcA := maSource(30, 100)
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)

	h.run(func(c *config.Run) {
		c.Workers, c.MaxConnsPerBox = 1, 1
		c.MultiAppendBatch = "10"
	})
	assertIntact(t, srcA, dstA, 30)
	if b := h.dst.MaxBatchSeen.Load(); b != 10 {
		t.Fatalf("largest batch %d, want exactly 10", b)
	}
	if c := h.dst.AppendCmds.Load(); c != 3 {
		t.Fatalf("30 messages at 10 per command should be 3 commands, got %d", c)
	}
}

// 5. Auto batch size stays inside its own documented limits.
func TestMultiAppendAutoRespectsItsLimits(t *testing.T) {
	srcA := maSource(40, 100)
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)

	h.run(func(c *config.Run) { c.Workers, c.MaxConnsPerBox = 1, 1 })
	policy := h.cfg.MultiAppendPolicyFor()
	if !policy.Auto {
		t.Fatal("the default policy must be automatic")
	}
	if b := h.dst.MaxBatchSeen.Load(); b > int64(policy.MaxCount) {
		t.Fatalf("auto batching sent %d messages in one command, above its own cap of %d",
			b, policy.MaxCount)
	}
	assertIntact(t, srcA, dstA, 40)
}

// 6 + 7. A message at or above the solo threshold is never batched, and the
// aggregate byte budget closes a batch early.
func TestMultiAppendLargeMessageGoesAloneAndByteCapApplies(t *testing.T) {
	srcA := fakeimap.NewAccount("alice", "pw1")
	inbox := srcA.Folder("INBOX")
	// Two small messages, then one very large one, then two more small.
	inbox.Add(msgBody(1, "small", true, 100), nil, "17-Jul-2026 10:00:00 +0000")
	inbox.Add(msgBody(2, "small", true, 100), nil, "17-Jul-2026 10:00:00 +0000")
	inbox.Add(msgBody(3, "huge", true, 12<<20), nil, "17-Jul-2026 10:00:00 +0000")
	inbox.Add(msgBody(4, "small", true, 100), nil, "17-Jul-2026 10:00:00 +0000")
	inbox.Add(msgBody(5, "small", true, 100), nil, "17-Jul-2026 10:00:00 +0000")
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)

	h.run(func(c *config.Run) { c.Workers, c.MaxConnsPerBox = 1, 1 })
	assertIntact(t, srcA, dstA, 5)
	// The 12 MiB message is above SoloBytes for this configuration, so it
	// must have travelled in a command of its own.
	policy := h.cfg.MultiAppendPolicyFor()
	if int64(12<<20) < policy.SoloBytes {
		t.Fatalf("test fixture no longer exceeds the solo threshold (%d)", policy.SoloBytes)
	}
	if b := h.dst.MaxBatchSeen.Load(); b > 2 {
		t.Fatalf("a message above the solo threshold was batched with %d others", b-1)
	}
}

// 8 + 9. The connection dies AFTER a whole batch is stored but BEFORE the
// tagged OK: the ambiguous-batch case. Reconciliation must adopt what
// landed instead of appending it again.
func TestMultiAppendAckLossReconcilesWithoutDuplicates(t *testing.T) {
	srcA := maSource(24, 150)
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)
	// Kill the line once a batch of 2 or more has been committed.
	h.dst.DropBeforeOKBatch.Store(2)

	go func() {
		// Let one batch land, then stop killing so the run can finish.
		for h.dst.AppendCount.Load() < 2 {
			time.Sleep(5 * time.Millisecond)
		}
		h.dst.DropBeforeOKBatch.Store(0)
	}()
	counts := h.run(func(c *config.Run) { c.Workers, c.MaxConnsPerBox = 1, 1 })
	if counts["SUCCESS"] != 1 && counts["WARNINGS"] != 1 {
		t.Fatalf("expected the run to complete, got %v (%v)", counts, tail(h.sessions, 10))
	}
	// The whole point: no duplicates despite an acknowledged-but-unknown batch.
	assertIntact(t, srcA, dstA, 24)
	assertStatsIdentity(t, h)
	if !h.logContains("reconcil") && !h.logContains("adopted") {
		t.Fatalf("expected reconciliation/adoption after the lost acknowledgement:\n%v",
			tail(h.sessions, 12))
	}
}

// 10. Cancellation during a batch: MailFerry stops promptly, leaves no
// duplicates, and a later run completes the migration.
func TestMultiAppendCancellationMidBatch(t *testing.T) {
	srcA := maSource(60, 2000)
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)
	h.dst.AppendDelayMS.Store(15) // per message, so a batch takes real time

	cfg := config.Defaults()
	cfg.CSVFile = "test.csv"
	cfg.DBPath = filepath.Join(h.dir, "migration.db")
	cfg.LogsDir = filepath.Join(h.dir, "logs")
	cfg.Workers, cfg.MaxConnsPerBox = 1, 1
	cfg.Timeout = 30
	cfg.RunID = "ma-cancel"
	specs := []config.MailboxSpec{{
		Index: 1,
		Src: config.Endpoint{Host: "127.0.0.1", Port: h.src.Port(), Security: "none",
			User: "alice", Password: "pw1"},
		Dst: config.Endpoint{Host: "127.0.0.1", Port: h.dst.Port(), Security: "none",
			User: "bob", Password: "pw2"},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for h.dst.AppendCount.Load() < 4 {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	engine.RunMigration(ctx, cfg, specs, engine.NewStats(), func(string) {},
		func(config.MailboxSpec) func(string, ...any) {
			return func(string, ...any) {}
		})
	delivered := len(dstA.Folder("INBOX").Msgs)
	if delivered == 0 || delivered >= 60 {
		t.Fatalf("cancellation should stop mid-migration, delivered %d of 60", delivered)
	}
	if u := uniqueBodies(dstA.Folder("INBOX")); u != delivered {
		t.Fatalf("cancellation left duplicates: %d unique of %d", u, delivered)
	}
	// A subsequent run finishes the job without duplicating anything.
	h.dst.AppendDelayMS.Store(0)
	if counts := h.run(func(c *config.Run) {
		c.DBPath = cfg.DBPath
		c.Workers, c.MaxConnsPerBox = 1, 1
	}); counts["SUCCESS"] != 1 {
		t.Fatalf("resume after cancellation: %v (%v)", counts, tail(h.sessions, 10))
	}
	assertIntact(t, srcA, dstA, 60)
}

// 10b. Cancellation at MANY different points, batched and unbatched, must
// never leave a duplicate behind after the migration is resumed.
//
// This test earned its place: it caught a real defect. A cancelled run used
// to walk away from append commands it had ALREADY put on the wire, so those
// messages stayed "inflight" while the destination was still committing
// them — and the resume could scan, not find them yet, and copy them again.
// Batching widened that window from one message to a whole batch, which is
// what made it reproducible. The fix is that cancellation settles what it
// has already sent (see settleSent in transferPass).
func TestMultiAppendCancelResumeNeverDuplicates(t *testing.T) {
	for _, mode := range []string{"auto", "off"} {
		for _, cancelAfter := range []int64{1, 3, 5, 8, 13, 21} {
			mode, cancelAfter := mode, cancelAfter
			t.Run(fmt.Sprintf("%s/cancel-after-%d", mode, cancelAfter), func(t *testing.T) {
				const total = 60
				srcA := maSource(total, 800)
				dstA := fakeimap.NewAccount("bob", "pw2")
				h := newHarness(t, srcA, dstA)
				// A per-message store cost, so the destination is genuinely
				// mid-commit when the cancellation lands.
				h.dst.AppendDelayMS.Store(4)

				cfg := config.Defaults()
				cfg.CSVFile = "test.csv"
				cfg.DBPath = filepath.Join(h.dir, "migration.db")
				cfg.LogsDir = filepath.Join(h.dir, "logs")
				cfg.Workers, cfg.MaxConnsPerBox = 1, 1
				cfg.Timeout = 30
				cfg.RunID = "ma-cancel-stress"
				cfg.MultiAppend = mode
				specs := []config.MailboxSpec{{
					Index: 1,
					Src: config.Endpoint{Host: "127.0.0.1", Port: h.src.Port(),
						Security: "none", User: "alice", Password: "pw1"},
					Dst: config.Endpoint{Host: "127.0.0.1", Port: h.dst.Port(),
						Security: "none", User: "bob", Password: "pw2"},
				}}
				ctx, cancel := context.WithCancel(context.Background())
				go func() {
					deadline := time.Now().Add(10 * time.Second)
					for h.dst.AppendCount.Load() < cancelAfter && time.Now().Before(deadline) {
						time.Sleep(time.Millisecond)
					}
					cancel()
				}()
				engine.RunMigration(ctx, cfg, specs, engine.NewStats(), func(string) {},
					func(config.MailboxSpec) func(string, ...any) {
						return func(string, ...any) {}
					})
				h.dst.AppendDelayMS.Store(0)

				counts := h.run(func(c *config.Run) {
					c.DBPath = cfg.DBPath
					c.Workers, c.MaxConnsPerBox = 1, 1
					c.MultiAppend = mode
				})
				if counts["SUCCESS"] != 1 && counts["WARNINGS"] != 1 {
					t.Fatalf("resume did not complete: %v (%v)", counts, tail(h.logLines(), 12))
				}
				assertIntact(t, srcA, dstA, total)
			})
		}
	}
}

// 10c. The status an operator reads must match what actually happened —
// a display that says "Active" while nothing is batching is worse than no
// display at all.
func TestMultiAppendStatusReflectsReality(t *testing.T) {
	t.Run("supported and used", func(t *testing.T) {
		srcA := maSource(30, 200)
		dstA := fakeimap.NewAccount("bob", "pw2")
		h := newHarness(t, srcA, dstA)
		h.run(func(c *config.Run) { c.Workers, c.MaxConnsPerBox = 1, 1 })

		ma := h.stats.Snapshot().MultiAppend()
		if !ma.Enabled || !ma.Probed || !ma.Supported {
			t.Fatalf("destination advertises MULTIAPPEND: %+v", ma)
		}
		if ma.Commands == 0 || ma.Messages == 0 {
			t.Fatalf("batching happened but the status shows none: %+v", ma)
		}
		if ma.Messages != h.dst.AppendCount.Load() {
			t.Fatalf("status counted %d batched message(s), server stored %d",
				ma.Messages, h.dst.AppendCount.Load())
		}
		if got := ma.State(); got != "Active (Auto)" {
			t.Fatalf("State() = %q, want Active (Auto)", got)
		}
	})

	t.Run("not supported", func(t *testing.T) {
		srcA := maSource(12, 200)
		dstA := fakeimap.NewAccount("bob", "pw2")
		h := newHarness(t, srcA, dstA)
		h.dst.NoMultiAppend.Store(true)
		h.run(func(c *config.Run) { c.Workers, c.MaxConnsPerBox = 1, 1 })

		ma := h.stats.Snapshot().MultiAppend()
		if ma.Supported || ma.Commands != 0 {
			t.Fatalf("a server without the capability must not report batching: %+v", ma)
		}
		if got := ma.State(); got != "Not supported by destination" {
			t.Fatalf("State() = %q", got)
		}
	})

	t.Run("disabled by configuration", func(t *testing.T) {
		srcA := maSource(12, 200)
		dstA := fakeimap.NewAccount("bob", "pw2")
		h := newHarness(t, srcA, dstA)
		h.run(func(c *config.Run) { c.MultiAppend = "off" })

		if got := h.stats.Snapshot().MultiAppend().State(); got != "Disabled" {
			t.Fatalf("State() = %q, want Disabled", got)
		}
	})

	t.Run("destination capabilities are captured", func(t *testing.T) {
		srcA := maSource(4, 100)
		dstA := fakeimap.NewAccount("bob", "pw2")
		h := newHarness(t, srcA, dstA)
		h.run(nil)

		for _, mb := range h.stats.Snapshot().Mailboxes {
			if len(mb.Dst.Advertised) == 0 {
				t.Fatal("destination capabilities were never recorded")
			}
			var found bool
			for _, c := range mb.Dst.Advertised {
				if c == "MULTIAPPEND" {
					found = true
				}
			}
			if !found {
				t.Fatalf("advertised list is missing MULTIAPPEND: %v", mb.Dst.Advertised)
			}
		}
	})
}

// 11. The destination advertises MULTIAPPEND and then rejects it (NO).
// RFC 3502 makes that atomic — nothing was appended — so MailFerry must
// fall back and still deliver every message exactly once.
func TestMultiAppendAdvertisedButRejected(t *testing.T) {
	srcA := maSource(20, 100)
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)
	h.dst.MultiAppendBroken.Store(true)

	counts := h.run(func(c *config.Run) { c.Workers, c.MaxConnsPerBox = 1, 1 })
	if counts["SUCCESS"] != 1 {
		t.Fatalf("a lying server must not fail the migration: %v (%v)",
			counts, tail(h.sessions, 12))
	}
	assertIntact(t, srcA, dstA, 20)
	assertStatsIdentity(t, h)
	if !h.logContains("MULTIAPPEND") {
		t.Fatalf("the fallback should be explained in the log:\n%v", tail(h.sessions, 12))
	}
}

// 11b. The same, but the server answers BAD — it could not even parse the
// command. That is a protocol-desynchronisation risk, so MailFerry drops the
// connection, reconciles and finishes with single appends.
func TestMultiAppendAdvertisedButUnparseable(t *testing.T) {
	srcA := maSource(20, 100)
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)
	h.dst.MultiAppendBroken.Store(true)
	h.dst.MultiAppendBAD.Store(true)

	counts := h.run(func(c *config.Run) { c.Workers, c.MaxConnsPerBox = 1, 1 })
	if counts["SUCCESS"] != 1 && counts["WARNINGS"] != 1 {
		t.Fatalf("expected recovery from a BAD MULTIAPPEND: %v (%v)",
			counts, tail(h.sessions, 12))
	}
	assertIntact(t, srcA, dstA, 20)
	assertStatsIdentity(t, h)
}

// 12. Worker concurrency + batching: many mailboxes at once must not break
// accounting or create duplicates, and the batch size must respect the
// concurrency-scaled budget.
func TestMultiAppendWithWorkerConcurrency(t *testing.T) {
	const boxes = 4
	var srcAccts, dstAccts []*fakeimap.Account
	for i := 0; i < boxes; i++ {
		a := fakeimap.NewAccount(fmt.Sprintf("src%d", i), "pw1")
		for j := 1; j <= 20; j++ {
			a.Folder("INBOX").Add(msgBody(j, fmt.Sprintf("box%d", i), true, 300), nil,
				"17-Jul-2026 10:00:00 +0000")
		}
		srcAccts = append(srcAccts, a)
		dstAccts = append(dstAccts, fakeimap.NewAccount(fmt.Sprintf("dst%d", i), "pw2"))
	}
	src := fakeimap.NewServer(srcAccts...)
	dst := fakeimap.NewServer(dstAccts...)
	if err := src.Start(); err != nil {
		t.Fatal(err)
	}
	if err := dst.Start(); err != nil {
		t.Fatal(err)
	}
	defer src.Stop()
	defer dst.Stop()

	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.CSVFile = "test.csv"
	cfg.DBPath = filepath.Join(dir, "migration.db")
	cfg.LogsDir = filepath.Join(dir, "logs")
	cfg.Workers, cfg.MaxConnsPerBox = boxes, 1
	cfg.Timeout = 30
	cfg.RunID = "ma-conc"
	var specs []config.MailboxSpec
	for i := 0; i < boxes; i++ {
		specs = append(specs, config.MailboxSpec{
			Index: i + 1,
			Src: config.Endpoint{Host: "127.0.0.1", Port: src.Port(), Security: "none",
				User: srcAccts[i].User, Password: "pw1"},
			Dst: config.Endpoint{Host: "127.0.0.1", Port: dst.Port(), Security: "none",
				User: dstAccts[i].User, Password: "pw2"},
		})
	}
	stats := engine.NewStats()
	res, err := engine.RunMigration(context.Background(), cfg, specs, stats,
		func(string) {}, func(config.MailboxSpec) func(string, ...any) {
			return func(string, ...any) {}
		})
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts["SUCCESS"] != boxes {
		t.Fatalf("concurrent batched migration: %v", res.Counts)
	}
	for i := 0; i < boxes; i++ {
		if n := len(dstAccts[i].Folder("INBOX").Msgs); n != 20 {
			t.Fatalf("mailbox %d: %d message(s), want 20", i, n)
		}
		if u := uniqueBodies(dstAccts[i].Folder("INBOX")); u != 20 {
			t.Fatalf("mailbox %d: duplicates (%d unique)", i, u)
		}
	}
	agg := stats.Snapshot().Agg()
	if sum := agg.Appended + agg.Adopted + agg.PriorDone + agg.Planned; agg.MsgsDone != sum {
		t.Fatalf("statistics identity broken under concurrency: %d != %d", agg.MsgsDone, sum)
	}
	policy := cfg.MultiAppendPolicyFor()
	if b := dst.MaxBatchSeen.Load(); b > int64(policy.MaxCount) {
		t.Fatalf("batch of %d exceeded the concurrency-scaled cap of %d", b, policy.MaxCount)
	}
}

// 13. Retry/recovery: one poisonous message inside a batch. The batch is
// rejected atomically, the messages are retried individually, the genuinely
// bad one is isolated and everything else migrates.
func TestMultiAppendRetryIsolatesTheOffender(t *testing.T) {
	srcA := maSource(16, 100)
	// UID 7's body carries the marker the destination refuses.
	srcA.Folder("INBOX").Msgs[6].Body = append(srcA.Folder("INBOX").Msgs[6].Body,
		[]byte("\r\nPOISON-MARKER\r\n")...)
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)
	h.dst.AppendReject = []byte("POISON-MARKER")

	counts := h.run(func(c *config.Run) { c.Workers, c.MaxConnsPerBox = 1, 1 })
	if counts["WARNINGS"] != 1 {
		t.Fatalf("expected COMPLETED WITH WARNINGS, got %v (%v)", counts, tail(h.sessions, 14))
	}
	if n := len(dstA.Folder("INBOX").Msgs); n != 15 {
		t.Fatalf("15 good messages should have migrated, got %d", n)
	}
	if u := uniqueBodies(dstA.Folder("INBOX")); u != 15 {
		t.Fatalf("the rejected batch was partially stored: %d unique of %d", u, 15)
	}
	assertStatsIdentity(t, h)
}

// 14. A dry run must issue NEITHER an APPEND nor a MULTIAPPEND.
func TestMultiAppendDryRunWritesNothing(t *testing.T) {
	srcA := maSource(30, 100)
	dstA := fakeimap.NewAccount("bob", "pw2")
	h := newHarness(t, srcA, dstA)

	h.run(func(c *config.Run) { c.DryRun = true; c.Ephemeral = true })
	if n := h.dst.AppendCmds.Load(); n != 0 {
		t.Fatalf("dry run issued %d append command(s)", n)
	}
	if n := h.dst.AppendCount.Load(); n != 0 {
		t.Fatalf("dry run stored %d message(s)", n)
	}
	if n := len(dstA.Folder("INBOX").Msgs); n != 0 {
		t.Fatalf("dry run left %d message(s) on the destination", n)
	}
	agg := h.stats.Snapshot().Agg()
	if agg.Planned != 30 {
		t.Fatalf("dry run should plan 30 message(s), got %d", agg.Planned)
	}
	assertStatsIdentity(t, h)
	// And the policy itself refuses to enable batching for a dry run.
	if h.cfg.MultiAppendEnabled() {
		t.Fatal("MULTIAPPEND must never be enabled for a dry run")
	}
}

// 15. Batching is authentication-independent: the same batched migration
// works when both endpoints authenticate with OAuth (SASL XOAUTH2).
func TestMultiAppendWithOAuthEndpoints(t *testing.T) {
	srcA := maSource(20, 150)
	srcA.OAuthToken = "src-bearer-token"
	dstA := fakeimap.NewAccount("bob", "pw2")
	dstA.OAuthToken = "dst-bearer-token"
	h := newHarness(t, srcA, dstA)

	srcTok := newStaticTokenProfile(t, h.dir, "src-prof", "src-bearer-token")
	dstTok := newStaticTokenProfile(t, h.dir, "dst-prof", "dst-bearer-token")

	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.CSVFile = "test.csv"
	cfg.DBPath = filepath.Join(dir, "migration.db")
	cfg.LogsDir = filepath.Join(dir, "logs")
	cfg.Workers, cfg.MaxConnsPerBox = 1, 1
	cfg.Timeout = 30
	cfg.RunID = "ma-oauth"
	cfg.OAuthProfiles = map[string]config.OAuthProfile{
		"src-prof": srcTok, "dst-prof": dstTok,
	}
	specs := []config.MailboxSpec{{
		Index: 1,
		Src: config.Endpoint{Host: "127.0.0.1", Port: h.src.Port(), Security: "none",
			User: "alice"},
		Dst: config.Endpoint{Host: "127.0.0.1", Port: h.dst.Port(), Security: "none",
			User: "bob"},
		SrcAuth: "src-prof", DstAuth: "dst-prof",
	}}
	res, err := engine.RunMigration(context.Background(), cfg, specs, engine.NewStats(),
		func(string) {}, func(config.MailboxSpec) func(string, ...any) {
			return func(string, ...any) {}
		})
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts["SUCCESS"] != 1 {
		t.Fatalf("OAuth + MULTIAPPEND migration: %v", res.Counts)
	}
	assertIntact(t, srcA, dstA, 20)
	if h.dst.MaxBatchSeen.Load() < 2 {
		t.Fatal("batching should be unaffected by the authentication mechanism")
	}
}

// newStaticTokenProfile writes a bearer token to a 0600 file and returns a
// delegated profile referencing it — no provider is ever contacted because
// a pre-provisioned access token needs no token endpoint.
func newStaticTokenProfile(t *testing.T, dir, name, token string) config.OAuthProfile {
	t.Helper()
	p := filepath.Join(dir, name+".token")
	if err := os.WriteFile(p, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	return config.OAuthProfile{
		Name: name, Provider: "generic", ClientID: "test-client",
		TokenURL: "https://token.example.test/oauth2/token", AccessToken: "file:" + p,
	}
}
