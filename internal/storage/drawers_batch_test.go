// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"
)

// batchOf builds n drawers with distinct content, so each gets a distinct
// deterministic ID.
func batchOf(prefix string, n int) []Drawer {
	ds := make([]Drawer, n)
	for i := range n {
		ds[i] = Drawer{
			Hall:       "facts",
			Content:    fmt.Sprintf("%s-%d", prefix, i),
			SourceType: "session",
			FiledAt:    "2026-08-25T00:00:00Z",
		}
	}
	return ds
}

func lineCount(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(b) == 0 {
		return 0
	}
	return strings.Count(strings.TrimRight(string(b), "\n"), "\n") + 1
}

// TestAppendDrawersAppendsWholeBatch is the count contract: a batch of N new
// drawers appends N, and every one of them is readable afterwards.
func TestAppendDrawersAppendsWholeBatch(t *testing.T) {
	v := testVault(t)
	const project, wing, room = "proj", "wing-a", "room-1"

	n, err := v.AppendDrawers(project, wing, room, batchOf("chunk", 25))
	if err != nil {
		t.Fatalf("AppendDrawers: %v", err)
	}
	if n != 25 {
		t.Fatalf("appended = %d, want 25", n)
	}

	got, err := v.ListDrawers(project, wing, room)
	if err != nil {
		t.Fatalf("ListDrawers: %v", err)
	}
	if len(got) != 25 {
		t.Fatalf("ListDrawers = %d drawers, want 25", len(got))
	}

	path, err := v.DrawerFile(project, wing, room)
	if err != nil {
		t.Fatal(err)
	}
	if lc := lineCount(t, path); lc != 25 {
		t.Fatalf("file has %d lines, want 25", lc)
	}
}

// TestAppendDrawersReappendIsZeroAndWritesNothing is the property the backfill
// turns on: re-ingesting an archive that is already filed must add nothing AND
// must not rewrite the room. A whole-file rewrite here is what made a re-run
// cost the same as the first run.
func TestAppendDrawersReappendIsZeroAndWritesNothing(t *testing.T) {
	v := testVault(t)
	const project, wing, room = "proj", "wing-a", "room-1"
	batch := batchOf("chunk", 12)

	if n, err := v.AppendDrawers(project, wing, room, batch); err != nil || n != 12 {
		t.Fatalf("first AppendDrawers = (%d, %v), want (12, nil)", n, err)
	}

	path, err := v.DrawerFile(project, wing, room)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	n, err := v.AppendDrawers(project, wing, room, batch)
	if err != nil {
		t.Fatalf("re-append: %v", err)
	}
	if n != 0 {
		t.Fatalf("re-append appended = %d, want 0", n)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("re-append rewrote the room file (%d bytes -> %d bytes)", len(before), len(after))
	}
}

// TestAppendDrawersPartialOverlap: only the drawers not already filed land, and
// the count reports exactly those.
func TestAppendDrawersPartialOverlap(t *testing.T) {
	v := testVault(t)
	const project, wing, room = "proj", "wing-a", "room-1"

	if _, err := v.AppendDrawers(project, wing, room, batchOf("chunk", 10)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// First 10 are duplicates, next 6 are new.
	n, err := v.AppendDrawers(project, wing, room, batchOf("chunk", 16))
	if err != nil {
		t.Fatalf("AppendDrawers: %v", err)
	}
	if n != 6 {
		t.Fatalf("appended = %d, want 6", n)
	}
	got, _ := v.ListDrawers(project, wing, room)
	if len(got) != 16 {
		t.Fatalf("ListDrawers = %d, want 16", len(got))
	}
}

// TestAppendDrawersDedupsWithinBatch: two chunks with identical content inside
// ONE batch collide on the deterministic ID. The in-call set must catch that,
// because there is no on-disk line to catch it for them.
func TestAppendDrawersDedupsWithinBatch(t *testing.T) {
	v := testVault(t)
	const project, wing, room = "proj", "wing-a", "room-1"

	same := Drawer{Hall: "facts", Content: "identical", SourceType: "session", FiledAt: "2026-08-25T00:00:00Z"}
	n, err := v.AppendDrawers(project, wing, room, []Drawer{same, same, same})
	if err != nil {
		t.Fatalf("AppendDrawers: %v", err)
	}
	if n != 1 {
		t.Fatalf("appended = %d, want 1", n)
	}
	got, _ := v.ListDrawers(project, wing, room)
	if len(got) != 1 {
		t.Fatalf("ListDrawers = %d, want 1", len(got))
	}
}

func TestAppendDrawersEmptyBatch(t *testing.T) {
	v := testVault(t)
	n, err := v.AppendDrawers("proj", "wing-a", "room-1", nil)
	if err != nil {
		t.Fatalf("AppendDrawers(nil): %v", err)
	}
	if n != 0 {
		t.Fatalf("appended = %d, want 0", n)
	}
	path, err := v.DrawerFile("proj", "wing-a", "room-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty batch created %s (stat err = %v)", path, err)
	}
}

