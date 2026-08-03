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

// MULTIAPPEND (RFC 3502) — several messages uploaded in ONE APPEND command.
//
// Implemented against the specification, not against another client's
// behaviour. The parts that matter, verified in RFC 3502 and RFC 4315:
//
//	append          = "APPEND" SP mailbox 1*append-message
//	append-message  = [SP flag-list] [SP date-time] SP literal
//
//	"If the append is unsuccessful for any reason (including being
//	 cancelled), the mailbox MUST be restored to its state before the
//	 APPEND attempt; no partial appending is permitted."   — RFC 3502
//
//	"A zero-length message literal argument is an error, and MUST return a
//	 NO. This can be used to cancel the append."           — RFC 3502
//
//	"If the server also supports the [MULTIAPPEND] extension, and if
//	 multiple messages were appended in the APPEND command, then the second
//	 value [of APPENDUID] is a UID set containing the UIDs assigned to the
//	 appended messages, in the order they were transmitted."
//	                                                       — RFC 4315
//
// Two consequences shape everything MailFerry does with it:
//
//  1. ATOMICITY IS A GIFT. A tagged NO means NOTHING was appended, so a
//     rejected batch can be retried message-by-message with no duplicate
//     risk whatsoever.
//
//  2. THE AMBIGUOUS CASE IS UNCHANGED. If the connection dies before the
//     tagged completion arrives, the batch may have been committed in full
//     or not at all — exactly the same ambiguity a single APPEND has, only
//     wider. MailFerry therefore keeps its per-message durable intent rows
//     and resolves the outcome per message by fingerprint on the next pass
//     (see engine.reconcileInflight). A batch is NEVER a unit of durable
//     accounting.
//
// Memory: the literal for each message streams straight from the source
// FETCH to the destination socket, one chunk at a time, exactly as the
// single-message APPEND path does. Batching changes how many tagged
// commands are issued — never how much of a mailbox is resident in RAM.
package imapx

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MultiAppend is one MULTIAPPEND command being built and streamed.
//
// It is deliberately LAZY: nothing is written to the socket until the first
// message is added, so a batch that ends up empty (cancellation, an empty
// plan) costs no protocol at all and needs no RFC 3502 zero-length-literal
// cancellation. Once a literal has been declared its octets MUST be
// delivered, so a caller that cannot deliver them must tear the connection
// down and let reconciliation resolve the outcome.
type MultiAppend struct {
	c       *Client
	mailbox string
	p       *Pending
	n       int
	plus    bool // LITERAL+ is in use: no continuation round trips
	err     error
	done    bool
}

// SupportsMultiAppend reports whether this connection may use MULTIAPPEND:
// the server advertises the capability and conservative baseline mode is
// off. Capability is re-read after STARTTLS and after login, so this always
// reflects the negotiated session.
func (c *Client) SupportsMultiAppend() bool {
	return !c.Baseline && c.Has("MULTIAPPEND")
}

// MultiAppendBegin opens a MULTIAPPEND for mailboxWire. Nothing reaches the
// socket here — see the type comment. Under --dry-run the choke point
// refuses immediately, so a dry run provably issues neither APPEND nor
// MULTIAPPEND.
func (c *Client) MultiAppendBegin(mailboxWire string) (*MultiAppend, error) {
	if c.ReadOnly {
		return nil, blockedDryRun("APPEND") // choke point: nothing is written
	}
	if !c.SupportsMultiAppend() {
		return nil, fmt.Errorf("MULTIAPPEND is not available on this connection")
	}
	return &MultiAppend{c: c, mailbox: mailboxWire,
		plus: c.Has("LITERAL+") && !c.Baseline}, nil
}

// Count is how many messages have been added so far.
func (m *MultiAppend) Count() int { return m.n }

// AddBegin declares the next message's flags, INTERNALDATE and literal size.
// Flags and INTERNALDATE are emitted in exactly the same form as the
// single-message APPEND path, so a batched message is byte-for-byte the same
// upload as an unbatched one.
//
// With LITERAL+ the octets may follow immediately; without it AddBegin waits
// for the server's continuation first, as RFC 3501 requires.
func (m *MultiAppend) AddBegin(flags, internalDate string, size int64) error {
	if m.err != nil {
		return m.err
	}
	if m.done {
		return fmt.Errorf("MULTIAPPEND: message added after the command was finished")
	}
	if size <= 0 {
		// A zero-length literal is an RFC 3502 error (it cancels the whole
		// command). MailFerry never sends one: a zero-size message is not
		// batchable and is left to the single-append path to report.
		return fmt.Errorf("MULTIAPPEND: refusing to append a zero-length message")
	}

	part := ""
	if m.n == 0 {
		// First message: allocate the tag and open the command.
		m.c.pendAppends.Add(1)
		m.c.mu.Lock()
		m.c.tagN++
		tag := fmt.Sprintf("MF%04d", m.c.tagN)
		p := &Pending{tag: tag, name: "APPEND", types: map[string]bool{}, ch: make(chan error, 1)}
		m.c.pending[tag] = p
		m.c.order = append(m.c.order, tag)
		m.c.mu.Unlock()
		m.p = p
		part = tag + " APPEND " + quoteIMAP(m.mailbox)
	}
	if flags != "" {
		part += " (" + flags + ")"
	}
	if internalDate != "" {
		part += " " + quoteIMAP(internalDate)
	}
	plus := ""
	if m.plus {
		plus = "+"
	}
	part += fmt.Sprintf(" {%d%s}", size, plus)

	if err := m.writeHeader(part); err != nil {
		m.fail(err)
		return err
	}
	if !m.plus {
		if err := m.awaitContinuation(); err != nil {
			m.fail(err)
			return err
		}
	}
	m.n++
	return nil
}

