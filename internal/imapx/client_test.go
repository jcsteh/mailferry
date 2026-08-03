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

package imapx

import (
	"io"
	"net"
	"testing"
	"time"
)

// TestConnectIPv6Literal proves an IPv6 literal host dials correctly: the
// address must be bracketed ("[::1]:port"), which "host:port" string
// formatting gets wrong. Guards the net.JoinHostPort form in Connect.
func TestConnectIPv6Literal(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		conn.Write([]byte("* OK [CAPABILITY IMAP4rev1] fake ready\r\n"))
		io.Copy(io.Discard, conn) // hold open until the client closes
		conn.Close()
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	c := NewClient(Endpoint{Host: "::1", Port: port, Security: "none"},
		5*time.Second, true, nil, "test", nil)
	if err := c.Connect(); err != nil {
		t.Fatalf("IPv6 literal host must dial as [::1]:port, got: %v", err)
	}
	c.Abort(nil)
}
