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

// The F6 Errors view, redesigned in the Results visual language: a
// master/detail screen that keeps two truths clearly apart —
//
//	A. ERRORS & WARNINGS      run-level events from this run's engine bus
//	B. FAILED MESSAGE REGISTRY outstanding per-message failures (post-run)
//
// One selection cursor spans both sections; the selected entry's full
// story (decoded subject, sender, complete reason, retry status) renders
// in a dedicated detail panel, so the master rows stay aligned no matter
// how long a subject or server response is. The status line always
// describes the overall state truthfully — "all clear" appears only when
// there is genuinely nothing needing attention.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/ajsap/mailferry/v2/internal/engine"
	"github.com/ajsap/mailferry/v2/internal/state"
	"github.com/ajsap/mailferry/v2/internal/util"
	"github.com/charmbracelet/lipgloss"
)

// errItem is one selectable row: a run-level error OR a registry entry.
type errItem struct {
	isReg bool
	err   engine.LogEntry
	reg   state.FailedRow
}

// errorItems builds the combined, search-filtered selection list: run-level
// errors first (newest first), then the Failed Message Registry entries.
func (m *Model) errorItems() []errItem {
	var out []errItem
	for _, e := range m.errorRows() {
		out = append(out, errItem{err: e})
	}
	q := strings.ToLower(m.viewSearch(vErrors))
	for _, r := range m.registryRows() {
		if q != "" &&
			!strings.Contains(strings.ToLower(r.Mailbox), q) &&
			!strings.Contains(strings.ToLower(r.Folder), q) &&
			!strings.Contains(strings.ToLower(util.DecodeMIME(r.Subject)), q) &&
			!strings.Contains(strings.ToLower(r.Reason), q) &&
			!strings.Contains(strings.ToLower(r.FType), q) {
			continue
		}
		out = append(out, errItem{isReg: true, reg: r})
	}
	return out
}

// registryRows returns the Failed Message Registry entries available to
// this session (delivered with the authoritative end-of-run payload).
func (m *Model) registryRows() []state.FailedRow {
	if m.results == nil {
		return nil
	}
	return m.results.Res.FailedRegistry
}

