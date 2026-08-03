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

// tuishot: development tool — renders populated TUI frames for the
// documentation screenshots (the Go equivalent of tools/capture_views.py).
// Fixture data uses RFC-2606 example domains only.
package main

// Usage: tuishot [view] [cols] [rows]
//
// The size arguments exist so a layout can be eyeballed at the widths
// operators actually use — a cramped SSH window as well as a wide desktop —
// without rebuilding anything.
import (
	"fmt"
	"os"
	"strconv"

	"github.com/ajsap/mailferry/v2/internal/tui"
)

func main() {
	view := "dashboard"
	if len(os.Args) > 1 {
		view = os.Args[1]
	}
	cols, rows := tui.ShotCols, tui.ShotRows
	if len(os.Args) > 2 {
		if n, err := strconv.Atoi(os.Args[2]); err == nil && n > 0 {
			cols = n
		}
	}
	if len(os.Args) > 3 {
		if n, err := strconv.Atoi(os.Args[3]); err == nil && n > 0 {
			rows = n
		}
	}
	fmt.Print(tui.RenderShotSize(view, cols, rows))
}
