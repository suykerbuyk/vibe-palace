// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// --- Classifier table -------------------------------------------------------

// hasPath reports whether want is in paths.
func hasPath(paths []string, want string) bool {
	return slices.Contains(paths, want)
}

func TestClassifyDirty_SweepRulesPositive(t *testing.T) {
	cases := []struct {
		name   string
		status string
		path   string
	}{
		{"session summary", "??", "Projects/vibe-palace/sessions/2026-06-17.md"},
		{"transcript manifest", "??", "Projects/vibe-palace/transcripts/abc.manifest.json"},
		{"transcript zst", "??", "Projects/vibe-palace/transcripts/abc.jsonl.zst"},
		{"kg entities", " M", "palace/vibe-palace/kg/entities.jsonl"},
		// DEEP triples path (H1) — nests under a source-derived subpath incl. .claude (L2).
		{"deep triples", "??", "palace/vibe-palace/kg/triples/.claude/plans/foo.md--mentioned_in--1234.json"},
		// DEEP drawers path (H1) — palace/<p>/drawers/<p>/<room>/drawers.jsonl.
		{"deep drawers", "??", "palace/cando-rs/drawers/cando-rs/api/drawers.jsonl"},
		{"surface tracked", " M", "Projects/vibe-palace/.surface"},
		{"palace surface tracked", " M", "palace/vibe-palace/.surface"},
		{"templates surface tracked", " M", "Templates/.surface"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			swept, reported := classifyDirty([]PorcelainEntry{{Status: c.status, Path: c.path}})
			if !hasPath(swept, c.path) {
				t.Errorf("expected %q swept, got swept=%v reported=%v", c.path, swept, reported)
			}
			if hasPath(reported, c.path) {
				t.Errorf("%q should not be reported", c.path)
			}
		})
	}
}

func TestClassifyDirty_NegativeCasesReported(t *testing.T) {
	cases := []struct {
		name   string
		status string
		path   string
	}{
		{"resume note", " M", "Projects/vibe-palace/resume.md"},
		{"task note", " M", "Projects/vibe-palace/tasks/x.md"},
		{"knowledge user file", " M", "Knowledge/foo.md"},
		{"non-palace note", "??", "Inbox/random.md"},
		// Stray Projects/p/ scaffold from an accidental `vp init` slug.
		{"stray config", "??", "Projects/p/config.toml"},
		{"stray commands readme", "??", "Projects/p/commands/README.md"},
		{"stray skills readme", "??", "Projects/p/skills/README.md"},
		// Wrong suffix under triples/ must not match (rule requires .json).
		{"triples wrong suffix", "??", "palace/p/kg/triples/foo.txt"},
		// palace file outside kg/drawers must not match.
		{"palace stray note", "??", "palace/p/notes.md"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			swept, reported := classifyDirty([]PorcelainEntry{{Status: c.status, Path: c.path}})
			if !hasPath(reported, c.path) {
				t.Errorf("expected %q reported, got swept=%v reported=%v", c.path, swept, reported)
			}
			if hasPath(swept, c.path) {
				t.Errorf("%q must never be swept", c.path)
			}
		})
	}
}

// TestClassifyDirty_SurfaceStatusGate covers the H2 status gate: tracked
// modifications of .surface (in any M form) sweep; untracked (??) reports.
func TestClassifyDirty_SurfaceStatusGate(t *testing.T) {
	const surf = "Projects/vibe-palace/.surface"

	for _, status := range []string{" M", "M ", "MM"} {
		t.Run("sweep_"+strings.ReplaceAll(status, " ", "_"), func(t *testing.T) {
			swept, reported := classifyDirty([]PorcelainEntry{{Status: status, Path: surf}})
			if !hasPath(swept, surf) {
				t.Errorf("status %q: expected %q swept, got swept=%v reported=%v", status, surf, swept, reported)
			}
		})
	}

	// The motivating stray case: untracked .surface under Projects/p/ must be
	// reported, NOT swept (split-brain commit guard, H2).
	t.Run("untracked_reported", func(t *testing.T) {
		const stray = "Projects/p/.surface"
		swept, reported := classifyDirty([]PorcelainEntry{{Status: "??", Path: stray}})
		if !hasPath(reported, stray) {
			t.Errorf("untracked %q must be reported, got swept=%v reported=%v", stray, swept, reported)
		}
		if hasPath(swept, stray) {
			t.Errorf("untracked stray %q must never be swept", stray)
		}
	})
}

