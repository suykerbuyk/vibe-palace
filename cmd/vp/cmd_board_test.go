// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/taskgraph"
)

// stripDates removes the CreateTime/ModTime header lines from an ACTIVE
// task's on-disk file, simulating a legacy task that predates
// board-reporting-createtime-modtime-fields and was never migrated. There is
// no vault-API writer for this — CreateTime is immutable and ModTime is
// force-restamped by every mutator — so this edits the file directly, the
// same way a pre-migration file would have looked on disk.
func stripDates(t *testing.T, v *storage.Vault, proj, slug string) {
	t.Helper()
	path, err := v.TaskFile(proj, slug)
	if err != nil {
		t.Fatalf("TaskFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read task file: %v", err)
	}
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "**CreateTime:**") || strings.HasPrefix(line, "**ModTime:**") {
			continue
		}
		kept = append(kept, line)
	}
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatalf("write task file: %v", err)
	}
}

func TestRunBoardEmpty(t *testing.T) {
	v := testVault(t)
	var buf bytes.Buffer
	code := runBoard(v, "test-proj", false, &buf)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	out := buf.String()
	for _, section := range []string{"ACTIVE", "ICEBOX", "HISTORY"} {
		if !strings.Contains(out, section) {
			t.Errorf("missing section %q in:\n%s", section, out)
		}
	}
	if strings.Count(out, "(none)") != 3 {
		t.Errorf("expected 3 empty-section markers, got:\n%s", out)
	}
}

func TestRunBoardActiveEpicMixedChildren(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "big-epic", "")
	if err := v.UpdateTaskStatus("test-proj", "big-epic", "in_progress"); err != nil {
		t.Fatalf("UpdateTaskStatus: %v", err)
	}
	mkTask(t, v, "test-proj", "child-open", "big-epic")
	mkTask(t, v, "test-proj", "child-done", "big-epic")
	if err := v.RetireTask("test-proj", "child-done"); err != nil {
		t.Fatalf("RetireTask: %v", err)
	}

	var buf bytes.Buffer
	if code := runBoard(v, "test-proj", false, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	out := buf.String()

	active, _, _ := strings.Cut(out, "ICEBOX")
	if !strings.Contains(active, "EPIC  big-epic") {
		t.Errorf("epic must appear under ACTIVE, not moved to HISTORY:\n%s", out)
	}
	_, history, _ := strings.Cut(out, "HISTORY")
	if strings.Contains(history, "big-epic") {
		t.Errorf("active epic leaked into HISTORY:\n%s", out)
	}

	for _, line := range strings.Split(active, "\n") {
		switch {
		case strings.Contains(line, "child-open"):
			if strings.Contains(line, "completed") {
				t.Errorf("open child must not show a completed date: %q", line)
			}
			if !strings.Contains(line, "created") {
				t.Errorf("open child missing created date: %q", line)
			}
		case strings.Contains(line, "child-done"):
			if !strings.Contains(line, "created") || !strings.Contains(line, "completed") {
				t.Errorf("done child must show both created and completed dates: %q", line)
			}
		}
	}
}

func TestRunBoardIceboxAlwaysRenders(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "icebox-epic", "")
	if err := v.UpdateTaskStatus("test-proj", "icebox-epic", storage.StatusIcebox); err != nil {
		t.Fatalf("icebox epic: %v", err)
	}
	mkTask(t, v, "test-proj", "icebox-child", "icebox-epic")

	mkTask(t, v, "test-proj", "icebox-standalone", "")
	if err := v.UpdateTaskStatus("test-proj", "icebox-standalone", storage.StatusIcebox); err != nil {
		t.Fatalf("icebox standalone: %v", err)
	}

	var buf bytes.Buffer
	if code := runBoard(v, "test-proj", false, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	out := buf.String()
	_, rest, _ := strings.Cut(out, "ICEBOX")
	icebox, _, _ := strings.Cut(rest, "HISTORY")
	if !strings.Contains(icebox, "icebox-epic") {
		t.Errorf("iceboxed epic missing from ICEBOX with no flag:\n%s", out)
	}
	if !strings.Contains(icebox, "icebox-standalone") {
		t.Errorf("iceboxed standalone task missing from ICEBOX with no flag:\n%s", out)
	}
}

