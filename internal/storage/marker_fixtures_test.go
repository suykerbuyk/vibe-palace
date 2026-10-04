// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// Fixture trees for the marker-gated behaviour (task
// tidy-pull-and-audit-behaviour-keyed-on-the-migration-marker, XC5). They are
// hand-built and never run the migration command:
//
//   - "migrated": the marker, the derived ignore lines, and `git rm --cached`
//     of the derived paths plus their deletion from the working tree, all in
//     one commit;
//   - "migrated then reverted": the same, plus `git revert`.

const (
	fixtureDrawer = "palace/p/drawers/w/r/drawers.jsonl"
	markerDate    = "2026-10-04"
	malformedToml = "format = 2\nauthored_only = 5\n"
)

// layUnmigratedDrawerVault commits a tracked drawer, a tracked format stamp
// and the canonical .gitignore: a v8 vault before the migration.
func layUnmigratedDrawerVault(t *testing.T) string {
	t.Helper()
	dir := initTestRepo(t)
	// The v8 lines, written directly rather than by the reconciler under test.
	canonical, _ := appendMissingGitignoreLines(nil, CanonicalGitignorePatterns)
	writeFile(t, dir, ".gitignore", string(canonical))
	if err := surface.WriteFormat(dir, surface.RequiredDataFormat); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, fixtureDrawer, `{"id":"d1"}`+"\n")
	gitRun(t, dir, "add", "--", ".gitignore", ".vibe-palace/vault.toml", fixtureDrawer)
	gitRun(t, dir, "commit", "-q", "-m", "v8 vault")
	return dir
}

// migrateFixture makes the migration commit on dir by hand: the marker, the
// derived ignore lines, and the drawers untracked and deleted.
func migrateFixture(t *testing.T, dir string) {
	t.Helper()
	if err := surface.WriteVaultManifest(dir, surface.VaultManifest{Format: surface.RequiredDataFormat, AuthoredOnly: markerDate}); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "rm", "-q", "-r", "--cached", "--", "palace/p/drawers")
	gitRun(t, dir, "add", "--", ".gitignore", ".vibe-palace/vault.toml")
	gitRun(t, dir, "commit", "-q", "-m", "migration")
	gitRun(t, dir, "clean", "-q", "-f", "-X", "--", "palace/p/drawers")
}

// layMigratedVault is a "migrated" fixture.
func layMigratedVault(t *testing.T) string {
	t.Helper()
	dir := layUnmigratedDrawerVault(t)
	migrateFixture(t, dir)
	return dir
}

// layMigratedThenRevertedVault is a "migrated then reverted" fixture.
func layMigratedThenRevertedVault(t *testing.T) string {
	t.Helper()
	dir := layMigratedVault(t)
	gitRun(t, dir, "revert", "--no-edit", "HEAD")
	return dir
}

// writeMalformedMarker lays a manifest whose marker has the wrong type.
func writeMalformedMarker(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, dir, ".vibe-palace/vault.toml", malformedToml)
}

// hasDerivedLines reports whether .gitignore holds any derived-index line.
func hasDerivedLines(t *testing.T, dir string) bool {
	t.Helper()
	present, err := gitignorePresentLines(dir + "/.gitignore")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range MigratedVaultGitignorePatterns {
		if _, ok := present[l]; ok {
			return true
		}
	}
	return false
}

// wantMarkerErr fails unless err is non-nil and names the marker key.
func wantMarkerErr(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "authored_only") {
		t.Fatalf("%s: want an error naming authored_only, got %v", what, err)
	}
}
