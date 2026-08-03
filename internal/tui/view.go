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
	"sort"
	"strings"
	"time"

	"github.com/ajsap/mailferry/v2/internal/engine"
	"github.com/ajsap/mailferry/v2/internal/identity"
	"github.com/ajsap/mailferry/v2/internal/util"
	"github.com/charmbracelet/lipgloss"
)

func timeNow() time.Time { return time.Now() }

const minCols, minRows = 72, 18

func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	w, h := m.width, m.height
	if w == 0 {
		w, h = 100, 30
	}
	if m.shuttingDown {
		return m.shutdownView(w, h)
	}
	// The quit gate outranks every other modal: once it is up, nothing else
	// may take the screen or the keyboard.
	if m.confirmQuit {
		return m.confirmQuitView(w, h)
	}
	if m.popup != nil {
		return m.popupBox(w, h)
	}
	if w < minCols || h < minRows {
		return m.tooSmall(w, h)
	}
	var b strings.Builder
	b.WriteString(m.banner(w))
	b.WriteString("\n")
	b.WriteString(m.navBar(w))
	b.WriteString("\n")
	b.WriteString(strings.Repeat("─", w))
	b.WriteString("\n")
	// body fills the middle; footer is one line
	bodyH := h - 5
	body := m.safeBody(w, bodyH)
	lines := strings.Split(body, "\n")
	for i := 0; i < bodyH; i++ {
		if i < len(lines) {
			b.WriteString(clip(lines[i], w))
		}
		b.WriteString("\n")
	}
	b.WriteString(m.footer(w))
	return b.String()
}

// safeBody guards rendering: a view bug degrades to an error notice and the
// migration continues — navigation must never freeze (parity with classic).
func (m *Model) safeBody(w, h int) (out string) {
	defer func() {
		if r := recover(); r != nil {
			m.bus.Log("ERROR", "tui", fmt.Sprintf("view %q failed to render: %v",
				viewName(m.active), r))
			out = "\n" + cRed.Render(fmt.Sprintf("  The %s view failed to render.", viewName(m.active))) +
				"\n\n  The migration continues unaffected.\n" +
				cDim.Render("  Press 1 for the Dashboard, or 8 to check the Logs.")
		}
	}()
	return m.body(w, h)
}

func (m *Model) banner(w int) string {
	title := center(identity.BannerLine(), w)
	slogan := center(identity.Slogan, w)
	return cBanner.Render(title) + "\n" + cDim.Render(slogan)
}

func (m *Model) navBar(w int) string {
	var parts []string
	for i := 0; i < numViews; i++ {
		label := fmt.Sprintf("F%d %s", i+1, viewNames[i])
		if i+1 == 10 {
			label = "F10 " + viewNames[i]
		}
		if i == m.active {
			parts = append(parts, cBold.Render("▐"+label+"▌"))
		} else {
			parts = append(parts, cDim.Render(label))
		}
	}
	bar := " " + strings.Join(parts, " ")
	badge := ""
	if m.bus.Paused {
		badge = cYellow.Render(" ▮▮PAUSED")
	} else if m.frozen {
		badge = cCyan.Render(" ❄FROZEN")
	}
	return clip(bar, w-lipgloss.Width(badge)) + badge
}

func (m *Model) footer(w int) string {
	if m.searching {
		return cCyan.Render(clip(fmt.Sprintf(" search: /%s▏  (⏎ keep · Esc clear)", m.search), w))
	}
	hint := ""
	if m.active == vResults {
		hint = "⏎/Esc Mailboxes   F1–F10 Views   F6 Failed msgs   F8 Logs   S Reports   q Quit"
	} else {
		hint = viewFooters[m.active]
		if m.finished {
			hint = "Esc Results   " + hint
		}
	}
	if m.flash != "" {
		hint = m.flash + "   ·   " + hint
		m.flash = ""
	}
	return cDim.Render(clip(" "+hint, w))
}

