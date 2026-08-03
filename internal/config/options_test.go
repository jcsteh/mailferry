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

// TestCatalogWellFormed checks every option is usable: accessors present,
// enum options carry values, numeric options carry sane bounds, and each
// option round-trips a mid-range value through its own setter.
func TestCatalogWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, o := range Catalog() {
		if seen[o.Name()] {
			t.Fatalf("duplicate option %q", o.Name())
		}
		seen[o.Name()] = true
		if o.Get == nil || o.Set == nil {
			t.Fatalf("option %q missing accessors", o.Name())
		}
		r := Defaults()
		if o.Get(r) == nil {
			t.Fatalf("option %q Get returned nil", o.Name())
		}
		switch o.Kind {
		case KindEnum:
			if len(o.Enum) < 2 {
				t.Fatalf("enum option %q needs >=2 values", o.Name())
			}
			if !o.Set(r, o.Enum[len(o.Enum)-1]) {
				t.Fatalf("enum option %q rejected its own last value", o.Name())
			}
		case KindInt, KindFloat:
			if o.Max <= o.Min {
				t.Fatalf("numeric option %q has no usable range [%v,%v]", o.Name(), o.Min, o.Max)
			}
			mid := (o.Min + o.Max) / 2
			if o.Kind == KindInt {
				mid = float64(int(mid))
			}
			if !o.Set(r, mid) {
				t.Fatalf("numeric option %q rejected mid-range %v", o.Name(), mid)
			}
			if o.Set(r, o.Max+o.Step*100) {
				t.Fatalf("numeric option %q accepted an over-max value", o.Name())
			}
		case KindBool:
			if !o.Set(r, true) || !o.Set(r, false) {
				t.Fatalf("bool option %q rejected a bool", o.Name())
			}
		}
	}
}

// TestLoadTOMLUsesCatalog proves mailferry.toml values flow through the same
// option catalogue: valid values apply, out-of-range values are rejected
// with a warning and the built-in default stands (TOML > default).
func TestLoadTOMLUsesCatalog(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "mailferry.toml", `[network]
timeout_seconds = 45

[migration]
parallel_mailboxes = 250
`)
	cfg := Defaults()
	warns, _, _ := LoadTOML(cfg, p, false)
	if cfg.Timeout != 45 {
		t.Fatalf("valid timeout not applied: %v", cfg.Timeout)
	}
	// 250 is above the parallel_mailboxes max (100): default 10 must stand.
	if cfg.Workers != 10 {
		t.Fatalf("out-of-range workers should keep the default, got %d", cfg.Workers)
	}
	joined := strings.Join(warns, " | ")
	if !strings.Contains(joined, "parallel_mailboxes") || !strings.Contains(joined, "invalid") {
		t.Fatalf("expected an invalid-value warning, got %v", warns)
	}
}

func TestFindOption(t *testing.T) {
	if _, ok := FindOption("network.timeout_seconds"); !ok {
		t.Fatal("known option not found")
	}
	if _, ok := FindOption("nope.nope"); ok {
		t.Fatal("unknown option should not be found")
	}
}
