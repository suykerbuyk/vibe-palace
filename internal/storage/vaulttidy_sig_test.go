// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"strings"
	"testing"
)

// Task signing-policy-for-capture-paths-and-signed-project-rename, Stage 1, part A:
// a manifest's detached signature is swept WITH its manifest.

const (
	sigManifest = "Projects/vibe-palace/transcripts/abc.manifest.json"
	sigPath     = sigManifest + ".sig"
)

// A-T1: a worktree-present .sig whose manifest is on disk is swept. The sibling
// is seeded LOCALLY here (never in a shared helper), so an orphan-sweeping
// mutation cannot hide behind a pre-seeded table (A-2).
func TestClassifyDirty_ManifestSigSweptWithManifest(t *testing.T) {
	for _, status := range []string{"??", " M", "A ", "MM"} {
		t.Run(strings.ReplaceAll(status, " ", "_"), func(t *testing.T) {
			vaultPath := t.TempDir()
			writeFile(t, vaultPath, sigManifest, "{}\n")
			swept, reported, deferred := classifyDirty(vaultPath, []PorcelainEntry{{Status: status, Path: sigPath}}, false)
			if !hasPath(swept, sigPath) || hasPath(reported, sigPath) || len(deferred) != 0 {
				t.Errorf("status %q: want %q swept, got swept=%v reported=%v deferred=%v", status, sigPath, swept, reported, deferred)
			}
		})
	}
}

// A-T2: an on-disk orphan .sig (no manifest seeded) is Reported, and so are the
// nested and non-transcript shapes, even with a manifest beside them.
func TestClassifyDirty_OrphanOrMisplacedSigReported(t *testing.T) {
	t.Run("orphan_on_disk", func(t *testing.T) {
		vaultPath := t.TempDir()
		writeFile(t, vaultPath, sigPath, "sig\n") // present, manifest absent
		for _, status := range []string{"??", " M"} {
			swept, reported, deferred := classifyDirty(vaultPath, []PorcelainEntry{{Status: status, Path: sigPath}}, false)
			if !hasPath(reported, sigPath) || hasPath(swept, sigPath) || len(deferred) != 0 {
				t.Errorf("status %q: orphan .sig must be reported only, got swept=%v reported=%v deferred=%v", status, swept, reported, deferred)
			}
		}
	})
	for _, p := range []string{
		"Projects/vibe-palace/transcripts/sub/abc.manifest.json.sig",
		"Projects/vibe-palace/notes/abc.manifest.json.sig",
		"palace/vibe-palace/transcripts/abc.manifest.json.sig",
		"Projects/vibe-palace/transcripts/abc.sig",
	} {
		t.Run(p, func(t *testing.T) {
			vaultPath := t.TempDir()
			base := strings.TrimSuffix(p, ".sig")
			writeFile(t, vaultPath, base, "{}\n")
			swept, reported, _ := classifyDirty(vaultPath, []PorcelainEntry{{Status: "??", Path: p}}, false)
			if !hasPath(reported, p) || hasPath(swept, p) {
				t.Errorf("%q must stay reported, got swept=%v reported=%v", p, swept, reported)
			}
		})
	}
}

// A-T4: the same sweep on a migrated vault.
func TestClassifyDirty_ManifestSigSweptOnMigratedVault(t *testing.T) {
	vaultPath := t.TempDir()
	writeFile(t, vaultPath, sigManifest, "{}\n")
	swept, reported, _ := classifyDirty(vaultPath, []PorcelainEntry{{Status: "??", Path: sigPath}}, true)
	if !hasPath(swept, sigPath) || hasPath(reported, sigPath) {
		t.Errorf("migrated vault: want %q swept, got swept=%v reported=%v", sigPath, swept, reported)
	}
}

// A-T6 (A-1): a DELETION of a tracked .sig is swept with no stat: the re-sign
// window (manifest on disk, .sig removed) and the pair-deletion (neither on
// disk) both sweep. A sibling check on the deletion path would Report the pair.
func TestClassifyDirty_ManifestSigDeletionSwept(t *testing.T) {
	deletions := []string{" D", "D ", "MD", "AD", "DD"}
	t.Run("pair_deletion_neither_on_disk", func(t *testing.T) {
		vaultPath := t.TempDir() // nothing on disk
		for _, status := range deletions {
			swept, reported, deferred := classifyDirty(vaultPath, []PorcelainEntry{
				{Status: status, Path: sigManifest},
				{Status: status, Path: sigPath},
			}, false)
			if !hasPath(swept, sigPath) || !hasPath(swept, sigManifest) || len(reported) != 0 || len(deferred) != 0 {
				t.Errorf("status %q: pair-deletion must sweep both, got swept=%v reported=%v deferred=%v", status, swept, reported, deferred)
			}
		}
	})
	t.Run("resign_window_manifest_on_disk", func(t *testing.T) {
		vaultPath := t.TempDir()
		writeFile(t, vaultPath, sigManifest, "{}\n")
		swept, reported, _ := classifyDirty(vaultPath, []PorcelainEntry{
			{Status: " M", Path: sigManifest},
			{Status: " D", Path: sigPath},
		}, false)
		if !hasPath(swept, sigPath) || !hasPath(swept, sigManifest) || len(reported) != 0 {
			t.Errorf("re-sign window must sweep both, got swept=%v reported=%v", swept, reported)
		}
	})
	t.Run("both_modified_together", func(t *testing.T) {
		vaultPath := t.TempDir()
		writeFile(t, vaultPath, sigManifest, "{}\n")
		writeFile(t, vaultPath, sigPath, "sig\n")
		swept, reported, _ := classifyDirty(vaultPath, []PorcelainEntry{
			{Status: " M", Path: sigManifest},
			{Status: " M", Path: sigPath},
		}, false)
		if !hasPath(swept, sigPath) || !hasPath(swept, sigManifest) || len(reported) != 0 {
			t.Errorf("a re-signed pair must sweep both, got swept=%v reported=%v", swept, reported)
		}
	})
}

