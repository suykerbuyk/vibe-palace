// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func testSearchEngine(t *testing.T) (*search.Engine, *storage.Vault) {
	t.Helper()
	vault := storage.NewVault(t.TempDir())
	cfg := storage.Config{SearchDefaultLimit: 10}
	emb := embedder.NewMock(384)
	eng := search.NewEngine(emb, vault, cfg)
	t.Cleanup(func() { eng.Close() })
	return eng, vault
}

func seedDrawer(t *testing.T, v *storage.Vault, eng *search.Engine, project, wing, room, content, hall string) {
	t.Helper()
	d := storage.Drawer{
		Content:    content,
		Hall:       hall,
		SourceType: "session",
		FiledAt:    "2026-04-07T10:00:00Z",
	}
	if err := v.AppendDrawer(project, wing, room, d); err != nil {
		t.Fatal(err)
	}
	drawers, _ := v.ListDrawers(project, wing, room)
	for _, stored := range drawers {
		if stored.Content == content {
			if err := eng.IndexDrawers(context.Background(), []search.DrawerInput{{Project: project, Wing: wing, Room: room, Drawer: stored}}); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
}

func TestSearchToolBasic(t *testing.T) {
	eng, vault := testSearchEngine(t)
	seedDrawer(t, vault, eng, "proj", "wing-a", "room-1", "Go concurrency patterns", "facts")

	tool := SearchTool(eng)
	result, err := tool.Handler(context.Background(),
		json.RawMessage(`{"query": "concurrency", "project": "proj"}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	results := result.([]search.SearchResult)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Content != "Go concurrency patterns" {
		t.Errorf("content = %q, want 'Go concurrency patterns'", results[0].Content)
	}
	if results[0].Score <= 0 {
		t.Error("score should be positive")
	}
}

func TestSearchToolValidation(t *testing.T) {
	eng, _ := testSearchEngine(t)
	tool := SearchTool(eng)

	// Missing query.
	_, err := tool.Handler(context.Background(),
		json.RawMessage(`{"project": "proj"}`))
	if err == nil {
		t.Error("expected error for missing query")
	}

	// Missing project.
	_, err = tool.Handler(context.Background(),
		json.RawMessage(`{"query": "test"}`))
	if err == nil {
		t.Error("expected error for missing project")
	}
}

func TestSearchToolLimitClamping(t *testing.T) {
	eng, vault := testSearchEngine(t)
	for i := range 60 {
		d := storage.Drawer{
			Content:    string(rune('A'+i%26)) + " unique content",
			Hall:       "facts",
			SourceType: "manual",
			FiledAt:    "2026-04-07T00:00:00Z",
		}
		_ = vault.AppendDrawer("proj", "wing-a", "room-1", d)
	}
	_, _ = eng.Rebuild(context.Background(), "proj")

	tool := SearchTool(eng)
	result, err := tool.Handler(context.Background(),
		json.RawMessage(`{"query": "content", "project": "proj", "limit": 100}`))
	if err != nil {
		t.Fatal(err)
	}

	results := result.([]search.SearchResult)
	if len(results) > 50 {
		t.Errorf("limit clamping failed: got %d results, max should be 50", len(results))
	}
}

func TestCrossProjectToolBasic(t *testing.T) {
	eng, vault := testSearchEngine(t)
	seedDrawer(t, vault, eng, "proj-a", "wing-1", "room-1", "alpha content", "facts")
	seedDrawer(t, vault, eng, "proj-b", "wing-1", "room-1", "beta content", "facts")

	tool := SearchCrossProjectTool(eng)
	result, err := tool.Handler(context.Background(),
		json.RawMessage(`{"query": "content"}`))
	if err != nil {
		t.Fatal(err)
	}

	results := result.([]search.SearchResult)
	if len(results) != 2 {
		t.Errorf("cross-project: got %d results, want 2", len(results))
	}
}

func TestCrossProjectToolValidation(t *testing.T) {
	eng, _ := testSearchEngine(t)
	tool := SearchCrossProjectTool(eng)

	_, err := tool.Handler(context.Background(),
		json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for missing query")
	}
}

// TestSearchToolEmptyResults pins "known-but-empty project -> [], no error" —
// exactly the case Rebuild's own doc comment protects. The project must be a
// genuine member of the vault (a bare Projects/<slug>/ is enough for
// ListAllProjects to report it), or this would actually be exercising the
// unknown-project path covered separately by
// TestSearchToolUnknownProjectIsError below.
func TestSearchToolEmptyResults(t *testing.T) {
	eng, vault := testSearchEngine(t)
	if err := os.MkdirAll(filepath.Join(vault.Root, "Projects", "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := SearchTool(eng)

	result, err := tool.Handler(context.Background(),
		json.RawMessage(`{"query": "nothing", "project": "proj"}`))
	if err != nil {
		t.Fatal(err)
	}

	results := result.([]search.SearchResult)
	if len(results) != 0 {
		t.Errorf("expected empty results, got %d", len(results))
	}
}

// TestSearchToolUnknownProjectIsError pins the fix this task makes: a project
// absent from the vault is a tool error, never []. Before this task, an
// unknown project and a known-but-empty one were indistinguishable from an
// MCP caller's point of view; this test is the one that would have failed had
// requireSearchProject's CLI-only guard not been given an MCP-reachable
// equivalent. searchHandler renders the *search.UnknownProjectError into a
// fresh message naming the defect (not wrapped with %w), so this asserts on
// the message text rather than errors.As.
func TestSearchToolUnknownProjectIsError(t *testing.T) {
	eng, _ := testSearchEngine(t)
	tool := SearchTool(eng)

	_, err := tool.Handler(context.Background(),
		json.RawMessage(`{"query": "nothing", "project": "nosuchproject"}`))
	if err == nil {
		t.Fatal("expected an error for an unknown project, got nil")
	}
	if !strings.Contains(err.Error(), "unknown project") || !strings.Contains(err.Error(), "nosuchproject") {
		t.Errorf("error does not name the unknown project: %v", err)
	}
}

// TestSearchToolIncludeRawDefaultHidesRawWhenSummaryExists is the vp_search
// tool-handler proof of the include_raw wiring: with no include_raw field
// (defaulting to false), a query matching only the raw iteration body text
// must not surface that entry's raw row once a cached summary exists for it.
// It uses a real on-disk iterations.md + cached IterationSummary (via
// vault.IterationsFile/WriteIterationSummary), not seedDrawer/AppendDrawer,
// because only the iteration corpus collector sets SummaryAvailable.
func TestSearchToolIncludeRawDefaultHidesRawWhenSummaryExists(t *testing.T) {
	eng, vault := testSearchEngine(t)
	ctx := context.Background()
	const project = "tool-hide-raw-default"
	const rawMarker = "PANGOLIN_TOOL_RAW_ONLY_MARKER"

	writeToolIterationsMD(t, vault, project, strings.Join([]string{
		"## Iteration 1 — first",
		"",
		"Body containing " + rawMarker + " and nothing the summary will mention.",
		"",
		"---",
		"",
	}, "\n"))

	if err := vault.WriteIterationSummary(project, storage.IterationSummary{
		N:          1,
		MatchIndex: 0,
		Summary:    "A summary that never mentions the raw-only marker at all.",
	}); err != nil {
		t.Fatalf("WriteIterationSummary: %v", err)
	}
	if _, err := eng.Rebuild(ctx, project); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	tool := SearchTool(eng)
	result, err := tool.Handler(ctx, json.RawMessage(
		`{"query": "`+rawMarker+`", "project": "`+project+`"}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	results := result.([]search.SearchResult)
	for _, r := range results {
		if r.SourceType == "iteration_raw" {
			t.Fatalf("default vp_search surfaced the raw row when a summary exists: %+v", r)
		}
	}
}

// TestSearchToolIncludeRawTrueRestoresRawRow proves include_raw:true threads
// from the vp_search JSON params through SearchFilters.IncludeRaw and back
// into searchReady's filter, restoring the raw row alongside the summary row
// that TestSearchToolIncludeRawDefaultHidesRawWhenSummaryExists shows hidden
// by default.
func TestSearchToolIncludeRawTrueRestoresRawRow(t *testing.T) {
	eng, vault := testSearchEngine(t)
	ctx := context.Background()
	const project = "tool-include-raw"
	const rawMarker = "ECHIDNA_TOOL_INCLUDE_RAW_MARKER"

	writeToolIterationsMD(t, vault, project, strings.Join([]string{
		"## Iteration 1 — first",
		"",
		"Body containing " + rawMarker + ".",
		"",
		"---",
		"",
	}, "\n"))

	if err := vault.WriteIterationSummary(project, storage.IterationSummary{
		N:          1,
		MatchIndex: 0,
		Summary:    "Cached summary text for the same entry.",
	}); err != nil {
		t.Fatalf("WriteIterationSummary: %v", err)
	}
	if _, err := eng.Rebuild(ctx, project); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	tool := SearchTool(eng)
	result, err := tool.Handler(ctx, json.RawMessage(
		`{"query": "`+rawMarker+`", "project": "`+project+`", "include_raw": true}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	results := result.([]search.SearchResult)
	var sawRaw, sawSummary bool
	for _, r := range results {
		switch r.SourceType {
		case "iteration_raw":
			sawRaw = true
		case "iteration":
			sawSummary = true
		}
	}
	if !sawRaw {
		t.Errorf("include_raw: true must restore the raw row; results=%+v", results)
	}
	if !sawSummary {
		t.Errorf("include_raw: true must not hide the summary row; results=%+v", results)
	}
}

// TestCrossSearchToolIncludeRawDefaultHidesRawWhenSummaryExists mirrors
// TestSearchToolIncludeRawDefaultHidesRawWhenSummaryExists for
// vp_search_cross_project, proving the same include_raw wiring on the
// cross-project handler (whose SearchFilters literal carries no Project
// field).
func TestCrossSearchToolIncludeRawDefaultHidesRawWhenSummaryExists(t *testing.T) {
	eng, vault := testSearchEngine(t)
	ctx := context.Background()
	const project = "cross-tool-hide-raw-default"
	const rawMarker = "QUOKKA_CROSS_TOOL_RAW_ONLY_MARKER"

	writeToolIterationsMD(t, vault, project, strings.Join([]string{
		"## Iteration 1 — first",
		"",
		"Body containing " + rawMarker + " and nothing the summary will mention.",
		"",
		"---",
		"",
	}, "\n"))

	if err := vault.WriteIterationSummary(project, storage.IterationSummary{
		N:          1,
		MatchIndex: 0,
		Summary:    "A summary that never mentions the raw-only marker at all.",
	}); err != nil {
		t.Fatalf("WriteIterationSummary: %v", err)
	}
	if _, err := eng.Rebuild(ctx, project); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	tool := SearchCrossProjectTool(eng)
	result, err := tool.Handler(ctx, json.RawMessage(`{"query": "`+rawMarker+`"}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	results := result.([]search.SearchResult)
	for _, r := range results {
		if r.SourceType == "iteration_raw" {
			t.Fatalf("default vp_search_cross_project surfaced the raw row when a summary exists: %+v", r)
		}
	}
}

// TestCrossSearchToolIncludeRawTrueRestoresRawRow mirrors
// TestSearchToolIncludeRawTrueRestoresRawRow for vp_search_cross_project.
func TestCrossSearchToolIncludeRawTrueRestoresRawRow(t *testing.T) {
	eng, vault := testSearchEngine(t)
	ctx := context.Background()
	const project = "cross-tool-include-raw"
	const rawMarker = "WOMBAT_CROSS_TOOL_INCLUDE_RAW_MARKER"

	writeToolIterationsMD(t, vault, project, strings.Join([]string{
		"## Iteration 1 — first",
		"",
		"Body containing " + rawMarker + ".",
		"",
		"---",
		"",
	}, "\n"))

	if err := vault.WriteIterationSummary(project, storage.IterationSummary{
		N:          1,
		MatchIndex: 0,
		Summary:    "Cached summary text for the same entry.",
	}); err != nil {
		t.Fatalf("WriteIterationSummary: %v", err)
	}
	if _, err := eng.Rebuild(ctx, project); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	tool := SearchCrossProjectTool(eng)
	result, err := tool.Handler(ctx, json.RawMessage(
		`{"query": "`+rawMarker+`", "include_raw": true}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	results := result.([]search.SearchResult)
	var sawRaw, sawSummary bool
	for _, r := range results {
		switch r.SourceType {
		case "iteration_raw":
			sawRaw = true
		case "iteration":
			sawSummary = true
		}
	}
	if !sawRaw {
		t.Errorf("include_raw: true must restore the raw row; results=%+v", results)
	}
	if !sawSummary {
		t.Errorf("include_raw: true must not hide the summary row; results=%+v", results)
	}
}

// writeToolIterationsMD writes a real iterations.md for project directly
// via the vault's own path resolution (this package cannot reuse
// internal/search's unexported writeIterationsMD test helper), so
// collectIterationCorpus produces a genuine RAW row whose SummaryAvailable
// metadata reflects an actual cached IterationSummary written alongside it.
func writeToolIterationsMD(t *testing.T, v *storage.Vault, project, body string) {
	t.Helper()
	path, err := v.IterationsFile(project)
	if err != nil {
		t.Fatalf("IterationsFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestEngineSearchUnknownProjectError pins Engine.Search's own contract
// directly: an unknown project is a *search.UnknownProjectError, checkable
// with errors.As, returned before ensureIndex/Rebuild ever run.
func TestEngineSearchUnknownProjectError(t *testing.T) {
	eng, _ := testSearchEngine(t)

	_, err := eng.Search(context.Background(), "nothing", search.SearchFilters{Project: "nosuchproject"})
	if err == nil {
		t.Fatal("expected an error for an unknown project, got nil")
	}
	var unk *search.UnknownProjectError
	if !errors.As(err, &unk) {
		t.Fatalf("error is not *search.UnknownProjectError: %v", err)
	}
	if unk.Project != "nosuchproject" {
		t.Errorf("UnknownProjectError.Project = %q, want %q", unk.Project, "nosuchproject")
	}
}
