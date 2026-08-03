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

// Package fakeimap: an in-memory IMAP server for the automated test suite,
// with poison-message and stall behaviours for resilience regression tests.
package fakeimap

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Msg struct {
	UID          uint32
	Body         []byte
	Flags        map[string]bool
	InternalDate string
}

func (m *Msg) HeaderFields(wanted []string) []byte {
	end := bytes.Index(m.Body, []byte("\r\n\r\n"))
	header := m.Body
	if end >= 0 {
		header = m.Body[:end+2]
	}
	var out [][]byte
	take := false
	for _, line := range bytes.Split(header, []byte("\r\n")) {
		if len(line) == 0 {
			break
		}
		if line[0] == ' ' || line[0] == '\t' {
			if take {
				out = append(out, line)
			}
			continue
		}
		name := strings.ToLower(string(bytes.SplitN(line, []byte(":"), 2)[0]))
		take = false
		for _, w := range wanted {
			if name == w {
				take = true
				break
			}
		}
		if take {
			out = append(out, line)
		}
	}
	joined := bytes.Join(out, []byte("\r\n"))
	if len(joined) > 0 {
		return append(joined, []byte("\r\n\r\n")...)
	}
	return []byte("\r\n")
}

type Folder struct {
	Name        string
	Attrs       []string
	UIDValidity uint32
	UIDNext     uint32
	Msgs        []*Msg
}

func NewFolder(name string, uidvalidity uint32, attrs ...string) *Folder {
	return &Folder{Name: name, UIDValidity: uidvalidity, UIDNext: 1, Attrs: attrs}
}

func (f *Folder) Add(body []byte, flags []string, internalDate string) *Msg {
	m := &Msg{UID: f.UIDNext, Body: body, Flags: map[string]bool{},
		InternalDate: internalDate}
	for _, fl := range flags {
		m.Flags[fl] = true
	}
	f.UIDNext++
	f.Msgs = append(f.Msgs, m)
	return m
}

// remove deletes the given messages from the folder (UID MOVE source side).
// The messages are rehomed, not lost — MOVE is a relocation, not a purge.
func (f *Folder) remove(victims []*Msg) {
	if len(victims) == 0 {
		return
	}
	drop := map[*Msg]bool{}
	for _, m := range victims {
		drop[m] = true
	}
	kept := f.Msgs[:0]
	for _, m := range f.Msgs {
		if !drop[m] {
			kept = append(kept, m)
		}
	}
	f.Msgs = kept
}

func (f *Folder) bySet(set string) []*Msg {
	var out []*Msg
	for _, part := range strings.Split(set, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.Index(part, ":"); i >= 0 {
			lo, _ := strconv.ParseUint(part[:i], 10, 32)
			hiS := part[i+1:]
			var hi uint64
			if hiS == "*" {
				hi = uint64(f.UIDNext)
			} else {
				hi, _ = strconv.ParseUint(hiS, 10, 32)
			}
			for _, m := range f.Msgs {
				if uint64(m.UID) >= lo && uint64(m.UID) <= hi {
					out = append(out, m)
				}
			}
		} else {
			u, _ := strconv.ParseUint(part, 10, 32)
			for _, m := range f.Msgs {
				if uint64(m.UID) == u {
					out = append(out, m)
				}
			}
		}
	}
	return out
}

type Account struct {
	User     string
	Password string
	// OAuthToken, when set, enables SASL XOAUTH2 for this account: a client
	// presenting exactly this bearer token authenticates. Password LOGIN
	// still works too, so a single fixture can exercise both mechanisms.
	OAuthToken string
	mu         sync.Mutex
	Folders    map[string]*Folder
	order      []string
}

func NewAccount(user, password string) *Account {
	a := &Account{User: user, Password: password, Folders: map[string]*Folder{}}
	a.AddFolder(NewFolder("INBOX", 1111))
	return a
}

func (a *Account) AddFolder(f *Folder) *Folder {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Folders[f.Name] = f
	a.order = append(a.order, f.Name)
	return f
}

