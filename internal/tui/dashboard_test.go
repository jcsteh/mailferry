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

// Dashboard completion state and responsive layout.
//
// A Runtime that keeps counting after a migration has finished is not a
// cosmetic problem — it is the screen reporting something that is no longer
// true. And a completion time invented by the display would be a second
// answer to a question the reports have already answered.
package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ajsap/mailferry/v2/internal/engine"
	"github.com/ajsap/mailferry/v2/internal/util"
	"github.com/charmbracelet/lipgloss"
)

func dashModel(t *testing.T) *Model {
	t.Helper()
	done := make(chan struct{})
	stats := shotStats(time.Now())
	m := New(stats, engine.NewBus(), func() {}, func() {}, time.Second, done)
	t.Cleanup(m.stopSysmon)
	m.width, m.height = 124, 40
	m.snap = stats.Snapshot()
	m.active = vDashboard
	return m
}

// TestRuntimeFreezesOnCompletion: once the run is over the elapsed time must
// stop moving, and must equal the real duration rather than "now minus
// start".
func TestRuntimeFreezesOnCompletion(t *testing.T) {
	m := dashModel(t)

	// While running, the elapsed time tracks the clock.
	live1 := m.runElapsed()
	time.Sleep(15 * time.Millisecond)
	if live2 := m.runElapsed(); live2 <= live1 {
		t.Fatal("a running migration's Runtime must keep counting")
	}

	// The engine reports its authoritative completion instant.
	finished := m.snap.BatchStart.Add(37 * time.Minute)
	m.results = &ResultMsg{Res: engine.RunResult{
		Counts: map[string]int{}, FinishedAt: finished}}
	m.finished = true

	frozen := m.runElapsed()
	if want := (37 * time.Minute).Seconds(); frozen != want {
		t.Fatalf("Runtime = %.0fs, want the real duration %.0fs", frozen, want)
	}
	time.Sleep(15 * time.Millisecond)
	if again := m.runElapsed(); again != frozen {
		t.Fatalf("Runtime moved after completion: %.3f → %.3f", frozen, again)
	}
}

// TestCompletionTimestampIsTheReportedOne: the Dashboard must show the SAME
// instant the reporting subsystem uses, never one of its own.
func TestCompletionTimestampIsTheReportedOne(t *testing.T) {
	m := dashModel(t)
	if _, ok := m.runFinishedAt(); ok {
		t.Fatal("a running migration has no completion time")
	}

	finished := time.Date(2026, 8, 3, 21, 14, 37, 0, time.Local)
	m.results = &ResultMsg{Res: engine.RunResult{
		Counts: map[string]int{}, FinishedAt: finished}}
	m.finished = true

	got, ok := m.runFinishedAt()
	if !ok || !got.Equal(finished) {
		t.Fatalf("runFinishedAt = %v (%v), want the engine's FinishedAt %v", got, ok, finished)
	}

	// It must reach the screen, formatted by the shared stamp helper, with
	// seconds — an operator comparing logs needs them.
	v := m.View()
	stamp := util.FmtStamp(finished)
	if !strings.Contains(v, stamp) {
		t.Fatalf("the Dashboard does not show the completion time %q:\n%s", stamp, v)
	}
	if !strings.Contains(stamp, "37") {
		t.Fatalf("the completion stamp omits seconds: %q", stamp)
	}
	// And it must be labelled — "Completed" / "Interrupted" / "Finished".
	if !strings.Contains(v, m.stampLabel()) {
		t.Fatalf("the completion time is not labelled %q:\n%s", m.stampLabel(), v)
	}
}

// TestCompletionRowAbsentWhileRunning: nothing is claimed before the run
// actually ends.
func TestCompletionRowAbsentWhileRunning(t *testing.T) {
	m := dashModel(t)
	v := m.View()
	for _, never := range []string{"Completed ", "Interrupted "} {
		if strings.Contains(v, never) {
			t.Fatalf("a running migration must not show %q:\n%s", never, v)
		}
	}
}

