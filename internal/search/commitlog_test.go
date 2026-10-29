// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCommitLog seeds Projects/<project>/commit-log.md with the exact header
// shape ArchiveCommitBodies writes ("\n## commit <sha>\n\n<body>\n"), so the
// collector reads a real-format file, not a test-only approximation.
func writeCommitLog(t *testing.T, vaultRoot, project string, entries [][2]string) {
	t.Helper()
	dir := filepath.Join(vaultRoot, "Projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		sha, body := e[0], e[1]
		if body == "" {
			b.WriteString("\n## commit " + sha + "\n")
		} else {
			b.WriteString("\n## commit " + sha + "\n\n" + body + "\n")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "commit-log.md"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCommitLogCorpusIsBuiltAndSearchable is the PHYSICAL break-it-to-red test
// (impl brief SF1): a project whose ONLY corpus is commit-log.md is built by a
// real Rebuild and the commit is found by its message text, attributed to its
// SHA, exactly once. It asserts BOTH the hit and stats.CommitChunks > 0, so
// reverting the Seam-1 walk block in engine.go (which stops appending commit
// chunks and leaves stats.CommitChunks at 0) turns this test red — a comment
// could not. A focused collector unit test would stay green if the walk never
// called the collector; this goes through Rebuild/Search.
func TestCommitLogCorpusIsBuiltAndSearchable(t *testing.T) {
	eng, v := testEngine(t)
	const sha = "a4d5781a62a3a9c9e148c1ed1bbe4b7ba1864abd"
	const phrase = "lower the automatic-run archive cap to eight mebibytes"
	writeCommitLog(t, v.Root, "proj", [][2]string{
		{sha, "fix(ingest): " + phrase + "\n\nthe body explains the cap change in detail"},
	})

	st, err := eng.Rebuild(context.Background(), "proj")
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if st.CommitChunks == 0 {
		t.Fatal("stats.CommitChunks == 0: the walk did not index the commit-log tier " +
			"(Seam 1 missing?)")
	}

	res := search(t, eng, "proj", phrase)
	var hits []SearchResult
	for _, r := range res {
		if r.SourceRef == "commit/"+sha {
			hits = append(hits, r)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("commit hits = %d, want exactly 1 (SourceRef dedup to one hit per SHA); results=%d", len(hits), len(res))
	}
	if hits[0].SourceType != commitSourceType {
		t.Errorf("SourceType = %q, want %q", hits[0].SourceType, commitSourceType)
	}
	if !strings.HasPrefix(hits[0].DrawerID, "commit.") {
		t.Errorf("chunk id = %q, want a commit.-prefixed id", hits[0].DrawerID)
	}
}

// TestCommitLogMultiChunkDedupsToOneHit: a long commit message that sub-chunks
// into several parts still surfaces as ONE hit (dedup on the shared
// commit/<sha> SourceRef), while stats.CommitChunks counts every chunk.
func TestCommitLogMultiChunkDedupsToOneHit(t *testing.T) {
	eng, v := testEngine(t)
	const sha = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	phrase := "distinctive-commit-marker-xyzzy"
	// A body well over DefaultChunkConfig's 800 chars so it sub-chunks.
	body := phrase + " " + strings.Repeat("padding words that fill the chunk ", 60) + " " + phrase
	writeCommitLog(t, v.Root, "proj", [][2]string{{sha, body}})

	st, err := eng.Rebuild(context.Background(), "proj")
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if st.CommitChunks < 2 {
		t.Fatalf("CommitChunks = %d, want >= 2 for a >800-char body", st.CommitChunks)
	}
	res := search(t, eng, "proj", phrase)
	n := 0
	for _, r := range res {
		if r.SourceRef == "commit/"+sha {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("multi-chunk commit surfaced %d times, want 1 (dedup on SourceRef)", n)
	}
}

// TestCommitLogIDsDoNotCollideWithNotesOrIterations: for overlapping content,
// a commit chunk id (commit.) is disjoint from a note id (note.) and an
// iteration id (iter.), so one tier's cached vector can never answer for
// another's.
func TestCommitLogIDsDoNotCollideWithNotesOrIterations(t *testing.T) {
	_, v := testEngine(t)
	const shared = "exactly the same sentence appears in three tiers at once"
	writeCommitLog(t, v.Root, "proj", [][2]string{{"c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00", shared}})
	writeSessionNote(t, v.Root, "proj", "2026-05-13-aaaa0000-01", "2026-05-13", "wrap", shared)
	writeIterationsMD(t, v.Root, "proj", "## Iteration 1 — one\n\n"+shared+"\n\n---\n")

	commitIDs, _, _, err := collectCommitLogCorpus(v, "proj")
	if err != nil {
		t.Fatal(err)
	}
	noteIDs, _, _, err := collectNoteCorpus(v, "proj")
	if err != nil {
		t.Fatal(err)
	}
	iterIDs, _, _, err := collectIterationCorpus(v, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(commitIDs) == 0 || len(noteIDs) == 0 || len(iterIDs) == 0 {
		t.Fatalf("empty corpus: commit=%d note=%d iter=%d", len(commitIDs), len(noteIDs), len(iterIDs))
	}
	seen := map[string]string{}
	for _, id := range noteIDs {
		seen[id] = "note"
	}
	for _, id := range iterIDs {
		seen[id] = "iteration"
	}
	for _, id := range commitIDs {
		if !strings.HasPrefix(id, "commit.") {
			t.Errorf("commit id %q lacks the commit. prefix", id)
		}
		if other, ok := seen[id]; ok {
			t.Fatalf("commit id %q collides with a %s id", id, other)
		}
	}
}

// TestCommitLogOnlyProjectStillSearchableAndNotEmpty: a project with ONLY a
// commit-log is not TrulyEmpty (so a search reaches the walk rather than
// erroring nothing-indexable), and a notes-only project is unaffected
// (regression).
func TestCommitLogOnlyProjectStillSearchableAndNotEmpty(t *testing.T) {
	t.Run("commit-log-only is not empty", func(t *testing.T) {
		eng, v := testEngine(t)
		writeCommitLog(t, v.Root, "proj", [][2]string{{"feedface00feedface00feedface00feedface00", "feat: a thing"}})
		empty, err := eng.TrulyEmpty("proj")
		if err != nil {
			t.Fatal(err)
		}
		if empty {
			t.Fatal("a commit-log-only project reported TrulyEmpty = true")
		}
	})
	t.Run("notes-only still searchable", func(t *testing.T) {
		eng, v := testEngine(t)
		writeSessionNote(t, v.Root, "proj", "2026-05-13-aaaa0000-01", "2026-05-13", "wrap", "the note body mentions kumquats")
		if _, err := eng.Rebuild(context.Background(), "proj"); err != nil {
			t.Fatalf("Rebuild: %v", err)
		}
		res := search(t, eng, "proj", "kumquats")
		found := false
		for _, r := range res {
			if strings.Contains(r.Content, "kumquats") {
				found = true
			}
		}
		if !found {
			t.Fatal("notes-only project: the note is no longer searchable (regression)")
		}
	})
}
