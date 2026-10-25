// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLocalOverride writes a .vibe-palace/local.toml holding body under dir
// (the gitignored tier-1 override of ADR-012 Amendment 2026-10-25, Option (b)).
func writeLocalOverride(t *testing.T, dir, body string) string {
	t.Helper()
	od := filepath.Join(dir, ".vibe-palace")
	if err := os.MkdirAll(od, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(od, "local.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Test: the new .vibe-palace/local.toml override resolves tier-1, with a
// cwd-local: source. MUTATION: point tier-1 back at the identity marker and the
// source prefix flips / the path reverts to tier-3.
func TestResolveTier1_LocalOverrideWins(t *testing.T) {
	f := newBindFixture(t)
	alt := makeTestVault(t, filepath.Join(f.home, "alt-vault"))
	dir := f.checkout(t, "loose", "[project]\nname = \"loose\"\n")
	local := writeLocalOverride(t, dir, "vault_path = \""+alt+"\"\n")

	r := mustResolve(t, dir)
	if r.Path != alt {
		t.Errorf("Path = %q, want %q", r.Path, alt)
	}
	if want := "cwd-local:" + local; r.Source != want {
		t.Errorf("Source = %q, want %q", r.Source, want)
	}
	if len(r.Warnings) != 0 {
		t.Errorf("override-only resolution should not warn; got %v", r.Warnings)
	}
}

// Test: the override wins over a legacy .vibe-palace.toml vault_path even when
// both are present and name the SAME root; the redundant legacy key is flagged.
func TestResolveTier1_OverrideWinsOverLegacySameRoot(t *testing.T) {
	f := newBindFixture(t)
	alt := makeTestVault(t, filepath.Join(f.home, "alt-vault"))
	// Legacy marker sets the same root (trailing slash to prove sameVaultRoot, not
	// string equality, decides it).
	dir := f.checkout(t, "loose", "vault_path = \""+alt+"/\"\n[project]\nname = \"loose\"\n")
	local := writeLocalOverride(t, dir, "vault_path = \""+alt+"\"\n")

	r := mustResolve(t, dir)
	if r.Path != alt {
		t.Errorf("Path = %q, want %q", r.Path, alt)
	}
	if !strings.HasPrefix(r.Source, "cwd-local:") || !strings.HasSuffix(r.Source, local) {
		t.Errorf("Source = %q, want cwd-local: suffixed by %q", r.Source, local)
	}
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "redundant") {
		t.Errorf("want one 'redundant' warning naming the legacy key; got %v", r.Warnings)
	}
}

// Test: a legacy .vibe-palace.toml vault_path is still honored during the
// deprecation window when there is no override file, AND the warning is surfaced
// in Resolution.Warnings (the channel vp status prints).
func TestResolveTier1_LegacyHonoredWithDeprecationWarning(t *testing.T) {
	f := newBindFixture(t)
	alt := makeTestVault(t, filepath.Join(f.home, "alt-vault"))
	dir := f.checkout(t, "loose", "vault_path = \""+alt+"\"\n[project]\nname = \"loose\"\n")

	r := mustResolve(t, dir)
	if r.Path != alt {
		t.Errorf("Path = %q, want %q (legacy honored during the window)", r.Path, alt)
	}
	if want := "cwd:" + filepath.Join(dir, ".vibe-palace.toml"); r.Source != want {
		t.Errorf("Source = %q, want %q (legacy keeps the cwd: prefix)", r.Source, want)
	}
	if len(r.Warnings) == 0 {
		t.Fatalf("legacy tier-1 must surface a deprecation warning; got none")
	}
	w := strings.Join(r.Warnings, "\n")
	for _, want := range []string{".vibe-palace.toml", "DEPRECATED", ".vibe-palace/local.toml"} {
		if !strings.Contains(w, want) {
			t.Errorf("deprecation warning missing %q:\n%s", want, w)
		}
	}
}

// Test: the override and the legacy marker setting DIFFERENT vaults is a refusal
// that wraps ErrVaultBindingRejected and names both files and both roots. The
// refusal must NOT be a bare error — a non-sentinel error falls through vp hook
// to the live vault.
func TestResolveTier1_OverrideVsLegacyDisagreementRefuses(t *testing.T) {
	f := newBindFixture(t)
	newV := makeTestVault(t, filepath.Join(f.home, "new-vault"))
	oldV := makeTestVault(t, filepath.Join(f.home, "old-vault"))
	dir := f.checkout(t, "loose", "vault_path = \""+oldV+"\"\n[project]\nname = \"loose\"\n")
	local := writeLocalOverride(t, dir, "vault_path = \""+newV+"\"\n")

	err := mustReject(t, dir) // asserts ErrVaultBindingRejected and not fs.ErrNotExist
	for _, want := range []string{local, filepath.Join(dir, ".vibe-palace.toml"), newV, oldV} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("disagreement refusal does not name %q:\n%v", want, err)
		}
	}
}

