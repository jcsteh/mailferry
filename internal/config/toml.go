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

package config

// mailferry.toml — optional, auto-generated, never fatal. Same keys and
// philosophy as the Python engine: sensible defaults always work; the file
// exists only so advanced users can tune behaviour. CLI flags override it.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ajsap/mailferry/v2/internal/identity"
	"github.com/ajsap/mailferry/v2/internal/paths"
)

const TOMLTemplate = `# MailFerry Configuration
# High-Performance Native IMAP Migration Engine
# Author: Andy Saputra <andy@saputra.org>
# https://github.com/ajsap/mailferry
#
# Generated automatically on first launch. Every value below is the
# built-in default — MailFerry behaves identically whether or not this
# file exists. Edit what you need; delete anything you don't. Command-line
# flags always override this file. Invalid values fall back to the
# default with a warning; unknown keys are ignored with a warning.

[migration]
# Mailboxes migrated concurrently (CSV rows in flight at once).
parallel_mailboxes = 10
# Extra folder pipelines *inside* one large mailbox (connection pairs).
parallel_folders = 3
# Simultaneous connections allowed per server host (be kind to servers).
per_host_connections = 8
# Mailbox admission order: "csv" (file order) or "size" (largest first).
order = "csv"
# IMAP MULTIAPPEND (RFC 3502): upload several messages in ONE append
# command when the DESTINATION server advertises support. This is a
# transport optimisation - it never changes what is migrated, and a
# destination without the capability transparently uses one append per
# message. "auto" uses it whenever it is available; "off" never does
# (troubleshooting / awkward servers).
multiappend = "auto"
# How many messages may be grouped into one MULTIAPPEND command.
# "auto" sizes each batch from message sizes, an aggregate byte budget,
# the source fetch pipeline and the concurrency configured above.
# A fixed count (2-100) overrides it.
#
#   IMPORTANT: this is NOT parallel_mailboxes.
#     parallel_mailboxes      = mailboxes migrated CONCURRENTLY
#     multiappend_batch_size  = messages grouped into ONE append command
multiappend_batch_size = "auto"
# Skip mailboxes already recorded SUCCESS in the State Database.
skip_completed = false
# Every APPEND is confirmed via APPENDUID / destination probe before a
# message is marked done. Always on — recorded here for transparency.
verify_after_copy = true

[network]
# Per-connection inactivity watchdog in seconds.
timeout_seconds = 120
# COMPRESS=DEFLATE when the server offers it: "auto" or "off".
compress = "auto"

[retry]
# Attempts per batch level during failure isolation (see [recovery]).
batch_attempts = 3
# Transfer passes per message before it is recorded as failed.
message_attempts = 3
# Reconnect attempts per folder for ordinary connection trouble.
reconnect_attempts = 5
# Whole-mailbox retry attempts after a hard failure.
mailbox_attempts = 2
# Base delay in seconds between mailbox retries (doubles each attempt).
retry_delay = 30
# Backoff strategy: "exponential" (only supported value).
reconnect_backoff = "exponential"

[recovery]
# Progressively isolate messages that repeatedly break the transfer
# (batch -> halves -> single) instead of retrying the same batch forever.
isolate_failed_messages = true
# Exclude messages already recorded in the Failed Message Registry from
# the work queue, so one poison message cannot stall every future run.
# Setting this to false is the persistent equivalent of running with
# --retry-failed: failed messages are attempted again, but nothing is
# rewritten in the State Database merely because the setting is off.
# To re-queue messages PERMANENTLY instead, use "mailferry retry-failed",
# which resets them to planned and marks the registry RETRY_PENDING.
skip_known_failed_messages = true
# Mark a mailbox stalled after this long without measurable progress
# and recover it automatically (0 disables the supervisor).
stale_timeout_seconds = 300
# Connection-recovery attempts per stall before Recovery Mode.
recovery_retries = 3
# Wait between connection-recovery attempts, in seconds.
recovery_interval_seconds = 30

[logging]
# Directory for logs. Empty = the native per-OS default.
# directory = ""
# "info" (default), "debug" (full tracebacks) or "trace" (wire protocol,
# credentials always redacted).
level = "info"
# Delete per-run logs older than this many days at startup (0 = keep all).
keep_days = 30

[dashboard]
# Dashboard refresh interval in milliseconds (display only).
refresh_ms = 250
# Show the live wire-throughput Speed column.
show_transfer_speed = true

[database]
# Absolute path of the State Database. Empty = the native per-OS default
# (mailferry.db under the platform application-state directory).
# path = ""
# Worker/lease heartbeat interval in seconds.
heartbeat_seconds = 15
# A cluster worker silent for this long is offline; its mailboxes are
# reclaimed automatically by the remaining workers.
worker_timeout_seconds = 60
# Hard cap for any unexplained lease left in the State Database.
lock_timeout_seconds = 300

# ---------------------------------------------------------------------------
# OAuth 2.0 / modern authentication (optional)
# ---------------------------------------------------------------------------
# The easiest way to create these profiles is the auth command, which
# authorises in the browser (or headless) and stores the secret in your OS
# keychain automatically:
#     mailferry auth add microsoft365 --name client-a --client-id <guid> --tenant <tenant>
#     mailferry auth add google        --name gmail-a  --client-id <id> --client-secret env:SECRET
#     mailferry auth list | show | test | refresh | remove
# See docs/OAUTH.md. You can also write [oauth.<name>] sections by hand.
#
# Reference a profile from the CSV by putting "oauth:<name>" in that
# endpoint's password column — the canonical 10-column CSV is otherwise
# unchanged. Source and destination auth are independent.
#
# SECRETS ARE NEVER STORED HERE. client_secret / refresh_token / access_token
# / key_json hold an external secret *reference*, never a literal value:
#     env:VAR_NAME             an environment variable
#     file:/path/to/secret     a file (0600 recommended)
#     keychain:SERVICE[/ACCT]  the OS keychain (macOS security / Linux secret-tool)
#     cmd:program args         stdout of a command
# MailFerry resolves the reference at run time and never writes the secret to
# the State Database, logs, reports or traces. A literal secret is rejected.
#
# kind = delegated (default)  one user's own mailbox (interactive sign-in)
# kind = application          Microsoft 365 app-only: one app -> many mailboxes
# kind = service_account      Google domain-wide delegation -> many mailboxes
#
# Delegated Microsoft 365 (SASL XOAUTH2, outlook.office365.com:993):
# [oauth.client-a]
# provider = "microsoft"
# tenant = "common"                 # or your tenant GUID / domain
# client_id = "00000000-0000-0000-0000-000000000000"
# refresh_token = "keychain:mailferry/client-a"
#
# Delegated Google / Gmail (SASL XOAUTH2, imap.gmail.com:993):
# [oauth.gmail-a]
# provider = "google"
# client_id = "xxxxx.apps.googleusercontent.com"
# client_secret = "env:MAILFERRY_GOOGLE_SECRET"
# refresh_token = "file:/etc/mailferry/gmail.refresh"
#
# Bulk Microsoft 365 (one tenant app -> many mailboxes, IMAP.AccessAsApp):
# [oauth.tenant-bulk]
# provider = "microsoft"
# kind = "application"
# tenant = "example.com"
# client_id = "00000000-0000-0000-0000-000000000000"
# client_secret = "keychain:mailferry/tenant-bulk"
#
# Bulk Google Workspace (service account + domain-wide delegation):
# [oauth.ws-bulk]
# provider = "google"
# kind = "service_account"
# key_json = "file:/etc/mailferry/service-account.json"
#
# Any standards-compliant OAuth IMAP server (explicit endpoints):
# [oauth.custom]
# provider = "generic"
# token_url = "https://id.example.com/oauth2/token"
# client_id = "mailferry"
# refresh_token = "env:MAILFERRY_CUSTOM_REFRESH"
# # mechanism = "OAUTHBEARER"        # default is XOAUTH2
`