// TestDashboardLayoutAcrossTerminalWidths: the Dashboard must stay readable
// from a cramped SSH window to a very wide desktop — no wrapping, no
// overflow, no lost information. Driven through the same renderer the
// screenshot tool uses, so what is tested is what ships.
func TestDashboardLayoutAcrossTerminalWidths(t *testing.T) {
	sizes := []struct {
		name       string
		cols, rows int
	}{
		{"narrow", 72, 24},
		{"small", 80, 24},
		{"standard", 100, 30},
		{"shot", ShotCols, ShotRows},
		{"wide", 160, 48},
		{"ultrawide", 220, 60},
	}
	for _, view := range []string{"dashboard", "dashboard-complete", "dashboard-mixed"} {
		for _, s := range sizes {
			v := RenderShotSize(view, s.cols, s.rows)

			for i, line := range strings.Split(v, "\n") {
				if n := lipgloss.Width(line); n > s.cols {
					t.Fatalf("%s/%s (%d cols): line %d overflows to %d cols:\n%q",
						view, s.name, s.cols, i, n, line)
				}
			}
			// The essentials survive at every width.
			for _, want := range []string{"CSV File", "Mode", "Runtime", "MULTIAPPEND"} {
				if !strings.Contains(v, want) {
					t.Fatalf("%s/%s: Dashboard lost %q:\n%s", view, s.name, want, v)
				}
			}
			// The connection panel appears whole or not at all — never a
			// heading with no body under it. The DETAILED panel's body is its
			// field labels; the AGGREGATED panel's body is its counts.
			aggregated := strings.Contains(v, "↓ Sources")
			if !aggregated && strings.Contains(v, "↓ Source") && !strings.Contains(v, "Server") {
				t.Fatalf("%s/%s: connection heading without its fields:\n%s", view, s.name, v)
			}
			if aggregated && !strings.Contains(v, "Unknown") {
				t.Fatalf("%s/%s: aggregated heading without its platform counts:\n%s",
					view, s.name, v)
			}
			// Both sides are always shown together.
			if strings.Contains(v, "↓ Source") != strings.Contains(v, "↑ Destination") {
				t.Fatalf("%s/%s: only one side of the connection panel rendered:\n%s",
					view, s.name, v)
			}
			// A completed run states its completion time at every width.
			if view == "dashboard-complete" && !strings.Contains(v, "Completed") {
				t.Fatalf("%s/%s: completed run does not say so:\n%s", view, s.name, v)
			}
		}
	}
}

// TestDashboardAdaptsPanelToSpace: the connection panel uses the room it
// has. Wide terminals get the full labelled two-column form; narrow ones get
// a condensed line per side; a short terminal gives the rows back to the
// mailbox list, which is the primary content.
func TestDashboardAdaptsPanelToSpace(t *testing.T) {
	countLines := func(v, label string) int {
		n := 0
		for _, line := range strings.Split(v, "\n") {
			if strings.Contains(line, label) {
				n++
			}
		}
		return n
	}

	// Wide: both sides share each labelled row.
	wide := RenderShotSize("dashboard", 140, 40)
	if countLines(wide, "Host") != 1 {
		t.Fatalf("a wide terminal should place both hosts on one row:\n%s", wide)
	}
	// The panel describes the CONNECTIONS: identity, how it authenticated,
	// the negotiated transport and what that link is doing right now.
	for _, want := range []string{"Host", "Server", "Auth", "TLS", "Speed"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("the wide panel omits the %q field:\n%s", want, wide)
		}
	}
	// It must NOT repeat the run's cumulative traffic — that is the
	// Sent/Rcvd row's job, and duplicating it invites contradiction.
	if strings.Contains(wide, "Sent/Rcvd") {
		if strings.Count(wide, "Sent/Rcvd") > 1 {
			t.Fatalf("cumulative traffic appears more than once:\n%s", wide)
		}
	}

	// Narrow but tall: condensed, one line per side, still carrying the facts.
	narrow := RenderShotSize("dashboard", 78, 40)
	if countLines(narrow, "↓ Source") != 1 || countLines(narrow, "↑ Destination") != 1 {
		t.Fatalf("a narrow terminal should render one line per side:\n%s", narrow)
	}
	for _, want := range []string{"Dovecot", "Axigen", "OAuth (Microsoft)", "Password"} {
		if !strings.Contains(narrow, want) {
			t.Fatalf("the condensed panel dropped %q:\n%s", want, narrow)
		}
	}

	// Short: the panel yields so the mailbox list still has room.
	short := RenderShotSize("dashboard", 124, 20)
	if strings.Contains(short, "↓ Source") {
		t.Fatalf("a short terminal should give the rows to the mailbox list:\n%s", short)
	}
	if !strings.Contains(short, "Mailbox") {
		t.Fatalf("the mailbox table must survive on a short terminal:\n%s", short)
	}
}

