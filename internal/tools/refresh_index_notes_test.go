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

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// noteOnlyProjectFixture builds a vault holding one project whose entire
// history is SESSION NOTES: no palace store, no iterations.md, and no transcript
// archives. This is the shape the live vault's qa-metabuild-system is in, and it
// is the project class this change exists for.
func noteOnlyProjectFixture(t *testing.T, project string, notes map[string]string) (*storage.Vault, *search.Engine) {
	t.Helper()
	vault := storage.NewVault(t.TempDir())

	dir := filepath.Join(vault.Root, "Projects", project, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for stem, body := range notes {
		content := strings.Join([]string{
			"---",
			"session_id: " + stem,
			"project: " + project,
			"date: 2026-08-12",
			"title: wrap",
			"---",
			body,
		}, "\n")
		if err := os.WriteFile(filepath.Join(dir, stem+".md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Preconditions. If any of these were false the fixture would be measuring
	// something other than the note-only class.
	if _, err := os.Stat(filepath.Join(vault.Root, "palace", project)); err == nil {
		t.Fatalf("fixture is degenerate: palace/%s already exists", project)
	}
	if _, err := os.Stat(filepath.Join(vault.Root, "Projects", project, "iterations.md")); err == nil {
		t.Fatal("fixture is degenerate: an iterations.md would index this project anyway")
	}
	if _, err := os.Stat(filepath.Join(vault.Root, "Projects", project, "transcripts")); err == nil {
		t.Fatal("fixture is degenerate: an archive would backfill this project anyway")
	}

	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := search.NewEngine(embedder.NewMock(384), vault, cfg)
	t.Cleanup(func() { eng.Close() })
	return vault, eng
}

// 🔴 TestRefreshIndexNoLongerRefusesANoteOnlyProject drives the refusal BRANCH,
// not the refusal's wording. The condition it turns on is
// `!hadStore && stats.Indexed == 0 && bf.ArchivesIngested == 0 && bf.ArchivesSkipped == 0`
// — the note corpus moves `stats.Indexed` off zero, so the branch is no longer
// reachable for this project and the tool takes the success path. Asserting on
// the message text alone would still pass if the message were edited and the
// behaviour left broken.
func TestRefreshIndexNoLongerRefusesANoteOnlyProject(t *testing.T) {
	const project = "qa-notes-only"
	const needle = "the metabuild harness lost its ordering guarantee on rerun"

	vault, eng := noteOnlyProjectFixture(t, project, map[string]string{
		"2026-08-12-1111aaaa-01": "Wrap note: " + needle + ", and we fixed it.",
	})

	// NOTE: there is deliberately no "unreachable before the refresh" control
	// here, unlike the archive backfill test. Engine.Search calls ensureIndex,
	// which runs a full Rebuild on a cold project — so the note corpus is
	// reachable from the FIRST search without any explicit refresh at all. That
	// is a stronger property than the one under test, not a degenerate fixture,
	// and TestRefreshIndexStillRefusesAProjectWithNoNotesEither carries the
	// non-vacuity by showing the same tool still refuses when the notes are
	// absent.
	tool := RefreshIndexTool(eng, vault)
	params, _ := json.Marshal(refreshIndexParams{Project: project})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("vp_refresh_index still refuses a note-only project: %v", err)
	}

	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", res)
	}
	if got := m["status"]; got != "rebuilt" {
		t.Errorf("status = %v, want rebuilt", got)
	}
	if got := m["had_palace_store"]; got != false {
		t.Errorf("had_palace_store = %v, want false — this project has no store", got)
	}
	if got, _ := m["note_chunks"].(int); got == 0 {
		t.Fatalf("note_chunks = %v, want > 0 (result: %v)", m["note_chunks"], m)
	}
	if got, _ := m["indexed"].(int); got == 0 {
		t.Fatalf("indexed = %v, want > 0 — the refusal keys on this", m["indexed"])
	}
	if got, _ := m["drawers"].(int); got != 0 {
		t.Errorf("drawers = %v, want 0 — the note pass writes no drawers", got)
	}
	if got, _ := m["archives_found"].(int); got != 0 {
		t.Errorf("archives_found = %v, want 0", got)
	}

	// THE ACCEPTANCE: `vp search` reaches the note.
	after, err := eng.Search(context.Background(), needle, search.SearchFilters{Project: project, Limit: 5})
	if err != nil {
		t.Fatalf("post-search: %v", err)
	}
	var found *search.SearchResult
	for i := range after {
		if strings.Contains(after[i].Content, needle) {
			found = &after[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("a note-only project is still unreachable from vp search; %d results: %+v", len(after), after)
	}
	if found.SourceType != "session-note" {
		t.Errorf("SourceType = %q, want %q — a note hit must be distinguishable from a transcript hit",
			found.SourceType, "session-note")
	}
	if found.SourceRef != "sessions/2026-08-12-1111aaaa-01.md" {
		t.Errorf("SourceRef = %q, want the note's path so a reader can navigate back to it", found.SourceRef)
	}

	// The note pass must not have manufactured a drawer store or a knowledge
	// graph on the way through.
	if _, err := os.Stat(filepath.Join(vault.Root, "palace", project, "drawers")); err == nil {
		t.Error("the note pass created a drawer store")
	}
	if _, err := os.Stat(filepath.Join(vault.Root, "palace", project, "kg")); err == nil {
		t.Error("the note pass created a knowledge graph")
	}
}

// TestRefreshIndexStillRefusesAProjectWithNoNotesEither keeps the refusal
// REACHABLE. A refusal that can never fire is the failure mode one layer below
// the one this change fixes, so the negative case is pinned explicitly: no
// store, no notes, no iterations, no archives.
func TestRefreshIndexStillRefusesAProjectWithNoNotesEither(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := search.NewEngine(embedder.NewMock(384), vault, cfg)
	t.Cleanup(func() { eng.Close() })

	// Assert the fixture really is empty on all four axes.
	for _, rel := range []string{
		filepath.Join("palace", "truly-empty"),
		filepath.Join("Projects", "truly-empty", "sessions"),
		filepath.Join("Projects", "truly-empty", "iterations.md"),
		filepath.Join("Projects", "truly-empty", "transcripts"),
	} {
		if _, err := os.Stat(filepath.Join(vault.Root, rel)); err == nil {
			t.Fatalf("fixture is degenerate: %s exists", rel)
		}
	}

	tool := RefreshIndexTool(eng, vault)
	params, _ := json.Marshal(refreshIndexParams{Project: "truly-empty"})
	_, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatal("the refusal must still fire for a project with no store, no notes and no archives")
	}
	if !strings.Contains(err.Error(), "nothing to refresh") {
		t.Errorf("refusal must say nothing to refresh, got %q", err)
	}
	// The message must enumerate every corpus source it actually looked at,
	// including the one added here — a short enumeration is the same class of
	// dishonesty this refusal exists to prevent.
	if !strings.Contains(err.Error(), "0 session-note chunks") {
		t.Errorf("refusal must report the session-note corpus it checked: %q", err)
	}
	// And it must no longer advise deleting history that a note would make
	// indexable.
	if strings.Contains(err.Error(), "delete the orphaned history") {
		t.Errorf("refusal still advises deleting history that the note corpus can index: %q", err)
	}
	// The principle worth keeping.
	if !strings.Contains(err.Error(), "cannot invent content that was never captured") {
		t.Errorf("refusal dropped the principle it exists to state: %q", err)
	}
}

// TestRefreshIndexNoteCorpusIsNotLimitedToNoteOnlyProjects pins the widened
// blast radius honestly. Any project with session notes gains note rows —
// whether or not it also has drawers, iterations or archives. Nothing in this
// change may claim otherwise.
func TestRefreshIndexNoteCorpusIsNotLimitedToNoteOnlyProjects(t *testing.T) {
	const project = "notes-and-iterations"
	vault, eng := noteOnlyProjectFixture(t, project, map[string]string{
		"2026-08-12-2222bbbb-01": "A wrap note in a project that also has iteration history.",
	})
	if err := os.WriteFile(filepath.Join(vault.Root, "Projects", project, "iterations.md"),
		[]byte("## Iteration 1 — start\n\nAn iteration entry.\n\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := RefreshIndexTool(eng, vault)
	params, _ := json.Marshal(refreshIndexParams{Project: project})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	m := res.(map[string]any)
	iter, _ := m["iteration_chunks"].(int)
	note, _ := m["note_chunks"].(int)
	indexed, _ := m["indexed"].(int)
	if iter == 0 {
		t.Errorf("iteration_chunks = %d, want > 0 — the note source must not displace the iteration source", iter)
	}
	if note == 0 {
		t.Errorf("note_chunks = %d, want > 0 — a project with iterations still gets its notes indexed", note)
	}
	if indexed != iter+note {
		t.Errorf("indexed = %d, want %d (both sources, additively)", indexed, iter+note)
	}
}
