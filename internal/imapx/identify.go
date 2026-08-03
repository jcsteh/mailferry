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

// Identifying the server MailFerry is actually talking to.
//
// THE RULE THAT SHAPES THIS FILE: identification comes only from what the
// server SAYS ABOUT ITSELF on the established connection. There is no
// hostname table, no bundled provider database, and nothing to maintain as
// providers come and go. Identify() is not even given the hostname, so
// guessing from a domain is structurally impossible rather than merely
// discouraged.
//
// Five evidence sources, all free — every one is already collected during a
// normal connection, so nothing here costs an extra round trip, in
// descending order of confidence:
//
//	OAuth provider       the strongest signal there is. A bearer token minted
//	                     by Microsoft's identity platform and ACCEPTED by
//	                     this server proves whose service it is; no other
//	                     vendor can accept it. This is learned from the
//	                     completed authentication, not from configuration
//	                     alone — an unauthenticated connection offers it.
//	IMAP ID (RFC 2971)   the server's own "name"/"version"/"vendor" fields
//	CAPABILITY           only where a capability is itself an identification
//	                     (a vendor-private extension names its vendor)
//	greeting             the untagged OK banner it opened with
//	TLS certificate      the organisation and common name it presented
//
// A self-report is not a hostname lookup: the server is telling us what it
// is. MailFerry tidies that self-report for display and never invents one.
// When nothing identifies the server, the answer is "Unknown" — which is a
// useful, honest answer, and far better than a confident guess.
package imapx

import (
	"crypto/tls"
	"fmt"
	"sort"
	"strings"
)

// Evidence is one piece of self-reported identification, kept with its
// origin so a disagreement can be explained rather than silently resolved.
type Evidence struct {
	Source string // "IMAP ID", "TLS certificate", "greeting", "CAPABILITY"
	Field  string // the specific field within that source
	Value  string // what the server said
	Name   string // the product name derived from it ("" = no identification)
	Weight int    // confidence; higher wins
}

// ServerIdent is the resolved identification for one connection.
type ServerIdent struct {
	// Name is the display name, or "" when the server could not be
	// positively identified. Callers render "" as "Unknown".
	Name string
	// Version is the self-reported version, when the server offered one.
	Version string
	// All is every piece of evidence gathered, agreeing or not.
	All []Evidence
	// Conflicts are the pieces that named a DIFFERENT product from the one
	// chosen. Logged at debug level; never shown on the Dashboard.
	Conflicts []Evidence
	// TLSVersion is the negotiated transport, e.g. "TLS 1.3", or "" for a
	// plaintext connection.
	TLSVersion string
	// Security is how the transport was established: "TLS 1.3", "STARTTLS →
	// TLS 1.2", or "none".
	Security string
}

// Identified reports whether the server named itself.
func (s ServerIdent) Identified() bool { return s.Name != "" }

// Display renders the product for the UI, or "Unknown".
func (s ServerIdent) Display() string {
	if s.Name == "" {
		return "Unknown"
	}
	return s.Name
}

// evidence weights. IMAP ID is the server answering the actual question, so
// it outranks everything; a certificate is issued to an operator rather than
// a product, so it ranks below a banner the software itself printed.
const (
	weightOAuth      = 50
	weightIMAPID     = 40
	weightCapability = 30
	weightGreeting   = 20
	weightCert       = 10
)

// oauthProviderNames maps a provider MailFerry successfully authenticated
// against to the service that accepted the token. This is not a lookup of
// the server — it is the meaning of a completed authentication: a Microsoft
// identity-platform token for the Outlook IMAP resource is only ever
// accepted by Exchange Online.
var oauthProviderNames = map[string]string{
	"microsoft": "Exchange Online",
	"google":    "Gmail",
}

