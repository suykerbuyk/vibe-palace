// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeDepartureRecord drops a minimal, valid departure record for slug under
// vaultRoot, so staleProjectBinding sees the project as departed from the
// default vault (departure.Read reports recorded=true).
func writeDepartureRecord(t *testing.T, vaultRoot, slug string) {
	t.Helper()
	dir := filepath.Join(vaultRoot, "Audits", "departures")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"slug":"` + slug + `","kind":"deleted"}`
	if err := os.WriteFile(filepath.Join(dir, slug+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mustBuild builds the snapshot and fails on error.
func mustBuild(t *testing.T) ProjectVaultSnapshot {
	t.Helper()
	snap, err := BuildProjectVaultSnapshot(context.Background())
	if err != nil {
		t.Fatalf("BuildProjectVaultSnapshot: unexpected error: %v", err)
	}
	if !snap.built {
		t.Fatal("BuildProjectVaultSnapshot returned a snapshot with built==false and no error")
	}
	return snap
}

// Case 1: slug bound to the startup vault -> VaultBoundHere.
func TestProjectVault_BoundHere(t *testing.T) {
	f := newBindFixture(t)
	here := makeTestVault(t, filepath.Join(f.home, "here"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+here+"\"\n")

	snap := mustBuild(t)
	verdict, info, err := ClassifyProjectVault("qa", here, snap)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if verdict != VaultBoundHere {
		t.Fatalf("verdict = %v, want VaultBoundHere", verdict)
	}
	if info != here {
		t.Fatalf("info = %q, want the resolved root %q", info, here)
	}
}

// Case 2: slug bound to a different vault -> VaultBoundElsewhere, info non-empty.
func TestProjectVault_BoundElsewhere(t *testing.T) {
	f := newBindFixture(t)
	other := makeTestVault(t, filepath.Join(f.home, "other"))
	startup := makeTestVault(t, filepath.Join(f.home, "startup"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+other+"\"\n")

	snap := mustBuild(t)
	verdict, info, err := ClassifyProjectVault("qa", startup, snap)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if verdict != VaultBoundElsewhere {
		t.Fatalf("verdict = %v, want VaultBoundElsewhere", verdict)
	}
	if info == "" {
		t.Fatal("info is empty; want the non-empty resolved root (D3)")
	}
	if info != other {
		t.Fatalf("info = %q, want %q", info, other)
	}
}

// Case 3: slug absent from the table -> VaultUnbound, nil, empty info.
func TestProjectVault_Unbound(t *testing.T) {
	f := newBindFixture(t)
	other := makeTestVault(t, filepath.Join(f.home, "other"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+other+"\"\n")

	snap := mustBuild(t)
	verdict, info, err := ClassifyProjectVault("not-bound", f.global, snap)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if verdict != VaultUnbound {
		t.Fatalf("verdict = %v, want VaultUnbound", verdict)
	}
	if info != "" {
		t.Fatalf("info = %q, want empty", info)
	}
}

// Case 4 (S2): a stale [project_vaults] binding, and a missing target, both
// resolve to VaultBindingBroken with a non-empty reason.
func TestProjectVault_BindingBroken_Stale(t *testing.T) {
	f := newBindFixture(t)
	// target is a real vault that does NOT hold the project; the default vault
	// records qa's departure -> staleProjectBinding refuses.
	target := makeTestVault(t, filepath.Join(f.home, "moved-target"))
	writeDepartureRecord(t, f.global, "qa")
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+target+"\"\n")

	snap := mustBuild(t)
	verdict, info, err := ClassifyProjectVault("qa", f.global, snap)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if verdict != VaultBindingBroken {
		t.Fatalf("verdict = %v, want VaultBindingBroken (stale binding)", verdict)
	}
	if info == "" {
		t.Fatal("info is empty; want the broken reason (D3)")
	}
}

func TestProjectVault_BindingBroken_MissingTarget(t *testing.T) {
	f := newBindFixture(t)
	missing := filepath.Join(f.home, "does-not-exist")
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+missing+"\"\n")

	snap := mustBuild(t)
	verdict, info, err := ClassifyProjectVault("qa", f.global, snap)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if verdict != VaultBindingBroken {
		t.Fatalf("verdict = %v, want VaultBindingBroken (missing target)", verdict)
	}
	if info == "" {
		t.Fatal("info is empty; want the broken reason")
	}
}

// Case 5 (M4): a relative target parses but boundVaultRoot rejects non-abs ->
// VaultBindingBroken.
func TestProjectVault_BindingBroken_RelativeTarget(t *testing.T) {
	f := newBindFixture(t)
	f.writeConfig(t, "\n[project_vaults]\nqa = \"relative/path\"\n")

	snap := mustBuild(t)
	verdict, info, err := ClassifyProjectVault("qa", f.global, snap)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if verdict != VaultBindingBroken {
		t.Fatalf("verdict = %v, want VaultBindingBroken (relative target)", verdict)
	}
	if info == "" {
		t.Fatal("info is empty; want the broken reason")
	}
}

// Case 6 (S3): a symlinked bound root in a SEPARATE parent forces the two
// cleaned paths to differ, so sameVaultRoot must use EvalSymlinks to find them
// equal -> VaultBoundHere. A naive == would read Elsewhere.
func TestProjectVault_Canonicalization_ForcesEvalSymlinks(t *testing.T) {
	f := newBindFixture(t)
	real := makeTestVault(t, filepath.Join(f.home, "real"))
	linkParent := filepath.Join(f.home, "links")
	if err := os.MkdirAll(linkParent, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(linkParent, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	// Precondition: the two cleaned strings DIFFER, so the test exercises the
	// EvalSymlinks branch rather than the lexical Clean branch.
	if filepath.Clean(real) == filepath.Clean(link) {
		t.Fatalf("cleaned paths are equal (%q); test would not exercise EvalSymlinks", real)
	}
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+link+"\"\n")

	snap := mustBuild(t)
	verdict, _, err := ClassifyProjectVault("qa", real, snap)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if verdict != VaultBoundHere {
		t.Fatalf("verdict = %v, want VaultBoundHere (symlink resolves to the same tree)", verdict)
	}
}

// Case 7 (S3 Clean branch): a trailing-slash spelling is equal after Clean, no
// EvalSymlinks needed -> VaultBoundHere.
func TestProjectVault_Canonicalization_CleanBranch(t *testing.T) {
	f := newBindFixture(t)
	real := makeTestVault(t, filepath.Join(f.home, "real"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+real+"/\"\n")

	snap := mustBuild(t)
	verdict, _, err := ClassifyProjectVault("qa", real, snap)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if verdict != VaultBoundHere {
		t.Fatalf("verdict = %v, want VaultBoundHere (trailing slash)", verdict)
	}
}

// Case 8 (S3 negative): a dangling (nonexistent) startupRoot vs a real bound
// root stays VaultBoundElsewhere (EvalSymlinks on the dangling side fails, and
// sameVaultRoot keeps the lexical verdict).
func TestProjectVault_Canonicalization_DanglingStartupRoot(t *testing.T) {
	f := newBindFixture(t)
	real := makeTestVault(t, filepath.Join(f.home, "real"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+real+"\"\n")
	dangling := filepath.Join(f.home, "nonexistent-startup")

	snap := mustBuild(t)
	verdict, _, err := ClassifyProjectVault("qa", dangling, snap)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if verdict != VaultBoundElsewhere {
		t.Fatalf("verdict = %v, want VaultBoundElsewhere (dangling startupRoot)", verdict)
	}
}

// Case 9 (S1): an empty startupRoot is a classify-level fault -> an error and
// VaultVerdictInvalid, NEVER VaultUnbound.
func TestProjectVault_EmptyStartupRoot_Errors(t *testing.T) {
	f := newBindFixture(t)
	here := makeTestVault(t, filepath.Join(f.home, "here"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+here+"\"\n")

	snap := mustBuild(t)
	verdict, _, err := ClassifyProjectVault("qa", "", snap)
	if err == nil {
		t.Fatal("empty startupRoot: want an error, got nil")
	}
	if verdict == VaultUnbound {
		t.Fatal("empty startupRoot classified as VaultUnbound (would read as allow); want VaultVerdictInvalid")
	}
	if verdict != VaultVerdictInvalid {
		t.Fatalf("verdict = %v, want VaultVerdictInvalid", verdict)
	}
}

// Case 10 (M3): a host with no bindings -> a BUILT, empty, allow-all snapshot.
// Covers both an absent config and a present config with no [project_vaults].
func TestProjectVault_NoBindings_BuiltEmptyAllowAll(t *testing.T) {
	t.Run("present_config_no_table", func(t *testing.T) {
		f := newBindFixture(t) // default config: vault_path only, no table
		snap, err := BuildProjectVaultSnapshot(context.Background())
		if err != nil {
			t.Fatalf("build: unexpected error: %v", err)
		}
		if !snap.built {
			t.Fatal("built==false; want a built empty snapshot")
		}
		verdict, _, err := ClassifyProjectVault("qa", f.global, snap)
		if err != nil {
			t.Fatalf("classify: %v", err)
		}
		if verdict != VaultUnbound {
			t.Fatalf("verdict = %v, want VaultUnbound", verdict)
		}
	})

	t.Run("absent_config", func(t *testing.T) {
		f := newBindFixture(t)
		if err := os.Remove(f.cfg); err != nil {
			t.Fatal(err)
		}
		snap, err := BuildProjectVaultSnapshot(context.Background())
		if err != nil {
			t.Fatalf("build on absent config: unexpected error: %v", err)
		}
		if !snap.built {
			t.Fatal("built==false; want a built empty snapshot on an absent config")
		}
		verdict, _, err := ClassifyProjectVault("qa", filepath.Join(f.home, "any"), snap)
		if err != nil {
			t.Fatalf("classify: %v", err)
		}
		if verdict != VaultUnbound {
			t.Fatalf("verdict = %v, want VaultUnbound", verdict)
		}
	})
}

// Case 11: a malformed table (a non-string value) is a BUILD FAILURE -> an error
// wrapping ErrVaultBindingRejected and an UNBUILT snapshot; classify fails
// closed (VaultVerdictInvalid), never VaultUnbound.
func TestProjectVault_BuildFailure_MalformedTable_FailsClosed(t *testing.T) {
	f := newBindFixture(t)
	f.writeConfig(t, "\n[project_vaults]\nqa = 3\n")

	snap, err := BuildProjectVaultSnapshot(context.Background())
	if err == nil {
		t.Fatal("malformed table: want a build error, got nil")
	}
	if !errors.Is(err, ErrVaultBindingRejected) {
		t.Fatalf("build error does not wrap ErrVaultBindingRejected: %v", err)
	}
	if snap.built {
		t.Fatal("build failure returned built==true; must never be a usable snapshot")
	}
	verdict, _, cerr := ClassifyProjectVault("qa", f.global, snap)
	if cerr == nil {
		t.Fatal("classify on an unbuilt snapshot: want an error, got nil")
	}
	if verdict == VaultUnbound {
		t.Fatal("unbuilt snapshot classified as VaultUnbound (allow-all); want VaultVerdictInvalid (fail closed)")
	}
	if verdict != VaultVerdictInvalid {
		t.Fatalf("verdict = %v, want VaultVerdictInvalid", verdict)
	}
}

// Case 12 (D2): bindings present but the global vault root does not resolve is a
// BUILD FAILURE detected by the up-front ResolveGlobalVaultPath (which
// staleProjectBinding would otherwise swallow). Fails closed.
func TestProjectVault_BuildFailure_GlobalRootUnresolvable_FailsClosed(t *testing.T) {
	f := newBindFixture(t)
	real := makeTestVault(t, filepath.Join(f.home, "real"))
	// Overwrite the config with a [project_vaults] table but NO vault_path, so
	// readProjectVaults succeeds while ResolveGlobalVaultPath -> VaultRoot errors.
	body := "[project_vaults]\nqa = \"" + real + "\"\n"
	if err := os.WriteFile(f.cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	snap, err := BuildProjectVaultSnapshot(context.Background())
	if err == nil {
		t.Fatal("unresolvable global root with bindings present: want a build error, got nil")
	}
	if snap.built {
		t.Fatal("build failure returned built==true")
	}
	verdict, _, cerr := ClassifyProjectVault("qa", real, snap)
	if cerr == nil {
		t.Fatal("classify on an unbuilt snapshot: want an error")
	}
	if verdict != VaultVerdictInvalid {
		t.Fatalf("verdict = %v, want VaultVerdictInvalid", verdict)
	}
}

// Case 13 (D3): a zero snapEntry present in a built snapshot is a decision for
// NEITHER Elsewhere NOR Broken.
func TestProjectVault_ZeroSnapEntry_IsNoDecision(t *testing.T) {
	snap := ProjectVaultSnapshot{built: true, entries: map[string]snapEntry{"qa": {}}}
	verdict, _, _ := ClassifyProjectVault("qa", "/some/startup/root", snap)
	if verdict != VaultVerdictInvalid {
		t.Fatalf("zero snapEntry classified as %v; must be VaultVerdictInvalid (neither Here/Elsewhere nor Broken)", verdict)
	}
}

// Case 14 (M5): the seam is cwd-independent — building and classifying from two
// different working directories yields identical verdicts.
func TestProjectVault_CwdIndependent(t *testing.T) {
	f := newBindFixture(t)
	other := makeTestVault(t, filepath.Join(f.home, "other"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+other+"\"\n")

	dirA := t.TempDir()
	dirB := t.TempDir()

	t.Chdir(dirA)
	snapA := mustBuild(t)
	vA, _, errA := ClassifyProjectVault("qa", f.global, snapA)
	if errA != nil {
		t.Fatalf("classify in dirA: %v", errA)
	}

	t.Chdir(dirB)
	snapB := mustBuild(t)
	vB, _, errB := ClassifyProjectVault("qa", f.global, snapB)
	if errB != nil {
		t.Fatalf("classify in dirB: %v", errB)
	}

	if vA != vB {
		t.Fatalf("verdict differs by cwd: dirA=%v dirB=%v", vA, vB)
	}
	if vA != VaultBoundElsewhere {
		t.Fatalf("verdict = %v, want VaultBoundElsewhere", vA)
	}
}

// A pre-cancelled context aborts the build: an unbuilt snapshot and an error
// whose chain is context.Canceled; classifying on it fails closed.
func TestProjectVault_BuildFailure_CancelledContext_FailsClosed(t *testing.T) {
	f := newBindFixture(t)
	here := makeTestVault(t, filepath.Join(f.home, "here"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+here+"\"\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	snap, err := BuildProjectVaultSnapshot(ctx)
	if err == nil {
		t.Fatal("cancelled context: want a build error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("build error chain is not context.Canceled: %v", err)
	}
	if snap.built {
		t.Fatal("cancelled build returned built==true; must never be a usable snapshot")
	}
	verdict, _, cerr := ClassifyProjectVault("qa", here, snap)
	if cerr == nil {
		t.Fatal("classify on an unbuilt (cancelled) snapshot: want an error")
	}
	if verdict != VaultVerdictInvalid {
		t.Fatalf("verdict = %v, want VaultVerdictInvalid (fail closed)", verdict)
	}
}

// The context.Background() path is unaffected by the new signature: a normal
// build succeeds and classifies as before.
func TestProjectVault_BackgroundContext_Unaffected(t *testing.T) {
	f := newBindFixture(t)
	here := makeTestVault(t, filepath.Join(f.home, "here"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+here+"\"\n")

	snap, err := BuildProjectVaultSnapshot(context.Background())
	if err != nil {
		t.Fatalf("background build: unexpected error: %v", err)
	}
	if !snap.built {
		t.Fatal("background build returned built==false")
	}
	verdict, _, err := ClassifyProjectVault("qa", here, snap)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if verdict != VaultBoundHere {
		t.Fatalf("verdict = %v, want VaultBoundHere", verdict)
	}
}
