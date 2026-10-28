// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// This file realizes the planned Tests-table rows of
// palace-navigation-over-the-host-local-chunk-store that the child's own suite
// left unwritten (task palace-nav-test-matrix-gaps): R2, C1(c), the Relabel
// lock/reload spies, C3, and R1. The Empty-host row is realized in
// internal/tools (the palace tools live there).
//
// Fixtures seedStoreChunk, markMigrated, newProjectVault and readGen are in
// store_reader_test.go; MoveCandidate, Relabel, QueryDrawers and the glideDedup
// seam are the package's own.

// appendTracked files a tracked drawer (the pre-marker corpus) under wing/room.
func appendTracked(t *testing.T, v *storage.Vault, project, wing, room, content string) {
	t.Helper()
	if err := v.AppendDrawer(project, wing, room, storage.Drawer{
		Content: content, Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("AppendDrawer %s/%s: %v", wing, room, err)
	}
}

// ownedChunkOf builds one OwnedChunk the way the store's own fixtures do.
func ownedChunkOf(content, wing, room, hall, sourceType string) indexstore.OwnedChunk {
	return indexstore.OwnedChunk{
		Chunk:     indexstore.Chunk{ID: index.ChunkID(content), Content: content, Wing: wing, Room: room, Hall: hall},
		Ownership: indexstore.Ownership{SourceType: sourceType},
	}
}

// readGenEpoch reads the project's full store change counter (Gen and Epoch).
func readGenEpoch(t *testing.T, v *storage.Vault, project string) indexstore.Gen {
	t.Helper()
	g, err := indexstore.ReadGeneration(v, project)
	if err != nil {
		t.Fatalf("ReadGeneration: %v", err)
	}
	return g
}

// contentCounts tallies how many returned hits carry each content.
func contentCounts(hits []storage.DrawerHit) map[string]int {
	m := map[string]int{}
	for _, h := range hits {
		m[h.Content]++
	}
	return m
}

// --- R2: unledgered visibility through the palace reader --------------------

// The navigation reader shows a ledgered archive's chunks, a note's decision
// chunk and a ledgered import batch's chunk, and NONE of an unledgered
// (failure-only) archive's — the liveness fold enforces it, re-asserted here
// through the palace reader, not only the store child's fold.
func TestR2_UnledgeredArchiveInvisibleThroughReader(t *testing.T) {
	v, p := newProjectVault(t)
	markMigrated(t, v.Root) // store-only view: no tracked fallback.

	const (
		cA = "ledgered archive A chunk about the api handler"
		cD = "the team decided to adopt the host-local chunk store"
		cM = "mempalace import batch chunk about deployment"
		cB = "unledgered archive B chunk that must never show"
	)

	ix, err := ProjectIndexing(v, p)
	if err != nil {
		t.Fatalf("ProjectIndexing: %v", err)
	}
	tx, err := indexstore.Lock(context.Background(), v, p, indexstore.NoTimeout)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	tx.UseRecipe(ix.Recipe)
	if _, err := tx.EnsureLedger(nil); err != nil {
		_ = tx.Release()
		t.Fatalf("EnsureLedger: %v", err)
	}
	// Archive A, ledgered live.
	archiveA := indexstore.ArchiveCommit{
		SessionID: "SA", SHA: "shaA", StartDay: "2026-05-02", StartDaySource: indexstore.DayFromTranscript,
		Chunks: []indexstore.OwnedChunk{ownedChunkOf(cA, "wa", "api", "facts", "transcript")},
	}
	if err := tx.CommitArchive(archiveA, noVecs{}); err != nil {
		_ = tx.Release()
		t.Fatalf("CommitArchive A: %v", err)
	}
	// A ledgered import batch owning the mempalace chunk.
	batch := indexstore.BatchCommit{
		BatchID: "batch-1", StartDay: "2026-05-03",
		Chunks: []indexstore.OwnedChunk{ownedChunkOf(cM, "wm", "general", "facts", "mempalace")},
	}
	if err := tx.CommitBatch(batch, noVecs{}); err != nil {
		_ = tx.Release()
		t.Fatalf("CommitBatch: %v", err)
	}
	// A note-owned decision chunk (a note owner is always live).
	dec := ownedChunkOf(cD, "wd", "decisions", "decisions", "decision")
	dec.Day = "2026-05-04"
	if err := tx.Append(indexstore.NoteOwner("notes/decision.md"), []indexstore.OwnedChunk{dec}); err != nil {
		_ = tx.Release()
		t.Fatalf("Append decision: %v", err)
	}
	// Archive B: its chunk is written and owned by the archive, but the archive
	// is NEVER ledgered — only a failure count is recorded. Its owner is not
	// live, so the fold drops the chunk.
	if err := tx.Append(indexstore.ArchiveOwner("shaB"), []indexstore.OwnedChunk{ownedChunkOf(cB, "wb", "general", "facts", "transcript")}); err != nil {
		_ = tx.Release()
		t.Fatalf("Append archive B chunk: %v", err)
	}
	if err := tx.RecordFailure("SB", "shaB", errFixture); err != nil {
		_ = tx.Release()
		t.Fatalf("RecordFailure B: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	hits, err := QueryDrawers(v, storage.DrawerQuery{Project: p})
	if err != nil {
		t.Fatalf("QueryDrawers: %v", err)
	}
	got := contentCounts(hits)
	for _, want := range []string{cA, cD, cM} {
		if got[want] != 1 {
			t.Errorf("content %q returned %d times, want 1 (ledgered/decision/batch must be visible)", want, got[want])
		}
	}
	if got[cB] != 0 {
		t.Errorf("unledgered failure-only archive B chunk was returned %d times, want 0", got[cB])
	}
}

// --- C1(c): forced-equal 32-bit drawer ids ----------------------------------

// Two DIFFERENT tracked-drawer contents forced to share one 32-bit
// storage.DrawerID are BOTH returned, while a tracked copy of a STORE chunk's
// content is deduped — proving the glide dedup keys on index.ChunkID(content),
// never the 32-bit id.
func TestC1c_ForcedDrawerIDCollisionKeepsBoth(t *testing.T) {
	v, p := newProjectVault(t) // pre-marker: tracked fallback active.

	const (
		inStore = "content that is both a store chunk and a tracked copy"
		y1      = "distinct tracked content ONE"
		y2      = "distinct tracked content TWO"
	)
	seedStoreChunk(t, v, p, "alpha", "api", inStore, "transcript")
	appendTracked(t, v, p, "beta", "general", inStore) // same content → must dedup (ChunkID)
	appendTracked(t, v, p, "gamma", "general", y1)     // distinct → must survive
	appendTracked(t, v, p, "delta", "general", y2)     // distinct → must survive

	// Force every tracked drawer onto ONE 32-bit id. ChunkID is untouched.
	orig := drawerHitSeam
	drawerHitSeam = func(h storage.DrawerHit) storage.DrawerHit { h.ID = "c0ffee00"; return h }
	defer func() { drawerHitSeam = orig }()

	hits, err := QueryDrawers(v, storage.DrawerQuery{Project: p})
	if err != nil {
		t.Fatalf("QueryDrawers: %v", err)
	}
	got := contentCounts(hits)
	if got[inStore] != 1 {
		t.Errorf("store content returned %d times, want 1 (the tracked copy must dedup by ChunkID)", got[inStore])
	}
	if got[y1] != 1 || got[y2] != 1 {
		t.Errorf("distinct colliding-id contents returned y1=%d y2=%d, want 1 each (no dedup on the 32-bit id)", got[y1], got[y2])
	}
	// The two distinct survivors really do share the forced 32-bit id.
	var collided int
	for _, h := range hits {
		if (h.Content == y1 || h.Content == y2) && h.ID == "c0ffee00" {
			collided++
		}
	}
	if collided != 2 {
		t.Errorf("expected both distinct survivors to carry the forced id c0ffee00, got %d", collided)
	}
}

// --- Relabel spies: commit-lock once, run-lock never, full reload -----------

// One Relabel takes the index COMMIT lock exactly once (on a path under
// palace/.local/locks/), takes the index RUN lock ZERO times, and moves both
// Gen and Epoch so a second engine does a FULL reload and sees the new room.
func TestRelabelLockCountsAndFullReload(t *testing.T) {
	v, p := newProjectVault(t)
	markMigrated(t, v.Root)
	const content = "Set up the kubernetes cluster for deployment."
	id := index.ChunkID(content)
	seedStoreChunk(t, v, p, "proj", "api", content, "transcript")

	// A second engine, opened BEFORE the relabel: its snapshot sees room "api".
	gBefore := readGenEpoch(t, v, p)
	stBefore, err := indexstore.ReadStore(v, p)
	if err != nil {
		t.Fatalf("ReadStore before: %v", err)
	}
	if roomOf(stBefore, id) != "api" {
		t.Fatalf("pre-relabel room = %q, want api", roomOf(stBefore, id))
	}

	var commitAcquired, runEvents int
	var commitProjects []string
	restoreCommit := indexstore.ObserveCommitLocks(func(project string, ev indexstore.CommitLockEvent) {
		if ev == indexstore.CommitAcquired {
			commitAcquired++
			commitProjects = append(commitProjects, project)
		}
	})
	restoreRun := indexstore.ObserveRunLocks(func(indexstore.RunLockEvent) { runEvents++ })

	moves := []MoveCandidate{{DrawerID: id, Wing: "proj", FromRoom: "api", ToRoom: "devops"}}
	err = Relabel(context.Background(), v, p, moves, 5*time.Second)
	restoreCommit()
	restoreRun()
	if err != nil {
		t.Fatalf("Relabel: %v", err)
	}

	if commitAcquired != 1 {
		t.Errorf("index commit lock acquired %d times, want exactly 1 (projects=%v)", commitAcquired, commitProjects)
	}
	if len(commitProjects) == 1 && commitProjects[0] != p {
		t.Errorf("commit lock taken for project %q, want %q", commitProjects[0], p)
	}
	if runEvents != 0 {
		t.Errorf("index RUN lock touched %d times, want 0 (a relabel never takes the run lock)", runEvents)
	}

	// The commit lock lives under palace/.local/locks/.
	lockPath, err := v.IndexCommitLockPath(p)
	if err != nil {
		t.Fatalf("IndexCommitLockPath: %v", err)
	}
	wantSeg := filepath.ToSlash(filepath.Join("palace", ".local", "locks"))
	if !strings.Contains(filepath.ToSlash(lockPath), wantSeg) {
		t.Errorf("commit lock path %q is not under %q", lockPath, wantSeg)
	}

	// Full reload: both Gen and Epoch moved, the signal a running engine reloads
	// the project in full (not an append-only Gen bump).
	gAfter := readGenEpoch(t, v, p)
	if !(gAfter.Gen > gBefore.Gen) {
		t.Errorf("Gen did not advance: before=%d after=%d", gBefore.Gen, gAfter.Gen)
	}
	if gAfter.Epoch == gBefore.Epoch {
		t.Errorf("Epoch did not change (before=after=%d): a relabel must force a FULL reload, not an append", gBefore.Epoch)
	}

	// The second engine's NEXT read sees the moved room.
	stAfter, err := indexstore.ReadStore(v, p)
	if err != nil {
		t.Fatalf("ReadStore after: %v", err)
	}
	if roomOf(stAfter, id) != "devops" {
		t.Errorf("post-relabel room = %q, want devops", roomOf(stAfter, id))
	}
}

// --- C3: id, embed vector and owner set preserved across a relabel ----------

// After a relabel the chunk id, its embed-cache vector and its owner set
// (source_sha256 values) are unchanged; only the room differs.
func TestC3_RelabelPreservesIDVectorOwners(t *testing.T) {
	v, p := newProjectVault(t)
	markMigrated(t, v.Root)
	const content = "The ingester commits each archive in four durable steps."
	id := index.ChunkID(content)

	ix, err := ProjectIndexing(v, p)
	if err != nil {
		t.Fatalf("ProjectIndexing: %v", err)
	}
	tx, err := indexstore.Lock(context.Background(), v, p, indexstore.NoTimeout)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	tx.UseRecipe(ix.Recipe)
	if _, err := tx.EnsureLedger(nil); err != nil {
		_ = tx.Release()
		t.Fatalf("EnsureLedger: %v", err)
	}
	const sha = "shaOwner"
	commit := indexstore.ArchiveCommit{
		SessionID: "S1", SHA: sha, StartDay: "2026-05-10", StartDaySource: indexstore.DayFromTranscript,
		Chunks:  []indexstore.OwnedChunk{ownedChunkOf(content, "proj", "api", "facts", "transcript")},
		Vectors: map[string][]float32{id: {0.25, 0.5, 0.75}},
	}
	if err := tx.CommitArchive(commit, vecWriter{v}); err != nil {
		_ = tx.Release()
		t.Fatalf("CommitArchive: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	vecBefore := readVec(t, v, p, id)
	ownersBefore := ownerSHAs(t, v, p, id)

	moves := []MoveCandidate{{DrawerID: id, Wing: "proj", FromRoom: "api", ToRoom: "devops"}}
	if err := Relabel(context.Background(), v, p, moves, 5*time.Second); err != nil {
		t.Fatalf("Relabel: %v", err)
	}

	st, err := indexstore.ReadStore(v, p)
	if err != nil {
		t.Fatalf("ReadStore: %v", err)
	}
	var found bool
	for _, c := range st.Chunks(true) {
		if c.ID != id {
			continue
		}
		found = true
		if c.Room != "devops" {
			t.Errorf("room = %q, want devops", c.Room)
		}
	}
	if !found {
		t.Fatalf("chunk id %s changed or vanished across the relabel", id)
	}

	if vecAfter := readVec(t, v, p, id); vecAfter != vecBefore {
		t.Errorf("embed-cache vector changed across a relabel:\n before=%q\n after =%q", vecBefore, vecAfter)
	}
	ownersAfter := ownerSHAs(t, v, p, id)
	if len(ownersAfter) != 1 || ownersAfter[0] != sha || len(ownersBefore) != 1 || ownersBefore[0] != sha {
		t.Errorf("owner set changed across a relabel: before=%v after=%v, want [%s]", ownersBefore, ownersAfter, sha)
	}
}

// --- R1: a relabel does not wait on a run ------------------------------------

// A goroutine holding the index RUN lock (as an ingest would) does not block a
// Relabel: it completes, and the run lock is still held afterwards.
func TestR1_RelabelDoesNotWaitOnRunLock(t *testing.T) {
	v, p := newProjectVault(t)
	markMigrated(t, v.Root)
	const content = "A relabel never touches the run lock."
	id := index.ChunkID(content)
	seedStoreChunk(t, v, p, "proj", "api", content, "transcript")

	rl, ok, err := indexstore.TryRunLock(v, indexstore.KindIngest, p)
	if err != nil || !ok {
		t.Fatalf("hold run lock: ok=%v err=%v", ok, err)
	}
	defer func() { _ = rl.Release() }()

	done := make(chan error, 1)
	moves := []MoveCandidate{{DrawerID: id, Wing: "proj", FromRoom: "api", ToRoom: "devops"}}
	go func() { done <- Relabel(context.Background(), v, p, moves, 5*time.Second) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Relabel while run lock held: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Relabel did not complete while the run lock was held (it must not wait on it)")
	}

	// The run lock is still ours: its holder record still names this process.
	h, err := indexstore.ReadHolder(v)
	if err != nil {
		t.Fatalf("ReadHolder after relabel: %v (the run lock was released)", err)
	}
	if h.PID != os.Getpid() {
		t.Errorf("run-lock holder pid = %d, want this process %d", h.PID, os.Getpid())
	}
}

// --- shared test helpers -----------------------------------------------------

var errFixture = &fixtureErr{}

type fixtureErr struct{}

func (*fixtureErr) Error() string { return "fixture: ingest failed" }

// noVecs writes no vectors, for commits whose vectors a test does not inspect.
type noVecs struct{}

func (noVecs) Put(string, string, []float32) error { return nil }

// vecWriter persists each vector as a <id>.vec file in the project's embed
// cache, the on-disk layout C3 reads back to prove a relabel leaves it alone.
type vecWriter struct{ v *storage.Vault }

func (w vecWriter) Put(project, id string, vec []float32) error {
	dir, err := w.v.EmbedCacheDir(project)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, id+".vec"), vecBytes(vec), 0o644)
}

func vecBytes(vec []float32) []byte {
	b := make([]byte, 0, len(vec)*4)
	for _, f := range vec {
		// A stable, test-only encoding; the value just has to round-trip.
		b = append(b, byte(int(f*100)), byte(int(f*100)>>8), 0, 0)
	}
	return b
}

func readVec(t *testing.T, v *storage.Vault, project, id string) string {
	t.Helper()
	dir, err := v.EmbedCacheDir(project)
	if err != nil {
		t.Fatalf("EmbedCacheDir: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, id+".vec"))
	if err != nil {
		t.Fatalf("read vec %s: %v", id, err)
	}
	return string(b)
}

func ownerSHAs(t *testing.T, v *storage.Vault, project, id string) []string {
	t.Helper()
	st, err := indexstore.ReadStore(v, project)
	if err != nil {
		t.Fatalf("ReadStore: %v", err)
	}
	for _, c := range st.Chunks(true) {
		if c.ID != id {
			continue
		}
		var shas []string
		for _, o := range c.Owners {
			if o.Kind == indexstore.OwnerArchive {
				shas = append(shas, o.SHA)
			}
		}
		return shas
	}
	return nil
}

func roomOf(st *indexstore.Store, id string) string {
	for _, c := range st.Chunks(true) {
		if c.ID == id {
			return c.Room
		}
	}
	return ""
}
