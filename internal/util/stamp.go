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

package util

import (
	"os"
	"strings"
	"time"
)

// FmtStamp renders an instant for operator-facing display (the Dashboard's
// and Results screen's completion time, the headless summary). It prefers
// the user's regional convention when the environment declares one MailFerry
// can map reliably (POSIX LC_ALL / LC_TIME / LANG territory); any territory
// not in the conservative table below falls back to unambiguous ISO 8601
// with the local UTC offset. The rendered value is presentation only —
// storage and logs always use ISO 8601 / RFC 3339.
//
// SECONDS AND ZONE ARE ALWAYS INCLUDED. A completion time exists to be
// correlated with server logs, ticket timestamps and other tooling, and
// "9:14 pm" is not enough to do that with — the operator needs 21:14:37 and
// to know which zone it is in.
func FmtStamp(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	if layout, ok := stampLayout(localeTerritory(os.Getenv)); ok {
		return t.Format(layout)
	}
	return t.Format("2006-01-02T15:04:05-07:00")
}

// localeTerritory extracts the ISO 3166 territory from the first POSIX
// locale variable that carries one ("en_NZ.UTF-8" → "NZ"). LC_ALL wins,
// then LC_TIME (the category that governs dates), then LANG.
func localeTerritory(getenv func(string) string) string {
	for _, name := range []string{"LC_ALL", "LC_TIME", "LANG"} {
		v := getenv(name)
		if v == "" || v == "C" || strings.HasPrefix(v, "C.") || strings.HasPrefix(v, "POSIX") {
			continue
		}
		if i := strings.IndexAny(v, ".@"); i >= 0 {
			v = v[:i]
		}
		if i := strings.Index(v, "_"); i >= 0 && len(v) > i+1 {
			return strings.ToUpper(v[i+1:])
		}
	}
	return ""
}

// stampLayout maps a territory to its everyday date/time convention.
// Deliberately conservative: only territories whose convention is
// well-established are listed — everything else renders ISO 8601, which is
// always correct. Ambiguous or mixed-convention territories (e.g. Canada)
// are intentionally absent.
func stampLayout(territory string) (string, bool) {
	switch territory {
	case "NZ", "AU", "GB", "IE", "IN", "ZA", "SG", "HK", "MY":
		return "02/01/2006 15:04:05 MST", true // day-first
	case "US", "PH":
		return "01/02/2006 15:04:05 MST", true // month-first
	case "DE", "AT", "CH", "FR", "ES", "IT", "NL", "BE", "PT", "PL", "CZ",
		"DK", "NO", "SE", "FI", "BR", "AR", "MX", "CL", "CO", "RU", "TR", "ID", "VN", "TH":
		return "02/01/2006 15:04:05 MST", true // day-first
	case "JP", "CN", "TW", "KR", "HU", "LT":
		return "2006/01/02 15:04:05 MST", true // year-first
	}
	return "", false
}