func (m *Model) errorsView(w, h int) string {
	items := m.errorItems()
	if m.errSel >= len(items) {
		m.errSel = len(items) - 1
	}
	if m.errSel < 0 {
		m.errSel = 0
	}
	nErr := 0
	for _, it := range items {
		if !it.isReg {
			nErr++
		}
	}
	nReg := len(items) - nErr
	regTotal := len(m.registryRows())

	var b strings.Builder
	line := func(s string) { b.WriteString(clip(s, w) + "\n") }

	// ---- layout budget --------------------------------------------------
	// headers+blanks ≈ 5 lines, registry column header 1, detail panel and
	// status line take the bottom; the two master lists share the rest.
	detailH := 0
	if len(items) > 0 {
		detailH = errDetailHeight
	}
	avail := h - 5 - detailH - 1 // -1 status line
	if avail < 4 {
		avail = 4
	}
	errH := nErr
	if nReg > 0 && errH > 4 {
		errH = 4 // registry present: run errors keep a compact window
	}
	if errH > avail-2 {
		errH = avail - 2
	}
	if errH < 0 {
		errH = 0
	}
	regH := avail - errH
	if regH > nReg+1 { // +1 column header
		regH = nReg + 1
	}

	// ---- A. run-level errors -------------------------------------------
	head := " ERRORS & WARNINGS"
	if q := m.viewSearch(vErrors); q != "" {
		head += "   /" + q
	}
	if nErr == 0 {
		line(cBold.Render(head) + cDim.Render(" — no run-level errors recorded"))
	} else {
		line(cBold.Render(head) + " — " + cYellow.Render(fmt.Sprintf("%d recorded", nErr)))
		top := m.sectionTop(0, nErr, errH)
		for i := top; i < nErr && i < top+errH; i++ {
			e := items[i].err
			cursor := "  "
			if i == m.errSel {
				cursor = cCyan.Render("▸ ")
			}
			line(cursor + sevStyle(e.Severity).Render(clip(fmt.Sprintf("%s  %-5s %-24s %s",
				e.TS.Format("15:04:05"), e.Severity, clip(e.Mailbox, 24), e.Message), w-3)))
		}
		if nErr > errH {
			line(cDim.Render(fmt.Sprintf("   … %d run-level entries — ↑↓ scrolls", nErr)))
		}
	}
	line("")

	// ---- B. Failed Message Registry ------------------------------------
	switch {
	case regTotal > 0:
		line(cBold.Render(" FAILED MESSAGE REGISTRY") + " — " +
			cRed.Render(fmt.Sprintf("%d outstanding", regTotal)))
	case m.finished:
		line(cBold.Render(" FAILED MESSAGE REGISTRY") + cDim.Render(" — none outstanding"))
	default:
		agg := m.snap.Agg()
		if agg.FailedMsgs > 0 {
			line(cBold.Render(" FAILED MESSAGE REGISTRY") + " — " + cYellow.Render(fmt.Sprintf(
				"%d recorded so far", agg.FailedMsgs)) + cDim.Render(" · details on completion or `mailferry failed`"))
		} else {
			line(cBold.Render(" FAILED MESSAGE REGISTRY") + cDim.Render(" — nothing recorded so far"))
		}
	}
	if nReg > 0 {
		// responsive columns: Folder narrows then disappears before the
		// mailbox and reason columns are allowed to collide.
		uidW, sizeW, ftW := 6, 9, 12
		fldW := 16
		if w < 96 {
			fldW = 10
		}
		if w < 80 {
			fldW = 0
		}
		mbW := w - uidW - sizeW - ftW - 8
		if fldW > 0 {
			mbW -= fldW + 1
		}
		if mbW < 16 {
			mbW = 16
		}
		hdr := "   " + cell(" Mailbox", mbW, false)
		if fldW > 0 {
			hdr += " " + cell("Folder", fldW, false)
		}
		hdr += " " + cell("UID", uidW, true) + " " + cell("Size", sizeW, true) + " " + cell("Reason", ftW, false)
		line(cDim.Render(clip(hdr, w)))
		top := m.sectionTop(nErr, nReg, regH-1)
		shown := 0
		for i := top; i < nReg && shown < regH-1; i++ {
			it := items[nErr+i]
			cursor := "  "
			if nErr+i == m.errSel {
				cursor = cCyan.Render("▸ ")
			}
			row := " " + cell(clip(it.reg.Mailbox, mbW), mbW, false)
			if fldW > 0 {
				row += " " + cell(clip(it.reg.Folder, fldW), fldW, false)
			}
			row += " " + cell(fmt.Sprint(it.reg.SrcUID), uidW, true) +
				" " + cell(util.FmtBytes(float64(it.reg.Size)), sizeW, true) +
				" " + cell(clip(it.reg.FType, ftW), ftW, false)
			line(cursor + clip(row, w-3))
			shown++
		}
		if rest := nReg - top - shown; rest > 0 {
			line(cDim.Render(fmt.Sprintf("   … +%d more — ↑↓ scrolls · mailferry failed lists all", rest)))
		}
	}

	// ---- detail panel for the selection --------------------------------
	if len(items) > 0 && m.errSel < len(items) {
		b.WriteString(m.errDetailPanel(items[m.errSel], w))
	}

	// ---- truthful overall status ---------------------------------------
	b.WriteString(m.errStatusLine(nErr, regTotal, w))
	return b.String()
}

// errDetailHeight: bordered panel = 2 border lines + up to 5 content lines.
const errDetailHeight = 8

// sectionTop computes the scroll offset of a section so the cursor stays
// visible. base is the section's first index in the combined list.
func (m *Model) sectionTop(base, n, window int) int {
	if window <= 0 {
		return 0
	}
	sel := m.errSel - base
	if sel < 0 || sel >= n {
		return 0
	}
	if sel < window {
		return 0
	}
	return sel - window + 1
}

// cell pads/clips s to an exact display width (right-aligned when right).
func cell(s string, width int, right bool) string {
	s = clip(s, width)
	pad := strings.Repeat(" ", max0(width-lipgloss.Width(s)))
	if right {
		return pad + s
	}
	return s + pad
}

