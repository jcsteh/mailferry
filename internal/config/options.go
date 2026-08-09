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

// The typed option catalogue: THE single source of truth for every
// operator-tunable configuration value. mailferry.toml loading, the
// interactive F9 Settings editor, `mailferry config` and validation all
// consume this one catalogue — there is deliberately no second list to
// drift out of sync. Credentials are never options: nothing in this
// catalogue reads or writes passwords, and a test enforces it.
package config

import (
	"fmt"
	"strconv"
	"strings"
)

// OptKind is the editing/validation type of an option.
type OptKind int

const (
	KindBool OptKind = iota
	KindInt
	KindFloat
	KindEnum
	KindString
)

// Option is one tunable value: its TOML address, its bounds, and its
// typed accessors on Run. Set validates and reports acceptance.
type Option struct {
	Section string
	Key     string
	Label   string // operator-facing name in F9
	Help    string // one-line description
	Kind    OptKind
	Min     float64 // numeric bounds (inclusive)
	Max     float64
	Step    float64 // F9 increment for +/- editing
	Enum    []string
	// LiveSafe: applying the value to the RUNNING process is safe
	// (display-cadence class). Everything else applies from the next run.
	LiveSafe bool

	Get func(*Run) any
	Set func(*Run, any) bool
}

// Name returns the canonical "section.key" address.
func (o Option) Name() string { return o.Section + "." + o.Key }

// numeric coerces TOML/edit values to float64.
func numeric(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

func boolean(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "yes", "on":
			return true, true
		case "false", "no", "off":
			return false, true
		}
	}
	return false, false
}

// intSetter returns a bounds-validated integer setter: min <= v <= max
// (inclusive), whole numbers only. The bounds passed here must equal the
// Option's Min/Max — opt() wires both from the same arguments.
func intSetter(min, max float64, dst func(*Run) *int) func(*Run, any) bool {
	return func(r *Run, v any) bool {
		f, ok := numeric(v)
		if !ok || f < min || (max > 0 && f > max) || f != float64(int(f)) {
			return false
		}
		*dst(r) = int(f)
		return true
	}
}

// floatSetter returns a bounds-validated float setter (inclusive bounds).
func floatSetter(min, max float64, dst func(*Run) *float64) func(*Run, any) bool {
	return func(r *Run, v any) bool {
		f, ok := numeric(v)
		if !ok || f < min || (max > 0 && f > max) {
			return false
		}
		*dst(r) = f
		return true
	}
}

func boolSetter(dst func(*Run) *bool) func(*Run, any) bool {
	return func(r *Run, v any) bool {
		b, ok := boolean(v)
		if !ok {
			return false
		}
		*dst(r) = b
		return true
	}
}

// FormatValue renders an option's current value the way the TOML file and
// the F9 editor display it.
func (o Option) FormatValue(r *Run) string {
	v := o.Get(r)
	switch x := v.(type) {
	case bool:
		return fmt.Sprint(x)
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprint(int64(x))
		}
		return fmt.Sprint(x)
	default:
		return fmt.Sprint(x)
	}
}

// opt builds one catalogue entry with bounds-aware setters.
func opt(section, key, label, help string, kind OptKind, min, max, step float64,
	enum []string, liveSafe bool, get func(*Run) any, set func(*Run, any) bool) Option {
	return Option{Section: section, Key: key, Label: label, Help: help, Kind: kind,
		Min: min, Max: max, Step: step, Enum: enum, LiveSafe: liveSafe, Get: get, Set: set}
}

