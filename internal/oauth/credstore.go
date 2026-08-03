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

// Secure credential storage for the `mailferry auth` workflow.
//
// SECURITY POSTURE (deliberate, documented, and coherent on every OS):
//
//	macOS    login keychain via security(1)          ref: keychain:SERVICE/ACCOUNT
//	Linux    Secret Service via secret-tool(1)       ref: keychain:SERVICE/ACCOUNT
//	Windows  DPAPI (CryptProtectData, current user)  ref: dpapi:PATH
//	last resort, any OS: a 0600 file                 ref: file:PATH
//
// A backend is chosen ONLY when MailFerry has verified it can also READ the
// value back on this platform — storing a reference a later run could not
// resolve is a defect, not a fallback.
//
// The 0600-file backend is filesystem-permission protection, NOT encryption:
// MailFerry does NOT invent crypto and does NOT store a key beside the
// ciphertext (that would be security theatre). It is reported to the operator
// so the choice is explicit, and the documented secure alternative on a
// headless Linux host without a Secret Service is to keep the secret in an
// external manager and reference it with env:, file: (on encrypted storage)
// or cmd: (e.g. a Vault/1Password CLI).
//
// Either way the profile in mailferry.toml holds only a REFERENCE; the secret
// itself never enters mailferry.toml, the CSV, the State Database, logs,
// traces or reports.
package oauth

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Backend names the storage mechanism a secret landed in, for operator
// feedback and for `auth show`.
type Backend string

const (
	BackendKeychain Backend = "OS keychain"
	BackendDPAPI    Backend = "Windows DPAPI (current user)"
	BackendFile     Backend = "0600 file"
)

// Description is the operator-facing sentence for a backend.
func (b Backend) Description() string {
	switch b {
	case BackendKeychain:
		return "the OS keychain"
	case BackendDPAPI:
		return "a DPAPI-protected file (encrypted for your Windows account)"
	default:
		return "a 0600 file (no OS secret service is available on this host)"
	}
}

// CredStore persists and removes named secrets, returning a SecretRef the
// configuration can carry. FileDir is where file-backed secrets live (the
// caller passes the app credentials directory).
type CredStore struct {
	Service string // keychain service name, e.g. "mailferry"
	FileDir string // directory for dpapi:/file: backed secrets
	// forceFile makes the store skip every OS-native backend (tests, or an
	// operator who explicitly prefers a plain file).
	forceFile bool
}

// NewCredStore builds a store for the given credentials directory.
func NewCredStore(fileDir string) *CredStore {
	return &CredStore{Service: "mailferry", FileDir: fileDir}
}

// UseFileOnly forces the 0600-file backend (skips every OS-native backend).
func (c *CredStore) UseFileOnly() *CredStore { c.forceFile = true; return c }

// Preferred reports the backend Store would choose right now, without
// storing anything — used by `auth add` to tell the operator up front where
// the credential is going to live.
func (c *CredStore) Preferred() Backend {
	switch {
	case c.forceFile:
		return BackendFile
	case keychainAvailable():
		return BackendKeychain
	case dpapiAvailable():
		return BackendDPAPI
	default:
		return BackendFile
	}
}

// Store persists secret under name and returns a SecretRef plus the backend
// it landed in. The secret value is never logged. A backend failure degrades
// to the next one rather than failing the authorisation the operator has
// already completed in the browser.
func (c *CredStore) Store(name, secret string) (ref string, backend Backend, err error) {
	if !c.forceFile {
		if keychainAvailable() {
			if serr := keychainStoreSecret(c.Service, name, secret); serr == nil {
				return "keychain:" + c.Service + "/" + name, BackendKeychain, nil
			}
		}
		if dpapiAvailable() {
			if r, derr := c.dpapiStore(name, secret); derr == nil {
				return r, BackendDPAPI, nil
			}
		}
	}
	r, ferr := c.fileStore(name, secret)
	if ferr != nil {
		return "", "", ferr
	}
	return r, BackendFile, nil
}

// Delete removes a stored secret from every backend it could be in. Missing
// is not an error (idempotent) — `auth remove` must always be able to finish.
func (c *CredStore) Delete(name string) error {
	if !c.forceFile {
		keychainRemove(c.Service, name)
	}
	var firstErr error
	for _, p := range []string{c.filePath(name), c.dpapiPath(name)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (c *CredStore) filePath(name string) string {
	return filepath.Join(c.FileDir, safeName(name)+".secret")
}

func (c *CredStore) dpapiPath(name string) string {
	return filepath.Join(c.FileDir, safeName(name)+".dpapi")
}

// ensureDir creates the credentials directory with owner-only permissions.
func (c *CredStore) ensureDir() error {
	if c.FileDir == "" {
		return fmt.Errorf("no credential directory configured")
	}
	return os.MkdirAll(c.FileDir, 0o700)
}

// dpapiStore writes a DPAPI-protected secret and returns a dpapi: reference.
func (c *CredStore) dpapiStore(name, secret string) (string, error) {
	if err := c.ensureDir(); err != nil {
		return "", err
	}
	blob, err := dpapiProtect([]byte(secret))
	if err != nil {
		return "", err
	}
	p := c.dpapiPath(name)
	if err := os.WriteFile(p, blob, 0o600); err != nil {
		return "", err
	}
	return "dpapi:" + p, nil
}

// fileStore writes the secret to a 0600 file and returns a file: reference.
func (c *CredStore) fileStore(name, secret string) (string, error) {
	if err := c.ensureDir(); err != nil {
		return "", err
	}
	p := c.filePath(name)
	if err := os.WriteFile(p, []byte(secret), 0o600); err != nil {
		return "", err
	}
	return "file:" + p, nil
}

// safeName keeps a profile name filesystem-safe.
func safeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
