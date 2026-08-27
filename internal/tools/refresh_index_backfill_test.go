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
// re-run: a second refresh must add no drawers rather than doubling the corpus.
//
// 🔴 THE MECHANISM CHANGED AND SO DID THIS TEST. It used to assert
// archives_ingested == 1 on the re-run, because the sweep re-read every archive
// every time and idempotence was carried entirely by content-derived DrawerIDs.
// The ingest ledger now skips an archive it has already ingested, so the re-run
// reports a SKIP and never opens the file — the corpus-level property is
// unchanged, the work behind it is not. The drawer-level dedup is still there
// underneath (AppendDrawers reports its count rather than erroring per
// duplicate) and is what makes a reingest after a crash harmless.
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
	if got := m["archives_found"]; got != 1 {
		t.Errorf("archives_found = %v, want 1 — the archive is still found on a re-run", got)
	}
	if got := m["archives_skipped"]; got != 1 {
		t.Errorf("archives_skipped = %v, want 1 — the ledger already covers it", got)
	}
	if got := m["archives_ingested"]; got != 0 {
		t.Errorf("archives_ingested = %v, want 0 — a skip must not be reported as an ingest", got)
	}
	if got := m["archive_drawers"].(int); got != 0 {
		t.Errorf("archive_drawers = %d on re-run, want 0 — a repeated backfill must not duplicate the corpus", got)
	}
}