var viewFooters = [numViews]string{
	"F2–F10 Views   1–0 aliases   p Pause   Space Freeze   R Retry failed   u Reload CSV   ^C/q Quit",
	"↑↓/k j Select   / Search   F1 Dashboard   ^C/q Quit",
	"↑↓/k j Select   ⏎ Details   r Retry   s Sort   f Filter   / Search   ^C/q Quit",
	"F1 Dashboard   ^C/q Quit",
	"↑↓/k j Navigate   ⏎ Details   / Search   End/F Follow   Esc Back   ^C/q Quit",
	"↑↓/k j Select   ⏎ Details   / Search   ^C/q Quit",
	"Space Freeze   ^L Redraw   ^C/q Quit",
	"↑↓/k j · PgUp/PgDn · Home/End Scroll   F Follow   / Search   Mouse wheel   ^C/q Quit",
	"↑↓/k j Select   ⏎/Space Edit   +/- Step   S Save   Esc Discard   ^C/q Quit",
	"↑↓ Scroll   Esc Back   ^C/q Quit",
}

func (m *Model) body(w, h int) string {
	switch m.active {
	case vDashboard:
		return m.dashboardView(w, h)
	case vWorkers:
		return m.workersView(w, h)
	case vMailboxes:
		return m.mailboxesView(w, h)
	case vQueue:
		return m.queueView(w, h)
	case vHistory:
		return m.historyView(w, h)
	case vErrors:
		return m.errorsView(w, h)
	case vPerformance:
		return m.performanceView(w, h)
	case vLogs:
		return m.logsView(w, h)
	case vSettings:
		return m.settingsView(w, h)
	case vHelp:
		return m.helpView(w, h)
	case vResults:
		return m.resultsView(w, h)
	}
	return ""
}

// ------------------------------------------------------------ dashboard --

// runFinishedAt is the ONE authoritative completion instant. It comes from
// the engine's RunResult — the same value the reports, the results CSV and
// the Results screen use. The Dashboard never stamps a time of its own, so
// there is exactly one completion time anywhere in MailFerry.
func (m *Model) runFinishedAt() (time.Time, bool) {
	if m.results != nil && !m.results.Res.FinishedAt.IsZero() {
		return m.results.Res.FinishedAt, true
	}
	return time.Time{}, false
}

// runElapsed is the run's duration: live while migrating, and FROZEN at the
// authoritative completion instant once the run is over. A Runtime that
// keeps counting after a migration has finished is simply wrong — it is no
// longer measuring anything.
func (m *Model) runElapsed() float64 {
	if fin, ok := m.runFinishedAt(); ok {
		// A completion instant that precedes the start means the clock moved
		// under us (NTP step, a resumed laptop). Showing 00:00:00 for a run
		// that plainly took time would be worse than the live figure, so
		// fall through rather than report an impossibility.
		if d := fin.Sub(m.snap.BatchStart).Seconds(); d >= 0 {
			return d
		}
	}
	return time.Since(m.snap.BatchStart).Seconds()
}

// dashRow renders one "Label   value" pair in the Dashboard's key/value
// style: no colons, aligned label column, value clipped to the cell.
func dashRow(label, value string, cell int) string {
	const labelW = 14
	if cell <= labelW+2 {
		return clip(label+" "+value, cell)
	}
	return fmt.Sprintf("%-*s%s", labelW, label, clip(value, cell-labelW))
}

