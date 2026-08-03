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

// The quit contract, and the live mailbox inspector.
//
// These are the two places where a wrong keystroke used to cost real work:
// Esc could end a migration, and the mailbox detail silently showed stale
// figures. Both are behaviour, so both are tested as behaviour.
package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ajsap/mailferry/v2/internal/engine"
)

// quitModel builds a model with a live-looking run.
func quitModel(t *testing.T, finished bool) *Model {
	t.Helper()
	done := make(chan struct{})
	stats := shotStats(time.Now())
	m := New(stats, engine.NewBus(), func() {}, func() {}, time.Second, done)
	t.Cleanup(m.stopSysmon)
	m.width, m.height = 124, 33
	m.snap = stats.Snapshot()
	m.finished = finished
	if finished {
		m.active = vResults
	}
	return m
}

func press(m *Model, key string) tea.Cmd {
	var msg tea.KeyMsg
	switch key {
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "ctrl+c":
		msg = tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	_, cmd := m.onKey(msg)
	return cmd
}

// TestEscNeverQuits is the headline guarantee: no amount of Esc, on any
// screen, may end MailFerry.
func TestEscNeverQuits(t *testing.T) {
	for _, finished := range []bool{false, true} {
		m := quitModel(t, finished)
		for _, view := range []int{vDashboard, vMailboxes, vLogs, vPerformance, vResults} {
			m.active = view
			for i := 0; i < 5; i++ {
				press(m, "esc")
				if m.quitting {
					t.Fatalf("Esc quit MailFerry (view %d, finished=%v)", view, finished)
				}
				if m.shuttingDown {
					t.Fatalf("Esc started a shutdown (view %d, finished=%v)", view, finished)
				}
				if m.confirmQuit {
					t.Fatalf("Esc must not even raise the quit dialog (view %d)", view)
				}
			}
		}
	}
}

// TestQuitAlwaysConfirms: q and Ctrl+C both raise the SAME gate, and neither
// exits on its own.
func TestQuitAlwaysConfirms(t *testing.T) {
	for _, key := range []string{"q", "ctrl+c"} {
		for _, finished := range []bool{false, true} {
			m := quitModel(t, finished)
			press(m, key)
			if !m.confirmQuit {
				t.Fatalf("%q (finished=%v) did not raise the quit confirmation", key, finished)
			}
			if m.quitting || m.shuttingDown {
				t.Fatalf("%q (finished=%v) exited without confirmation", key, finished)
			}
			// The dialog owns the screen.
			if !strings.Contains(m.View(), "Quit MailFerry?") {
				t.Fatalf("%q did not display the confirmation:\n%s", key, m.View())
			}
		}
	}
}

// TestQuitDialogCanBeDismissed: cancelling leaves the run entirely alone.
func TestQuitDialogCanBeDismissed(t *testing.T) {
	for _, dismiss := range []string{"esc", "n"} {
		m := quitModel(t, false)
		press(m, "q")
		press(m, dismiss)
		if m.confirmQuit {
			t.Fatalf("%q did not dismiss the quit dialog", dismiss)
		}
		if m.quitting || m.shuttingDown {
			t.Fatalf("%q ended the run instead of cancelling", dismiss)
		}
	}
}

// TestQuitConfirmedStopsGracefully: confirming a quit during a run must
// begin the graceful stop (cancelling the engine), not slam the door.
func TestQuitConfirmedStopsGracefully(t *testing.T) {
	cancelled := false
	done := make(chan struct{})
	stats := shotStats(time.Now())
	m := New(stats, engine.NewBus(), func() { cancelled = true }, func() {}, time.Second, done)
	t.Cleanup(m.stopSysmon)
	m.width, m.height = 124, 33
	m.snap = stats.Snapshot()

	press(m, "q")
	press(m, "y")
	if !cancelled {
		t.Fatal("confirming a quit must cancel the engine")
	}
	if !m.shuttingDown {
		t.Fatal("confirming a quit during a run must start the graceful stop")
	}
	if m.quitting {
		t.Fatal("a running migration must not exit before the graceful stop finishes")
	}
}

// TestQuitConfirmedAfterCompletionExits: with nothing left to stop, confirm
// leaves immediately.
func TestQuitConfirmedAfterCompletionExits(t *testing.T) {
	m := quitModel(t, true)
	press(m, "q")
	press(m, "enter") // Enter confirms, same as Y
	if !m.quitting {
		t.Fatal("a finished run should exit on confirmation")
	}
}

// TestCtrlCEscalates preserves the emergency path: repeated Ctrl+C still
// gets an operator out, it just passes through the confirmation first.
func TestCtrlCEscalates(t *testing.T) {
	forced := false
	done := make(chan struct{})
	stats := shotStats(time.Now())
	m := New(stats, engine.NewBus(), func() {}, func() { forced = true }, time.Second, done)
	t.Cleanup(m.stopSysmon)
	m.width, m.height = 124, 33
	m.snap = stats.Snapshot()

	press(m, "ctrl+c") // 1: confirm
	if !m.confirmQuit {
		t.Fatal("first Ctrl+C should raise the confirmation")
	}
	press(m, "ctrl+c") // 2: accept → graceful stop
	if !m.shuttingDown {
		t.Fatal("second Ctrl+C should begin the graceful stop")
	}
	press(m, "ctrl+c") // 3: force
	if !forced || !m.quitting {
		t.Fatal("third Ctrl+C should force an immediate exit")
	}
}

// TestQuitDialogExplainsSafety: an operator deciding whether to stop a
// migration must be told that completed work is already durable.
func TestQuitDialogExplainsSafety(t *testing.T) {
	m := quitModel(t, false)
	press(m, "q")
	v := m.View()
	for _, want := range []string{"State Database", "resumes"} {
		if !strings.Contains(v, want) {
			t.Fatalf("the running-run dialog must explain that stopping is safe (missing %q):\n%s",
				want, v)
		}
	}
}

// --- connection panel ------------------------------------------------------

// TestDashboardShowsConnectionPanel: the operator must be able to see what
// MailFerry actually learned about both servers, on both sides.
func TestDashboardShowsConnectionPanel(t *testing.T) {
	m := quitModel(t, false)
	m.active = vDashboard
	v := m.View()

	var lead engine.MBValues
	for _, mb := range m.snap.Mailboxes {
		if mb.Status == "RUNNING" {
			lead = mb
			break
		}
	}
	// Everything shown must be what the engine recorded for that mailbox.
	for _, want := range []string{
		"Source", "Destination", lead.Src.Host, lead.Dst.Host,
		lead.Src.ServerLabel(), lead.Dst.ServerLabel(),
		lead.Src.Auth, lead.Dst.Auth,
		tlsLabel(lead.Src.Security), tlsLabel(lead.Dst.Security),
	} {
		if want == "" || want == "-" {
			continue
		}
		if !strings.Contains(v, want) {
			t.Fatalf("the connection panel omits %q:\n%s", want, v)
		}
	}
}

// TestConnectionPanelSaysUnknownRatherThanGuessing: an unidentified server
// must be labelled Unknown on the Dashboard, not inferred from its hostname.
func TestConnectionPanelSaysUnknownRatherThanGuessing(t *testing.T) {
	m := quitModel(t, false)
	stats := shotStats(time.Now())
	// Strip every identification, leaving only hostnames behind.
	for _, mb := range stats.Snapshot().Mailboxes {
		stats.Mailbox(mb.Index, mb.Label, "", "", "").Set(func(v *engine.MBValues) {
			v.Src.Server, v.Dst.Server = "", ""
			v.Src.ServerVersion, v.Dst.ServerVersion = "", ""
		})
	}
	m.snap = stats.Snapshot()
	m.active = vDashboard
	v := m.View()
	if !strings.Contains(v, "Unknown") {
		t.Fatalf("an unidentified server must be shown as Unknown:\n%s", v)
	}
	// The hostnames are still on screen, but must not have produced a name.
	for _, guess := range []string{"Dovecot", "Axigen", "Exchange", "Gmail"} {
		if strings.Contains(v, guess) {
			t.Fatalf("a product name (%q) appeared with no supporting evidence:\n%s", guess, v)
		}
	}
}

// TestConnectionPanelAbsentBeforeConnecting: nothing is claimed before a
// connection has been made.
func TestConnectionPanelAbsentBeforeConnecting(t *testing.T) {
	done := make(chan struct{})
	stats := engine.NewStats()
	stats.Workers = 2
	m := New(stats, engine.NewBus(), func() {}, func() {}, time.Second, done)
	t.Cleanup(m.stopSysmon)
	m.width, m.height = 124, 33
	m.snap = stats.Snapshot()
	if lines := m.connectionPanel(60, 3, true); len(lines) != 0 {
		t.Fatalf("nothing has connected yet, so nothing should be claimed: %v", lines)
	}
}

// --- live mailbox inspector ------------------------------------------------

// TestMailboxInspectorIsLive is the point of the whole feature: the detail
// must reflect the CURRENT snapshot, not the one captured when Enter was
// pressed.
func TestMailboxInspectorIsLive(t *testing.T) {
	done := make(chan struct{})
	stats := shotStats(time.Now())
	m := New(stats, engine.NewBus(), func() {}, func() {}, time.Second, done)
	t.Cleanup(m.stopSysmon)
	m.width, m.height = 124, 40
	m.snap = stats.Snapshot()

	// Open the inspector on a running mailbox.
	var target engine.MBValues
	for _, mb := range m.snap.Mailboxes {
		if mb.Status == "RUNNING" {
			target = mb
			break
		}
	}
	if target.Label == "" {
		t.Fatal("fixture has no running mailbox")
	}
	m.openMailboxPopup(target)
	if m.popup == nil || m.popup.Live == "" {
		t.Fatal("the mailbox popup must be a LIVE inspector, not a frozen snapshot")
	}
	before := m.View()

	// The engine moves on, and the ticker refreshes the snapshot.
	stats.Mailbox(target.Index, target.Label, "", "", "").Set(func(v *engine.MBValues) {
		v.MsgsDone += 1234
		v.Op = "INBOX: FETCH->APPEND 9999/9999"
	})
	m.snap = stats.Snapshot()
	after := m.View()

	if before == after {
		t.Fatal("the inspector did not update when the snapshot changed")
	}
	if !strings.Contains(after, "9999/9999") {
		t.Fatalf("the inspector shows stale data after a snapshot change:\n%s", after)
	}
}

// TestMailboxInspectorShowsLiveFields: everything the operator asked to see
// must actually be present.
func TestMailboxInspectorShowsLiveFields(t *testing.T) {
	m := quitModel(t, false)
	var running engine.MBValues
	for _, mb := range m.snap.Mailboxes {
		if mb.Status == "RUNNING" {
			running = mb
			break
		}
	}
	lines := strings.Join(m.mailboxInspector(running.Label), "\n")
	for _, want := range []string{
		"Status", "Folders", "Messages", "Progress", "Throughput", "ETA",
		"Adopted", "Failed", "reconnects", "Elapsed",
		"Source", "Destination", "Server", "Auth", "TLS", "Speed",
		"MULTIAPPEND", "Operation",
	} {
		if !strings.Contains(lines, want) {
			t.Fatalf("the live inspector omits %q:\n%s", want, lines)
		}
	}
}

// TestMailboxInspectorSurvivesDisappearance: re-sorting, filtering or a
// vanished mailbox must not panic or show another mailbox's figures.
func TestMailboxInspectorSurvivesDisappearance(t *testing.T) {
	m := quitModel(t, false)
	lines := strings.Join(m.mailboxInspector("nobody@example.invalid"), "\n")
	if !strings.Contains(lines, "no longer in the run") {
		t.Fatalf("a missing mailbox must say so rather than showing someone else's data:\n%s", lines)
	}
}

// TestInspectorClosesOnEscOrEnterOnly: q must not close the inspector,
// because q means quit everywhere else.
func TestInspectorClosesOnEscOrEnterOnly(t *testing.T) {
	for _, key := range []string{"esc", "enter"} {
		m := quitModel(t, false)
		m.openMailboxPopup(m.snap.Mailboxes[0])
		press(m, key)
		if m.popup != nil {
			t.Fatalf("%q should return to the list", key)
		}
	}
	m := quitModel(t, false)
	m.openMailboxPopup(m.snap.Mailboxes[0])
	press(m, "q")
	if m.popup == nil {
		t.Fatal("q must not double as a close key inside the inspector")
	}
	if m.quitting {
		t.Fatal("q inside the inspector must not quit either")
	}
}
