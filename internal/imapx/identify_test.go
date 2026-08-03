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

// Server identification must come from the server, never from a hostname.
// The tests below encode that as a rule rather than a hope.
package imapx

import (
	"strings"
	"testing"
)

// TestOAuthProviderIsStrongestEvidence: a token only Microsoft could mint,
// accepted by this server, outranks anything the server says about itself.
// Nothing else can accept that token.
func TestOAuthProviderIsStrongestEvidence(t *testing.T) {
	c := &Client{Caps: map[string]bool{}, Greeting: "Dovecot ready."}
	c.idFields = map[string]string{"name": "Something.Else"}

	id := c.Identify("microsoft")
	if id.Name != "Exchange Online" {
		t.Fatalf("a completed Microsoft OAuth must identify the service, got %q", id.Name)
	}
	if len(id.Conflicts) == 0 {
		t.Fatal("the disagreeing self-reports must be recorded as conflicts")
	}

	if got := (&Client{Caps: map[string]bool{}}).Identify("google").Name; got != "Gmail" {
		t.Fatalf("google OAuth identification: %q", got)
	}
	// A provider MailFerry has no service name for must not invent one; the
	// weaker evidence then decides.
	c2 := &Client{Caps: map[string]bool{}, Greeting: "Axigen IMAP server ready"}
	if got := c2.Identify("generic").Name; got != "Axigen" {
		t.Fatalf("an unnamed provider should fall through to the greeting, got %q", got)
	}
	// Password authentication contributes nothing.
	c3 := &Client{Caps: map[string]bool{}, Greeting: "ready"}
	if id := c3.Identify(""); id.Identified() {
		t.Fatalf("password auth must not identify anything: %q", id.Name)
	}
}

// TestIdentifyUsesIMAPIDFirst: absent OAuth, the server answering RFC 2971
// outranks every weaker signal.
func TestIdentifyUsesIMAPIDFirst(t *testing.T) {
	c := &Client{Caps: map[string]bool{}, Greeting: "Dovecot ready."}
	c.idFields = map[string]string{"name": "Microsoft.Exchange", "version": "15.20"}

	id := c.Identify("")
	// "Microsoft.Exchange" is the display-name mapping's canonical example:
	// the IMAP service's self-report renders as Exchange Online.
	if id.Name != "Exchange Online" {
		t.Fatalf("IMAP ID must win: got %q", id.Name)
	}
	if id.Version != "15.20" {
		t.Fatalf("self-reported version lost: %q", id.Version)
	}
	// The greeting disagreed, so it must be recorded as a conflict rather
	// than silently discarded.
	if len(id.Conflicts) == 0 {
		t.Fatal("a disagreeing greeting must be recorded as a conflict")
	}
	if !strings.Contains(id.ConflictLog(), "Dovecot") {
		t.Fatalf("the conflict log should name the losing evidence: %q", id.ConflictLog())
	}
}

// TestIdentifyFallsBackThroughEvidence walks down the confidence ladder.
func TestIdentifyFallsBackThroughEvidence(t *testing.T) {
	// Greeting only.
	c := &Client{Caps: map[string]bool{}, Greeting: "Axigen IMAP server ready"}
	if got := c.Identify("").Name; got != "Axigen" {
		t.Fatalf("greeting identification: got %q", got)
	}
	// A vendor-private capability identifies its vendor.
	c = &Client{Caps: map[string]bool{"X-GM-EXT-1": true}, Greeting: "ready"}
	if got := c.Identify("").Name; got != "Gmail" {
		t.Fatalf("capability identification: got %q", got)
	}
	// Certificate organisation, weakest but real.
	c = &Client{Caps: map[string]bool{}, Greeting: "ready", certOrg: "Zimbra Inc"}
	if got := c.Identify("").Name; got != "Zimbra" {
		t.Fatalf("certificate identification: got %q", got)
	}
}

// TestIdentifyUnknownRatherThanGuess is the whole point: no evidence means
// "Unknown", never an inference.
func TestIdentifyUnknownRatherThanGuess(t *testing.T) {
	c := &Client{
		Caps:     map[string]bool{"IMAP4REV1": true, "IDLE": true, "UIDPLUS": true},
		Greeting: "IMAP4rev1 server ready",
		EP:       Endpoint{Host: "outlook.office365.com", Port: 993},
	}
	id := c.Identify("")
	if id.Identified() {
		t.Fatalf("a generic server must not be identified: got %q", id.Name)
	}
	if id.Display() != "Unknown" {
		t.Fatalf("Display() = %q, want Unknown", id.Display())
	}
	// Generic capabilities identify nothing.
	for _, e := range id.All {
		if e.Source == "CAPABILITY" && e.Name != "" {
			t.Fatalf("generic capability %q must not identify a vendor", e.Field)
		}
	}
}

