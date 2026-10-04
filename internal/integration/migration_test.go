// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/kgread"
	"github.com/suykerbuyk/vibe-palace/internal/migrate"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestIntegrationVibeVaultImportWritesArchives proves that ImportVibeVault
// writes each session as a transcript archive dated by the session, and
// nothing derived: no drawers, no tracked KG, nothing searchable until the
// host-local pending-archive ingester indexes the archives.
func TestIntegrationVibeVaultImportWritesArchives(t *testing.T) {
	h := newHarness(t, true) // real ONNX embedder for meaningful search

	// Create a VibeVault-style project with session files.
	sessDir := filepath.Join(h.Vault.Root, "Projects", "test-project", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	session1 := `---
session_id: "2026-04-01-01"
project: test-project
date: "2026-04-01"
title: "Database Migration Design"
summary: "Designed schema migration system"
tag: planning
---
## Human

We need a database migration system for PostgreSQL that supports
versioned up and down migrations with rollback capability.

## Assistant

I recommend using sequential numbered SQL files with a migrations
tracking table. Each migration runs inside a transaction for atomicity.
The system should support both forward migrations and rollback.
`

	session2 := `---
session_id: "2026-04-02-01"
project: test-project
date: "2026-04-02"
title: "Authentication Middleware"
summary: "Implemented JWT auth middleware"
tag: implementation
---
## Human

Let's implement JWT authentication middleware for our HTTP API.

## Assistant

I'll create middleware that validates Bearer tokens from the Authorization
header. We'll use RS256 signing with key rotation support. The middleware
extracts claims and attaches the authenticated user to the request context.
We modified internal/auth/middleware.go and internal/auth/jwt.go for this.
The middleware in internal/auth/middleware.go validates the Bearer header,
and internal/auth/jwt.go wraps the RS256 signing.
`

	for name, content := range map[string]string{
		"2026-04-01-01.md": session1,
		"2026-04-02-01.md": session2,
	} {
		if err := os.WriteFile(filepath.Join(sessDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Run the import.
	result, err := migrate.ImportVibeVault(
		context.Background(), h.Vault, h.Vault,
		migrate.ImportOptions{},
	)
	if err != nil {
		t.Fatalf("ImportVibeVault: %v", err)
	}
	if result.SessionsImported != 2 {
		t.Fatalf("SessionsImported = %d, want 2", result.SessionsImported)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}

	// The sessions are archived, dated by their own date, and nothing else
	// is written: no drawers and no tracked KG (task
	// importers-write-the-frozen-tracked-corpus, Scope 3).
	if result.ArchivesWritten != 2 || result.DrawersCreated != 0 || result.EntitiesCreated != 0 {
		t.Fatalf("archives %d, drawers %d, entities %d; want 2, 0, 0", result.ArchivesWritten, result.DrawersCreated, result.EntitiesCreated)
	}
	for _, day := range []string{"2026-04-01", "2026-04-02"} {
		if m, _ := filepath.Glob(filepath.Join(h.Vault.Root, "Projects", "test-project", "transcripts", day+"-*.manifest.json")); len(m) != 1 {
			t.Errorf("want one archive dated %s, got %v", day, m)
		}
	}
	if wings, _ := h.Vault.ListWings("test-project"); len(wings) != 0 {
		t.Errorf("the import wrote drawers: wings %v", wings)
	}
	if entities, _, _ := h.Vault.ListEntities("test-project"); len(entities) != 0 {
		t.Errorf("the import wrote tracked KG entities: %d", len(entities))
	}

	// The archives are not indexed on this host until the pending-archive
	// ingester runs: that is what the import's last line says. This import is
	// in place, so the session notes themselves are in Projects/<p>/sessions/
	// and the note corpus serves their text (source_type session-note); no
	// result may come from the archives (a transcript chunk, source_type
	// session).
	results, err := h.Engine.Search(context.Background(), "database migration PostgreSQL rollback",
		search.SearchFilters{Project: "test-project", IncludeRaw: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, r := range results {
		if r.SourceType == "session" {
			t.Errorf("an archive's transcript was served before any ingest: %s", truncate(r.Content, 80))
		}
	}
}

// TestIntegrationVibeVaultIdempotentReimport proves that importing the same
// sessions twice writes no second archive.
func TestIntegrationVibeVaultIdempotentReimport(t *testing.T) {
	h := newHarness(t, false) // mock embedder sufficient for idempotency test

	sessDir := filepath.Join(h.Vault.Root, "Projects", "idempotent-proj", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	session := `---
session_id: "2026-04-01-01"
project: idempotent-proj
date: "2026-04-01"
title: "Idempotency Test"
summary: "Test session for idempotency"
---
We discussed implementing a caching layer with Redis for the API gateway.
The cache invalidation strategy uses TTL with event-driven purging.
We also reviewed internal/cache/redis.go for connection pooling issues.
`
	if err := os.WriteFile(filepath.Join(sessDir, "2026-04-01-01.md"), []byte(session), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// First import.
	r1, err := migrate.ImportVibeVault(ctx, h.Vault, h.Vault, migrate.ImportOptions{})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if r1.SessionsImported != 1 {
		t.Fatalf("first: SessionsImported = %d, want 1", r1.SessionsImported)
	}

	// Count the archives after the first import.
	manifests1, _ := filepath.Glob(filepath.Join(h.Vault.Root, "Projects", "idempotent-proj", "transcripts", "*.manifest.json"))
	if len(manifests1) != 1 {
		t.Fatalf("first import: %d archives, want 1", len(manifests1))
	}

	// Second import — should skip.
	r2, err := migrate.ImportVibeVault(ctx, h.Vault, h.Vault, migrate.ImportOptions{})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if r2.SessionsSkipped != 1 {
		t.Errorf("second: SessionsSkipped = %d, want 1", r2.SessionsSkipped)
	}
	if r2.SessionsImported != 0 {
		t.Errorf("second: SessionsImported = %d, want 0", r2.SessionsImported)
	}

	// Verify no new archive was written.
	manifests2, _ := filepath.Glob(filepath.Join(h.Vault.Root, "Projects", "idempotent-proj", "transcripts", "*.manifest.json"))
	if len(manifests2) != len(manifests1) || r2.ArchivesWritten != 0 {
		t.Errorf("archives changed: %d → %d (written %d)", len(manifests1), len(manifests2), r2.ArchivesWritten)
	}
}

// TestIntegrationMemPalaceImportToSearch proves that ImportMemPalace stores
// drawers that are indexed and searchable, and that entities and triples
// are queryable through the storage layer.
func TestIntegrationMemPalaceImportToSearch(t *testing.T) {
	h := newHarness(t, true) // real ONNX for meaningful search

	// Build a MemPalace export fixture.
	export := map[string]any{
		"exported_at": "2026-04-09T00:00:00Z",
		"drawers": []map[string]any{
			{
				"id":          "mp-d1",
				"wing":        "technical",
				"room":        "golang",
				"content":     "Implemented a concurrent worker pool in Go using channels and goroutines for processing HTTP requests in parallel with graceful shutdown support.",
				"source_file": "session-01.md",
				"chunk_index": 0,
				"added_by":    "claude",
				"filed_at":    "2026-03-15T10:00:00Z",
			},
			{
				"id":          "mp-d2",
				"wing":        "emotions",
				"room":        "gratitude",
				"content":     "Feeling deeply grateful for the progress made on the memory palace project. The knowledge graph is coming together beautifully.",
				"source_file": "session-02.md",
				"chunk_index": 0,
				"added_by":    "claude",
				"filed_at":    "2026-03-16T10:00:00Z",
			},
			{
				"id":          "mp-d3",
				"wing":        "creative",
				"room":        "writing",
				"content":     "Explored narrative techniques for documenting technical decisions. The key insight is that decisions need context about what was rejected and why.",
				"source_file": "session-03.md",
				"chunk_index": 0,
				"added_by":    "claude",
				"filed_at":    "2026-03-17T10:00:00Z",
			},
		},
		"entities": []map[string]any{
			{
				"id":         "e-golang",
				"name":       "Go",
				"type":       "technology",
				"properties": map[string]string{"domain": "programming"},
				"created_at": "2026-03-15T00:00:00Z",
			},
		},
		"triples": []map[string]any{
			{
				"subject":        "Go",
				"predicate":      "used_in",
				"object":         "worker pool",
				"valid_from":     "2026-03-15T00:00:00Z",
				"valid_to":       nil,
				"confidence":     0.9,
				"source_session": "session-01",
			},
		},
	}

	exportPath := filepath.Join(h.Vault.Root, "mempalace-export.json")
	data, _ := json.MarshalIndent(export, "", "  ")
	if err := os.WriteFile(exportPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Run the import.
	mpExport, err := migrate.LoadMemPalaceExport(exportPath)
	if err != nil {
		t.Fatalf("LoadMemPalaceExport: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(h.Vault.Root, "Projects", "mp-proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := migrate.ImportMemPalace(
		context.Background(), h.Vault, "mp-proj", h.Engine, h.Embedder, mpExport,
		migrate.ImportOptions{},
	)
	if err != nil {
		t.Fatalf("ImportMemPalace: %v", err)
	}
	if result.DrawersCreated != 3 {
		t.Errorf("DrawersCreated = %d, want 3", result.DrawersCreated)
	}
	if result.EntitiesCreated != 1 {
		t.Errorf("EntitiesCreated = %d, want 1", result.EntitiesCreated)
	}
	if result.TriplesCreated != 1 {
		t.Errorf("TriplesCreated = %d, want 1", result.TriplesCreated)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}

	// The import is local-only: nothing under the tracked palace/mp-proj/.
	if _, err := os.Stat(filepath.Join(h.Vault.Root, "palace", "mp-proj")); !os.IsNotExist(err) {
		t.Errorf("the import wrote tracked palace/mp-proj (stat err %v)", err)
	}

	// Prove the imported drawers are searchable from the host-local store.
	results, err := h.Engine.Search(context.Background(), "concurrent worker pool goroutines channels",
		search.SearchFilters{Project: "mp-proj"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("search for 'worker pool goroutines' returned no results after MemPalace import")
	}
	top := strings.ToLower(results[0].Content)
	if !strings.Contains(top, "worker pool") && !strings.Contains(top, "goroutine") {
		t.Errorf("top result should mention worker pool/goroutine, got: %s", truncate(results[0].Content, 120))
	}

	// Search across a different topic — should find the emotions drawer.
	emotionResults, err := h.Engine.Search(context.Background(), "grateful progress knowledge graph memory palace",
		search.SearchFilters{Project: "mp-proj"})
	if err != nil {
		t.Fatalf("Search emotions: %v", err)
	}
	if len(emotionResults) == 0 {
		t.Fatal("search for 'grateful knowledge graph' returned no results")
	}

	// Prove KG entities were imported.
	entities, _, err := kgread.ListEntities(h.Vault, "mp-proj")
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	foundGo := false
	for _, e := range entities {
		if e.Name == "Go" && e.Type == "technology" {
			foundGo = true
			break
		}
	}
	if !foundGo {
		t.Error("expected 'Go' entity in mempalace KG after import")
	}

	// Prove KG triples were imported.
	triples, err := kgread.QueryEntity(h.Vault, "mp-proj", "Go", "", "out")
	if err != nil {
		t.Fatalf("QueryEntity: %v", err)
	}
	foundTriple := false
	for _, tr := range triples {
		if tr.Predicate == "used_in" && tr.Object == "worker pool" {
			// Host-local KG payloads keep identity fields and origin only
			// (ruling C1): the export's confidence is not carried.
			foundTriple = true
			break
		}
	}
	if !foundTriple {
		t.Error("expected 'Go used_in worker pool' triple in mempalace KG")
	}
}

// TestIntegrationMemPalaceIdempotent proves that importing the same MemPalace
// export twice does not create duplicate drawers (content-addressed dedup).
func TestIntegrationMemPalaceIdempotent(t *testing.T) {
	h := newHarness(t, false)

	export := map[string]any{
		"exported_at": "2026-04-09T00:00:00Z",
		"drawers": []map[string]any{
			{
				"id":       "mp-idem-1",
				"wing":     "technical",
				"room":     "testing",
				"content":  "Integration tests prove that components work together correctly.",
				"filed_at": "2026-04-01T00:00:00Z",
			},
		},
		"entities": []map[string]any{},
		"triples":  []map[string]any{},
	}

	exportPath := filepath.Join(h.Vault.Root, "export.json")
	data, _ := json.MarshalIndent(export, "", "  ")
	os.WriteFile(exportPath, data, 0o644)

	ctx := context.Background()
	mpExport, err := migrate.LoadMemPalaceExport(exportPath)
	if err != nil {
		t.Fatalf("LoadMemPalaceExport: %v", err)
	}

	// First import.
	if err := os.MkdirAll(filepath.Join(h.Vault.Root, "Projects", "mp-proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	r1, err := migrate.ImportMemPalace(ctx, h.Vault, "mp-proj", h.Engine, h.Embedder, mpExport, migrate.ImportOptions{})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if r1.DrawersCreated != 1 {
		t.Fatalf("first: DrawersCreated = %d, want 1", r1.DrawersCreated)
	}

	drawers1, _ := localChunks(t, h.Vault, "mp-proj")

	// Second import — AppendDrawer dedup should prevent new drawers.
	r2, err := migrate.ImportMemPalace(ctx, h.Vault, "mp-proj", h.Engine, h.Embedder, mpExport, migrate.ImportOptions{})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if r2.DrawersCreated != 0 {
		t.Errorf("second: DrawersCreated = %d, want 0", r2.DrawersCreated)
	}

	drawers2, _ := localChunks(t, h.Vault, "mp-proj")
	if drawers2 != drawers1 {
		t.Errorf("drawer count changed: %d → %d", drawers1, drawers2)
	}
}

// TestIntegrationMigrateVibeVaultDryRunNoSideEffects proves that dry-run
// mode does not create any drawers, KG entities, or idempotency markers.
func TestIntegrationMigrateVibeVaultDryRunNoSideEffects(t *testing.T) {
	h := newHarness(t, false)

	sessDir := filepath.Join(h.Vault.Root, "Projects", "dryrun-proj", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	session := `---
session_id: "2026-04-01-01"
project: dryrun-proj
date: "2026-04-01"
title: "Dry Run Test"
summary: "Should not persist"
---
Content that should not be indexed during dry run.
We discussed internal/api/handler.go refactoring.
`
	os.WriteFile(filepath.Join(sessDir, "2026-04-01-01.md"), []byte(session), 0o644)

	result, err := migrate.ImportVibeVault(
		context.Background(), h.Vault, h.Vault,
		migrate.ImportOptions{DryRun: true},
	)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if result.SessionsImported != 1 {
		t.Errorf("SessionsImported = %d, want 1 (counted but not written)", result.SessionsImported)
	}

	// Verify no drawers were created.
	drawers, _ := countAllDrawers(t, h.Vault, "dryrun-proj")
	if drawers != 0 {
		t.Errorf("expected 0 drawers after dry run, got %d", drawers)
	}

	// Verify no KG entities were created.
	entities, _, _ := h.Vault.ListEntities("dryrun-proj")
	if len(entities) != 0 {
		t.Errorf("expected 0 entities after dry run, got %d", len(entities))
	}

	// Verify no idempotency marker was written.
	importsDir, _ := h.Vault.ImportsDir("dryrun-proj")
	markerPath := filepath.Join(importsDir, "imported-sessions.jsonl")
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Error("idempotency marker should not exist after dry run")
	}

	// Now do a real import — should succeed since dry run left no markers.
	r2, err := migrate.ImportVibeVault(
		context.Background(), h.Vault, h.Vault,
		migrate.ImportOptions{},
	)
	if err != nil {
		t.Fatalf("real import after dry run: %v", err)
	}
	if r2.SessionsImported != 1 {
		t.Errorf("real import: SessionsImported = %d, want 1", r2.SessionsImported)
	}
}

// localChunks counts a project's chunks in the host-local store, visible or
// not.
func localChunks(t *testing.T, v *storage.Vault, project string) (int, error) {
	t.Helper()
	st, err := indexstore.ReadStore(v, project)
	if err != nil {
		return 0, err
	}
	return len(st.Chunks(false)), nil
}
