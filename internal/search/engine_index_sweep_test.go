// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// sweepSeedStore commits one chunk to project's host-local index store while
// the project exists, so the store, its chunks.jsonl, its .generation counter
// and its commit lock file are all real.
func sweepSeedStore(t *testing.T, v *storage.Vault, project, content string) {
	t.Helper()
	tx, err := indexstore.Lock(context.Background(), v, project, indexstore.NoTimeout)
	if err != nil {
		t.Fatalf("Lock %s: %v", project, err)
	}
	tx.UseRecipe(index.ChunkRecipe{IndexerVersion: index.IndexerVersion})
	chunk := indexstore.OwnedChunk{Chunk: indexstore.Chunk{ID: index.ChunkID(content), Content: content}, Ownership: indexstore.Ownership{Day: "2026-05-13"}}
	if err := tx.Append(indexstore.NoteOwner("notes/"+project+".md"), []indexstore.OwnedChunk{chunk}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func sweepWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sweepExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return false
}

// The engine's reap runs the index sweep: a Rebuild of a live project removes
// the index store of every project gone from the vault (and the tombstone the
// same pass made), changes the gone project's epoch, and leaves the lock files,
// the live store and the store of a slug whose palace/ tree still holds a
// regular file.
func TestEngineReapSweepsGoneProjectsIndexStores(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	root := v.Root

	// live: a real project with a drawer and an index store.
	sweepWrite(t, root, "Projects/live/resume.md", "# live\n")
	addDrawer(t, v, "live", "wing", "room", "a live drawer", "facts")
	sweepSeedStore(t, v, "live", "a live chunk")

	// gone: had a store while it existed, then left the vault entirely.
	sweepWrite(t, root, "Projects/gone/resume.md", "# gone\n")
	sweepSeedStore(t, v, "gone", "a gone chunk")
	if err := os.RemoveAll(filepath.Join(root, "Projects", "gone")); err != nil {
		t.Fatal(err)
	}

	// resid: palace/resid/ holds only .local/, which is not a store.
	sweepWrite(t, root, "palace/resid/.local/x", "x\n")
	sweepWrite(t, root, "palace/.local/index/resid/chunks.jsonl", "{}\n")

	// kept: palace/kept/ holds an untracked regular file, so kept exists.
	sweepWrite(t, root, "palace/kept/ingested-archives.jsonl", "{}\n")
	sweepWrite(t, root, "palace/.local/index/kept/chunks.jsonl", "{}\n")

	idxRoot := v.IndexRootDir()
	goneDir := filepath.Join(idxRoot, "gone")
	if !sweepExists(t, filepath.Join(goneDir, "chunks.jsonl")) {
		t.Fatal("fixture: index/gone/chunks.jsonl was not written")
	}
	goneBefore, err := indexstore.ReadGeneration(v, "gone")
	if err != nil {
		t.Fatal(err)
	}
	if goneBefore == (indexstore.Gen{}) {
		t.Fatal("fixture: .generation/gone should exist before the sweep")
	}
	residBefore, err := indexstore.ReadGeneration(v, "resid")
	if err != nil {
		t.Fatal(err)
	}
	if residBefore != (indexstore.Gen{}) {
		t.Fatalf("fixture: .generation/resid should be absent, got %+v", residBefore)
	}
	liveChunks := filepath.Join(idxRoot, "live", "chunks.jsonl")
	liveBody, err := os.ReadFile(liveChunks)
	if err != nil {
		t.Fatal(err)
	}
	keptBody := "{}\n"
	locksBefore, err := os.ReadDir(v.IndexLocksDir())
	if err != nil {
		t.Fatal(err)
	}
	var lockNames []string
	for _, e := range locksBefore {
		lockNames = append(lockNames, e.Name())
	}
	goneLock, _ := v.IndexCommitLockPath("gone")
	if !sweepExists(t, goneLock) {
		t.Fatalf("fixture: %s should exist (locks: %v)", goneLock, lockNames)
	}

	if _, err := eng.Rebuild(ctx, "live"); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	for _, s := range []string{"gone", "resid"} {
		if sweepExists(t, filepath.Join(idxRoot, s)) {
			t.Errorf("index/%s/ survived the engine's index sweep", s)
		}
	}
	entries, err := os.ReadDir(idxRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), storage.IndexTombstonePrefix) {
			t.Errorf("tombstone %s left behind; the same pass must delete it", e.Name())
		}
	}
	goneAfter, err := indexstore.ReadGeneration(v, "gone")
	if err != nil {
		t.Fatal(err)
	}
	if goneAfter.Epoch == goneBefore.Epoch {
		t.Errorf(".generation/gone epoch unchanged (%x); the removal must change it", goneAfter.Epoch)
	}
	genGone, _ := v.IndexGenerationPath("gone")
	if !sweepExists(t, genGone) {
		t.Error(".generation/gone must stay after the removal")
	}
	residAfter, err := indexstore.ReadGeneration(v, "resid")
	if err != nil {
		t.Fatal(err)
	}
	if residAfter.Epoch == 0 {
		t.Errorf(".generation/resid = %+v after the removal, want a new epoch", residAfter)
	}
	for _, n := range lockNames {
		if !sweepExists(t, filepath.Join(v.IndexLocksDir(), n)) {
			t.Errorf("lock file %s removed by the sweep", n)
		}
	}
	if got, err := os.ReadFile(liveChunks); err != nil || string(got) != string(liveBody) {
		t.Errorf("index/live/chunks.jsonl changed: err %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(idxRoot, "kept", "chunks.jsonl")); err != nil || string(got) != keptBody {
		t.Errorf("index/kept/ must be kept (palace/kept/ holds a regular file): %q, %v", got, err)
	}
}
