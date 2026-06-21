// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/enrichment"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// mockCompleter is a tiny in-test llm.Completer. When err is non-nil,
// Complete returns it; otherwise it returns the canned JSON in resp.
type mockCompleter struct {
	resp string
	err  error
}

func (m mockCompleter) Complete(ctx context.Context, system, user string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.resp, nil
}

func (m mockCompleter) Name() string { return "mock" }

// enrichTranscript is a minimal valid Claude Code JSONL transcript so
// ExtractPromptInput yields a non-empty PromptInput.
const enrichTranscript = `{"type":"user","message":{"role":"user","content":"please implement the feature"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"},{"type":"tool_use","name":"Edit","input":{"file_path":"internal/foo.go"}}]}}
`

const cannedEnrichment = `{"summary":"LLM refined summary","decisions":["LLM decision one"],"open_threads":["LLM thread one"],"tag":"refactor"}`

// emptyEnrichment is a well-formed but all-empty enrichment response: blank
// summary, empty arrays, and a tag that fails validation. applyEnrichment must
// treat it as a miss (nothing applied, no provenance, plain body).
const emptyEnrichment = `{"summary":"","decisions":[],"open_threads":[],"tag":"bogus"}`

func TestWriteSessionHappyPath(t *testing.T) {
	vault := testVault(t)

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj",
		Summary: "Implemented feature X",
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	if result.Status != "ok" {
		t.Errorf("status = %q, want %q", result.Status, "ok")
	}
	if result.Project != "test-proj" {
		t.Errorf("project = %q, want %q", result.Project, "test-proj")
	}
	if result.SessionID == "" {
		t.Error("session_id is empty")
	}
	if result.Iteration < 1 {
		t.Errorf("iteration = %d, want >= 1", result.Iteration)
	}
}

func TestWriteSessionTitleDefaulting(t *testing.T) {
	vault := testVault(t)

	// No title: should default to summary (truncated at 80).
	long := "This is a very long summary that exceeds eighty characters and should be truncated by the title defaulting logic in WriteSession"
	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj",
		Summary: long,
	})
	if err != nil {
		t.Fatal(err)
	}

	meta, _, err := vault.ReadSession("test-proj", result.SessionID[:10], result.Iteration)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Title) > 80 {
		t.Errorf("title length = %d, want <= 80", len(meta.Title))
	}
}

func TestWriteSessionWithFriction(t *testing.T) {
	vault := testVault(t)

	// Transcript with friction signals: corrections and rework.
	transcript := "wrong wrong wrong undo revert go back start over try again scratch that"
	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "Session with friction",
		Transcript: transcript,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FrictionScore == 0 {
		t.Error("expected non-zero friction score for transcript with friction signals")
	}
}

func TestWriteSessionWithArchiveLink(t *testing.T) {
	vault := testVault(t)

	// Seed a real archive.
	srcPath := filepath.Join(t.TempDir(), "src.jsonl")
	jsonl := `{"type":"permission-mode","permissionMode":"bypassPermissions","sessionId":"link-session"}
{"type":"user","message":{"role":"user","content":"hi"}}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-6","content":"hello"}}
`
	if err := os.WriteFile(srcPath, []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}
	archRes, err := archive.Create(archive.CreateOptions{
		Adapter:     archive.ClaudeCodeAdapterName,
		SessionID:   "link-session",
		SourcePath:  srcPath,
		VaultRoot:   vault.Root,
		ProjectSlug: "test-proj",
	})
	if err != nil {
		t.Fatalf("seed archive: %v", err)
	}

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:          "test-proj",
		Summary:          "Session linked to archive",
		ArchiveSessionID: "link-session",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Session frontmatter should carry archive: field.
	meta, _, err := vault.ReadSession("test-proj", result.SessionID[:10], result.Iteration)
	if err != nil {
		t.Fatal(err)
	}
	wantArchive := archive.VaultRelPath(vault.Root, archRes.ManifestPath)
	if meta.Archive != wantArchive {
		t.Errorf("session.archive = %q, want %q", meta.Archive, wantArchive)
	}

	// Manifest should have the back-link.
	m, err := archive.ReadManifest(archRes.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.VaultRelSessionNote == "" {
		t.Error("manifest vault_rel_session_note was not updated")
	}
}

func TestWriteSessionNeedsIndexingTrue(t *testing.T) {
	vault := testVault(t)
	indexer := NewIndexer(vault, nil, nil, storage.Config{})

	result, err := WriteSession(context.Background(), vault, indexer, SessionParams{
		Project:       "test-proj",
		Summary:       "Session with deferred indexing",
		Transcript:    "Some transcript text that would normally be indexed.",
		NeedsIndexing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" {
		t.Errorf("status = %q, want %q", result.Status, "ok")
	}
	// With NeedsIndexing=true, no drawers should be created.
	// (The indexer runs but writes no drawers only because embedder is nil;
	// the key assertion is that WriteSession does not error and returns ok.)
}

func TestWriteSessionNeedsIndexingFalse(t *testing.T) {
	vault := testVault(t)
	indexer := NewIndexer(vault, nil, nil, storage.Config{})

	result, err := WriteSession(context.Background(), vault, indexer, SessionParams{
		Project:       "test-proj",
		Summary:       "Session with immediate indexing",
		Transcript:    "Some transcript text that should be indexed right away.",
		NeedsIndexing: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" {
		t.Errorf("status = %q, want %q", result.Status, "ok")
	}
}

func TestWriteSessionMissingProject(t *testing.T) {
	vault := testVault(t)

	_, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Summary: "No project specified",
	})
	if err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestWriteSessionMissingSummary(t *testing.T) {
	vault := testVault(t)

	_, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj",
	})
	if err == nil {
		t.Fatal("expected error for missing summary")
	}
}

