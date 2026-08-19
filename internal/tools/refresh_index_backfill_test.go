// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// archivedProjectFixture builds a vault holding one project that has a
// TRANSCRIPT ARCHIVE and NO palace store — the state the vault audit reports as
// "history under Projects/ but unsearchable", whose prescribed remedy is
// index-or-delete.
//
// The archive is produced by archive.Create through the inline adapter, so the
// on-disk pair (manifest + zstd stream) is the real format the backfill reads,
// not a hand-rolled approximation of it.
func archivedProjectFixture(t *testing.T, project, transcript string) (*storage.Vault, *search.Engine) {
	t.Helper()
	vault := storage.NewVault(t.TempDir())

	if _, err := archive.Create(archive.CreateOptions{
		Adapter:       archive.InlineAdapterName,
		SessionID:     "backfill-fixture-session",
		SourceContent: []byte(transcript),
		VaultRoot:     vault.Root,
		ProjectSlug:   project,
	}); err != nil {
		t.Fatalf("create fixture archive: %v", err)
	}

	// Precondition: no palace store. If one existed the backfill would be
	// indistinguishable from an ordinary re-embed and this fixture would be
	// measuring nothing.
	if _, err := os.Stat(filepath.Join(vault.Root, "palace", project)); err == nil {
		t.Fatalf("fixture is degenerate: palace/%s already exists", project)
	}

	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := search.NewEngine(embedder.NewMock(384), vault, cfg)
	t.Cleanup(func() { eng.Close() })
	return vault, eng
}

// TestRefreshIndexBackfillsArchivedTranscriptsIntoSearch is the acceptance gate
// for Piece 2 of refresh-index-reports-rebuilt-for-a-project-it-never-touched.
//
// Piece 1 made the tool stop LYING about a no-op. It still could not do the
// thing the audit prescribes: drawers were written in exactly one place —
// capture.Indexer.IndexTranscript at capture time — so a project whose sessions
// were captured before that, or whose store was lost, had content in
// Projects/<slug>/transcripts/ and no path back into the index.
//
// 🔴 THE ASSERTION IS A QUERY, NOT A COUNT. "drawers > 0" would pass on drawers
// that no search can reach, which is the same shape of false success this whole
// task exists to remove. This searches for content that exists ONLY inside the
// archived transcript and asserts it comes back attributed to that archive.
func TestRefreshIndexBackfillsArchivedTranscriptsIntoSearch(t *testing.T) {
	const project = "backfill-proj"
	// A line that appears nowhere except inside the archive.
	const needle = `{"type":"user","text":"the quokka telemetry pipeline drops frames on ingest"}`
	transcript := `{"type":"system","text":"session start"}` + "\n" + needle + "\n" +
		`{"type":"assistant","text":"acknowledged"}` + "\n"

	vault, eng := archivedProjectFixture(t, project, transcript)

	// Control: the term is unreachable BEFORE the refresh. Without this the
	// test cannot tell a working backfill from content that was already there.
	before, err := eng.Search(context.Background(), needle, search.SearchFilters{Project: project, Limit: 5})
	if err != nil {
		t.Fatalf("pre-search: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("fixture is degenerate: %d results before any refresh", len(before))
	}

	tool := RefreshIndexTool(eng, vault)
	params, _ := json.Marshal(refreshIndexParams{Project: project})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("refresh index refused a project that HAS archives to ingest: %v", err)
	}

	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", res)
	}
	if got := m["archives_ingested"]; got != 1 {
		t.Errorf("archives_ingested = %v, want 1 (result: %v)", got, m)
	}
	if fails, _ := m["archive_failures"].([]string); len(fails) != 0 {
		t.Errorf("archive_failures = %v, want none", fails)
	}

	// THE ACCEPTANCE: the archived content is now reachable by search.
	after, err := eng.Search(context.Background(), needle, search.SearchFilters{Project: project, Limit: 5})
	if err != nil {
		t.Fatalf("post-search: %v", err)
	}
	if len(after) == 0 {
		t.Fatal("archived transcript content is still unsearchable after vp_refresh_index — the backfill did not reach the index")
	}
	var found *search.SearchResult
	for i := range after {
		if strings.Contains(after[i].Content, "quokka telemetry pipeline") {
			found = &after[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("search returned %d results, none carrying the archived content: %+v", len(after), after)
	}
	if found.SourceRef != "backfill-fixture-session" {
		t.Errorf("SourceRef = %q, want the archived session id — a backfilled drawer must be attributable to the archive it came from", found.SourceRef)
	}
	if found.SourceType != "session" {
		t.Errorf("SourceType = %q, want \"session\" — backfilled drawers must be indistinguishable from capture-time ones, since the input bytes are identical", found.SourceType)
	}
}

// TestRefreshIndexBackfillIsIdempotent pins the property that makes this safe to
// re-run: storage.DrawerID is content-derived and AppendDrawer's "already
// exists" is skipped, so a second refresh must add no drawers rather than
// doubling the corpus.
func TestRefreshIndexBackfillIsIdempotent(t *testing.T) {
	const project = "backfill-idem"
	transcript := `{"type":"user","text":"idempotence check for the backfill sweep"}` + "\n"
	vault, eng := archivedProjectFixture(t, project, transcript)

	tool := RefreshIndexTool(eng, vault)
	params, _ := json.Marshal(refreshIndexParams{Project: project})

	first, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	firstDrawers := first.(map[string]any)["archive_drawers"].(int)
	if firstDrawers == 0 {
		t.Fatal("first refresh wrote no drawers from the archive")
	}

	second, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	m := second.(map[string]any)
	if got := m["archives_ingested"]; got != 1 {
		t.Errorf("archives_ingested = %v, want 1 — the archive is still found and read on a re-run", got)
	}
	if got := m["archive_drawers"].(int); got != 0 {
		t.Errorf("archive_drawers = %d on re-run, want 0 — a repeated backfill must not duplicate the corpus", got)
	}
}

// TestRefreshIndexStillRefusesWhenThereIsNothingToBackfill pins that Piece 1's
// refusal survives Piece 2. A project with no store AND no archives has nothing
// anywhere, and reporting success for it is the original defect.
func TestRefreshIndexStillRefusesWhenThereIsNothingToBackfill(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := search.NewEngine(embedder.NewMock(384), vault, cfg)
	t.Cleanup(func() { eng.Close() })

	tool := RefreshIndexTool(eng, vault)
	params, _ := json.Marshal(refreshIndexParams{Project: "empty-proj"})
	_, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatal("expected a refusal for a project with no store and no archives")
	}
	if !strings.Contains(err.Error(), "nothing to refresh") {
		t.Errorf("refusal must say nothing to refresh, got %q", err)
	}
	// The message must no longer claim a backfill does not exist — it does now.
	if strings.Contains(err.Error(), "no path that adds them after the fact") {
		t.Errorf("refusal still asserts no backfill path exists, which this unit made false: %q", err)
	}
}