// errDetailPanel renders the selected entry's full story: long subjects
// and server responses live HERE, wrapped, so the master rows above never
// lose their alignment.
func (m *Model) errDetailPanel(it errItem, w int) string {
	iw := w - 6
	if iw < 20 {
		iw = 20
	}
	var lines []string
	kv := func(k, v string) {
		lines = append(lines, " "+cDim.Render(fmt.Sprintf("%-9s", k))+" "+clip(v, iw-11))
	}
	if it.isReg {
		r := it.reg
		where := fmt.Sprintf("%s · %s · UID %d · %s", r.Mailbox, orEmpty(r.Folder),
			r.SrcUID, util.FmtBytes(float64(r.Size)))
		kv("Message", where)
		kv("Subject", util.DecodeMIME(orEmpty(r.Subject)))
		from := util.DecodeMIME(orEmpty(r.Sender))
		if r.Date != "" {
			from += cDim.Render("  ·  " + r.Date)
		}
		kv("From", from)
		status := fmt.Sprintf("%s · %d attempt(s) · status %s", r.FType, r.FailCount, r.Status)
		if r.LastTS > 0 {
			status += cDim.Render("  ·  last " + time.Unix(int64(r.LastTS), 0).Format("02 Jan 15:04"))
		}
		kv("Failure", status)
		kv("Reason", orEmpty(r.Reason))
		return " " + strings.ReplaceAll(titledBox("Selected failed message — ⏎ full details",
			lines, w-2), "\n", "\n ") + "\n"
	}
	e := it.err
	kv("Time", e.TS.Format("2006-01-02 15:04:05")+"  ·  "+e.Severity)
	kv("Mailbox", orEmpty(e.Mailbox))
	for i, wl := range wrapLines("", e.Message, iw-11) {
		if i >= 3 {
			break
		}
		k := "Message"
		if i > 0 {
			k = ""
		}
		kv(k, wl)
	}
	return " " + strings.ReplaceAll(titledBox("Selected error — ⏎ full details",
		lines, w-2), "\n", "\n ") + "\n"
}

// errStatusLine tells the overall truth in one line — never "all clear"
// while anything remains outstanding.
func (m *Model) errStatusLine(nErr, nReg int, w int) string {
	switch {
	case nErr == 0 && nReg == 0:
		return cGreen.Render(clip("  all clear — no run-level errors and no outstanding failed messages", w))
	case nReg > 0:
		s := fmt.Sprintf("  %d outstanding failed message(s)", nReg)
		if nErr > 0 {
			s = fmt.Sprintf("  %d run-level error(s) · %s", nErr, strings.TrimSpace(s))
		}
		return cYellow.Render(clip(s+" — retry: mailferry retry-failed", w))
	default:
		return cYellow.Render(clip(fmt.Sprintf(
			"  %d run-level error(s) recorded — no outstanding failed messages", nErr), w))
	}
}

// openFailedPopup shows every stored registry field for one message.
func (m *Model) openFailedPopup(r state.FailedRow) {
	ts := func(v float64) string {
		if v <= 0 {
			return "-"
		}
		return time.Unix(int64(v), 0).Format("2006-01-02 15:04:05")
	}
	lines := []string{
		"Mailbox    " + r.Mailbox,
		"Folder     " + orEmpty(r.Folder) + fmt.Sprintf(" · UID %d · %s", r.SrcUID,
			util.FmtBytes(float64(r.Size))),
		"Message-ID " + orEmpty(r.MessageID),
		"",
	}
	lines = append(lines, wrapLines("Subject  ", util.DecodeMIME(orEmpty(r.Subject)), 56)...)
	lines = append(lines, "From     "+util.DecodeMIME(orEmpty(r.Sender)))
	if r.Date != "" {
		lines = append(lines, "Date     "+r.Date)
	}
	lines = append(lines, "",
		fmt.Sprintf("Failure  %s · %d attempt(s) · status %s", r.FType, r.FailCount, r.Status),
		"First    "+ts(r.FirstTS)+" · last "+ts(r.LastTS), "")
	lines = append(lines, wrapLines("Reason   ", orEmpty(r.Reason), 56)...)
	lines = append(lines, "", cDim.Render("Retry: mailferry retry-failed — full list: mailferry failed"))
	m.popup = &popupState{Title: "Failed message", Lines: lines}
}