func TestAppendDrawersInvalidSlug(t *testing.T) {
	v := testVault(t)
	if _, err := v.AppendDrawers("BAD PROJECT", "wing-a", "room-1", batchOf("c", 1)); err == nil {
		t.Error("AppendDrawers with invalid slug should return error")
	}
}

// TestAppendDrawerWrapperKeepsDuplicateError pins the contract MoveDrawer and
// the MCP drawer tools depend on: the n=1 entry point still ERRORS on a
// duplicate even though the batch entry point reports a count instead.
func TestAppendDrawerWrapperKeepsDuplicateError(t *testing.T) {
	v := testVault(t)
	d := Drawer{Hall: "facts", Content: "once", SourceType: "session", FiledAt: "2026-08-25T00:00:00Z"}

	if err := v.AppendDrawer("proj", "wing-a", "room-1", d); err != nil {
		t.Fatalf("first append: %v", err)
	}
	err := v.AppendDrawer("proj", "wing-a", "room-1", d)
	if err == nil {
		t.Fatal("duplicate append should return error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %q, want it to contain %q", err, "already exists")
	}
	// IndexTranscript is no longer the matcher, but MoveDrawer and the drawer
	// tools still read this string; the ID must still be in it.
	if !strings.Contains(err.Error(), DrawerID("wing-a", d.Content)) {
		t.Fatalf("error = %q, want it to name the drawer ID", err)
	}
}