func (a *Account) Folder(name string) *Folder {
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.EqualFold(name, "INBOX") {
		for k, f := range a.Folders {
			if strings.EqualFold(k, "INBOX") {
				return f
			}
		}
	}
	return a.Folders[name]
}

func (a *Account) TotalMsgs() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, f := range a.Folders {
		n += len(f.Msgs)
	}
	return n
}

// Server is one fake IMAP endpoint.
type Server struct {
	Accounts map[string]*Account
	Caps     []string
	Addr     string
	// ServerName / ServerVersion are what this server reports for itself in
	// its RFC 2971 ID response. Empty ServerName answers NIL — a server that
	// declines to identify itself, which MailFerry must render as "Unknown"
	// rather than inferring anything from the hostname.
	ServerName    string
	ServerVersion string

	AppendCount   atomic.Int64 // MESSAGES appended (one per message, always)
	AppendCmds    atomic.Int64 // APPEND *commands* seen (a MULTIAPPEND counts once)
	MaxBatchSeen  atomic.Int64 // largest message count in a single APPEND command
	MultiAppendN  atomic.Int64 // APPEND commands that carried more than one message
	FetchBodyN    atomic.Int64
	KillCount     atomic.Int64
	CopyCount     atomic.Int64 // UID COPY commands honoured (dedup quarantine)
	MoveCount     atomic.Int64 // UID MOVE commands honoured (dedup quarantine)
	ExpungeCount  atomic.Int64 // EXPUNGE commands seen — dedup must keep this 0
	NoMove        atomic.Bool  // hide the MOVE capability (force copy+flag path)
	CopyDelayMS   atomic.Int64 // artificial per-COPY/MOVE latency (dedup cancel tests)
	AppendReject  []byte       // bodies containing this get "NO APPEND failed"
	AppendKill    []byte       // bodies containing this kill the connection
	AppendDelayMS atomic.Int64 // artificial per-append latency (slow-server tests)
	DropAfterOKN  atomic.Int64 // kill AFTER sending OK for the Nth append (ack-loss test)
	StallAfterN   atomic.Int64 // after N body fetches, stop responding (hung server)
	StallOnce     atomic.Bool  // clear StallAfterN after the first stall (recover next conn)
	// MULTIAPPEND behaviours. NoMultiAppend hides the capability entirely;
	// MultiAppendBroken advertises it and then rejects it, which is the
	// dangerous "server lied" case MailFerry must survive without
	// duplicating mail. MultiAppendBAD makes that rejection a BAD (a parse
	// failure) rather than a NO.
	NoMultiAppend     atomic.Bool
	MultiAppendBroken atomic.Bool
	MultiAppendBAD    atomic.Bool
	// DropBeforeOKBatch kills the connection after a MULTIAPPEND of this
	// many messages has been fully stored but BEFORE the tagged OK is sent —
	// the acknowledgement-loss case for a whole batch.
	DropBeforeOKBatch atomic.Int64
	StallCount        atomic.Int64
	CompressConns     atomic.Int64 // connections that negotiated COMPRESS=DEFLATE
	storeMu           sync.Mutex
	StoreLog          []string // "folder set items" per UID STORE (flag-sync tests)
	mu                sync.Mutex
	ln                net.Listener
}

// StoreEvents returns a copy of the UID STORE audit log.
func (s *Server) StoreEvents() []string {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	return append([]string(nil), s.StoreLog...)
}

// capString renders the advertised capability list, honouring NoMove so
// tests can force the copy+flag quarantine path on MOVE-less servers, and
// advertising AUTH=XOAUTH2 whenever any account has an OAuth token set.
func (s *Server) capString() string {
	caps := append([]string(nil), s.Caps...)
	if s.offersXOAUTH2() {
		caps = append(caps, "AUTH=XOAUTH2")
	}
	var out []string
	for _, c := range caps {
		if s.NoMove.Load() && strings.EqualFold(c, "MOVE") {
			continue
		}
		if s.NoMultiAppend.Load() && strings.EqualFold(c, "MULTIAPPEND") {
			continue
		}
		out = append(out, c)
	}
	return strings.Join(out, " ")
}