func (m *Model) dashboardView(w, h int) string {
	agg := m.snap.Agg()
	br, mr := m.displayRates()
	var b strings.Builder

	type pair struct{ k, v string }
	left := []pair{
		{"CSV File", m.snap.CSVFile},
		{"State DB", m.snap.DBPath},
		{"Mode", m.snap.Mode},
		{"Workers", fmt.Sprintf("%d / %d",
			agg.Counts["RUNNING"]+agg.Counts["RETRYING"], m.snap.Workers)},
		{"Runtime", util.FmtDHMS(m.runElapsed())},
	}
	// Once the run is over, the Dashboard says so and shows WHEN — from the
	// authoritative FinishedAt, formatted by the same locale-aware helper the
	// Results screen and the reports use.
	if fin, ok := m.runFinishedAt(); ok {
		left = append(left, pair{m.stampLabel(), util.FmtStamp(fin)})
	}

	eta := "-"
	if e, ok := m.eta(); ok {
		eta = util.FmtDHMS(e)
	}
	right := []pair{
		// Cumulative traffic for the WHOLE migration. Per-connection
		// throughput lives in the Source/Destination panel below; these two
		// answer different questions and must not be confused.
		{"Sent/Rcvd", util.FmtBytes(float64(agg.WireTX)) + " / " + util.FmtBytes(float64(agg.WireRX))},
		// Byte rate and message rate are two equally useful measurements —
		// parenthesising the second made it look like an aside.
		{"Rate", fmt.Sprintf("%s/s · %.1f msg/s", util.FmtBytes(br), mr)},
		{"ETA", eta},
		// Payload bytes confirmed migrated vs total planned for the run —
		// completed versus total, hence "Progress". Distinct from Sent/Rcvd,
		// which is wire traffic.
		{"Progress", util.FmtBytes(float64(agg.BytesDone)) + " / " + util.FmtBytes(float64(agg.BytesTotal))},
		{"Cluster", fmt.Sprint(len(m.bus.ClusterSnapshot()))},
		// MULTIAPPEND is transport tuning, so it belongs in the
		// run-information block — NOT in the Mode line, which is reserved
		// for things that change what a run does.
		{"MULTIAPPEND", m.snap.MultiAppend().Compact()},
	}

	// Two balanced columns when there is room; one stacked column when there
	// is not. Splitting the width evenly is what stops the left half sitting
	// half-empty while the right half is squeezed.
	cell, gap := (w-3)/2, 3
	twoCol := w >= 92
	if !twoCol {
		cell, gap = w, 0
	}
	rows := len(left)
	if len(right) > rows {
		rows = len(right)
	}
	head := 0
	if twoCol {
		for i := 0; i < rows; i++ {
			l, r := "", ""
			if i < len(left) {
				l = dashRow(left[i].k, left[i].v, cell)
			}
			if i < len(right) {
				r = dashRow(right[i].k, right[i].v, cell)
			}
			b.WriteString(padTo(l, cell+gap) + r + "\n")
			head++
		}
	} else {
		for _, p := range append(append([]pair{}, left...), right...) {
			b.WriteString(dashRow(p.k, p.v, cell) + "\n")
			head++
		}
	}

	// Connection panel: what MailFerry actually learned about the two servers
	// it is talking to. On a short terminal it yields to the mailbox list —
	// the list is the primary content, and the same detail is one keypress
	// away in the F3 inspector.
	conn := m.connectionPanel(cell, gap, twoCol)
	const minTableRows = 6
	if h-head-len(conn)-2 < minTableRows {
		conn = nil
	}
	for _, l := range conn {
		b.WriteString(clip(l, w) + "\n")
	}
	b.WriteString(strings.Repeat("─", w) + "\n")
	// The table gets whatever the header block did not use. Derived rather
	// than hard-coded so adding a row here can never silently push the
	// mailbox table off the bottom of the screen.
	b.WriteString(m.mailboxTable(w, h-head-len(conn)-2, true))
	b.WriteString(m.aggFooter(w))
	return b.String()
}

// sideSummary aggregates what is known about every LIVE connection on one
// side of the run. The panel renders from this rather than from any single
// mailbox, because an MSP run migrating from three different platforms at
// once has no "representative" mailbox — showing the first one's details
// would simply be untrue of the run.
type sideSummary struct {
	count     int
	hosts     map[string]bool
	labels    map[string]bool // full ServerLabel()s (name + version)
	platforms map[string]int  // identified platform -> mailbox count
	auths     map[string]bool
	tls       map[string]bool
	speed     float64 // sum of current per-connection throughput
	speedOK   bool
}

