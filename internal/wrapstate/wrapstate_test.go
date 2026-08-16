// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNextIterFromIterationsMD(t *testing.T) {
	tests := []struct {
		name    string
		content string // empty means do not write the file
		want    int
	}{
		{name: "missing file", content: "", want: 1},
		{name: "no headers", content: "# Iterations\n\nno entries yet\n", want: 1},
		{name: "single header h2 canonical", content: "## Iteration 1 — first (2026-01-01)\n", want: 2},
		{name: "many headers", content: "## Iteration 40 — a\n## Iteration 41 — b\n## Iteration 168 — z\n", want: 169},
		{name: "out of order", content: "## Iteration 168 — z\n## Iteration 40 — a\n", want: 169},

		// The reader is tolerant of BOTH levels on purpose. It returns a bare
		// number and so cannot report a wrong level — a strict matcher could only
		// under-count silently, which is the defect these cases pin down.

		// Legacy H3 narratives still count. A file migrated to H2 keeps its
		// history readable even if an H3 header survives somewhere.
		{name: "legacy h3 still counts", content: "### Iteration 7 — legacy\n", want: 8},

		// Mixed levels: the max wins regardless of level. Before the fix, a
		// strict-H3 matcher read this as 8 — the H2 999 was invisible to it.
		{name: "mixed levels take the max", content: "## Iteration 999 — h2\n### Iteration 7 — h3\n", want: 1000},

		// THE rusty-can CASE. An all-H2 file returned 1 — the "fresh project"
		// signal — on a project with real history, so a wrap would have
		// renumbered from scratch on top of it.
		{name: "all h2 file is not a fresh project", content: "## Iteration 50 — real history\n", want: 51},

		// A heading-shaped line inside a fence is sample text, not a header, and
		// must not move the counter.
		{name: "fenced heading ignored", content: "## Iteration 3 — real\n\n```md\n## Iteration 900 — an example in a doc snippet\n```\n", want: 4},
		{name: "fenced tilde heading ignored", content: "## Iteration 3 — sample\n\n~~~\n## Iteration 900 — sample\n~~~\n", want: 4},

		// REGRESSION, from the live iterations.md (line 698). A backtick run that
		// opens AND closes on one line is inline code in prose, not a fence — its
		// info string carries a backtick, which CommonMark forbids in an opening
		// fence. A naive "starts with ```" scanner reads it as a lone OPEN,
		// inverts its fence state, and swallows every heading that follows: it
		// lost 187 of 191 headings and reported iteration 77 on a project at 190.
		{
			name:    "inline code run is not an opening fence",
			content: "## Iteration 3 — real\n\nsee the ```bash tutorial``` for details\n\n## Iteration 190 — also real\n",
			want:    191,
		},
		// The same shape must not break a REAL fence that follows it.
		{
			name:    "real fence still works after an inline code run",
			content: "## Iteration 3 — real\n\nsee ```bash tutorial``` here\n\n```md\n## Iteration 900 — sample\n```\n\n## Iteration 190 — real\n",
			want:    191,
		},
		// A closing delimiter carries no info string; "```go" mid-fence is content.
		{
			name:    "info string only opens, bare run closes",
			content: "## Iteration 5 — real\n\n```go\n// ## Iteration 900 — sample\n```\n\n## Iteration 7 — real\n",
			want:    8,
		},
		// An indented 4+ space line is an indented code block, not a fence
		// delimiter, and must not toggle fence state.
		{
			name:    "four-space indent is not a fence delimiter",
			content: "## Iteration 3 — real\n\n    ```\n\n## Iteration 190 — real\n",
			want:    191,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "iterations.md")
			if tc.content != "" {
				if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
					t.Fatalf("write iterations.md: %v", err)
				}
			} else {
				// keep path pointing at a non-existent file
				path = filepath.Join(dir, "missing.md")
			}
			got, err := NextIterFromIterationsMD(path)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestNextIterFromIterationsMD_EmptyPath(t *testing.T) {
	if got, err := NextIterFromIterationsMD(""); err != nil || got != 1 {
		t.Errorf("empty path: got (%d, %v), want (1, nil)", got, err)
	}
}

