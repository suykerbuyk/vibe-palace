// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWriteAndReadSession(t *testing.T) {
	v := testVault(t)
	meta := SessionMeta{
		Date:          "2026-03-15",
		Title:         "Test Session",
		Summary:       "Implemented feature X",
		Tag:           "implementation",
		FrictionScore: 20,
		Decisions:     []string{"Use JSONL format", "Skip caching for now"},
		FilesChanged:  []string{"drawers.go", "drawers_test.go"},
	}
	body := "## Transcript\n\nSome session content here.\n"

	id, err := v.WriteSession("proj", meta, body)
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	if id != "2026-03-15-01" {
		t.Errorf("ID = %q, want %q", id, "2026-03-15-01")
	}

	gotMeta, gotBody, err := v.ReadSession("proj", "2026-03-15", 1)
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if gotMeta.Title != "Test Session" {
		t.Errorf("Title = %q, want %q", gotMeta.Title, "Test Session")
	}
	if gotMeta.Summary != "Implemented feature X" {
		t.Errorf("Summary = %q, want %q", gotMeta.Summary, "Implemented feature X")
	}
	if gotMeta.Project != "proj" {
		t.Errorf("Project = %q, want %q", gotMeta.Project, "proj")
	}
	if gotMeta.Iteration != 1 {
		t.Errorf("Iteration = %d, want 1", gotMeta.Iteration)
	}
	if gotMeta.FrictionScore != 20 {
		t.Errorf("FrictionScore = %d, want 20", gotMeta.FrictionScore)
	}
	if len(gotMeta.Decisions) != 2 {
		t.Errorf("Decisions count = %d, want 2", len(gotMeta.Decisions))
	}
	if len(gotMeta.FilesChanged) != 2 {
		t.Errorf("FilesChanged count = %d, want 2", len(gotMeta.FilesChanged))
	}
	if gotBody != "## Transcript\n\nSome session content here.\n" {
		t.Errorf("body = %q", gotBody)
	}
}

func TestWriteSessionAutoIncrement(t *testing.T) {
	v := testVault(t)

	for i := 1; i <= 3; i++ {
		meta := SessionMeta{Date: "2026-03-15", Title: "Session"}
		id, err := v.WriteSession("proj", meta, "")
		if err != nil {
			t.Fatalf("WriteSession %d: %v", i, err)
		}
		want := "2026-03-15-" + string(rune('0'+i))
		wantFmt := ""
		switch i {
		case 1:
			wantFmt = "2026-03-15-01"
		case 2:
			wantFmt = "2026-03-15-02"
		case 3:
			wantFmt = "2026-03-15-03"
		}
		if id != wantFmt {
			t.Errorf("iteration %d: ID = %q, want %q (also tried %q)", i, id, wantFmt, want)
		}
	}
}

func TestNextIteration(t *testing.T) {
	v := testVault(t)

	n, err := v.NextIteration("proj", "2026-03-15")
	if err != nil {
		t.Fatalf("NextIteration: %v", err)
	}
	if n != 1 {
		t.Errorf("NextIteration = %d, want 1", n)
	}

	// Write one session, then check again.
	meta := SessionMeta{Date: "2026-03-15", Title: "Session"}
	if _, err := v.WriteSession("proj", meta, ""); err != nil {
		t.Fatal(err)
	}

	n, err = v.NextIteration("proj", "2026-03-15")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("NextIteration after write = %d, want 2", n)
	}
}

func TestListSessions(t *testing.T) {
	v := testVault(t)
	dates := []string{"2026-03-14", "2026-03-15", "2026-03-16"}
	for _, d := range dates {
		meta := SessionMeta{Date: d, Title: "Session on " + d}
		if _, err := v.WriteSession("proj", meta, ""); err != nil {
			t.Fatal(err)
		}
	}

	// All sessions.
	got, err := v.ListSessions("proj", "", "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListSessions returned %d, want 3", len(got))
	}

	// Date range filter.
	got, err = v.ListSessions("proj", "2026-03-15", "2026-03-15", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("date filtered returned %d, want 1", len(got))
	}
	if got[0].Date != "2026-03-15" {
		t.Errorf("Date = %q, want %q", got[0].Date, "2026-03-15")
	}

	// Limit.
	got, err = v.ListSessions("proj", "", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("limited list returned %d, want 2", len(got))
	}
}

func TestListSessionsEmpty(t *testing.T) {
	v := testVault(t)
	got, err := v.ListSessions("proj", "", "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(got))
	}
}