// TestIdentifyIgnoresHostnameEntirely is the structural guarantee. The
// hostname is the single most tempting — and most wrong — signal, so the
// same server evidence must produce the same answer whatever it is called.
func TestIdentifyIgnoresHostnameEntirely(t *testing.T) {
	mk := func(host string) ServerIdent {
		c := &Client{Caps: map[string]bool{}, Greeting: "ready",
			EP: Endpoint{Host: host, Port: 993}}
		return c.Identify("")
	}
	for _, host := range []string{
		"outlook.office365.com", "imap.gmail.com", "mail.example.com", "",
	} {
		if id := mk(host); id.Identified() {
			t.Fatalf("host %q produced an identification (%q) from no evidence",
				host, id.Name)
		}
	}
}

// TestUnrecognisedSelfReportIsKept: a server MailFerry has never heard of
// still identifies itself — the table only tidies known names.
func TestUnrecognisedSelfReportIsKept(t *testing.T) {
	c := &Client{Caps: map[string]bool{}}
	c.idFields = map[string]string{"name": "AcmeMail"}
	if got := c.Identify("").Name; got != "AcmeMail" {
		t.Fatalf("an unknown self-report must be used verbatim, got %q", got)
	}
	// RFC 2971 NIL means "declining to answer" and must not become a name.
	c.idFields = map[string]string{"name": "NIL"}
	if id := c.Identify(""); id.Identified() {
		t.Fatalf("NIL must not be treated as a product name: %q", id.Name)
	}
}

// TestCaptureIDParsesResponse checks the RFC 2971 wire form.
func TestCaptureIDParsesResponse(t *testing.T) {
	c := &Client{Caps: map[string]bool{}}
	res := Result{Data: []*Response{{Type: "ID", Tokens: []any{
		[]any{"name", "Dovecot", "version", "2.3.21", "vendor", "NIL"},
	}}}}
	c.captureID(res)
	got := c.IDFields()
	if got["name"] != "Dovecot" || got["version"] != "2.3.21" {
		t.Fatalf("ID parse: %v", got)
	}
	if _, ok := got["vendor"]; ok {
		t.Fatal("NIL fields must be dropped, not stored as the string NIL")
	}
}

// TestIMAPProductRecognition covers every product the engine recognises from
// IMAP evidence, each via a realistic form of the evidence that product
// actually emits. A product that stops being recognised fails here by name.
func TestIMAPProductRecognition(t *testing.T) {
	cases := []struct {
		product  string
		greeting string            // "" = no greeting evidence
		id       map[string]string // RFC 2971 ID fields, nil = none
		want     string
	}{
		{"Dovecot", "Dovecot ready.",
			map[string]string{"name": "Dovecot", "version": "2.3.21"}, "Dovecot"},
		{"Courier IMAP", "Courier-IMAP ready. Copyright 1998-2022 Double Precision, Inc.",
			nil, "Courier IMAP"},
		{"Cyrus IMAP", "mail.example.com Cyrus IMAP 3.8.1 server ready",
			map[string]string{"name": "Cyrus IMAPD", "version": "3.8.1"}, "Cyrus IMAP"},
		{"Exchange", "The Microsoft Exchange IMAP4 service is ready.",
			map[string]string{"name": "Microsoft.Exchange"}, "Exchange Online"},
		{"Zimbra", "mail.example.com Zimbra IMAP4rev1 server ready",
			map[string]string{"name": "Zimbra", "version": "10.0.6"}, "Zimbra"},
		{"Axigen", "Axigen IMAP server ready",
			map[string]string{"name": "Axigen Mail Server"}, "Axigen"},
		{"SmarterMail", "IMAP4rev1 SmarterMail server ready", nil, "SmarterMail"},
		{"MDaemon", "mail.example.com IMAP4rev1 MDaemon 23.5.0 ready", nil, "MDaemon"},
		{"Kerio Connect", "Kerio Connect 10.0.5 IMAP4rev1 server ready", nil, "Kerio Connect"},
		{"MailEnable", "IMAP4rev1 server ready at mail.example.com (MailEnable)",
			nil, "MailEnable"},
		{"WildDuck", "mail.example.com WildDuck IMAP ready",
			map[string]string{"name": "WildDuck", "version": "1.40.10"}, "WildDuck"},
		{"Stalwart", "Stalwart IMAP4rev2 at your service", nil, "Stalwart"},
	}
	for _, c := range cases {
		// Via the greeting alone.
		if c.greeting != "" {
			cl := &Client{Caps: map[string]bool{}, Greeting: c.greeting}
			if got := cl.Identify("").Name; got != c.want {
				t.Errorf("%s greeting %q -> %q, want %q", c.product, c.greeting, got, c.want)
			}
		}
		// Via RFC 2971 ID alone (higher confidence, must agree).
		if c.id != nil {
			cl := &Client{Caps: map[string]bool{}}
			cl.idFields = c.id
			if got := cl.Identify("").Name; got != c.want {
				t.Errorf("%s IMAP ID %v -> %q, want %q", c.product, c.id, got, c.want)
			}
		}
	}
}

