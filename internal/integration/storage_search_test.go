// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestIntegrationStorageToSearch proves that drawers written via the storage
// layer become searchable after a rebuild, with correct semantic ranking.
func TestIntegrationStorageToSearch(t *testing.T) {
	h := newHarness(t, true) // real ONNX
	ctx := context.Background()

	// Seed drawers with distinct topics across wings and rooms.
	h.addDrawer(t, "proj", "dev", "go", "Go goroutines and channels enable lightweight concurrency patterns", "facts", "2026-04-01")
	h.addDrawer(t, "proj", "dev", "go", "The sync.Mutex protects shared state in concurrent Go programs", "facts", "2026-04-01")
	h.addDrawer(t, "proj", "data", "sql", "PostgreSQL uses MVCC for transaction isolation and concurrent reads", "facts", "2026-04-02")
	h.addDrawer(t, "proj", "infra", "docker", "Docker containers package applications with their dependencies for deployment", "facts", "2026-04-03")
	h.addDrawer(t, "proj", "dev", "go", "Go interfaces enable polymorphism without inheritance hierarchies", "discoveries", "2026-04-03")

	if _, err := h.Engine.Rebuild(ctx, "proj"); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// Search for Go content — should rank Go drawers highest.
	results, err := h.Engine.Search(ctx, "goroutines and concurrency in Go", search.SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for Go query")
	}
	if results[0].Wing != "dev" || results[0].Room != "go" {
		t.Errorf("top result = %s/%s, want dev/go", results[0].Wing, results[0].Room)
	}

	// Search for SQL content.
	results, err = h.Engine.Search(ctx, "database transaction isolation", search.SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for SQL query")
	}
	if results[0].Wing != "data" || results[0].Room != "sql" {
		t.Errorf("top result = %s/%s, want data/sql", results[0].Wing, results[0].Room)
	}

	// Wing filter: only dev wing.
	results, err = h.Engine.Search(ctx, "any content", search.SearchFilters{Project: "proj", Wing: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Wing != "dev" {
			t.Errorf("wing filter leaked: got %s", r.Wing)
		}
	}

	// Hall filter: only discoveries.
	results, err = h.Engine.Search(ctx, "any content", search.SearchFilters{Project: "proj", Hall: "discoveries"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Hall != "discoveries" {
			t.Errorf("hall filter leaked: got %s", r.Hall)
		}
	}

	// Date range filter.
	results, err = h.Engine.Search(ctx, "any content", search.SearchFilters{
		Project:  "proj",
		DateFrom: "2026-04-02",
		DateTo:   "2026-04-02",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Date != "2026-04-02" {
			t.Errorf("date filter leaked: got %s", r.Date)
		}
	}
}

// TestIntegrationStorageSearchMetadataPreservation verifies that all drawer
// metadata fields survive the storage → index → search round-trip.
func TestIntegrationStorageSearchMetadataPreservation(t *testing.T) {
	h := newHarness(t, true) // real ONNX
	ctx := context.Background()

	d := storage.Drawer{
		Content:    "Kubernetes pod scheduling uses node affinity rules for placement",
		Hall:       "advice",
		SourceType: "session",
		SourceRef:  "session-meta-01",
		ChunkIndex: 7,
		FiledAt:    "2026-03-15T14:30:00Z",
		AddedBy:    "capture",
	}
	if err := h.Vault.AppendDrawer("proj", "infra", "k8s", d); err != nil {
		t.Fatal(err)
	}

	if _, err := h.Engine.Rebuild(ctx, "proj"); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	results, err := h.Engine.Search(ctx, "kubernetes scheduling", search.SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.Wing != "infra" {
		t.Errorf("Wing = %q, want %q", r.Wing, "infra")
	}
	if r.Room != "k8s" {
		t.Errorf("Room = %q, want %q", r.Room, "k8s")
	}
	if r.Hall != "advice" {
		t.Errorf("Hall = %q, want %q", r.Hall, "advice")
	}
	if r.SourceType != "session" {
		t.Errorf("SourceType = %q, want %q", r.SourceType, "session")
	}
	if r.SourceRef != "session-meta-01" {
		t.Errorf("SourceRef = %q, want %q", r.SourceRef, "session-meta-01")
	}
	if r.Date != "2026-03-15" {
		t.Errorf("Date = %q, want %q", r.Date, "2026-03-15")
	}
	if r.Project != "proj" {
		t.Errorf("Project = %q, want %q", r.Project, "proj")
	}
}