// F-1: an unmerged modify/delete conflict ("DU"/"UD") is not a plain deletion.
// An on-disk .sig with its manifest absent stays Reported, not swept.
func TestClassifyDirty_ManifestSigUnmergedDeleteOrphanReported(t *testing.T) {
	for _, status := range []string{"DU", "UD"} {
		t.Run(status, func(t *testing.T) {
			vaultPath := t.TempDir()
			writeFile(t, vaultPath, sigPath, "sig\n") // present, manifest absent
			swept, reported, deferred := classifyDirty(vaultPath, []PorcelainEntry{{Status: status, Path: sigPath}}, false)
			if !hasPath(reported, sigPath) || hasPath(swept, sigPath) || len(deferred) != 0 {
				t.Errorf("status %q: orphan .sig must be reported only, got swept=%v reported=%v deferred=%v", status, swept, reported, deferred)
			}
		})
	}
}

// A-T3: end to end against real git. A new manifest and its .sig land in ONE
// tidy commit and leave no genuine dirt, so SyncVault does not refuse; a
// tracked-pair deletion also leaves none.
func TestTidyAndSync_ManifestSigTravelsWithManifest(t *testing.T) {
	dir, bare := syncSeedRemote(t)
	writeFile(t, dir, sigManifest, "{\"project_slug\":\"vibe-palace\"}\n")
	writeFile(t, dir, sigPath, "-----BEGIN SSH SIGNATURE-----\nfake\n-----END SSH SIGNATURE-----\n")

	res, err := SyncVault(dir, []string{"origin"})
	if err != nil {
		t.Fatalf("SyncVault: %v", err)
	}
	if res.Refused || len(res.GenuineDirt) != 0 {
		t.Fatalf("a new .sig must not block sync: refused=%v dirt=%v", res.Refused, res.GenuineDirt)
	}
	if !res.Committed {
		t.Fatal("expected the pair to be committed")
	}
	files := gitRun(t, dir, "show", "--name-only", "--pretty=format:", "HEAD")
	if !strings.Contains(files, sigManifest+"\n") || !strings.Contains(files, sigPath) {
		t.Errorf("manifest and .sig must be in ONE commit, HEAD touches: %q", files)
	}
	if tree := gitRun(t, bare, "ls-tree", "-r", "--name-only", "main"); !strings.Contains(tree, sigPath) {
		t.Errorf(".sig did not reach the remote: %q", tree)
	}

	// Pair deletion: nothing is left as genuine dirt.
	gitRun(t, dir, "rm", "-q", "-f", "--", sigManifest, sigPath) // staged "D " on both, neither on disk
	scan, err := TidyScan(dir)
	if err != nil {
		t.Fatalf("TidyScan: %v", err)
	}
	if hasPath(scan.Reported, sigPath) || hasPath(scan.Reported, sigManifest) {
		t.Errorf("pair deletion must not be reported, reported=%v", scan.Reported)
	}
}

// A-T5: the signature backups never reach porcelain on a vault carrying the
// canonical .gitignore (archive.Create names them <tag>.sig.bak).
func TestManifestSigBackupsAreGitignored(t *testing.T) {
	dir := initTestRepo(t)
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatalf("ReconcileVaultGitignore: %v", err)
	}
	const t1 = "Projects/vibe-palace/transcripts/abc.manifest.json.0123456789ab.sig.bak"
	const t2 = "Projects/vibe-palace/transcripts/abc.manifest.json.0123456789ab.bak"
	writeFile(t, dir, t1, "sig\n")
	writeFile(t, dir, t2, "{}\n")
	if status := gitRun(t, dir, "status", "--porcelain", "-uall", "--", "Projects"); strings.TrimSpace(status) != "" {
		t.Errorf("backups must be gitignored, status: %q", status)
	}
}
