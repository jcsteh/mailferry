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

package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ajsap/mailferry/v2/internal/config"
	"github.com/ajsap/mailferry/v2/internal/engine"
)

func settingsModel(t *testing.T, cfgPath string) *Model {
	t.Helper()
	done := make(chan struct{})
	m := New(engine.NewStats(), engine.NewBus(), func() {}, func() {}, time.Second, done)
	t.Cleanup(m.stopSysmon)
	cfg := config.Defaults()
	cfg.CSVFile = "mailboxes.csv"
	m.WithSettings(&SettingsCtx{Cfg: cfg, ConfigPath: cfgPath,
		CLISet: map[string]bool{"network.timeout_seconds": true}})
	m.active = vSettings
	m.snap = m.stats.Snapshot()
	return m
}

func TestSettingsShotRenders(t *testing.T) {
	// A tall render shows every category and the read-only runtime panel.
	m := settingsModel(t, "~/mailferry.toml")
	f := m.settingsView(124, 60)
	for _, want := range []string{"SETTINGS", "Migration", "Network & Retry", "Recovery",
		"Logging", "Interface", "State Database & Cluster", "Runtime", "read-only"} {
		if !strings.Contains(f, want) {
			t.Fatalf("settings frame lacks %q:\n%s", want, f)
		}
	}
	// The CLI-override note must be visible and honest.
	if !strings.Contains(f, "CLI flag overrides this run") {
		t.Fatalf("CLI-override indication missing:\n%s", f)
	}
	// The RenderShot path must also produce a frame without panicking.
	if s := RenderShot("settings"); !strings.Contains(s, "SETTINGS") {
		t.Fatalf("settings shot frame missing header:\n%s", s)
	}
}

func TestSettingsEditShotShowsUnsaved(t *testing.T) {
	f := RenderShot("settings-edit")
	if !strings.Contains(f, "unsaved change") {
		t.Fatalf("edited settings must indicate unsaved changes:\n%s", f)
	}
}

// TestSettingsToggleAndSavePersists drives the editor through the model's own
// key handler: cycle an enum, edit a number, save, and confirm the file.
func TestSettingsSaveWritesToml(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mailferry.toml")
	m := settingsModel(t, p)

	// Select "Compression" (an enum) and cycle it to a new value.
	o, _ := config.FindOption("network.compress")
	m.stagePending(o, "off")
	if len(m.set.pending) != 1 {
		t.Fatalf("staging a change should mark it pending, got %d", len(m.set.pending))
	}
	m.saveSettings()
	if m.set.invalid != "" {
		t.Fatalf("save reported error: %s", m.set.invalid)
	}
	if len(m.set.pending) != 0 {
		t.Fatal("pending changes should clear after a successful save")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("config file not written: %v", err)
	}
	if !strings.Contains(string(data), `compress = "off"`) {
		t.Fatalf("saved config missing the change:\n%s", data)
	}
}

// TestSettingsCancelDiscards proves Esc with pending changes discards them
// and writes nothing (zero side effects until an explicit save).
func TestSettingsCancelDiscards(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mailferry.toml")
	m := settingsModel(t, p)
	o, _ := config.FindOption("migration.parallel_mailboxes")
	m.stagePending(o, "42")
	if consumed := m.settingsKey("esc", nil); !consumed {
		t.Fatal("Esc with pending changes should be consumed by the editor")
	}
	if len(m.set.pending) != 0 {
		t.Fatal("Esc should discard pending changes")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("cancelling must not create or modify the config file")
	}
}

// TestSettingsInvalidRejected proves an out-of-range edit never stages and
// never corrupts anything.
func TestSettingsInvalidRejected(t *testing.T) {
	m := settingsModel(t, filepath.Join(t.TempDir(), "mailferry.toml"))
	o, _ := config.FindOption("migration.parallel_mailboxes")
	m.stagePending(o, "99999") // > max
	if len(m.set.pending) != 0 {
		t.Fatal("out-of-range value must not stage")
	}
	if m.set.invalid == "" {
		t.Fatal("an out-of-range edit should report a validation notice")
	}
}