// --- Porcelain parser -------------------------------------------------------

// TestParsePorcelainZ_RenameAlignment verifies that a rename record (two NUL
// fields: new path then old path) does not corrupt the alignment of records
// that follow it, and that the rename is reported while a trailing sweep
// artifact is still correctly swept.
func TestParsePorcelainZ_RenameAlignment(t *testing.T) {
	// Records (NUL-separated): rename consumes TWO fields, then a session sweep,
	// then a delete of a swept artifact.
	raw := strings.Join([]string{
		"R  Projects/vibe-palace/notes/new.md", // rename new path
		"Projects/vibe-palace/notes/old.md",    // rename old path (extra field)
		"?? Projects/vibe-palace/sessions/s.md",
		"D  palace/vibe-palace/kg/entities.jsonl",
	}, "\x00") + "\x00"

	entries := parsePorcelainZ(raw)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries (rename collapses 2 fields), got %d: %#v", len(entries), entries)
	}
	if entries[0].Status != "R " || entries[0].Path != "Projects/vibe-palace/notes/new.md" {
		t.Errorf("rename entry mis-parsed: %#v", entries[0])
	}
	if entries[1].Path != "Projects/vibe-palace/sessions/s.md" {
		t.Errorf("alignment corrupted after rename; entry[1]=%#v", entries[1])
	}
	if entries[2].Status != "D " || entries[2].Path != "palace/vibe-palace/kg/entities.jsonl" {
		t.Errorf("delete entry mis-parsed: %#v", entries[2])
	}

	swept, reported := classifyDirty(entries)
	if !hasPath(reported, "Projects/vibe-palace/notes/new.md") {
		t.Errorf("rename should be reported, got reported=%v", reported)
	}
	if !hasPath(swept, "Projects/vibe-palace/sessions/s.md") {
		t.Errorf("session after rename should be swept, got swept=%v", swept)
	}
	if !hasPath(swept, "palace/vibe-palace/kg/entities.jsonl") {
		t.Errorf("delete of swept artifact should be swept (git add stages D), got swept=%v", swept)
	}
}

func TestParsePorcelainZ_Empty(t *testing.T) {
	if entries := parsePorcelainZ(""); len(entries) != 0 {
		t.Errorf("empty output should parse to 0 entries, got %#v", entries)
	}
}

// --- TidyVault integration --------------------------------------------------