// selfReports maps a token a server uses FOR ITSELF onto the product name to
// display. This is not a provider database and is not keyed on hostnames: it
// only tidies strings the server volunteered. An unrecognised self-report is
// still used verbatim (see cleanProduct), so a server MailFerry has never
// heard of still identifies itself correctly — the table only fixes casing
// and expands names that are unhelpful as printed.
//
// Deliberately ABSENT: distributions and suites that do not identify
// themselves on the wire. mailcow, Mailu, iRedMail and docker-mailserver
// all speak through the Postfix and Dovecot they bundle, so those are what
// the evidence names — identifying the suite would require exactly the kind
// of fingerprinting this design refuses to do.
var selfReports = []struct {
	token string // matched case-insensitively against the self-report
	name  string
}{
	{"microsoft.exchange", "Exchange Online"},
	{"exchange", "Exchange Online"},
	{"dovecot", "Dovecot"},
	{"cyrus", "Cyrus IMAP"},
	{"axigen", "Axigen"},
	{"gimap", "Gmail"},
	{"zimbra", "Zimbra"},
	{"icewarp", "IceWarp"},
	{"mdaemon", "MDaemon"},
	{"kerio", "Kerio Connect"},
	{"courier", "Courier IMAP"},
	{"smartermail", "SmarterMail"},
	{"mailenable", "MailEnable"},
	{"wildduck", "WildDuck"},
	{"openwave", "Openwave"},
	{"sun java system", "Oracle Communications"},
	{"proton", "Proton Mail Bridge"},
	{"fastmail", "Fastmail"},
	{"yahoo", "Yahoo Mail"},
	{"zoho", "Zoho Mail"},
	{"hmailserver", "hMailServer"},
	{"greenmail", "GreenMail"},
	{"stalwart", "Stalwart"},
}

// smtpReports maps a token an SMTP server uses for itself in its 220
// greeting banner onto a display name. A SEPARATE table from selfReports
// because the two protocols name different software: the same machine may
// greet as "Dovecot" on 993 and "Postfix" on 25, and both answers are
// correct — they are different programs. Exchange's SMTP service also
// styles itself differently ("Microsoft ESMTP MAIL Service") from its IMAP
// service, and the on-premises SMTP product is displayed as "Microsoft
// Exchange" rather than "Exchange Online" because an SMTP banner carries no
// evidence of the hosted service.
//
// ORDER MATTERS: first match wins, and the bundled-component rule depends on
// it. Zimbra's MTA is Postfix; a banner reading "ESMTP Postfix (Zimbra)"
// names both, and the software that printed it is Postfix — so Postfix is
// listed first and wins. "Zimbra" alone still identifies Zimbra.
//
// MailFerry does not speak SMTP yet. This table and IdentifySMTPGreeting
// exist so the identification engine is ready when it does; nothing calls
// them from a live connection today.
var smtpReports = []struct {
	token string
	name  string
}{
	{"postfix", "Postfix"},
	{"exim", "Exim"},
	{"sendmail", "Sendmail"},
	{"microsoft esmtp mail service", "Microsoft Exchange"},
	{"microsoft smtp", "Microsoft Exchange"},
	{"exchange", "Microsoft Exchange"},
	{"zimbra", "Zimbra"},
	{"mdaemon", "MDaemon"},
	{"kerio", "Kerio Connect"},
	{"mailenable", "MailEnable"},
	{"smartermail", "SmarterMail"},
	{"stalwart", "Stalwart"},
	{"gromox", "gromox (grommunio)"},
}

// IdentifySMTPGreeting resolves a server identity from an SMTP 220 greeting
// banner. Pure function — it exists ahead of SMTP support so the engine can
// already answer the question when a greeting is put in front of it. The
// same rule as everywhere else applies: no positive evidence, no name.
func IdentifySMTPGreeting(greeting string) ServerIdent {
	out := ServerIdent{}
	g := strings.TrimSpace(greeting)
	if g == "" {
		return out
	}
	low := strings.ToLower(g)
	name := ""
	for _, r := range smtpReports {
		if strings.Contains(low, r.token) {
			name = r.name
			break
		}
	}
	out.All = append(out.All, Evidence{Source: "SMTP greeting", Field: "banner",
		Value: trimEvidence(g), Name: name, Weight: weightGreeting})
	out.Name = name
	return out
}

