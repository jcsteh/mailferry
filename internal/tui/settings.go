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

// The interactive F9 Settings view. Every editable row comes from the ONE
// typed option catalogue (config.Catalog) — the same accessors, bounds and
// validation mailferry.toml loading uses, so the two can never drift.
//
// Contract (settled with the owner):
//   - viewing, navigating and cancelling edits NEVER touch the filesystem;
//     only an explicit Save (S) writes mailferry.toml — atomically, via
//     config.SaveValues, comments preserved;
//   - a CLI flag override for this invocation is shown as such and is NOT
//     silently persisted — saving writes only the values the operator
//     changed here;
//   - most settings apply from the next run (marked); display-cadence
//     settings apply live;
//   - runtime/cluster facts stay visible but clearly read-only;
//   - credentials are not options and can never appear here.
package tui

import (
	"fmt"
	"strings"

	"github.com/ajsap/mailferry/v2/internal/config"
)

// SettingsCtx wires the run configuration into the Settings editor.
type SettingsCtx struct {
	Cfg        *config.Run
	ConfigPath string          // the file Save writes (portable-aware: the loaded path)
	CLISet     map[string]bool // option names overridden by CLI flags this run
}

// WithSettings attaches the editor context (builder style, so New()'s
// signature and every existing caller stay unchanged).
func (m *Model) WithSettings(s *SettingsCtx) *Model {
	m.settings = s
	return m
}

// settingsState is the F9 editor's presentation state.
type settingsState struct {
	sel     int               // selected row (options only; headers skipped)
	editing bool              // text-edit mode for numeric/string rows
	buf     string            // edit buffer
	pending map[string]string // option name → new value (not yet saved)
	invalid string            // last rejected input, for the notice line
	saved   string            // last save confirmation
}

// settingsRows returns the catalogue in category order.
func settingsRows() []config.Option { return config.Catalog() }

func (m *Model) settingsOption() (config.Option, bool) {
	rows := settingsRows()
	if m.set.sel < 0 || m.set.sel >= len(rows) {
		return config.Option{}, false
	}
	return rows[m.set.sel], true
}

// pendingOrCurrent renders the row's value: a pending edit wins over the
// effective value of the running configuration.
func (m *Model) pendingOrCurrent(o config.Option) (string, bool) {
	if m.settings == nil {
		return "-", false
	}
	if v, ok := m.set.pending[o.Name()]; ok {
		return v, true
	}
	return o.FormatValue(m.settings.Cfg), false
}

// sectionTitle maps TOML sections to F9 category headings.
var sectionTitle = map[string]string{
	"migration": "Migration",
	"network":   "Network & Retry",
	"retry":     "Network & Retry",
	"recovery":  "Recovery",
	"logging":   "Logging",
	"dashboard": "Interface",
	"database":  "State Database & Cluster",
}