func TestWriteSessionAllFields(t *testing.T) {
	vault := testVault(t)

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:      "test-proj",
		Summary:      "Full session with all metadata",
		Title:        "Test Session",
		Tag:          "implementation",
		Model:        "claude-opus-4-6",
		Decisions:    []string{"Use brute-force search", "Skip HNSW"},
		FilesChanged: []string{"internal/capture/session.go"},
		OpenThreads:  []string{"Phase 6 next"},
	})
	if err != nil {
		t.Fatal(err)
	}

	meta, body, err := vault.ReadSession("test-proj", result.SessionID[:10], result.Iteration)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "Test Session" {
		t.Errorf("title = %q, want %q", meta.Title, "Test Session")
	}
	if meta.Tag != "implementation" {
		t.Errorf("tag = %q, want %q", meta.Tag, "implementation")
	}
	if meta.Model != "claude-opus-4-6" {
		t.Errorf("model = %q, want %q", meta.Model, "claude-opus-4-6")
	}
	if len(meta.Decisions) != 2 {
		t.Errorf("decisions count = %d, want 2", len(meta.Decisions))
	}
	if len(meta.FilesChanged) != 1 {
		t.Errorf("files_changed count = %d, want 1", len(meta.FilesChanged))
	}
	if len(meta.OpenThreads) != 1 {
		t.Errorf("open_threads count = %d, want 1", len(meta.OpenThreads))
	}
	// Body should contain the summary section.
	if body == "" {
		t.Error("body is empty")
	}
}

func TestWriteSessionEnrichmentSuccess(t *testing.T) {
	vault := testVault(t)

	enr := enrichment.NewEnricher(mockCompleter{resp: cannedEnrichment}, "test-model", 5*time.Second, "")
	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "plain heuristic summary",
		Tag:        "implementation",
		Decisions:  []string{"plain decision"},
		Transcript: enrichTranscript,
		Enricher:   enr,
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	meta, body, err := vault.ReadSession("test-proj", result.SessionID[:10], result.Iteration)
	if err != nil {
		t.Fatal(err)
	}

	// LLM values, not the plain inputs.
	if meta.Summary != "LLM refined summary" {
		t.Errorf("summary = %q, want LLM-refined value", meta.Summary)
	}
	if len(meta.Decisions) != 1 || meta.Decisions[0] != "LLM decision one" {
		t.Errorf("decisions = %v, want LLM decision", meta.Decisions)
	}
	if len(meta.OpenThreads) != 1 || meta.OpenThreads[0] != "LLM thread one" {
		t.Errorf("open_threads = %v, want LLM thread", meta.OpenThreads)
	}
	if meta.Tag != "refactor" {
		t.Errorf("tag = %q, want refactor", meta.Tag)
	}

	// Provenance frontmatter.
	if meta.EnrichedBy != "test-model" {
		t.Errorf("enriched_by = %q, want test-model", meta.EnrichedBy)
	}
	if meta.EnrichedAt == "" {
		t.Error("enriched_at is empty")
	}
	if _, perr := time.Parse(time.RFC3339, meta.EnrichedAt); perr != nil {
		t.Errorf("enriched_at %q does not parse as RFC3339: %v", meta.EnrichedAt, perr)
	}

	// Body fence + footer.
	if !strings.Contains(body, "<!-- enriched -->") {
		t.Errorf("body missing enriched fence:\n%s", body)
	}
	if !strings.Contains(body, "<!-- /enriched -->") {
		t.Errorf("body missing closing fence:\n%s", body)
	}
	if !strings.Contains(body, "*enriched by test-model*") {
		t.Errorf("body missing enriched footer:\n%s", body)
	}
}