// TestStatisticsAreNotDuplicated: the Dashboard answers each question once.
// Cumulative traffic belongs to Sent/Rcvd; per-connection throughput belongs
// to the connection panel. Showing either in both places invites two figures
// that disagree.
func TestStatisticsAreNotDuplicated(t *testing.T) {
	v := RenderShotSize("dashboard", ShotCols, ShotRows)

	if n := strings.Count(v, "Sent/Rcvd"); n != 1 {
		t.Fatalf("cumulative traffic should appear exactly once, found %d:\n%s", n, v)
	}
	if strings.Contains(v, "Wire RX/TX") {
		t.Fatalf("the superseded Wire RX/TX row is still present:\n%s", v)
	}
	// The connection panel must not repeat cumulative byte totals: its rows
	// are Host / Server / Auth / TLS / Speed and nothing else.
	panel := connectionPanelLines(v)
	if len(panel) == 0 {
		t.Fatalf("no connection panel rendered:\n%s", v)
	}
	for _, line := range panel {
		for _, banned := range []string{"Sent", "Rcvd", "RX", "TX", "↓1", "↑1"} {
			if strings.Contains(line, banned) {
				t.Fatalf("the connection panel repeats cumulative traffic (%q): %q",
					banned, line)
			}
		}
	}
}

// TestConnectionPanelFields: each side reports the five connection facts,
// and Speed is a rate rather than a total.
func TestConnectionPanelFields(t *testing.T) {
	v := RenderShotSize("dashboard", ShotCols, ShotRows)
	for _, want := range []string{"Host", "Server", "Auth", "TLS", "Speed"} {
		if !strings.Contains(v, want) {
			t.Fatalf("the connection panel omits %q:\n%s", want, v)
		}
	}
	// A running connection reports a per-second figure on both sides.
	panel := strings.Join(connectionPanelLines(v), "\n")
	if strings.Count(panel, "/s") < 2 {
		t.Fatalf("both connections should report a throughput rate:\n%s", panel)
	}
	// TLS carries the version alone — the label already says TLS.
	if strings.Contains(panel, "TLS         TLS ") {
		t.Fatalf("the TLS value repeats its own label:\n%s", panel)
	}
}

