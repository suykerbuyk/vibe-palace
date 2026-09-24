// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import "testing"

// T5a: the fingerprint sidecar belongs to its vectors. Reaping an orphaned
// cache removes it too, or it alone would keep the directory from emptying and
// the orphan would never be reaped.
func TestSweepEmbedCaches_ReapRemovesTheFingerprintSidecar(t *testing.T) {
	root := t.TempDir()
	v := &Vault{Root: root}
	keeper(t, root)
	sweepFile(t, root, "palace/.local/embed-cache/gone/a.vec", "AAAA")
	sweepFile(t, root, "palace/.local/embed-cache/gone/"+EmbedCacheFingerprintFile, "vp-embed behaviour=1 model=m max_seq_len=0\n")

	res := mustSweep(t, v)
	if res.Reaped != 1 || sweepExists(root, "palace/.local/embed-cache/gone") {
		t.Fatalf("res = %+v; an orphan holding a fingerprint sidecar must still be reaped", res)
	}
}

// T5b: a legacy vector merged into a fingerprinted directory is of unknown
// regime, so the merge drops the directory's sidecar and the embed cache
// re-validates it. A merge that merges nothing leaves the sidecar alone.
func TestSweepEmbedCaches_LegacyMergeDropsTheTargetFingerprint(t *testing.T) {
	root := t.TempDir()
	v := &Vault{Root: root}
	sweepFile(t, root, "Projects/p/sessions/n.md", "x")
	sweepFile(t, root, "palace/p/.local/embed-cache/only-legacy.vec", "LEG!")
	sweepFile(t, root, "palace/.local/embed-cache/p/new.vec", "NEW!")
	sweepFile(t, root, "palace/.local/embed-cache/p/"+EmbedCacheFingerprintFile, "vp-embed behaviour=1 model=m max_seq_len=0\n")

	res := mustSweep(t, v)
	if res.Merged != 1 {
		t.Fatalf("res = %+v, want one merged legacy vector", res)
	}
	if sweepExists(root, "palace/.local/embed-cache/p/"+EmbedCacheFingerprintFile) {
		t.Error("a merge that brought in a legacy vector must drop the target's fingerprint sidecar")
	}

	root2 := t.TempDir()
	v2 := &Vault{Root: root2}
	sweepFile(t, root2, "Projects/p/sessions/n.md", "x")
	sweepFile(t, root2, "palace/p/.local/embed-cache/same.vec", "OLD!")
	sweepFile(t, root2, "palace/.local/embed-cache/p/same.vec", "NEW!")
	sweepFile(t, root2, "palace/.local/embed-cache/p/"+EmbedCacheFingerprintFile, "fp\n")
	if res := mustSweep(t, v2); res.Merged != 0 || res.Dropped != 1 {
		t.Fatalf("res = %+v, want the legacy copy dropped, nothing merged", res)
	}
	if !sweepExists(root2, "palace/.local/embed-cache/p/"+EmbedCacheFingerprintFile) {
		t.Error("a merge that merged nothing must leave the sidecar alone")
	}
}
