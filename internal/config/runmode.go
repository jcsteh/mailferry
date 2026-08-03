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

package config

import "strings"

// RunMode is the ONE authoritative description of a run's active
// characteristics. Every surface — startup banner, Dashboard, Results,
// headless summary, reports and (via the persisted run record) attach —
// renders from this, so no presentation layer independently guesses whether
// a run is a dry run, date-range-scoped, portable, and so on.
//
// Taxonomy (deliberate — not "every flag is a mode"):
//   - Primary: the operation itself ("Migration & Sync").
//   - Modifiers: behavioural changes significant enough that an operator must
//     see them at a glance — they change WHAT the run does or writes:
//     Dry Run (writes nothing), Date Range (selection window), Portable
//     (state beside the executable), Ephemeral (no persistent state), Backup
//     (re-applies changed flags to already-synced mail), Baseline (RFC-3501
//     conservative transport).
//   - Everything else (workers, timeouts, retries, compression, includes,
//     order, …) is ordinary tuning and never appears in the mode line.
type RunMode struct {
	Primary   string   // "Migration & Sync"
	Modifiers []string // active significant modifiers, in a stable order
}

// RunModeOf derives the authoritative run mode from the resolved config and
// whether portable mode is active (a process-global the caller supplies so
// this stays a pure function, testable for every combination).
func RunModeOf(r *Run, portable bool) RunMode {
	m := RunMode{Primary: "Migration & Sync"}
	add := func(s string) { m.Modifiers = append(m.Modifiers, s) }
	if r.DryRun {
		add("Dry Run")
	}
	if r.Range.Active {
		add("Date Range")
	}
	if portable {
		add("Portable")
	}
	// A dry run is always ephemeral by construction, so showing "Ephemeral"
	// alongside "Dry Run" would be redundant — only surface it on its own.
	if r.Ephemeral && !r.DryRun {
		add("Ephemeral")
	}
	if r.SyncFlags {
		add("Backup")
	}
	if r.Baseline {
		add("Baseline")
	}
	return m
}

// Label renders the mode joined by sep, e.g. "Migration & Sync · Dry Run".
// Presentation surfaces choose the separator that suits them (a middle dot
// in the TUI, a comma in plain text); the underlying data is identical.
func (m RunMode) Label(sep string) string {
	if len(m.Modifiers) == 0 {
		return m.Primary
	}
	return m.Primary + sep + strings.Join(m.Modifiers, sep)
}

// Display is the canonical TUI rendering (middle-dot separated).
func (m RunMode) Display() string { return m.Label(" · ") }

// Machine returns a stable, comma-joined, lower-case modifier list for
// reports and the persisted run record — never parsed from a decorative
// display string. Empty when only the primary operation is active.
func (m RunMode) Machine() string {
	if len(m.Modifiers) == 0 {
		return ""
	}
	keys := make([]string, len(m.Modifiers))
	for i, mod := range m.Modifiers {
		keys[i] = strings.ReplaceAll(strings.ToLower(mod), " ", "-")
	}
	return strings.Join(keys, ",")
}
