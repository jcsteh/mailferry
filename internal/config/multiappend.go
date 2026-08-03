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

// The MULTIAPPEND batching policy — ONE authoritative implementation, used
// by the migration engine, validated by the option catalogue and reported by
// `mailferry config`. Nothing else decides how large a batch may be.
//
// TERMINOLOGY (deliberate, and never conflated):
//
//	Workers                 mailboxes migrated concurrently — CONCURRENCY
//	Parallel folders        folder pipelines inside one mailbox — CONCURRENCY
//	MULTIAPPEND batch size  messages grouped into ONE IMAP APPEND command
//
// A batch is not a worker, does not add concurrency, and never becomes a
// unit of durable accounting.
//
// ---------------------------------------------------------------------------
// WHY "Auto" IS SHAPED THE WAY IT IS
// ---------------------------------------------------------------------------
//
// RFC 3502 makes a MULTIAPPEND all-or-nothing: "if the append is
// unsuccessful ... no partial appending is permitted". That single fact
// drives the whole heuristic. A batch is an atomic unit of WORK (not of
// bookkeeping), so its size trades round trips saved against work redone
// when something goes wrong, and against how large a transaction the
// destination server is asked to hold open.
//
// Four independent limits therefore apply, and the smallest wins:
//
//  1. MESSAGE COUNT. Servers vary; nobody documents a hard MULTIAPPEND
//     limit, so MailFerry stays conservative. The count budget shrinks as
//     configured concurrency grows, because every concurrent destination
//     pipeline holds its own open transaction.
//
//  2. AGGREGATE BYTES. A hundred 4 KB messages and a hundred 50 MB messages
//     are not the same request. The byte budget bounds how much data one
//     atomic command carries — which bounds the cost of retrying it and the
//     size of the transaction the server must hold.
//
//  3. THE SOURCE PIPELINE. A batch may not be wider than the source FETCH
//     window: the destination command stays open while MailFerry waits for
//     each next body, and waiting on bodies that have not even been
//     requested would hold a command open for no reason.
//
//  4. LARGE MESSAGES GO ALONE. A message at or above a quarter of the byte
//     budget is uploaded by itself with an ordinary APPEND, so one big
//     message never drags smaller ones into its atomic failure domain.
//
// None of these bound CLIENT memory, because they do not need to: message
// bodies stream from the source socket to the destination socket in fixed
// chunks whether or not batching is on. Memory stays O(chunk), not
// O(batch).
package config

import (
	"fmt"
	"strconv"
	"strings"
)

// MULTIAPPEND policy constants. They are named, not inlined, so the
// documentation, the tests and `mailferry config` all quote the same values.
const (
	// MABatchCountBudget is divided by the configured concurrency to get the
	// per-batch message-count cap.
	MABatchCountBudget = 256
	MABatchCountMin    = 8
	MABatchCountMax    = 32

	// MABatchByteBudget is divided by the configured concurrency to get the
	// per-batch aggregate-byte cap. It bounds server-side transaction size
	// and retry cost — NOT client memory (bodies always stream).
	MABatchByteBudget = 256 << 20
	MABatchByteMin    = 4 << 20
	MABatchByteMax    = 32 << 20

	// MASoloDivisor: a message at or above byteCap/MASoloDivisor is appended
	// alone rather than batched.
	MASoloDivisor = 4

	// MABatchSizeMax is the largest explicit batch size an operator may set.
	MABatchSizeMax = 100
)

// MultiAppendPolicy is the resolved batching policy for one run.
type MultiAppendPolicy struct {
	Enabled   bool  // MULTIAPPEND may be used when the destination advertises it
	Auto      bool  // batch size is chosen automatically
	MaxCount  int   // messages per batch (>= 2 when Enabled)
	MaxBytes  int64 // aggregate literal bytes per batch
	SoloBytes int64 // a message this size or larger is never batched
}

// Describe renders the policy the way F9, logs and `mailferry config` show
// it — operator language, no implementation internals.
func (p MultiAppendPolicy) Describe() string {
	if !p.Enabled {
		return "off (every message uses a single APPEND)"
	}
	if p.Auto {
		return fmt.Sprintf("auto (up to %d messages or %s per command)",
			p.MaxCount, humanBytes(p.MaxBytes))
	}
	return fmt.Sprintf("%d messages per command (max %s)", p.MaxCount, humanBytes(p.MaxBytes))
}

// BatchLabel renders the configured batch sizing for status surfaces:
// "auto", or the explicit count as a plain number.
func (p MultiAppendPolicy) BatchLabel() string {
	if p.Auto {
		return "auto"
	}
	return fmt.Sprint(p.MaxCount)
}

// MultiAppendEnabled reports whether the run permits MULTIAPPEND at all.
// A dry run never does — it writes nothing to any server, so there is
// nothing to batch.
func (r *Run) MultiAppendEnabled() bool {
	return !r.DryRun && normaliseMA(r.MultiAppend) == "auto"
}

// MultiAppendPolicyFor resolves the batching policy from the run
// configuration. It is pure and deterministic, so the heuristic is tested
// directly rather than inferred from migration behaviour.
func (r *Run) MultiAppendPolicyFor() MultiAppendPolicy {
	p := MultiAppendPolicy{Enabled: r.MultiAppendEnabled()}
	if !p.Enabled {
		return p
	}
	// Concurrency = destination append pipelines this configuration may run
	// at once: mailboxes in flight × folder pipelines inside each.
	conc := r.Workers
	if conc < 1 {
		conc = 1
	}
	if r.MaxConnsPerBox > 1 {
		conc *= r.MaxConnsPerBox
	}
	p.MaxBytes = clamp64(int64(MABatchByteBudget/conc), MABatchByteMin, MABatchByteMax)
	p.SoloBytes = p.MaxBytes / MASoloDivisor

	if n, ok := explicitBatchSize(r.MultiAppendBatch); ok {
		p.MaxCount = n
		return p
	}
	p.Auto = true
	p.MaxCount = clampInt(MABatchCountBudget/conc, MABatchCountMin, MABatchCountMax)
	// Never group more messages than the source FETCH pipeline has in
	// flight — the destination command stays open while bodies arrive.
	if fw := r.FetchWindow; fw > 1 && p.MaxCount > fw {
		p.MaxCount = fw
	}
	return p
}

// explicitBatchSize parses a fixed batch size; ok=false means "auto".
func explicitBatchSize(v string) (int, bool) {
	s := strings.ToLower(strings.TrimSpace(v))
	if s == "" || s == "auto" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 2 || n > MABatchSizeMax {
		return 0, false
	}
	return n, true
}

// normaliseMA canonicalises the multiappend setting. "on"/"true"/"yes" are
// accepted as aliases of "auto": MailFerry has no way to force an
// unsupported server to accept MULTIAPPEND, and failing a migration over an
// optimisation would be wrong, so "force on" and "use when available" are
// the same behaviour and are spelled one way.
func normaliseMA(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "false", "no", "0", "disabled":
		return "off"
	case "", "auto", "on", "true", "yes", "1", "enabled":
		return "auto"
	}
	return "" // invalid
}

// ValidMultiAppendBatch reports whether v is an acceptable batch-size value
// ("auto" or 2..MABatchSizeMax).
func ValidMultiAppendBatch(v string) bool {
	s := strings.ToLower(strings.TrimSpace(v))
	if s == "auto" {
		return true
	}
	_, ok := explicitBatchSize(s)
	return ok
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clamp64(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// humanBytes renders a byte budget compactly (MiB granularity is enough).
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