// summariseSide collects one side's connection evidence across the run.
// Aggregation is by IDENTIFIED PLATFORM, never by hostname — two Dovecot
// servers on different hosts are one platform, and an unidentified server
// counts as Unknown rather than being guessed at.
func (m *Model) summariseSide(dst bool) sideSummary {
	s := sideSummary{hosts: map[string]bool{}, labels: map[string]bool{},
		platforms: map[string]int{}, auths: map[string]bool{}, tls: map[string]bool{}}
	for _, mb := range m.snap.Mailboxes {
		side := mb.Src
		if dst {
			side = mb.Dst
		}
		// Only connections that actually happened contribute. A queued
		// mailbox has no evidence yet, and counting it would report
		// "Unknown" for servers MailFerry simply has not spoken to.
		if len(side.Caps) == 0 {
			continue
		}
		s.count++
		if side.Host != "" {
			s.hosts[side.Host] = true
		}
		name := side.Server
		if name == "" {
			name = "Unknown"
		}
		s.platforms[name]++
		s.labels[side.ServerLabel()] = true
		if side.Auth != "" {
			s.auths[side.Auth] = true
		}
		if t := tlsLabel(side.Security); t != "-" {
			s.tls[t] = true
		}
		if v, ok := m.sideSpeed(mb.Index, dst); ok {
			s.speed += v
			s.speedOK = true
		}
	}
	return s
}

// hostCell names the side's host, or counts them when several share the run.
func (s sideSummary) hostCell() string {
	if len(s.hosts) == 1 {
		for h := range s.hosts {
			return h
		}
	}
	if len(s.hosts) == 0 {
		return "-"
	}
	return fmt.Sprintf("%d hosts", len(s.hosts))
}

// serverCell names the side's platform: with the version when every
// connection reported the same one, name-only when versions differ.
func (s sideSummary) serverCell() string {
	if len(s.labels) == 1 {
		for l := range s.labels {
			return l
		}
	}
	if len(s.platforms) == 1 {
		for p := range s.platforms {
			return p
		}
	}
	return "-"
}

func (s sideSummary) speedCell() string {
	if !s.speedOK {
		return "-"
	}
	return util.FmtBytes(s.speed) + "/s"
}

// joinedOrMixed renders a small set of values ("OAuth (Microsoft) ·
// Password"), or "mixed" when listing them would not fit a cell.
func joinedOrMixed(set map[string]bool) string {
	if len(set) == 0 {
		return "-"
	}
	vals := make([]string, 0, len(set))
	for v := range set {
		vals = append(vals, v)
	}
	sort.Strings(vals)
	if len(vals) > 2 {
		return "mixed"
	}
	return strings.Join(vals, " · ")
}