// offersXOAUTH2 reports whether any account accepts a bearer token.
func (s *Server) offersXOAUTH2() bool {
	for _, a := range s.Accounts {
		if a.OAuthToken != "" {
			return true
		}
	}
	return false
}

// authXOAUTH2 decodes a base64 XOAUTH2 initial response and returns the
// matching account when the embedded bearer token is exactly the account's
// OAuthToken. Format: "user=<u>\x01auth=Bearer <token>\x01\x01".
func (s *Server) authXOAUTH2(ir string) *Account {
	raw, err := base64.StdEncoding.DecodeString(ir)
	if err != nil {
		return nil
	}
	var user, token string
	for _, part := range strings.Split(string(raw), "\x01") {
		switch {
		case strings.HasPrefix(part, "user="):
			user = strings.TrimPrefix(part, "user=")
		case strings.HasPrefix(part, "auth=Bearer "):
			token = strings.TrimPrefix(part, "auth=Bearer ")
		}
	}
	a := s.Accounts[user]
	if a != nil && a.OAuthToken != "" && a.OAuthToken == token {
		return a
	}
	return nil
}

func NewServer(accounts ...*Account) *Server {
	s := &Server{Accounts: map[string]*Account{},
		Caps: []string{"IMAP4rev1", "UIDPLUS", "LITERAL+", "NAMESPACE",
			"SPECIAL-USE", "ID", "UNSELECT", "COMPRESS=DEFLATE", "MOVE",
			"MULTIAPPEND"}}
	for _, a := range accounts {
		s.Accounts[a.User] = a
	}
	return s
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.ln = ln
	s.Addr = ln.Addr().String()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.client(conn)
		}
	}()
	return nil
}

func (s *Server) Port() int {
	_, p, _ := net.SplitHostPort(s.Addr)
	n, _ := strconv.Atoi(p)
	return n
}

func (s *Server) Stop() {
	if s.ln != nil {
		s.ln.Close()
	}
}

var litRe = regexp.MustCompile(`\{(\d+)(\+)?\}$`)
var quotedOrAtom = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"|(\S+)`)