func TestWriteSessionEnrichmentFailureFallsBack(t *testing.T) {
	vault := testVault(t)

	enr := enrichment.NewEnricher(mockCompleter{err: fmt.Errorf("boom")}, "test-model", 5*time.Second, "")
	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "plain heuristic summary",
		Transcript: enrichTranscript,
		Enricher:   enr,
	})
	// Capture must never fail because enrichment failed.
	if err != nil {
		t.Fatalf("WriteSession should not fail on enrichment error: %v", err)
	}

	meta, body, err := vault.ReadSession("test-proj", result.SessionID[:10], result.Iteration)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Summary != "plain heuristic summary" {
		t.Errorf("summary = %q, want plain heuristic summary", meta.Summary)
	}
	if meta.EnrichedBy != "" {
		t.Errorf("enriched_by = %q, want empty on failure", meta.EnrichedBy)
	}
	if strings.Contains(body, "<!-- enriched -->") {
		t.Errorf("body should not be fenced on enrichment failure:\n%s", body)
	}
}

// TestWriteSessionEmptyEnrichmentResultEnqueues covers the empty-result miss:
// a non-nil but all-empty enrichment.Result (blank summary, empty arrays,
// invalid tag) applies nothing, so the note is written PLAIN (no fence, no
// provenance) and the job is enqueued for a later drain when CWD is set.
func TestWriteSessionEmptyEnrichmentResultEnqueues(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()

	enr := enrichment.NewEnricher(mockCompleter{resp: emptyEnrichment}, "test-model", 5*time.Second, "")
	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "plain heuristic summary",
		Transcript: enrichTranscript,
		Enricher:   enr,
		CWD:        cwd,
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	meta, body, err := vault.ReadSession("test-proj", result.SessionID[:10], result.Iteration)
	if err != nil {
		t.Fatal(err)
	}

	// Plain heuristic summary survives; no LLM values applied.
	if meta.Summary != "plain heuristic summary" {
		t.Errorf("summary = %q, want plain heuristic summary", meta.Summary)
	}
	// No provenance frontmatter on a miss.
	if meta.EnrichedBy != "" {
		t.Errorf("enriched_by = %q, want empty on empty-result miss", meta.EnrichedBy)
	}
	if meta.EnrichedAt != "" {
		t.Errorf("enriched_at = %q, want empty on empty-result miss", meta.EnrichedAt)
	}
	// No enriched fence in the body.
	if strings.Contains(body, "<!-- enriched -->") {
		t.Errorf("body should be plain on empty-result miss:\n%s", body)
	}

	// The job must be enqueued for a later drain.
	dir := filepath.Join(cwd, ".vibe-palace", "enrichment-queue")
	matches, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 queued item under %s, found %d: %v", dir, len(matches), matches)
	}
}

func TestWriteSessionNilEnricherUnchanged(t *testing.T) {
	vault := testVault(t)

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "plain heuristic summary",
		Transcript: enrichTranscript,
		// Enricher nil: enrichment disabled.
	})
	if err != nil {
		t.Fatal(err)
	}
	meta, body, err := vault.ReadSession("test-proj", result.SessionID[:10], result.Iteration)
	if err != nil {
		t.Fatal(err)
	}
	if meta.EnrichedBy != "" || meta.EnrichedAt != "" {
		t.Errorf("nil enricher set provenance: by=%q at=%q", meta.EnrichedBy, meta.EnrichedAt)
	}
	if strings.Contains(body, "<!-- enriched -->") {
		t.Errorf("nil enricher produced fenced body:\n%s", body)
	}
}

func TestBuildSessionBodyFenceIdempotent(t *testing.T) {
	p := SessionParams{
		Summary:     "a summary",
		Decisions:   []string{"d1"},
		OpenThreads: []string{"t1"},
	}

	plain := buildSessionBody(p)

	// Adding EnrichedBy wraps the plain body verbatim in the fence.
	p.EnrichedBy = "test-model"
	fenced := buildSessionBody(p)

	if !strings.Contains(fenced, plain) {
		t.Errorf("fenced body does not contain the plain body verbatim\nplain:\n%s\nfenced:\n%s", plain, fenced)
	}
	want := "<!-- enriched -->\n" + plain + "<!-- /enriched -->\n\n*enriched by test-model*\n"
	if fenced != want {
		t.Errorf("fenced body mismatch\ngot:\n%q\nwant:\n%q", fenced, want)
	}

	// Non-enriched path must be byte-identical to legacy assembly.
	p.EnrichedBy = ""
	if got := buildSessionBody(p); got != plain {
		t.Errorf("non-enriched body changed: got %q want %q", got, plain)
	}
}

func TestParseIteration(t *testing.T) {
	tests := []struct {
		id   string
		want int
	}{
		{"2026-04-15-01", 1},
		{"2026-04-15-03", 3},
		{"2026-04-15-12", 12},
		{"bad", 0},
		{"", 0},
	}
	for _, tt := range tests {
		got := ParseIteration(tt.id)
		if got != tt.want {
			t.Errorf("ParseIteration(%q) = %d, want %d", tt.id, got, tt.want)
		}
	}
}