// TestSMTPGreetingRecognition covers every product the engine recognises
// from an SMTP 220 banner. MailFerry does not speak SMTP yet — this proves
// the engine is ready for the greeting when it does.
func TestSMTPGreetingRecognition(t *testing.T) {
	cases := []struct {
		banner string
		want   string
	}{
		{"220 mail.example.com ESMTP Postfix", "Postfix"},
		{"220 mail.example.com ESMTP Exim 4.97 Mon, 03 Aug 2026 21:14:37 +1200", "Exim"},
		{"220 mail.example.com ESMTP Sendmail 8.18.1/8.18.1; Mon, 3 Aug 2026", "Sendmail"},
		{"220 mail.example.com Microsoft ESMTP MAIL Service ready at Mon, 3 Aug 2026", "Microsoft Exchange"},
		{"220 mail.example.com ESMTP Postfix (Zimbra)", "Postfix"}, // the software that spoke
		{"220 mail.example.com Zimbra ESMTP ready", "Zimbra"},
		{"220 mail.example.com ESMTP MDaemon 23.5.0", "MDaemon"},
		{"220 mail.example.com Kerio Connect 10.0.5 ESMTP ready", "Kerio Connect"},
		{"220 mail.example.com ESMTP MailEnable Service, Version: 10.45", "MailEnable"},
		{"220 mail.example.com SmarterMail", "SmarterMail"},
		{"220 mail.example.com Stalwart ESMTP at your service", "Stalwart"},
		{"220 mail.example.com gromox esmtp", "gromox (grommunio)"},
		// No positive evidence: no name, never a guess.
		{"220 mail.example.com ESMTP", ""},
		{"220 mail.example.com ESMTP service ready", ""},
		{"", ""},
	}
	for _, c := range cases {
		id := IdentifySMTPGreeting(c.banner)
		if id.Name != c.want {
			t.Errorf("SMTP banner %q -> %q, want %q", c.banner, id.Name, c.want)
		}
		if c.want == "" && c.banner != "" {
			if id.Display() != "Unknown" {
				t.Errorf("unidentified banner %q must display Unknown, got %q",
					c.banner, id.Display())
			}
			// The evidence is still recorded, so debugging can see what was said.
			if len(id.All) == 0 {
				t.Errorf("banner %q left no evidence trail", c.banner)
			}
		}
	}
}

// TestSuitesAreNotIdentified: mail suites and distributions speak through
// the Postfix and Dovecot they bundle. The evidence names the software that
// actually spoke; identifying the suite would be fingerprinting.
func TestSuitesAreNotIdentified(t *testing.T) {
	// A mailcow/Mailu/iRedMail box greets as Dovecot on IMAP…
	cl := &Client{Caps: map[string]bool{}, Greeting: "Dovecot ready."}
	if got := cl.Identify("").Name; got != "Dovecot" {
		t.Fatalf("bundled Dovecot must be identified as Dovecot, got %q", got)
	}
	// …and as Postfix on SMTP.
	if got := IdentifySMTPGreeting("220 mail.example.com ESMTP Postfix").Name; got != "Postfix" {
		t.Fatalf("bundled Postfix must be identified as Postfix, got %q", got)
	}
	// The suite names themselves must not be produced by any evidence path.
	for _, suite := range []string{"mailcow", "Mailu", "iRedMail", "docker-mailserver"} {
		if got := productFrom(suite); got != "" && got != suite {
			t.Errorf("suite %q must not map to a product, got %q", suite, got)
		}
	}
}

// TestSecurityLabel describes the transport honestly.
func TestSecurityLabel(t *testing.T) {
	c := &Client{}
	if got := c.securityLabel(); got != "none" {
		t.Fatalf("plaintext should report none, got %q", got)
	}
	c.tlsVersion = "TLS 1.3"
	if got := c.securityLabel(); got != "TLS 1.3" {
		t.Fatalf("implicit TLS: %q", got)
	}
	c.viaSTARTTLS = true
	if got := c.securityLabel(); got != "STARTTLS → TLS 1.3" {
		t.Fatalf("STARTTLS upgrade should be distinguishable: %q", got)
	}
}