// DefaultTOMLPath is the native per-OS configuration location
// (MAILFERRY_CONFIG_DIR overrides it for tests/controlled deployments).
func DefaultTOMLPath() string {
	return paths.Default().ConfigFile
}

// FindTOML resolves which configuration file applies, WITHOUT touching
// the filesystem beyond read-only stats:
//
//	--config PATH > ./mailferry.toml > native location > legacy dev location
//
// legacy reports whether the chosen file is a pre-rc.2 development
// location (e.g. ~/.config/mailferry on macOS) — read, never written.
func FindTOML(explicit string) (path string, legacy bool) {
	if explicit != "" {
		return explicit, false
	}
	if _, err := os.Stat("mailferry.toml"); err == nil {
		return "mailferry.toml", false
	}
	p := paths.Default()
	if _, err := os.Stat(p.ConfigFile); err == nil {
		return p.ConfigFile, false
	}
	if p.LegacyConfigFile != "" {
		if _, err := os.Stat(p.LegacyConfigFile); err == nil {
			return p.LegacyConfigFile, true
		}
	}
	return p.ConfigFile, false
}

// parseTOML handles the subset MailFerry generates: [sections],
// key = value with strings/ints/floats/bools. Junk lines raise.
func parseTOML(text string) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	section := ""
	for n, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if _, ok := out[section]; !ok {
				out[section] = map[string]any{}
			}
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			return nil, fmt.Errorf("line %d: expected 'key = value' or '[section]'", n+1)
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		if i := strings.Index(v, "#"); i >= 0 && !strings.HasPrefix(v, `"`) && !strings.HasPrefix(v, "'") {
			v = strings.TrimSpace(v[:i])
		}
		var val any
		switch {
		case strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) && len(v) >= 2:
			val = v[1 : len(v)-1]
		case strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") && len(v) >= 2:
			val = v[1 : len(v)-1]
		case v == "true", v == "false":
			val = v == "true"
		default:
			if iv, err := strconv.ParseInt(v, 10, 64); err == nil {
				val = iv
			} else if fv, err := strconv.ParseFloat(v, 64); err == nil {
				val = fv
			} else {
				val = v
			}
		}
		if section == "" {
			return nil, fmt.Errorf("line %d: key outside a [section]", n+1)
		}
		out[section][k] = val
	}
	return out, nil
}

