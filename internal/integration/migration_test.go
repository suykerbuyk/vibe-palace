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

	"github.com/suykerbuyk/vibe-palace/internal/migrate"
	"github.com/suykerbuyk/vibe-palace/internal/search"
)

// TestIntegrationVibeVaultImportToSearch proves that ImportVibeVault feeds
// session transcripts through the capture pipeline and the resulting drawers
// are searchable via the search engine, and KG entities are queryable.
func TestIntegrationVibeVaultImportToSearch(t *testing.T) {
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
		context.Background(), h.Vault, h.Vault, h.Engine, h.Embedder, h.Config,
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

	// Prove drawers landed in storage.
	wings, err := h.Vault.ListWings("test-project")
	if err != nil {
		t.Fatalf("ListWings: %v", err)
	}
	if len(wings) == 0 {
		t.Fatal("expected at least one wing after import")
	}

	totalDrawers := 0
	for _, wing := range wings {
		drawers, err := h.Vault.ListDrawersByWing("test-project", wing)
		if err != nil {
			t.Fatalf("ListDrawersByWing(%s): %v", wing, err)
		}
		totalDrawers += len(drawers)
	}
	if totalDrawers == 0 {
		t.Fatal("expected drawers in storage after import")
	}

	// Prove imported content is searchable — query for database migration.
	results, err := h.Engine.Search(context.Background(), "database migration PostgreSQL rollback",
		search.SearchFilters{Project: "test-project"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("search for 'database migration' returned no results after import")
	}

	// Top result should be about database migration, not JWT auth.
	top := strings.ToLower(results[0].Content)
	if !strings.Contains(top, "migration") && !strings.Contains(top, "database") {
		t.Errorf("top result should mention migration/database, got: %s", truncate(results[0].Content, 120))
	}

	// Search for JWT authentication — should find session 2.
	jwtResults, err := h.Engine.Search(context.Background(), "JWT authentication middleware Bearer token",
		search.SearchFilters{Project: "test-project"})
	if err != nil {
		t.Fatalf("Search JWT: %v", err)
	}
	if len(jwtResults) == 0 {
		t.Fatal("search for 'JWT authentication' returned no results after import")
	}

	topJwt := strings.ToLower(jwtResults[0].Content)
	if !strings.Contains(topJwt, "jwt") && !strings.Contains(topJwt, "auth") && !strings.Contains(topJwt, "token") {
		t.Errorf("top JWT result should mention jwt/auth/token, got: %s", truncate(jwtResults[0].Content, 120))
	}

	// Prove KG entities were extracted from session transcripts.
	entities, err := h.Vault.ListEntities("test-project")
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(entities) == 0 {
		t.Fatal("expected KG entities from imported transcripts")
	}

	// Session 2 mentions internal/auth/middleware.go — verify file entity exists.
	foundFile := false
	for _, e := range entities {
		if e.Type == "file" && strings.Contains(e.Name, "middleware.go") {
			foundFile = true
			break
		}
	}
	if !foundFile {
		t.Error("expected file entity for middleware.go from session 2 transcript")
	}
}

// TestIntegrationVibeVaultIdempotentReimport proves that importing the same
// sessions twice does not create duplicate drawers or KG entries, and that
// previously imported content remains searchable.
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
	r1, err := migrate.ImportVibeVault(ctx, h.Vault, h.Vault, h.Engine, h.Embedder, h.Config, migrate.ImportOptions{})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if r1.SessionsImported != 1 {
		t.Fatalf("first: SessionsImported = %d, want 1", r1.SessionsImported)
	}

	// Count drawers and entities after first import.
	drawers1, _ := countAllDrawers(t, h.Vault, "idempotent-proj")
	entities1, _ := h.Vault.ListEntities("idempotent-proj")

	// Second import — should skip.
	r2, err := migrate.ImportVibeVault(ctx, h.Vault, h.Vault, h.Engine, h.Embedder, h.Config, migrate.ImportOptions{})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if r2.SessionsSkipped != 1 {
		t.Errorf("second: SessionsSkipped = %d, want 1", r2.SessionsSkipped)
	}
	if r2.SessionsImported != 0 {
		t.Errorf("second: SessionsImported = %d, want 0", r2.SessionsImported)
	}

	// Verify no new drawers or entities were created.
	drawers2, _ := countAllDrawers(t, h.Vault, "idempotent-proj")
	entities2, _ := h.Vault.ListEntities("idempotent-proj")

	if drawers2 != drawers1 {
		t.Errorf("drawer count changed: %d → %d", drawers1, drawers2)
	}
	if len(entities2) != len(entities1) {
		t.Errorf("entity count changed: %d → %d", len(entities1), len(entities2))
	}

	// Verify content is still searchable after re-import.
	results, err := h.Engine.Search(ctx, "caching Redis API gateway",
		search.SearchFilters{Project: "idempotent-proj"})
	if err != nil {
		t.Fatalf("Search after reimport: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected search results after idempotent re-import")
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
	result, err := migrate.ImportMemPalace(
		context.Background(), h.Vault, h.Engine, h.Embedder, exportPath,
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

	// Prove drawers landed in storage under "mempalace" project.
	projects, err := h.Vault.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	foundMempalace := false
	for _, p := range projects {
		if p == "mempalace" {
			foundMempalace = true
			break
		}
	}
	if !foundMempalace {
		t.Fatal("expected 'mempalace' project in vault after import")
	}

	// Prove wing mapping: "technical" and "emotions" should appear as wings.
	wings, err := h.Vault.ListWings("mempalace")
	if err != nil {
		t.Fatalf("ListWings: %v", err)
	}
	wingSet := make(map[string]bool)
	for _, w := range wings {
		wingSet[w] = true
	}
	if !wingSet["technical"] {
		t.Error("expected 'technical' wing in mempalace project")
	}
	if !wingSet["emotions"] {
		t.Error("expected 'emotions' wing in mempalace project")
	}

	// Prove imported content is searchable.
	results, err := h.Engine.Search(context.Background(), "concurrent worker pool goroutines channels",
		search.SearchFilters{Project: "mempalace"})
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
		search.SearchFilters{Project: "mempalace"})
	if err != nil {
		t.Fatalf("Search emotions: %v", err)
	}
	if len(emotionResults) == 0 {
		t.Fatal("search for 'grateful knowledge graph' returned no results")
	}

	// Prove KG entities were imported.
	entities, err := h.Vault.ListEntities("mempalace")
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
	triples, err := h.Vault.QueryEntity("mempalace", "Go", "", "out")
	if err != nil {
		t.Fatalf("QueryEntity: %v", err)
	}
	foundTriple := false
	for _, tr := range triples {
		if tr.Predicate == "used_in" && tr.Object == "worker pool" {
			foundTriple = true
			if tr.Confidence != 0.9 {
				t.Errorf("triple confidence = %f, want 0.9", tr.Confidence)
			}
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

	// First import.
	r1, err := migrate.ImportMemPalace(ctx, h.Vault, h.Engine, h.Embedder, exportPath, migrate.ImportOptions{})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if r1.DrawersCreated != 1 {
		t.Fatalf("first: DrawersCreated = %d, want 1", r1.DrawersCreated)
	}

	drawers1, _ := countAllDrawers(t, h.Vault, "mempalace")

	// Second import — AppendDrawer dedup should prevent new drawers.
	r2, err := migrate.ImportMemPalace(ctx, h.Vault, h.Engine, h.Embedder, exportPath, migrate.ImportOptions{})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if r2.DrawersCreated != 0 {
		t.Errorf("second: DrawersCreated = %d, want 0", r2.DrawersCreated)
	}

	drawers2, _ := countAllDrawers(t, h.Vault, "mempalace")
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
		context.Background(), h.Vault, h.Vault, h.Engine, h.Embedder, h.Config,
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
	entities, _ := h.Vault.ListEntities("dryrun-proj")
	if len(entities) != 0 {
		t.Errorf("expected 0 entities after dry run, got %d", len(entities))
	}

	// Verify no idempotency marker was written.
	localDir, _ := h.Vault.LocalDir("dryrun-proj")
	markerPath := filepath.Join(localDir, "imported-sessions.jsonl")
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Error("idempotency marker should not exist after dry run")
	}

	// Now do a real import — should succeed since dry run left no markers.
	r2, err := migrate.ImportVibeVault(
		context.Background(), h.Vault, h.Vault, h.Engine, h.Embedder, h.Config,
		migrate.ImportOptions{},
	)
	if err != nil {
		t.Fatalf("real import after dry run: %v", err)
	}
	if r2.SessionsImported != 1 {
		t.Errorf("real import: SessionsImported = %d, want 1", r2.SessionsImported)
	}
}