func (m *Model) settingsView(w, h int) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(clip(s, w) + "\n") }
	if m.settings == nil {
		line(cBold.Render(" SETTINGS"))
		line("")
		line(cDim.Render("  (settings editing is available during `mailferry run`)"))
		return b.String()
	}
	head := " SETTINGS"
	if n := len(m.set.pending); n > 0 {
		head += "   " + cYellow.Render(fmt.Sprintf("● %d unsaved change(s)", n))
	}
	line(cBold.Render(head) + cDim.Render("   "+m.settings.ConfigPath))
	line("")

	rows := settingsRows()
	if m.set.sel >= len(rows) {
		m.set.sel = len(rows) - 1
	}
	if m.set.sel < 0 {
		m.set.sel = 0
	}

	// visible window: keep the selection on screen, headers included
	// (runtime panel + notices take the last lines)
	avail := h - 8
	if avail < 6 {
		avail = 6
	}
	// build all body lines first, tracking which line the cursor is on
	var body []string
	cursorLine := 0
	lastSect := ""
	for i, o := range rows {
		if t := sectionTitle[o.Section]; t != lastSect {
			if lastSect != "" {
				body = append(body, "")
			}
			body = append(body, cCyan.Render(" "+t))
			lastSect = t
		}
		val, isPending := m.pendingOrCurrent(o)
		mark := "  "
		if isPending {
			mark = cYellow.Render("● ")
		}
		cursor := "  "
		if i == m.set.sel {
			cursor = cCyan.Render("▸ ")
		}
		valCell := val
		if i == m.set.sel && m.set.editing {
			valCell = m.set.buf + "▏"
		}
		note := ""
		if m.settings.CLISet[o.Name()] {
			note = cYellow.Render("  (CLI flag overrides this run)")
		} else if o.LiveSafe {
			note = cDim.Render("  (applies live)")
		}
		row := fmt.Sprintf("%s%s%-26s %-14s%s", cursor, mark,
			clip(o.Label, 26), clip(valCell, 14), note)
		if i == m.set.sel {
			body = append(body, row, cDim.Render("      "+clip(o.Help+
				editHint(o), w-8)))
			cursorLine = len(body) - 2
		} else {
			body = append(body, row)
		}
	}
	top := 0
	if cursorLine >= avail {
		top = cursorLine - avail + 1
	}
	for i := top; i < len(body) && i < top+avail; i++ {
		line(body[i])
	}

	// runtime facts — clearly read-only
	line("")
	line(cCyan.Render(" Runtime") + cDim.Render("  (read-only)"))
	paused := "running"
	if m.bus.Paused {
		paused = cYellow.Render("PAUSED")
	}
	line(cDim.Render(fmt.Sprintf("   migration %s · run %s · State Database %s",
		paused, m.snap.CSVFile, m.snap.DBPath)))
	line(cDim.Render(fmt.Sprintf("   worker %s · %d worker(s) on this State Database · logs %s",
		m.bus.WorkerID, len(m.bus.ClusterSnapshot()), m.snap.LogsDir)))

	switch {
	case m.set.invalid != "":
		line(cRed.Render("  " + m.set.invalid))
	case m.set.saved != "":
		line(cGreen.Render("  " + m.set.saved))
	case len(m.set.pending) > 0:
		line(cYellow.Render("  S saves the marked changes to mailferry.toml — Esc discards them"))
	default:
		line("")
	}
	return b.String()
}

// editHint explains how the selected row is edited, per kind.
func editHint(o config.Option) string {
	switch o.Kind {
	case config.KindBool:
		return "  ·  ⏎/Space toggles"
	case config.KindEnum:
		return "  ·  ⏎/Space cycles " + strings.Join(o.Enum, "/")
	default:
		if o.Max > o.Min {
			return fmt.Sprintf("  ·  ⏎ edits · +/- steps · range %v–%v", o.Min, o.Max)
		}
		return "  ·  ⏎ edits · +/- steps"
	}
}

