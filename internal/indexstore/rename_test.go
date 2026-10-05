// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// errFault is the injected crash the AdoptRenamedStore step hook returns.
var errFault = errors.New("injected fault")

// seedRenameStore gives slug a store (one chunk with the given wing), a ledger
// with a session record whose archive_path lives under Projects/<slug>/, an
// embed cache with a vector, and a host-local imports marker dir. It leaves
// Projects/<slug>/ present (the project still live).
func seedRenameStore(t *testing.T, v *storage.Vault, slug, wing string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(v.Root, "Projects", slug), 0o755); err != nil {
		t.Fatal(err)
	}
	tx := mustLock(t)(Lock(context.Background(), v, slug, NoTimeout))
	if err := tx.Append(NoteOwner("notes/n.md"), withDay([]OwnedChunk{ownedChunk("chunk of "+slug, wing, "general")}, "2026-05-01")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	dir, _ := v.IndexDir(slug)
	ledger := `{"kind":"session","session_id":"s1","source_sha256":"` + strings.Repeat("a", 64) +
		`","state":"live","archive_path":"` + v.Root + `/Projects/` + slug + `/transcripts/x.tar.zst"}` + "\n" +
		`{"kind":"baseline"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, ledgerFile), []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}
	cache, _ := v.EmbedCacheDir(slug)
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "x.vec"), []byte("vector"), 0o644); err != nil {
		t.Fatal(err)
	}
	imports, _ := v.ImportsDir(slug)
	if err := os.MkdirAll(imports, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imports, "imported-sessions.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := v.WriteRenamePending(slug, "new"); err != nil {
		t.Fatal(err)
	}
}

// trackRename simulates the on-disk effect of the tracked rename commit (done
// by storage.ApplyRename, increment 1): Projects/<from>/ gone, Projects/<to>/
// present. The host-local index store is untouched.
func trackRename(t *testing.T, v *storage.Vault, from, to string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(v.Root, "Projects", from)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(v.Root, "Projects", to), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readRel(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestAdoptRenamedStore_KeepsTheStore: the index store is moved old->new, with
// every chunk id/owner and every ledger byte identical except wing (old->new)
// and the archive_path segment; the embed cache is REMOVED (M0=rebuild); the
// imports dir is carried; and the rename-pending record is gone.
func TestAdoptRenamedStore_KeepsTheStore(t *testing.T) {
	v := newVault(t)
	seedRenameStore(t, v, "old", "old")
	oldDir, _ := v.IndexDir("old")
	beforeChunks := readRel(t, filepath.Join(oldDir, chunksFile))
	beforeLedger := readRel(t, filepath.Join(oldDir, ledgerFile))

	trackRename(t, v, "old", "new")
	res, err := AdoptRenamedProject(context.Background(), v, "old", "new")
	if err != nil {
		t.Fatalf("AdoptRenamedProject: %v", err)
	}
	if res.Outcome != RenameHostLocalDone || !res.IndexRenamed || !res.ImportsCarried {
		t.Fatalf("result = %+v, want done/renamed/imports-carried", res)
	}

	newDir, _ := v.IndexDir("new")
	if dirExists(oldDir) {
		t.Error("index/old/ still exists after the move")
	}
	if !dirExists(newDir) {
		t.Fatal("index/new/ does not exist after the move")
	}
	// Only wing changed in chunks.jsonl (ids are content hashes; content kept).
	afterChunks := readRel(t, filepath.Join(newDir, chunksFile))
	if want := strings.ReplaceAll(beforeChunks, `"wing":"old"`, `"wing":"new"`); afterChunks != want {
		t.Errorf("chunks changed beyond wing:\n got %q\nwant %q", afterChunks, want)
	}
	// Only the archive_path segment changed in the ledger.
	afterLedger := readRel(t, filepath.Join(newDir, ledgerFile))
	if want := strings.ReplaceAll(beforeLedger, "/Projects/old/", "/Projects/new/"); afterLedger != want {
		t.Errorf("ledger changed beyond archive_path:\n got %q\nwant %q", afterLedger, want)
	}
	// Embed cache removed (not carried).
	if oldCache, _ := v.EmbedCacheDir("old"); dirExists(oldCache) {
		t.Error("embed-cache/old/ was not removed (M0 = rebuild)")
	}
	if newCache, _ := v.EmbedCacheDir("new"); dirExists(newCache) {
		t.Error("embed-cache/new/ exists: the cache must rebuild, not be carried")
	}
	// Imports dir carried.
	if newImports, _ := v.ImportsDir("new"); !dirExists(newImports) {
		t.Error("imports/new/ missing after the carry")
	}
	if oldImports, _ := v.ImportsDir("old"); dirExists(oldImports) {
		t.Error("imports/old/ still present after the carry")
	}
	// Rename-pending record gone.
	if _, ok, _ := v.RenamePending("old"); ok {
		t.Error("the rename-pending record was not removed after the step")
	}
}

// TestAdoptRenamedStore_RefusesWhenNotClean: a store already at the target is an
// unmergeable collision; the step refuses and names vp index rebuild.
func TestAdoptRenamedStore_RefusesTwoStores(t *testing.T) {
	v := newVault(t)
	seedRenameStore(t, v, "old", "old")
	// A pre-existing store at the target.
	seedRenameStore(t, v, "new", "new")
	trackRename(t, v, "old", "new") // Projects/old gone, Projects/new present
	_, err := AdoptRenamedProject(context.Background(), v, "old", "new")
	if err == nil || !strings.Contains(err.Error(), "refusing to merge") {
		t.Fatalf("want a merge refusal, got %v", err)
	}
	if oldDir, _ := v.IndexDir("old"); !dirExists(oldDir) {
		t.Error("index/old/ was moved despite the refusal")
	}
}

// TestAdoptRenamedStore_RunLockBusy: a held lifecycle run lock makes the step
// report busy and change nothing.
func TestAdoptRenamedStore_RunLockBusy(t *testing.T) {
	v := newVault(t)
	seedRenameStore(t, v, "old", "old")
	trackRename(t, v, "old", "new")
	rl := mustTryRunLock(t)(TryRunLock(v, KindLifecycle, "someone-else"))
	defer rl.Release()
	res, err := AdoptRenamedProject(context.Background(), v, "old", "new")
	if err != nil {
		t.Fatalf("AdoptRenamedProject: %v", err)
	}
	if res.Outcome != RenameHostLocalBusy {
		t.Fatalf("outcome = %q, want busy", res.Outcome)
	}
	if oldDir, _ := v.IndexDir("old"); !dirExists(oldDir) {
		t.Error("index/old/ moved though the run lock was busy")
	}
}

// TestAdoptRenamedStore_NeverTwoCommitLocks: at no point are two index commit
// locks held at once (the commit lock is a leaf).
func TestAdoptRenamedStore_NeverTwoCommitLocks(t *testing.T) {
	v := newVault(t)
	seedRenameStore(t, v, "old", "old")
	trackRename(t, v, "old", "new")

	held := map[string]bool{}
	max := 0
	restore := ObserveCommitLocks(func(project string, ev CommitLockEvent) {
		switch ev {
		case CommitAcquired:
			held[project] = true
		case CommitReleased:
			delete(held, project)
		}
		if len(held) > max {
			max = len(held)
		}
	})
	defer restore()

	if _, err := AdoptRenamedProject(context.Background(), v, "old", "new"); err != nil {
		t.Fatalf("AdoptRenamedProject: %v", err)
	}
	if max > 1 {
		t.Errorf("held %d commit locks at once, want at most 1", max)
	}
}

// TestAdoptRenamedStore_CrashAtEachStepReRunConverges: a crash after each step
// of the under-lock core leaves a state from which a re-run converges to the
// same end state.
func TestAdoptRenamedStore_CrashAtEachStepReRunConverges(t *testing.T) {
	for _, step := range []string{"renamed", "wing", "archive", "cache"} {
		t.Run(step, func(t *testing.T) {
			v := newVault(t)
			seedRenameStore(t, v, "old", "old")
			trackRename(t, v, "old", "new")

			// Crash after `step`.
			adoptRenamedStoreHook = func(s string) error {
				if s == step {
					return errFault
				}
				return nil
			}
			_, err := AdoptRenamedProject(context.Background(), v, "old", "new")
			adoptRenamedStoreHook = nil
			if err == nil {
				t.Fatalf("injected crash at %q did not surface", step)
			}

			// Re-run to completion.
			res, err := AdoptRenamedProject(context.Background(), v, "old", "new")
			if err != nil {
				t.Fatalf("re-run after crash at %q: %v", step, err)
			}
			if res.Outcome != RenameHostLocalDone {
				t.Fatalf("re-run outcome = %q, want done", res.Outcome)
			}
			newDir, _ := v.IndexDir("new")
			if oldDir, _ := v.IndexDir("old"); dirExists(oldDir) || !dirExists(newDir) {
				t.Error("store not converged to index/new/ after the re-run")
			}
			if c := readRel(t, filepath.Join(newDir, chunksFile)); strings.Contains(c, `"wing":"old"`) {
				t.Error("a chunk still names wing old after the re-run")
			}
			if _, ok, _ := v.RenamePending("old"); ok {
				t.Error("rename-pending record not cleared after the re-run")
			}
		})
	}
}

// TestAdoptRenamedStore_UndoAfterRevert: after the tracked commit is reverted
// (old back, new gone), the undo moves the store back and relabels old again.
func TestAdoptRenamedStore_UndoAfterRevert(t *testing.T) {
	v := newVault(t)
	seedRenameStore(t, v, "old", "old")
	trackRename(t, v, "old", "new")
	if _, err := AdoptRenamedProject(context.Background(), v, "old", "new"); err != nil {
		t.Fatalf("forward: %v", err)
	}
	// Revert the tracked rename: old back, new gone.
	trackRename(t, v, "new", "old")
	// Re-write the pending record the forward step cleared (undo expects it, and
	// a real revert leaves the rename unfinished again).
	if err := v.WriteRenamePending("old", "new"); err != nil {
		t.Fatal(err)
	}
	res, err := UndoRenamedProject(context.Background(), v, "old", "new")
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if res.Outcome != RenameHostLocalDone {
		t.Fatalf("undo outcome = %q", res.Outcome)
	}
	oldDir, _ := v.IndexDir("old")
	newDir, _ := v.IndexDir("new")
	if !dirExists(oldDir) || dirExists(newDir) {
		t.Fatal("undo did not move the store back to index/old/")
	}
	if c := readRel(t, filepath.Join(oldDir, chunksFile)); strings.Contains(c, `"wing":"new"`) {
		t.Error("a chunk still names wing new after the undo")
	}
	if _, ok, _ := v.RenamePending("old"); ok {
		t.Error("rename-pending record not cleared by the undo")
	}
	// Second undo is a no-op: same end state, no error (SF2).
	before := treeOf(t, oldDir)
	res2, err := UndoRenamedProject(context.Background(), v, "old", "new")
	if err != nil {
		t.Fatalf("second undo: %v", err)
	}
	if res2.Outcome != RenameHostLocalDone {
		t.Fatalf("second undo outcome = %q, want done", res2.Outcome)
	}
	if after := treeOf(t, oldDir); !reflect.DeepEqual(before, after) {
		t.Error("a second undo changed index/old/")
	}
	if dirExists(newDir) {
		t.Error("a second undo re-created index/new/")
	}
}

// TestAdoptRenamedStore_UndoWithAbsentStore: if a sweep reaped index/<new>/ in
// the window (new gone from the vault, no record names it), the undo finds no
// store and treats it as handled — old re-ingests from archives.
func TestAdoptRenamedStore_UndoWithAbsentStore(t *testing.T) {
	v := newVault(t)
	seedRenameStore(t, v, "old", "old")
	trackRename(t, v, "old", "new")
	if _, err := AdoptRenamedProject(context.Background(), v, "old", "new"); err != nil {
		t.Fatalf("forward: %v", err)
	}
	trackRename(t, v, "new", "old") // revert
	if err := v.WriteRenamePending("old", "new"); err != nil {
		t.Fatal(err)
	}
	// A sweep in the window reaps index/new/ (new is gone, no record names it).
	ReapGoneProjects(context.Background(), v)
	if newDir, _ := v.IndexDir("new"); dirExists(newDir) {
		t.Fatal("precondition: index/new/ should have been reaped")
	}
	res, err := UndoRenamedProject(context.Background(), v, "old", "new")
	if err != nil {
		t.Fatalf("undo with an absent store: %v", err)
	}
	if res.Outcome != RenameHostLocalDone {
		t.Fatalf("undo outcome = %q", res.Outcome)
	}
	if oldDir, _ := v.IndexDir("old"); dirExists(oldDir) {
		t.Error("index/old/ exists though the store was reaped before the undo; old should re-ingest")
	}
	if _, ok, _ := v.RenamePending("old"); ok {
		t.Error("rename-pending record not cleared by the undo")
	}
}

// TestAdoptRenamedStore_SweepWindowKeepsTheStore: on the renaming host, between
// the tracked commit and the host-local step, the sweep keeps index/old/ because
// the rename-pending record is present.
func TestAdoptRenamedStore_SweepWindowKeepsTheStore(t *testing.T) {
	v := newVault(t)
	seedRenameStore(t, v, "old", "old") // writes the rename-pending record
	trackRename(t, v, "old", "new")     // old gone from the vault, but record present
	ReapGoneProjects(context.Background(), v)
	if oldDir, _ := v.IndexDir("old"); !dirExists(oldDir) {
		t.Error("the sweep reaped index/old/ despite the rename-pending record")
	}
}

// TestAdoptRenamedStore_SecondHostReIngests: a host with NO rename-pending
// record (it only pulled the rename) reaps index/old/ like any departed store;
// it will re-ingest new from archives.
func TestAdoptRenamedStore_SecondHostReIngests(t *testing.T) {
	v := newVault(t)
	seedRenameStore(t, v, "old", "old")
	// This host did not run the rename: it has no record.
	if err := v.RemoveRenamePending("old"); err != nil {
		t.Fatal(err)
	}
	trackRename(t, v, "old", "new")
	ReapGoneProjects(context.Background(), v)
	if oldDir, _ := v.IndexDir("old"); dirExists(oldDir) {
		t.Error("a second host did not reap index/old/ (no record should mean reap)")
	}
}
