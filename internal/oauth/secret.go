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

// Secret resolution for OAuth. MailFerry's data-integrity posture is that
// credentials are the operator's and are never persisted by MailFerry (not
// in the State Database, logs, reports or traces). OAuth introduces
// long-lived secrets — client secrets and refresh tokens — so this package
// deliberately refuses to store or even accept them inline in mailferry.toml
// or the CSV. Instead, configuration carries a *reference* to an external
// secret source, resolved at run time:
//
//	env:NAME                 an environment variable
//	file:/path/to/secret     a file (whitespace-trimmed), 0600 recommended
//	keychain:SERVICE[/ACCT]  the OS secret service — macOS login keychain
//	                         (security(1)) or Linux Secret Service
//	                         (secret-tool(1)); see keychain.go
//	dpapi:/path/to/blob      a Windows DPAPI-protected file, decryptable only
//	                         by the Windows account that stored it
//	cmd:program arg ...      stdout of a command (explicit external source)
//
// A literal secret (no recognised scheme) is REJECTED with an actionable
// error, so a token can never end up in a repository-adjacent file by
// accident. Resolved values live only in memory for the run and never reach
// any log, error or report.
package oauth

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// SecretRef is a reference to an external secret source, e.g. "env:MS_RT".
type SecretRef string

// secretSchemes documents the accepted reference forms (for error text).
const secretSchemes = "env:NAME | file:/path | keychain:SERVICE[/ACCOUNT] | dpapi:/path | cmd:program args"

// looksLikeRef reports whether s uses one of the accepted schemes. Used by
// config validation to reject an inline literal secret before a run starts.
func looksLikeRef(s string) bool {
	for _, p := range []string{"env:", "file:", "keychain:", "dpapi:", "cmd:"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// ValidateRef checks a reference's scheme without resolving it (so config
// validation never reads secrets or hits the network/keychain). An empty
// reference is allowed (optional secret); a non-empty literal is rejected.
func ValidateRef(field, ref string) error {
	if ref == "" {
		return nil
	}
	if !looksLikeRef(ref) {
		return fmt.Errorf("%s must be an external secret reference (%s) — a literal "+
			"secret must never be written into configuration", field, secretSchemes)
	}
	return nil
}

// Resolve reads the referenced secret. The returned value is sensitive: it
// must never be logged. Errors are safe to surface (they never contain the
// secret value).
func (r SecretRef) Resolve() (string, error) {
	s := string(r)
	switch {
	case s == "":
		return "", nil
	case strings.HasPrefix(s, "env:"):
		name := strings.TrimPrefix(s, "env:")
		v, ok := os.LookupEnv(name)
		if !ok {
			return "", fmt.Errorf("environment variable %q is not set", name)
		}
		return v, nil
	case strings.HasPrefix(s, "file:"):
		path := strings.TrimPrefix(s, "file:")
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("secret file: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	case strings.HasPrefix(s, "keychain:"):
		return resolveKeychain(strings.TrimPrefix(s, "keychain:"))
	case strings.HasPrefix(s, "dpapi:"):
		return resolveDPAPI(strings.TrimPrefix(s, "dpapi:"))
	case strings.HasPrefix(s, "cmd:"):
		return resolveCmd(strings.TrimPrefix(s, "cmd:"))
	default:
		return "", fmt.Errorf("unrecognised secret reference (expected %s)", secretSchemes)
	}
}

// resolveKeychain reads a secret from the OS-native secret service. The
// backend is chosen per platform by keychain.go — the SAME table `auth add`
// used to store it, so a reference MailFerry wrote is always a reference
// MailFerry can read on that host. Spec is SERVICE or SERVICE/ACCOUNT.
func resolveKeychain(spec string) (string, error) {
	service, account := spec, ""
	if i := strings.Index(spec, "/"); i >= 0 {
		service, account = spec[:i], spec[i+1:]
	}
	return keychainLookup(service, account)
}

// resolveDPAPI reads a Windows DPAPI-protected credential file. On any other
// platform this fails with an explicit, accurate explanation rather than
// pretending the reference is malformed.
func resolveDPAPI(path string) (string, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("dpapi secret file: %w", err)
	}
	plain, err := dpapiUnprotect(blob)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(plain)), nil
}

// resolveCmd runs a command and returns its trimmed stdout. The command is
// the operator's explicit choice of external secret source.
func resolveCmd(cmdline string) (string, error) {
	fields := strings.Fields(cmdline)
	if len(fields) == 0 {
		return "", errors.New("cmd: secret reference is empty")
	}
	out, err := exec.Command(fields[0], fields[1:]...).Output()
	if err != nil {
		return "", fmt.Errorf("secret command %q failed", fields[0])
	}
	return strings.TrimSpace(string(out)), nil
}
