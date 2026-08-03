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

// The OS-native secret backends behind the "keychain:" reference scheme.
//
// ONE table governs both directions — writing (CredStore, auth add) and
// reading (SecretRef.Resolve, every run). They were previously written by
// two independent switches, which allowed a reference to be STORED on a
// platform that could not RESOLVE it. Keeping the pair in one file makes
// that class of incoherence impossible:
//
//	macOS    security(1) generic passwords in the login keychain
//	Linux    Secret Service via secret-tool(1) (libsecret)
//	Windows  no plaintext-retrieval CLI — the DPAPI backend is used
//	         instead (see dpapi_windows.go and the "dpapi:" scheme)
//
// Every backend reports availability BEFORE it is used, so `auth add` can
// choose a backend it knows the migration run will be able to read back.
package oauth

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// keychainAvailable reports whether an OS-native secret service is reachable
// for storing AND resolving a "keychain:" reference on this platform.
func keychainAvailable() bool {
	switch runtime.GOOS {
	case "darwin":
		_, err := exec.LookPath("security")
		return err == nil
	case "linux":
		_, err := exec.LookPath("secret-tool")
		return err == nil
	default:
		return false
	}
}

// keychainStoreSecret writes service/account = secret to the OS store.
// The secret is handed to the helper over stdin where the helper supports
// it (Linux), so it never appears in a process argument list; macOS
// security(1) has no stdin form for add-generic-password.
func keychainStoreSecret(service, account, secret string) error {
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("security", "add-generic-password",
			"-U", "-s", service, "-a", account, "-w", secret)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("macOS keychain refused the item: %s", firstLine(string(out)))
		}
		return nil
	case "linux":
		cmd := exec.Command("secret-tool", "store", "--label=MailFerry "+account,
			"service", service, "account", account)
		cmd.Stdin = strings.NewReader(secret)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("Secret Service refused the item: %s", firstLine(string(out)))
		}
		return nil
	default:
		return errors.New("no OS keychain backend on this platform")
	}
}

// keychainLookup reads service/account back out of the OS store.
func keychainLookup(service, account string) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		args := []string{"find-generic-password", "-s", service, "-w"}
		if account != "" {
			args = append(args, "-a", account)
		}
		out, err := exec.Command("security", args...).Output()
		if err != nil {
			return "", fmt.Errorf("macOS keychain lookup for service %q failed "+
				"(is the item present and the keychain unlocked?)", service)
		}
		return strings.TrimRight(string(out), "\r\n"), nil
	case "linux":
		args := []string{"lookup", "service", service}
		if account != "" {
			args = append(args, "account", account)
		}
		out, err := exec.Command("secret-tool", args...).Output()
		if err != nil {
			return "", fmt.Errorf("Secret Service lookup for service %q failed "+
				"(is secret-tool installed and the collection unlocked?)", service)
		}
		return strings.TrimRight(string(out), "\r\n"), nil
	default:
		return "", fmt.Errorf("keychain: references are not resolvable on %s — "+
			"use dpapi: (Windows), env:, file: or cmd:", runtime.GOOS)
	}
}

// keychainRemove deletes an item. A missing item is not an error.
func keychainRemove(service, account string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("security", "delete-generic-password",
			"-s", service, "-a", account).Run()
	case "linux":
		if _, err := exec.LookPath("secret-tool"); err == nil {
			exec.Command("secret-tool", "clear", "service", service, "account", account).Run()
		}
	}
}

// firstLine keeps helper diagnostics to one line and never echoes a secret
// (helpers report status, not values).
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "no diagnostic"
	}
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}