// templateEntry is one documented option from TOMLTemplate — the template
// doubles as the catalogue of known options with their comments.
type templateEntry struct {
	Section  string
	Key      string
	Comments []string
	Line     string // "key = default"
}

func templateCatalog() []templateEntry {
	var out []templateEntry
	section := ""
	var pending []string
	for _, raw := range strings.Split(TOMLTemplate, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			pending = nil
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			section = strings.TrimSpace(line[1 : len(line)-1])
			pending = nil
		case strings.HasPrefix(line, "#"):
			if section != "" {
				pending = append(pending, line)
			}
		case strings.Contains(line, "=") && section != "":
			key := strings.TrimSpace(line[:strings.Index(line, "=")])
			out = append(out, templateEntry{section, key, pending, line})
			pending = nil
		}
	}
	return out
}

// upgradeAppend documents options this MailFerry version knows about that an
// older configuration file does not mention. It ONLY ever appends a fully
// commented block (never touches existing lines, never re-opens [sections]),
// so user customisations are preserved and the file stays valid for strict
// TOML parsers. Returns how many options were documented.
func upgradeAppend(path, raw string, tree map[string]map[string]any) int {
	var missing []templateEntry
	for _, e := range templateCatalog() {
		if body, ok := tree[e.Section]; ok {
			if _, ok2 := body[e.Key]; ok2 {
				continue // present and active
			}
		}
		if strings.Contains(raw, e.Key) {
			continue // mentioned somewhere (commented out counts) — leave alone
		}
		missing = append(missing, e)
	}
	if len(missing) == 0 {
		return 0
	}
	var b strings.Builder
	if !strings.HasSuffix(raw, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n# ---- New options documented by MailFerry v" + identity.Version +
		" on " + time.Now().Format("2006-01-02") + " ----\n")
	b.WriteString("# These are the built-in defaults, shown for reference only. To change\n")
	b.WriteString("# one, uncomment it under its [section] above (adding the section if it\n")
	b.WriteString("# is missing). This block is safe to edit or delete.\n")
	for _, e := range missing {
		b.WriteString("#\n# [" + e.Section + "]\n")
		for _, cline := range e.Comments {
			b.WriteString("# " + strings.TrimPrefix(cline, "# ") + "\n")
		}
		b.WriteString("# " + e.Line + "\n")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0 // best effort: read-only config is fine
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		return 0
	}
	return len(missing)
}

