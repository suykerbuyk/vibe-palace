// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/notesummary"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/summarize"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// TestCaptureThenDrain_SessionSummaryEndToEnd is this feature's true
// end-to-end proof, mirroring integration_iteration_summary_test.go's
// TestEnqueueThenDrain_EndToEnd but for the session-note side, which has a
// DIFFERENT real job-creation path: there is no standalone
// vp_enqueue_session_summary MCP tool. The enqueue happens IMPLICITLY, inside
// internal/capture.WriteSession, whenever a real (non-auto-capture, long
// enough) capture is made. So this test drives the REAL vp_capture_session
// tool handler (tools.CaptureSessionTool(vault, indexer).Handler, called
// directly with JSON-marshaled args exactly as the MCP server's own dispatch
// would, minus the transport) rather than calling
// summarize.EnqueueSessionSummary directly or hand-seeding a queue file.
//
// The chain proven, step by step:
//
//  1. vp_capture_session (real handler) writes a session note whose rendered
//     body clears notesummary.LengthGateBytes, which makes
//     internal/capture.WriteSession's own enqueue-on-write logic drop a real
//     KindSessionNote job into the host-local queue.
//  2. That queue file is confirmed on disk at the exact path
//     internal/summarize's own naming convention produces.
//  3. vp drain summaries (runDrainSummaries, the real CLI entrypoint body,
//     now wired with SessionNote: sessionNoteSummarizer per this phase's
//     cmd_drain.go change) claims and processes it against a real,
//     config-driven notesummary.SessionNoteSummarizer backed by an httptest
//     server.
//  4. The vault-committed note frontmatter now carries SearchSummary/
//     SearchSummaryAt/SearchSummaryModel.
//  5. internal/search's real corpus-collection path (Engine.Rebuild, the same
//     entrypoint `vp search`/`vp_refresh_index` use) is exercised against the
//     now-summarized note and asked to produce a distinct summary row
//     alongside the raw row — internal/search/notes.go's collectNoteCorpus is
//     unexported, so this drives it through Engine.Rebuild + Engine.Search
//     (both exported), the same low-friction technique
//     internal/search/notes_test.go's own newest tests
//     (TestCollectNoteCorpus_SummaryRowDistinctFromRawRow) use to pin the
//     summary-row contract, just reached here through the public surface
//     since this test lives outside package search.
func TestCaptureThenDrain_SessionSummaryEndToEnd(t *testing.T) {
	const cannedSummary = "Captured session summarized end to end for search retrieval."

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{
					"role": "assistant",
					// notesummary/prompt.go's resultJSON: the ONE field it
					// decodes is "search_summary" — confirmed by reading
					// prompt.go before writing this test.
					"content": fmt.Sprintf(`{"search_summary":%q}`, cannedSummary),
				}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	const envName = "VP_TEST_SESSION_SUMMARY_KEY"
	t.Setenv(envName, "sk-session-summary-test")

	projectPath := t.TempDir()
	vaultRoot := t.TempDir()
	const slug = "test-project"

	markerCfg := fmt.Sprintf("vault_path = %q\n\n[project]\nname = %q\n", vaultRoot, slug)
	if err := os.WriteFile(filepath.Join(projectPath, ".vibe-palace.toml"), []byte(markerCfg), 0o644); err != nil {
		t.Fatalf("write project marker config: %v", err)
	}

	vault := storage.NewVault(vaultRoot)
	cfgPath, err := vault.ProjectConfigFile(slug)
	if err != nil {
		t.Fatalf("ProjectConfigFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir project config dir: %v", err)
	}
	summCfg := "[summarization]\n" +
		"enabled = true\n" +
		"provider = \"openai\"\n" +
		"model = \"session-note-test-model\"\n" +
		"api_key_env = \"" + envName + "\"\n" +
		"base_url = \"" + srv.URL + "\"\n" +
		"max_tokens = 512\n" +
		"timeout_seconds = 10\n"
	if err := os.WriteFile(cfgPath, []byte(summCfg), 0o644); err != nil {
		t.Fatalf("write summarization config: %v", err)
	}

	// Step 1: the REAL enqueue path. vp_capture_session's Handler is an
	// mcp.Tool.Handler (func(context.Context, json.RawMessage) (any, error))
	// — called directly here with marshaled args, without spinning up a real
	// MCP server, exactly as internal/tools/session_tools_test.go's own tests
	// do. Tag is deliberately NOT storage.TagAutoCapture (an auto-capture is
	// excluded from summarization enqueue entirely — see session.go's
	// isAutoCapture check) and the summary text is padded well past
	// notesummary.LengthGateBytes once rendered into the note body, mirroring
	// internal/capture/session_test.go's own
	// TestWriteSessionSessionSummaryEnqueueForLongBody fixture
	// (strings.Repeat("word ", 400)).
	longSummary := strings.Repeat("word ", 400)
	tool := tools.CaptureSessionTool(vault, nil)
	params, err := json.Marshal(map[string]any{
		"project": slug,
		"summary": longSummary,
		"tag":     "implementation",
		"cwd":     projectPath,
	})
	if err != nil {
		t.Fatalf("marshal capture args: %v", err)
	}
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("CaptureSessionTool handler: %v", err)
	}

	// The handler's return type (tools.captureSessionResult) is unexported,
	// so — exactly as the real MCP transport would — round-trip it through
	// JSON rather than reaching for an unexported type from this package.
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal handler result: %v", err)
	}
	var captured struct {
		Status    string `json:"status"`
		NotePath  string `json:"note_path"`
		Iteration int    `json:"iteration"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &captured); err != nil {
		t.Fatalf("unmarshal handler result: %v", err)
	}
	if captured.Status != "ok" {
		t.Fatalf("capture status = %q, want %q (raw result: %s)", captured.Status, "ok", raw)
	}

	date := captured.SessionID[:10]
	fp := capture.ParseFingerprint(captured.SessionID)

	// Precondition, asserted explicitly rather than assumed: the rendered
	// body actually clears notesummary.LengthGateBytes. If the fixture ever
	// stopped doing so (e.g. buildSessionBody's rendering changed), this
	// test would otherwise silently validate the wrong thing — mirror the
	// same discipline internal/capture/session_test.go's own length-gate
	// tests use.
	_, body, err := vault.ReadSession(slug, date, fp, captured.Iteration)
	if err != nil {
		t.Fatalf("ReadSession (pre-drain): %v", err)
	}
	if len(body) <= notesummary.LengthGateBytes {
		t.Fatalf("test fixture invariant broken: captured body length %d does not exceed gate %d", len(body), notesummary.LengthGateBytes)
	}

	// Step 2: confirm a real KindSessionNote queue file landed on disk, at
	// the exact location internal/summarize's own naming convention
	// (session-<stem>.json) produces — the REAL enqueue path, not
	// summarize.EnqueueSessionSummary called directly and not a hand-built
	// fixture file.
	stem := storage.SessionStem(date, fp, captured.Iteration)
	queueFile := filepath.Join(summarize.QueueDir(projectPath), fmt.Sprintf("session-%s.json", stem))
	if _, err := os.Stat(queueFile); err != nil {
		t.Fatalf("expected a real queue file at %s, got: %v", queueFile, err)
	}

	// Step 3: the REAL `vp drain summaries` CLI entrypoint body. Like
	// runSummarizeIterations's sibling test, runDrainSummaries takes no
	// injectable Summarizer parameter — it resolves the project's own
	// [summarization] config and builds a real, config-driven
	// summarize.DispatchSummarizer itself (see cmd_drain.go's SessionNote
	// wiring added by this phase).
	var buf bytes.Buffer
	code := runDrainSummaries(projectPath, 10, &buf)
	if code != cli.ExitOK {
		t.Fatalf("runDrainSummaries: code = %d, want ExitOK; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "drained=1") {
		t.Errorf("expected drained=1 in output, got: %s", buf.String())
	}

	// The queue file must be gone: actually claimed and Done'd, not left
	// behind.
	if _, err := os.Stat(queueFile); !os.IsNotExist(err) {
		t.Errorf("queue file still present after drain (job was not processed): stat err = %v", err)
	}

	// Step 4: the vault-committed note now carries a real SearchSummary,
	// with non-empty provenance fields, read through the same
	// vault.ReadSession path internal/search/notes.go's collectNoteCorpus
	// uses to surface it in search results.
	meta, _, err := vault.ReadSession(slug, date, fp, captured.Iteration)
	if err != nil {
		t.Fatalf("ReadSession (post-drain): %v", err)
	}
	if meta.SearchSummary != cannedSummary {
		t.Errorf("SearchSummary = %q, want %q", meta.SearchSummary, cannedSummary)
	}
	if meta.SearchSummaryModel == "" {
		t.Error("SearchSummaryModel is empty, want the configured model")
	}
	if meta.SearchSummaryAt == "" {
		t.Error("SearchSummaryAt is empty, want a timestamp")
	}

	// Step 5: the final, tie-it-together assertion. Drive the REAL
	// internal/search corpus-collection path (Engine.Rebuild, exported) and
	// confirm it now produces a summary row for this note, distinct in both
	// SourceRef and SourceType from the note's raw row — proving the whole
	// pipeline (capture -> enqueue -> drain -> LLM -> cache write -> search
	// index) actually connects end to end, not just that each piece works in
	// isolation.
	//
	// collectNoteCorpus itself is unexported (package search), so this goes
	// through Engine.Rebuild + Engine.Search instead — the same corpus this
	// package's own vp_search/vp_refresh_index surfaces use. A generous
	// Limit (with a fresh, single-note project) is large enough that every
	// indexed row for this project comes back regardless of the mock
	// embedder's hash-based (non-semantic) vector, so this does not depend
	// on query relevance ranking.
	emb := embedder.NewMock(384)
	eng := search.NewEngine(emb, vault, storage.Config{SearchDefaultLimit: 50})
	defer eng.Close()

	stats, err := eng.Rebuild(context.Background(), slug)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	// At least a raw chunk plus the additive summary row.
	if stats.NoteChunks < 2 {
		t.Fatalf("NoteChunks = %d, want at least 2 (raw + summary)", stats.NoteChunks)
	}

	results, err := eng.Search(context.Background(), "session summarized for search", search.SearchFilters{
		Project: slug,
		Limit:   50,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	var summaryRow, rawRow *search.SearchResult
	for i := range results {
		r := results[i]
		if r.Content == cannedSummary {
			summaryRow = &r
		}
	}
	if summaryRow == nil {
		t.Fatalf("expected a search result row whose Content is the canned summary; got %d results: %+v", len(results), results)
	}
	for i := range results {
		r := results[i]
		if r.SourceRef != summaryRow.SourceRef {
			rawRow = &r
			break
		}
	}
	if rawRow == nil {
		t.Fatalf("expected a raw row distinct from the summary row alongside it; got %d results: %+v", len(results), results)
	}

	// The load-bearing assertions: distinct SourceRef AND distinct
	// SourceType, per internal/search/notes.go's noteSummarySourceRef/
	// noteSummarySourceType doc comments (re-derive their exact literal
	// values with `grep -n noteSummarySourceType internal/search/notes.go`
	// rather than trusting this comment — the point pinned here is
	// distinctness, not a specific string).
	if rawRow.SourceRef == summaryRow.SourceRef {
		t.Errorf("raw and summary rows share a SourceRef %q; must be distinct", rawRow.SourceRef)
	}
	if rawRow.SourceType == summaryRow.SourceType {
		t.Errorf("raw and summary rows share a SourceType %q; must be distinct", rawRow.SourceType)
	}
}
