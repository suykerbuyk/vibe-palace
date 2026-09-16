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