// FormatIterationHeader is the writer half of the heading contract: the server
// owns the canonical H2 header, so the file can never drift to the wrong level.
func TestFormatIterationHeader(t *testing.T) {
	if got, want := FormatIterationHeader(191, "what changed"), "## Iteration 191 — what changed"; got != want {
		t.Fatalf("FormatIterationHeader = %q, want %q", got, want)
	}
	// Title is trimmed so the composed header is stable regardless of caller
	// whitespace.
	if got, want := FormatIterationHeader(7, "  padded  "), "## Iteration 7 — padded"; got != want {
		t.Fatalf("FormatIterationHeader did not trim title: %q, want %q", got, want)
	}
	// The unpadded form is pinned deliberately: zero-padding was DECLINED by the
	// operator, because a padded header only helps `grep | sort` over a cat-ed
	// archive — the access path vp_get_iteration retires.
	if got, want := FormatIterationHeader(7, "t"), "## Iteration 7 — t"; got != want {
		t.Fatalf("FormatIterationHeader must stay unpadded: %q, want %q", got, want)
	}
}

// TestFormatIterationHeaderStripsDoubledPrefix pins the WRITER half of the
// doubled-prefix defect. rezbldrvault holds five headings shaped
// "## Iteration 40 — Iteration 40 — …", and they exist because this function
// faithfully prefixed a title that already carried the prefix. The check and the
// one-shot migration cleaned the five up; without this strip the next caller who
// passes an already-prefixed title writes the sixth, and the repair regenerates
// its own work.
func TestFormatIterationHeaderStripsDoubledPrefix(t *testing.T) {
	// The exact shape the live archive carried.
	if got, want := FormatIterationHeader(40, "Iteration 40 — Foo"), "## Iteration 40 — Foo"; got != want {
		t.Errorf("FormatIterationHeader(40, %q) = %q, want %q", "Iteration 40 — Foo", got, want)
	}
	// Doubled more than once: a strip that fixes only the first layer leaves a
	// heading that still fails HasDoubledPrefix while looking repaired.
	if got, want := FormatIterationHeader(40, "Iteration 40 — Iteration 40 — Foo"), "## Iteration 40 — Foo"; got != want {
		t.Errorf("FormatIterationHeader did not strip a twice-doubled title: %q, want %q", got, want)
	}
	// A DIFFERENT number in the prefix is still a doubled prefix — the corruption
	// is the shape, not the agreement.
	if got, want := FormatIterationHeader(41, "Iteration 40 — Foo"), "## Iteration 41 — Foo"; got != want {
		t.Errorf("FormatIterationHeader(41, %q) = %q, want %q", "Iteration 40 — Foo", got, want)
	}
	// A title that merely MENTIONS an iteration mid-sentence must survive intact.
	// The regex is anchored precisely so this is not collateral damage.
	const mentions = "revisits the Iteration 5 — decision"
	if got, want := FormatIterationHeader(12, mentions), "## Iteration 12 — "+mentions; got != want {
		t.Errorf("FormatIterationHeader mangled a mid-sentence mention: %q, want %q", got, want)
	}
	// The composed header is canonical by construction — the writer cannot emit
	// something its own oracle rejects.
	if h := FormatIterationHeader(40, "Iteration 40 — Foo"); !IsCanonicalHeader(h) {
		t.Errorf("writer emitted a header its own oracle rejects: %q", h)
	}
}

