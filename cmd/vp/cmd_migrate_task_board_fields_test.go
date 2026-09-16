// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
	statusSum, fieldsSum, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if statusSum.Failed != 0 || fieldsSum.Failed != 0 {
		t.Fatalf("unexpected failures: status=%+v fields=%+v\n%s", statusSum, fieldsSum, buf.String())
	}
	if fieldsSum.Applied != 1 || fieldsSum.ToMigrate != 1 {
		t.Fatalf("fieldsSum = %+v, want Applied=1 ToMigrate=1", fieldsSum)
	}
	plan := fieldsSum.Plans[0]
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
	_, fieldsSum, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	plan := fieldsSum.Plans[0]
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
	statusSum, fieldsSum, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if statusSum.Failed != 0 || fieldsSum.Failed != 0 {
		t.Fatalf("unexpected failures: %+v %+v\n%s", statusSum, fieldsSum, buf.String())
	}
	if fieldsSum.Applied != 2 {
		t.Fatalf("fieldsSum.Applied = %d, want 2", fieldsSum.Applied)
	}
	for _, slug := range []string{"finished", "dropped"} {
		for _, p := range fieldsSum.Plans {
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
	statusSum, fieldsSum, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if statusSum.Failed != 0 || fieldsSum.Failed != 0 {
		t.Fatalf("unexpected failures: %+v %+v\n%s", statusSum, fieldsSum, buf.String())
	}
	var destPlan *boardFieldsPlan
	for i := range fieldsSum.Plans {
		if fieldsSum.Plans[i].Project == "b" && fieldsSum.Plans[i].Slug == slug {
			destPlan = &fieldsSum.Plans[i]
		}
	}
	if destPlan == nil {
		t.Fatalf("no plan found for b/%s; plans=%+v", slug, fieldsSum.Plans)
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
	statusSum, fieldsSum, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if statusSum.Failed != 0 || fieldsSum.Failed != 0 {
		t.Fatalf("unexpected failures: %+v %+v\n%s", statusSum, fieldsSum, buf.String())
	}
	var destPlan *boardFieldsPlan
	for i := range fieldsSum.Plans {
		if fieldsSum.Plans[i].Project == "c" && fieldsSum.Plans[i].Slug == slug {
			destPlan = &fieldsSum.Plans[i]
		}
	}
	if destPlan == nil {
		t.Fatalf("no plan found for c/%s; plans=%+v", slug, fieldsSum.Plans)
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
	statusSum, fieldsSum, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if statusSum.Failed != 0 {
		t.Fatalf("unexpected phase-1 failures: %+v\n%s", statusSum, buf.String())
	}

	var activePlan *boardFieldsPlan
	for i := range fieldsSum.Plans {
		p := &fieldsSum.Plans[i]
		if p.Project == "d" && p.Slug == "x" && p.Dir == "" {
			activePlan = p
		}
	}
	if activePlan == nil {
		t.Fatalf("no plan found for active d/x; plans=%+v", fieldsSum.Plans)
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
	if fieldsSum.UnknownDates < 1 {
		t.Errorf("fieldsSum.UnknownDates = %d, want >= 1", fieldsSum.UnknownDates)
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
	if fieldsSum.ShadowRefusals < 1 {
		t.Errorf("fieldsSum.ShadowRefusals = %d, want >= 1 (d's own stray cancelled/x.md shadows its active x.md)", fieldsSum.ShadowRefusals)
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
	rel := bfWriteTask(t, root, "p", "", "current", "Current", "planning", "**CreateTime:** 2025-01-01\n**ModTime:** 2025-01-01\n**DataFormat:** 1")
	before := bfRead(t, root, rel)
	bfCommit(t, root, "seed already-migrated file", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	_, fieldsSum, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if fieldsSum.AlreadyMigrated != 1 || fieldsSum.ToMigrate != 0 || fieldsSum.Applied != 0 {
		t.Fatalf("fieldsSum = %+v, want AlreadyMigrated=1 ToMigrate=0 Applied=0", fieldsSum)
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
	statusSum, fieldsSum, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	_ = statusSum
	if fieldsSum.ShadowRefusals != 1 {
		t.Fatalf("fieldsSum.ShadowRefusals = %d, want 1", fieldsSum.ShadowRefusals)
	}
	if fieldsSum.Applied != 1 {
		t.Fatalf("fieldsSum.Applied = %d, want 1 (the active copy must still migrate)", fieldsSum.Applied)
	}
	doneAfter := bfRead(t, root, doneRel)
	if doneBefore != doneAfter {
		t.Errorf("the shadowed archived file must be left byte-identical:\nbefore:\n%s\nafter:\n%s", doneBefore, doneAfter)
	}
	activeAfter := bfRead(t, root, activeRel)
	if !strings.Contains(activeAfter, "**CreateTime:**") {
		t.Errorf("the active file must still be migrated:\n%s", activeAfter)
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
	statusSum, fieldsSum, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if statusSum.Applied != 1 {
		t.Fatalf("expected phase 1 to repair the file: %+v\n%s", statusSum, buf.String())
	}
	if fieldsSum.Dirty != 0 {
		t.Errorf("fieldsSum.Dirty = %d, want 0 — phase 1's own write must not dirty-skip phase 2", fieldsSum.Dirty)
	}
	if fieldsSum.Applied != 1 {
		t.Fatalf("fieldsSum.Applied = %d, want 1 — phase 2 must still backfill the file phase 1 touched", fieldsSum.Applied)
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
	_, fieldsSum, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if fieldsSum.Dirty != 1 {
		t.Fatalf("fieldsSum.Dirty = %d, want 1", fieldsSum.Dirty)
	}
	if fieldsSum.Applied != 0 {
		t.Fatalf("fieldsSum.Applied = %d, want 0 — a dirty file must not be written", fieldsSum.Applied)
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
	statusSum, fieldsSum, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if statusSum.Failed != 0 || fieldsSum.Failed != 0 || statusSum.Dirty != 0 || fieldsSum.Dirty != 0 {
		t.Fatalf("expected a fully clean run: status=%+v fields=%+v", statusSum, fieldsSum)
	}
	if n, err := surface.ReadFormat(root); err != nil || n != surface.RequiredDataFormat {
		t.Errorf("ReadFormat = (%d, %v), want (%d, nil)", n, err, surface.RequiredDataFormat)
	}
	if !strings.Contains(buf.String(), fmt.Sprintf("RequiredDataFormat stamped at %d.", surface.RequiredDataFormat)) {
		t.Errorf("report must confirm the stamp:\n%s", buf.String())
	}
}

func TestRunTaskBoardFieldsPhase1FailureBlocksPhase2AndFormatStamp(t *testing.T) {
	root := bfVault(t, "p")
	// Phase 1's own shadow-slug hazard: a slug present in BOTH tasks/ and
	// tasks/done/ makes runTaskStatusMigration refuse and count Failed.
	bfWriteTask(t, root, "p", "", "dup", "Dup Active", "planning", "")
	bfWriteTask(t, root, "p", "done", "dup", "Dup Archived", "In Progress", "") // disagrees with done/, so phase 1 would try to fix it
	// A second, ordinary task so phase 2 would have SOMETHING to do if it ran.
	bfWriteTask(t, root, "p", "", "ordinary", "Ordinary", "planning", "")
	bfCommit(t, root, "seed phase-1-failure fixture", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	statusSum, fieldsSum, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if statusSum.Failed == 0 {
		t.Fatalf("expected phase 1 to report a failure from its own shadow-slug guard, got %+v\n%s", statusSum, buf.String())
	}
	if fieldsSum.Scanned != 0 {
		t.Errorf("phase 2 must not have run at all: fieldsSum = %+v", fieldsSum)
	}
	if !strings.Contains(buf.String(), "phase 2 will NOT run") {
		t.Errorf("report must say phase 2 did not run:\n%s", buf.String())
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
	_, fieldsSum, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if fieldsSum.Plans[0].StatusFrom != "pending" || fieldsSum.Plans[0].StatusTo != "planning" {
		t.Fatalf("plan = %+v, want StatusFrom=pending StatusTo=planning", fieldsSum.Plans[0])
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
	statusSum, fieldsSum, err := runTaskBoardFieldsMigration(root, "", false, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if statusSum.Applied != 0 || fieldsSum.Applied != 0 {
		t.Fatalf("report mode must apply nothing: status=%+v fields=%+v", statusSum, fieldsSum)
	}
	if fieldsSum.ToMigrate != 1 {
		t.Fatalf("fieldsSum.ToMigrate = %d, want 1 (report mode must still compute what WOULD change)", fieldsSum.ToMigrate)
	}
	after := bfRead(t, root, rel)
	if before != after {
		t.Errorf("report mode wrote to disk:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if n, err := surface.ReadFormat(root); err != nil || n != 0 {
		t.Errorf("ReadFormat = (%d, %v), want (0, nil) — report mode must not stamp anything", n, err)
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
	statusSum, fieldsSum, err := runTaskBoardFieldsMigration(root, "", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if statusSum.Applied != 1 {
		t.Fatalf("expected phase 1 to apply one write: status=%+v", statusSum)
	}
	// Phase 2 must reach BOTH files: the plain active one, and the archived
	// one phase 1 itself just repaired — its own write must not read as
	// "dirty" and get skipped (see the phase1Written guard in
	// runTaskBoardFieldsMigration).
	if fieldsSum.Applied != 2 {
		t.Fatalf("expected phase 2 to apply two writes (including the file phase 1 touched): fields=%+v", fieldsSum)
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
	_, fieldsSum, err := runTaskBoardFieldsMigration(root, "b", true, &buf)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if fieldsSum.Applied != 1 {
		t.Fatalf("fieldsSum.Applied = %d, want 1", fieldsSum.Applied)
	}
	if fieldsSum.Plans[0].CreateTime != "2026-01-01" {
		t.Errorf("CreateTime = %q, want 2026-01-01 even with --project b", fieldsSum.Plans[0].CreateTime)
	}
}
