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

// Package identity is the single source of truth for MailFerry's version
// and branding. Every banner, report, header and artefact derives from
// these constants — never hard-code them elsewhere.
//
// ---------------------------------------------------------------------------
// THE ONLY PLACE A VERSION IS WRITTEN
// ---------------------------------------------------------------------------
//
// Two constants define the release: Core (the semantic version) and Stage
// (the release stage, empty for a final release). EVERYTHING else — the
// Dashboard title, the splash, `version`, Help/About, release artefact
// names, the git tag, log headers, diagnostics, the IMAP ID MailFerry sends,
// generated documentation and the build script — derives from them.
//
// Promoting a release candidate to the final release is therefore a
// one-line change: set Stage to "". Nothing else in the repository needs
// touching, and a test enforces that no other version string exists.
//
//	Core = "2.1.0", Stage = "dev"     → v2.1.0-dev      "v2.1.0 dev"
//	Core = "2.1.0", Stage = "alpha1"  → v2.1.0-alpha1   "v2.1.0 alpha1"
//	Core = "2.1.0", Stage = "beta2"   → v2.1.0-beta2    "v2.1.0 beta2"
//	Core = "2.1.0", Stage = "RC1"     → v2.1.0-RC1      "v2.1.0 RC1"
//	Core = "2.1.0", Stage = ""        → v2.1.0          "v2.1.0"
//
// Two renderings exist because they serve different masters. Version is the
// SemVer 2.0.0 form with a pre-release suffix — it goes into tags, artefact
// filenames and anything a machine compares. Display is the human form,
// where a space reads better than a hyphen.
package identity

import "fmt"

const (
	// Core is the semantic version: MAJOR.MINOR.PATCH, nothing else.
	Core = "2.1.0"
	// Stage is the release stage — "dev", "alpha1", "beta2", "RC1", … —
	// or "" for a final release. This is the ONLY place it is written.
	Stage = ""

	Product      = "MailFerry"
	Title        = "MailFerry – IMAP Migration & Sync"
	Slogan       = "High-Performance Native IMAP Migration Engine"
	Author       = "Andy Saputra"
	AuthorEmail  = "andy@saputra.org"
	Copyright    = "Copyright (C) 2026 Andy Saputra"
	Repository   = "https://github.com/ajsap/mailferry"
	ProjectURL   = "https://saputra.org"
	SupportURL   = "https://github.com/ajsap/mailferry/issues"
	DocsURL      = "https://github.com/ajsap/mailferry/tree/main/docs"
	LicenseName  = "GNU Affero General Public License v3.0"
	LicenseShort = "GNU AGPL v3.0"
)

// Version is the SemVer form: "2.1.0-RC1" for a staged build, "2.1.0" for a
// final release. Use it for tags, artefact names, the IMAP ID and anything
// a machine compares or sorts.
var Version = composeVersion(Core, Stage)

// composeVersion builds the SemVer form. Named (rather than inlined) so the
// stage rules can be tested against every shape, not just the current one.
func composeVersion(core, stage string) string {
	if stage == "" {
		return core
	}
	return core + "-" + stage
}

// composeDisplay builds the human form.
func composeDisplay(core, stage string) string {
	if stage == "" {
		return "v" + core
	}
	return "v" + core + " " + stage
}

// Display is the human form with a leading v: "v2.1.0 RC1", or "v2.1.0" for
// a final release. Use it in banners and anything an operator reads.
var Display = composeDisplay(Core, Stage)

// Prerelease reports whether this build is a staged (non-final) release.
// Surfaces that should say so — the splash, About — ask this rather than
// pattern-matching a version string.
func Prerelease() bool { return Stage != "" }

// BannerLine is the one-line product banner shown across the UI.
func BannerLine() string {
	return fmt.Sprintf("%s %s — IMAP Migration & Sync", Product, Display)
}

// VersionText is the --version output.
func VersionText() string {
	return BannerLine() + "\n" + Slogan + "\n"
}

// AboutLines feeds --about and the Help/About view.
func AboutLines() []string {
	return []string{
		"About " + Product,
		Slogan,
		"",
		fmt.Sprintf("%-14s %s%s", "Version", Display,
			map[bool]string{true: "   (pre-release)"}[Prerelease()]),
		fmt.Sprintf("%-14s %s <%s>", "Author", Author, AuthorEmail),
		fmt.Sprintf("%-14s %s", "Repository", Repository),
		fmt.Sprintf("%-14s %s", "Documentation", DocsURL),
		fmt.Sprintf("%-14s %s", "Issue Tracker", SupportURL),
		fmt.Sprintf("%-14s %s", "Community", ProjectURL),
		fmt.Sprintf("%-14s %s", "Licence", LicenseShort),
		"",
		Copyright,
	}
}

func AboutText() string {
	out := ""
	for _, l := range AboutLines() {
		out += l + "\n"
	}
	return out
}
