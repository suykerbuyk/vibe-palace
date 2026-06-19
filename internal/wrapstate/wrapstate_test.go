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
		{name: "single header", content: "### Iteration 1 — first (2026-01-01)\n", want: 2},
		{name: "many headers", content: "### Iteration 40 — a\n### Iteration 41 — b\n### Iteration 168 — z\n", want: 169},
		{name: "out of order", content: "### Iteration 168 — z\n### Iteration 40 — a\n", want: 169},
		// The H3 (###) level is canonical; a stale H2 (##) line must NOT be
		// parsed as an iteration header.
		{name: "h2 ignored", content: "## Iteration 999 — wrong level\n### Iteration 7 — right\n", want: 8},
		{name: "only h2 stale fixture", content: "## Iteration 50 — stale format\n", want: 1},
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
