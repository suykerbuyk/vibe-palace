// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// otherRecipe differs from testRecipe in one input.
var otherRecipe = index.ChunkRecipe{IndexerVersion: index.IndexerVersion, Transcript: index.TranscriptRecipe{MaxChars: 500}}

func fingerprintPath(t *testing.T, v *storage.Vault, project string) string {
	t.Helper()
	dir, err := v.IndexDir(project)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, fingerprintFile)
}

func mustFingerprint(t *testing.T, v *storage.Vault, project string, r index.ChunkRecipe) FingerprintStatus {
	t.Helper()
	st, err := ReadFingerprint(v, project, r)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// A missing chunks.fingerprint is unbuilt, never a mismatch, and the first
// CommitArchive under UseRecipe records the recipe.
func TestMissingFingerprintIsUnbuilt(t *testing.T) {
	v := newVault(t)
	fakeArchives(t, "A")
	if st := mustFingerprint(t, v, "alpha", testRecipe); st != FingerprintMissing {
		t.Fatalf("no sidecar: %v, want missing", st)
	}
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(commitOf("s1", "A", "2026-05-01", "one"), noVectors{})
	})
	if st := mustFingerprint(t, v, "alpha", testRecipe); st != FingerprintMatch {
		t.Fatalf("after the first CommitArchive: %v, want match", st)
	}
	if st := mustFingerprint(t, v, "alpha", otherRecipe); st != FingerprintMismatch {
		t.Fatalf("against another recipe: %v, want mismatch", st)
	}
}