// capabilitySignatures are capabilities that IDENTIFY their vendor because
// they are vendor-private extensions — a server advertising X-GM-EXT-1 is
// telling you it is Gmail. Generic capabilities (IDLE, UIDPLUS, …) identify
// nothing and are deliberately absent.
var capabilitySignatures = []struct {
	capability string
	name       string
}{
	{"X-GM-EXT-1", "Gmail"},
	{"XAPPLEPUSHSERVICE", "Apple Mail Server"},
	{"X-DOVECOT", "Dovecot"},
	{"XMSEXCHANGE", "Microsoft Exchange"},
}

// productFrom derives a display name from a string the server said about
// itself. Returns "" when nothing recognisable is present.
func productFrom(self string) string {
	low := strings.ToLower(self)
	for _, r := range selfReports {
		if strings.Contains(low, r.token) {
			return r.name
		}
	}
	return ""
}

// Identify resolves the server's identity from the evidence collected during
// this connection. It deliberately takes NO hostname argument, so guessing
// from a domain is structurally impossible rather than merely discouraged.
//
// oauthProvider is the provider whose bearer token this connection
// successfully authenticated with ("microsoft", "google", …), or "" for a
// password login or a failed/absent authentication.
func (c *Client) Identify(oauthProvider string) ServerIdent {
	out := ServerIdent{TLSVersion: c.tlsVersion, Security: c.securityLabel()}

	add := func(source, field, value string, weight int, name string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		out.All = append(out.All, Evidence{Source: source, Field: field,
			Value: trimEvidence(value), Name: name, Weight: weight})
	}

	// 0. A completed OAuth authentication — the strongest signal available.
	//    Only the provider's own service can accept its token.
	if p := strings.ToLower(strings.TrimSpace(oauthProvider)); p != "" {
		add("OAuth", "provider", p, weightOAuth, oauthProviderNames[p])
	}

	// 1. IMAP ID (RFC 2971) — the server answering the actual question.
	if id := c.IDFields(); len(id) > 0 {
		name := id["name"]
		vendor := id["vendor"]
		out.Version = id["version"]
		add("IMAP ID", "name", name, weightIMAPID, cleanProduct(name))
		add("IMAP ID", "vendor", vendor, weightIMAPID-5, cleanProduct(vendor))
		add("IMAP ID", "version", out.Version, 0, "")
	}

	// 2. Vendor-private capabilities.
	for _, sig := range capabilitySignatures {
		if c.Has(sig.capability) {
			add("CAPABILITY", sig.capability, sig.capability, weightCapability, sig.name)
		}
	}

	// 3. The greeting banner the software printed for itself.
	add("greeting", "banner", c.Greeting, weightGreeting, productFrom(c.Greeting))

	// 4. TLS certificate identity — an operator's name more often than a
	//    product's, so it is the weakest signal, but it is real evidence.
	if c.certOrg != "" {
		add("TLS certificate", "organisation", c.certOrg, weightCert, productFrom(c.certOrg))
	}
	if c.certCN != "" {
		add("TLS certificate", "subject", c.certCN, 0, "")
	}

	// Resolve: the highest-weighted piece of evidence that actually named a
	// product wins; anything naming a DIFFERENT product is recorded as a
	// conflict for the debug log rather than being averaged away.
	best := -1
	for i, e := range out.All {
		if e.Name == "" {
			continue
		}
		if best < 0 || e.Weight > out.All[best].Weight {
			best = i
		}
	}
	if best < 0 {
		return out
	}
	out.Name = out.All[best].Name
	for _, e := range out.All {
		if e.Name != "" && e.Name != out.Name {
			out.Conflicts = append(out.Conflicts, e)
		}
	}
	return out
}