// connectionPanel renders the Source/Destination block. Everything in it was
// learned from the live connections — MailFerry never guesses a server from
// a hostname, and prints "Unknown" rather than inventing an answer.
//
// Two presentations, chosen by what is true of the run:
//   - every connected mailbox on both sides uses ONE identified platform per
//     side → the detailed per-side panel (even across several hosts);
//   - different platforms participate in the same run → an aggregated
//     platform count per side, because no single pair represents the run.
//
// The F3 inspector remains authoritative for any individual mailbox.
func (m *Model) connectionPanel(cell, gap int, twoCol bool) []string {
	src, dst := m.summariseSide(false), m.summariseSide(true)
	if src.count == 0 && dst.count == 0 {
		return nil
	}
	if len(src.platforms) > 1 || len(dst.platforms) > 1 {
		return m.aggregatedPanel(src, dst, cell, gap, twoCol)
	}

	// Detailed: one platform per side. Speed is the summed current
	// throughput of that side's live connections — for the common single
	// pair it is simply that connection's rate. The panel deliberately does
	// not repeat the run's cumulative traffic (the Sent/Rcvd row's job),
	// and shows one figure per side: every IMAP connection both sends and
	// receives by nature, so a TX/RX split would add a column without
	// adding an answer.
	fields := func(s sideSummary) []string {
		return []string{
			dashRow("  Host", s.hostCell(), cell),
			dashRow("  Server", s.serverCell(), cell),
			dashRow("  Auth", joinedOrMixed(s.auths), cell),
			dashRow("  TLS", joinedOrMixed(s.tls), cell),
			dashRow("  Speed", s.speedCell(), cell),
		}
	}
	srcRows, dstRows := fields(src), fields(dst)

	out := []string{""}
	if twoCol {
		out = append(out, padTo(cCyan.Render("↓ Source"), cell+gap)+
			cCyan.Render("↑ Destination"))
		for i := range srcRows {
			out = append(out, padTo(srcRows[i], cell+gap)+dstRows[i])
		}
		return out
	}
	// Narrow: one condensed line per side; the full detail is a keypress
	// away in the F3 inspector.
	condensed := func(s sideSummary) string {
		parts := []string{s.hostCell(), s.serverCell()}
		if a := joinedOrMixed(s.auths); a != "-" {
			parts = append(parts, a)
		}
		if t := joinedOrMixed(s.tls); t != "-" {
			parts = append(parts, "TLS "+t)
		}
		if s.speedOK {
			parts = append(parts, s.speedCell())
		}
		return strings.Join(parts, " · ")
	}
	out = append(out,
		cCyan.Render("↓ Source")+"       "+clip(condensed(src), cell-15),
		cCyan.Render("↑ Destination")+"  "+clip(condensed(dst), cell-15))
	return out
}

// aggregatedPanel is the heterogeneous-run presentation: how many mailboxes
// are talking to each identified platform, per side. Counts aggregate by
// platform, never by hostname, so twelve Exchange Online mailboxes across
// four tenants read as one line.
func (m *Model) aggregatedPanel(src, dst sideSummary, cell, gap int, twoCol bool) []string {
	srcRows := platformRows(src.platforms, cell)
	dstRows := platformRows(dst.platforms, cell)
	out := []string{""}
	if twoCol {
		out = append(out, padTo(cCyan.Render("↓ Sources"), cell+gap)+
			cCyan.Render("↑ Destinations"))
		n := len(srcRows)
		if len(dstRows) > n {
			n = len(dstRows)
		}
		for i := 0; i < n; i++ {
			l, r := "", ""
			if i < len(srcRows) {
				l = srcRows[i]
			}
			if i < len(dstRows) {
				r = dstRows[i]
			}
			out = append(out, padTo(l, cell+gap)+r)
		}
		return out
	}
	out = append(out,
		cCyan.Render("↓ Sources")+"      "+clip(inlinePlatforms(src.platforms), cell-15),
		cCyan.Render("↑ Destinations")+" "+clip(inlinePlatforms(dst.platforms), cell-15))
	return out
}

// platformRows renders one side's platform counts, largest first, name as
// the tie-break so the list never reshuffles between refreshes.
func platformRows(platforms map[string]int, cell int) []string {
	list := sortedPlatforms(platforms)
	const maxRows = 6
	var out []string
	for i, p := range list {
		if i == maxRows-1 && len(list) > maxRows {
			rest := 0
			for _, q := range list[i:] {
				rest += q.n
			}
			out = append(out, clip(fmt.Sprintf("  %-18s %3d", "(others)", rest), cell))
			break
		}
		out = append(out, clip(fmt.Sprintf("  %-18s %3d", clip(p.name, 18), p.n), cell))
	}
	return out
}

// inlinePlatforms is the narrow-terminal form: "Exchange Online 12 · Dovecot 8".
func inlinePlatforms(platforms map[string]int) string {
	var parts []string
	for _, p := range sortedPlatforms(platforms) {
		parts = append(parts, fmt.Sprintf("%s %d", p.name, p.n))
	}
	return strings.Join(parts, " · ")
}

type platformCount struct {
	name string
	n    int
}

