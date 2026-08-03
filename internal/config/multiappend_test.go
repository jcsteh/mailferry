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

import (
	"strings"
	"testing"
)

// TestMultiAppendDefaults: batching is on and automatic out of the box, and
// the default batch is conservative rather than arbitrary.
func TestMultiAppendDefaults(t *testing.T) {
	r := Defaults()
	if r.MultiAppend != "auto" || r.MultiAppendBatch != "auto" {
		t.Fatalf("defaults should be auto/auto, got %q/%q", r.MultiAppend, r.MultiAppendBatch)
	}
	p := r.MultiAppendPolicyFor()
	if !p.Enabled || !p.Auto {
		t.Fatalf("default policy should be enabled + automatic: %+v", p)
	}
	if p.MaxCount < 2 || p.MaxCount > MABatchCountMax {
		t.Fatalf("default batch count %d is outside its own bounds", p.MaxCount)
	}
	if p.MaxBytes < MABatchByteMin || p.MaxBytes > MABatchByteMax {
		t.Fatalf("default byte budget %d is outside its own bounds", p.MaxBytes)
	}
	if p.SoloBytes <= 0 || p.SoloBytes >= p.MaxBytes {
		t.Fatalf("solo threshold %d must be a fraction of the byte budget", p.SoloBytes)
	}
	// Auto must never plan a batch wider than the source fetch pipeline.
	if p.MaxCount > r.FetchWindow {
		t.Fatalf("auto batch %d exceeds the fetch window %d", p.MaxCount, r.FetchWindow)
	}
}

// TestMultiAppendConcurrencyScaling: more concurrent pipelines means a
// smaller per-batch budget, because each pipeline holds its own open
// transaction on the destination.
func TestMultiAppendConcurrencyScaling(t *testing.T) {
	low := Defaults()
	low.Workers, low.MaxConnsPerBox, low.FetchWindow = 1, 1, 64
	high := Defaults()
	high.Workers, high.MaxConnsPerBox, high.FetchWindow = 32, 4, 64

	lp, hp := low.MultiAppendPolicyFor(), high.MultiAppendPolicyFor()
	if hp.MaxCount > lp.MaxCount {
		t.Fatalf("high concurrency must not get a larger count budget: %d > %d",
			hp.MaxCount, lp.MaxCount)
	}
	if hp.MaxBytes > lp.MaxBytes {
		t.Fatalf("high concurrency must not get a larger byte budget: %d > %d",
			hp.MaxBytes, lp.MaxBytes)
	}
	// Even at extreme concurrency the batch never collapses to something
	// useless — the floors are part of the contract.
	if hp.MaxCount < MABatchCountMin || hp.MaxBytes < MABatchByteMin {
		t.Fatalf("budgets fell below their documented floors: %+v", hp)
	}
}

// TestMultiAppendExplicitOverride: a fixed size is honoured and turns Auto
// off, but the byte guard still applies.
func TestMultiAppendExplicitOverride(t *testing.T) {
	r := Defaults()
	r.MultiAppendBatch = "25"
	p := r.MultiAppendPolicyFor()
	if p.Auto {
		t.Fatal("an explicit batch size must not report itself as automatic")
	}
	if p.MaxCount != 25 {
		t.Fatalf("explicit batch size ignored: %d", p.MaxCount)
	}
	if p.MaxBytes <= 0 {
		t.Fatal("the aggregate byte guard must still apply to an explicit size")
	}
	if !strings.Contains(p.Describe(), "25") {
		t.Fatalf("Describe should state the fixed size: %q", p.Describe())
	}
}

// TestMultiAppendOffAndDryRun: both must disable batching outright.
func TestMultiAppendOffAndDryRun(t *testing.T) {
	off := Defaults()
	off.MultiAppend = "off"
	if off.MultiAppendEnabled() || off.MultiAppendPolicyFor().Enabled {
		t.Fatal("multiappend=off must disable batching")
	}
	if !strings.Contains(off.MultiAppendPolicyFor().Describe(), "off") {
		t.Fatalf("Describe should say off: %q", off.MultiAppendPolicyFor().Describe())
	}
	dry := Defaults()
	dry.DryRun = true
	if dry.MultiAppendEnabled() {
		t.Fatal("a dry run writes nothing, so batching must be disabled")
	}
}

// TestMultiAppendOptionValidation: the CLI, the TOML file and F9 all go
// through these catalogue entries, so their bounds are the contract.
func TestMultiAppendOptionValidation(t *testing.T) {
	mode, ok := FindOption("migration.multiappend")
	if !ok {
		t.Fatal("migration.multiappend missing from the option catalogue")
	}
	size, ok := FindOption("migration.multiappend_batch_size")
	if !ok {
		t.Fatal("migration.multiappend_batch_size missing from the option catalogue")
	}
	r := Defaults()
	for _, good := range []string{"auto", "off", "on", "OFF"} {
		if !mode.Set(r, good) {
			t.Fatalf("multiappend should accept %q", good)
		}
	}
	for _, bad := range []string{"sometimes", "2", ""} {
		if mode.Set(r, bad) {
			t.Fatalf("multiappend should reject %q", bad)
		}
	}
	for _, good := range []string{"auto", "2", "100"} {
		if !size.Set(r, good) {
			t.Fatalf("batch size should accept %q", good)
		}
	}
	// 1 is not a batch, 0/negative are nonsense, and the cap is enforced.
	for _, bad := range []string{"0", "1", "-4", "101", "many"} {
		if size.Set(r, bad) {
			t.Fatalf("batch size should reject %q", bad)
		}
	}
}

// TestMultiAppendTerminologyIsUnambiguous: the operator-facing wording must
// never let batch size be mistaken for concurrency. This is a documented
// requirement, so it is tested rather than trusted.
func TestMultiAppendTerminologyIsUnambiguous(t *testing.T) {
	size, _ := FindOption("migration.multiappend_batch_size")
	workers, _ := FindOption("migration.parallel_mailboxes")
	if strings.Contains(strings.ToLower(size.Help), "worker") &&
		!strings.Contains(strings.ToLower(size.Help), "not workers") {
		t.Fatalf("batch-size help must not describe itself in worker terms: %q", size.Help)
	}
	if !strings.Contains(strings.ToLower(size.Help), "append command") {
		t.Fatalf("batch-size help must say what a batch IS: %q", size.Help)
	}
	if !strings.Contains(strings.ToLower(workers.Help), "concurrent") {
		t.Fatalf("workers help must say it is concurrency: %q", workers.Help)
	}
	if size.Label == workers.Label {
		t.Fatal("the two settings must not share a label")
	}
}

// TestMultiAppendTOMLRoundTrip: the documented template values load back
// through the same catalogue the editor uses.
func TestMultiAppendTOMLRoundTrip(t *testing.T) {
	if !strings.Contains(TOMLTemplate, "multiappend = \"auto\"") {
		t.Fatal("the configuration template must document multiappend")
	}
	if !strings.Contains(TOMLTemplate, "multiappend_batch_size = \"auto\"") {
		t.Fatal("the configuration template must document multiappend_batch_size")
	}
	tree, err := parseTOML(TOMLTemplate)
	if err != nil {
		t.Fatalf("template does not parse: %v", err)
	}
	r := Defaults()
	for _, key := range []string{"multiappend", "multiappend_batch_size"} {
		o, _ := FindOption("migration." + key)
		if !o.Set(r, tree["migration"][key]) {
			t.Fatalf("template value for %s is rejected by its own option", key)
		}
	}
}
