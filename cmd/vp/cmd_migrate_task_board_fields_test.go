// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// bfVault builds a vault root with Projects/<p>/tasks{,/done,/cancelled} for
// each named project, git-initialized with a local-only identity and
// gpgsign disabled — the same configuration testinfra.WithGit() applies to
// an ephemeral harness vault, inlined here so these tests build their own
// precisely-dated, multi-step commit history (raw file edits interleaved
// with storage.GitCommitAllAt calls) without pulling in the full MCP tool
// harness. Deliberately NO initial commit (unborn HEAD), matching WithGit()'s
// own documented design: the first fixture write is the first commit.
func bfVault(t *testing.T, projects ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range projects {
		for _, d := range []string{"tasks", "tasks/done", "tasks/cancelled"} {
			if err := os.MkdirAll(filepath.Join(root, "Projects", p, d), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
		}
	}
	if err := storage.GitInit(root); err != nil {
		t.Fatalf("git init: %v", err)
	}
	for _, kv := range [][2]string{
		{"user.name", "bftest"},
		{"user.email", "bftest@vibe-palace.invalid"},
		{"commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", "-C", root, "config", kv[0], kv[1])
		cmd.Env = storage.SafeGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config %s %s: %s: %v", kv[0], kv[1], out, err)
		}
	}
	// Every board-fields test needs surface.RequiredDataFormat to start
	// UNMIGRATED (format 0), matching what a real pre-migration vault looks
	// like — a fresh harness-shaped vault would otherwise be born-current.
	// No manifest was ever written here (t.TempDir(), no surface.WriteFormat
	// call), so ReadFormat already reads 0; nothing to reset.
	return root
}

// bfCommit stages everything dirty at root and commits with both the author
// and committer date pinned to at — storage.GitCommitAllAt, the exact
// primitive testinfra.WithGitCommit wraps as a declarative SeedOption. Used
// directly here because these tests interleave several dated commits with
// raw file edits within one test body.
func bfCommit(t *testing.T, root, message string, at time.Time) {
	t.Helper()
	if err := storage.GitCommitAllAt(root, message, at); err != nil {
		t.Fatalf("commit %q at %s: %v", message, at, err)
	}
}

// bfBody is a fixed body block appended after the header, so a byte-diff
// test can assert this exact substring survives a migration untouched.
const bfBody = "## Context\n\nFixture body for board-fields migration tests.\n"

// bfWriteTask writes a task file with an optional extra header line (e.g. an
// existing "**CreateTime:** ..." line, for the already-migrated fixture) and
// bfBody, returning the vault-relative path.
func bfWriteTask(t *testing.T, root, project, sub, slug, title, status, extraHeaderLine string) string {
	t.Helper()
	dir := filepath.Join(root, "Projects", project, "tasks", sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	if status != "" {
		fmt.Fprintf(&b, "**Status:** %s\n", status)
	}
	b.WriteString("**Priority:** medium\n")
	if extraHeaderLine != "" {
		b.WriteString(extraHeaderLine + "\n")
	}
	b.WriteString("\n" + bfBody)
	path := filepath.Join(dir, slug+".md")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	rel := "Projects/" + project + "/tasks/" + slug + ".md"
	if sub != "" {
		rel = "Projects/" + project + "/tasks/" + sub + "/" + slug + ".md"
	}
	return rel
}

// bfTombstone writes a tombstone at Projects/<project>/tasks/cancelled/<slug>.md
// titled exactly "Moved to <movedToProject>" — the precise, reproducible
// string MoveProvenance.TombstoneSpec renders.
func bfTombstone(t *testing.T, root, project, slug, movedToProject string) {
	t.Helper()
	dir := filepath.Join(root, "Projects", project, "tasks", "cancelled")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	content := fmt.Sprintf("# Moved to %s\n\n**Status:** cancelled\n**Priority:** medium\n\n"+
		"## Context\n\nTombstone fixture.\n", movedToProject)
	path := filepath.Join(dir, slug+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// bfRead reads a vault-relative path's current content.
func bfRead(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// bfSimulateHop lands one hop of a cross-project move as TWO SEPARATE
// commits — a destination add (nothing else dirty) followed by a source
// delete+tombstone — replicating exactly the shape internal/tools's real
// `move` handler produces (independently pinned by
// internal/testinfra/git_test.go's TestDrivenMoveProducesTwoSeparateCommits)
// without driving the MCP dispatch, so these tests keep full control over
// commit dates and avoid CreateTime already being stamped by a real `create`
// call (which would make the fixture "already migrated" before the test
// even starts).
func bfSimulateHop(t *testing.T, root, fromProject, toProject, slug string, extraSections string, at time.Time) {
	t.Helper()

	destDir := filepath.Join(root, "Projects", toProject, "tasks")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", destDir, err)
	}
	destContent := fmt.Sprintf("# Movable\n\n**Status:** planning\n**Priority:** medium\n\n%s\n"+
		"## Moved from %s\n\nMoved.\n%s", bfBody, fromProject, extraSections)
	if err := os.WriteFile(filepath.Join(destDir, slug+".md"), []byte(destContent), 0o644); err != nil {
		t.Fatalf("write destination: %v", err)
	}
	bfCommit(t, root, fmt.Sprintf("move %s %s->%s: destination", slug, fromProject, toProject), at)

	if err := os.Remove(filepath.Join(root, "Projects", fromProject, "tasks", slug+".md")); err != nil {
		t.Fatalf("remove origin: %v", err)
	}
	bfTombstone(t, root, fromProject, slug, toProject)
	bfCommit(t, root, fmt.Sprintf("move %s %s->%s: source", slug, fromProject, toProject), at)
}

// --- Fixtures required by the task's own testing strategy ---

func TestRunTaskBoardFieldsPlainNeverTouchedTask(t *testing.T) {
	root := bfVault(t, "p")
	bfWriteTask(t, root, "p", "", "solo", "Solo Task", "planning", "")
	created := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	bfCommit(t, root, "create solo", created)

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.Failed != 0 {
		t.Fatalf("unexpected failures: status=%+v fields=%+v\n%s", ps, ps, buf.String())
	}
	if ps.Applied != 1 || ps.ToMigrate != 1 {
		t.Fatalf("ps = %+v, want Applied=1 ToMigrate=1", ps)
	}
	plan := ps.Plans[0]
	if plan.CreateTime != "2026-01-05" {
		t.Errorf("CreateTime = %q, want 2026-01-05", plan.CreateTime)
	}
	if plan.ModTime != "2026-01-05" {
		t.Errorf("ModTime = %q, want 2026-01-05", plan.ModTime)
	}
	if plan.CreateWhy != "git log --follow" {
		t.Errorf("CreateWhy = %q, want %q", plan.CreateWhy, "git log --follow")
	}
	got := bfRead(t, root, "Projects/p/tasks/solo.md")
	if !strings.Contains(got, "**CreateTime:** 2026-01-05") || !strings.Contains(got, "**ModTime:** 2026-01-05") {
		t.Errorf("on-disk file missing stamped fields:\n%s", got)
	}
	// Constant-RELATIVE, deliberately. This assertion used to hardcode "2" and was
	// guarded by a RequiredDataFormat != 2 fatal placed BELOW it — so when the
	// constant moved, the hardcoded assertion failed first and the guard's
	// explanatory message never printed. Deriving the expected marker from the
	// constant removes both the literal and the need for a guard.
	wantFormat := strconv.Itoa(surface.RequiredDataFormat)
	if !strings.Contains(got, "**DataFormat:** "+wantFormat) {
		t.Errorf("on-disk file missing DataFormat marker %q:\n%s", wantFormat, got)
	}
	if !strings.HasSuffix(got, bfBody) {
		t.Errorf("body content changed:\n%s", got)
	}
	if n, err := surface.ReadFormat(root); err != nil || n != surface.RequiredDataFormat {
		t.Errorf("ReadFormat = (%d, %v), want (%d, nil)", n, err, surface.RequiredDataFormat)
	}
}

func TestRunTaskBoardFieldsSeveralIntermediateStatusChanges(t *testing.T) {
	root := bfVault(t, "p")
	rel := bfWriteTask(t, root, "p", "", "churny", "Churny Task", "planning", "")
	day0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	bfCommit(t, root, "create churny", day0)

	// Two more edits on distinct, later days — ModTime must read the LATEST,
	// CreateTime must still read the EARLIEST, exercising git log -1 vs
	// --reverse against the same (non-moved) path.
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)),
		[]byte("# Churny Task\n\n**Status:** in_progress\n**Priority:** medium\n\n"+bfBody), 0o644); err != nil {
		t.Fatalf("edit 1: %v", err)
	}
	day1 := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	bfCommit(t, root, "status -> in_progress", day1)

	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)),
		[]byte("# Churny Task\n\n**Status:** blocked\n**Priority:** medium\n\n"+bfBody), 0o644); err != nil {
		t.Fatalf("edit 2: %v", err)
	}
	day2 := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	bfCommit(t, root, "status -> blocked", day2)

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	plan := ps.Plans[0]
	if plan.CreateTime != "2026-01-01" {
		t.Errorf("CreateTime = %q, want 2026-01-01 (earliest)", plan.CreateTime)
	}
	if plan.ModTime != "2026-02-01" {
		t.Errorf("ModTime = %q, want 2026-02-01 (latest)", plan.ModTime)
	}
	// "blocked" is already a current valid status value — Status must be
	// left alone, not guessed at or renamed.
	if plan.StatusTo != "" {
		t.Errorf("StatusTo = %q, want unchanged (already valid)", plan.StatusTo)
	}
}