// TestSettingsCLINotSilentlyPersisted proves a CLI-overridden value is not
// written to TOML unless the operator explicitly edits and saves it.
func TestSettingsCLINotSilentlyPersisted(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mailferry.toml")
	m := settingsModel(t, p)
	// network.timeout_seconds is CLI-overridden this run but unedited.
	m.saveSettings() // nothing pending
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("saving with no edits must not write the CLI value into TOML")
	}
}

// TestSettingsShowsMultiAppendDistinctFromWorkers: F9 must present the
// MULTIAPPEND controls as a Performance/Migration concern that an operator
// cannot mistake for concurrency. Workers and batch size are different
// things, and the screen has to say so.
func TestSettingsShowsMultiAppendDistinctFromWorkers(t *testing.T) {
	m := settingsModel(t, "~/mailferry.toml")
	f := m.settingsView(140, 80)
	for _, want := range []string{"MULTIAPPEND", "MULTIAPPEND batch size", "Parallel mailboxes"} {
		if !strings.Contains(f, want) {
			t.Fatalf("settings frame lacks %q:\n%s", want, f)
		}
	}
	// Both default to Auto and are editable through the same catalogue.
	mode, _ := config.FindOption("migration.multiappend")
	size, _ := config.FindOption("migration.multiappend_batch_size")
	if mode.FormatValue(m.settings.Cfg) != "auto" || size.FormatValue(m.settings.Cfg) != "auto" {
		t.Fatalf("both MULTIAPPEND settings should default to auto, got %q/%q",
			mode.FormatValue(m.settings.Cfg), size.FormatValue(m.settings.Cfg))
	}
	// Selecting the batch-size row and typing a valid count stages it; an
	// invalid one is rejected without staging anything.
	rows := settingsRows()
	for i, o := range rows {
		if o.Name() == "migration.multiappend_batch_size" {
			m.set.sel = i
		}
	}
	m.commitEdit(size, "16")
	if m.set.pending["migration.multiappend_batch_size"] != "16" {
		t.Fatalf("a valid batch size should stage: %v", m.set.pending)
	}
	m.commitEdit(size, "999")
	if m.set.pending["migration.multiappend_batch_size"] != "16" {
		t.Fatalf("an out-of-range batch size must not overwrite the staged value: %v",
			m.set.pending)
	}
	if m.set.invalid == "" {
		t.Fatal("an out-of-range batch size should explain itself")
	}
}

// TestRunModeIgnoresMultiAppend: batching is an optimisation, not a run
// mode. It must never appear in the primary mode line.
func TestRunModeIgnoresMultiAppend(t *testing.T) {
	cfg := config.Defaults()
	for _, v := range []string{"auto", "off"} {
		cfg.MultiAppend = v
		cfg.MultiAppendBatch = "20"
		mode := config.RunModeOf(cfg, false)
		if strings.Contains(strings.ToUpper(mode.Display()), "MULTIAPPEND") {
			t.Fatalf("MULTIAPPEND leaked into the mode line: %q", mode.Display())
		}
		if strings.Contains(mode.Machine(), "multiappend") {
			t.Fatalf("MULTIAPPEND leaked into the machine mode: %q", mode.Machine())
		}
		if mode.Display() != "Migration & Sync" {
			t.Fatalf("mode should be unchanged by batching: %q", mode.Display())
		}
	}
}

// TestMultiAppendVisibleOnRunSurfaces: an operator must be able to tell,
// without leaving the run screens, whether batching is available and whether
// it is working. The assertions below are deliberately about BEHAVIOUR — the
// status vocabulary comes from engine.MAValues (pinned by its own tests), so
// the views are checked for "shows the authoritative status" rather than for
// particular cosmetic wording.
func TestMultiAppendVisibleOnRunSurfaces(t *testing.T) {
	// The status the engine considers authoritative for the shot fixture.
	want := shotSnapshotForTest().MultiAppend()

	for _, view := range []string{"dashboard", "mailboxes", "performance"} {
		f := RenderShot(view)
		if !strings.Contains(f, "MULTIAPPEND") {
			t.Fatalf("%s does not surface MULTIAPPEND at all:\n%s", view, f)
		}
		// Whatever the engine says the state is, that is what the screen
		// must show — a view that invents its own wording would drift.
		if !strings.Contains(f, want.Compact()) && !strings.Contains(f, want.State()) {
			t.Fatalf("%s shows a status the engine does not report (%q / %q):\n%s",
				view, want.Compact(), want.State(), f)
		}
		// The optimisation must never be promoted into the Mode line.
		for _, line := range strings.Split(f, "\n") {
			if strings.Contains(line, "Mode") && strings.Contains(line, "MULTIAPPEND") {
				t.Fatalf("%s: MULTIAPPEND leaked into the Mode line: %q", view, line)
			}
		}
	}
}