// ContainsIterationHeader guards the tool door: the server composes the header,
// so a narrative body carrying its own would double it — except a heading-shaped
// line inside a code fence, which is sample text, not a header (the iter-184
// lesson).
func TestContainsIterationHeader(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "plain prose has none", content: "just some prose with no header at all\n", want: false},
		{name: "h2 iteration header detected", content: "## Iteration 191 — smuggled\n\nbody\n", want: true},
		{name: "h3 iteration header detected", content: "### Iteration 192 — also smuggled\n", want: true},
		{name: "h3 subsection is not an iteration header", content: "### Phase 1 — adapter\n\nbody\n", want: false},
		{name: "heading inside a fence is sample text", content: "body\n\n```md\n### Iteration 900 — quoted example\n```\n", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContainsIterationHeader(tc.content); got != tc.want {
				t.Fatalf("ContainsIterationHeader = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestClassifyWrapShape(t *testing.T) {
	tests := []struct {
		name string
		in   Result
		want WrapShape
	}{
		{
			name: "fresh-feature beats planning",
			in: Result{
				CommitsSinceLastIter: []CommitInfo{{SHA: "abc"}},
				TaskDeltas:           TaskDeltas{Added: []string{"x"}},
			},
			want: ShapeFreshFeature,
		},
		{
			name: "planning when no commits but tasks added",
			in:   Result{TaskDeltas: TaskDeltas{Added: []string{"x"}}},
			want: ShapePlanning,
		},
		{
			name: "bookkeeping when no signals",
			in:   Result{},
			want: ShapeBookkeeping,
		},
		{
			name: "retired-only is bookkeeping",
			in:   Result{TaskDeltas: TaskDeltas{Retired: []string{"x"}}},
			want: ShapeBookkeeping,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyWrapShape(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestComputeTaskDeltas(t *testing.T) {
	snap := Snapshot{
		Active:    []string{"alpha", "beta", "gamma"},
		Done:      []string{"old-done"},
		Cancelled: []string{"old-cancel"},
	}
	// alpha stayed active; beta moved to done; gamma moved to cancelled;
	// delta is brand new active; old-done/old-cancel unchanged.
	got := ComputeTaskDeltas(snap,
		[]string{"alpha", "delta"},      // live active
		[]string{"old-done", "beta"},    // live done
		[]string{"old-cancel", "gamma"}, // live cancelled
	)
	want := TaskDeltas{
		Added:     []string{"delta"},
		Retired:   []string{"beta"},
		Cancelled: []string{"gamma"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestComputeTaskDeltas_EmptySnapshot(t *testing.T) {
	// Empty snapshot ⇒ every live active reads as added; nothing retired.
	got := ComputeTaskDeltas(Snapshot{}, []string{"a", "b"}, nil, nil)
	if !reflect.DeepEqual(got.Added, []string{"a", "b"}) {
		t.Errorf("added = %v, want [a b]", got.Added)
	}
	if len(got.Retired) != 0 || len(got.Cancelled) != 0 {
		t.Errorf("expected no retired/cancelled, got %+v", got)
	}
}

func TestComputeTaskDeltas_NeverNil(t *testing.T) {
	got := ComputeTaskDeltas(Snapshot{}, nil, nil, nil)
	if got.Added == nil || got.Retired == nil || got.Cancelled == nil {
		t.Errorf("delta slices must be non-nil, got %+v", got)
	}
}

func TestEnumerateLiveTasksFS(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("a.md")
	mustWrite("b.md")
	mustWrite(".hidden.md") // skipped
	mustWrite("notes.txt")  // skipped (non-md)
	mustWrite("done/c.md")
	mustWrite("cancelled/d.md")

	active, done, cancelled, err := EnumerateLiveTasksFS(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(active, []string{"a", "b"}) {
		t.Errorf("active = %v, want [a b]", active)
	}
	if !reflect.DeepEqual(done, []string{"c"}) {
		t.Errorf("done = %v, want [c]", done)
	}
	if !reflect.DeepEqual(cancelled, []string{"d"}) {
		t.Errorf("cancelled = %v, want [d]", cancelled)
	}
}

func TestEnumerateLiveTasksFS_MissingDir(t *testing.T) {
	active, done, cancelled, err := EnumerateLiveTasksFS(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("missing dir should not error, got %v", err)
	}
	if len(active) != 0 || len(done) != 0 || len(cancelled) != 0 {
		t.Errorf("missing dir should yield empty sets, got %v %v %v", active, done, cancelled)
	}
}

func TestReadSnapshot_RoundTrip(t *testing.T) {
	root := t.TempDir()
	in := StampInput{
		Project:     "demo",
		ProjectRoot: root,
		TasksDir:    "", // empty tasks dir ⇒ empty snapshot sets
		Iter:        7,
	}
	// Seed a vault tasks dir so the snapshot has content.
	tasksDir := filepath.Join(t.TempDir(), "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "open-task.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	in.TasksDir = tasksDir

	res, err := StampIter(in)
	if err != nil {
		t.Fatalf("StampIter: %v", err)
	}
	if res.Iter != 7 || res.BytesWritten == 0 || res.SnapshotBytes == 0 {
		t.Errorf("unexpected stamp result: %+v", res)
	}

	// last-iter file content.
	data, err := os.ReadFile(filepath.Join(root, AnchorDir, AnchorFile))
	if err != nil {
		t.Fatalf("read last-iter: %v", err)
	}
	if string(data) != "7\n" {
		t.Errorf("last-iter = %q, want %q", string(data), "7\n")
	}

	// Snapshot round-trips through ReadSnapshot.
	snap, err := ReadSnapshot(root)
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	if snap.IterN != 7 {
		t.Errorf("snapshot IterN = %d, want 7", snap.IterN)
	}
	if !reflect.DeepEqual(snap.Active, []string{"open-task"}) {
		t.Errorf("snapshot Active = %v, want [open-task]", snap.Active)
	}
}

func TestReadSnapshot_Missing(t *testing.T) {
	snap, err := ReadSnapshot(t.TempDir())
	if err != nil {
		t.Fatalf("missing snapshot should not error, got %v", err)
	}
	if snap.IterN != 0 || len(snap.Active) != 0 {
		t.Errorf("missing snapshot should be empty, got %+v", snap)
	}
}

func TestStampIter_Validation(t *testing.T) {
	if _, err := StampIter(StampInput{ProjectRoot: "", Iter: 1}); err == nil {
		t.Error("empty project_path should error")
	}
	if _, err := StampIter(StampInput{ProjectRoot: "relative/path", Iter: 1}); err == nil {
		t.Error("relative project_path should error")
	}
	if _, err := StampIter(StampInput{ProjectRoot: t.TempDir(), Iter: 0}); err == nil {
		t.Error("iter < 1 should error")
	}
}

func TestTestCountsFromTestingMD(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantUnit    int
		wantIntegr  int
		wantWarning bool
	}{
		{
			name:       "bold approx headline",
			body:       "Testing\n\n**~2291 tests** across 48 packages + **1 integration test**.\n",
			wantUnit:   2291,
			wantIntegr: 1,
		},
		{
			name:     "plain count no integration",
			body:     "We have 42 tests in this repo.\n",
			wantUnit: 42,
		},
		{
			name:        "no count found",
			body:        "This document has no headline counts.\n",
			wantWarning: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "TESTING.md")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			u, i, l, warn := testCountsFromTestingMD(path)
			if u != tc.wantUnit {
				t.Errorf("unit = %d, want %d", u, tc.wantUnit)
			}
			if i != tc.wantIntegr {
				t.Errorf("integration = %d, want %d", i, tc.wantIntegr)
			}
			if l != 0 {
				t.Errorf("lint = %d, want 0", l)
			}
			if (warn != "") != tc.wantWarning {
				t.Errorf("warning = %q, wantWarning=%v", warn, tc.wantWarning)
			}
		})
	}
}

func TestTestCountsFromTestingMD_Missing(t *testing.T) {
	_, _, _, warn := testCountsFromTestingMD(filepath.Join(t.TempDir(), "nope.md"))
	if warn == "" {
		t.Error("missing file should yield a warning")
	}
}
