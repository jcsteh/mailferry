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

import "testing"

func rangeActive() DateRange { return DateRange{Active: true} }

func TestRunModeCombinations(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(*Run)
		portable    bool
		wantDisplay string
		wantMachine string
	}{
		{"normal", func(r *Run) {}, false, "Migration & Sync", ""},
		{"dry-run", func(r *Run) { r.DryRun = true; r.Ephemeral = true }, false,
			"Migration & Sync · Dry Run", "dry-run"},
		{"date-range", func(r *Run) { r.Range = rangeActive() }, false,
			"Migration & Sync · Date Range", "date-range"},
		{"dry-run+date-range", func(r *Run) { r.DryRun = true; r.Ephemeral = true; r.Range = rangeActive() },
			false, "Migration & Sync · Dry Run · Date Range", "dry-run,date-range"},
		{"portable", func(r *Run) {}, true, "Migration & Sync · Portable", "portable"},
		{"backup", func(r *Run) { r.SyncFlags = true }, false, "Migration & Sync · Backup", "backup"},
		{"baseline", func(r *Run) { r.Baseline = true }, false, "Migration & Sync · Baseline", "baseline"},
		{"ephemeral-alone", func(r *Run) { r.Ephemeral = true }, false,
			"Migration & Sync · Ephemeral", "ephemeral"},
		{"all", func(r *Run) {
			r.DryRun = true
			r.Ephemeral = true
			r.Range = rangeActive()
			r.SyncFlags = true
			r.Baseline = true
		}, true, "Migration & Sync · Dry Run · Date Range · Portable · Backup · Baseline",
			"dry-run,date-range,portable,backup,baseline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Defaults()
			tc.mutate(r)
			m := RunModeOf(r, tc.portable)
			if got := m.Display(); got != tc.wantDisplay {
				t.Fatalf("Display = %q, want %q", got, tc.wantDisplay)
			}
			if got := m.Machine(); got != tc.wantMachine {
				t.Fatalf("Machine = %q, want %q", got, tc.wantMachine)
			}
		})
	}
}

// TestRunModeDryRunSuppressesEphemeral: a dry run is inherently ephemeral, so
// "Ephemeral" must not appear redundantly beside "Dry Run".
func TestRunModeDryRunSuppressesEphemeral(t *testing.T) {
	r := Defaults()
	r.DryRun = true
	r.Ephemeral = true // dry-run forces this
	m := RunModeOf(r, false)
	for _, mod := range m.Modifiers {
		if mod == "Ephemeral" {
			t.Fatalf("Ephemeral must be suppressed under Dry Run: %v", m.Modifiers)
		}
	}
}

// TestRunModeSeparatorFlexible: different surfaces can render different
// separators from the SAME underlying data.
func TestRunModeSeparatorFlexible(t *testing.T) {
	r := Defaults()
	r.DryRun = true
	r.Ephemeral = true
	m := RunModeOf(r, false)
	if got := m.Label(", "); got != "Migration & Sync, Dry Run" {
		t.Fatalf("comma separator: %q", got)
	}
	if got := m.Label(" · "); got != "Migration & Sync · Dry Run" {
		t.Fatalf("dot separator: %q", got)
	}
}
