// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// bigTranscript produces enough text to chunk into many drawers, so a per-chunk
// append loop and a per-room batch are actually distinguishable.
func bigTranscript(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString("We decided to route the drawer store through the append primitive. ")
		b.WriteString("The test coverage for chunk ")
		b.WriteString(strings.Repeat("x", 1+i%17))
		b.WriteString(" is measured, and the discovery is that the dedup scan dominates. ")
	}
	return b.String()
}

func countDrawers(t *testing.T, v *storage.Vault, project string) int {
	t.Helper()
	wing := palace.DetectWing(project, "")
	rooms, err := v.ListRooms(project, wing)
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	var n int
	for _, room := range rooms {
		ds, err := v.ListDrawers(project, wing, room)
		if err != nil {
			t.Fatalf("ListDrawers(%s): %v", room, err)
		}
		n += len(ds)
	}
	return n
}

// TestIndexTranscriptReindexAddsNothing is the backfill's load-bearing
// property: feeding the same archive twice must append nothing the second time
// and must leave the corpus byte-identical in count.
func TestIndexTranscriptReindexAddsNothing(t *testing.T) {
	v := testVault(t)
	eng, emb := testEngine(t, v)
	idx := NewIndexer(v, eng, emb, storage.Config{})

	transcript := bigTranscript(40)

	first, err := idx.IndexTranscript(context.Background(), "session-01", "test-proj", transcript)
	if err != nil {
		t.Fatalf("first IndexTranscript: %v", err)
	}
	if first.Drawers == 0 {
		t.Fatal("first pass wrote no drawers")
	}
	afterFirst := countDrawers(t, v, "test-proj")
	if afterFirst != first.Drawers {
		t.Fatalf("stats.Drawers = %d but corpus holds %d", first.Drawers, afterFirst)
	}

	second, err := idx.IndexTranscript(context.Background(), "session-01", "test-proj", transcript)
	if err != nil {
		t.Fatalf("second IndexTranscript: %v", err)
	}
	if second.Drawers != 0 {
		t.Fatalf("re-index reported %d new drawers, want 0", second.Drawers)
	}
	if got := countDrawers(t, v, "test-proj"); got != afterFirst {
		t.Fatalf("re-index changed the corpus: %d -> %d drawers", afterFirst, got)
	}
}

// TestIndexTranscriptPartialReindex: a transcript that shares a prefix with an
// already-indexed one must add only the chunks that are genuinely new, and the
// count must report exactly those.
func TestIndexTranscriptPartialReindex(t *testing.T) {
	v := testVault(t)
	eng, emb := testEngine(t, v)
	idx := NewIndexer(v, eng, emb, storage.Config{})

	base := bigTranscript(20)
	if _, err := idx.IndexTranscript(context.Background(), "session-01", "test-proj", base); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before := countDrawers(t, v, "test-proj")

	st, err := idx.IndexTranscript(context.Background(), "session-02", "test-proj", base+bigTranscript(10))
	if err != nil {
		t.Fatalf("IndexTranscript: %v", err)
	}
	after := countDrawers(t, v, "test-proj")
	if after-before != st.Drawers {
		t.Fatalf("stats.Drawers = %d but corpus grew by %d", st.Drawers, after-before)
	}
	if st.Drawers == 0 {
		t.Fatal("extended transcript added no drawers at all")
	}
}

// TestIndexTranscriptDoesNotAppendPerChunk is the structural pin for this
// unit's whole point. IndexTranscript used to call AppendDrawer once per chunk,
// and AppendDrawer reads and scans the entire room file — so the cost was
// O(chunks × drawers in room) and a backfill could not finish. A behavioural
// test cannot tell one read from six hundred; the shape is pinned here.
func TestIndexTranscriptDoesNotAppendPerChunk(t *testing.T) {
	fn := findFuncDecl(t, "indexer.go", "IndexTranscript")

	if n := countSelectorCalls(fn, "AppendDrawer"); n != 0 {
		t.Errorf("IndexTranscript makes %d AppendDrawer (singular) calls, want 0 — "+
			"the per-chunk entry point rescans the whole room every time", n)
	}
	if n := countSelectorCalls(fn, "AppendDrawers"); n != 1 {
		t.Errorf("IndexTranscript makes %d AppendDrawers calls, want exactly 1", n)
	}

	// The one batch call is allowed to be in a loop — over ROOMS, which is
	// bounded by the room list. What it must not be in is a loop over chunks.
	// `locs` is the per-chunk slice, so a range over it containing the append
	// is the regression.
	if rangesOverLocsAndAppends(fn) {
		t.Error("IndexTranscript appends inside a range over the per-chunk slice — " +
			"the batch must be grouped by room first")
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

// countSelectorCalls counts calls of the form <anything>.<sel>(...). The
// receiver is deliberately not pinned: the point is the method NAME, whatever
// it is called on.
func countSelectorCalls(fn *ast.FuncDecl, sel string) int {
	n := 0
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if se, ok := call.Fun.(*ast.SelectorExpr); ok && se.Sel.Name == sel {
			n++
		}
		return true
	})
	return n
}

// rangesOverLocsAndAppends reports a `for ... := range locs` whose body appends
// drawers — the exact per-chunk shape this unit removed.
func rangesOverLocsAndAppends(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn, func(node ast.Node) bool {
		rs, ok := node.(*ast.RangeStmt)
		if !ok {
			return true
		}
		id, ok := rs.X.(*ast.Ident)
		if !ok || id.Name != "locs" {
			return true
		}
		ast.Inspect(rs.Body, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			if se, ok := call.Fun.(*ast.SelectorExpr); ok &&
				strings.HasPrefix(se.Sel.Name, "AppendDrawer") {
				found = true
			}
			return true
		})
		return true
	})
	return found
}
