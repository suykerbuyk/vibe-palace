// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
)

// TestVaultSplitPurge_RefusesAFileWrittenAfterTheBind pins the one window the
// digest bind cannot close: a file that reaches a purged slug tree AFTER purge
// re-derived the manifest. That file is in no manifest row, so it was never
// copied. Purge must refuse, name it, and delete nothing at all, in any tree.
//
// The seam stands in for a capture hook, a task amend or a memory write landing
// while the purge runs. Both slug trees are exercised, because the walk visits
// palace/<slug> and Projects/<slug> separately.
func TestVaultSplitPurge_RefusesAFileWrittenAfterTheBind(t *testing.T) {
	for _, late := range []string{
		"Projects/alpha/sessions/2026-09-23-late-01.md",
		"palace/alpha/drawers/alpha/general/late.jsonl",
	} {
		t.Run(late, func(t *testing.T) {
			root := splitFixtureVault(t, "alpha")
			writeSplitFile(t, root, "palace/.local/embed-cache/alpha/d1.vec", "cache")
			dest := splitDest(t)
			p := splitPlannedParams(t, root, dest, "alpha")
			p.Action = "apply"
			if _, err := callSplit(t, root, p); err != nil {
				t.Fatalf("apply: %v", err)
			}

			splitPurgeAfterVerify = func() { writeSplitFile(t, root, late, "written while purge ran\n") }
			t.Cleanup(func() { splitPurgeAfterVerify = nil })

			sourceBefore := snapshotTree(t, root)
			destBefore := snapshotTree(t, dest)

			p.Action = "purge"
			_, err := callSplit(t, root, p)
			if err == nil {
				_, serr := os.Stat(filepath.Join(root, filepath.FromSlash(late)))
				_, derr := os.Stat(filepath.Join(dest, filepath.FromSlash(late)))
				t.Fatalf("purge must refuse a file that is in no manifest row, and it succeeded "+
					"(late file in source: %v; in destination: %v)", serr == nil, derr == nil)
			}
			if !apperr.IsCaller(err) {
				t.Errorf("the refusal is the operator's to act on and must be a caller error, got %T: %v", err, err)
			}
			for _, want := range []string{"not in the manifest", late, "Nothing was deleted", "fresh destination"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal must contain %q, got: %v", want, err)
				}
			}

			if _, serr := os.Stat(filepath.Join(root, filepath.FromSlash(late))); serr != nil {
				t.Errorf("the uncopied file must survive the refusal: %v", serr)
			}
			// Nothing deleted anywhere: every file present before purge (the
			// embed cache included, which purge removes first) is still there.
			after := snapshotTree(t, root)
			for rel, sum := range sourceBefore {
				if after[rel] != sum {
					t.Errorf("source file %s was removed or changed by a refused purge", rel)
				}
			}
			if got := snapshotTree(t, dest); !equalStringMaps(destBefore, got) {
				t.Error("a refused purge must not touch the destination")
			}
		})
	}
}

// TestVaultSplitPurge_SubtractSetFilesNeedNoHash is the false-positive guard
// for the refusal above: every class the subtract set removes from the manifest
// is legitimately unhashed, and purge must still remove it. It is expected green
// with and without the unmanifested-file refusal; it goes red only if that
// refusal ever catches a file plan deliberately left out.
func TestVaultSplitPurge_SubtractSetFilesNeedNoHash(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	subtracted := []string{
		"palace/.local/embed-cache/alpha/d1.vec",
		"palace/alpha/.local/imported-sessions.jsonl",
		"palace/alpha/.surface",
		"Projects/alpha/.surface",
		"Projects/alpha/commit-log.anchor",
		"Projects/alpha/config.toml",
		"Projects/alpha/sessions/.local/scratch.txt",
	}
	for _, rel := range subtracted {
		writeSplitFile(t, root, rel, "machine-local\n")
	}
	dest := splitDest(t)
	p := splitPlannedParams(t, root, dest, "alpha")
	p.Action = "apply"
	if _, err := callSplit(t, root, p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	p.Action = "purge"
	if _, err := callSplit(t, root, p); err != nil {
		t.Fatalf("purge must remove subtract-set files without a hash, got: %v", err)
	}
	for _, rel := range subtracted {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s must be gone after purge (stat err: %v)", rel, err)
		}
	}
}