func TestSearchSessions(t *testing.T) {
	v := testVault(t)
	v.WriteSession("proj", SessionMeta{Date: "2026-03-15", Title: "Auth refactor", Summary: "Rewrote auth middleware", FrictionScore: 40}, "")
	v.WriteSession("proj", SessionMeta{Date: "2026-03-16", Title: "Bug fix", Summary: "Fixed login timeout", FrictionScore: 10}, "")
	v.WriteSession("proj", SessionMeta{Date: "2026-03-17", Title: "Feature work", Summary: "Added dashboard", Decisions: []string{"Use auth tokens"}, FrictionScore: 25}, "")

	// Text search.
	got, err := v.SearchSessions("auth", "proj", 0, 0)
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("search 'auth' returned %d, want 2", len(got))
	}

	// Friction filter.
	got, err = v.SearchSessions("", "proj", 25, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("friction>=25 returned %d, want 2", len(got))
	}

	// Combined.
	got, err = v.SearchSessions("auth", "proj", 30, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("combined search returned %d, want 1", len(got))
	}

	// With limit.
	got, err = v.SearchSessions("", "proj", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("limited search returned %d, want 1", len(got))
	}
}

func TestWriteSessionInvalidDate(t *testing.T) {
	v := testVault(t)
	meta := SessionMeta{Date: "not-a-date"}
	_, err := v.WriteSession("proj", meta, "")
	if err == nil {
		t.Error("WriteSession with invalid date should return error")
	}
}

func TestWriteSessionInvalidProject(t *testing.T) {
	v := testVault(t)
	meta := SessionMeta{Date: "2026-03-15"}
	_, err := v.WriteSession("BAD PROJECT", meta, "")
	if err == nil {
		t.Error("WriteSession with invalid project should return error")
	}
}

func TestReadSessionNotFound(t *testing.T) {
	v := testVault(t)
	_, _, err := v.ReadSession("proj", "2026-03-15", 1)
	if err == nil {
		t.Error("ReadSession for missing file should return error")
	}
}

func TestParseFrontmatterEmptyBody(t *testing.T) {
	data := []byte("---\ntitle: Test\n---\n")
	meta, body, err := ParseFrontmatter(data)
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	if meta.Title != "Test" {
		t.Errorf("Title = %q, want %q", meta.Title, "Test")
	}
	if body != "" {
		t.Errorf("body = %q, want empty", body)
	}
}

func TestParseFrontmatterMissingDelimiter(t *testing.T) {
	_, _, err := ParseFrontmatter([]byte("no frontmatter here"))
	if err == nil {
		t.Error("ParseFrontmatter without delimiters should return error")
	}
}

func TestSessionFileFormat(t *testing.T) {
	v := testVault(t)
	meta := SessionMeta{Date: "2026-03-15", Title: "Test"}
	id, err := v.WriteSession("proj", meta, "body content")
	if err != nil {
		t.Fatal(err)
	}
	_ = id

	path, _ := v.SessionFile("proj", "2026-03-15", 1)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	if content[:4] != "---\n" {
		t.Error("file should start with ---")
	}
	// Should contain the closing delimiter.
	if !containsLine(content, "---") {
		t.Error("file should contain closing ---")
	}
}

func containsLine(s, line string) bool {
	return slices.Contains(splitLines(s), line)
}

func TestWriteSessionNeedsIndexing(t *testing.T) {
	v := testVault(t)

	// Round-trip with NeedsIndexing: true.
	meta := SessionMeta{
		Date:          "2026-03-15",
		Title:         "Hook-captured session",
		NeedsIndexing: true,
	}
	_, err := v.WriteSession("proj", meta, "")
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	got, _, err := v.ReadSession("proj", "2026-03-15", 1)
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if !got.NeedsIndexing {
		t.Error("NeedsIndexing = false after round-trip, want true")
	}

	// Verify omitempty: a session with NeedsIndexing false should not
	// contain the field in the written YAML.
	meta2 := SessionMeta{
		Date:          "2026-03-16",
		Title:         "Normal session",
		NeedsIndexing: false,
	}
	_, err = v.WriteSession("proj", meta2, "")
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	path, _ := v.SessionFile("proj", "2026-03-16", 1)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "needs_indexing") {
		t.Error("YAML contains needs_indexing when false; omitempty should suppress it")
	}
}