// TestRefreshIndexBackfillHonoursCancellation pins the only cancellation point
// on this path. IndexTranscript's chunk/classify/append work takes no ctx at
// all, so before the check at the top of the archive loop a client that gave up
// — an MCP idle timeout, an operator's Ctrl-C — was released while the sweep
// kept decompressing and writing. A cancelled context must produce an ERROR and
// no ingested archive, never a short success whose counts describe a sweep that
// was cut off.
func TestRefreshIndexBackfillHonoursCancellation(t *testing.T) {
	const project = "backfill-cancel"
	transcript := `{"type":"user","text":"cancellation check for the backfill sweep"}` + "\n"
	vault, eng := archivedProjectFixture(t, project, transcript)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tool := RefreshIndexTool(eng, vault)
	params, _ := json.Marshal(refreshIndexParams{Project: project})

	_, err := tool.Handler(ctx, params)
	if err == nil {
		t.Fatal("a cancelled refresh must return an error, not a short success")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("error = %q, want it to name the cancellation", err)
	}

	// Nothing was ingested, so nothing may have been written.
	if _, statErr := os.Stat(filepath.Join(vault.Root, "palace", project, "drawers")); statErr == nil {
		t.Error("cancelled backfill wrote drawers")
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

// --- Ratchet 1 (refresh-index-reports-rebuilt-while-writing-nothing) ---
//
// The task proposed asserting that a "rebuilt" status implies at least one
// observable write under palace/<project>/. The chair RULED that expectation
// WRONG and this test deliberately does not encode it.
//
// Rebuild builds the index IN MEMORY (e.indexes[project]); .vec files are
// written only as a cache-MISS side effect. So a rebuild of a project whose
// vectors are all cached correctly writes nothing, and the control case the
// task recorded against `dotfiles` — healthy store, working index, zero
// filesystem effect — was legitimate behaviour rather than the defect.
//
// What the status must actually mean is narrower and is what is pinned here:
// "rebuilt" is claimed ONLY when there was something to refresh, the refusal
// fires when there was not, and the counts that make the claim falsifiable are
// present on the success path. That is a status that CAN fail, which was the
// task's real complaint.

// TestRefreshIndexRebuiltOnlyWhenThereWasSomethingToRefresh is the ratchet: a
// hardcoded `{"status":"rebuilt"}` cannot satisfy both halves at once.
func TestRefreshIndexRebuiltOnlyWhenThereWasSomethingToRefresh(t *testing.T) {
	t.Run("nothing to refresh refuses instead of reporting rebuilt", func(t *testing.T) {
		vault := storage.NewVault(t.TempDir())
		cfg := storage.Config{SearchDefaultLimit: 10}
		eng := search.NewEngine(embedder.NewMock(384), vault, cfg)
		t.Cleanup(func() { eng.Close() })

		tool := RefreshIndexTool(eng, vault)
		params, _ := json.Marshal(refreshIndexParams{Project: "no-such-corpus"})
		res, err := tool.Handler(context.Background(), params)
		if err == nil {
			t.Fatalf("want a refusal when there is nothing to refresh, got result %v", res)
		}
		if res != nil {
			t.Errorf("a refusal must not also return a result: %v", res)
		}
	})

	t.Run("something to refresh reports rebuilt with falsifiable counts", func(t *testing.T) {
		const project = "ratchet-proj"
		vault, eng := archivedProjectFixture(t, project,
			`{"type":"user","message":{"content":"index the ratchet corpus"}}`+"\n")

		tool := RefreshIndexTool(eng, vault)
		params, _ := json.Marshal(refreshIndexParams{Project: project})
		res, err := tool.Handler(context.Background(), params)
		if err != nil {
			t.Fatalf("refresh with an archive to ingest must succeed: %v", err)
		}
		m, ok := res.(map[string]any)
		if !ok {
			t.Fatalf("result is %T, want map[string]any", res)
		}
		if m["status"] != "rebuilt" {
			t.Errorf("status = %v, want rebuilt", m["status"])
		}

		// The counts are what make "rebuilt" falsifiable. A status with no
		// counts behind it is the unfalsifiable success this task is about.
		for _, k := range []string{
			"drawers", "iteration_chunks", "indexed", "embedded", "cache_hits",
			"reaped", "archives_found", "archives_ingested", "archive_drawers",
			"had_palace_store",
		} {
			if _, present := m[k]; !present {
				t.Errorf("success payload is missing %q — the status is unfalsifiable without it", k)
			}
		}

		// And at least one of them must be non-zero, or "rebuilt" is describing
		// nothing. NOT a filesystem assertion: this counts work done, which is
		// the claim the status actually makes.
		if m["indexed"].(int) == 0 && m["archives_ingested"].(int) == 0 {
			t.Errorf("reported rebuilt with indexed=0 and archives_ingested=0: %v", m)
		}
	})
}

// TestRefreshIndexCacheHitRebuildIsLegitimate is the CONTROL CASE, recorded as
// a test so the ruling cannot be re-litigated from the symptom alone.
//
// A second refresh re-embeds the same corpus, so every vector is a cache hit
// and the pass writes no new .vec file. That is a legitimate "rebuilt" —
// indexed > 0 with embedded == 0 — and any future ratchet that demands a
// filesystem write per rebuild would red this and be WRONG to.
func TestRefreshIndexCacheHitRebuildIsLegitimate(t *testing.T) {
	const project = "control-proj"
	vault, eng := archivedProjectFixture(t, project,
		`{"type":"user","message":{"content":"control corpus for the cache-hit ruling"}}`+"\n")

	tool := RefreshIndexTool(eng, vault)
	params, _ := json.Marshal(refreshIndexParams{Project: project})

	if _, err := tool.Handler(context.Background(), params); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("second refresh must still succeed — the corpus exists: %v", err)
	}
	m := res.(map[string]any)
	if m["status"] != "rebuilt" {
		t.Fatalf("status = %v, want rebuilt on the cache-hit pass", m["status"])
	}
	if m["indexed"].(int) == 0 {
		t.Fatalf("control case is degenerate: indexed = 0, so nothing was re-embedded")
	}
	// The point of the control: embedded may legitimately be 0 here. Assert the
	// rebuild is still reported as real work, via the count that describes it.
	if m["cache_hits"].(int) == 0 && m["embedded"].(int) == 0 {
		t.Errorf("neither cache_hits nor embedded is set: %v", m)
	}
}

// TestRefreshIndexIsRegisteredMutating pins the flag itself. The tool writes on
// three paths — AppendDrawer via the backfill, .vec files on a cache miss, and
// creating palace/<slug>/ — so a stale binary must refuse it, and it must never
// be served on a read-only `vp mcp serve`.
func TestRefreshIndexIsRegisteredMutating(t *testing.T) {
	if tool := RefreshIndexTool(nil, nil); !tool.Mutating {
		t.Error("vp_refresh_index must be registered Mutating: it writes drawers, .vec cache files, and can create palace/<slug>/")
	}
	for _, n := range ReadOnlyServeToolNames {
		if n == "vp_refresh_index" {
			t.Error("vp_refresh_index must not be on the read-only serve allow-list: it is a writer")
		}
	}
}

// --- the archive ingest ledger (run two) ------------------------------------

// readLedger returns the raw ledger rows for a project, or nil when the file
// does not exist.
func readLedger(t *testing.T, vault *storage.Vault, project string) []storage.IngestedArchive {
	t.Helper()
	path, err := vault.IngestedArchivesFile(project)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read ledger: %v", err)
	}
	var rows []storage.IngestedArchive
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec storage.IngestedArchive
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("ledger line %q does not parse: %v", line, err)
		}
		rows = append(rows, rec)
	}
	return rows
}