func TestRunTaskBoardFieldsRetiredAndCancelledTasksGetBackfillNotStatusRewrite(t *testing.T) {
	root := bfVault(t, "p")
	bfWriteTask(t, root, "p", "done", "finished", "Finished", "done", "")
	bfWriteTask(t, root, "p", "cancelled", "dropped", "Dropped", "cancelled", "")
	created := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	bfCommit(t, root, "seed archived", created)

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.Failed != 0 {
		t.Fatalf("unexpected failures: %+v %+v\n%s", ps, ps, buf.String())
	}
	if ps.Applied != 2 {
		t.Fatalf("ps.Applied = %d, want 2", ps.Applied)
	}
	for _, slug := range []string{"finished", "dropped"} {
		for _, p := range ps.Plans {
			if p.Slug != slug {
				continue
			}
			if p.StatusTo != "" {
				t.Errorf("%s: StatusTo = %q, want unchanged — phase 2 must not touch archived Status (phase 1 owns it)", slug, p.StatusTo)
			}
			if p.CreateTime != "2026-03-01" || p.ModTime != "2026-03-01" {
				t.Errorf("%s: CreateTime/ModTime = %q/%q, want 2026-03-01/2026-03-01", slug, p.CreateTime, p.ModTime)
			}
		}
	}
}

func TestRunTaskBoardFieldsCrossProjectSingleHopMove(t *testing.T) {
	root := bfVault(t, "a", "b")
	slug := "movable"
	bfWriteTask(t, root, "a", "", slug, "Movable", "planning", "")
	created := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	bfCommit(t, root, "create movable in a", created)

	movedAt := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	bfSimulateHop(t, root, "a", "b", slug, "", movedAt)

	// Sanity check on the claim this whole design rests on: --follow alone,
	// against the destination's own current path, must NOT recover the
	// pre-move date (it should either find nothing, or find only the
	// destination-add commit) — this is what makes the tombstone chase
	// necessary rather than decorative.
	followOnly, err := gitLogFirstDate(root, "--follow", "--reverse", "--format=%ad", "--date=format:%Y-%m-%d",
		"--", "Projects/b/tasks/"+slug+".md")
	if err != nil {
		t.Fatalf("sanity --follow query: %v", err)
	}
	if followOnly == "2026-01-01" {
		t.Fatalf("sanity check failed: --follow alone recovered the pre-move date — the two-commit-split "+
			"fixture is not faithful to production (git log --follow found: %s)", followOnly)
	}

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.Failed != 0 {
		t.Fatalf("unexpected failures: %+v %+v\n%s", ps, ps, buf.String())
	}
	var destPlan *boardFieldsPlan
	for i := range ps.Plans {
		if ps.Plans[i].Project == "b" && ps.Plans[i].Slug == slug {
			destPlan = &ps.Plans[i]
		}
	}
	if destPlan == nil {
		t.Fatalf("no plan found for b/%s; plans=%+v", slug, ps.Plans)
	}
	if destPlan.CreateTime != "2026-01-01" {
		t.Errorf("CreateTime = %q, want 2026-01-01 (the true pre-move creation date, via the tombstone chase)", destPlan.CreateTime)
	}
	if !strings.Contains(destPlan.CreateWhy, "single-hop tombstone-chase via a") {
		t.Errorf("CreateWhy = %q, want it to name the single-hop chase via a", destPlan.CreateWhy)
	}
	if destPlan.ModTime != "2026-02-01" {
		t.Errorf("ModTime = %q, want 2026-02-01 (the destination's own most recent commit)", destPlan.ModTime)
	}
}

func TestRunTaskBoardFieldsCrossProjectTwoHopMove(t *testing.T) {
	root := bfVault(t, "a", "b", "c")
	slug := "movable"
	bfWriteTask(t, root, "a", "", slug, "Movable", "planning", "")
	created := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	bfCommit(t, root, "create movable in a", created)

	firstMove := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	bfSimulateHop(t, root, "a", "b", slug, "", firstMove)

	secondMove := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	bfSimulateHop(t, root, "b", "c", slug, "\n## Moved from b\n\nMoved again.\n", secondMove)

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.Failed != 0 {
		t.Fatalf("unexpected failures: %+v %+v\n%s", ps, ps, buf.String())
	}
	var destPlan *boardFieldsPlan
	for i := range ps.Plans {
		if ps.Plans[i].Project == "c" && ps.Plans[i].Slug == slug {
			destPlan = &ps.Plans[i]
		}
	}
	if destPlan == nil {
		t.Fatalf("no plan found for c/%s; plans=%+v", slug, ps.Plans)
	}
	if destPlan.CreateTime != "2026-01-01" {
		t.Errorf("CreateTime = %q, want 2026-01-01 (the ORIGINAL creation date, two hops back)", destPlan.CreateTime)
	}
	if !strings.Contains(destPlan.CreateWhy, "2-hop tombstone-chase via b -> a") {
		t.Errorf("CreateWhy = %q, want it to name the 2-hop chase via b -> a", destPlan.CreateWhy)
	}
	if destPlan.ModTime != "2026-03-01" {
		t.Errorf("ModTime = %q, want 2026-03-01", destPlan.ModTime)
	}
}