func toks(s string) []string {
	var out []string
	for _, m := range quotedOrAtom.FindAllStringSubmatch(s, -1) {
		if m[1] != "" || strings.HasPrefix(m[0], `"`) {
			v := strings.ReplaceAll(m[1], `\"`, `"`)
			out = append(out, strings.ReplaceAll(v, `\\`, `\`))
		} else {
			out = append(out, m[2])
		}
	}
	return out
}

func (s *Server) client(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	var acct *Account
	var selected *Folder
	var out io.Writer = conn
	var zw *flate.Writer
	send := func(line string) bool {
		_, err := out.Write([]byte(line + "\r\n"))
		if err == nil && zw != nil {
			err = zw.Flush()
		}
		return err == nil
	}
	sendRaw := func(b []byte) bool {
		_, err := out.Write(b)
		if err == nil && zw != nil {
			err = zw.Flush()
		}
		return err == nil
	}
	send("* OK [CAPABILITY " + s.capString() + "] fake server ready")
	for {
		raw, err := br.ReadString('\n')
		if err != nil {
			return
		}
		raw = strings.TrimRight(raw, "\r\n")
		var lits [][]byte
		for {
			m := litRe.FindStringSubmatchIndex(raw)
			if m == nil {
				break
			}
			n, _ := strconv.Atoi(raw[m[2]:m[3]])
			if m[4] < 0 { // synchronising literal
				send("+ OK")
			}
			buf := make([]byte, n)
			if _, err := readFull(br, buf); err != nil {
				return
			}
			lits = append(lits, buf)
			rest, err := br.ReadString('\n')
			if err != nil {
				return
			}
			raw = raw[:m[0]] + "\x00LIT\x00" + strings.TrimRight(rest, "\r\n")
		}
		parts := strings.SplitN(raw, " ", 3)
		if len(parts) < 2 {
			continue
		}
		tag, verb := parts[0], strings.ToUpper(parts[1])
		rest := ""
		if len(parts) > 2 {
			rest = parts[2]
		}
		if verb == "UID" && rest != "" {
			sub, r2, _ := strings.Cut(rest, " ")
			verb = "UID " + strings.ToUpper(sub)
			rest = r2
		}
		switch verb {
		case "CAPABILITY":
			send("* CAPABILITY " + s.capString())
			send(tag + " OK done")
		case "NOOP":
			send(tag + " OK done")
		case "ID":
			// RFC 2971. ServerName lets a test present itself as any
			// product — or as nothing, to exercise the "Unknown" path that
			// MailFerry must prefer over guessing.
			if s.ServerName == "" {
				send("* ID NIL")
			} else {
				send(fmt.Sprintf(`* ID ("name" "%s" "version" "%s")`,
					s.ServerName, s.ServerVersion))
			}
			send(tag + " OK done")
		case "NAMESPACE":
			send(`* NAMESPACE (("" "/")) NIL NIL`)
			send(tag + " OK done")
		case "COMPRESS":
			if zw != nil {
				send(tag + " NO [COMPRESSIONACTIVE] already compressed")
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(rest), "DEFLATE") {
				send(tag + " BAD unsupported compression")
				continue
			}
			send(tag + " OK DEFLATE active")
			zw, _ = flate.NewWriter(conn, 6)
			out = zw
			br = bufio.NewReader(flate.NewReader(br))
			s.CompressConns.Add(1)
		case "LOGIN":
			t := toks(rest)
			if len(t) >= 2 {
				if a := s.Accounts[t[0]]; a != nil && a.Password == t[1] {
					acct = a
					send(tag + " OK [CAPABILITY " + s.capString() + "] logged in")
					continue
				}
			}
			send(tag + " NO [AUTHENTICATIONFAILED] bad credentials")
		case "AUTHENTICATE":
			// SASL XOAUTH2 with the initial-response (SASL-IR) form both
			// first-class providers use: AUTHENTICATE XOAUTH2 <base64>.
			mech, ir, _ := strings.Cut(strings.TrimSpace(rest), " ")
			if !strings.EqualFold(mech, "XOAUTH2") {
				send(tag + " NO unsupported SASL mechanism")
				continue
			}
			if a := s.authXOAUTH2(ir); a != nil {
				acct = a
				send(tag + " OK [CAPABILITY " + s.capString() + "] authenticated")
				continue
			}
			// Provider behaviour on a bad token: a base64 error continuation,
			// then wait for the client's empty line before the tagged NO.
			send("+ eyJzdGF0dXMiOiI0MDEiLCJzY2hlbWVzIjoiQmVhcmVyIn0=")
			if _, rerr := br.ReadString('\n'); rerr != nil {
				return
			}
			send(tag + " NO [AUTHENTICATIONFAILED] invalid bearer token")
		case "LOGOUT":
			send("* BYE bye")
			send(tag + " OK done")
			return
		default:
			if acct == nil {
				send(tag + " NO not authenticated")
				continue
			}
			switch verb {
			case "LIST":
				acct.mu.Lock()
				names := append([]string(nil), acct.order...)
				acct.mu.Unlock()
				for _, name := range names {
					f := acct.Folders[name]
					send(fmt.Sprintf(`* LIST (%s) "/" "%s"`, strings.Join(f.Attrs, " "), name))
				}
				send(tag + " OK done")
			case "SUBSCRIBE":
				send(tag + " OK done")
			case "CREATE":
				t := toks(rest)
				if len(t) > 0 {
					if acct.Folder(t[0]) != nil {
						send(tag + " NO [ALREADYEXISTS] exists")
					} else {
						acct.AddFolder(NewFolder(t[0], 2222+uint32(len(acct.Folders))))
						send(tag + " OK created")
					}
				}
			case "STATUS":
				t := toks(rest)
				f := acct.Folder(t[0])
				if f == nil {
					send(tag + " NO no such folder")
					continue
				}
				var size int64
				for _, m := range f.Msgs {
					size += int64(len(m.Body))
				}
				send(fmt.Sprintf(`* STATUS "%s" (MESSAGES %d UIDNEXT %d UIDVALIDITY %d)`,
					t[0], len(f.Msgs), f.UIDNext, f.UIDValidity))
				send(tag + " OK done")
			case "SELECT", "EXAMINE":
				t := toks(rest)
				f := acct.Folder(t[0])
				if f == nil {
					send(tag + " NO no such folder")
					continue
				}
				selected = f
				send(fmt.Sprintf("* %d EXISTS", len(f.Msgs)))
				send("* 0 RECENT")
				send(`* FLAGS (\Answered \Flagged \Deleted \Seen \Draft)`)
				send(fmt.Sprintf("* OK [UIDVALIDITY %d] ok", f.UIDValidity))
				send(fmt.Sprintf("* OK [UIDNEXT %d] ok", f.UIDNext))
				ro := "READ-WRITE"
				if verb == "EXAMINE" {
					ro = "READ-ONLY"
				}
				send(tag + " OK [" + ro + "] done")
			case "UNSELECT":
				selected = nil
				send(tag + " OK done")
			case "UID SEARCH":
				if selected == nil {
					send(tag + " NO nothing selected")
					continue
				}
				var b strings.Builder
				b.WriteString("* SEARCH")
				for _, m := range selected.Msgs {
					fmt.Fprintf(&b, " %d", m.UID)
				}
				send(b.String())
				send(tag + " OK done")
			case "UID FETCH":
				if selected == nil {
					send(tag + " NO nothing selected")
					continue
				}
				set, items, _ := strings.Cut(rest, " ")
				itemsUp := strings.ToUpper(items)
				wantHdr := strings.Contains(itemsUp, "HEADER.FIELDS")
				wantBody := strings.Contains(itemsUp, "BODY.PEEK[]") ||
					strings.Contains(itemsUp, "BODY[]")
				var fields []string
				if wantHdr {
					if mm := regexp.MustCompile(`(?i)HEADER\.FIELDS \(([^)]*)\)`).
						FindStringSubmatch(items); mm != nil {
						for _, f := range strings.Fields(mm[1]) {
							fields = append(fields, strings.ToLower(f))
						}
					}
				}
				for seq, m := range selected.bySet(set) {
					var attrs []string
					attrs = append(attrs, fmt.Sprintf("UID %d", m.UID))
					if strings.Contains(itemsUp, "FLAGS") {
						var fl []string
						for f := range m.Flags {
							fl = append(fl, f)
						}
						attrs = append(attrs, "FLAGS ("+strings.Join(fl, " ")+")")
					}
					if strings.Contains(itemsUp, "INTERNALDATE") {
						attrs = append(attrs, `INTERNALDATE "`+m.InternalDate+`"`)
					}
					if strings.Contains(itemsUp, "RFC822.SIZE") {
						attrs = append(attrs, fmt.Sprintf("RFC822.SIZE %d", len(m.Body)))
					}
					if wantHdr {
						data := m.HeaderFields(fields)
						sendRaw([]byte(fmt.Sprintf("* %d FETCH (%s BODY[HEADER.FIELDS (MESSAGE-ID DATE FROM TO SUBJECT)] {%d}\r\n",
							seq+1, strings.Join(attrs, " "), len(data))))
						sendRaw(data)
						sendRaw([]byte(")\r\n"))
					} else if wantBody {
						n := s.FetchBodyN.Add(1)
						if sa := s.StallAfterN.Load(); sa > 0 && n > sa {
							s.StallCount.Add(1)
							if s.StallOnce.Load() {
								s.StallAfterN.Store(0) // next connection recovers
							}
							time.Sleep(3600 * time.Second) // hung server: never reply
						}
						sendRaw([]byte(fmt.Sprintf("* %d FETCH (%s BODY[] {%d}\r\n",
							seq+1, strings.Join(attrs, " "), len(m.Body))))
						sendRaw(m.Body)
						sendRaw([]byte(")\r\n"))
					} else {
						send(fmt.Sprintf("* %d FETCH (%s)", seq+1, strings.Join(attrs, " ")))
					}
				}
				send(tag + " OK fetch done")
			case "UID STORE":
				if selected == nil {
					send(tag + " NO nothing selected")
					continue
				}
				set, items, _ := strings.Cut(rest, " ")
				s.storeMu.Lock()
				s.StoreLog = append(s.StoreLog, selected.Name+" "+set+" "+items)
				s.storeMu.Unlock()
				if mm := regexp.MustCompile(`\(([^)]*)\)`).FindStringSubmatch(items); mm != nil {
					for _, m := range selected.bySet(set) {
						m.Flags = map[string]bool{}
						for _, f := range strings.Fields(mm[1]) {
							m.Flags[f] = true
						}
					}
				}
				send(tag + " OK done")
			case "UID COPY", "UID MOVE":
				if selected == nil {
					send(tag + " NO nothing selected")
					continue
				}
				cset, destName, _ := strings.Cut(rest, " ")
				dest := acct.Folder(unquote(destName))
				if dest == nil {
					// RFC 3501/6851: point the client at TRYCREATE so it can
					// CREATE the quarantine folder and retry.
					send(tag + " NO [TRYCREATE] no such destination mailbox")
					continue
				}
				msgs := selected.bySet(cset)
				if d := s.CopyDelayMS.Load(); d > 0 {
					time.Sleep(time.Duration(d) * time.Millisecond)
				}
				acct.mu.Lock()
				for _, m := range msgs {
					dest.Add(append([]byte(nil), m.Body...), flagsOf(m), m.InternalDate)
				}
				if verb == "UID MOVE" {
					selected.remove(msgs)
				}
				acct.mu.Unlock()
				if verb == "UID MOVE" {
					s.MoveCount.Add(int64(len(msgs)))
					// RFC 6851: an atomic rehome of retained mail, not a
					// destructive EXPUNGE.
					send(tag + " OK move done")
				} else {
					s.CopyCount.Add(int64(len(msgs)))
					send(tag + " OK copy done")
				}
			case "EXPUNGE", "UID EXPUNGE":
				// Recorded so dedup tests can assert it is NEVER issued —
				// permanent deletion is not part of the reversible design.
				s.ExpungeCount.Add(1)
				send(tag + " OK expunge done")
			case "APPEND":
				// One APPEND command carries 1..N messages (RFC 3502
				// MULTIAPPEND). Each literal was replaced by a \x00LIT\x00
				// marker above, so the segments around the markers are the
				// per-message [flags] [date] parts, in order.
				segs := strings.Split(rest, "\x00LIT\x00")
				t := toks(segs[0])
				if len(t) == 0 {
					send(tag + " BAD append")
					continue
				}
				f := acct.Folder(t[0])
				if f == nil {
					send(tag + " NO [TRYCREATE] no such folder")
					continue
				}
				s.AppendCmds.Add(1)
				if n := int64(len(lits)); n > 1 {
					s.MultiAppendN.Add(1)
					for {
						cur := s.MaxBatchSeen.Load()
						if n <= cur || s.MaxBatchSeen.CompareAndSwap(cur, n) {
							break
						}
					}
					// "Advertised but malfunctioning": the capability was
					// offered and the command is now refused anyway. RFC 3502
					// atomicity means NOTHING may be stored.
					if s.MultiAppendBroken.Load() {
						if s.MultiAppendBAD.Load() {
							send(tag + " BAD APPEND unexpected literal")
						} else {
							send(tag + " NO MULTIAPPEND not really supported")
						}
						continue
					}
				}
				// The simulated store cost is PER MESSAGE, not per command:
				// a real server storing eight messages in one MULTIAPPEND
				// does eight messages' worth of work.
				if d := s.AppendDelayMS.Load(); d > 0 {
					time.Sleep(time.Duration(d) * time.Duration(len(lits)) * time.Millisecond)
				}
				// Validate the WHOLE command before storing anything: RFC
				// 3502 forbids partial appending, so a rejection must leave
				// the mailbox exactly as it was.
				for _, body := range lits {
					if len(s.AppendKill) > 0 && bytes.Contains(body, s.AppendKill) {
						s.KillCount.Add(1)
						return // content filter RSTs the line
					}
				}
				rejected := false
				for _, body := range lits {
					if len(s.AppendReject) > 0 && bytes.Contains(body, s.AppendReject) {
						rejected = true
					}
				}
				if rejected {
					send(tag + " NO APPEND failed") // atomic: nothing stored
					continue
				}
				var stored []*Msg
				acct.mu.Lock()
				for i, body := range lits {
					head := segs[0]
					if i > 0 {
						head = segs[i]
					}
					stored = append(stored, f.Add(body, appendFlags(head),
						appendDate(head, i == 0)))
				}
				acct.mu.Unlock()
				n := s.AppendCount.Add(int64(len(lits)))
				if v := s.DropBeforeOKBatch.Load(); v > 0 && int64(len(lits)) >= v {
					// Stored, but the acknowledgement never arrives: the
					// ambiguous-batch case reconciliation must resolve.
					return
				}
				send(fmt.Sprintf("%s OK [APPENDUID %d %s] append done",
					tag, f.UIDValidity, appendUIDSet(stored)))
				if v := s.DropAfterOKN.Load(); v > 0 && n >= v && n-int64(len(lits)) < v {
					return // ack sent, then the line dies (dup-prevention test)
				}
			default:
				send(tag + " BAD unknown command " + verb)
			}
		}
	}
}

// appendFlags extracts the optional flag-list of one append-message.
func appendFlags(head string) []string {
	var flags []string
	if mm := regexp.MustCompile(`\(([^)]*)\)`).FindStringSubmatch(head); mm != nil {
		for _, fl := range strings.Fields(mm[1]) {
			if !strings.EqualFold(fl, `\Recent`) {
				flags = append(flags, fl)
			}
		}
	}
	return flags
}

// appendDate extracts the optional date-time of one append-message. On the
// FIRST message the segment also contains the quoted mailbox name, so the
// date is the LAST quoted string and only counts when there is more than
// one; later segments carry the date alone.
func appendDate(head string, first bool) string {
	q := regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`).FindAllStringSubmatch(head, -1)
	want := 1
	if first {
		want = 2 // mailbox + date
	}
	if len(q) >= want {
		return q[len(q)-1][1]
	}
	return "17-Jul-2026 10:00:00 +0000"
}