func sortedPlatforms(platforms map[string]int) []platformCount {
	list := make([]platformCount, 0, len(platforms))
	for name, n := range platforms {
		list = append(list, platformCount{name, n})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].name < list[j].name
	})
	return list
}

// padTo right-pads s to width, measuring the rendered width so styled text
// lines up with plain text.
func padTo(s string, width int) string {
	if n := width - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// tlsLabel renders the negotiated transport under a "TLS" heading, so the
// value carries the version rather than repeating the word: "1.3", or
// "1.2 (STARTTLS)" when TLS was reached by upgrading in band. A plaintext
// connection says so plainly — that is not a detail to soften.
func tlsLabel(security string) string {
	switch {
	case security == "" || security == "-":
		return "-"
	case security == "none":
		return "none (plaintext)"
	case strings.HasPrefix(security, "STARTTLS → TLS "):
		return strings.TrimPrefix(security, "STARTTLS → TLS ") + " (STARTTLS)"
	case strings.HasPrefix(security, "TLS "):
		return strings.TrimPrefix(security, "TLS ")
	default:
		return security
	}
}

func (m *Model) mailboxTable(w, h int, detail bool) string {
	const idxW, stW, flW, msW, spW, elW = 3, 8, 7, 15, 10, 14
	var b strings.Builder
	mwidth := w - (idxW + 2 + stW + 1 + flW + 1 + msW + 1 + 5 + 1 + spW + 1 + elW)
	if mwidth < 14 {
		mwidth = 14
	}
	b.WriteString(cDim.Render(fmt.Sprintf("%*s  %-*s %-*s %-*s %-*s %5s %*s %*s",
		idxW, "#", mwidth, "Mailbox", stW, "Status", flW, "Fldr", msW, "Msgs",
		"Fail", spW, "Speed", elW, "Elapsed")) + "\n")
	shown := 0
	for _, mb := range m.snap.Mailboxes {
		if shown >= h {
			break
		}
		active := mb.Status == "RUNNING" || mb.Status == "RETRYING" || mb.Status == "REMOTE"
		el := "-"
		if !mb.Start.IsZero() {
			end := mb.End
			if end.IsZero() {
				end = time.Now()
			}
			el = util.FmtDHMS(end.Sub(mb.Start).Seconds())
		}
		msgs := "-"
		if mb.MsgsTotal > 0 {
			msgs = fmt.Sprintf("%d/%d", mb.MsgsDone, mb.MsgsTotal)
		}
		fl := "-"
		if mb.FoldersTotal > 0 {
			fl = fmt.Sprintf("%d/%d", mb.FolderIndex, mb.FoldersTotal)
		}
		speed := "-"
		if active {
			speed = util.FmtBytes(m.mbSpeed(mb.Index)) + "/s"
		}
		nfail := mb.Skipped + mb.FailedMsgs
		failCell := "-"
		if nfail > 0 {
			failCell = cRed.Render(fmt.Sprintf("%d", nfail))
		}
		st := statusStyle(mb.Status).Render(fmt.Sprintf("%-*s", stW, mb.Status))
		b.WriteString(fmt.Sprintf("%*d  %-*s %s %-*s %-*s %5s %*s %*s\n",
			idxW, mb.Index, mwidth, clip(mb.Label, mwidth), st,
			flW, fl, msW, clip(msgs, msW), failCell, spW, speed, elW, el))
		shown++
		if detail && mb.Status == "RUNNING" && shown < h {
			bar := progressBar(mb.BytesDone, mb.BytesTotal, mb.MsgsDone, mb.MsgsTotal, 24)
			b.WriteString("      " + bar + "  " + cCyan.Render(clip(mb.Op, w-40)) + "\n")
			shown++
		}
		if mb.Status == "WARNINGS" && shown < h {
			b.WriteString("      " + cYellow.Render(clip(fmt.Sprintf(
				"completed with warnings — %d in the Failed Message Registry "+
					"(mailferry failed / retry-failed)", nfail), w-8)) + "\n")
			shown++
		}
	}
	return b.String()
}

func (m *Model) aggFooter(w int) string {
	agg := m.snap.Agg()
	br, mr := m.displayRates()
	c := agg.Counts
	done := c["SUCCESS"] + c["PARTIAL"] + c["FAILED"] + c["CANCELLED"] + c["WARNINGS"] + c["STALE"]
	frag := fmt.Sprintf("Done %d/%d   %s   %s   %s", done, len(m.snap.Mailboxes),
		cGreen.Render(fmt.Sprintf("%d ok", c["SUCCESS"])),
		cYellow.Render(fmt.Sprintf("%d partial", c["PARTIAL"])),
		cRed.Render(fmt.Sprintf("%d failed", c["FAILED"])))
	if c["WARNINGS"] > 0 {
		frag += "   " + cYellow.Render(fmt.Sprintf("%d with warnings", c["WARNINGS"]))
	}
	if c["REMOTE"] > 0 {
		frag += "   " + cCyan.Render(fmt.Sprintf("%d on other workers", c["REMOTE"]))
	}
	line2 := fmt.Sprintf("Msgs %d/%d (%s)   Progress %s   Rate %s/s (%.1f msg/s)   New %d   Adopted %d",
		agg.MsgsDone, agg.MsgsTotal, util.Pct(agg.MsgsDone, agg.MsgsTotal),
		util.FmtBytes(float64(agg.BytesDone)), util.FmtBytes(br), mr, agg.Appended, agg.Adopted)
	if agg.FailedMsgs > 0 {
		line2 += "   " + cRed.Render(fmt.Sprintf("MsgFail %d", agg.FailedMsgs))
	}
	st := m.snap.Stalls
	line3 := ""
	if st[0] > 0 {
		line3 = cYellow.Render(fmt.Sprintf("Stalls %d (recovered %d)", st[0], st[1]))
	}
	out := strings.Repeat("─", w) + "\n" + clip(frag, w) + "\n" + clip(line2, w)
	if line3 != "" {
		out += "\n" + line3
	}
	return out
}

func progressBar(bd, bt, md, mt int64, width int) string {
	total, val := bt, bd
	if total == 0 {
		total, val = mt, md
	}
	frac := 0.0
	if total > 0 {
		frac = float64(val) / float64(total)
		if frac > 1 {
			frac = 1
		}
	}
	fill := int(float64(width) * frac)
	return strings.Repeat("█", fill) + strings.Repeat("░", width-fill) +
		fmt.Sprintf(" %4s", util.Pct(val, total))
}

// -------------------------------------------------------------- helpers --

func center(s string, w int) string {
	vis := lipgloss.Width(s)
	if vis >= w {
		return s
	}
	left := (w - vis) / 2
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", w-vis-left)
}

func clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	// trim on visible width, preserving styling best-effort
	out := ""
	cur := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if cur+rw > w {
			break
		}
		out += string(r)
		cur += rw
	}
	return out
}

