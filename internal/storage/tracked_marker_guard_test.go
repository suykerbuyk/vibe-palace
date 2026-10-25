// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/project"
)

// stubMarkerTracked forces the markerIsTracked seam for one test and restores it
// (the SetMisboundRefuse restore pattern), so a later test in the package is not
// poisoned.
func stubMarkerTracked(t *testing.T, tracked bool, err error) {
	t.Helper()
	orig := markerIsTracked
	markerIsTracked = func(string) (bool, error) { return tracked, err }
	t.Cleanup(func() { markerIsTracked = orig })
}

// Test (U4): a TRACKED .vibe-palace.toml that carries vault_path is a hard refusal
// wrapping ErrVaultBindingRejected (and never fs.ErrNotExist). MUTATION: delete the
// tracked-marker guard in ResolveVaultBinding and this goes red — the marker's
// vault_path is honored (tier-1) with no error.
func TestResolveRefusesTrackedMarkerWithVaultPath(t *testing.T) {
	f := newBindFixture(t)
	alt := makeTestVault(t, filepath.Join(f.home, "alt-vault"))
	dir := f.checkout(t, "loose", "vault_path = \""+alt+"\"\n[project]\nname = \"loose\"\n")
	stubMarkerTracked(t, true, nil)

	err := mustReject(t, dir) // asserts ErrVaultBindingRejected AND not fs.ErrNotExist
	for _, want := range []string{"git-tracked", "identity only", project.LocalOverrideName} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q:\n%v", want, err)
		}
	}
}

// Test (U4, fail-closed): a marker that sets vault_path while git CANNOT be asked
// whether it is tracked is a refusal, not a silent honor. MUTATION: swallow the
// err branch (treat can't-ask as untracked) and this goes red.
func TestResolveRefusesWhenTrackednessUnknown(t *testing.T) {
	f := newBindFixture(t)
	alt := makeTestVault(t, filepath.Join(f.home, "alt-vault"))
	dir := f.checkout(t, "loose", "vault_path = \""+alt+"\"\n[project]\nname = \"loose\"\n")
	stubMarkerTracked(t, false, fmt.Errorf("git exploded"))

	err := mustReject(t, dir)
	for _, want := range []string{"could not be asked", "git exploded"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("fail-closed refusal missing %q:\n%v", want, err)
		}
	}
}

// Test (U4): an UNTRACKED marker with vault_path is NOT refused by the U4 guard —
// it stays the deprecation-window tier-1 lever, with the migration nudge.
func TestResolveHonorsUntrackedMarkerWithVaultPath(t *testing.T) {
	f := newBindFixture(t)
	alt := makeTestVault(t, filepath.Join(f.home, "alt-vault"))
	dir := f.checkout(t, "loose", "vault_path = \""+alt+"\"\n[project]\nname = \"loose\"\n")
	stubMarkerTracked(t, false, nil)

	r := mustResolve(t, dir)
	if r.Path != alt {
		t.Errorf("Path = %q, want the untracked marker's tier-1 vault %q (honored)", r.Path, alt)
	}
	if len(r.Warnings) == 0 {
		t.Errorf("an untracked legacy marker's vault_path must still surface the deprecation nudge; got none")
	}
}

// Test (U4): the guard does NOT run git when the marker carries no vault_path (cost
// bound). The seam is set to explode; resolving a vault_path-less marker must still
// succeed, proving markerIsTracked was never consulted.
func TestResolveSkipsTrackednessWhenNoVaultPath(t *testing.T) {
	f := newBindFixture(t)
	dir := f.checkout(t, "loose", "[project]\nname = \"loose\"\n")
	stubGitSlug(t, "")
	stubMarkerTracked(t, false, fmt.Errorf("markerIsTracked must not be called for a marker without vault_path"))

	r := mustResolve(t, dir)
	if r.Path != f.global {
		t.Errorf("Path = %q, want the global vault (identity-only marker, tracked check skipped)", r.Path)
	}
}