// fixtureManifest returns the single manifest of a fixture project.
func fixtureManifest(t *testing.T, vault *storage.Vault, project string) (*archive.Manifest, string) {
	t.Helper()
	entries, err := archive.ListEntries(vault.Root, project)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("fixture holds %d archives, want exactly 1", len(entries))
	}
	return entries[0].Manifest, entries[0].ManifestPath
}

// TestRefreshIndexBackfillWritesLedgerRow: a successful ingest must record the
// archive's CONTENT HASH, which is the only key that survives a session being
// re-archived with new bytes.
func TestRefreshIndexBackfillWritesLedgerRow(t *testing.T) {
	const project = "ledger-write"
	transcript := `{"type":"user","text":"the ledger records the content hash"}` + "\n"
	vault, eng := archivedProjectFixture(t, project, transcript)

	man, _ := fixtureManifest(t, vault, project)
	if man.SourceSHA256 == "" {
		t.Fatal("fixture manifest carries no source_sha256")
	}

	tool := RefreshIndexTool(eng, vault)
	params, _ := json.Marshal(refreshIndexParams{Project: project})
	if _, err := tool.Handler(context.Background(), params); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	rows := readLedger(t, vault, project)
	if len(rows) != 1 {
		t.Fatalf("ledger holds %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].SourceSHA256 != man.SourceSHA256 {
		t.Errorf("ledger row keyed on %q, want the manifest's source_sha256 %q",
			rows[0].SourceSHA256, man.SourceSHA256)
	}
	if rows[0].SessionID != man.SessionID {
		t.Errorf("ledger row session_id = %q, want %q", rows[0].SessionID, man.SessionID)
	}
}

// TestRefreshIndexBackfillSkipsWithoutExtracting is the point of this unit, and
// the assertion is deliberately NOT "the second run added no drawers" — that
// was already true before the ledger, because the drawers were already filed.
// It proves the archive was never OPENED: the .jsonl.zst is deleted between the
// runs, so a second run that still extracts reports an extract failure, and one
// that consults the ledger reports a skip and never touches the file.
func TestRefreshIndexBackfillSkipsWithoutExtracting(t *testing.T) {
	const project = "ledger-skip"
	transcript := `{"type":"user","text":"run two must not decompress this archive"}` + "\n"
	vault, eng := archivedProjectFixture(t, project, transcript)

	tool := RefreshIndexTool(eng, vault)
	params, _ := json.Marshal(refreshIndexParams{Project: project})

	first, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	fm := first.(map[string]any)
	if got := fm["archives_ingested"]; got != 1 {
		t.Fatalf("first run archives_ingested = %v, want 1", got)
	}
	if got := fm["archives_skipped"]; got != 0 {
		t.Fatalf("first run archives_skipped = %v, want 0", got)
	}

	// Remove the archive body. The manifest stays, so the entry is still FOUND;
	// only reading it is now impossible.
	_, manifestPath := fixtureManifest(t, vault, project)
	archivePath := strings.TrimSuffix(manifestPath, ".manifest.json") + ".jsonl.zst"
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("remove archive body: %v", err)
	}

	second, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	sm := second.(map[string]any)
	if got := sm["archives_found"]; got != 1 {
		t.Errorf("second run archives_found = %v, want 1", got)
	}
	if got := sm["archives_skipped"]; got != 1 {
		t.Errorf("second run archives_skipped = %v, want 1 — the ledger must skip it", got)
	}
	if got := sm["archives_ingested"]; got != 0 {
		t.Errorf("second run archives_ingested = %v, want 0 — a skip is not an ingest", got)
	}
	if got := sm["archive_drawers"].(int); got != 0 {
		t.Errorf("second run archive_drawers = %d, want 0", got)
	}
	// The proof: extracting a deleted file cannot succeed, so an empty failure
	// list means no extract was attempted.
	if fails, _ := sm["archive_failures"].([]string); len(fails) != 0 {
		t.Errorf("second run reported %v — it opened the archive instead of skipping it", fails)
	}
	if rows := readLedger(t, vault, project); len(rows) != 1 {
		t.Errorf("ledger holds %d rows after two runs, want 1", len(rows))
	}
}

