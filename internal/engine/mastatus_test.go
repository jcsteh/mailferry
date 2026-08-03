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

// The MULTIAPPEND status vocabulary is a small closed set of phrases an
// operator learns once, so it is pinned by tests rather than left to drift.
package engine

import (
	"strings"
	"testing"
)

func TestMAStatusVocabulary(t *testing.T) {
	cases := []struct {
		name string
		v    MAValues
		want string
	}{
		{"configuration off", MAValues{}, "Disabled"},
		{"on, nothing probed yet",
			MAValues{Enabled: true, BatchLabel: "auto"}, "Enabled (Auto)"},
		{"probed, server cannot",
			MAValues{Enabled: true, BatchLabel: "auto", Probed: true},
			"Not supported by destination"},
		{"probed, server can, idle",
			MAValues{Enabled: true, BatchLabel: "auto", Probed: true, Supported: true},
			"Ready (Auto)"},
		{"batching now",
			MAValues{Enabled: true, BatchLabel: "auto", Probed: true, Supported: true,
				Commands: 10, Messages: 78, LastBatch: 8}, "Active (Auto)"},
		{"explicit batch size",
			MAValues{Enabled: true, BatchLabel: "16", Probed: true, Supported: true,
				Commands: 4, Messages: 64}, "Active (Batch Size: 16)"},
		{"server misbehaved",
			MAValues{Enabled: true, BatchLabel: "auto", Probed: true, Supported: true,
				Demoted: true}, "Fallback (server rejected batches)"},
	}
	for _, c := range cases {
		if got := c.v.State(); got != c.want {
			t.Fatalf("%s: State() = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestMADetailCarriesRuntimeFigures: the detail form must add the numbers an
// operator asked for — how many commands, the average, the most recent size.
func TestMADetailCarriesRuntimeFigures(t *testing.T) {
	v := MAValues{Enabled: true, BatchLabel: "auto", Probed: true, Supported: true,
		Commands: 10, Messages: 78, LastBatch: 8}
	d := v.Detail()
	for _, want := range []string{"Active (Auto)", "10 cmd", "avg 7.8/cmd", "last 8"} {
		if !strings.Contains(d, want) {
			t.Fatalf("Detail() = %q, missing %q", d, want)
		}
	}
	// Idle profiles must not invent figures (and must not divide by zero).
	idle := MAValues{Enabled: true, BatchLabel: "auto", Probed: true, Supported: true}
	if strings.Contains(idle.Detail(), "avg") {
		t.Fatalf("an idle profile must not report an average: %q", idle.Detail())
	}
}

// TestMACompactStaysShort: the Dashboard column is narrow, so the compact
// form must fit without the caller truncating something meaningful away.
func TestMACompactStaysShort(t *testing.T) {
	for _, v := range []MAValues{
		{},
		{Enabled: true, BatchLabel: "auto"},
		{Enabled: true, BatchLabel: "auto", Probed: true},
		{Enabled: true, BatchLabel: "auto", Probed: true, Supported: true},
		{Enabled: true, BatchLabel: "auto", Probed: true, Supported: true,
			Commands: 684, Messages: 5364, LastBatch: 8},
		{Enabled: true, BatchLabel: "16", Probed: true, Supported: true,
			Commands: 4, Messages: 64},
	} {
		if got := v.Compact(); len(got) > 26 {
			t.Fatalf("Compact() = %q (%d chars) — too wide for the Dashboard column",
				got, len(got))
		}
	}
}

// TestSnapshotMultiAppendAggregation: a mixed run must report what is really
// happening rather than averaging it into something meaningless.
func TestSnapshotMultiAppendAggregation(t *testing.T) {
	// One destination batches, another cannot.
	snap := Snapshot{Mailboxes: []MBValues{
		{MA: MAValues{Enabled: true, BatchLabel: "auto", Probed: true, Supported: true,
			Commands: 5, Messages: 40, LastBatch: 8}},
		{MA: MAValues{Enabled: true, BatchLabel: "auto", Probed: true}},
	}}
	got := snap.MultiAppend()
	if !got.Enabled || !got.Supported || got.Commands != 5 || got.Messages != 40 {
		t.Fatalf("aggregate lost information: %+v", got)
	}
	if got.State() != "Active (Auto)" {
		t.Fatalf("a run where batching IS happening must say so: %q", got.State())
	}

	// Every destination probed, none supports it.
	none := Snapshot{Mailboxes: []MBValues{
		{MA: MAValues{Enabled: true, BatchLabel: "auto", Probed: true}},
		{MA: MAValues{Enabled: true, BatchLabel: "auto", Probed: true}},
	}}
	if s := none.MultiAppend().State(); s != "Not supported by destination" {
		t.Fatalf("all-unsupported run: %q", s)
	}

	// Configuration off everywhere.
	off := Snapshot{Mailboxes: []MBValues{{}, {}}}
	if s := off.MultiAppend().State(); s != "Disabled" {
		t.Fatalf("disabled run: %q", s)
	}

	// A run with no mailboxes must not panic or claim anything.
	if s := (Snapshot{}).MultiAppend().State(); s != "Disabled" {
		t.Fatalf("empty run: %q", s)
	}
}

// TestMAIsNotAccounting guards the boundary that matters: MULTIAPPEND
// telemetry must never leak into the statistics identity.
func TestMAIsNotAccounting(t *testing.T) {
	snap := Snapshot{Mailboxes: []MBValues{{
		MsgsDone: 100, Appended: 60, Adopted: 40,
		MA: MAValues{Enabled: true, Probed: true, Supported: true, Commands: 8, Messages: 60},
	}}}
	a := snap.Agg()
	if a.MsgsDone != a.Appended+a.Adopted+a.PriorDone+a.Planned {
		t.Fatalf("statistics identity broken: %+v", a)
	}
	if a.MsgsDone != 100 || a.Appended != 60 {
		t.Fatalf("batch telemetry altered message accounting: %+v", a)
	}
}