// appendUIDSet renders the APPENDUID value. RFC 4315 is explicit that the
// UID SET form MUST NOT be used for a single message ("a server MUST NOT
// send a range such as 123:123"), so a one-message APPEND reports one UID —
// which is exactly the case a client without MULTIAPPEND relies on.
func appendUIDSet(stored []*Msg) string {
	if len(stored) == 1 {
		return strconv.FormatUint(uint64(stored[0].UID), 10)
	}
	parts := make([]string, 0, len(stored))
	for i := 0; i < len(stored); {
		j := i
		for j+1 < len(stored) && stored[j+1].UID == stored[j].UID+1 {
			j++
		}
		if j > i {
			parts = append(parts, fmt.Sprintf("%d:%d", stored[i].UID, stored[j].UID))
		} else {
			parts = append(parts, strconv.FormatUint(uint64(stored[i].UID), 10))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// unquote strips one layer of IMAP double-quoting from a mailbox argument.
func unquote(s string) string {
	t := toks(s)
	if len(t) > 0 {
		return t[0]
	}
	return s
}

// flagsOf returns a message's flags as a slice (\Recent is never persisted),
// so a COPY/MOVE preserves the message's own flags on the quarantine copy.
func flagsOf(m *Msg) []string {
	var out []string
	for f := range m.Flags {
		if !strings.EqualFold(f, `\Recent`) {
			out = append(out, f)
		}
	}
	return out
}

func readFull(br *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := br.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