// TestRunTaskBoardFieldsCycleDetectedReportsUnknownNotFailure builds the
// minimal reciprocal cycle: project d holds an active task AND (a stray,
// unrelated) tasks/cancelled/x.md titled "Moved to b"; project b holds
// tasks/cancelled/x.md titled "Moved to d". Chasing from d's active file:
// hop 1 finds b (visited={d,b}); hop 2 looks for a tombstone titled
// "Moved to b" and finds d's OWN stray one — but d is already visited, so
// the guard must fire rather than loop. d's own stray tombstone is itself a
// same-slug shadow (active + cancelled coexisting in d), which is expected
// and asserted separately.
func TestRunTaskBoardFieldsCycleDetectedReportsUnknownNotFailure(t *testing.T) {
	root := bfVault(t, "b", "d")
	bfWriteTask(t, root, "d", "", "x", "X", "planning", "")
	bfTombstone(t, root, "d", "x", "b") // d's own stray tombstone, titled "Moved to b"
	bfTombstone(t, root, "b", "x", "d") // b's tombstone, titled "Moved to d"
	bfCommit(t, root, "seed cycle fixture", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.Failed != 0 {
		t.Fatalf("unexpected phase-1 failures: %+v\n%s", ps, buf.String())
	}

	var activePlan *boardFieldsPlan
	for i := range ps.Plans {
		p := &ps.Plans[i]
		if p.Project == "d" && p.Slug == "x" && p.Dir == "" {
			activePlan = p
		}
	}
	if activePlan == nil {
		t.Fatalf("no plan found for active d/x; plans=%+v", ps.Plans)
	}
	if activePlan.Failed {
		t.Errorf("a cycle must be reported as an unknown date, never as a Failed write")
	}
	if activePlan.CreateTime != "" {
		t.Errorf("CreateTime = %q, want empty (unknown) on a detected cycle", activePlan.CreateTime)
	}
	if !strings.Contains(activePlan.CreateWhy, "cycle detected") {
		t.Errorf("CreateWhy = %q, want it to name the detected cycle", activePlan.CreateWhy)
	}
	if ps.UnknownDates < 1 {
		t.Errorf("ps.UnknownDates = %d, want >= 1", ps.UnknownDates)
	}
	// The cycle must not have looped: findTombstoneSource/deriveCreateTime
	// returning at all (rather than the test hanging or stack-overflowing)
	// is itself part of what this test proves; the explicit reason string
	// checked above is the second half.
	if !strings.Contains(activePlan.CreateWhy, "d -> b -> d") {
		t.Errorf("CreateWhy = %q, want the chain d -> b -> d spelled out", activePlan.CreateWhy)
	}

	// d's own stray tombstone is a same-slug shadow (active d/x + cancelled
	// d/x) — expected to be refused, not silently written.
	if ps.Refusals < 1 {
		t.Errorf("ps.Refusals = %d, want >= 1 (d's own stray cancelled/x.md shadows its active x.md)", ps.Refusals)
	}
}

func TestRunTaskBoardFieldsTombstoneTieBreakIsDeterministic(t *testing.T) {
	root := bfVault(t, "m1", "m2", "p")
	// Two independent, unrelated projects each coincidentally hold a
	// same-slug tombstone naming "p" as their destination.
	bfTombstone(t, root, "m1", "x", "p")
	bfTombstone(t, root, "m2", "x", "p")
	bfCommit(t, root, "seed tie-break fixture", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	src, found, err := findTombstoneSource(root, []string{"m1", "m2", "p"}, "p", "x")
	if err != nil {
		t.Fatalf("findTombstoneSource: %v", err)
	}
	if !found {
		t.Fatal("expected a match, found none")
	}
	if src != "m1" {
		t.Errorf("tie-break winner = %q, want %q (lexicographically first)", src, "m1")
	}
}

func TestRunTaskBoardFieldsAlreadyMigratedFileIsSkippedEntirely(t *testing.T) {
	root := bfVault(t, "p")
	// The marker is the constant, never a literal: "already migrated" means at the
	// CURRENT required format, and a literal here went false the moment the constant moved.
	rel := bfWriteTask(t, root, "p", "", "current", "Current", "planning",
		fmt.Sprintf("**CreateTime:** 2025-01-01\n**ModTime:** 2025-01-01\n**DataFormat:** %d", surface.RequiredDataFormat))
	before := bfRead(t, root, rel)
	bfCommit(t, root, "seed already-migrated file", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.NoWork != 1 || ps.ToMigrate != 0 || ps.Applied != 0 {
		t.Fatalf("ps = %+v, want AlreadyMigrated=1 ToMigrate=0 Applied=0", ps)
	}
	after := bfRead(t, root, rel)
	if before != after {
		t.Errorf("an already-migrated file must be left byte-identical:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestRunTaskBoardFieldsShadowSlugRefusedArchivedLeftActiveMigrated(t *testing.T) {
	root := bfVault(t, "p")
	activeRel := bfWriteTask(t, root, "p", "", "dup", "Dup Active", "planning", "")
	doneRel := bfWriteTask(t, root, "p", "done", "dup", "Dup Archived", "done", "")
	doneBefore := bfRead(t, root, doneRel)
	bfCommit(t, root, "seed shadow-slug fixture", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	// 🔴 CONTRACT CHANGE, DELIBERATE. This used to refuse the shadowed archived
	// file and migrate everything else. A refusal is now a WHOLE-RUN refusal that
	// writes nothing: the shipped per-file behaviour is exactly what produced a
	// half-migrated vault, and for a one-time coordinated migration "some files
	// moved, some did not, and the run reported both" is the state with no safe
	// recovery. The operator reads the refusal with the vault untouched instead.
	if ps.Refusals != 1 {
		t.Fatalf("ps.Refusals = %d, want 1", ps.Refusals)
	}
	if ps.Applied != 0 {
		t.Fatalf("ps.Applied = %d, want 0 — a refusal must write nothing at all", ps.Applied)
	}
	doneAfter := bfRead(t, root, doneRel)
	if doneBefore != doneAfter {
		t.Errorf("the shadowed archived file must be left byte-identical:\nbefore:\n%s\nafter:\n%s", doneBefore, doneAfter)
	}
	activeAfter := bfRead(t, root, activeRel)
	if strings.Contains(activeAfter, "**CreateTime:**") {
		t.Errorf("no file may be migrated when the run refuses:\n%s", activeAfter)
	}
	// The overall run still has a nonzero Failed count (the refusal), so
	// RequiredDataFormat must NOT have advanced.
	if n, err := surface.ReadFormat(root); err != nil || n == surface.RequiredDataFormat {
		t.Errorf("ReadFormat = (%d, %v), want anything but %d — a shadow-slug refusal must block the format stamp",
			n, err, surface.RequiredDataFormat)
	}
}

// TestRunTaskBoardFieldsPhase1sOwnWriteDoesNotDirtySkipPhase2 pins a real
// interaction: both phases write directly with no staging and no
// auto-commit, so a file phase 1 just repaired is, by construction,
// uncommitted the moment phase 2 reaches it — on every real run, not an
// edge case. A naive per-file HasUncommittedChanges check in phase 2 cannot
// tell that dirt apart from a genuinely unrelated in-flight operator edit,
// and would skip phase 2's own backfill for every file phase 1 touched —
// defeating Operator Decision 1's whole point (one coordinated-window
// command) on exactly the files it folded in.
func TestRunTaskBoardFieldsPhase1sOwnWriteDoesNotDirtySkipPhase2(t *testing.T) {
	root := bfVault(t, "p")
	// A Status that disagrees with its directory, so phase 1 has real work
	// to do (and so writes the file, uncommitted, before phase 2 runs).
	rel := bfWriteTask(t, root, "p", "done", "archived", "Archived", "In Progress", "")
	bfCommit(t, root, "seed archived (wrong status)", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.StatusRepairs != 1 {
		t.Fatalf("expected phase 1 to repair the file: %+v\n%s", ps, buf.String())
	}
	if ps.Dirty != 0 {
		t.Errorf("ps.Dirty = %d, want 0 — phase 1's own write must not dirty-skip phase 2", ps.Dirty)
	}
	if ps.Applied != 1 {
		t.Fatalf("ps.Applied = %d, want 1 — phase 2 must still backfill the file phase 1 touched", ps.Applied)
	}
	got := bfRead(t, root, rel)
	if !strings.Contains(got, "**Status:** done") {
		t.Errorf("phase 1's rename must have landed:\n%s", got)
	}
	if !strings.Contains(got, "**CreateTime:**") || !strings.Contains(got, "**ModTime:**") || !strings.Contains(got, "**DataFormat:**") {
		t.Errorf("phase 2's backfill must have landed on top of phase 1's write:\n%s", got)
	}
	if n, err := surface.ReadFormat(root); err != nil || n != surface.RequiredDataFormat {
		t.Errorf("ReadFormat = (%d, %v), want (%d, nil) — a fully clean two-phase run must still stamp the format",
			n, err, surface.RequiredDataFormat)
	}
}

func TestRunTaskBoardFieldsDirtyFileSkippedBlocksWriteFormat(t *testing.T) {
	root := bfVault(t, "p")
	rel := bfWriteTask(t, root, "p", "", "solo", "Solo", "planning", "")
	bfCommit(t, root, "create solo", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	// Uncommitted edit: the file is now dirty relative to HEAD.
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)),
		[]byte("# Solo\n\n**Status:** planning\n**Priority:** medium\n\n"+bfBody+"\nUncommitted edit.\n"), 0o644); err != nil {
		t.Fatalf("dirty edit: %v", err)
	}

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.Dirty != 1 {
		t.Fatalf("ps.Dirty = %d, want 1", ps.Dirty)
	}
	if ps.Applied != 0 {
		t.Fatalf("ps.Applied = %d, want 0 — a dirty file must not be written", ps.Applied)
	}
	got := bfRead(t, root, rel)
	if strings.Contains(got, "CreateTime") {
		t.Errorf("a dirty file must not gain a CreateTime field:\n%s", got)
	}
	if !strings.Contains(buf.String(), "RequiredDataFormat was NOT advanced") {
		t.Errorf("report must say the format was not advanced:\n%s", buf.String())
	}
	if n, err := surface.ReadFormat(root); err != nil || n == surface.RequiredDataFormat {
		t.Errorf("ReadFormat = (%d, %v), want anything but %d — a Dirty skip must block the format stamp",
			n, err, surface.RequiredDataFormat)
	}
}

func TestRunTaskBoardFieldsCleanRunAdvancesRequiredDataFormat(t *testing.T) {
	root := bfVault(t, "p")
	bfWriteTask(t, root, "p", "", "solo", "Solo", "planning", "")
	bfCommit(t, root, "create solo", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	if n, err := surface.ReadFormat(root); err != nil || n != 0 {
		t.Fatalf("precondition: ReadFormat = (%d, %v), want (0, nil)", n, err)
	}

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.Failed != 0 || ps.Dirty != 0 {
		t.Fatalf("expected a fully clean run: status=%+v fields=%+v", ps, ps)
	}
	if n, err := surface.ReadFormat(root); err != nil || n != surface.RequiredDataFormat {
		t.Errorf("ReadFormat = (%d, %v), want (%d, nil)", n, err, surface.RequiredDataFormat)
	}
	if !strings.Contains(buf.String(), fmt.Sprintf("RequiredDataFormat stamped at %d.", surface.RequiredDataFormat)) {
		t.Errorf("report must confirm the stamp:\n%s", buf.String())
	}
}

func TestRunTaskBoardFieldsRefusalBlocksEveryWriteAndTheFormatStamp(t *testing.T) {
	root := bfVault(t, "p")
	// A slug present in BOTH tasks/ and tasks/done/ is refused: the writer
	// resolves active first, so migrating the archived copy would edit the wrong
	// file. There is no "phase 1" to fail any more — the archived status repair
	// is folded into the same plan — so what this pins now is that ONE refusal
	// stops the WHOLE run, including the ordinary file that had real work to do.
	bfWriteTask(t, root, "p", "", "dup", "Dup Active", "planning", "")
	bfWriteTask(t, root, "p", "done", "dup", "Dup Archived", "In Progress", "") // disagrees with done/, so phase 1 would try to fix it
	// A second, ordinary task so phase 2 would have SOMETHING to do if it ran.
	bfWriteTask(t, root, "p", "", "ordinary", "Ordinary", "planning", "")
	bfCommit(t, root, "seed phase-1-failure fixture", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.Refusals == 0 {
		t.Fatalf("expected a shadow-slug refusal, got %+v\n%s", ps, buf.String())
	}
	if ps.Applied != 0 {
		t.Errorf("no file may be written when the run refuses: ps = %+v", ps)
	}
	if !strings.Contains(buf.String(), "Nothing was written") {
		t.Errorf("report must say nothing was written:\n%s", buf.String())
	}
	// The ordinary file had real work planned and must still be untouched.
	if got := bfRead(t, root, "Projects/p/tasks/ordinary.md"); strings.Contains(got, "**CreateTime:**") {
		t.Errorf("the ordinary file was migrated despite the refusal:\n%s", got)
	}
	if n, err := surface.ReadFormat(root); err != nil || n == surface.RequiredDataFormat {
		t.Errorf("ReadFormat = (%d, %v), want anything but %d — a phase-1 failure must block the stamp",
			n, err, surface.RequiredDataFormat)
	}
}

func TestRunTaskBoardFieldsActivePendingRenamedToPlanning(t *testing.T) {
	root := bfVault(t, "p")
	rel := bfWriteTask(t, root, "p", "", "legacy", "Legacy", "pending", "")
	bfCommit(t, root, "create legacy pending task", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.Plans[0].StatusFrom != "pending" || ps.Plans[0].StatusTo != "planning" {
		t.Fatalf("plan = %+v, want StatusFrom=pending StatusTo=planning", ps.Plans[0])
	}
	got := bfRead(t, root, rel)
	if !strings.Contains(got, "**Status:** planning") {
		t.Errorf("on-disk Status not renamed:\n%s", got)
	}
	if strings.Contains(got, "**Status:** pending") {
		t.Errorf("on-disk Status still says pending:\n%s", got)
	}
}

func TestRunTaskBoardFieldsReportModeNeverWrites(t *testing.T) {
	root := bfVault(t, "p")
	rel := bfWriteTask(t, root, "p", "", "legacy", "Legacy", "pending", "")
	bfCommit(t, root, "create legacy pending task", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	before := bfRead(t, root, rel)

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", false, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	// StatusRepairs is a PLANNED count now, not an applied one, so report mode
	// reports it as nonzero — that is defect 5's fix, and asserting it here keeps
	// the two halves (plans work, writes nothing) from being confused again.
	if ps.Applied != 0 {
		t.Fatalf("report mode must apply nothing: %+v", ps)
	}
	if ps.StatusRepairs != 1 {
		t.Fatalf("ps.StatusRepairs = %d, want 1 — report mode must COUNT the repair it would make", ps.StatusRepairs)
	}
	if ps.ToMigrate != 1 {
		t.Fatalf("ps.ToMigrate = %d, want 1 (report mode must still compute what WOULD change)", ps.ToMigrate)
	}
	after := bfRead(t, root, rel)
	if before != after {
		t.Errorf("report mode wrote to disk:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if n, err := surface.ReadFormat(root); err != nil || n != 0 {
		t.Errorf("ReadFormat = (%d, %v), want (0, nil) — report mode must not stamp anything", n, err)
	}
	// Defect 5: the PRINTED roll-up must report planned work. The shipped version
	// formatted an applied count here, which is 0 in report mode by construction,
	// so a report listing repairs summarised them as "0 repaired".
	if !strings.Contains(buf.String(), "1 to migrate (1 of them a Status repair)") {
		t.Errorf("printed roll-up must count PLANNED work:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "REPORT ONLY") {
		t.Errorf("report must say REPORT ONLY:\n%s", buf.String())
	}
}

func TestRunTaskBoardFieldsRollbackBannerListsBothPhasesPaths(t *testing.T) {
	root := bfVault(t, "p")
	// Phase 1 target: an archived file whose Status disagrees with its
	// directory.
	bfWriteTask(t, root, "p", "done", "archived", "Archived", "In Progress", "")
	// Phase 2 target: a plain active task.
	bfWriteTask(t, root, "p", "", "active", "Active", "planning", "")
	bfCommit(t, root, "seed rollback-banner fixture", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.StatusRepairs != 1 {
		t.Fatalf("expected phase 1 to apply one write: status=%+v", ps)
	}
	// Phase 2 must reach BOTH files: the plain active one, and the archived
	// one phase 1 itself just repaired — its own write must not read as
	// "dirty" and get skipped (see the phase1Written guard in
	// runTaskBoardFieldsMigration).
	if ps.Applied != 2 {
		t.Fatalf("expected phase 2 to apply two writes (including the file phase 1 touched): fields=%+v", ps)
	}
	out := buf.String()
	if !strings.Contains(out, "Projects/p/tasks/done/archived.md") {
		t.Errorf("rollback banner missing phase 1's path:\n%s", out)
	}
	if !strings.Contains(out, "Projects/p/tasks/active.md") {
		t.Errorf("rollback banner missing phase 2's path:\n%s", out)
	}
	if !strings.Contains(out, "checkout --") {
		t.Errorf("rollback banner missing the git checkout command:\n%s", out)
	}
	// The FINAL, combined banner (after phase 2's own summary) must list the
	// phase-1-and-phase-2-written path exactly once, even though phase 1's
	// own intermediate banner (printed earlier, on its own) also names it.
	_, combined, ok := strings.Cut(out, "RequiredDataFormat stamped")
	if !ok {
		t.Fatalf("could not find the combined banner section in output:\n%s", out)
	}
	if strings.Count(combined, `"Projects/p/tasks/done/archived.md"`) != 1 {
		t.Errorf("combined rollback banner should list the phase-1-and-phase-2-written path exactly once:\n%s", combined)
	}
	if !strings.Contains(out, "vault.toml") {
		t.Errorf("rollback banner should note the format stamp is deliberately excluded:\n%s", out)
	}
}

func TestRunTaskBoardFieldsProjectFlagDoesNotBlockCrossProjectTombstoneSearch(t *testing.T) {
	root := bfVault(t, "a", "b")
	slug := "movable"
	bfWriteTask(t, root, "a", "", slug, "Movable", "planning", "")
	created := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	bfCommit(t, root, "create movable in a", created)
	movedAt := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	bfSimulateHop(t, root, "a", "b", slug, "", movedAt)

	var buf bytes.Buffer
	// Scoped to "b" only — the tombstone lives in "a", which is outside the
	// scan scope but must still be searchable for the chase.
	ps, err := runTaskBoardFieldsMigration(root, "b", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.Applied != 1 {
		t.Fatalf("ps.Applied = %d, want 1", ps.Applied)
	}
	if ps.Plans[0].CreateTime != "2026-01-01" {
		t.Errorf("CreateTime = %q, want 2026-01-01 even with --project b", ps.Plans[0].CreateTime)
	}
}

// --- Tests added by task-board-fields-migration-fails-on-real-vault-data -----

// bfTreeSHA hashes every file in the vault, so a test can assert that a code
// path wrote NOTHING rather than merely that it returned an error.
func bfTreeSHA(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(paths)
	for _, p := range paths {
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			t.Fatalf("read %s: %v", p, rerr)
		}
		fmt.Fprintf(h, "%s\n%x\n", p, sha256.Sum256(b))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// bfWriteRaw overwrites a task file with arbitrary bytes, for fixtures that must
// carry a shape the typed writers would refuse to produce.
func bfWriteRaw(t *testing.T, root, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// TestBoardFieldsAlreadyMalformedFileIsSkippedNotFailed is defect 1, with the
// distinction the Tier-2 rehearsal forced.
//
// 🔴 A FILE THAT WAS ALREADY BROKEN IS NOT A MIGRATION DEFECT. The first cut of
// this validated the simulated OUTPUT and refused the whole run on any failure —
// which, against the real corpus, refused 57 files where the shipped version
// refused 24, because ValidateWholeTaskFile is the OVERWRITE validator (it
// demands a complete well-formed task file) while this migration only upserts
// header fields. 30 of those 57 were merely an older header format with no
// Status line, and the shipped code migrated them without complaint.
//
// So: already-malformed files are SKIPPED (never written — their header block is
// exactly what cannot be trusted to place a field), reported, and they hold the
// format stamp down. Only a file this migration would BREAK stops the run.
func TestBoardFieldsAlreadyMalformedFileIsSkippedNotFailed(t *testing.T) {
	root := bfVault(t, "p")
	good := bfWriteTask(t, root, "p", "", "good", "Good Task", "pending", "")
	bad := bfWriteTask(t, root, "p", "", "bad", "Bad Task", "pending", "")
	bfCommit(t, root, "seed", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	// Two Priority lines is one of the real damage shapes the live corpus carries.
	bfWriteRaw(t, root, bad, "# Bad Task\n\n**Status:** pending\n**Priority:** medium\n**Priority:** high\n\n## Context\n\nbody\n")
	bfCommit(t, root, "break bad", time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC))
	badBefore := bfRead(t, root, bad)

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.Preexisting != 1 {
		t.Fatalf("ps.Preexisting = %d, want 1\n%s", ps.Preexisting, buf.String())
	}
	if ps.Refusals != 0 || ps.Failed != 0 {
		t.Errorf("pre-existing damage must not FAIL the run: ps = %+v", ps)
	}
	if !strings.Contains(buf.String(), "already malformed before this run") {
		t.Errorf("report must name the pre-existing damage:\n%s", buf.String())
	}
	// The malformed file is never written to: its header block is the thing that
	// cannot be trusted, so inserting a field into it would be guessing.
	if after := bfRead(t, root, bad); after != badBefore {
		t.Errorf("a malformed file must be left byte-identical:\nbefore:\n%s\nafter:\n%s", badBefore, after)
	}
	// The healthy file still migrates: one broken file does not strand the rest.
	if got := bfRead(t, root, good); !strings.Contains(got, "**CreateTime:**") {
		t.Errorf("a healthy file must still migrate:\n%s", got)
	}
	// And the vault is NOT declared current while a file could not be processed.
	if ps.WillStampFormat {
		t.Errorf("a skipped malformed file must hold the format stamp down")
	}
	if n, rerr := surface.ReadFormat(root); rerr != nil || n == surface.RequiredDataFormat {
		t.Errorf("ReadFormat = (%d, %v) — must not stamp with a file unprocessed", n, rerr)
	}
}

// TestBoardFieldsModTimeSurvivesAnArchivedStatusRepair is defect 2.
//
// The shipped version wrote the archived file twice — a status repair through a
// writer that force-restamps ModTime to today, then a backfill — and derived
// ModTime in between. This pins that the stored ModTime is the GIT date.
func TestBoardFieldsModTimeSurvivesAnArchivedStatusRepair(t *testing.T) {
	root := bfVault(t, "p")
	rel := bfWriteTask(t, root, "p", "done", "old", "Old Archived", "retired", "")
	when := time.Date(2026, 2, 3, 12, 0, 0, 0, time.UTC)
	bfCommit(t, root, "seed archived", when)

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.Failed != 0 || ps.Applied != 1 {
		t.Fatalf("ps = %+v\n%s", ps, buf.String())
	}
	got := bfRead(t, root, rel)
	if !strings.Contains(got, "**ModTime:** 2026-02-03") {
		t.Errorf("ModTime must be the git date, not today:\n%s", got)
	}
	if strings.Contains(got, "**ModTime:** "+time.Now().UTC().Format("2006-01-02")) {
		t.Errorf("ModTime was restamped to today — the two-write hazard is back:\n%s", got)
	}
	if !strings.Contains(got, "**Status:** done") {
		t.Errorf("the archived status repair did not land:\n%s", got)
	}
}

// TestBoardFieldsPlannerWritesNothing pins that planning is read-only.
//
// It is a BEHAVIOURAL test, not a structural guarantee: planBoardFieldsMigration
// takes a root string and could write. The enforcement is the sourceaudit
// ratchet; this catches a regression in this implementation.
func TestBoardFieldsPlannerWritesNothing(t *testing.T) {
	root := bfVault(t, "p")
	for _, slug := range []string{"a", "b", "c"} {
		bfWriteTask(t, root, "p", "", slug, "Task "+slug, "pending", "")
	}
	bfWriteTask(t, root, "p", "done", "d", "Archived", "retired", "")
	bfCommit(t, root, "seed", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	before := bfTreeSHA(t, root)
	ps, err := planBoardFieldsMigration(root, "")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if ps.ToMigrate != 4 {
		t.Fatalf("ps.ToMigrate = %d, want 4", ps.ToMigrate)
	}
	if after := bfTreeSHA(t, root); after != before {
		t.Errorf("planning wrote to the vault")
	}
}

// TestBoardFieldsWritesEachFileExactlyOnce is D1.
//
// The seam is atomicfile.SetWriteObserver, NOT a counter inside the executor: an
// executor-scoped counter counts writes through the executor, and the
// regression that matters is a second writer appearing beside it.
func TestBoardFieldsWritesEachFileExactlyOnce(t *testing.T) {
	root := bfVault(t, "p")
	bfWriteTask(t, root, "p", "", "active", "Active", "pending", "")
	bfWriteTask(t, root, "p", "done", "arch", "Archived", "retired", "")
	bfWriteTask(t, root, "p", "cancelled", "cxl", "Cancelled", "In Progress", "")
	bfCommit(t, root, "seed", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	counts := map[string]int{}
	var mu sync.Mutex
	restore := atomicfile.SetWriteObserver(func(abs string) {
		if !strings.Contains(filepath.ToSlash(abs), "/tasks/") {
			return
		}
		mu.Lock()
		counts[abs]++
		mu.Unlock()
	})
	defer restore()

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.Applied != 3 {
		t.Fatalf("ps.Applied = %d, want 3\n%s", ps.Applied, buf.String())
	}
	if len(counts) != 3 {
		t.Fatalf("observed writes to %d task files, want 3: %v", len(counts), counts)
	}
	for p, n := range counts {
		if n != 1 {
			t.Errorf("%s was written %d times, want exactly 1 — the two-write shape is back", p, n)
		}
	}
}

// TestBoardFieldsStatusPopulationMatchesMigrateTaskStatus pins the archived
// repair population against `vp migrate task-status`, which is what makes "a
// second write call, not a second decision" checkable rather than asserted.
func TestBoardFieldsStatusPopulationMatchesMigrateTaskStatus(t *testing.T) {
	root := bfVault(t, "p")
	// Shapes that actually differ between a fence-aware detector and a naive one.
	bfWriteTask(t, root, "p", "done", "cased", "Cased", "Retired", "")
	bfWriteTask(t, root, "p", "done", "agrees", "Agrees", "cancelled", "")
	bfWriteTask(t, root, "p", "done", "spaced", "Spaced", "retired   ", "")
	fenced := bfWriteTask(t, root, "p", "done", "fenced", "Fenced", "done", "")
	bfCommit(t, root, "seed", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	// A Status line quoted inside a code fence is sample text, not metadata.
	bfWriteRaw(t, root, fenced, "# Fenced\n\n**Status:** done\n**Priority:** medium\n\n## Context\n\n```\n**Status:** pending\n```\n")
	bfCommit(t, root, "add fenced sample", time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC))

	ps, err := planBoardFieldsMigration(root, "")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	got := map[string]string{}
	for _, p := range ps.Plans {
		if p.StatusTo != "" {
			got[p.Slug] = p.StatusTo
		}
	}

	var sbuf bytes.Buffer
	sum, err := runTaskStatusMigration(root, "", false, &sbuf)
	if err != nil {
		t.Fatalf("task-status: %v", err)
	}
	want := map[string]string{}
	for _, p := range sum.Plans {
		if !p.Failed && !p.Skipped {
			want[p.Slug] = p.Want
		}
	}
	if len(got) != len(want) {
		t.Fatalf("population mismatch: board-fields=%v task-status=%v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("slug %q: board-fields=%q task-status=%q", k, got[k], v)
		}
	}
	if _, repaired := got["fenced"]; repaired {
		t.Errorf("a fenced sample Status line must not be treated as a disagreement")
	}
	if _, repaired := got["agrees"]; repaired {
		t.Errorf("a done/ file reading %q agrees with its directory and must be left alone", "cancelled")
	}
}

// TestBoardFieldsCanonicalOrderOnAPreExistingModTime is defect 6 (RC3).
//
// The fixture is the LIVE shape: an ACTIVE task carrying ModTime and no
// CreateTime, which is what amending a legacy task produces. An archived fixture
// would pass vacuously, because both fields are inserted together there.
func TestBoardFieldsCanonicalOrderOnAPreExistingModTime(t *testing.T) {
	root := bfVault(t, "p")
	rel := bfWriteTask(t, root, "p", "", "amended", "Amended Legacy", "pending", "")
	bfWriteRaw(t, root, rel, "# Amended Legacy\n\n**Status:** pending\n**Priority:** medium\n**ModTime:** 2026-03-04\n\n## Context\n\nbody\n")
	bfCommit(t, root, "seed amended legacy", time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	if _, err := runTaskBoardFieldsMigration(root, "", true, &buf); err != nil {
		t.Fatalf("migration: %v", err)
	}
	got := bfRead(t, root, rel)
	ci := strings.Index(got, "**CreateTime:**")
	mi := strings.Index(got, "**ModTime:**")
	if ci < 0 || mi < 0 {
		t.Fatalf("both fields must be present:\n%s", got)
	}
	if ci > mi {
		t.Errorf("CreateTime must precede ModTime, matching CreateTask:\n%s", got)
	}
	if !strings.Contains(got, "**ModTime:** 2026-03-04") {
		t.Errorf("an existing ModTime is authoritative and must not be overwritten:\n%s", got)
	}
}

// TestBoardFieldsFillsOnlyTheAbsentFields is defect 7's per-field fill.
func TestBoardFieldsFillsOnlyTheAbsentFields(t *testing.T) {
	root := bfVault(t, "p")
	rel := bfWriteTask(t, root, "p", "", "partial", "Partial", "planning", "")
	bfWriteRaw(t, root, rel, "# Partial\n\n**Status:** planning\n**Priority:** medium\n**CreateTime:** 2020-01-01\n**ModTime:** 2020-02-02\n\n## Context\n\nbody\n")
	bfCommit(t, root, "seed partial", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	if _, err := runTaskBoardFieldsMigration(root, "", true, &buf); err != nil {
		t.Fatalf("migration: %v", err)
	}
	got := bfRead(t, root, rel)
	if !strings.Contains(got, "**DataFormat:** "+strconv.Itoa(surface.RequiredDataFormat)) {
		t.Errorf("an absent DataFormat must be filled even though CreateTime is present:\n%s", got)
	}
	if !strings.Contains(got, "**CreateTime:** 2020-01-01") || !strings.Contains(got, "**ModTime:** 2020-02-02") {
		t.Errorf("present values are authoritative and must be untouched:\n%s", got)
	}

	// Idempotent: a second run has nothing absent to fill.
	before := bfRead(t, root, rel)
	var buf2 bytes.Buffer
	ps2, err := runTaskBoardFieldsMigration(root, "", true, &buf2)
	if err != nil {
		t.Fatalf("second migration: %v", err)
	}
	if ps2.Applied != 0 {
		t.Errorf("second run applied %d writes, want 0", ps2.Applied)
	}
	if after := bfRead(t, root, rel); after != before {
		t.Errorf("second run changed the file:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestBoardFieldsRefreshesAStaleDataFormat pins the Chair's cross-task coupling:
// a marker BELOW the current constant is refreshed, not skipped.
func TestBoardFieldsRefreshesAStaleDataFormat(t *testing.T) {
	root := bfVault(t, "p")
	rel := bfWriteTask(t, root, "p", "", "stale", "Stale", "planning", "")
	bfWriteRaw(t, root, rel, "# Stale\n\n**Status:** planning\n**Priority:** medium\n**CreateTime:** 2020-01-01\n**ModTime:** 2020-02-02\n**DataFormat:** 0\n\n## Context\n\nbody\n")
	bfCommit(t, root, "seed stale", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	if _, err := runTaskBoardFieldsMigration(root, "", true, &buf); err != nil {
		t.Fatalf("migration: %v", err)
	}
	got := bfRead(t, root, rel)
	if !strings.Contains(got, "**DataFormat:** "+strconv.Itoa(surface.RequiredDataFormat)) {
		t.Errorf("a DataFormat below the constant must be refreshed:\n%s", got)
	}
}

// TestBoardFieldsScopedRunDoesNotStampVaultFormat is defect 3.
func TestBoardFieldsScopedRunDoesNotStampVaultFormat(t *testing.T) {
	root := bfVault(t, "a", "b")
	bfWriteTask(t, root, "a", "", "ta", "Task A", "pending", "")
	bfWriteTask(t, root, "b", "", "tb", "Task B", "pending", "")
	bfCommit(t, root, "seed", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "a", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if ps.WillStampFormat {
		t.Errorf("a scoped run that leaves project b unmigrated must not predict a stamp")
	}
	if n, rerr := surface.ReadFormat(root); rerr != nil || n == surface.RequiredDataFormat {
		t.Errorf("ReadFormat = (%d, %v) — a scoped run must not stamp the whole vault", n, rerr)
	}
}

// TestBoardFieldsStampsOnceTheLastProjectCompletes proves the stamp is a
// DERIVATION over the whole vault, not merely a refusal on --project.
func TestBoardFieldsStampsOnceTheLastProjectCompletes(t *testing.T) {
	root := bfVault(t, "a", "b")
	bfWriteTask(t, root, "a", "", "ta", "Task A", "pending", "")
	bfWriteTask(t, root, "b", "", "tb", "Task B", "pending", "")
	bfCommit(t, root, "seed", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	if _, err := runTaskBoardFieldsMigration(root, "a", true, &buf); err != nil {
		t.Fatalf("migrate a: %v", err)
	}
	if n, _ := surface.ReadFormat(root); n == surface.RequiredDataFormat {
		t.Fatalf("stamped after only project a migrated")
	}
	// Project a's files are now dirty (uncommitted), which would make the second
	// run skip them; commit so the run sees a clean tree.
	bfCommit(t, root, "land project a", time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC))

	var buf2 bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "b", true, &buf2)
	if err != nil {
		t.Fatalf("migrate b: %v", err)
	}
	if !ps.WillStampFormat {
		t.Errorf("the run completing the vault must predict a stamp: %s", ps.StampReason)
	}
	if n, rerr := surface.ReadFormat(root); rerr != nil || n != surface.RequiredDataFormat {
		t.Errorf("ReadFormat = (%d, %v), want %d — a sequence of scoped runs must stamp when the last completes",
			n, rerr, surface.RequiredDataFormat)
	}
}

// TestBoardFieldsReportAndApplyPlanIdentically is defect 4: one planner, two
// consumers, so report and apply cannot disagree about what would happen.
func TestBoardFieldsReportAndApplyPlanIdentically(t *testing.T) {
	seed := func(t *testing.T) string {
		root := bfVault(t, "p")
		bfWriteTask(t, root, "p", "", "one", "One", "pending", "")
		bfWriteTask(t, root, "p", "done", "two", "Two", "retired", "")
		bfCommit(t, root, "seed", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
		return root
	}
	rootReport := seed(t)
	rootApply := seed(t)

	var b1, b2 bytes.Buffer
	psReport, err := runTaskBoardFieldsMigration(rootReport, "", false, &b1)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	psApply, err := runTaskBoardFieldsMigration(rootApply, "", true, &b2)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(psReport.Plans) != len(psApply.Plans) {
		t.Fatalf("plan lengths differ: %d vs %d", len(psReport.Plans), len(psApply.Plans))
	}
	for i := range psReport.Plans {
		r, a := psReport.Plans[i], psApply.Plans[i]
		// Zero the execution-only fields; everything the PLANNER decided must match.
		a.Applied, a.Failed, a.Skipped, a.Drifted = false, false, false, false
		if r != a {
			t.Errorf("plan entry %d differs:\nreport: %+v\napply:  %+v", i, r, a)
		}
	}
	if psReport.WillStampFormat != psApply.WillStampFormat {
		t.Errorf("stamp prediction differs: report=%v apply=%v", psReport.WillStampFormat, psApply.WillStampFormat)
	}
	if psReport.ToMigrate != psApply.ToMigrate || psReport.StatusRepairs != psApply.StatusRepairs {
		t.Errorf("roll-ups differ: report=%+v apply=%+v", psReport, psApply)
	}
}

// TestBoardFieldsIdempotentOverAFullCorpus pins the property the rehearsal
// measured on the real vault, so the rework cannot lose it.
func TestBoardFieldsIdempotentOverAFullCorpus(t *testing.T) {
	root := bfVault(t, "a", "b")
	bfWriteTask(t, root, "a", "", "one", "One", "pending", "")
	bfWriteTask(t, root, "a", "done", "two", "Two", "retired", "")
	bfWriteTask(t, root, "b", "", "three", "Three", "in_progress", "")
	bfWriteTask(t, root, "b", "cancelled", "four", "Four", "In Progress", "")
	bfCommit(t, root, "seed", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	var b1 bytes.Buffer
	if _, err := runTaskBoardFieldsMigration(root, "", true, &b1); err != nil {
		t.Fatalf("first: %v", err)
	}
	first := bfTreeSHA(t, root)
	bfCommit(t, root, "land first run", time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC))

	var b2 bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", true, &b2)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if ps.Applied != 0 {
		t.Errorf("second run applied %d writes, want 0\n%s", ps.Applied, b2.String())
	}
	if ps.NoWork != 4 {
		t.Errorf("ps.NoWork = %d, want 4 — every file should be current", ps.NoWork)
	}
	_ = first
}

// TestBoardFieldsReportRowDistinguishesTransitionFromNoChange guards the FIX-row
// render, which shipped with nothing asserting it at all.
//
// Two assertions, deliberately — this is report prose, not a write path. What
// must hold is that the two shapes are DISTINGUISHABLE: a row that changes a
// Status says so, and a row that does not carries no Status clause and no
// sentinel. The render previously emitted `Status "cancelled"->"unchanged"`,
// which reads just as naturally as setting the status to the string "unchanged"
// — on the operator's primary gate before a one-time, vault-wide migration.
func TestBoardFieldsReportRowDistinguishesTransitionFromNoChange(t *testing.T) {
	root := bfVault(t, "p")
	bfWriteTask(t, root, "p", "", "moves", "Moves", "pending", "")     // pending -> planning
	bfWriteTask(t, root, "p", "", "stays", "Stays", "in_progress", "") // already valid: no transition
	bfCommit(t, root, "seed", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	if _, err := runTaskBoardFieldsMigration(root, "", false, &buf); err != nil {
		t.Fatalf("migration: %v", err)
	}
	var moves, stays string
	for _, ln := range strings.Split(buf.String(), "\n") {
		switch {
		case strings.HasPrefix(ln, "  FIX ") && strings.Contains(ln, "p/moves "):
			moves = ln
		case strings.HasPrefix(ln, "  FIX ") && strings.Contains(ln, "p/stays "):
			stays = ln
		}
	}
	if moves == "" || stays == "" {
		t.Fatalf("expected a FIX row for each fixture:\n%s", buf.String())
	}

	// A real transition names both ends, and only those.
	if !strings.Contains(moves, `Status "pending" -> "planning"`) {
		t.Errorf("a row that changes Status must name both ends:\n%s", moves)
	}
	// A row with no transition carries no Status clause and invents no value.
	if strings.Contains(stays, "Status ") || strings.Contains(stays, "unchanged") {
		t.Errorf("a row with no Status change must carry no Status clause and no sentinel:\n%s", stays)
	}
}

// TestBoardFieldsNamesTheShadowedPairRatherThanAHashMismatch is a TRUTH fix, not
// a safety fix. The outcome was already safe; the reported cause was false.
//
// 🔴 THE OLD GUARD WAS ACTIVE-ONLY, SO A done/+cancelled/ PAIR PASSED PLANNING,
// and the write was then refused downstream by ApplyTaskMigrationFields'
// WantSHA256 compare -- which reports a HASH MISMATCH. The operator was told the
// file changed under the plan. It had not; the plan had resolved to a different
// file, and anyone diagnosing it was sent to the wrong place. Naming the shadow
// in the planner puts the CAS back to catching what it is for.
//
// The planner has no io.Writer in its signature, so it calls the PURE predicate
// and the renderer prints the cause. Keeping it print-free is a CONVENTION:
// plannerNoWrite guards vault writes, not printing, and nothing goes red if the
// signature is widened and a print added.
func TestBoardFieldsNamesTheShadowedPairRatherThanAHashMismatch(t *testing.T) {
	root := bfVault(t, "p")
	bfWriteTask(t, root, "p", "done", "pair", "Pair", "done", "")
	bfWriteTask(t, root, "p", "cancelled", "pair", "Pair", "cancelled", "")
	bfCommit(t, root, "seed", time.Now())

	var buf bytes.Buffer
	ps, err := runTaskBoardFieldsMigration(root, "", false, &buf)
	if err != nil {
		t.Fatalf("runTaskBoardFieldsMigration: %v", err)
	}
	if ps.Refusals == 0 {
		t.Fatalf("the shadowed cancelled/ copy was not refused at plan time; out:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "the same slug also exists in tasks/done/") {
		t.Errorf("the refusal must NAME the shadowed pair; out:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "resolves active first") {
		t.Errorf("blamed an ACTIVE twin that does not exist -- the false cause this test exists to "+
			"prevent; out:\n%s", buf.String())
	}
	// And it must not be the hash that catches it: a SHA mismatch reported for a
	// shadowed pair is a true refusal with a false reason.
	if strings.Contains(strings.ToLower(buf.String()), "sha256") {
		t.Errorf("a hash mismatch was reported for a shadowed pair; out:\n%s", buf.String())
	}
}
