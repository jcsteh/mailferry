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

// Documentation shot support (cmd/tuishot): renders populated View()
// frames for screenshots. Fixture data only — RFC-2606 example domains.
package tui

import (
	"fmt"
	"time"

	"github.com/ajsap/mailferry/v2/internal/config"
	"github.com/ajsap/mailferry/v2/internal/engine"
	"github.com/ajsap/mailferry/v2/internal/state"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// RenderShot returns one fully rendered frame of the named view using
// synthetic fixture data (RFC-2606 example domains only). Views:
// dashboard, workers, history, logs, results-success, results-warnings,
// results-nothing.
// ShotCols / ShotRows are the canonical screenshot dimensions.
const ShotCols, ShotRows = 124, 33

func RenderShot(view string) string { return RenderShotSize(view, ShotCols, ShotRows) }

// RenderShotSize renders a view at a specific terminal size. Layout has to
// hold up in a cramped SSH window as well as on a wide desktop, so both the
// screenshot tool and the layout tests drive it through here rather than
// each inventing their own harness.
func RenderShotSize(view string, cols, rows int) string {
	lipgloss.SetColorProfile(termenv.ANSI256)
	now := time.Now()
	stats := shotStats(now)

	bus := engine.NewBus()
	bus.WorkerID = "ferry:70210:a1b2c3d4"
	return renderShotWith(view, stats, bus, now, cols, rows)
}

// shotStats builds the synthetic fixture the screenshot views render from.
// It is a named function so tests can assert that a view displays what the
// ENGINE reports, rather than re-hard-coding expected presentation strings
// that would drift the moment the wording changed.
func shotStats(now time.Time) *engine.Stats {
	stats := engine.NewStats()
	stats.CSVFile = "mailboxes.csv"
	stats.DBPath = "./mailferry.db"
	stats.LogsDir = "./logs"
	stats.Workers = 4

	type fix struct {
		label, status, op string
		fldr, fldrTot     int
		done, total       int64
		bdone, btotal     int64
		failed            int64
	}
	rows := []fix{
		{"user1@example.org", "SUCCESS", "", 6, 6, 4211, 4211, 512 << 20, 512 << 20, 0},
		{"user2@example.org", "SUCCESS", "", 4, 4, 1874, 1874, 96 << 20, 96 << 20, 0},
		{"user3@example.org", "WARNINGS", "", 9, 9, 3409, 3417, 1207 << 20, 1210 << 20, 8},
		{"user4@example.org", "RUNNING", "INBOX: FETCH->APPEND 512/2210", 3, 11, 1642, 5804, 210 << 20, 730 << 20, 0},
		{"user5@example.org", "RUNNING", "Sent: FETCH->APPEND 88/410", 2, 7, 410, 2996, 66 << 20, 402 << 20, 0},
		{"user6@example.org", "REMOTE", "worker helm:70211", 0, 0, 902, 3120, 100 << 20, 350 << 20, 0},
		{"user7@example.org", "RETRYING", "reconnect 2/5", 1, 5, 204, 1450, 30 << 20, 190 << 20, 0},
		{"user8@example.org", "QUEUED", "", 0, 0, 0, 0, 0, 0, 0},
	}
	for i, r := range rows {
		mb := stats.Mailbox(i+1, r.label, "imap.example.com", "imap.example.net",
			"user"+fmt.Sprint(i+1)+"@example.net")
		st := r
		mb.Set(func(m *engine.MBValues) {
			m.Status = st.status
			m.Op = st.op
			m.FolderIndex = st.fldr
			m.FoldersTotal = st.fldrTot
			m.MsgsDone, m.MsgsTotal = st.done, st.total
			m.BytesDone, m.BytesTotal = st.bdone, st.btotal
			m.FailedMsgs = st.failed
			m.Start = now.Add(-time.Duration(25+i*7) * time.Minute)
			if st.status == "SUCCESS" || st.status == "WARNINGS" {
				m.End = now.Add(-time.Duration(i) * time.Minute)
			}
			m.Src.Host, m.Dst.Host = "imap.example.com", "imap.example.net"
			m.Src.ConnState, m.Dst.ConnState = "ready", "ready"
			m.Src.Caps = []string{"SSL", "UID+", "LIT+", "ZIP"}
			m.Dst.Caps = []string{"SSL", "UID+", "LIT+", "MA"}
			m.Src.Advertised = []string{"IMAP4REV1", "IDLE", "LITERAL+", "MOVE",
				"NAMESPACE", "UIDPLUS", "COMPRESS=DEFLATE"}
			m.Dst.Advertised = []string{"IMAP4REV1", "CONDSTORE", "IDLE", "LITERAL+",
				"MOVE", "MULTIAPPEND", "NAMESPACE", "QRESYNC", "SPECIAL-USE", "UIDPLUS"}
			m.MA = engine.MAValues{Enabled: true, BatchLabel: "auto", Probed: true,
				Supported: true, Commands: 61 + int64(i*7), Messages: 478 + int64(i*55),
				LastBatch: 8}
			// Identification as it would be learned from the live
			// connections — self-reported product names, never hostnames.
			m.Src.Server, m.Src.ServerVersion = "Dovecot", "2.3.21"
			m.Dst.Server, m.Dst.ServerVersion = "Axigen", "10.3"
			m.Src.Security, m.Dst.Security = "TLS 1.3", "STARTTLS → TLS 1.2"
			m.Src.Auth, m.Dst.Auth = "OAuth (Microsoft)", "Password"
			m.Src.RXBytes = st.bdone + st.bdone/9
			m.Dst.TXBytes = st.bdone + st.bdone/8
			m.Folder = "INBOX"
		})
	}
	stats.BatchStart = now.Add(-92 * time.Minute)
	return stats
}

// renderShotWith renders one view from a prepared fixture at a given size.
func renderShotWith(view string, stats *engine.Stats, bus *engine.Bus, now time.Time,
	cols, rows int) string {
	bus.SetCluster([]engine.WorkerInfo{
		{ID: "ferry:70210:a1b2c3d4", Host: "ferry.local", Status: "WORKING",
			Active: 2, Heartbeat: now.Add(-3 * time.Second), HBAge: 3,
			Started: now.Add(-92 * time.Minute)},
		{ID: "helm:70211:e5f6a7b8", Host: "helm.local", Status: "WORKING",
			Active: 1, Heartbeat: now.Add(-7 * time.Second), HBAge: 7,
			Started: now.Add(-31 * time.Minute)},
		{ID: "quay:1044:c9d0e1f2", Host: "quay.local", Status: "IDLE",
			Active: 0, Heartbeat: now.Add(-12 * time.Second), HBAge: 12,
			Started: now.Add(-8 * time.Minute)},
	})
	hist := []struct{ ev, st, mb, det string }{
		{"Run started", "OK", "-", "mailboxes.csv · 8 mailbox(es) · run 20260718-093012"},
		{"Worker joined", "OK", "-", "ferry:70210:a1b2c3d4 — cluster on ./mailferry.db"},
		{"Migration started", "OK", "user1@example.org", "user1 → user1@example.net"},
		{"Folder migrated", "OK", "user1@example.org", "INBOX: 2,210 msgs, 380.0 MB"},
		{"Worker joined", "OK", "-", "helm:70211:e5f6a7b8 — cluster on ./mailferry.db"},
		{"Stalled transfer detected", "WARN", "user4@example.org", "no progress for 5m 0s — verifying"},
		{"Connection recovery", "WARN", "user4@example.org", "attempt 1/3 — reconnecting from the last checkpoint"},
		{"Transfer recovered", "OK", "user4@example.org", "resumed after reconnect 1 — no data lost"},
		{"Stalled transfer detected", "WARN", "user3@example.org", "no progress for 5m 0s — verifying"},
		{"Entering Recovery Mode", "WARN", "user3@example.org", "isolate the failing batch, keep everything else moving"},
		{"Batch isolation", "WARN", "user3@example.org", "Archive: batch 8 → 4+4 → 2+2 → 1 (attempt 2/3)"},
		{"Failed message isolated", "WARN", "user3@example.org", "Archive UID 4188 — connection dies on APPEND"},
		{"Message recorded", "OK", "user3@example.org", "Failed Message Registry: CONNECTION_RESET · will be skipped"},
		{"Migration resumed", "OK", "user3@example.org", "Archive: continuing after UID 4188"},
		{"Completed with warnings", "WARN", "user3@example.org", "3,409 of 3,417 migrated · 8 in the registry (99.77%)"},
		{"Stale lock auto-reset", "OK", "user6@example.org", "quay:31022 last heartbeat 214s ago (> 90s) — that worker is dead; continuing"},
		{"Worker takeover", "WARN", "user6@example.org", "reclaimed from offline worker helm:70155 — resuming from the last checkpoint"},
		{"Folder migrated", "OK", "user2@example.org", "Sent: 512 msgs, 48.1 MB"},
	}
	for _, h := range hist {
		bus.History(h.ev, h.st, h.mb, h.det)
	}
	logsFix := []struct{ sev, mb, msg string }{
		{"INFO", "-", "cluster: joined as worker ferry:70210:a1b2c3d4 (offline threshold 60s)"},
		{"INFO", "user1@example.org", "plan: 6 folder(s), est 4,211 msgs"},
		{"INFO", "user1@example.org", "src: COMPRESS=DEFLATE enabled"},
		{"INFO", "user4@example.org", "INBOX: streaming FETCH->APPEND window 8"},
		{"WARN", "user4@example.org", "src: stalled — connection recovery 1/3"},
		{"INFO", "user4@example.org", "transfer recovered: INBOX resumed and completed (reconnect 1)"},
		{"WARN", "user3@example.org", "dst: APPEND NO — isolating (batch 8 → 4+4)"},
		{"WARN", "user3@example.org", "recovery mode — isolate problematic messages"},
		{"ERROR", "user3@example.org", "Archive UID 4188 failed permanently: CONNECTION_RESET"},
		{"INFO", "user3@example.org", "registry: 8 message(s) recorded — future runs skip them"},
		{"INFO", "user6@example.org", "lease lost — mailbox taken over by another worker; stopping local work"},
		{"INFO", "user2@example.org", "done: src=1874 dst=1874 synced=1874 adopted=0 copied=1874"},
		{"INFO", "-", "migration paused"},
		{"INFO", "-", "migration resumed"},
	}
	for i := 0; i < 2; i++ {
		for _, l := range logsFix {
			bus.Log(l.sev, l.mb, l.msg)
		}
	}

	done := make(chan struct{})
	m := New(stats, bus, func() {}, func() {}, 250*time.Millisecond, done)
	m.width, m.height = cols, rows
	m.snap = stats.Snapshot()
	m.lastRates = [2]float64{9.4 * 1024 * 1024, 41}
	switch view {
	case "workers":
		m.active = vWorkers
	case "dashboard-mixed":
		// A heterogeneous MSP run: several source platforms in one batch, so
		// the connection panel switches to the aggregated per-platform form.
		platforms := []struct{ name, ver, host string }{
			{"Exchange Online", "15.20", "t1.example.com"},
			{"Exchange Online", "15.20", "t2.example.com"},
			{"Exchange Online", "15.20", "t2.example.com"},
			{"Dovecot", "2.3.21", "dc.example.org"},
			{"Dovecot", "2.3.21", "dc.example.org"},
			{"SmarterMail", "", "sm.example.org"},
			{"", "", "old.example.org"}, // declined to identify itself
			{"Dovecot", "2.3.21", "dc2.example.org"},
		}
		for i, mb := range m.snap.Mailboxes {
			p := platforms[i%len(platforms)]
			stats.Mailbox(mb.Index, mb.Label, "", "", "").Set(func(v *engine.MBValues) {
				v.Src.Host, v.Src.Server, v.Src.ServerVersion = p.host, p.name, p.ver
				if p.name == "Exchange Online" {
					v.Src.Auth = "OAuth (Microsoft)"
				}
			})
		}
		m.snap = stats.Snapshot()
		m.active = vDashboard
	case "dashboard-complete":
		// The Dashboard AFTER the run: Runtime frozen, completion time from
		// the engine's authoritative FinishedAt. The fixture's start is
		// pinned to that instant minus the run duration so the frozen
		// Runtime is the real arithmetic rather than a coincidence.
		finalizeShotStats(stats, "results-success", now)
		m.results = shotResult("results-success")
		stats.BatchStart = m.results.Res.FinishedAt.Add(
			-time.Duration(m.results.Runtime) * time.Second)
		m.snap = stats.Snapshot()
		m.finished = true
		m.active = vDashboard
	case "mailboxes":
		// F3 with a running mailbox selected, so the expanded detail row —
		// including the destination's MULTIAPPEND state and notable
		// capabilities — is part of the captured frame.
		m.active = vMailboxes
		m.mailSel = 3
	case "performance":
		m.active = vPerformance
	case "history":
		m.active = vHistory
	case "logs":
		m.active = vLogs
	case "errors", "errors-mixed":
		finalizeShotStats(stats, "results-warnings", now)
		m.snap = stats.Snapshot()
		m.finished = true
		m.results = shotResult("results-warnings")
		if view == "errors-mixed" {
			shotRunErrors(bus) // mixed: run-level errors AND registry entries
		} else {
			// registry-only: a fresh bus with no run-level errors, so the
			// view shows the honest "no run-level errors recorded" wording
			// beside the outstanding Failed Message Registry.
			m.bus = engine.NewBus()
			m.bus.WorkerID = "ferry:70210:a1b2c3d4"
		}
		m.active = vErrors
	case "settings":
		m.snap = stats.Snapshot()
		m.settings = shotSettings()
		m.active = vSettings
	case "settings-edit":
		m.snap = stats.Snapshot()
		m.settings = shotSettings()
		m.active = vSettings
		// pre-stage an edit so the capture shows the unsaved-change state
		if o, ok := config.FindOption("migration.parallel_mailboxes"); ok {
			m.stagePending(o, "20")
		}
		m.set.sel = 0
	case "results-success", "results-warnings", "results-nothing", "results-dryrun":
		finalizeShotStats(stats, view, now)
		m.snap = stats.Snapshot()
		m.finished = true
		m.resultsShown = true
		m.results = shotResult(view)
		m.active = vResults
	default:
		m.active = vDashboard
	}
	primeShotRates(m)
	frame := m.View()
	m.stopSysmon()
	return frame
}

// primeShotRates feeds the PER-CONNECTION rate trackers two samples so the
// screenshots show real throughput figures instead of dashes. A rate needs
// two observations by definition, and a single fixture snapshot cannot
// produce one.
//
// Only the per-side trackers are touched: the fixture sets the run-wide rate
// and message rate deliberately, and driving those through the sampler would
// overwrite curated figures with arithmetic from synthetic byte counts.
func primeShotRates(m *Model) {
	const srcPerSec, dstPerSec = 12 << 20, 11 << 20 // ~12 / ~11 MB per second
	then := m.snap.TS.Add(-time.Second)
	for _, mb := range m.snap.Mailboxes {
		if mb.Status != "RUNNING" && mb.Status != "RETRYING" {
			continue // an idle connection genuinely has no throughput
		}
		if m.mbSrcWire[mb.Index] == nil {
			m.mbSrcWire[mb.Index] = engine.NewRateTracker()
			m.mbDstWire[mb.Index] = engine.NewRateTracker()
		}
		if m.mbWire[mb.Index] == nil {
			m.mbWire[mb.Index] = engine.NewRateTracker()
			m.mbPay[mb.Index] = engine.NewRateTracker()
		}
		src := mb.Src.RXBytes + mb.Src.TXBytes
		dst := mb.Dst.RXBytes + mb.Dst.TXBytes
		m.mbSrcWire[mb.Index].Update(then, sub0(src, srcPerSec), 0)
		m.mbSrcWire[mb.Index].Update(m.snap.TS, src, 0)
		m.mbDstWire[mb.Index].Update(then, sub0(dst, dstPerSec), 0)
		m.mbDstWire[mb.Index].Update(m.snap.TS, dst, 0)
		// The per-mailbox Speed column reads mbWire/mbPay; prime them with
		// the same story so the table and the connection panel agree — a
		// screenshot showing 0 B/s rows under a 36 MB/s panel would look
		// like a bug, and would be one.
		w := src
		if dst > w {
			w = dst
		}
		m.mbWire[mb.Index].Update(then, sub0(w, srcPerSec), 0)
		m.mbWire[mb.Index].Update(m.snap.TS, w, 0)
	}
}

func sub0(v, d int64) int64 {
	if v < d {
		return 0
	}
	return v - d
}

// finalizeShotStats converts the live fixture into a finished batch for
// the Results-view screenshots. Fictional data only.
func finalizeShotStats(stats *engine.Stats, view string, now time.Time) {
	for i, mb := range stats.Mailboxes {
		i := i
		mb.Set(func(v *engine.MBValues) {
			v.Op, v.Detail, v.Error = "", "", ""
			if v.MsgsTotal == 0 {
				v.MsgsTotal = int64(900 + 331*i)
				v.BytesTotal = int64(120<<20 + i*33<<20)
			}
			v.MsgsDone, v.BytesDone = v.MsgsTotal, v.BytesTotal
			v.FolderIndex, v.FoldersTotal = maxI(v.FoldersTotal, 4), maxI(v.FoldersTotal, 4)
			v.Status = "SUCCESS"
			v.FailedMsgs, v.Skipped = 0, 0
			if v.Start.IsZero() {
				v.Start = now.Add(-time.Duration(80+i*9) * time.Minute)
			}
			v.End = now.Add(-time.Duration(i) * time.Minute)
			if view == "results-dryrun" {
				// Dry run: every message WOULD be copied — counted as Planned,
				// nothing appended/adopted. Identity: done = planned.
				v.Appended, v.Adopted, v.Planned = 0, 0, v.MsgsTotal
				v.MsgsDone = v.MsgsTotal
				v.Src.RXBytes = int64(40<<10 + i*11<<10)
				v.Dst.TXBytes = 0 // dry run writes nothing to the destination
				return
			}
			switch view {
			case "results-nothing":
				v.Appended, v.Adopted = 0, v.MsgsTotal
				v.Start = now.Add(-time.Duration(25+i*3) * time.Second)
				v.End = now.Add(-time.Duration(i) * time.Second)
				// verification pass only: tiny wire traffic, no uploads
				v.Src.RXBytes = int64(38<<10 + i*9<<10)
				v.Dst.TXBytes = int64(2<<10 + i<<10)
			case "results-warnings":
				v.Appended, v.Adopted = v.MsgsTotal, 0
				if i == 2 { // user3: completed with warnings, 8 in registry
					v.Status = "WARNINGS"
					v.FailedMsgs = 8
					v.MsgsDone = v.MsgsTotal - 8
				}
			default:
				v.Appended, v.Adopted = v.MsgsTotal, 0
			}
			// Engine invariant (also asserted by tests): synced messages
			// are exactly the copied plus the adopted ones. Failed
			// messages are never inside MsgsDone.
			v.Appended = v.MsgsDone - v.Adopted
		})
	}
}

func maxI(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// shotSettings builds a fictional F9 editor context (RFC-2606 data only).
func shotSettings() *SettingsCtx {
	cfg := config.Defaults()
	cfg.CSVFile = "mailboxes.csv"
	return &SettingsCtx{
		Cfg:        cfg,
		ConfigPath: "~/Library/Application Support/MailFerry/mailferry.toml",
		CLISet:     map[string]bool{"network.timeout_seconds": true},
	}
}

// shotRunErrors injects a couple of run-level errors so the F6 capture can
// show run-level Errors & Warnings alongside the Failed Message Registry.
func shotRunErrors(bus *engine.Bus) {
	bus.Log("WARN", "user4@example.org", "src: stalled — connection recovery 1/3")
	bus.Log("ERROR", "user3@example.org", "Archive UID 4188 failed permanently: CONNECTION_RESET")
}

// shotResult builds the fixture ResultMsg for the Results screenshots.
func shotResult(view string) *ResultMsg {
	// A fixed fictional completion instant keeps the screenshots and the
	// render tests deterministic (never "now").
	finishedAt := time.Date(2026, 7, 18, 10, 42, 17, 0, time.Local)
	r := &ResultMsg{
		RunID:      "20260718-093012-4f2a",
		WorkerID:   "ferry:70210",
		ResultsCSV: "./logs/results.csv",
		SessionLog: "./logs/session.log",
		Runtime:    5520,
		Res:        engine.RunResult{Counts: map[string]int{}, FinishedAt: finishedAt},
	}
	switch view {
	case "results-nothing":
		r.Runtime = 9
	case "results-dryrun":
		r.Runtime = 14
		r.DryRun = true
		r.Modes = []string{"Dry Run", "Date Range"}
		r.RangeLabel = "2026-07-01 → 2026-07-18"
	case "results-warnings":
		r.FailedCSV = "./logs/failed_messages.csv"
		r.Res.Outstanding = 8
		mk := func(folder string, uid uint32, subj, sender, ftype, reason string, size int64) state.FailedRow {
			return state.FailedRow{Mailbox: "user3@example.org", Folder: folder,
				SrcUID: uid, Subject: subj, Sender: sender, FType: ftype,
				Reason: reason, Size: size, FailCount: 3, Status: "FAILED"}
		}
		r.Res.FailedRegistry = []state.FailedRow{
			mk("Archive", 4188, "=?utf-8?B?UsOpc3Vtw6kgb2YgUTMgcGxhbnM=?=", "pat@example.com",
				"CONNECTION_RESET", "connection dies on APPEND", 24<<20),
			mk("Archive", 4191, "Large design bundle (print masters)", "kim@example.com",
				"APPEND_NO", "APPEND: NO APPEND failed", 31<<20),
			mk("Archive", 4204, "=?utf-8?Q?Invoice_=E2=80=93_July?=", "billing@example.net",
				"APPEND_NO", "APPEND: NO APPEND failed", 240<<10),
			mk("Archive", 4207, "Weekly report w29", "ops@example.net",
				"APPEND_NO", "APPEND: NO APPEND failed", 88<<10),
			mk("Archive", 4211, "Site photos — final set", "site@example.org",
				"APPEND_NO", "APPEND: NO APPEND failed", 18<<20),
			mk("INBOX", 5102, "Contract counter-signed", "legal@example.net",
				"APPEND_NO", "APPEND: NO APPEND failed", 3<<20),
			mk("INBOX", 5188, "=?utf-8?Q?Re:_Freight_=E2=80=93_ETA?=", "cargo@example.net",
				"APPEND_NO", "APPEND: NO APPEND failed", 55<<10),
			mk("INBOX", 5201, "Renovation quote v4", "quotes@example.com",
				"APPEND_NO", "APPEND: NO APPEND failed", 5<<20),
		}
	}
	return r
}