// LoadTOML applies the config file onto a defaults Run. Never fatal: every
// problem is a warning and the default stands. Returns (warnings, path,
// created).
func LoadTOML(r *Run, explicit string, generate bool) ([]string, string, bool) {
	var warns []string
	path, legacy := FindTOML(explicit)
	if legacy {
		warns = append(warns, fmt.Sprintf(
			"configuration loaded from the legacy development location %s — "+
				"the canonical location is %s (move the file there to adopt "+
				"the new default; nothing is moved automatically)",
			path, paths.Default().ConfigFile))
	}
	if _, err := os.Stat(path); err != nil {
		if explicit != "" {
			return []string{fmt.Sprintf("config file %s not found — using built-in defaults", path)}, path, false
		}
		if generate {
			if err := paths.EnsureParent(path); err == nil {
				if err := os.WriteFile(path, []byte(TOMLTemplate), 0o600); err == nil {
					return warns, path, true
				}
			}
			warns = append(warns, fmt.Sprintf("could not create %s — using built-in defaults", path))
		}
		return warns, path, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf("config %s: %v — using built-in defaults", path, err)}, path, false
	}
	tree, err := parseTOML(string(data))
	if err != nil {
		return []string{fmt.Sprintf("config %s: could not parse (%v) — using built-in defaults", path, err)}, path, false
	}
	if generate { // ensure mode only: read-only loads never write anything
		if n := upgradeAppend(path, string(data), tree); n > 0 {
			warns = append(warns, fmt.Sprintf(
				"config: documented %d new option(s) at the end of %s (commented "+
					"defaults — your settings are untouched)", n, path))
		}
	}

	// Apply values through the ONE typed option catalogue (options.go) —
	// the same accessors, bounds and validation the F9 Settings editor and
	// `mailferry config` use, so the interfaces can never drift apart. The
	// few entries below the catalogue are structural (paths) or accepted
	// for transparency, not tunables.
	catalog := map[string]Option{}
	for _, o := range Catalog() {
		catalog[o.Name()] = o
	}
	for sect, body := range tree {
		// [oauth.<name>] sections are authentication profiles, parsed
		// separately by LoadOAuthProfiles — not tunables. Skip them here so
		// they never raise "unknown setting" warnings.
		if sect == "oauth" || strings.HasPrefix(sect, "oauth.") {
			continue
		}
		for key, val := range body {
			full := sect + "." + key
			if o, found := catalog[full]; found {
				if !o.Set(r, val) {
					warns = append(warns, fmt.Sprintf(
						"config: %s = %v invalid — using the default", full, val))
				}
				continue
			}
			ok := true
			switch full {
			case "migration.verify_after_copy":
				_, ok = val.(bool) // always on; accepted for transparency
			case "retry.reconnect_backoff":
				s, isS := val.(string)
				ok = isS && s == "exponential"
			case "database.path":
				sv, isS := val.(string)
				if ok = isS; ok && sv != "" {
					r.DBPath = sv
				}
			case "logging.directory":
				sv2, isS2 := val.(string)
				if ok = isS2; ok && sv2 != "" {
					r.LogsDir = sv2
				}
			case "dashboard.show_transfer_speed":
				_, ok = val.(bool)
			default:
				warns = append(warns, fmt.Sprintf(
					"config: unknown setting '%s' ignored (kept for forward compatibility)", full))
				continue
			}
			if !ok {
				warns = append(warns, fmt.Sprintf(
					"config: %s = %v invalid — using the default", full, val))
			}
		}
	}
	return warns, path, false
}
