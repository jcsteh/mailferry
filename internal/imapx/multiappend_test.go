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

// Protocol-level MULTIAPPEND tests: the exact bytes MailFerry puts on the
// wire are checked against the RFC 3502 grammar, and the RFC 4315 APPENDUID
// UID-set rules are checked directly. These are deliberately independent of
// the migration engine — a protocol regression should fail here, precisely,
// rather than as a mysterious end-to-end symptom.
package imapx

import (
	"bufio"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingServer is a minimal IMAP responder that records the command lines
// it receives and consumes announced literals correctly.
type recordingServer struct {
	ln   net.Listener
	caps string
	// reply is the tagged completion sent after the command ends.
	reply string

	mu    sync.Mutex
	lines []string
}

// record appends a received line under the lock.
func (s *recordingServer) record(line string) {
	s.mu.Lock()
	s.lines = append(s.lines, line)
	s.mu.Unlock()
}

// recorded returns a snapshot of the lines received so far.
func (s *recordingServer) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

// lastTag returns the most recent tagged command seen.
func (s *recordingServer) lastTag() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.lines) - 1; i >= 0; i-- {
		if f := strings.Fields(s.lines[i]); len(f) > 0 && strings.HasPrefix(f[0], "MF") {
			return f[0]
		}
	}
	return ""
}

func newRecordingServer(t *testing.T, caps, reply string) *recordingServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &recordingServer{ln: ln, caps: caps, reply: reply}
	go s.serve()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *recordingServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

var litRe = regexp.MustCompile(`\{(\d+)(\+)?\}$`)

func (s *recordingServer) serve() {
	conn, err := s.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	fmt.Fprintf(conn, "* OK [CAPABILITY %s] ready\r\n", s.caps)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		s.record(line)
		if m := litRe.FindStringSubmatch(line); m != nil {
			n := 0
			fmt.Sscanf(m[1], "%d", &n)
			if m[2] == "" { // synchronising literal
				fmt.Fprint(conn, "+ go ahead\r\n")
			}
			buf := make([]byte, n)
			if _, err := ioReadFull(br, buf); err != nil {
				return
			}
			continue // the rest of the command follows
		}
		// A line with no literal ends a command. Answer the first tagged one.
		if strings.HasPrefix(line, "MF") {
			tag := strings.Fields(line)[0]
			fmt.Fprintf(conn, "%s %s\r\n", tag, s.reply)
		} else if line == "" && s.reply != "" {
			// command terminated after a literal: reply to the open tag
			if tag := s.lastTag(); tag != "" {
				fmt.Fprintf(conn, "%s %s\r\n", tag, s.reply)
			}
		}
	}
}

func connectTo(t *testing.T, port int) *Client {
	t.Helper()
	c := NewClient(Endpoint{Host: "127.0.0.1", Port: port, Security: "none"},
		5*time.Second, true, nil, "test", nil)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Abort(nil) })
	return c
}

// TestMultiAppendWireSyntax pins the emitted command against the RFC 3502
// grammar: APPEND SP mailbox 1*( [SP flags] [SP date] SP literal ), one
// literal announcement per message, and CRLF terminating the command.
func TestMultiAppendWireSyntax(t *testing.T) {
	s := newRecordingServer(t, "IMAP4rev1 LITERAL+ MULTIAPPEND UIDPLUS",
		"OK [APPENDUID 42 10:11] done")
	c := connectTo(t, s.port())
	if !c.SupportsMultiAppend() {
		t.Fatal("MULTIAPPEND capability was advertised but not detected")
	}
	ma, err := c.MultiAppendBegin("Archive")
	if err != nil {
		t.Fatal(err)
	}
	if err := ma.AddBegin(`\Seen`, "17-Jul-2026 10:00:00 +0000", 5); err != nil {
		t.Fatal(err)
	}
	if err := ma.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := ma.AddBegin("", "18-Jul-2026 11:00:00 +0000", 3); err != nil {
		t.Fatal(err)
	}
	if err := ma.Write([]byte("bye")); err != nil {
		t.Fatal(err)
	}
	p, err := ma.Finish()
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Wait()
	if err != nil {
		t.Fatal(err)
	}
	c.AppendDone()

	lines := s.recorded()
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 recorded lines, got %q", lines)
	}
	first := lines[len(lines)-3]
	second := lines[len(lines)-2]
	last := lines[len(lines)-1]
	wantFirst := `APPEND "Archive" (\Seen) "17-Jul-2026 10:00:00 +0000" {5+}`
	if !strings.HasSuffix(first, wantFirst) {
		t.Fatalf("first append-message:\n got  %q\n want suffix %q", first, wantFirst)
	}
	// The continuation message must begin with a SPACE and carry no mailbox.
	wantSecond := ` "18-Jul-2026 11:00:00 +0000" {3+}`
	if second != wantSecond {
		t.Fatalf("second append-message:\n got  %q\n want %q", second, wantSecond)
	}
	if last != "" {
		t.Fatalf("the command must be terminated by a bare CRLF, got %q", last)
	}
	if uids := AppendUIDsOf(res, 2); len(uids) != 2 || uids[0] != 10 || uids[1] != 11 {
		t.Fatalf("APPENDUID uid-set not mapped to messages: %v", uids)
	}
}

