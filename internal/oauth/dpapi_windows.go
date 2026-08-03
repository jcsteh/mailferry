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

//go:build windows

// Windows secure credential storage: DPAPI (Data Protection API).
//
// Windows has no first-class command-line credential store that returns a
// plaintext secret (cmdkey deliberately does not), so MailFerry does NOT
// pretend a "keychain:" reference works there. Instead it protects the
// secret with CryptProtectData under the CURRENT USER scope and stores the
// ciphertext in the credentials directory; only the same Windows user
// account on the same machine can decrypt it.
//
// This is real OS-provided cryptography with an OS-managed key — not
// MailFerry-invented crypto, and not a key stored beside the ciphertext.
// The reference written into mailferry.toml is "dpapi:<path>".
//
// CGO-free: crypt32.dll is called through the lazy system-DLL loader in
// golang.org/x/sys/windows, which MailFerry already depends on.
package oauth

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	crypt32              = windows.NewLazySystemDLL("crypt32.dll")
	procCryptProtectData = crypt32.NewProc("CryptProtectData")
	procCryptUnprotect   = crypt32.NewProc("CryptUnprotectData")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procLocalFree        = kernel32.NewProc("LocalFree")
)

// cryptProtectUIForbidden fails rather than prompting: MailFerry runs
// unattended migrations and must never block on a UI dialog.
const cryptProtectUIForbidden = 0x1

// dataBlob is the Win32 DATA_BLOB structure.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(b []byte) dataBlob {
	if len(b) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

// bytes copies the blob out and releases the LocalAlloc'd buffer.
func (b *dataBlob) bytes() []byte {
	if b.pbData == nil || b.cbData == 0 {
		return nil
	}
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	procLocalFree.Call(uintptr(unsafe.Pointer(b.pbData)))
	b.pbData = nil
	return out
}

// dpapiAvailable reports whether DPAPI can be used (always true on Windows
// with crypt32 present).
func dpapiAvailable() bool { return crypt32.Load() == nil }

// dpapiProtect encrypts plaintext for the current Windows user.
func dpapiProtect(plaintext []byte) ([]byte, error) {
	in := newBlob(plaintext)
	var out dataBlob
	r, _, err := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0,
		uintptr(cryptProtectUIForbidden), uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, fmt.Errorf("Windows DPAPI could not protect the credential: %v", err)
	}
	return out.bytes(), nil
}

// dpapiUnprotect decrypts a blob produced by dpapiProtect. It fails for a
// different Windows user or a different machine — by design.
func dpapiUnprotect(ciphertext []byte) ([]byte, error) {
	in := newBlob(ciphertext)
	var out dataBlob
	r, _, err := procCryptUnprotect.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0,
		uintptr(cryptProtectUIForbidden), uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, fmt.Errorf("Windows DPAPI could not unprotect the credential "+
			"(it is bound to the Windows user account and machine that stored it): %v", err)
	}
	return out.bytes(), nil
}
