// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// storeChunk seeds one note-owned chunk into project's host-local store. A note
// owner is always live (its day is its own), so the chunk is visible without a
// ledgered archive — the simplest fixture for the navigation reader.
func seedStoreChunk(t *testing.T, v *storage.Vault, project, wing, room, content, sourceType string) {
	t.Helper()
	ix, err := ProjectIndexing(v, project)
	if err != nil {
		t.Fatalf("ProjectIndexing: %v", err)
	}
	tx, err := indexstore.Lock(context.Background(), v, project, indexstore.NoTimeout)
	if err != nil {
		t.Fatalf("indexstore.Lock: %v", err)
	}
	tx.UseRecipe(ix.Recipe)
	rec := indexstore.OwnedChunk{
		Chunk:     indexstore.Chunk{ID: index.ChunkID(content), Content: content, Wing: wing, Room: room, Hall: "facts"},
		Ownership: indexstore.Ownership{SourceType: sourceType, Day: "2026-05-01"},
	}
	if err := tx.Append(indexstore.NoteOwner("notes/"+content+".md"), []indexstore.OwnedChunk{rec}); err != nil {
		_ = tx.Release()
		t.Fatalf("Append: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func markMigrated(t *testing.T, root string) {
	t.Helper()
	if err := surface.WriteVaultManifest(root, surface.VaultManifest{
		Format:       surface.RequiredDataFormat,
		AuthoredOnly: "2026-10-04",
	}); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}
}

func newProjectVault(t *testing.T) (*storage.Vault, string) {
	t.Helper()
	v := storage.NewVault(t.TempDir())
	testutil.InitProject(t, v.Root, "proj")
	return v, "proj"
}

// Wings and rooms come from chunk-store metadata, not a drawers/ directory.
func TestWingsAndRoomsFromStoreMetadata(t *testing.T) {
	v, p := newProjectVault(t)
	markMigrated(t, v.Root)
	seedStoreChunk(t, v, p, "alpha", "api", "alpha api content", "transcript")
	seedStoreChunk(t, v, p, "beta", "data", "beta data content", "transcript")

	g, err := BuildGraph(v, p)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	if g.Stats().Wings != 2 {
		t.Errorf("wings = %d, want 2 (from store metadata, with no drawers/ dir)", g.Stats().Wings)
	}
	if _, ok := g.Nodes["alpha/api"]; !ok {
		t.Error("missing alpha/api node")
	}
	if _, ok := g.Nodes["beta/data"]; !ok {
		t.Error("missing beta/data node")
	}
}

// Before the marker, the tracked drawers are read too, merged with the store.
func TestFallbackBeforeMarker(t *testing.T) {
	v, p := newProjectVault(t)
	// No marker. Store holds wing alpha; a tracked drawer holds wing beta.
	seedStoreChunk(t, v, p, "alpha", "api", "alpha store content", "transcript")
	if err := v.AppendDrawer(p, "beta", "general", storage.Drawer{
		Content: "beta tracked content", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("AppendDrawer: %v", err)
	}

	g, err := BuildGraph(v, p)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	if _, ok := g.Nodes["alpha/api"]; !ok {
		t.Error("store wing alpha/api missing")
	}
	if _, ok := g.Nodes["beta/general"]; !ok {
		t.Error("tracked-fallback wing beta/general missing before the marker")
	}
}

// A tracked drawer whose content is already a store chunk is deduped, through
// the shared helper (not a private dedup), by content hash alone.
func TestGlideDedupThroughSharedHelper(t *testing.T) {
	v, p := newProjectVault(t)
	const shared = "shared content in both store and a tracked drawer"
	const only = "content only in a tracked drawer"
	seedStoreChunk(t, v, p, "alpha", "api", shared, "transcript")
	// The same content as a tracked drawer under a DIFFERENT wing/room: it must
	// be deduped (content hash alone, never the wing).
	if err := v.AppendDrawer(p, "beta", "general", storage.Drawer{
		Content: shared, Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("AppendDrawer shared: %v", err)
	}
	if err := v.AppendDrawer(p, "beta", "general", storage.Drawer{
		Content: only, Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("AppendDrawer only: %v", err)
	}

	// Spy that the fallback dedups through the shared helper.
	calls := 0
	orig := glideDedup
	glideDedup = func(ids map[string]struct{}, content string) bool {
		calls++
		return orig(ids, content)
	}
	defer func() { glideDedup = orig }()

	hits, err := QueryDrawers(v, storage.DrawerQuery{Project: p})
	if err != nil {
		t.Fatalf("QueryDrawers: %v", err)
	}
	if calls == 0 {
		t.Error("glideDedup was never called: the fallback did not dedup through the shared helper")
	}
	var sharedCount, onlyCount int
	for _, h := range hits {
		switch h.Content {
		case shared:
			sharedCount++
		case only:
			onlyCount++
		}
	}
	if sharedCount != 1 {
		t.Errorf("shared content returned %d times, want 1 (deduped against the store chunk)", sharedCount)
	}
	if onlyCount != 1 {
		t.Errorf("tracked-only content returned %d times, want 1", onlyCount)
	}
}

// After the marker, Relabel rewrites a chunk's room in the store through Tx and
// bumps the store's change counter. Ids are unchanged.
func TestRelabelThroughTxAndCounter(t *testing.T) {
	v, p := newProjectVault(t)
	markMigrated(t, v.Root)
	const content = "Set up the kubernetes cluster for deployment."
	id := index.ChunkID(content)
	seedStoreChunk(t, v, p, "proj", "api", content, "transcript")

	genBefore := readGen(t, v, p)

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
		if c.ID == id {
			found = true
			if c.Room != "devops" {
				t.Errorf("room = %q, want devops after relabel", c.Room)
			}
		}
	}
	if !found {
		t.Fatalf("relabelled chunk %s not found (id must not change)", id)
	}
	if readGen(t, v, p) <= genBefore {
		t.Errorf("store change counter did not advance after a relabel (before=%d)", genBefore)
	}
}

// Before the marker, Relabel refuses and writes nothing.
func TestRelabelRefusesBeforeMarker(t *testing.T) {
	v, p := newProjectVault(t)
	const content = "Set up the kubernetes cluster for deployment."
	seedStoreChunk(t, v, p, "proj", "api", content, "transcript")

	moves := []MoveCandidate{{DrawerID: index.ChunkID(content), Wing: "proj", FromRoom: "api", ToRoom: "devops"}}
	err := Relabel(context.Background(), v, p, moves, 5*time.Second)
	if !errors.Is(err, ErrRelabelBeforeMarker) {
		t.Fatalf("Relabel before marker = %v, want ErrRelabelBeforeMarker", err)
	}
	st, _ := indexstore.ReadStore(v, p)
	for _, c := range st.Chunks(true) {
		if c.ID == index.ChunkID(content) && c.Room != "api" {
			t.Errorf("chunk room changed to %q despite the refusal", c.Room)
		}
	}
}

// On a lock timeout Relabel writes nothing and returns an error naming the busy
// lock; the store stays byte-identical.
func TestRelabelLockTimeout(t *testing.T) {
	v, p := newProjectVault(t)
	markMigrated(t, v.Root)
	const content = "Set up the kubernetes cluster for deployment."
	seedStoreChunk(t, v, p, "proj", "api", content, "transcript")

	// Hold the commit lock for the whole relabel attempt.
	holder, err := indexstore.Lock(context.Background(), v, p, indexstore.NoTimeout)
	if err != nil {
		t.Fatalf("hold lock: %v", err)
	}

	genBefore := readGen(t, v, p)
	moves := []MoveCandidate{{DrawerID: index.ChunkID(content), Wing: "proj", FromRoom: "api", ToRoom: "devops"}}
	err = Relabel(context.Background(), v, p, moves, 0)
	if err == nil {
		_ = holder.Release()
		t.Fatal("Relabel returned nil while the commit lock was held; want a timeout error")
	}

	// Nothing was written: the counter did not move and the room is unchanged.
	if readGen(t, v, p) != genBefore {
		t.Errorf("store counter moved on a refused relabel (before=%d)", genBefore)
	}
	if err := holder.Release(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	st, _ := indexstore.ReadStore(v, p)
	for _, c := range st.Chunks(true) {
		if c.ID == index.ChunkID(content) && c.Room != "api" {
			t.Errorf("chunk room changed to %q despite the timeout", c.Room)
		}
	}
}

// readGen reads the project's store change counter (the Gen component).
func readGen(t *testing.T, v *storage.Vault, project string) uint64 {
	t.Helper()
	g, err := indexstore.ReadGeneration(v, project)
	if err != nil {
		t.Fatalf("ReadGeneration: %v", err)
	}
	return g.Gen
}
