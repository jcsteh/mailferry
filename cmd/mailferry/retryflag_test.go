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

// CLI wiring for the runtime retry override and its deprecated alias.
package main

import (
	"strings"
	"testing"

	"github.com/ajsap/mailferry/v2/internal/config"
)

// TestRetryFailedFlagsMapToOneOption: the new flag and the deprecated alias
// must both drive the SAME configuration option, so F9 Settings can honestly
// report "CLI flag overrides this run" for either.
func TestRetryFailedFlagsMapToOneOption(t *testing.T) {
	const want = "recovery.skip_known_failed_messages"
	for _, flagName := range []string{"retry-failed", "no-skip-failed"} {
		got, ok := cliFlagToOption[flagName]
		if !ok {
			t.Fatalf("--%s is not mapped to a configuration option", flagName)
		}
		if got != want {
			t.Fatalf("--%s maps to %q, want %q", flagName, got, want)
		}
	}
}

// TestSkipKnownFailedOptionSemantics pins what the option means, since both
// flags and the TOML file share it: true excludes registry entries from the
// work queue, false admits them.
func TestSkipKnownFailedOptionSemantics(t *testing.T) {
	o, ok := config.FindOption("recovery.skip_known_failed_messages")
	if !ok {
		t.Fatal("recovery.skip_known_failed_messages missing from the catalogue")
	}
	r := config.Defaults()
	if !r.SkipKnownFailed {
		t.Fatal("failed messages must be excluded by DEFAULT — one poison message " +
			"must not stall every future run")
	}
	if !o.Set(r, "false") || r.SkipKnownFailed {
		t.Fatal("setting the option false must admit failed messages")
	}
	if !o.Set(r, "true") || !r.SkipKnownFailed {
		t.Fatal("setting the option true must exclude them again")
	}
	// The help text must point at the runtime flag, not just the command,
	// so an operator reading F9 learns both routes exist.
	if !strings.Contains(o.Help, "--retry-failed") {
		t.Fatalf("option help should mention the runtime flag: %q", o.Help)
	}
}

// TestDeprecatedAliasStillDocumented: the alias must remain discoverable and
// clearly marked deprecated for the v2.1.x series.
func TestDeprecatedAliasStillDocumented(t *testing.T) {
	if _, ok := cliFlagToOption["no-skip-failed"]; !ok {
		t.Fatal("--no-skip-failed must remain a working alias through v2.1.x")
	}
}