// dirtyArtifactVault seeds a git vault with one committed artifact (so its later
// modification is tracked) and several untracked artifacts + non-artifact dirt.
func TestTidyVault_SweepsArtifactsReportsDirt(t *testing.T) {
	dir := initTestRepo(t)

	// Commit a .surface so a later modification shows as tracked (" M").
	writeFile(t, dir, "Projects/vibe-palace/.surface", "v1\n")
	gitRun(t, dir, "add", "Projects/vibe-palace/.surface")
	gitRun(t, dir, "commit", "-m", "seed surface")

	// Tracked modification of .surface (hook churn) → swept.
	writeFile(t, dir, "Projects/vibe-palace/.surface", "v2\n")
	// Untracked capture artifacts → swept.
	writeFile(t, dir, "Projects/vibe-palace/sessions/2026-06-17.md", "session\n")
	writeFile(t, dir, "palace/cando-rs/drawers/cando-rs/api/drawers.jsonl", "{}\n")
	writeFile(t, dir, "palace/vibe-palace/kg/triples/.claude/plans/foo.md--m--1.json", "{}\n")
	// Non-artifact dirt → reported, never staged.
	writeFile(t, dir, "Projects/vibe-palace/resume.md", "resume\n")
	writeFile(t, dir, "Projects/p/config.toml", "stray\n")
	writeFile(t, dir, "Projects/p/.surface", "stray-stamp\n")

	res, err := TidyVault(dir, false)
	if err != nil {
		t.Fatalf("TidyVault: %v", err)
	}
	if !res.Committed {
		t.Fatal("expected a commit")
	}
	if res.CommitSHA == "" {
		t.Error("expected a CommitSHA")
	}
	if res.RemoteResults != nil {
		t.Error("local-only commit should have nil RemoteResults")
	}

	// Swept set.
	for _, want := range []string{
		"Projects/vibe-palace/.surface",
		"Projects/vibe-palace/sessions/2026-06-17.md",
		"palace/cando-rs/drawers/cando-rs/api/drawers.jsonl",
		"palace/vibe-palace/kg/triples/.claude/plans/foo.md--m--1.json",
	} {
		if !hasPath(res.Swept, want) {
			t.Errorf("expected %q swept, swept=%v", want, res.Swept)
		}
	}

	// Reported set — and crucially NEVER swept.
	for _, want := range []string{
		"Projects/vibe-palace/resume.md",
		"Projects/p/config.toml",
		"Projects/p/.surface",
	} {
		if !hasPath(res.Reported, want) {
			t.Errorf("expected %q reported, reported=%v", want, res.Reported)
		}
		if hasPath(res.Swept, want) {
			t.Errorf("non-artifact dirt %q must never be swept", want)
		}
	}

	// The reported paths must remain dirty in the working tree (not committed).
	status := gitRun(t, dir, "status", "--porcelain", "-uall")
	if !strings.Contains(status, "Projects/p/config.toml") {
		t.Errorf("stray scaffold should remain dirty, status:\n%s", status)
	}
	if !strings.Contains(status, "Projects/p/.surface") {
		t.Errorf("stray .surface should remain dirty, status:\n%s", status)
	}
	// Swept artifacts must be gone from the dirty tree.
	if strings.Contains(status, "sessions/2026-06-17.md") {
		t.Errorf("swept session should be committed, status:\n%s", status)
	}
}

// TestTidyVault_MemoryReportedAsUserContent verifies that uncommitted user
// memory under Projects/<slug>/memory/ is classified into ReportedUserContent
// (a subset of the full Reported catch-all) — distinct from genuinely-unexpected
// dirt — and is NEVER swept (memory has no sweepRule; it is committed by
// wrap/SessionEnd, not by tidy).
func TestTidyVault_MemoryReportedAsUserContent(t *testing.T) {
	dir := initTestRepo(t)

	// One sweepable artifact so a commit is created.
	writeFile(t, dir, "Projects/vibe-palace/sessions/2026-06-17.md", "session\n")
	// User memory (expected user content; un-swept).
	const mem = "Projects/vibe-palace/memory/MEMORY.md"
	const memNested = "Projects/vibe-palace/memory/notes/extra.md"
	writeFile(t, dir, mem, "memory\n")
	writeFile(t, dir, memNested, "more\n")
	// Genuinely-unexpected dirt.
	const dirt = "Projects/p/config.toml"
	writeFile(t, dir, dirt, "stray\n")

	res, err := TidyVault(dir, false)
	if err != nil {
		t.Fatalf("TidyVault: %v", err)
	}

	// Memory lands in ReportedUserContent and (per chosen semantics) also in the
	// full Reported catch-all — but NEVER in Swept.
	for _, want := range []string{mem, memNested} {
		if !hasPath(res.ReportedUserContent, want) {
			t.Errorf("expected %q in ReportedUserContent, got %v", want, res.ReportedUserContent)
		}
		if !hasPath(res.Reported, want) {
			t.Errorf("expected %q to remain in full Reported catch-all, got %v", want, res.Reported)
		}
		if hasPath(res.Swept, want) {
			t.Errorf("user memory %q must never be swept", want)
		}
	}

	// Unexpected dirt is reported but NOT classified as user content.
	if !hasPath(res.Reported, dirt) {
		t.Errorf("expected %q in Reported, got %v", dirt, res.Reported)
	}
	if hasPath(res.ReportedUserContent, dirt) {
		t.Errorf("unexpected dirt %q must not be ReportedUserContent, got %v", dirt, res.ReportedUserContent)
	}
	if hasPath(res.Swept, dirt) {
		t.Errorf("unexpected dirt %q must never be swept", dirt)
	}

	// Memory must remain dirty in the working tree (tidy never committed it).
	status := gitRun(t, dir, "status", "--porcelain", "-uall")
	if !strings.Contains(status, "memory/MEMORY.md") {
		t.Errorf("user memory should remain dirty, status:\n%s", status)
	}
}