// Catalog returns every tunable option, grouped in presentation order.
// Section order here IS the F9 category order.
func Catalog() []Option {
	var out []Option
	add := func(o Option) { out = append(out, o) }

	// ---- [migration] ---------------------------------------------------
	add(opt("migration", "parallel_mailboxes", "Parallel mailboxes",
		"Mailboxes migrated concurrently (CSV rows in flight at once)",
		KindInt, 1, 100, 1, nil, false,
		func(r *Run) any { return r.Workers },
		intSetter(1, 100, func(r *Run) *int { return &r.Workers })))
	add(opt("migration", "parallel_folders", "Parallel folders",
		"Extra folder pipelines inside one large mailbox (connection pairs)",
		KindInt, 1, 16, 1, nil, false,
		func(r *Run) any { return r.MaxConnsPerBox },
		intSetter(1, 16, func(r *Run) *int { return &r.MaxConnsPerBox })))
	add(opt("migration", "per_host_connections", "Per-host connections",
		"Simultaneous connections allowed per server host",
		KindInt, 1, 64, 1, nil, false,
		func(r *Run) any { return r.PerHostConns },
		intSetter(1, 64, func(r *Run) *int { return &r.PerHostConns })))
	add(opt("migration", "order", "Admission order",
		"Mailbox admission order: csv (file order) or size (largest first)",
		KindEnum, 0, 0, 0, []string{"csv", "size"}, false,
		func(r *Run) any { return r.Order },
		func(r *Run, v any) bool {
			s, ok := v.(string)
			if !ok || (s != "csv" && s != "size") {
				return false
			}
			r.Order = s
			return true
		}))
	// MULTIAPPEND is a transport optimisation, NOT concurrency. The labels
	// and help text below exist to make that impossible to confuse with
	// Workers ("Parallel mailboxes") above.
	add(opt("migration", "multiappend", "MULTIAPPEND",
		"Group messages into one IMAP append command when the destination supports it: auto or off",
		KindEnum, 0, 0, 0, []string{"auto", "off"}, false,
		func(r *Run) any { return normaliseMA(r.MultiAppend) },
		func(r *Run, v any) bool {
			s, ok := v.(string)
			// An empty value is not a setting. (An empty FIELD still means
			// "auto" — see normaliseMA — but an operator writing nothing is
			// a mistake worth reporting, not a silent default.)
			if !ok || strings.TrimSpace(s) == "" {
				return false
			}
			n := normaliseMA(s)
			if n == "" {
				return false
			}
			r.MultiAppend = n
			return true
		}))
	add(opt("migration", "multiappend_batch_size", "MULTIAPPEND batch size",
		"Messages grouped per append command: auto, or a fixed count (this is not Workers)",
		KindString, 0, 0, 0, nil, false,
		func(r *Run) any {
			if r.MultiAppendBatch == "" {
				return "auto"
			}
			return strings.ToLower(strings.TrimSpace(r.MultiAppendBatch))
		},
		func(r *Run, v any) bool {
			s := fmt.Sprint(v)
			if !ValidMultiAppendBatch(s) {
				return false
			}
			r.MultiAppendBatch = strings.ToLower(strings.TrimSpace(s))
			return true
		}))
	add(opt("migration", "skip_completed", "Skip completed",
		"Skip mailboxes already recorded SUCCESS in the State Database",
		KindBool, 0, 0, 0, nil, false,
		func(r *Run) any { return r.SkipCompleted },
		boolSetter(func(r *Run) *bool { return &r.SkipCompleted })))

	// ---- [network] -----------------------------------------------------
	add(opt("network", "timeout_seconds", "Inactivity timeout",
		"Per-connection inactivity watchdog in seconds",
		KindFloat, 20, 3600, 10, nil, false,
		func(r *Run) any { return r.Timeout },
		floatSetter(20, 3600, func(r *Run) *float64 { return &r.Timeout })))
	add(opt("network", "compress", "Compression",
		"COMPRESS=DEFLATE when the server offers it: auto or off",
		KindEnum, 0, 0, 0, []string{"auto", "off"}, false,
		func(r *Run) any { return r.Compress },
		func(r *Run, v any) bool {
			s, ok := v.(string)
			if !ok || (s != "auto" && s != "off") {
				return false
			}
			r.Compress = s
			return true
		}))

	// ---- [retry] -------------------------------------------------------
	add(opt("retry", "batch_attempts", "Batch attempts",
		"Attempts per batch level during failure isolation",
		KindInt, 1, 10, 1, nil, false,
		func(r *Run) any { return r.BatchAttempts },
		intSetter(1, 10, func(r *Run) *int { return &r.BatchAttempts })))
	add(opt("retry", "message_attempts", "Message attempts",
		"Transfer passes per message before it is recorded as failed",
		KindInt, 1, 10, 1, nil, false,
		func(r *Run) any { return r.MsgRetries },
		intSetter(1, 10, func(r *Run) *int { return &r.MsgRetries })))
	add(opt("retry", "reconnect_attempts", "Reconnect attempts",
		"Reconnect attempts per folder for ordinary connection trouble",
		KindInt, 1, 20, 1, nil, false,
		func(r *Run) any { return r.ReconnectAttempts },
		intSetter(1, 20, func(r *Run) *int { return &r.ReconnectAttempts })))
	add(opt("retry", "mailbox_attempts", "Mailbox attempts",
		"Whole-mailbox retry attempts after a hard failure",
		KindInt, 0, 10, 1, nil, false,
		func(r *Run) any { return r.Retries },
		intSetter(0, 10, func(r *Run) *int { return &r.Retries })))
	add(opt("retry", "retry_delay", "Retry delay",
		"Base delay in seconds between mailbox retries (doubles each attempt)",
		KindFloat, 1, 3600, 5, nil, false,
		func(r *Run) any { return r.RetryDelay },
		floatSetter(1, 3600, func(r *Run) *float64 { return &r.RetryDelay })))

	// ---- [recovery] ----------------------------------------------------
	add(opt("recovery", "isolate_failed_messages", "Isolate failed messages",
		"Progressively isolate messages that repeatedly break the transfer",
		KindBool, 0, 0, 0, nil, false,
		func(r *Run) any { return r.IsolateFailed },
		boolSetter(func(r *Run) *bool { return &r.IsolateFailed })))
	add(opt("recovery", "skip_known_failed_messages", "Skip known failed",
		"Exclude Failed Message Registry entries from the work queue (false = same as --retry-failed)",
		KindBool, 0, 0, 0, nil, false,
		func(r *Run) any { return r.SkipKnownFailed },
		boolSetter(func(r *Run) *bool { return &r.SkipKnownFailed })))
	add(opt("recovery", "stale_timeout_seconds", "Stale timeout",
		"Mark a mailbox stalled after this long without progress (0 disables)",
		KindFloat, 0, 86400, 30, nil, false,
		func(r *Run) any { return r.StaleTimeout },
		floatSetter(0, 86400, func(r *Run) *float64 { return &r.StaleTimeout })))
	add(opt("recovery", "recovery_retries", "Recovery retries",
		"Connection-recovery attempts per stall before Recovery Mode",
		KindInt, 1, 10, 1, nil, false,
		func(r *Run) any { return r.RecoveryRetries },
		intSetter(1, 10, func(r *Run) *int { return &r.RecoveryRetries })))
	add(opt("recovery", "recovery_interval_seconds", "Recovery interval",
		"Wait between connection-recovery attempts, in seconds",
		KindFloat, 1, 3600, 5, nil, false,
		func(r *Run) any { return r.RecoveryInterval },
		floatSetter(1, 3600, func(r *Run) *float64 { return &r.RecoveryInterval })))

	// ---- [logging] -----------------------------------------------------
	add(opt("logging", "level", "Log level",
		"info (default), debug (full tracebacks) or trace (wire protocol, redacted)",
		KindEnum, 0, 0, 0, []string{"info", "debug", "trace"}, false,
		func(r *Run) any {
			switch {
			case r.Trace:
				return "trace"
			case r.Debug:
				return "debug"
			}
			return "info"
		},
		func(r *Run, v any) bool {
			s, ok := v.(string)
			if !ok {
				return false
			}
			switch s {
			case "info":
				r.Debug, r.Trace = false, false
			case "debug":
				r.Debug, r.Trace = true, false
			case "trace":
				r.Trace = true
			default:
				return false
			}
			return true
		}))
	add(opt("logging", "keep_days", "Keep logs (days)",
		"Delete per-run logs older than this many days at startup (0 keeps all)",
		KindInt, 0, 3650, 1, nil, false,
		func(r *Run) any { return r.LogKeepDays },
		intSetter(0, 3650, func(r *Run) *int { return &r.LogKeepDays })))

	// ---- [dashboard] ---------------------------------------------------
	add(opt("dashboard", "refresh_ms", "Refresh interval (ms)",
		"Dashboard refresh cadence in milliseconds — display only",
		KindInt, 50, 5000, 50, nil, true,
		func(r *Run) any { return r.RefreshMS },
		intSetter(50, 5000, func(r *Run) *int { return &r.RefreshMS })))

	// ---- [database] ----------------------------------------------------
	add(opt("database", "heartbeat_seconds", "Heartbeat (s)",
		"Worker/lease heartbeat interval in seconds",
		KindFloat, 5, 300, 5, nil, false,
		func(r *Run) any { return r.DBHeartbeat },
		floatSetter(5, 300, func(r *Run) *float64 { return &r.DBHeartbeat })))
	add(opt("database", "worker_timeout_seconds", "Worker timeout (s)",
		"A cluster worker silent for this long is offline; its mailboxes are reclaimed",
		KindFloat, 20, 3600, 10, nil, false,
		func(r *Run) any { return r.WorkerTimeout },
		floatSetter(20, 3600, func(r *Run) *float64 { return &r.WorkerTimeout })))
	add(opt("database", "lock_timeout_seconds", "Lock timeout (s)",
		"Hard cap for any unexplained lease left in the State Database",
		KindFloat, 30, 7200, 30, nil, false,
		func(r *Run) any { return r.LockTimeout },
		floatSetter(30, 7200, func(r *Run) *float64 { return &r.LockTimeout })))
	return out
}

// FindOption looks an option up by its "section.key" address.
func FindOption(name string) (Option, bool) {
	for _, o := range Catalog() {
		if o.Name() == name {
			return o, true
		}
	}
	return Option{}, false
}
