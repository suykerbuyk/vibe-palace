// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// seedSession writes a session file directly so tests don't depend on
// CaptureSessionTool. It returns the host-scoped session ID the writer minted.
func seedSession(t *testing.T, vault *storage.Vault, project string, meta storage.SessionMeta, body string) string {
	t.Helper()
	id, err := vault.WriteSession(project, meta, body)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return id
}

func seedTestSessions(t *testing.T, vault *storage.Vault) {
	t.Helper()
	seedSession(t, vault, "test-proj", storage.SessionMeta{
		Date:          "2026-04-01",
		Title:         "Implement chunking engine",
		Summary:       "Built sliding window chunker with sentence boundaries.",
		Tag:           "implementation",
		FrictionScore: 15,
		Decisions:     []string{"Use sliding window", "800 char chunks"},
		FilesChanged:  []string{"internal/capture/chunker.go"},
		OpenThreads:   []string{"Phase 5 Task 5.3 next"},
	}, "\n## Summary\nBuilt chunker.\n")

	seedSession(t, vault, "test-proj", storage.SessionMeta{
		Date:          "2026-04-02",
		Title:         "Debug friction scoring",
		Summary:       "Fixed friction score calculation for edge cases.",
		Tag:           "debugging",
		FrictionScore: 65,
		Decisions:     []string{"Cap each signal at 25"},
		OpenThreads:   []string{"Test with real transcripts"},
	}, "\n## Summary\nFixed friction scoring.\n")

	seedSession(t, vault, "test-proj", storage.SessionMeta{
		Date:          "2026-04-03",
		Title:         "Session query tools design",
		Summary:       "Planned the session query tool implementations.",
		Tag:           "planning",
		FrictionScore: 5,
	}, "\n## Summary\nDesign session.\n")
}

// ---------------------------------------------------------------------------
// vp_search_sessions
// ---------------------------------------------------------------------------

func TestSearchSessionsSchema(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := SearchSessionsTool(vault)
	if tool.Name != "vp_search_sessions" {
		t.Errorf("name = %q, want vp_search_sessions", tool.Name)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema, &schema); err != nil {
		t.Fatalf("invalid schema: %v", err)
	}
}

