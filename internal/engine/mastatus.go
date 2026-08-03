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

package engine

import "fmt"

// MAValues is the operator-facing MULTIAPPEND picture for one mailbox.
//
// It answers the only questions an operator actually has — is batching
// switched on, can this destination do it, and is it doing it right now —
// plus enough runtime detail to see the effect. It is telemetry, NOT
// accounting: nothing here participates in
// MsgsDone == Appended + Adopted + PriorDone + Planned, and the migration
// behaves identically whether or not anyone looks at it.
type MAValues struct {
	Enabled    bool   // configuration permits batching (auto, and not a dry run)
	BatchLabel string // "Auto" or an explicit count, e.g. "16"
	Probed     bool   // destination capabilities have been read
	Supported  bool   // destination advertises MULTIAPPEND
	Demoted    bool   // fell back after the destination mishandled a batch
	Commands   int64  // batched append commands issued
	Messages   int64  // messages carried by those commands
	LastBatch  int    // size of the most recent batch
}

// State is the short status label: "Active (Auto)", "Not supported", … It is
// deliberately a small, closed set of phrases so an operator learns them once.
func (v MAValues) State() string {
	switch {
	case !v.Enabled:
		return "Disabled"
	case v.Demoted:
		return "Fallback (server rejected batches)"
	case v.Probed && !v.Supported:
		return "Not supported by destination"
	case v.Commands > 0:
		return "Active (" + v.batch() + ")"
	case v.Probed && v.Supported:
		return "Ready (" + v.batch() + ")"
	default:
		return "Enabled (" + v.batch() + ")"
	}
}

// batch renders the configured batching mode the way F9 Settings shows it.
func (v MAValues) batch() string {
	if v.BatchLabel == "" || v.BatchLabel == "auto" {
		return "Auto"
	}
	return "Batch Size: " + v.BatchLabel
}

// Detail adds the runtime figures to State when there are any — average
// batch size and how many batched commands have been issued. Concise by
// design: the Dashboard is not a protocol trace.
func (v MAValues) Detail() string {
	s := v.State()
	if v.Commands == 0 {
		return s
	}
	return fmt.Sprintf("%s · %d cmd · avg %.1f/cmd · last %d",
		s, v.Commands, float64(v.Messages)/float64(v.Commands), v.LastBatch)
}

// Compact is the Dashboard form: the state plus the ONE runtime figure that
// matters (how many messages each append command is carrying). The Dashboard
// side column is narrow, so this stays short by construction rather than
// relying on the caller to truncate something meaningful away.
func (v MAValues) Compact() string {
	switch {
	case !v.Enabled:
		return "Disabled"
	case v.Demoted:
		return "Fallback (rejected)"
	case v.Probed && !v.Supported:
		return "Not supported"
	case v.Commands > 0:
		return fmt.Sprintf("Active (%s) avg %.1f", v.batchShort(),
			float64(v.Messages)/float64(v.Commands))
	case v.Probed && v.Supported:
		return "Ready (" + v.batchShort() + ")"
	default:
		return "Enabled (" + v.batchShort() + ")"
	}
}

// batchShort is the compact batch token. The full form spells out
// "Batch Size: 16" for clarity where there is room; here the row is already
// labelled MULTIAPPEND, so the bare number reads correctly and fits.
func (v MAValues) batchShort() string {
	if v.BatchLabel == "" || v.BatchLabel == "auto" {
		return "Auto"
	}
	return v.BatchLabel
}

// Active reports whether batching is actually happening.
func (v MAValues) Active() bool { return v.Enabled && v.Commands > 0 }

// MultiAppend aggregates the per-mailbox pictures into ONE run-level answer
// for the Dashboard. The rule is deliberately pessimistic-but-honest: a run
// is only "Active" if some mailbox really batched, and only "Not supported"
// once every probed destination has said so — a mixed run reports what is
// actually happening rather than averaging it into something meaningless.
func (s Snapshot) MultiAppend() MAValues {
	var out MAValues
	probed, supported, demoted := 0, 0, 0
	for _, m := range s.Mailboxes {
		if !m.MA.Enabled {
			continue
		}
		out.Enabled = true
		if out.BatchLabel == "" {
			out.BatchLabel = m.MA.BatchLabel
		}
		out.Commands += m.MA.Commands
		out.Messages += m.MA.Messages
		if m.MA.LastBatch > 0 {
			out.LastBatch = m.MA.LastBatch
		}
		if m.MA.Probed {
			probed++
			if m.MA.Supported {
				supported++
			}
		}
		if m.MA.Demoted {
			demoted++
		}
	}
	out.Probed = probed > 0
	out.Supported = supported > 0
	// Only call the whole run demoted when nothing is still batching happily.
	out.Demoted = demoted > 0 && supported == demoted
	return out
}