// TestMultiAppendSynchronisingLiterals: without LITERAL+ each literal is
// announced and waits for the server's continuation, as RFC 3501 requires.
func TestMultiAppendSynchronisingLiterals(t *testing.T) {
	s := newRecordingServer(t, "IMAP4rev1 MULTIAPPEND", "OK done")
	c := connectTo(t, s.port())
	ma, err := c.MultiAppendBegin("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"one", "two"} {
		if err := ma.AddBegin("", "", int64(len(body))); err != nil {
			t.Fatalf("synchronising literal: %v", err)
		}
		if err := ma.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	p, err := ma.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	c.AppendDone()
	for _, l := range s.recorded() {
		if strings.Contains(l, "+}") {
			t.Fatalf("non-synchronising literal used without LITERAL+: %q", l)
		}
	}
}

// TestMultiAppendRefusedWithoutCapability: MailFerry never issues the
// command speculatively.
func TestMultiAppendRefusedWithoutCapability(t *testing.T) {
	s := newRecordingServer(t, "IMAP4rev1 LITERAL+", "OK done")
	c := connectTo(t, s.port())
	if c.SupportsMultiAppend() {
		t.Fatal("MULTIAPPEND must not be assumed when it is not advertised")
	}
	if _, err := c.MultiAppendBegin("INBOX"); err == nil {
		t.Fatal("MultiAppendBegin must refuse when the capability is absent")
	}
}

// TestMultiAppendBaselineDisables: --baseline is RFC-3501-only, so an
// extension is off even when advertised.
func TestMultiAppendBaselineDisables(t *testing.T) {
	s := newRecordingServer(t, "IMAP4rev1 MULTIAPPEND", "OK done")
	c := connectTo(t, s.port())
	c.Baseline = true
	if c.SupportsMultiAppend() {
		t.Fatal("baseline mode must not use the MULTIAPPEND extension")
	}
}

// TestMultiAppendDryRunBlocked: the read-only choke point refuses before a
// single byte can reach the socket.
func TestMultiAppendDryRunBlocked(t *testing.T) {
	s := newRecordingServer(t, "IMAP4rev1 LITERAL+ MULTIAPPEND", "OK done")
	c := connectTo(t, s.port())
	c.ReadOnly = true
	before := len(s.recorded())
	_, err := c.MultiAppendBegin("INBOX")
	if err == nil || !IsDryRunBlocked(err) {
		t.Fatalf("dry run must block MULTIAPPEND at the choke point, got %v", err)
	}
	if now := len(s.recorded()); now != before {
		t.Fatalf("dry run wrote %d line(s) to the socket", now-before)
	}
}

// TestMultiAppendEmptyBatchSendsNothing: a batch that ends up with no
// messages must not leave a half-open command (which is why the command is
// opened lazily, on the first message).
func TestMultiAppendEmptyBatchSendsNothing(t *testing.T) {
	s := newRecordingServer(t, "IMAP4rev1 LITERAL+ MULTIAPPEND", "OK done")
	c := connectTo(t, s.port())
	before := len(s.recorded())
	ma, err := c.MultiAppendBegin("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	p, err := ma.Finish()
	if err != nil || p != nil {
		t.Fatalf("an empty batch must finish silently, got pending=%v err=%v", p, err)
	}
	if now := len(s.recorded()); now != before {
		t.Fatalf("an empty batch wrote %d line(s)", now-before)
	}
}

// TestMultiAppendZeroLengthRefused: RFC 3502 makes a zero-length literal an
// error that cancels the whole command, so MailFerry never sends one.
func TestMultiAppendZeroLengthRefused(t *testing.T) {
	s := newRecordingServer(t, "IMAP4rev1 LITERAL+ MULTIAPPEND", "OK done")
	c := connectTo(t, s.port())
	ma, _ := c.MultiAppendBegin("INBOX")
	if err := ma.AddBegin("", "", 0); err == nil {
		t.Fatal("a zero-length literal must be refused before it reaches the wire")
	}
}

// --- APPENDUID (RFC 4315) --------------------------------------------------

func TestAppendUIDSetParsing(t *testing.T) {
	cases := []struct {
		set  string
		want int
		uids []int64
	}{
		{"10:12", 3, []int64{10, 11, 12}},
		{"10,12,14", 3, []int64{10, 12, 14}},
		{"7:8,20", 3, []int64{7, 8, 20}},
		{"5", 1, []int64{5}},
	}
	for _, c := range cases {
		res := Result{Final: &Response{Code: []any{"APPENDUID", "42", c.set}}}
		got := AppendUIDsOf(res, c.want)
		if len(got) != len(c.uids) {
			t.Fatalf("%q -> %v, want %v", c.set, got, c.uids)
		}
		for i := range got {
			if got[i] != c.uids[i] {
				t.Fatalf("%q -> %v, want %v", c.set, got, c.uids)
			}
		}
	}
	// A count mismatch must yield nothing rather than a wrong mapping: a
	// guessed destination UID would corrupt flag sync and reconciliation.
	res := Result{Final: &Response{Code: []any{"APPENDUID", "42", "10:12"}}}
	if got := AppendUIDsOf(res, 4); got != nil {
		t.Fatalf("a mismatched uid-set must be discarded, got %v", got)
	}
	// A wildcard is forbidden by RFC 4315 and cannot be mapped.
	res = Result{Final: &Response{Code: []any{"APPENDUID", "42", "10:*"}}}
	if got := AppendUIDsOf(res, 2); got != nil {
		t.Fatalf("a wildcard uid-set must be discarded, got %v", got)
	}
	// No UIDPLUS at all: no UIDs, no guessing.
	if got := AppendUIDsOf(Result{Final: &Response{}}, 3); got != nil {
		t.Fatalf("missing APPENDUID must yield nil, got %v", got)
	}
}