func TestSearchSessionsAll(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTestSessions(t, vault)
	tool := SearchSessionsTool(vault)

	params := json.RawMessage(`{"project": "test-proj"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	results := result.([]sessionSearchResult)
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
}

func TestSearchSessionsQuery(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTestSessions(t, vault)
	tool := SearchSessionsTool(vault)

	params := json.RawMessage(`{"project": "test-proj", "query": "chunking"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	results := result.([]sessionSearchResult)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Title != "Implement chunking engine" {
		t.Errorf("title = %q", results[0].Title)
	}
}

func TestSearchSessionsDateRange(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTestSessions(t, vault)
	tool := SearchSessionsTool(vault)

	params := json.RawMessage(`{"project": "test-proj", "date_from": "2026-04-02", "date_to": "2026-04-02"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	results := result.([]sessionSearchResult)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
}

func TestSearchSessionsFrictionFilter(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTestSessions(t, vault)
	tool := SearchSessionsTool(vault)

	params := json.RawMessage(`{"project": "test-proj", "min_friction": 50}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	results := result.([]sessionSearchResult)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (friction >= 50)", len(results))
	}
	if results[0].FrictionScore != 65 {
		t.Errorf("friction = %d, want 65", results[0].FrictionScore)
	}
}

func TestSearchSessionsTagFilter(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTestSessions(t, vault)
	tool := SearchSessionsTool(vault)

	params := json.RawMessage(`{"project": "test-proj", "tag": "debugging"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	results := result.([]sessionSearchResult)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Tag != "debugging" {
		t.Errorf("tag = %q, want debugging", results[0].Tag)
	}
}

func TestSearchSessionsLimit(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTestSessions(t, vault)
	tool := SearchSessionsTool(vault)

	params := json.RawMessage(`{"project": "test-proj", "limit": 1}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	results := result.([]sessionSearchResult)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (limit)", len(results))
	}
}

func TestSearchSessionsNoResults(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTestSessions(t, vault)
	tool := SearchSessionsTool(vault)

	params := json.RawMessage(`{"project": "test-proj", "query": "nonexistent-xyzzy"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	results := result.([]sessionSearchResult)
	if results != nil {
		t.Fatalf("got %d results, want nil", len(results))
	}
}

func TestSearchSessionsMissingProject(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := SearchSessionsTool(vault)
	_, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestSearchSessionsLimitClamping(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTestSessions(t, vault)
	tool := SearchSessionsTool(vault)

	// Limit > 100 gets clamped to 100.
	params := json.RawMessage(`{"project": "test-proj", "limit": 999}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	results := result.([]sessionSearchResult)
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
}

// ---------------------------------------------------------------------------
// vp_get_session_detail
// ---------------------------------------------------------------------------

func TestGetSessionDetailSchema(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := GetSessionDetailTool(vault)
	if tool.Name != "vp_get_session_detail" {
		t.Errorf("name = %q, want vp_get_session_detail", tool.Name)
	}
}

func TestGetSessionDetailBasic(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	id := seedSession(t, vault, "test-proj", storage.SessionMeta{
		Date:          "2026-04-01",
		Title:         "Implement chunking engine",
		Summary:       "Built sliding window chunker with sentence boundaries.",
		Tag:           "implementation",
		FrictionScore: 15,
		Decisions:     []string{"Use sliding window", "800 char chunks"},
		FilesChanged:  []string{"internal/capture/chunker.go"},
		OpenThreads:   []string{"Phase 5 Task 5.3 next"},
	}, "\n## Summary\nBuilt chunker.\n")
	tool := GetSessionDetailTool(vault)

	// Use the host-scoped id the writer minted so the detail handler resolves
	// the file via the fingerprint threaded through parseSessionID → ReadSession.
	params := json.RawMessage(`{"project": "test-proj", "session_id": "` + id + `"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	r := result.(sessionDetailResult)
	if r.Title != "Implement chunking engine" {
		t.Errorf("title = %q", r.Title)
	}
	if r.Body == "" {
		t.Error("body is empty")
	}
	if r.FrictionScore != 15 {
		t.Errorf("friction = %d, want 15", r.FrictionScore)
	}
	if len(r.Decisions) != 2 {
		t.Errorf("decisions count = %d, want 2", len(r.Decisions))
	}
}

func TestGetSessionDetailMissing(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := GetSessionDetailTool(vault)

	params := json.RawMessage(`{"project": "test-proj", "session_id": "2026-01-01-01"}`)
	_, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for missing session")
	}
}

func TestGetSessionDetailBadID(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := GetSessionDetailTool(vault)

	params := json.RawMessage(`{"project": "test-proj", "session_id": "bad"}`)
	_, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for bad session_id")
	}
}

func TestGetSessionDetailMissingProject(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := GetSessionDetailTool(vault)
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"session_id": "2026-04-01-01"}`))
	if err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestGetSessionDetailMissingSessionID(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := GetSessionDetailTool(vault)
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"project": "test-proj"}`))
	if err == nil {
		t.Fatal("expected error for missing session_id")
	}
}

// ---------------------------------------------------------------------------
// vp_get_project_context
// ---------------------------------------------------------------------------

func TestGetProjectContextSchema(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)
	tool := GetProjectContextTool(vault, resolver)
	if tool.Name != "vp_get_project_context" {
		t.Errorf("name = %q, want vp_get_project_context", tool.Name)
	}
}

func TestGetProjectContextAllSections(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTestSessions(t, vault)
	resolver := vpctx.NewResolver(vault.Root)
	tool := GetProjectContextTool(vault, resolver)

	params := json.RawMessage(`{"project": "test-proj"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	r := result.(ProjectContext)
	if r.Project != "test-proj" {
		t.Errorf("project = %q", r.Project)
	}
	if len(r.Sessions) != 3 {
		t.Errorf("sessions count = %d, want 3", len(r.Sessions))
	}
	if len(r.Threads) == 0 {
		t.Error("threads is empty, expected aggregated open threads")
	}
	if len(r.Decisions) == 0 {
		t.Error("decisions is empty, expected aggregated decisions")
	}
}

func TestGetProjectContextSelectiveSections(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTestSessions(t, vault)
	resolver := vpctx.NewResolver(vault.Root)
	tool := GetProjectContextTool(vault, resolver)

	params := json.RawMessage(`{"project": "test-proj", "sections": ["sessions"]}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	r := result.(ProjectContext)
	if len(r.Sessions) == 0 {
		t.Error("sessions should be populated")
	}
	if r.Threads != nil {
		t.Errorf("threads should be nil when not requested, got %v", r.Threads)
	}
	if r.Decisions != nil {
		t.Errorf("decisions should be nil when not requested, got %v", r.Decisions)
	}
}

func TestGetProjectContextMaxSessions(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTestSessions(t, vault)
	resolver := vpctx.NewResolver(vault.Root)
	tool := GetProjectContextTool(vault, resolver)

	params := json.RawMessage(`{"project": "test-proj", "max_sessions": 2}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	r := result.(ProjectContext)
	if len(r.Sessions) != 2 {
		t.Errorf("sessions count = %d, want 2 (max_sessions)", len(r.Sessions))
	}
}

func TestGetProjectContextWithResume(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	// Create a resume file for the project.
	resumeDir := filepath.Join(vault.Root, "Projects", "test-proj")
	os.MkdirAll(resumeDir, 0755)
	os.WriteFile(filepath.Join(resumeDir, "resume.md"), []byte("# test-proj — Working Context\n\nPhase 5 in progress."), 0644)

	resolver := vpctx.NewResolver(vault.Root)
	tool := GetProjectContextTool(vault, resolver)

	params := json.RawMessage(`{"project": "test-proj", "sections": ["summary"]}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	r := result.(ProjectContext)
	if r.Summary == "" {
		t.Error("summary should be populated from resume")
	}
}

func TestGetProjectContextMissingProject(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)
	tool := GetProjectContextTool(vault, resolver)
	_, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestGetProjectContextDeduplicatesThreads(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	// Two sessions with overlapping open threads.
	seedSession(t, vault, "test-proj", storage.SessionMeta{
		Date:        "2026-04-01",
		Title:       "Session 1",
		Summary:     "First.",
		OpenThreads: []string{"thread-A", "thread-B"},
	}, "")
	seedSession(t, vault, "test-proj", storage.SessionMeta{
		Date:        "2026-04-02",
		Title:       "Session 2",
		Summary:     "Second.",
		OpenThreads: []string{"thread-B", "thread-C"},
	}, "")

	resolver := vpctx.NewResolver(vault.Root)
	tool := GetProjectContextTool(vault, resolver)

	params := json.RawMessage(`{"project": "test-proj", "sections": ["threads"]}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	r := result.(ProjectContext)
	if len(r.Threads) != 3 {
		t.Errorf("threads = %v, want 3 deduplicated", r.Threads)
	}
}

// ---------------------------------------------------------------------------
// vp_get_effectiveness
// ---------------------------------------------------------------------------

func TestGetEffectivenessSchema(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := GetEffectivenessTool(vault)
	if tool.Name != "vp_get_effectiveness" {
		t.Errorf("name = %q, want vp_get_effectiveness", tool.Name)
	}
}

func TestGetEffectivenessBasic(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTestSessions(t, vault)
	tool := GetEffectivenessTool(vault)

	params := json.RawMessage(`{"project": "test-proj", "weeks": 52}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	r := result.(EffectivenessResult)
	if r.Project != "test-proj" {
		t.Errorf("project = %q", r.Project)
	}
	if r.Overall.TotalSessions != 3 {
		t.Errorf("total sessions = %d, want 3", r.Overall.TotalSessions)
	}
	// Session 1 has decisions+files (context), session 2 has decisions (context),
	// session 3 has neither (no context).
	if r.Overall.WithContext != 2 {
		t.Errorf("with_context = %d, want 2", r.Overall.WithContext)
	}
	if r.Overall.WithoutContext != 1 {
		t.Errorf("without_context = %d, want 1", r.Overall.WithoutContext)
	}
}

func TestGetEffectivenessNoSessions(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := GetEffectivenessTool(vault)

	params := json.RawMessage(`{"project": "test-proj"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	r := result.(EffectivenessResult)
	if r.Overall.TotalSessions != 0 {
		t.Errorf("total sessions = %d, want 0", r.Overall.TotalSessions)
	}
}

func TestGetEffectivenessMissingProject(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := GetEffectivenessTool(vault)
	_, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestGetEffectivenessWeeksClamping(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTestSessions(t, vault)
	tool := GetEffectivenessTool(vault)

	// Weeks > 52 clamped.
	params := json.RawMessage(`{"project": "test-proj", "weeks": 100}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	r := result.(EffectivenessResult)
	if r.Overall.TotalSessions != 3 {
		t.Errorf("total sessions = %d, want 3", r.Overall.TotalSessions)
	}
}

func TestGetEffectivenessOutcomeScores(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTestSessions(t, vault)
	tool := GetEffectivenessTool(vault)

	params := json.RawMessage(`{"project": "test-proj", "weeks": 52}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	r := result.(EffectivenessResult)

	// Session 1: friction=15, outcome=85, has context
	// Session 2: friction=65, outcome=35, has context (has decisions)
	// Session 3: friction=5, outcome=95, no context
	// Avg context outcome: (85+35)/2 = 60
	// Avg no-context outcome: 95/1 = 95
	if r.Overall.AvgCtxOutcome != 60 {
		t.Errorf("avg context outcome = %.1f, want 60.0", r.Overall.AvgCtxOutcome)
	}
	if r.Overall.AvgNoCtxOutcome != 95 {
		t.Errorf("avg no-context outcome = %.1f, want 95.0", r.Overall.AvgNoCtxOutcome)
	}
}

// ---------------------------------------------------------------------------
// Helper tests
// ---------------------------------------------------------------------------

func TestParseSessionID(t *testing.T) {
	// Legacy host-agnostic id: fp is empty.
	date, fp, iter, err := parseSessionID("2026-04-07-03")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if date != "2026-04-07" {
		t.Errorf("date = %q, want 2026-04-07", date)
	}
	if fp != "" {
		t.Errorf("fp = %q, want empty for legacy id", fp)
	}
	if iter != 3 {
		t.Errorf("iteration = %d, want 3", iter)
	}

	// Host-scoped id: fp is the 8-hex segment.
	date, fp, iter, err = parseSessionID("2026-06-23-a1b2c3d4-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if date != "2026-06-23" || fp != "a1b2c3d4" || iter != 1 {
		t.Errorf("parse host-scoped = (%q, %q, %d), want (2026-06-23, a1b2c3d4, 1)", date, fp, iter)
	}
}

func TestParseSessionIDInvalid(t *testing.T) {
	_, _, _, err := parseSessionID("bad")
	if err == nil {
		t.Fatal("expected error for bad session ID")
	}
}

func TestSortStrings(t *testing.T) {
	s := []string{"c", "a", "b"}
	sortStrings(s)
	if s[0] != "a" || s[1] != "b" || s[2] != "c" {
		t.Errorf("sort failed: %v", s)
	}
}

func TestRoundTo(t *testing.T) {
	if r := roundTo(3.456, 1); r != 3.5 {
		t.Errorf("roundTo(3.456, 1) = %v, want 3.5", r)
	}
	if r := roundTo(3.456, 2); r != 3.46 {
		t.Errorf("roundTo(3.456, 2) = %v, want 3.46", r)
	}
}

func TestMetaMatchesQuery(t *testing.T) {
	meta := storage.SessionMeta{
		Title:     "Implement chunking",
		Summary:   "Built the chunker.",
		Decisions: []string{"Use sliding window"},
	}
	if !metaMatchesQuery(meta, "chunking") {
		t.Error("should match title")
	}
	if !metaMatchesQuery(meta, "sliding") {
		t.Error("should match decision")
	}
	if metaMatchesQuery(meta, "nonexistent") {
		t.Error("should not match")
	}
}

// TestParseSessionIDRejectsMalformed pins that parseSessionID validates the
// YYYY-MM-DD-NN layout positionally, so a slug-valid but malformed id is
// rejected rather than silently sliced into a different real session. The id
// now arrives off the wire via the session resource URI.
func TestParseSessionIDRejectsMalformed(t *testing.T) {
	bad := []string{
		"2026-06-201-5",          // misplaced hyphen: would slice to 2026-06-20 iter 5
		"2026-06-2-08",           // day not two digits / hyphen at wrong position
		"202-06-20-08",           // year not four digits
		"2026/06/20/08",          // wrong separators
		"2026-06-20",             // no iteration
		"xxxx-xx-xx-01",          // non-digit date
		"2026-06-20-",            // empty iteration
		"2026-06-23-a1b2c3d-01",  // fp segment not 8 chars
		"2026-06-23-A1B2C3D4-01", // fp not lowercase-hex
		"2026-06-23-a1b2c3z4-01", // fp has non-hex char
		"2026-06-23-a1b2c3d4-",   // host-scoped, empty iteration
		"2026-06-23-a1b2c3d4-xx", // host-scoped, non-digit iteration
	}
	for _, id := range bad {
		if _, _, _, err := parseSessionID(id); err == nil {
			t.Errorf("parseSessionID(%q): expected error, got nil", id)
		}
	}

	// A well-formed legacy id still parses (fp empty).
	date, fp, iter, err := parseSessionID("2026-06-20-08")
	if err != nil {
		t.Fatalf("parseSessionID(valid): unexpected error %v", err)
	}
	if date != "2026-06-20" || fp != "" || iter != 8 {
		t.Errorf("parseSessionID(valid) = (%q, %q, %d), want (2026-06-20, \"\", 8)", date, fp, iter)
	}
}
