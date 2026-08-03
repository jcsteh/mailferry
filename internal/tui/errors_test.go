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
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestErrorsViewRegistryTruthfulStatus(t *testing.T) {
	f := RenderShot("errors")
	// Two clearly separated sections.
	if !strings.Contains(f, "ERRORS & WARNINGS") || !strings.Contains(f, "FAILED MESSAGE REGISTRY") {
		t.Fatalf("both sections must be present:\n%s", f)
	}
	// With outstanding failures the status must NEVER claim all clear.
	if strings.Contains(f, "all clear") {
		t.Fatalf("must not say 'all clear' while failures are outstanding:\n%s", f)
	}
	if !strings.Contains(f, "outstanding") {
		t.Fatalf("status should mention outstanding failures:\n%s", f)
	}
	// Zero run-level errors must read honestly, not contradict the registry.
	if !strings.Contains(f, "no run-level errors recorded") {
		t.Fatalf("expected honest 'no run-level errors' wording:\n%s", f)
	}
	// RFC 2047 subject decoded in the detail panel; raw encoded word absent.
	if strings.Contains(f, "=?utf-8?") {
		t.Fatalf("encoded word must be decoded, not shown raw:\n%s", f)
	}
	// Retry guidance is discoverable.
	if !strings.Contains(f, "retry-failed") {
		t.Fatalf("retry guidance missing:\n%s", f)
	}
}

func TestErrorsViewMixed(t *testing.T) {
	f := RenderShot("errors-mixed")
	// Both run-level errors AND registry entries → status names both.
	if !strings.Contains(f, "run-level error") {
		t.Fatalf("mixed view should count run-level errors:\n%s", f)
	}
	if !strings.Contains(f, "recorded") {
		t.Fatalf("run-level errors header should show a recorded count:\n%s", f)
	}
}

// TestErrorsViewNoCredentialLeak: the F6 view renders only registry metadata
// (mailbox/folder/uid/subject/reason) — never anything password-shaped.
func TestErrorsViewNoCredentialLeak(t *testing.T) {
	for _, v := range []string{"errors", "errors-mixed"} {
		f := RenderShot(v)
		for _, bad := range []string{"password", "Bearer ", "auth=", "refresh_token"} {
			if strings.Contains(strings.ToLower(f), strings.ToLower(bad)) {
				t.Fatalf("%s view leaked %q:\n%s", v, bad, f)
			}
		}
	}
}

// TestErrorsViewResponsive: the redesign must render without panicking or
// producing over-wide lines at narrow and short terminals.
func TestErrorsViewResponsive(t *testing.T) {
	m := shotModelForErrors(t)
	m.results = shotResult("results-warnings")
	for _, dim := range [][2]int{{72, 18}, {80, 24}, {100, 20}, {160, 50}} {
		w, h := dim[0], dim[1]
		out := m.errorsView(w, h)
		for i, ln := range strings.Split(out, "\n") {
			if lipgloss.Width(ln) > w {
				t.Fatalf("errors view line %d exceeds width %d at %dx%d:\n%q", i, w, w, h, ln)
			}
		}
	}
}