// settingsKey handles every key press while F9 is active. Returns true
// when the key was consumed.
func (m *Model) settingsKey(key string, runes []rune) bool {
	if m.settings == nil {
		return false
	}
	o, okSel := m.settingsOption()

	// text-edit mode captures everything (so global keys like p/q/space
	// can be typed into values safely)
	if m.set.editing {
		switch key {
		case "enter":
			m.commitEdit(o, m.set.buf)
		case "esc":
			m.set.editing, m.set.buf = false, ""
		case "backspace":
			if len(m.set.buf) > 0 {
				m.set.buf = m.set.buf[:len(m.set.buf)-1]
			}
		default:
			if len(runes) == 1 && runes[0] >= ' ' {
				m.set.buf += string(runes)
			}
		}
		return true
	}

	switch key {
	case "up", "down", "pgup", "pgdown", "home", "end":
		m.set.invalid, m.set.saved = "", ""
		m.set.sel = clampNav(key, m.set.sel, len(settingsRows()))
		return true
	case "enter", " ":
		if !okSel {
			return true
		}
		m.set.invalid, m.set.saved = "", ""
		switch o.Kind {
		case config.KindBool:
			cur, _ := m.pendingOrCurrent(o)
			m.stagePending(o, map[string]string{"true": "false", "false": "true"}[cur])
		case config.KindEnum:
			cur, _ := m.pendingOrCurrent(o)
			next := o.Enum[0]
			for i, e := range o.Enum {
				if e == cur {
					next = o.Enum[(i+1)%len(o.Enum)]
				}
			}
			m.stagePending(o, next)
		default:
			cur, _ := m.pendingOrCurrent(o)
			m.set.editing, m.set.buf = true, cur
		}
		return true
	case "+", "=", "-":
		if !okSel || (o.Kind != config.KindInt && o.Kind != config.KindFloat) {
			return true
		}
		m.stepNumeric(o, key != "-")
		return true
	case "S", "s":
		m.saveSettings()
		return true
	case "esc", "q":
		if len(m.set.pending) > 0 {
			m.set.pending = map[string]string{}
			m.set.invalid, m.set.saved = "", "changes discarded — nothing was written"
			return true
		}
		return false // fall through to normal Esc/q navigation
	}
	return false
}

// stagePending validates the value against the catalogue and stages it —
// or reverts it when the edit lands back on the effective value.
func (m *Model) stagePending(o config.Option, v string) {
	scratch := *m.settings.Cfg
	if !o.Set(&scratch, v) {
		m.set.invalid = fmt.Sprintf("%s: %q is out of range — nothing staged", o.Label, v)
		return
	}
	if m.set.pending == nil {
		m.set.pending = map[string]string{}
	}
	if v == o.FormatValue(m.settings.Cfg) {
		delete(m.set.pending, o.Name()) // back to the current value: no change
	} else {
		m.set.pending[o.Name()] = v
	}
}

func (m *Model) commitEdit(o config.Option, v string) {
	m.set.editing, m.set.buf = false, ""
	m.stagePending(o, strings.TrimSpace(v))
}

// stepNumeric applies one +/- increment within the option's bounds.
func (m *Model) stepNumeric(o config.Option, up bool) {
	cur, _ := m.pendingOrCurrent(o)
	f := 0.0
	fmt.Sscanf(cur, "%g", &f)
	step := o.Step
	if step <= 0 {
		step = 1
	}
	if up {
		f += step
	} else {
		f -= step
	}
	if f < o.Min {
		f = o.Min
	}
	if o.Max > o.Min && f > o.Max {
		f = o.Max
	}
	if o.Kind == config.KindInt {
		m.stagePending(o, fmt.Sprint(int(f)))
	} else {
		m.stagePending(o, fmt.Sprint(f))
	}
}

// saveSettings persists the staged values via the atomic writer, then
// applies live-safe values to the running display.
func (m *Model) saveSettings() {
	if len(m.set.pending) == 0 {
		m.set.saved = "nothing to save"
		return
	}
	n := len(m.set.pending)
	if err := config.SaveValues(m.settings.ConfigPath, m.set.pending); err != nil {
		m.set.invalid = "save failed: " + err.Error() + " — mailferry.toml unchanged"
		return
	}
	// Live-apply the display-cadence class; everything else takes effect
	// on the next run (the file is the persistent source).
	for name, v := range m.set.pending {
		if o, okO := config.FindOption(name); okO && o.LiveSafe {
			o.Set(m.settings.Cfg, v)
			if name == "dashboard.refresh_ms" {
				m.refresh = refreshDur(m.settings.Cfg.RefreshMS)
			}
		}
	}
	m.set.pending = map[string]string{}
	m.set.invalid = ""
	m.set.saved = fmt.Sprintf("saved %d setting(s) to %s — persistent for future runs",
		n, m.settings.ConfigPath)
}