// TestTidyScan_ClassifiesWithoutCommitting verifies the read-only scan path:
// it populates Swept/Reported but never commits and leaves HEAD untouched.
func TestTidyScan_ClassifiesWithoutCommitting(t *testing.T) {
	dir := initTestRepo(t)
	headBefore := gitRun(t, dir, "rev-parse", "HEAD")

	// One sweepable artifact and one piece of non-artifact dirt.
	writeFile(t, dir, "Projects/vibe-palace/sessions/2026-06-17.md", "session\n")
	writeFile(t, dir, "Projects/vibe-palace/resume.md", "resume\n")

	res, err := TidyScan(dir)
	if err != nil {
		t.Fatalf("TidyScan: %v", err)
	}
	if res.Committed {
		t.Error("TidyScan must never commit")
	}
	if res.CommitSHA != "" {
		t.Errorf("TidyScan must leave CommitSHA empty, got %q", res.CommitSHA)
	}
	if !hasPath(res.Swept, "Projects/vibe-palace/sessions/2026-06-17.md") {
		t.Errorf("expected session swept, swept=%v", res.Swept)
	}
	if !hasPath(res.Reported, "Projects/vibe-palace/resume.md") {
		t.Errorf("expected resume reported, reported=%v", res.Reported)
	}

	// HEAD unchanged and the artifact still dirty (never staged/committed).
	if headAfter := gitRun(t, dir, "rev-parse", "HEAD"); headAfter != headBefore {
		t.Errorf("TidyScan created a commit: HEAD %s -> %s", headBefore, headAfter)
	}
	if status := gitRun(t, dir, "status", "--porcelain", "-uall"); !strings.Contains(status, "sessions/2026-06-17.md") {
		t.Errorf("scanned artifact should remain dirty, status:\n%s", status)
	}
}

func TestTidyVault_EmptySweepNoOp(t *testing.T) {
	dir := initTestRepo(t)
	// Only non-artifact dirt present.
	writeFile(t, dir, "Knowledge/foo.md", "note\n")

	res, err := TidyVault(dir, false)
	if err != nil {
		t.Fatalf("TidyVault: %v", err)
	}
	if res.Committed {
		t.Error("empty sweep set must not commit")
	}
	if res.CommitSHA != "" {
		t.Errorf("no-op should have empty CommitSHA, got %q", res.CommitSHA)
	}
	if !hasPath(res.Reported, "Knowledge/foo.md") {
		t.Errorf("expected Knowledge/foo.md reported, got %v", res.Reported)
	}
	// HEAD unchanged (delegate never invoked).
	if subj := gitRun(t, dir, "log", "-1", "--format=%s"); subj != "seed" {
		t.Errorf("no commit should have been created, HEAD subject=%q", subj)
	}
}

func TestTidyVault_PushDowngradeNoRemote(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "Projects/vibe-palace/sessions/s.md", "session\n")

	res, err := TidyVault(dir, true) // push requested, but no remotes
	if err != nil {
		t.Fatalf("TidyVault: %v", err)
	}
	if !res.PushDowngraded {
		t.Error("expected PushDowngraded=true on remote-less vault")
	}
	if !res.Committed {
		t.Error("expected local commit despite downgrade")
	}
	if res.RemoteResults != nil {
		t.Error("downgraded local commit should have nil RemoteResults")
	}
}