// Test: an override that disagrees with a tier-2 binding still refuses (the
// tier-1-vs-tier-2 check now keyed off the override file), and the refusal names
// the override file, not the legacy marker.
func TestResolveTier1_OverrideVsTier2Disagreement(t *testing.T) {
	f := newBindFixture(t)
	quantum := makeTestVault(t, filepath.Join(f.home, "quantum-vault"))
	stale := makeTestVault(t, filepath.Join(f.home, "stale-vault"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+quantum+"\"\n")
	dir := f.checkout(t, "qa", "[project]\nname = \"qa\"\n")
	local := writeLocalOverride(t, dir, "vault_path = \""+stale+"\"\n")

	err := mustReject(t, dir)
	for _, want := range []string{local, f.cfg, quantum, stale} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("tier1(override)-vs-tier2 refusal does not name %q:\n%v", want, err)
		}
	}
}

// Test: the override walk stops at $HOME, exactly like the identity-marker walk —
// a $HOME/.vibe-palace/local.toml must not bind a checkout under $HOME.
func TestResolveTier1_OverrideHomeBoundaryStopsWalk(t *testing.T) {
	f := newBindFixture(t)
	leak := makeTestVault(t, filepath.Join(f.home, "should-not-be-used"))
	writeLocalOverride(t, f.home, "vault_path = \""+leak+"\"\n")
	dir := f.checkout(t, "foo", "[project]\nname = \"foo\"\n")
	stubGitSlug(t, "")

	r := mustResolve(t, dir)
	if r.Path == leak {
		t.Fatalf("resolution used $HOME/.vibe-palace/local.toml (%q); the walk must stop at $HOME", leak)
	}
	if r.Path != f.global || !strings.HasPrefix(r.Source, "global:") {
		t.Errorf("got (%q, %q), want the global vault (home boundary blocks the override)", r.Path, r.Source)
	}
}

// Test: the override is found by walking up from a subdirectory, same as the
// identity marker.
func TestResolveTier1_OverrideFoundWalkingUp(t *testing.T) {
	f := newBindFixture(t)
	alt := makeTestVault(t, filepath.Join(f.home, "alt-vault"))
	dir := f.checkout(t, "loose", "[project]\nname = \"loose\"\n")
	local := writeLocalOverride(t, dir, "vault_path = \""+alt+"\"\n")
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	r := mustResolve(t, sub)
	if r.Path != alt || r.Source != "cwd-local:"+local {
		t.Errorf("got (%q, %q), want the override resolved from a child dir", r.Path, r.Source)
	}
}

// Test: an empty vault_path = "" in the override decodes to unset and falls
// through to the lower tiers (same predicate as today), without warning.
func TestResolveTier1_OverrideEmptyVaultPathIsUnset(t *testing.T) {
	f := newBindFixture(t)
	dir := f.checkout(t, "loose", "[project]\nname = \"loose\"\n")
	writeLocalOverride(t, dir, "vault_path = \"\"\n")
	stubGitSlug(t, "")

	r := mustResolve(t, dir)
	if r.Path != f.global || !strings.HasPrefix(r.Source, "global:") {
		t.Errorf("got (%q, %q), want the global vault (empty override vault_path is unset)", r.Path, r.Source)
	}
}

// Test (F1): a ~-path whose expansion fails (HOME unset, so UserHomeDir fails)
// is a REFUSAL wrapping ErrVaultBindingRejected, not a bare error — otherwise
// vp hook falls through to the live vault. MUTATION: drop the sentinel wrap in
// expandAbsVaultPath and this goes red (the error stops wrapping the sentinel).
func TestResolveTier1_UnexpandableTildeRefuses(t *testing.T) {
	f := newBindFixture(t)
	dir := f.checkout(t, "loose", "[project]\nname = \"loose\"\n")
	writeLocalOverride(t, dir, "vault_path = \"~/nope\"\n")
	// Make os.UserHomeDir fail. XDG_CONFIG_HOME stays absolute, so the global
	// config still resolves; only the ~-expansion of the override breaks.
	t.Setenv("HOME", "")

	_, err := ResolveVaultBinding(dir)
	if !errors.Is(err, ErrVaultBindingRejected) {
		t.Fatalf("errors.Is(err, ErrVaultBindingRejected) = false; an unexpandable ~ must refuse, not "+
			"fall through to the live vault: %v", err)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("refusal wraps fs.ErrNotExist (check/skills-show read that as \"no config\"): %v", err)
	}
}

