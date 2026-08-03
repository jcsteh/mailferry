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

// ONE way out.
//
// Every interactive exit path — `q`, Ctrl+C, Esc on the Results screen —
// funnels through requestQuit and the same confirmation dialog. Nothing else
// in the TUI may call tea.Quit for an operator-initiated exit.
//
// Why this is worth a file of its own: a migration is long-running work
// against somebody's mail, and a single mistyped keystroke used to be able
// to end it. Esc in particular is muscle memory for "go back" and must never
// mean "abandon the run". Confirming costs one keypress; not confirming has
// cost operators hours.
//
// SIGHUP is deliberately NOT routed here. A terminal that has gone away
// cannot answer a dialog, and stopping gracefully on hangup is a settled
// behaviour (v2.0.3). External SIGINT/SIGTERM to a headless process are
// likewise non-interactive stop requests, not questions.
package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// requestQuit raises the confirmation. It never exits by itself.
func (m *Model) requestQuit() {
	m.popup = nil // a modal must not sit underneath the dialog
	m.searching = false
	m.confirmQuit = true
}

// doQuit acts on a confirmed quit. With the engine still running this begins
// the graceful stop (the existing shutdown dialog then owns the screen);
// with the run already finished it simply leaves.
func (m *Model) doQuit() (tea.Model, tea.Cmd) {
	m.confirmQuit = false
	if m.finished {
		m.quitting = true
		m.stopSysmon()
		return m, tea.Quit
	}
	m.shuttingDown = true
	m.shutdownAt = timeNow()
	m.shutPhase = 0
	if m.cancel != nil {
		m.cancel()
	}
	return m, nil
}

// confirmQuitView renders the single quit confirmation. What it says depends
// on whether work is still in flight, because that is the only thing the
// operator actually needs to weigh.
func (m *Model) confirmQuitView(w, h int) string {
	inner := 62
	if inner > w-6 {
		inner = w - 6
	}
	var lines []string
	add := func(s string) { lines = append(lines, s) }

	add(cBold.Render(center("Quit MailFerry?", inner)))
	add("")
	if m.finished {
		add(center("The migration has finished.", inner))
		add("")
		add(cDim.Render(center("Results are recorded in the State Database", inner)))
		add(cDim.Render(center("and written to the run reports.", inner)))
	} else {
		running := m.runningCount()
		what := "A migration is still running."
		if running > 1 {
			what = plural(running, "mailbox is", "mailboxes are") + " still migrating."
		}
		add(cYellow.Render(center(what, inner)))
		add("")
		add(center("Quitting stops MailFerry.", inner))
		add("")
		// The reassurance that matters: stopping is safe, and why.
		add(cDim.Render(center("Everything already completed is safely recorded in the", inner)))
		add(cDim.Render(center("State Database. Re-running the same command resumes from", inner)))
		add(cDim.Render(center("exactly where this run stops — no mail is lost, and", inner)))
		add(cDim.Render(center("nothing is copied twice.", inner)))
	}
	add("")
	add(center(cGreen.Render("[Y] Quit")+"    "+
		cCyan.Render("[N] Keep going")+"    "+cDim.Render("Esc cancels"), inner))

	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).
		Width(inner).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}

// runningCount is how many mailboxes are actively migrating right now.
func (m *Model) runningCount() int {
	n := 0
	for _, mb := range m.snap.Mailboxes {
		if mb.Status == "RUNNING" || mb.Status == "RETRYING" {
			n++
		}
	}
	return n
}

// plural renders "1 mailbox is" / "4 mailboxes are".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa(n) + " " + many
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
