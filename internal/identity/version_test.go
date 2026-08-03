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

// The single-source version rule, enforced rather than trusted.
//
// The whole point of Core + Stage is that promoting a release candidate to
// the final release is one line. That property is easy to state and easy to
// erode — somebody hard-codes "2.1.0" in a banner and nobody notices until a
// release ships with two different versions in it. These tests make that
// impossible to do quietly.
package identity

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestVersionComposition pins both renderings for every stage shape.
func TestVersionComposition(t *testing.T) {
	cases := []struct {
		core, stage    string
		version, disp  string
		wantPrerelease bool
	}{
		{"2.1.0", "dev", "2.1.0-dev", "v2.1.0 dev", true},
		{"2.1.0", "alpha1", "2.1.0-alpha1", "v2.1.0 alpha1", true},
		{"2.1.0", "beta2", "2.1.0-beta2", "v2.1.0 beta2", true},
		{"2.1.0", "RC1", "2.1.0-RC1", "v2.1.0 RC1", true},
		{"2.1.0", "", "2.1.0", "v2.1.0", false},
		{"2.1.1", "", "2.1.1", "v2.1.1", false},
	}
	for _, c := range cases {
		gotV, gotD := composeVersion(c.core, c.stage), composeDisplay(c.core, c.stage)
		if gotV != c.version {
			t.Errorf("Core=%q Stage=%q: Version = %q, want %q", c.core, c.stage, gotV, c.version)
		}
		if gotD != c.disp {
			t.Errorf("Core=%q Stage=%q: Display = %q, want %q", c.core, c.stage, gotD, c.disp)
		}
		if (c.stage != "") != c.wantPrerelease {
			t.Errorf("Core=%q Stage=%q: prerelease flag wrong", c.core, c.stage)
		}
	}
}

// TestLiveVersionIsConsistent: the package's own values must agree with the
// same composition rules.
func TestLiveVersionIsConsistent(t *testing.T) {
	if Version != composeVersion(Core, Stage) {
		t.Fatalf("Version %q does not derive from Core/Stage", Version)
	}
	if Display != composeDisplay(Core, Stage) {
		t.Fatalf("Display %q does not derive from Core/Stage", Display)
	}
	if Prerelease() != (Stage != "") {
		t.Fatal("Prerelease() disagrees with Stage")
	}
	// Core must be a bare semantic version — the stage never belongs in it.
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(Core) {
		t.Fatalf("Core = %q, want MAJOR.MINOR.PATCH with no suffix", Core)
	}
	// Every operator-facing rendering must carry the version.
	for name, s := range map[string]string{
		"BannerLine": BannerLine(), "VersionText": VersionText(), "AboutText": AboutText(),
	} {
		if !strings.Contains(s, Core) {
			t.Fatalf("%s does not mention the version: %q", name, s)
		}
		if Prerelease() && !strings.Contains(s, Stage) {
			t.Fatalf("%s omits the release stage %q: %q", name, Stage, s)
		}
	}
}

// TestNoDuplicateVersionStringsInSource walks the repository and fails if any
// Go source outside this package hard-codes the current version. This is the
// test that keeps "change it in one place" true.
func TestNoDuplicateVersionStringsInSource(t *testing.T) {
	root := repoRoot(t)
	// The literal we must not find: the current Core version as a quoted
	// string. Comments and changelog prose are fine; a string literal is not.
	needle := regexp.MustCompile(`"` + regexp.QuoteMeta(Core) + `(-[A-Za-z0-9.]+)?"`)

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() {
				name := info.Name()
				// Skip build output and every dot-directory: local tooling
				// and editor state are not part of the repository's source.
				if name == "dist" || name == "node_modules" ||
					(len(name) > 1 && name[0] == '.') {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// This package defines the version; that is the whole point.
		if strings.Contains(filepath.ToSlash(path), "internal/identity/") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // prose may mention any version it likes
			}
			if needle.MatchString(line) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s:%d hard-codes the version — derive it from "+
					"identity.Version / identity.Display / identity.Core instead:\n  %s",
					rel, i+1, trimmed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestBuildScriptUsesTheAuthoritativeVersion: the release build must ask
// MailFerry for its version rather than parsing source or keeping a copy.
func TestBuildScriptUsesTheAuthoritativeVersion(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "build.sh"))
	if err != nil {
		t.Skipf("build.sh not present: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "version --bare") {
		t.Fatal("build.sh must obtain the version from `mailferry version --bare`")
	}
	if strings.Contains(s, "identity.go") {
		t.Fatal("build.sh must not parse identity.go — it should ask the binary")
	}
	if regexp.MustCompile(`VERSION=["']?\d+\.\d+\.\d+`).MatchString(s) {
		t.Fatal("build.sh must not hard-code a version")
	}
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not locate the module root")
	return ""
}