func TestRewriteSessionOverwritesInPlace(t *testing.T) {
	v := testVault(t)

	// Seed a session via the normal writer.
	id, err := v.WriteSession("proj", SessionMeta{
		Date:    "2026-05-01",
		Title:   "Original",
		Summary: "plain summary",
	}, "## Summary\n\nplain summary\n")
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	if id != "2026-05-01-01" {
		t.Fatalf("id = %q, want 2026-05-01-01", id)
	}

	// Rewrite the SAME date+iteration with modified meta/body.
	newMeta := SessionMeta{
		Date:        "2026-05-01",
		Title:       "Enriched",
		Summary:     "enriched summary",
		Tag:         "refactor",
		EnrichedBy:  "test-model",
		EnrichedAt:  "2026-05-01T00:00:00Z",
		Decisions:   []string{"d1"},
		OpenThreads: []string{"t1"},
	}
	if err := v.RewriteSession("proj", "2026-05-01", 1, newMeta, "## Summary\n\nenriched summary\n"); err != nil {
		t.Fatalf("RewriteSession: %v", err)
	}

	// No new iteration should have been created.
	if n, _ := v.NextIteration("proj", "2026-05-01"); n != 2 {
		t.Errorf("NextIteration = %d, want 2 (iteration must NOT be bumped)", n)
	}
	dir, _ := v.SessionDir("proj")
	matches, _ := filepath.Glob(filepath.Join(dir, "2026-05-01-*.md"))
	if len(matches) != 1 {
		t.Fatalf("found %d session files, want exactly 1 (no new file): %v", len(matches), matches)
	}

	// Round-trips with the new content and pinned identity fields.
	got, body, err := v.ReadSession("proj", "2026-05-01", 1)
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if got.Summary != "enriched summary" {
		t.Errorf("Summary = %q, want enriched summary", got.Summary)
	}
	if got.Tag != "refactor" {
		t.Errorf("Tag = %q, want refactor", got.Tag)
	}
	if got.EnrichedBy != "test-model" {
		t.Errorf("EnrichedBy = %q, want test-model", got.EnrichedBy)
	}
	if got.ID != "2026-05-01-01" {
		t.Errorf("ID = %q, want 2026-05-01-01", got.ID)
	}
	if got.Project != "proj" || got.Iteration != 1 {
		t.Errorf("identity drift: project=%q iteration=%d", got.Project, got.Iteration)
	}
	if !strings.Contains(body, "enriched summary") {
		t.Errorf("body = %q, want enriched summary", body)
	}
}

func TestRewriteSessionByteIdenticalFraming(t *testing.T) {
	v := testVault(t)

	meta := SessionMeta{
		Date:      "2026-05-02",
		Title:     "Framing",
		Summary:   "s",
		Decisions: []string{"d1"},
	}
	body := "## Summary\n\ns"

	// Write via WriteSession (which mutates identity fields), then capture the
	// on-disk bytes.
	id, err := v.WriteSession("proj", meta, body)
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	path, _ := v.SessionFile("proj", "2026-05-02", 1)
	writeBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = id

	// marshalSessionFile with the SAME identity-stamped meta must reproduce
	// the exact framing WriteSession produced.
	framed := meta
	framed.Project = "proj"
	framed.Iteration = 1
	framed.ID = "2026-05-02-01"
	got, err := marshalSessionFile(framed, body)
	if err != nil {
		t.Fatalf("marshalSessionFile: %v", err)
	}
	if string(got) != string(writeBytes) {
		t.Errorf("marshalSessionFile framing mismatch:\ngot:\n%q\nwant:\n%q", got, writeBytes)
	}
}

func TestRewriteSessionInvalidArgs(t *testing.T) {
	v := testVault(t)
	if err := v.RewriteSession("BAD PROJECT", "2026-05-01", 1, SessionMeta{}, ""); err == nil {
		t.Error("RewriteSession with invalid project should error")
	}
	if err := v.RewriteSession("proj", "not-a-date", 1, SessionMeta{}, ""); err == nil {
		t.Error("RewriteSession with invalid date should error")
	}
}

func TestWriteSessionAllFields(t *testing.T) {
	v := testVault(t)
	meta := SessionMeta{
		Date:          "2026-03-15",
		Title:         "Full Session",
		Summary:       "Complete test",
		Tag:           "implementation",
		Model:         "claude-opus-4-6",
		Branch:        "feature-x",
		Domain:        "personal",
		DurationMin:   45,
		Messages:      120,
		TokensIn:      50000,
		TokensOut:     30000,
		ToolUses:      25,
		FrictionScore: 15,
		Decisions:     []string{"decision-1"},
		FilesChanged:  []string{"file-1.go"},
		OpenThreads:   []string{"thread-1"},
		NotePath:      "/notes/session.md",
	}

	_, err := v.WriteSession("proj", meta, "transcript")
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	got, _, err := v.ReadSession("proj", "2026-03-15", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "claude-opus-4-6" {
		t.Errorf("Model = %q, want %q", got.Model, "claude-opus-4-6")
	}
	if got.DurationMin != 45 {
		t.Errorf("DurationMin = %d, want 45", got.DurationMin)
	}
	if got.TokensIn != 50000 {
		t.Errorf("TokensIn = %d, want 50000", got.TokensIn)
	}
	if got.NotePath != "/notes/session.md" {
		t.Errorf("NotePath = %q", got.NotePath)
	}

	// Verify session file lives in expected path.
	path, _ := v.SessionFile("proj", "2026-03-15", 1)
	wantDir := filepath.Join(v.Root, "Projects", "proj", "sessions")
	if filepath.Dir(path) != wantDir {
		t.Errorf("session dir = %q, want %q", filepath.Dir(path), wantDir)
	}
}