// TestReadDrawerFileSkipsMalformedLine is the H4 prerequisite: an append
// primitive that is not a whole-file replace can leave a torn final line, and
// ONE torn line must not take the whole room down with it.
func TestReadDrawerFileSkipsMalformedLine(t *testing.T) {
	v := testVault(t)
	const project, wing, room = "proj", "wing-a", "room-1"

	if _, err := v.AppendDrawers(project, wing, room, batchOf("good", 3)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	path, err := v.DrawerFile(project, wing, room)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a torn append: a truncated JSON object with no closing brace.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"deadbeef","content":"tor`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := v.ListDrawers(project, wing, room)
	if err != nil {
		t.Fatalf("ListDrawers over a torn line must not fail the room: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListDrawers = %d drawers, want 3 (the torn line dropped)", len(got))
	}

	// And a subsequent append must still work over the torn file — WITHOUT
	// swallowing its own first record into the torn one. The torn line has no
	// trailing newline, so an unhealed append would concatenate the two and
	// lose a good drawer on top of the damaged one.
	n, err := v.AppendDrawers(project, wing, room, batchOf("after", 2))
	if err != nil {
		t.Fatalf("AppendDrawers over a torn line: %v", err)
	}
	if n != 2 {
		t.Fatalf("appended = %d, want 2", n)
	}

	got, err = v.ListDrawers(project, wing, room)
	if err != nil {
		t.Fatalf("ListDrawers after append over torn line: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("ListDrawers = %d drawers, want 5 (3 good + 2 new, torn line dropped)", len(got))
	}
	for _, want := range []string{"after-0", "after-1"} {
		if !slices.ContainsFunc(got, func(d Drawer) bool { return d.Content == want }) {
			t.Errorf("drawer %q was swallowed by the torn line", want)
		}
	}
}

// TestAppendDrawersOneReadOneWrite is the structural half of the fix, and it is
// a source assertion on purpose: the defect this unit closes is not a wrong
// RESULT, it is a right result computed O(drawers) times. A behavioural test
// cannot see the difference between one read and twenty-five, so the shape is
// pinned here — exactly one os.ReadFile and exactly one appendUnderLock in the
// body, and neither of them inside a loop.
func TestAppendDrawersOneReadOneWrite(t *testing.T) {
	fn := findFuncDecl(t, "drawers.go", "AppendDrawers")

	reads := countCalls(fn, "os", "ReadFile")
	if reads != 1 {
		t.Errorf("AppendDrawers makes %d os.ReadFile calls, want exactly 1", reads)
	}
	appends := countCalls(fn, "v", "appendUnderLock")
	if appends != 1 {
		t.Errorf("AppendDrawers makes %d appendUnderLock calls, want exactly 1", appends)
	}
	if callsInsideLoop(fn, "os", "ReadFile") {
		t.Error("AppendDrawers reads the room file inside a loop — the quadratic term is back")
	}
	if callsInsideLoop(fn, "v", "appendUnderLock") {
		t.Error("AppendDrawers writes inside a loop — the quadratic term is back")
	}
	// The whole-file replace must be gone from this path: routing back to
	// atomicfile.Write reintroduces the O(N) rewrite per append.
	if countCalls(fn, "atomicfile", "Write") != 0 {
		t.Error("AppendDrawers calls atomicfile.Write — it must append via F4, not replace the file")
	}
}

// TestAppendDrawerIsAThinWrapper pins that the n=1 entry point does not carry a
// second copy of the read-and-scan: if it grows one, every caller that was
// moved onto the batch path is paying for it again.
func TestAppendDrawerIsAThinWrapper(t *testing.T) {
	fn := findFuncDecl(t, "drawers.go", "AppendDrawer")

	if n := countCalls(fn, "os", "ReadFile"); n != 0 {
		t.Errorf("AppendDrawer makes %d os.ReadFile calls, want 0 — it must delegate to AppendDrawers", n)
	}
	if n := countCalls(fn, "v", "AppendDrawers"); n != 1 {
		t.Errorf("AppendDrawer makes %d AppendDrawers calls, want exactly 1", n)
	}
}

// --- tiny AST helpers -------------------------------------------------------

func findFuncDecl(t *testing.T, file, name string) *ast.FuncDecl {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if ok && fd.Name.Name == name {
			return fd
		}
	}
	t.Fatalf("%s: func %s not found", file, name)
	return nil
}

// isCall reports whether n is a call of the form <recv>.<sel>(...).
func isCall(n ast.Node, recv, sel string) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	se, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || se.Sel.Name != sel {
		return false
	}
	id, ok := se.X.(*ast.Ident)
	return ok && id.Name == recv
}

func countCalls(fn *ast.FuncDecl, recv, sel string) int {
	n := 0
	ast.Inspect(fn, func(node ast.Node) bool {
		if isCall(node, recv, sel) {
			n++
		}
		return true
	})
	return n
}

func callsInsideLoop(fn *ast.FuncDecl, recv, sel string) bool {
	found := false
	var walkLoop func(ast.Node)
	walkLoop = func(body ast.Node) {
		ast.Inspect(body, func(node ast.Node) bool {
			if isCall(node, recv, sel) {
				found = true
			}
			return true
		})
	}
	ast.Inspect(fn, func(node ast.Node) bool {
		switch loop := node.(type) {
		case *ast.ForStmt:
			walkLoop(loop.Body)
		case *ast.RangeStmt:
			walkLoop(loop.Body)
		}
		return true
	})
	return found
}