func TestRunBoardHistorySupersessionUnconditional(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "successor", "")
	mkTask(t, v, "test-proj", "abandoned", "")
	if err := v.CancelTask("test-proj", "abandoned", "successor"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	mkTask(t, v, "test-proj", "plain-done", "")
	if err := v.RetireTask("test-proj", "plain-done"); err != nil {
		t.Fatalf("RetireTask: %v", err)
	}
	mkTask(t, v, "test-proj", "plain-epic", "")
	mkTask(t, v, "test-proj", "plain-epic-child", "plain-epic")
	if err := v.RetireTask("test-proj", "plain-epic"); err != nil {
		t.Fatalf("RetireTask epic: %v", err)
	}

	var buf bytes.Buffer
	if code := runBoard(v, "test-proj", false, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	out := buf.String()

	if !strings.Contains(out, "cancelled → superseded by successor") {
		t.Errorf("superseded task must render the supersession suffix:\n%s", out)
	}
	// The unconditional-call fix, at the GROUP HEADER: the header format
	// always writes "<epic> → <HistoryLabel()>". HistoryLabel() is called
	// unconditionally for every History-bucket group root — for a plain done
	// epic with no supersession link, it returns the bare "done", so the
	// header still shows the arrow with "done" on the right of it, proving
	// the call was never skipped rather than conditionally omitted.
	if !strings.Contains(out, "EPIC  plain-epic → done") {
		t.Errorf("plain done epic group header must still render '→ done':\n%s", out)
	}
	// At the MEMBER-ROW level the arrow itself is part of HistoryLabel()'s own
	// returned string (only present when superseded), so a plain done member
	// renders the bare status with no arrow — assert that HistoryLabel() was
	// still the thing that populated the column (it is the row's only status
	// text) and that it was not skipped in favor of, say, a blank field.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "plain-done") {
			if !strings.Contains(line, "done") {
				t.Errorf("plain-done row must carry HistoryLabel()'s bare status: %q", line)
			}
		}
	}
}