func TestTidyVault_PushFalseLocalOnly(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")

	writeFile(t, dir, "Projects/vibe-palace/sessions/s.md", "session\n")

	res, err := TidyVault(dir, false)
	if err != nil {
		t.Fatalf("TidyVault: %v", err)
	}
	if res.PushDowngraded {
		t.Error("push=false is not a downgrade")
	}
	if !res.Committed {
		t.Error("expected local commit")
	}
	if res.RemoteResults != nil {
		t.Error("push=false should leave RemoteResults nil")
	}
}

func TestTidyVault_PushMultiRemoteSuccess(t *testing.T) {
	dir := initTestRepo(t)
	bare1 := initBareRemote(t)
	bare2 := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "github", bare1)
	gitRun(t, dir, "remote", "add", "vault", bare2)
	gitRun(t, dir, "push", "github", "main")
	gitRun(t, dir, "push", "vault", "main")

	writeFile(t, dir, "Projects/vibe-palace/sessions/s.md", "session\n")

	res, err := TidyVault(dir, true)
	if err != nil {
		t.Fatalf("TidyVault: %v", err)
	}
	if res.PushDowngraded {
		t.Error("remotes present — should not downgrade")
	}
	if len(res.RemoteResults) != 2 {
		t.Errorf("expected 2 remote results, got %#v", res.RemoteResults)
	}
	for remote, perr := range res.RemoteResults {
		if perr != nil {
			t.Errorf("remote %s push failed: %v", remote, perr)
		}
	}
	// Commit must have landed on both bare remotes.
	for _, bare := range []string{bare1, bare2} {
		if subj := gitRun(t, bare, "log", "-1", "--format=%s"); !strings.Contains(subj, "vault tidy") {
			t.Errorf("remote %s missing tidy commit, subject=%q", bare, subj)
		}
	}
}

// TestTidyVault_StrandedWhenAllRemotesFail verifies that when a commit is
// created and push is attempted against a configured remote that no longer
// exists (push + fetch both fail), TidyResult.Stranded is true while the commit
// still lands locally. The dead-remote path exercises the same per-remote error
// surfacing CommitAndPushPaths uses, leaving RemoteResults populated with a
// failure and AnyPushed()==false.
func TestTidyVault_StrandedWhenAllRemotesFail(t *testing.T) {
	dir := initTestRepo(t)
	// Point a configured remote at a path that does not exist: both the push
	// and the fallback fetch fail, surfacing a per-remote error.
	gitRun(t, dir, "remote", "add", "origin", filepath.Join(t.TempDir(), "nonexistent.git"))

	writeFile(t, dir, "Projects/vibe-palace/sessions/s.md", "session\n")

	res, err := TidyVault(dir, true) // push requested against a dead remote
	if err != nil {
		t.Fatalf("TidyVault: %v", err)
	}
	if !res.Committed {
		t.Error("expected local commit despite push failure")
	}
	if res.PushDowngraded {
		t.Error("a configured remote was present — this is a strand, not a downgrade")
	}
	if len(res.RemoteResults) != 1 {
		t.Fatalf("expected 1 remote result, got %#v", res.RemoteResults)
	}
	if !res.Stranded {
		t.Errorf("expected Stranded=true when all remotes fail, results=%#v", res.RemoteResults)
	}
}

// TestTidyVault_NotStrandedOnSuccess verifies a normal successful multi-remote
// tidy leaves Stranded false.
func TestTidyVault_NotStrandedOnSuccess(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")

	writeFile(t, dir, "Projects/vibe-palace/sessions/s.md", "session\n")

	res, err := TidyVault(dir, true)
	if err != nil {
		t.Fatalf("TidyVault: %v", err)
	}
	if res.Stranded {
		t.Errorf("successful push must not be stranded, results=%#v", res.RemoteResults)
	}
}
