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

// SASL bearer-token authentication for the native IMAP client.
//
// This layer knows ONLY the two IMAP SASL mechanisms — XOAUTH2 (the form
// Microsoft 365 and Gmail document and require) and OAUTHBEARER (RFC 7628,
// for standards-compliant servers). It does NOT know about Microsoft,
// Google or any provider: it is handed a fresh bearer token by a caller-
// supplied TokenFn and encodes it per the mechanism. Provider knowledge
// (endpoints, scopes, refresh) lives entirely in internal/oauth, so the
// migration engine consumes an authenticated connection without caring how
// the token was obtained.
//
// SECURITY: the encoded SASL response embeds the bearer token. It is sent
// only as the AUTHENTICATE command's initial response, where the existing
// trace redaction (redactLogin) rewrites it to "AUTHENTICATE <mech> ****".
// The token is never passed to Log, never sent as a bare continuation
// line, and never included in an error message.
package imapx

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ajsap/mailferry/v2/internal/identity"
)

// SASLAuth carries everything the IMAP client needs to authenticate with a
// bearer token, and nothing about the provider that issued it. TokenFn is
// called each time a connection authenticates (including reconnects), so a
// long migration always presents a freshly-refreshed access token.
type SASLAuth struct {
	Mechanism string // "XOAUTH2" or "OAUTHBEARER"
	User      string // the mailbox login (may differ from the token subject)
	// TokenFn returns a currently-valid access token. It must never return
	// the token in an error; the error is surfaced to the operator.
	TokenFn func(context.Context) (string, error)
}

// MechXOAUTH2 / MechOAUTHBEARER are the canonical mechanism names.
const (
	MechXOAUTH2     = "XOAUTH2"
	MechOAUTHBEARER = "OAUTHBEARER"
)

// XOAUTH2Response builds the SASL XOAUTH2 initial client response
// (base64), exactly per the Microsoft and Google specifications:
//
//	base64("user=" user "\x01auth=Bearer " token "\x01\x01")
//
// Exported so a golden test can pin it against the official example
// vectors without a live token.
func XOAUTH2Response(user, token string) string {
	raw := "user=" + user + "\x01auth=Bearer " + token + "\x01\x01"
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// OAUTHBEARERResponse builds the SASL OAUTHBEARER initial client response
// (base64) per RFC 7628 §3.1:
//
//	base64("n,a=" user ",\x01host=" host "\x01port=" port "\x01auth=Bearer " token "\x01\x01")
//
// The gs2-header is "n," (no channel binding, no authzid beyond a=).
func OAUTHBEARERResponse(user, host string, port int, token string) string {
	var b strings.Builder
	b.WriteString("n,a=")
	b.WriteString(user)
	b.WriteString(",\x01")
	if host != "" {
		b.WriteString("host=" + host + "\x01")
	}
	if port > 0 {
		b.WriteString("port=" + strconv.Itoa(port) + "\x01")
	}
	b.WriteString("auth=Bearer ")
	b.WriteString(token)
	b.WriteString("\x01\x01")
	return base64.StdEncoding.EncodeToString([]byte(b.String()))
}

// saslResponse dispatches on the mechanism.
func saslResponse(mech, user, host string, port int, token string) (string, error) {
	switch strings.ToUpper(mech) {
	case MechXOAUTH2:
		return XOAUTH2Response(user, token), nil
	case MechOAUTHBEARER:
		return OAUTHBEARERResponse(user, host, port, token), nil
	default:
		return "", fmt.Errorf("unsupported SASL mechanism %q", mech)
	}
}

// authenticateSASL performs an AUTHENTICATE exchange using the configured
// bearer mechanism. It gates on the server actually advertising the
// mechanism, uses the initial-response form (RFC 4959 SASL-IR, which both
// first-class providers support), and handles the error-continuation the
// providers send on failure by replying with an empty line so the tagged
// NO can be read. Errors are actionable and never contain the token.
func (c *Client) authenticateSASL() error {
	c.Side.State("auth")
	mech := strings.ToUpper(c.Auth.Mechanism)

	if !c.Has("AUTH=" + mech) {
		offered := c.saslMechsOffered()
		hint := ""
		if offered != "" {
			hint = " (server offers: " + offered + ")"
		}
		return &AuthErr{fmt.Sprintf(
			"server %s does not advertise AUTH=%s%s — the mailbox may not have "+
				"OAuth/modern-auth enabled for IMAP, or the wrong endpoint is configured",
			c.EP.Host, mech, hint)}
	}

	ctx, cancel := context.WithTimeout(context.Background(), maxDur(30*time.Second, c.Timeout))
	defer cancel()
	token, err := c.Auth.TokenFn(ctx)
	if err != nil {
		// err comes from internal/oauth and is already redacted of secrets.
		return &AuthErr{"OAuth token could not be obtained: " + err.Error()}
	}
	ir, err := saslResponse(mech, c.Auth.User, c.EP.Host, c.EP.Port, token)
	if err != nil {
		return &AuthErr{err.Error()}
	}

	// Initial-response form: the token rides on the AUTHENTICATE line, where
	// redactLogin rewrites it to "AUTHENTICATE <mech> ****" in any trace.
	p, err := c.CmdNowait("AUTHENTICATE", mech+" "+ir)
	if err != nil {
		return &AuthErr{"AUTHENTICATE could not be sent: " + err.Error()}
	}
	done := make(chan error, 1)
	go func() { _, e := p.Wait(); done <- e }()

	// Providers reply to a bad token with a "+ <base64 json error>"
	// continuation and then wait for a line before sending the tagged NO.
	select {
	case r := <-c.contCh:
		_ = r               // the payload is an error blob; we don't echo it
		_ = c.writeLine("") // empty line (no secret) → server sends the tagged NO
		err = <-done
	case err = <-done:
	}
	if err != nil {
		return err // already an *AuthErr from route()
	}

	if len(c.Caps) == 0 {
		c.Cmd("CAPABILITY", "")
	}
	// RFC 2971 ID — same on both authentication paths, so a server is
	// identified identically whether the operator used OAuth or a password.
	if res, idErr := c.Cmd("ID", fmt.Sprintf(`("name" %s "version" %s)`,
		quoteIMAP(identity.Product), quoteIMAP(identity.Version)), "ID"); idErr == nil {
		c.captureID(res)
	}
	c.Side.State("ready")
	return nil
}

// saslMechsOffered lists the AUTH= mechanisms the server advertised, for a
// more helpful "not advertised" error.
func (c *Client) saslMechsOffered() string {
	var mechs []string
	for cap := range c.Caps {
		if strings.HasPrefix(cap, "AUTH=") {
			mechs = append(mechs, strings.TrimPrefix(cap, "AUTH="))
		}
	}
	return strings.Join(mechs, ", ")
}

func maxDur(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