func TestRunBoardHistorySortsMostRecentFirst(t *testing.T) {
	v := testVault(t)
	day := func(d int) func() time.Time {
		return func() time.Time { return time.Date(2026, time.January, d, 12, 0, 0, 0, time.UTC) }
	}

	v.SetClock(day(1))
	mkTask(t, v, "test-proj", "oldest", "")
	v.SetClock(day(10))
	mkTask(t, v, "test-proj", "middle", "")
	v.SetClock(day(20))
	mkTask(t, v, "test-proj", "newest", "")

	// Retire in an order that does NOT match creation order, and at distinct
	// times, so the assertion is really pinned to ModTime, not creation order.
	v.SetClock(day(11))
	if err := v.RetireTask("test-proj", "oldest"); err != nil {
		t.Fatalf("retire oldest: %v", err)
	}
	v.SetClock(day(30))
	if err := v.RetireTask("test-proj", "newest"); err != nil {
		t.Fatalf("retire newest: %v", err)
	}
	v.SetClock(day(20))
	if err := v.RetireTask("test-proj", "middle"); err != nil {
		t.Fatalf("retire middle: %v", err)
	}

	var buf bytes.Buffer
	if code := runBoard(v, "test-proj", false, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	out := buf.String()
	iNewest := strings.Index(out, "newest")
	iMiddle := strings.Index(out, "middle")
	iOldest := strings.Index(out, "oldest")
	if iNewest < 0 || iMiddle < 0 || iOldest < 0 {
		t.Fatalf("expected all three tasks in output:\n%s", out)
	}
	if !(iNewest < iMiddle && iMiddle < iOldest) {
		t.Errorf("HISTORY must sort most-recent-ModTime-first; got order newest=%d middle=%d oldest=%d in:\n%s",
			iNewest, iMiddle, iOldest, out)
	}
}

func TestRunBoardPriorityConsistentAcrossBuckets(t *testing.T) {
	v := testVault(t)
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "active-pri", Title: "Active Pri", Content: "body", Priority: "critical"})

	v.CreateTask("test-proj", storage.TaskSpec{Slug: "icebox-pri", Title: "Icebox Pri", Content: "body", Priority: "medium"})
	if err := v.UpdateTaskStatus("test-proj", "icebox-pri", storage.StatusIcebox); err != nil {
		t.Fatalf("icebox: %v", err)
	}

	v.CreateTask("test-proj", storage.TaskSpec{Slug: "history-pri", Title: "History Pri", Content: "body", Priority: "low"})
	if err := v.RetireTask("test-proj", "history-pri"); err != nil {
		t.Fatalf("retire: %v", err)
	}

	v.CreateTask("test-proj", storage.TaskSpec{Slug: "no-pri", Title: "No Pri", Content: "body", Priority: ""})

	var buf bytes.Buffer
	if code := runBoard(v, "test-proj", false, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	out := buf.String()

	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "active-pri"):
			if !strings.Contains(line, "critical") {
				t.Errorf("ACTIVE row dropped priority: %q", line)
			}
		case strings.Contains(line, "icebox-pri"):
			if !strings.Contains(line, "medium") {
				t.Errorf("ICEBOX row dropped priority: %q", line)
			}
		case strings.Contains(line, "history-pri"):
			if !strings.Contains(line, "low") {
				t.Errorf("HISTORY row dropped priority: %q", line)
			}
		case strings.Contains(line, "no-pri"):
			if !strings.Contains(line, priorityOrDash("")) {
				t.Errorf("empty-priority row missing dash placeholder: %q", line)
			}
		}
	}
}

func TestRunBoardMissingDatesUnknown(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "no-dates", "")
	stripDates(t, v, "test-proj", "no-dates")

	var buf bytes.Buffer
	if code := runBoard(v, "test-proj", false, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	out := buf.String()
	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "no-dates") {
			found = true
			if !strings.Contains(line, "unknown") {
				t.Errorf("missing-date row must render the literal 'unknown': %q", line)
			}
		}
	}
	if !found {
		t.Fatalf("no-dates task missing from output:\n%s", out)
	}
}

func TestRunBoardStaleParentsFlagged(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "retired-parent", "")
	mkTask(t, v, "test-proj", "still-active-child", "retired-parent")
	if err := v.RetireTask("test-proj", "retired-parent"); err != nil {
		t.Fatalf("RetireTask: %v", err)
	}

	var buf bytes.Buffer
	if code := runBoard(v, "test-proj", false, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	out := buf.String()

	_, history, found := strings.Cut(out, "HISTORY")
	if !found {
		t.Fatalf("no HISTORY section in:\n%s", out)
	}
	if !strings.Contains(history, "retired-parent") || !strings.Contains(history, "still-active-child") {
		t.Errorf("the stale child must still render inside its done parent's HISTORY group:\n%s", out)
	}
	if !strings.Contains(out, "PROBLEMS") || !strings.Contains(out, "stale parent:") {
		t.Errorf("expected a PROBLEMS section naming the stale parent:\n%s", out)
	}
}