// TestRefreshIndexBackfillIngestsArchiveWithNoSHA: an archive whose manifest
// carries no source_sha256 cannot be keyed, so it must be ingested every run —
// and must NOT leave a row keyed on "", which would grow the ledger while
// skipping nothing.
func TestRefreshIndexBackfillIngestsArchiveWithNoSHA(t *testing.T) {
	const project = "ledger-nosha"
	transcript := `{"type":"user","text":"this manifest loses its hash"}` + "\n"
	vault, eng := archivedProjectFixture(t, project, transcript)

	// Strip the hash from the manifest on disk, keeping every other field.
	_, manifestPath := fixtureManifest(t, vault, project)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["source_sha256"] = ""
	patched, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, patched, 0o644); err != nil {
		t.Fatal(err)
	}

	tool := RefreshIndexTool(eng, vault)
	params, _ := json.Marshal(refreshIndexParams{Project: project})

	first, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if got := first.(map[string]any)["archives_ingested"]; got != 1 {
		t.Fatalf("archives_ingested = %v, want 1 — an unkeyable archive is still ingested", got)
	}
	if rows := readLedger(t, vault, project); len(rows) != 0 {
		t.Fatalf("ledger holds %d rows, want 0 — an empty key must never be written: %+v", len(rows), rows)
	}

	// And it is ingested AGAIN next run, rather than silently skipped.
	second, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	sm := second.(map[string]any)
	if got := sm["archives_ingested"]; got != 1 {
		t.Errorf("second run archives_ingested = %v, want 1 — nothing can skip it", got)
	}
	if got := sm["archives_skipped"]; got != 0 {
		t.Errorf("second run archives_skipped = %v, want 0", got)
	}
}

// failingEmbedder makes capture.Indexer.IndexTranscript fail after the drawers
// are written, which is the realistic partial-ingest shape.
type failingEmbedder struct{ embedder.Embedder }

func (failingEmbedder) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("embedder is down")
}

// TestRefreshIndexBackfillDoesNotRecordAFailedIngest: the ledger is a claim
// that an archive was fully ingested. Recording one for a failed IndexTranscript
// would strand its content permanently — every later run would skip an archive
// that never finished.
func TestRefreshIndexBackfillDoesNotRecordAFailedIngest(t *testing.T) {
	const project = "ledger-fail"
	transcript := `{"type":"user","text":"the embedder fails partway through this ingest"}` + "\n"
	vault, _ := archivedProjectFixture(t, project, transcript)

	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := search.NewEngine(failingEmbedder{embedder.NewMock(384)}, vault, cfg)
	t.Cleanup(func() { eng.Close() })

	// The sweep is called directly rather than through the tool: a broken
	// embedder also fails engine.Rebuild, and the handler would return that
	// error instead of the backfill's own accounting, which is the thing under
	// test here.
	bs, err := backfillFromArchives(context.Background(), eng, vault, project)
	if err != nil {
		t.Fatalf("backfillFromArchives: %v", err)
	}
	if bs.ArchivesFound != 1 {
		t.Fatalf("ArchivesFound = %d, want 1", bs.ArchivesFound)
	}
	if bs.ArchivesIngested != 0 {
		t.Errorf("ArchivesIngested = %d, want 0 — the ingest failed", bs.ArchivesIngested)
	}
	if len(bs.Failures) != 1 || !strings.Contains(bs.Failures[0], "index:") {
		t.Errorf("Failures = %v, want one index failure", bs.Failures)
	}
	if rows := readLedger(t, vault, project); len(rows) != 0 {
		t.Fatalf("ledger holds %d rows after a failed ingest, want 0: %+v", len(rows), rows)
	}

	// And the archive is genuinely retried once the embedder recovers, rather
	// than stranded by a row that should never have been written.
	healthy := search.NewEngine(embedder.NewMock(384), vault, cfg)
	t.Cleanup(func() { healthy.Close() })
	retry, err := backfillFromArchives(context.Background(), healthy, vault, project)
	if err != nil {
		t.Fatalf("retry backfillFromArchives: %v", err)
	}
	if retry.ArchivesIngested != 1 {
		t.Errorf("retry ArchivesIngested = %d, want 1 — a failed ingest must not be skipped later", retry.ArchivesIngested)
	}
	if rows := readLedger(t, vault, project); len(rows) != 1 {
		t.Errorf("ledger holds %d rows after the retry, want 1", len(rows))
	}
}