// Test (F2): a CHILD .vibe-palace.toml (identity only) plus a PARENT
// .vibe-palace/local.toml must NOT bind the child to the parent's throwaway vault.
// The override is consulted only CO-LOCATED with the identity marker (ADR-012
// rule 4a). MUTATION: give readLocalOverride back its independent upward walk and
// this goes red — the child resolves to the parent's throwaway vault.
func TestResolveTier1_ParentOverrideDoesNotBindChildMarker(t *testing.T) {
	f := newBindFixture(t)
	throwaway := makeTestVault(t, filepath.Join(f.home, "throwaway-vault"))
	parent := filepath.Join(f.home, "code", "parent")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	// Child: identity only, no vault_path.
	if err := os.WriteFile(filepath.Join(child, ".vibe-palace.toml"), []byte("[project]\nname = \"qa\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Parent: a throwaway override, in a DIFFERENT directory than the child marker.
	writeLocalOverride(t, parent, "vault_path = \""+throwaway+"\"\n")
	stubGitSlug(t, "")

	t.Run("unbound_name_falls_to_global_not_parent_throwaway", func(t *testing.T) {
		r := mustResolve(t, child)
		if r.Path == throwaway {
			t.Fatalf("child bound to the PARENT's throwaway vault %q; a parent override must not bind a "+
				"child with its own identity marker (rule 4a)", throwaway)
		}
		if r.Path != f.global || !strings.HasPrefix(r.Source, "global:") {
			t.Errorf("got (%q, %q), want the global vault (tier-3), parent override ignored", r.Path, r.Source)
		}
	})

	t.Run("bound_name_resolves_tier2_not_parent_throwaway", func(t *testing.T) {
		quantum := makeTestVault(t, filepath.Join(f.home, "quantum-vault"))
		f.writeConfig(t, "\n[project_vaults]\nqa = \""+quantum+"\"\n")
		r := mustResolve(t, child)
		if r.Path != quantum {
			t.Errorf("Path = %q, want the tier-2 binding %q (never the parent throwaway)", r.Path, quantum)
		}
	})
}

// Test: a throwaway tree whose ONLY vibe-palace file is an override (no identity
// marker) keeps the override's own $HOME-bounded walk and is honored.
func TestResolveTier1_OverrideOnlyNoMarkerHonored(t *testing.T) {
	f := newBindFixture(t)
	alt := makeTestVault(t, filepath.Join(f.home, "alt-vault"))
	dir := filepath.Join(f.home, "code", "scratch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	local := writeLocalOverride(t, dir, "vault_path = \""+alt+"\"\n")
	stubGitSlug(t, "")

	r := mustResolve(t, dir)
	if r.Path != alt || r.Source != "cwd-local:"+local {
		t.Errorf("got (%q, %q), want the override honored for a no-marker throwaway tree", r.Path, r.Source)
	}
}

// Test (C1): a tree whose .vibe-palace is a plain FILE (a stray sentinel, not the
// override directory) must resolve WITHOUT a vault-binding refusal — ENOTDIR means
// the override cannot exist, which is "no override", not a hidden lever. MUTATION:
// drop the syscall.ENOTDIR arm in ReadLocalOverrideInDir and this goes red (the
// resolver refuses instead of falling through to tier-3).
func TestResolveTier1_DotVibePalaceFileIsNotAnOverride(t *testing.T) {
	f := newBindFixture(t)
	dir := f.checkout(t, "loose", "[project]\nname = \"loose\"\n")
	// Occupy .vibe-palace with a regular file, so .vibe-palace/local.toml is ENOTDIR.
	if err := os.WriteFile(filepath.Join(dir, ".vibe-palace"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubGitSlug(t, "")

	r, err := ResolveVaultBinding(dir)
	if err != nil {
		t.Fatalf("a .vibe-palace FILE must not refuse resolution (ENOTDIR = no override): %v", err)
	}
	if r.Path != f.global || !strings.HasPrefix(r.Source, "global:") {
		t.Errorf("got (%q, %q), want the global vault (tier-3; the stray file is not an override)", r.Path, r.Source)
	}
}

// Test: a vault_path swallowed by a table in the OVERRIDE file is refused, the
// same defence the identity marker has — it must never silently fall through to
// the live vault.
func TestResolveTier1_OverrideSwallowedVaultPathRefused(t *testing.T) {
	f := newBindFixture(t)
	dir := f.checkout(t, "loose", "[project]\nname = \"loose\"\n")
	// [section] header above vault_path scopes the key into the table.
	writeLocalOverride(t, dir, "[section]\nvault_path = \""+filepath.Join(f.home, "tw")+"\"\n")

	_, err := ResolveVaultBinding(dir)
	if !errors.Is(err, ErrVaultBindingRejected) {
		t.Fatalf("errors.Is(err, ErrVaultBindingRejected) = false; a swallowed override vault_path must refuse: %v", err)
	}
	if !errors.Is(err, ErrSwallowedVaultPath) {
		t.Errorf("errors.Is(err, ErrSwallowedVaultPath) = false; err = %v", err)
	}
}