func (m *Model) tooSmall(w, h int) string {
	agg := m.snap.Agg()
	msg := []string{
		identity.BannerLine(),
		"",
		cYellow.Render("Terminal too small to draw the dashboard."),
		fmt.Sprintf("Please enlarge to at least %dx%d (currently %dx%d).", minCols, minRows, w, h),
		"",
		"The migration is still running in the background —",
		fmt.Sprintf("%d msgs / %s synced so far.", agg.MsgsDone, util.FmtBytes(float64(agg.BytesDone))),
		"",
		cDim.Render("Ctrl+C stops gracefully."),
	}
	var b strings.Builder
	pad := (h - len(msg)) / 2
	for i := 0; i < pad; i++ {
		b.WriteString("\n")
	}
	for _, l := range msg {
		b.WriteString(center(l, w) + "\n")
	}
	return b.String()
}

func (m *Model) shutdownView(w, h int) string {
	title := "Gracefully Stopping " + identity.Product
	if m.forced {
		title = "Force-stopping " + identity.Product
	}
	elapsed := int(time.Since(m.shutdownAt).Seconds())
	inner := 52
	var lines []string
	lines = append(lines, cBold.Render(center(title, inner)))
	lines = append(lines, cDim.Render(center(identity.Slogan, inner)))
	lines = append(lines, "")
	allDone := m.shutPhase >= len(shutdownPhases)
	for i, p := range shutdownPhases {
		switch {
		case i < m.shutPhase || allDone:
			lines = append(lines, "  "+cGreen.Render("✓")+"  "+p)
		case i == m.shutPhase:
			lines = append(lines, "  "+cYellow.Render("▶")+"  "+cBold.Render(p+"..."))
		default:
			lines = append(lines, "  "+cDim.Render("·  "+p))
		}
	}
	lines = append(lines, "")
	if allDone {
		lines = append(lines, cGreen.Render(center("Shutdown complete — state saved.", inner)))
		lines = append(lines, cDim.Render(center("Re-run the same command to resume.", inner)))
	} else {
		lines = append(lines, cYellow.Render(center("Please wait. Do not close this terminal.", inner)))
		lines = append(lines, cDim.Render(center("Ctrl+C again forces an immediate exit.", inner)))
	}
	lines = append(lines, cDim.Render(center(fmt.Sprintf("elapsed %s", util.FmtDHMS(float64(elapsed))), inner)))

	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).
		Width(inner).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}