// historyRow returns the single HISTORY-section line naming slug.
//
// Scoped to the section on purpose: PROBLEMS also names every stale-parented
// child, so an unscoped search would match two lines and a row assertion could
// be satisfied by the PROBLEMS line instead of the row it is about. Fails on
// zero or more than one match rather than silently picking the first.
func historyRow(t *testing.T, out, slug string) string {
	t.Helper()
	_, rest, found := strings.Cut(out, "HISTORY")
	if !found {
		t.Fatalf("no HISTORY section in:\n%s", out)
	}
	history, _, _ := strings.Cut(rest, "PROBLEMS")
	var hits []string
	for _, line := range strings.Split(history, "\n") {
		if strings.Contains(line, slug) {
			hits = append(hits, line)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly one HISTORY line naming %q, got %d:\n%s", slug, len(hits), history)
	}
	return hits[0]
}

// assertStatusWord asserts slug's HISTORY row still carries want in its status
// column — and refuses to run at all if the slug itself contains want.
//
// 🔴 THE REFUSAL IS THE POINT, AND IT IS WHY THIS IS A FUNCTION RATHER THAN AN
// INLINE CHECK. A row assertion is a substring test over a whole rendered line,
// which already contains the slug, so `want` inside the slug satisfies it no
// matter what the renderer emitted. Every status-column assertion goes through
// here so the hygiene rule is enforced for all of them at once; a new one
// written inline would not be covered, which is the shape of guard this project
// rejects.
//
// It compares against the EXACT want, so it fires only on real vacuity and
// never on a near-miss: a slug of "in-progress-child" cannot satisfy a want of
// "in_progress" (hyphen vs underscore), and that assertion is correctly allowed.
func assertStatusWord(t *testing.T, out, slug, want string) {
	t.Helper()
	if strings.Contains(slug, want) {
		t.Fatalf("fixture slug %q contains status word %q — this assertion would be vacuous", slug, want)
	}
	if row := historyRow(t, out, slug); !strings.Contains(row, want) {
		t.Errorf("%s must still carry its own status word %q: %q", slug, want, row)
	}
}

// TestRunBoardHistoryNonDoneMemberIsNotLabeledCompleted pins the fix: a member
// of a History-bucket group is labeled from its OWN Meta.Done, not from the
// section it was sorted into.
//
// The cancelled and legacy-retired children are the load-bearing rows. Meta.Done
// and a Status == "done" string test diverge on exactly one shape — Done true
// while Status is not literally "done" — so without such a row in the fixture
// the two predicates are indistinguishable and the choice of Meta.Done ships
// unguarded. legacy-retired-child is that shape as the LIVE archived corpus
// actually holds it: every done task in the unmigrated vault still reads
// `Status: retired`, so a Status-keyed predicate would relabel essentially every
// done row in the real vault while this suite stayed green.
//
// 🔴 NO FIXTURE SLUG BELOW MAY CONTAIN A STATUS WORD, AND THAT IS NOT COSMETIC.
// The row assertions are substring checks over a whole rendered line, so a slug
// like "planning-child" would satisfy a `Contains(row, "planning")` status-column
// assertion by itself and the check would pass no matter what the renderer did.
// The slugs are therefore deliberately status-free ("unstarted", "parked",
// "finished", "abandoned", "legacy-archived").
//
// The rule is ENFORCED by assertStatusWord, not by this comment — renaming a
// slug back to its status word fails the test rather than silently re-vacuuming
// the assertion. This paragraph explains why the convention exists; the guard is
// what holds it.
func TestRunBoardHistoryNonDoneMemberIsNotLabeledCompleted(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "finished-epic", "")

	// Non-done members, reached through the normal lifecycle actions.
	mkTask(t, v, "test-proj", "unstarted-child", "finished-epic") // mkTask defaults to planning
	mkTask(t, v, "test-proj", "parked-child", "finished-epic")
	if err := v.UpdateTaskStatus("test-proj", "parked-child", storage.StatusIcebox); err != nil {
		t.Fatalf("icebox child: %v", err)
	}

	// Done members. finished-child is the ordinary shape; abandoned-child and
	// legacy-archived-child are both Done==true with a Status that is not "done".
	mkTask(t, v, "test-proj", "finished-child", "finished-epic")
	if err := v.RetireTask("test-proj", "finished-child"); err != nil {
		t.Fatalf("retire finished-child: %v", err)
	}
	mkTask(t, v, "test-proj", "abandoned-child", "finished-epic")
	if err := v.CancelTask("test-proj", "abandoned-child", ""); err != nil {
		t.Fatalf("cancel abandoned-child: %v", err)
	}
	// The normal lifecycle actions cannot produce Done==true with
	// Status=="retired": RetireTask writes "done", and UpdateTaskStatus refuses
	// "retired" (it is absent from validStatuses). SetTaskMigrationFields is the
	// migration writer that takes an unvalidated status and reaches archived
	// files, so it is how the pre-migration corpus's shape is reproduced here.
	mkTask(t, v, "test-proj", "legacy-archived-child", "finished-epic")
	if err := v.RetireTask("test-proj", "legacy-archived-child"); err != nil {
		t.Fatalf("retire legacy-archived-child: %v", err)
	}
	if err := v.SetTaskMigrationFields("test-proj", "legacy-archived-child", "retired", "", "", ""); err != nil {
		t.Fatalf("stamp legacy retired status: %v", err)
	}

	if err := v.RetireTask("test-proj", "finished-epic"); err != nil {
		t.Fatalf("retire epic: %v", err)
	}

	var buf bytes.Buffer
	if code := runBoard(v, "test-proj", false, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	out := buf.String()

	for _, slug := range []string{"unstarted-child", "parked-child"} {
		row := historyRow(t, out, slug)
		if !strings.Contains(row, "modified") {
			t.Errorf("%s is not done — its row must read \"modified\": %q", slug, row)
		}
		if strings.Contains(row, "completed") {
			t.Errorf("%s is not done — its row must NOT claim a completion: %q", slug, row)
		}
	}

	for _, slug := range []string{"finished-child", "abandoned-child", "legacy-archived-child"} {
		row := historyRow(t, out, slug)
		if !strings.Contains(row, "completed") {
			t.Errorf("%s is Done — its row must still read \"completed\": %q", slug, row)
		}
	}

	// The status column was never the defect; prove the fix did not disturb it.
	// assertStatusWord refuses a slug that would make the check vacuous.
	assertStatusWord(t, out, "unstarted-child", "planning")
	assertStatusWord(t, out, "legacy-archived-child", "retired")
}

// TestRunBoardHistoryMemberUnderCancelledEpicIsNotLabeledCompleted covers the
// other root a History group can have. The group-header assertion pins a
// deliberate scope boundary: a cancelled epic's own header still reads
// "(completed …)" because Meta.Done is true for it, and this task does not
// change done/cancelled rendering.
func TestRunBoardHistoryMemberUnderCancelledEpicIsNotLabeledCompleted(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "cancelled-epic", "")
	mkTask(t, v, "test-proj", "in-progress-child", "cancelled-epic")
	if err := v.UpdateTaskStatus("test-proj", "in-progress-child", storage.StatusInProgress); err != nil {
		t.Fatalf("in_progress child: %v", err)
	}
	if err := v.CancelTask("test-proj", "cancelled-epic", ""); err != nil {
		t.Fatalf("cancel epic: %v", err)
	}

	var buf bytes.Buffer
	if code := runBoard(v, "test-proj", false, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	out := buf.String()

	row := historyRow(t, out, "in-progress-child")
	if !strings.Contains(row, "modified") || strings.Contains(row, "completed") {
		t.Errorf("an in_progress child under a CANCELLED epic must read \"modified\": %q", row)
	}
	// Also exercises assertStatusWord's near-miss case in the suite rather than
	// in prose: the slug carries "progress" but the status renders "in_progress",
	// so the guard correctly does not fire and the assertion is real.
	assertStatusWord(t, out, "in-progress-child", "in_progress")
	header := historyRow(t, out, "cancelled-epic")
	if !strings.Contains(header, "(completed") {
		t.Errorf("the cancelled epic's own header is deliberately unchanged and must still "+
			"read \"(completed …)\": %q", header)
	}
}

func TestRunBoardProjectFlag(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "proj-a", "task-a", "")
	mkTask(t, v, "proj-b", "task-b", "")

	var buf bytes.Buffer
	if code := runBoard(v, "proj-b", false, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "task-b") {
		t.Errorf("expected the explicitly named project's task:\n%s", out)
	}
	if strings.Contains(out, "task-a") {
		t.Errorf("explicit --project must not leak the other project's tasks:\n%s", out)
	}
}