// cleanProduct normalises a self-reported product name for display, keeping
// an unrecognised one verbatim (minus obvious noise) so a server MailFerry
// has never seen still identifies itself.
func cleanProduct(self string) string {
	self = strings.TrimSpace(self)
	if self == "" {
		return ""
	}
	if known := productFrom(self); known != "" {
		return known
	}
	// "NIL" is the RFC 2971 way of declining to answer.
	if strings.EqualFold(self, "NIL") {
		return ""
	}
	if len(self) > 40 {
		self = self[:40]
	}
	return self
}

// trimEvidence keeps a recorded value to a sane length for logs.
func trimEvidence(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 120 {
		return s[:120]
	}
	return s
}

// ConflictLog renders the disagreements for a debug-level log line, or ""
// when every source agreed (or only one spoke).
func (s ServerIdent) ConflictLog() string {
	if len(s.Conflicts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.Conflicts))
	for _, e := range s.Conflicts {
		parts = append(parts, fmt.Sprintf("%s/%s=%q suggests %q",
			e.Source, e.Field, e.Value, e.Name))
	}
	sort.Strings(parts)
	return fmt.Sprintf("server identification chose %q; conflicting evidence: %s",
		s.Name, strings.Join(parts, "; "))
}

// EvidenceLog renders every piece of evidence for a debug-level log line.
func (s ServerIdent) EvidenceLog() string {
	if len(s.All) == 0 {
		return "server identification: no evidence offered by the server"
	}
	parts := make([]string, 0, len(s.All))
	for _, e := range s.All {
		parts = append(parts, fmt.Sprintf("%s/%s=%q", e.Source, e.Field, e.Value))
	}
	return "server identification evidence: " + strings.Join(parts, "; ")
}

// --------------------------------------------------------- capture points --

// IDFields returns the parsed RFC 2971 ID response, lower-cased keys.
func (c *Client) IDFields() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idFields == nil {
		return nil
	}
	out := make(map[string]string, len(c.idFields))
	for k, v := range c.idFields {
		out[k] = v
	}
	return out
}

// captureID parses the untagged ID response into idFields.
func (c *Client) captureID(res Result) {
	fields := map[string]string{}
	for _, d := range res.Data {
		for _, t := range d.Tokens {
			list, ok := t.([]any)
			if !ok {
				continue
			}
			for i := 0; i+1 < len(list); i += 2 {
				k, ok1 := TokStr(list[i])
				v, ok2 := TokStr(list[i+1])
				if ok1 && ok2 && !strings.EqualFold(v, "NIL") {
					fields[strings.ToLower(k)] = v
				}
			}
		}
	}
	if len(fields) == 0 {
		return
	}
	c.mu.Lock()
	c.idFields = fields
	c.mu.Unlock()
}

// captureTLS records the negotiated version and the certificate identity.
// It reads state that already exists on the connection — no extra traffic.
func (c *Client) captureTLS(starttls bool) {
	tc, ok := c.conn.(*tls.Conn)
	if !ok {
		return
	}
	st := tc.ConnectionState()
	c.tlsVersion = tlsVersionName(st.Version)
	c.viaSTARTTLS = starttls
	if len(st.PeerCertificates) > 0 {
		leaf := st.PeerCertificates[0]
		c.certCN = leaf.Subject.CommonName
		if len(leaf.Subject.Organization) > 0 {
			c.certOrg = leaf.Subject.Organization[0]
		}
	}
}

// securityLabel describes how the transport was established.
func (c *Client) securityLabel() string {
	switch {
	case c.tlsVersion == "":
		return "none"
	case c.viaSTARTTLS:
		return "STARTTLS → " + c.tlsVersion
	default:
		return c.tlsVersion
	}
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "TLS 1.3"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS10:
		return "TLS 1.0"
	default:
		return fmt.Sprintf("TLS 0x%04x", v)
	}
}