// mailboxRows applies the Mailboxes view's filter (f), sort (s) and search.
func (m *Model) mailboxRows() []engine.MBValues {
	rows := m.snap.Mailboxes
	if q := strings.ToLower(m.viewSearch(vMailboxes)); q != "" {
		var out []engine.MBValues
		for _, r := range rows {
			if strings.Contains(strings.ToLower(r.Label), q) ||
				strings.Contains(strings.ToLower(r.Status), q) {
				out = append(out, r)
			}
		}
		rows = out
	}
	switch m.mailFilter {
	case 1: // problems
		var out []engine.MBValues
		for _, r := range rows {
			switch r.Status {
			case "FAILED", "PARTIAL", "STALE", "WARNINGS", "RETRYING":
				out = append(out, r)
			}
		}
		rows = out
	case 2: // active
		var out []engine.MBValues
		for _, r := range rows {
			switch r.Status {
			case "RUNNING", "RETRYING", "REMOTE":
				out = append(out, r)
			}
		}
		rows = out
	}
	if m.mailSort > 0 {
		rows = append([]engine.MBValues(nil), rows...)
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := rows[i], rows[j]
			switch m.mailSort {
			case 1:
				if a.Status != b.Status {
					return a.Status < b.Status
				}
			case 2:
				ap, bp := pctOf(a), pctOf(b)
				if ap != bp {
					return ap > bp
				}
			}
			return a.Index < b.Index
		})
	}
	return rows
}

func pctOf(m engine.MBValues) float64 {
	if m.MsgsTotal <= 0 {
		return 0
	}
	return float64(m.MsgsDone) / float64(m.MsgsTotal)
}

// popupBox renders the modal detail box over the current frame.
func (m *Model) popupBox(w, h int) string {
	inner := 60
	if inner > w-6 {
		inner = w - 6
	}
	// A live popup is re-rendered from the CURRENT snapshot every frame; a
	// static one shows what it captured when it opened.
	body := m.popup.Lines
	live := m.popup.Live != ""
	if live {
		body = m.mailboxInspector(m.popup.Live)
	}
	var lines []string
	lines = append(lines, cBold.Render(center(m.popup.Title, inner)))
	lines = append(lines, "")
	for _, l := range body {
		lines = append(lines, " "+clip(l, inner-2))
	}
	lines = append(lines, "")
	hint := "Esc / ⏎ close"
	if live {
		hint = "live — updating · Esc / ⏎ back to the list"
	}
	lines = append(lines, cDim.Render(center(hint, inner)))
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).
		Width(inner).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}