// writeHeader emits one append-message header line (the literal announcement).
func (m *MultiAppend) writeHeader(part string) error {
	c := m.c
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed.Load() {
		return c.lostErr()
	}
	if _, err := c.bw.WriteString(part + "\r\n"); err != nil {
		return &ConnLost{err.Error()}
	}
	if err := c.flushWire(); err != nil {
		return &ConnLost{err.Error()}
	}
	c.trace("C:", part)
	return nil
}

// awaitContinuation blocks for the server's "+" before a synchronising
// literal. An early tagged completion means the server rejected the command.
func (m *MultiAppend) awaitContinuation() error {
	select {
	case <-m.c.contCh:
		return nil
	case err := <-m.p.ch:
		if err == nil {
			err = &ConnLost{"MULTIAPPEND completed before its literal"}
		}
		return err
	case <-time.After(m.c.Timeout):
		return &ConnLost{"timed out waiting for the MULTIAPPEND continuation"}
	}
}

// Write streams literal octets for the message opened by AddBegin.
func (m *MultiAppend) Write(chunk []byte) error {
	if m.err != nil {
		return m.err
	}
	c := m.c
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.bw.Write(chunk); err != nil {
		m.err = &ConnLost{err.Error()}
		return m.err
	}
	return nil
}

// Finish terminates the command. The tagged completion is pipelined via the
// returned Pending exactly like a single APPEND, so the caller can keep
// streaming the next batch while this one is acknowledged.
//
// A batch with no messages never touched the socket: Finish returns a nil
// Pending and no error, and the caller simply has nothing to settle.
func (m *MultiAppend) Finish() (*Pending, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.done = true
	if m.n == 0 {
		return nil, nil
	}
	c := m.c
	c.wmu.Lock()
	_, err := c.bw.WriteString("\r\n")
	if err == nil {
		err = c.flushWire()
	}
	c.wmu.Unlock()
	if err != nil {
		m.fail(&ConnLost{err.Error()})
		return nil, m.err
	}
	return m.p, nil
}

// fail records the terminal error and releases the append bookkeeping.
func (m *MultiAppend) fail(err error) {
	if m.err == nil {
		m.err = err
		if m.n > 0 || m.p != nil {
			m.c.pendAppends.Add(-1)
		}
	}
}

// ------------------------------------------------------------- APPENDUID --

// AppendUIDsOf extracts the destination UIDs from a MULTIAPPEND completion's
// [APPENDUID <uidvalidity> <uid-set>] response code, in the order the
// messages were transmitted (RFC 4315).
//
// It returns nil unless it can account for EXACTLY want UIDs. A server that
// omits UIDPLUS, or returns a set MailFerry cannot map one-to-one onto the
// messages it sent, must not be guessed at: the caller then records the
// messages as done without a destination UID, exactly as it already does for
// servers without UIDPLUS. Guessing a UID would corrupt --sync-flags and
// every later reconciliation.
func AppendUIDsOf(res Result, want int) []int64 {
	if res.Final == nil || len(res.Final.Code) < 3 || want <= 0 {
		return nil
	}
	if k, _ := TokStr(res.Final.Code[0]); !strings.EqualFold(k, "APPENDUID") {
		return nil
	}
	set, ok := TokStr(res.Final.Code[2])
	if !ok {
		return nil
	}
	uids := parseUIDSet(set)
	if len(uids) != want {
		return nil
	}
	return uids
}

// parseUIDSet expands an RFC 3501 sequence-set of literal UIDs ("12",
// "12:15", "12,14:16"). "*" is rejected: RFC 4315 forbids it in APPENDUID,
// and a wildcard cannot be mapped to a specific message.
func parseUIDSet(s string) []int64 {
	var out []int64
	for _, part := range strings.Split(strings.TrimSpace(s), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil
		}
		lo, hi, isRange := strings.Cut(part, ":")
		a, err := strconv.ParseInt(lo, 10, 64)
		if err != nil || a <= 0 {
			return nil
		}
		if !isRange {
			out = append(out, a)
			continue
		}
		b, err := strconv.ParseInt(hi, 10, 64)
		if err != nil || b <= 0 {
			return nil
		}
		if b < a {
			a, b = b, a
		}
		if b-a > 1<<20 { // a pathological range is a malformed response
			return nil
		}
		for u := a; u <= b; u++ {
			out = append(out, u)
		}
	}
	return out
}
