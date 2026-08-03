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

//go:build !windows

// DPAPI is a Windows facility. Elsewhere the scheme is recognised (so a
// configuration file copied between platforms produces an ACCURATE error
// rather than "unrecognised reference") but never usable.
package oauth

import (
	"errors"
	"runtime"
)

func dpapiAvailable() bool { return false }

func dpapiProtect([]byte) ([]byte, error) {
	return nil, errors.New("dpapi: credentials are a Windows facility")
}

func dpapiUnprotect([]byte) ([]byte, error) {
	return nil, errors.New("dpapi: credentials can only be read on Windows, " +
		"by the Windows user account that stored them — this host is " + runtime.GOOS)
}