// TestTLSLabel renders the transport honestly in every shape.
func TestTLSLabel(t *testing.T) {
	cases := map[string]string{
		"TLS 1.3":            "1.3",
		"TLS 1.2":            "1.2",
		"STARTTLS → TLS 1.2": "1.2 (STARTTLS)",
		"none":               "none (plaintext)",
		"":                   "-",
	}
	for in, want := range cases {
		if got := tlsLabel(in); got != want {
			t.Fatalf("tlsLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// connectionPanelLines extracts the rendered connection panel from a frame.
func connectionPanelLines(frame string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, "↓ Source") {
			in = true
		}
		if in {
			if strings.HasPrefix(strings.TrimSpace(line), "─") {
				break
			}
			out = append(out, line)
		}
	}
	return out
}

// connSide is shorthand for building one endpoint's connection facts.
type connSide struct {
	host, server, auth string
}

// heteroModel builds a model whose run touches the given connections. Caps
// is set on every side because that is the engine's mark of "actually
// connected" — the summary must never count servers MailFerry has not
// spoken to.
func heteroModel(t *testing.T, pairs [][2]connSide) *Model {
	t.Helper()
	done := make(chan struct{})
	stats := engine.NewStats()
	stats.Workers = 4
	for i, p := range pairs {
		mb := stats.Mailbox(i+1, fmt.Sprintf("user%d@example.org", i+1),
			p[0].host, p[1].host, fmt.Sprintf("user%d@example.net", i+1))
		src, dst := p[0], p[1]
		mb.Set(func(m *engine.MBValues) {
			m.Status = "RUNNING"
			m.Src.Host, m.Dst.Host = src.host, dst.host
			m.Src.Server, m.Dst.Server = src.server, dst.server
			m.Src.Auth, m.Dst.Auth = src.auth, dst.auth
			m.Src.Security, m.Dst.Security = "TLS 1.3", "TLS 1.3"
			m.Src.Caps = []string{"SSL"}
			m.Dst.Caps = []string{"SSL"}
		})
	}
	m := New(stats, engine.NewBus(), func() {}, func() {}, time.Second, done)
	t.Cleanup(m.stopSysmon)
	m.width, m.height = 124, 40
	m.snap = stats.Snapshot()
	m.active = vDashboard
	return m
}

// TestPanelSinglePlatformStaysDetailed: one platform per side keeps the full
// per-side panel — including when that platform spans several hosts, which
// is one platform, not several.
func TestPanelSinglePlatformStaysDetailed(t *testing.T) {
	m := heteroModel(t, [][2]connSide{
		{{"imap.example.com", "Exchange Online", "OAuth (Microsoft)"},
			{"mail.example.net", "Axigen", "Password"}},
	})
	v := m.View()
	for _, want := range []string{"↓ Source", "↑ Destination",
		"Exchange Online", "Axigen", "imap.example.com", "mail.example.net"} {
		if !strings.Contains(v, want) {
			t.Fatalf("detailed panel missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "↓ Sources") {
		t.Fatalf("a homogeneous run must not aggregate:\n%s", v)
	}

	// Same platform, three hosts: still detailed, host cell counts them.
	m = heteroModel(t, [][2]connSide{
		{{"a.example.com", "Dovecot", "Password"}, {"x.example.net", "Axigen", "Password"}},
		{{"b.example.com", "Dovecot", "Password"}, {"x.example.net", "Axigen", "Password"}},
		{{"c.example.com", "Dovecot", "Password"}, {"x.example.net", "Axigen", "Password"}},
	})
	v = m.View()
	if strings.Contains(v, "↓ Sources") {
		t.Fatalf("one platform across several hosts is still one platform:\n%s", v)
	}
	for _, want := range []string{"3 hosts", "Dovecot", "Axigen"} {
		if !strings.Contains(v, want) {
			t.Fatalf("multi-host detailed panel missing %q:\n%s", want, v)
		}
	}
}

// TestPanelHeterogeneousRunAggregates is the MSP case: different platforms
// in one run switch the panel to per-platform counts, because no single
// mailbox's details represent the run.
func TestPanelHeterogeneousRunAggregates(t *testing.T) {
	pairs := [][2]connSide{}
	add := func(n int, src, dst connSide) {
		for i := 0; i < n; i++ {
			pairs = append(pairs, [2]connSide{src, dst})
		}
	}
	// 5× Exchange Online (across two tenants' hosts), 3× Dovecot, 1 unknown.
	add(3, connSide{"t1.example.com", "Exchange Online", "OAuth (Microsoft)"},
		connSide{"ax.example.net", "Axigen", "Password"})
	add(2, connSide{"t2.example.com", "Exchange Online", "OAuth (Microsoft)"},
		connSide{"ax.example.net", "Axigen", "Password"})
	add(3, connSide{"dc.example.org", "Dovecot", "Password"},
		connSide{"ax.example.net", "Axigen", "Password"})
	add(1, connSide{"old.example.org", "", "Password"},
		connSide{"ax.example.net", "Axigen", "Password"})
	m := heteroModel(t, pairs)
	v := m.View()

	if !strings.Contains(v, "↓ Sources") || !strings.Contains(v, "↑ Destinations") {
		t.Fatalf("a heterogeneous run must aggregate:\n%s", v)
	}
	// Counts aggregate by PLATFORM: five Exchange mailboxes on two hosts are
	// one line, and the unidentified server is counted as Unknown.
	panel := strings.Join(connectionPanelLines(strings.ReplaceAll(v, "↓ Sources", "↓ Source")), "\n")
	for _, want := range []string{"Exchange Online", "Dovecot", "Unknown", "Axigen"} {
		if !strings.Contains(panel, want) {
			t.Fatalf("aggregated panel missing %q:\n%s", want, panel)
		}
	}
	for _, wantCount := range []string{"5", "3", "1", "9"} {
		if !strings.Contains(panel, wantCount) {
			t.Fatalf("aggregated panel missing count %s:\n%s", wantCount, panel)
		}
	}
	// Largest first, so the biggest platform leads the list.
	if strings.Index(panel, "Exchange Online") > strings.Index(panel, "Dovecot") {
		t.Fatalf("platforms should be ordered by count:\n%s", panel)
	}
	// No single mailbox's host may masquerade as the run's.
	if strings.Contains(panel, "t1.example.com") {
		t.Fatalf("the aggregated panel must not show a single mailbox's host:\n%s", panel)
	}
}

// TestPanelAggregationCorrectness pins the arithmetic directly.
func TestPanelAggregationCorrectness(t *testing.T) {
	m := heteroModel(t, [][2]connSide{
		{{"a.example.com", "Dovecot", "Password"}, {"g.example.net", "Gmail", "OAuth (Google)"}},
		{{"b.example.com", "Dovecot", "Password"}, {"g.example.net", "Gmail", "OAuth (Google)"}},
		{{"c.example.com", "Stalwart", "Password"}, {"h.example.net", "Axigen", "Password"}},
	})
	src := m.summariseSide(false)
	if src.count != 3 || src.platforms["Dovecot"] != 2 || src.platforms["Stalwart"] != 1 {
		t.Fatalf("source aggregation wrong: %+v", src.platforms)
	}
	dst := m.summariseSide(true)
	if dst.platforms["Gmail"] != 2 || dst.platforms["Axigen"] != 1 {
		t.Fatalf("destination aggregation wrong: %+v", dst.platforms)
	}
	// Deterministic order: count descending, then name.
	order := sortedPlatforms(map[string]int{"B": 2, "A": 2, "C": 5})
	if order[0].name != "C" || order[1].name != "A" || order[2].name != "B" {
		t.Fatalf("platform ordering unstable: %+v", order)
	}
}

// TestPanelMixedAuthShown: two auth mechanisms are listed; more collapse to
// "mixed" rather than overflowing the cell.
func TestPanelMixedAuthShown(t *testing.T) {
	m := heteroModel(t, [][2]connSide{
		{{"a.example.com", "Dovecot", "OAuth (Microsoft)"}, {"x.example.net", "Axigen", "Password"}},
		{{"b.example.com", "Dovecot", "Password"}, {"x.example.net", "Axigen", "Password"}},
	})
	v := m.View()
	if !strings.Contains(v, "OAuth (Microsoft) · Password") {
		t.Fatalf("two auth mechanisms should both be named:\n%s", v)
	}
	if got := joinedOrMixed(map[string]bool{"A": true, "B": true, "C": true}); got != "mixed" {
		t.Fatalf("three mechanisms should collapse to mixed, got %q", got)
	}
}

// TestPanelIgnoresUnconnectedMailboxes: a queued mailbox has produced no
// evidence and must not be counted — not even as Unknown.
func TestPanelIgnoresUnconnectedMailboxes(t *testing.T) {
	done := make(chan struct{})
	stats := engine.NewStats()
	stats.Workers = 2
	connected := stats.Mailbox(1, "user1@example.org", "a.example.com", "x.example.net", "u1@example.net")
	connected.Set(func(m *engine.MBValues) {
		m.Status = "RUNNING"
		m.Src.Host, m.Dst.Host = "a.example.com", "x.example.net"
		m.Src.Server, m.Dst.Server = "Dovecot", "Axigen"
		m.Src.Caps, m.Dst.Caps = []string{"SSL"}, []string{"SSL"}
	})
	queued := stats.Mailbox(2, "user2@example.org", "b.example.com", "x.example.net", "u2@example.net")
	queued.Set(func(m *engine.MBValues) { m.Status = "QUEUED" })
	m := New(stats, engine.NewBus(), func() {}, func() {}, time.Second, done)
	t.Cleanup(m.stopSysmon)
	m.width, m.height = 124, 40
	m.snap = stats.Snapshot()

	src := m.summariseSide(false)
	if src.count != 1 {
		t.Fatalf("only the connected mailbox should count, got %d", src.count)
	}
	if src.platforms["Unknown"] != 0 {
		t.Fatalf("a queued mailbox must not be reported Unknown: %+v", src.platforms)
	}
}

// TestDashboardDurationsAreClean: the elapsed figures must not read like
// debugging output.
func TestDashboardDurationsAreClean(t *testing.T) {
	if got := util.FmtDHMS(3600 + 32*60 + 5); got != "01:32:05" {
		t.Fatalf("a sub-day duration should be bare HH:MM:SS, got %q", got)
	}
	if got := util.FmtDHMS(86400 + 60); got != "1 day 00:01:00" {
		t.Fatalf("a one-day duration: %q", got)
	}
	if got := util.FmtDHMS(2*86400 + 60); got != "2 days 00:01:00" {
		t.Fatalf("a multi-day duration: %q", got)
	}
	if got := util.FmtDHMS(-5); got != "00:00:00" {
		t.Fatalf("a negative duration must clamp: %q", got)
	}
	m := dashModel(t)
	if strings.Contains(m.View(), "0 day") {
		t.Fatalf("the Dashboard still shows a zero day component:\n%s", m.View())
	}
}

// TestMailboxRuntimeFreezesToo: a finished mailbox's elapsed time must stop
// with the same discipline as the run's.
func TestMailboxRuntimeFreezesToo(t *testing.T) {
	m := dashModel(t)
	var done engine.MBValues
	for _, mb := range m.snap.Mailboxes {
		if !mb.End.IsZero() {
			done = mb
			break
		}
	}
	if done.Label == "" {
		t.Skip("fixture has no finished mailbox")
	}
	first := strings.Join(m.mailboxInspector(done.Label), "\n")
	time.Sleep(15 * time.Millisecond)
	second := strings.Join(m.mailboxInspector(done.Label), "\n")
	if first != second {
		t.Fatalf("a finished mailbox's detail is still moving:\n%s\n---\n%s", first, second)
	}
}