// Every chunk-writing step can be a project's first write, so each writes the
// sidecar: an archive-less Append, and a CommitBatch on another project. A
// commit that writes no chunk writes none.
func TestFirstChunkWriteWritesTheSidecar(t *testing.T) {
	v := newVault(t)
	fakeArchives(t)
	mustTx(t, v, func(tx *Tx) error {
		_, err := tx.EnsureLedger(nil)
		return err
	})
	if _, err := os.Stat(fingerprintPath(t, v, "alpha")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a commit that wrote no chunk wrote a sidecar: %v", err)
	}
	mustTx(t, v, func(tx *Tx) error {
		return tx.Append(NoteOwner("notes/n.md"), withDay([]OwnedChunk{ownedChunk("a note", "alpha", "general")}, "2026-05-01"))
	})
	if st := mustFingerprint(t, v, "alpha", testRecipe); st != FingerprintMatch {
		t.Fatalf("after an archive-less Append: %v, want match", st)
	}

	tx := mustLock(t)(Lock(context.Background(), v, "beta", NoTimeout))
	if _, err := tx.EnsureLedger(nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.CommitBatch(BatchCommit{BatchID: "b1", StartDay: "2026-05-01", Chunks: []OwnedChunk{ownedChunk("batch", "beta", "general")}}, noVectors{}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if st := mustFingerprint(t, v, "beta", testRecipe); st != FingerprintMatch {
		t.Fatalf("after a CommitBatch on another project: %v, want match", st)
	}
}

// Without a recipe the first chunk write refuses and writes nothing; a Tx that
// writes no chunks needs none.
func TestNoRecipeNoFirstWrite(t *testing.T) {
	v := newVault(t)
	tx, err := Lock(context.Background(), v, "alpha", NoTimeout) // no UseRecipe
	if err != nil {
		t.Fatal(err)
	}
	err = tx.Append(NoteOwner("notes/n.md"), withDay([]OwnedChunk{ownedChunk("a note", "alpha", "general")}, "2026-05-01"))
	if !errors.Is(err, ErrNoRecipe) {
		t.Fatalf("Append with no recipe: %v, want ErrNoRecipe", err)
	}
	if err := tx.Rewrite(nil); err != nil {
		t.Fatalf("Rewrite with no recipe: %v", err)
	}
	if _, err := tx.Reap(noOtherLive); err != nil {
		t.Fatalf("Reap with no recipe: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	dir, _ := v.IndexDir("alpha")
	for _, f := range []string{fingerprintFile, chunksFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s written by a refused first write: %v", f, err)
		}
	}
}

// A commit never rewrites an existing sidecar, matching or not; only a discard
// replaces it.
func TestACommitNeverRewritesAMismatch(t *testing.T) {
	v := newVault(t)
	fakeArchives(t, "A", "B")
	tx := mustLock(t)(Lock(context.Background(), v, "alpha", NoTimeout))
	tx.UseRecipe(otherRecipe)
	if _, err := tx.EnsureLedger(nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.CommitArchive(commitOf("s1", "A", "2026-05-01", "one"), noVectors{}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(fingerprintPath(t, v, "alpha"))
	if err != nil {
		t.Fatal(err)
	}
	mustTx(t, v, func(tx *Tx) error { // UseRecipe(testRecipe) via mustLock
		if err := tx.CommitArchive(commitOf("s2", "B", "2026-05-02", "two"), noVectors{}); err != nil {
			return err
		}
		return tx.Append(NoteOwner("notes/n.md"), withDay([]OwnedChunk{ownedChunk("a note", "alpha", "general")}, "2026-05-01"))
	})
	after, _ := os.ReadFile(fingerprintPath(t, v, "alpha"))
	if string(after) != string(before) {
		t.Fatal("a commit rewrote a mismatched sidecar")
	}
	if st := mustFingerprint(t, v, "alpha", testRecipe); st != FingerprintMismatch {
		t.Fatalf("state = %v, want mismatch", st)
	}
	mustTx(t, v, func(tx *Tx) error { return tx.Discard(DiscardChunks) })
	if st := mustFingerprint(t, v, "alpha", testRecipe); st != FingerprintMatch {
		t.Fatalf("after Discard(DiscardChunks) with UseRecipe: %v, want match", st)
	}
}

// Reading a mismatch changes nothing: chunks, ledger, local KG and the counter
// are as they were. Only the rebuild discards.
func TestMismatchNeverDiscardsOutsideARebuild(t *testing.T) {
	v := newVault(t)
	fakeArchives(t, "A")
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(commitOf("s1", "A", "2026-05-01", "one"), noVectors{})
	})
	dir, _ := v.IndexDir("alpha")
	read := func() (string, Gen) {
		var all string
		for _, f := range []string{chunksFile, ledgerFile, filepath.Join(kgDir, kgFile)} {
			b, _ := os.ReadFile(filepath.Join(dir, f))
			all += string(b) + "\x00"
		}
		g, err := ReadGeneration(v, "alpha")
		if err != nil {
			t.Fatal(err)
		}
		return all, g
	}
	files, gen := read()
	changed := testRecipe
	changed.Transcript.CustomRoomKeywords = map[string][]string{"r": {"k"}}
	if st := mustFingerprint(t, v, "alpha", changed); st != FingerprintMismatch {
		t.Fatalf("a changed custom keyword: %v, want mismatch", st)
	}
	if f2, g2 := read(); f2 != files || g2 != gen {
		t.Fatal("reading a mismatch changed the store or its counter")
	}
}

// Discard(DiscardChunks) writes the new sidecar LAST: a discard killed after any
// removal, or after the baseline, leaves the old sidecar, so the store reads
// mismatch and never match over a store that was not discarded.
func TestDiscardWritesTheFingerprintLast(t *testing.T) {
	for _, step := range []string{"discard-ledger", "discard-chunks", "discard-kg", "discard-graph", "discard-baseline"} {
		t.Run(step, func(t *testing.T) {
			v := newVault(t)
			fakeArchives(t, "A")
			tx := mustLock(t)(Lock(context.Background(), v, "alpha", NoTimeout))
			tx.UseRecipe(otherRecipe)
			if _, err := tx.EnsureLedger(nil); err != nil {
				t.Fatal(err)
			}
			if err := tx.CommitArchive(commitOf("s1", "A", "2026-05-01", "one"), noVectors{}); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			killAt(t, step)
			err := withTx(t, v, func(tx *Tx) error { return tx.Discard(DiscardChunks) })
			if !errors.Is(err, errKill) {
				t.Fatalf("discard killed at %s: %v", step, err)
			}
			if st := mustFingerprint(t, v, "alpha", testRecipe); st != FingerprintMismatch {
				t.Fatalf("killed at %s: %v, want mismatch", step, st)
			}
		})
	}
}

// A discard with no recipe refuses before it removes anything.
func TestDiscardWithoutARecipeRemovesNothing(t *testing.T) {
	v := newVault(t)
	fakeArchives(t, "A")
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(commitOf("s1", "A", "2026-05-01", "one"), noVectors{})
	})
	dir, _ := v.IndexDir("alpha")
	before, _ := os.ReadFile(filepath.Join(dir, ledgerFile))
	tx, err := Lock(context.Background(), v, "alpha", NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Discard(DiscardChunks); !errors.Is(err, ErrNoRecipe) {
		t.Fatalf("Discard with no recipe: %v, want ErrNoRecipe", err)
	}
	_ = tx.Release()
	if after, _ := os.ReadFile(filepath.Join(dir, ledgerFile)); string(after) != string(before) {
		t.Fatal("a refused discard removed the ledger")
	}
}

// The sidecar goes down BEFORE a step's chunk lines: CommitArchive killed after
// its vectors, or after its chunks, has already recorded the recipe, so no chunk
// line ever sits in a store whose recipe nothing records.
func TestCommitArchiveWritesTheSidecarBeforeItsChunks(t *testing.T) {
	for _, step := range []string{"vectors", "chunks"} {
		t.Run(step, func(t *testing.T) {
			v := newVault(t)
			fakeArchives(t, "A")
			mustTx(t, v, func(tx *Tx) error {
				_, err := tx.EnsureLedger(nil)
				return err
			})
			killAt(t, step)
			err := withTx(t, v, func(tx *Tx) error {
				return tx.CommitArchive(commitOf("s1", "A", "2026-05-01", "one"), noVectors{})
			})
			if !errors.Is(err, errKill) {
				t.Fatalf("killed at %s: %v", step, err)
			}
			if st := mustFingerprint(t, v, "alpha", testRecipe); st != FingerprintMatch {
				t.Fatalf("killed after %s: fingerprint %v, want match (written before the chunk lines)", step, st)
			}
		})
	}
}

// ReplaceOwned and Supersede can be a store's first chunk write too (after a
// lost sidecar, say), and each writes the sidecar.
func TestReplaceOwnedAndSupersedeWriteTheFirstSidecar(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		return tx.ReplaceOwned(NoteOwner("notes/n.md"), withDay([]OwnedChunk{ownedChunk("a note", "alpha", "general")}, "2026-05-01"))
	})
	if st := mustFingerprint(t, v, "alpha", testRecipe); st != FingerprintMatch {
		t.Fatalf("after a first ReplaceOwned: %v, want match", st)
	}

	fakeArchives(t, "A", "B")
	mustTxOn(t, v, "beta", func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(commitOf("s1", "A", "2026-05-01", "one"), noVectors{})
	})
	if err := os.Remove(fingerprintPath(t, v, "beta")); err != nil {
		t.Fatal(err)
	}
	mustTxOn(t, v, "beta", func(tx *Tx) error {
		return tx.Supersede(commitOf("s1", "B", "2026-05-02", "two"), noVectors{})
	})
	if st := mustFingerprint(t, v, "beta", testRecipe); st != FingerprintMatch {
		t.Fatalf("after a Supersede on a store with no sidecar: %v, want match", st)
	}
}