// TestBoardCommandProjectFlagOverridesAutoDetect drives cmdBoard().Run end to
// end (not just runBoard): it chdirs into a directory whose marker names one
// project, then passes --project naming a DIFFERENT one, and asserts the
// explicit flag wins — proving the wiring in cmdBoard's Run closure, not just
// detectTasksProject in isolation.
func TestBoardCommandProjectFlagOverridesAutoDetect(t *testing.T) {
	vaultDir := t.TempDir()
	projDir := t.TempDir()
	marker := filepath.Join(projDir, ".vibe-palace.toml")
	body := "vault_path = \"" + vaultDir + "\"\n\n[project]\nname = \"auto-detected\"\n"
	if err := os.WriteFile(marker, []byte(body), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	t.Chdir(projDir)

	v := storage.NewVault(vaultDir)
	mkTask(t, v, "auto-detected", "auto-task", "")
	mkTask(t, v, "explicit-proj", "explicit-task", "")

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code := cmdBoard().Run([]string{"--project", "explicit-proj", "--json"})
	w.Close()
	os.Stdout = old
	raw, _ := io.ReadAll(r)

	if code != cli.ExitOK {
		t.Fatalf("exit = %d, output:\n%s", code, raw)
	}
	if !strings.Contains(string(raw), "explicit-task") {
		t.Errorf("explicit --project must win over auto-detection:\n%s", raw)
	}
	if strings.Contains(string(raw), "auto-task") {
		t.Errorf("auto-detected project's task must not leak in:\n%s", raw)
	}
}

func TestRunBoardJSONRoundTrips(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "epic-a", "")
	mkTask(t, v, "test-proj", "child-a", "epic-a")
	mkTask(t, v, "test-proj", "lonely", "")
	mkTask(t, v, "test-proj", "iced", "")
	if err := v.UpdateTaskStatus("test-proj", "iced", storage.StatusIcebox); err != nil {
		t.Fatalf("icebox: %v", err)
	}
	mkTask(t, v, "test-proj", "gone", "")
	if err := v.RetireTask("test-proj", "gone"); err != nil {
		t.Fatalf("retire: %v", err)
	}

	var buf bytes.Buffer
	if code := runBoard(v, "test-proj", true, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}

	var got taskgraph.BoardView
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}

	g, err := taskgraph.BuildFromVault(v, "test-proj")
	if err != nil {
		t.Fatalf("BuildFromVault: %v", err)
	}
	want := g.Board()

	membersOf := func(groups []taskgraph.Group) [][]string {
		out := make([][]string, len(groups))
		for i, grp := range groups {
			out[i] = grp.Members
		}
		return out
	}
	toJSON := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	if toJSON(membersOf(got.Active)) != toJSON(membersOf(want.Active)) {
		t.Errorf("Active membership mismatch:\n got=%v\nwant=%v", got.Active, want.Active)
	}
	if toJSON(membersOf(got.Icebox)) != toJSON(membersOf(want.Icebox)) {
		t.Errorf("Icebox membership mismatch:\n got=%v\nwant=%v", got.Icebox, want.Icebox)
	}
	if toJSON(membersOf(got.History)) != toJSON(membersOf(want.History)) {
		t.Errorf("History membership mismatch:\n got=%v\nwant=%v", got.History, want.History)
	}
}

func TestBoardFlagValidation(t *testing.T) {
	var code int
	errs := captureStderr(t, func() { code = cmdBoard().Run([]string{"--nope"}) })
	if code != cli.ExitUser {
		t.Errorf("exit = %d, want ExitUser", code)
	}
	if !strings.Contains(errs, "vp board:") {
		t.Errorf("stderr = %q, want it prefixed with 'vp board:'", errs)
	}
}
