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

package oauth

import (
	"fmt"
	"os/exec"
	"runtime"
)

// OpenBrowser opens url in the operator's default browser using the OS
// launcher. A non-nil error means the caller should degrade to printing the
// URL for manual opening — it must never be treated as an authorisation
// failure.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default: // linux and other unixes
		if _, err := exec.LookPath("xdg-open"); err != nil {
			return fmt.Errorf("no browser launcher (xdg-open) available")
		}
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