// TestMailboxDetailSurfacesDestinationCapabilities: the per-mailbox detail is
// where "can this destination do X?" gets answered.
func TestMailboxDetailSurfacesDestinationCapabilities(t *testing.T) {
	f := RenderShot("mailboxes")
	snap := shotSnapshotForTest()
	var dst []string
	for _, mb := range snap.Mailboxes {
		if len(mb.Dst.Advertised) > 0 {
			dst = mb.Dst.Advertised
			break
		}
	}
	if len(dst) == 0 {
		t.Fatal("the screenshot fixture records no destination capabilities")
	}
	// Every notable capability the destination advertises must be visible.
	for _, c := range dst {
		if !isNotableCapability(c) {
			continue
		}
		if !strings.Contains(f, c) {
			t.Fatalf("mailbox detail omits advertised capability %q:\n%s", c, f)
		}
	}
}

// TestPerformanceReportsBatchingEffectiveness: the Performance view must
// report the EFFECT of batching (how many messages per command), not merely
// repeat the setting — otherwise it tells an operator nothing new.
func TestPerformanceReportsBatchingEffectiveness(t *testing.T) {
	ma := shotSnapshotForTest().MultiAppend()
	if ma.Commands == 0 {
		t.Skip("fixture has no batching activity to report")
	}
	f := RenderShot("performance")
	// The message and command totals are the substance; their exact framing
	// is presentation and is not asserted.
	for _, n := range []string{fmt.Sprint(ma.Messages), fmt.Sprint(ma.Commands)} {
		if !strings.Contains(f, n) {
			t.Fatalf("performance view omits batching figure %s:\n%s", n, f)
		}
	}
}

// TestNotableCapsDeterministic: the capability row must be stable between
// refreshes (fixed order, filtered) so it never flickers or dumps the
// server's entire vocabulary at the operator.
func TestNotableCapsDeterministic(t *testing.T) {
	in := []string{"ZZZ-UNKNOWN", "UIDPLUS", "IMAP4REV1", "MULTIAPPEND", "IDLE"}
	first := notableCaps(in)
	// Re-ordering the input must not re-order the output.
	shuffled := []string{"IDLE", "MULTIAPPEND", "IMAP4REV1", "UIDPLUS", "ZZZ-UNKNOWN"}
	if second := notableCaps(shuffled); second != first {
		t.Fatalf("capability row is input-order dependent: %q vs %q", first, second)
	}
	if strings.Contains(first, "ZZZ-UNKNOWN") {
		t.Fatalf("unknown capabilities must be filtered out: %q", first)
	}
	for _, want := range []string{"MULTIAPPEND", "UIDPLUS", "IDLE"} {
		if !strings.Contains(first, want) {
			t.Fatalf("notable capability %q was dropped: %q", want, first)
		}
	}
	// Order must follow the declared precedence, not the input.
	if idx := strings.Index(first, "MULTIAPPEND"); idx != 0 {
		t.Fatalf("MULTIAPPEND should lead the row: %q", first)
	}
	if notableCaps(nil) != "" {
		t.Fatal("no capabilities must render as nothing at all")
	}
}

// shotSnapshotForTest returns the same fixture snapshot RenderShot renders
// from, so assertions compare the screen against the engine's own view of
// the run instead of against duplicated literals.
func shotSnapshotForTest() engine.Snapshot {
	return shotStats(time.Now()).Snapshot()
}

// isNotableCapability reports whether c is one the detail row promises to
// show, so the test asserts the contract rather than a hard-coded list.
func isNotableCapability(c string) bool {
	for _, n := range notableCapabilities {
		if n == c {
			return true
		}
	}
	return false
}
