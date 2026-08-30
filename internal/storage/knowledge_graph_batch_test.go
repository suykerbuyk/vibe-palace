// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// entityBatchOf builds n entities with distinct IDs.
func entityBatchOf(prefix string, n int) []Entity {
	es := make([]Entity, n)
	for i := range n {
		es[i] = Entity{
			ID:        fmt.Sprintf("%s-%d", prefix, i),
			Name:      fmt.Sprintf("%s %d", prefix, i),
			Type:      "concept",
			CreatedAt: "2026-08-25T00:00:00Z",
		}
	}
	return es
}

func entitiesPath(t *testing.T, v *Vault, project string) string {
	t.Helper()
	path, err := v.KGEntitiesFile(project)
	if err != nil {
		t.Fatalf("KGEntitiesFile: %v", err)
	}
	return path
}

// TestAddEntitiesAppendsWholeBatch is the count contract: a batch of N new
// entities appends N, and every one of them is readable afterwards.
func TestAddEntitiesAppendsWholeBatch(t *testing.T) {
	v := testVault(t)
	const project = "proj"

	n, err := v.AddEntities(project, entityBatchOf("ent", 25))
	if err != nil {
		t.Fatalf("AddEntities: %v", err)
	}
	if n != 25 {
		t.Fatalf("appended = %d, want 25", n)
	}

	got, err := v.ListEntities(project)
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(got) != 25 {
		t.Fatalf("ListEntities = %d entities, want 25", len(got))
	}
}

// TestAddEntitiesPartialOverlap: only the entities not already filed land, and
// the count reports exactly those.
func TestAddEntitiesPartialOverlap(t *testing.T) {
	v := testVault(t)
	const project = "proj"

	if _, err := v.AddEntities(project, entityBatchOf("ent", 10)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// First 10 are duplicates on disk, next 6 are new.
	n, err := v.AddEntities(project, entityBatchOf("ent", 16))
	if err != nil {
		t.Fatalf("AddEntities: %v", err)
	}
	if n != 6 {
		t.Fatalf("appended = %d, want 6", n)
	}
	got, err := v.ListEntities(project)
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(got) != 16 {
		t.Fatalf("ListEntities = %d, want 16", len(got))
	}
}

// TestAddEntitiesDedupsWithinBatch: two entries with the same ID inside ONE
// batch must collapse to one. The in-call `seen` set has to catch that, because
// there is no on-disk line to catch it for them.
func TestAddEntitiesDedupsWithinBatch(t *testing.T) {
	v := testVault(t)
	const project = "proj"

	same := Entity{ID: "dup", Name: "Kai", Type: "person", CreatedAt: "2026-08-25T00:00:00Z"}
	n, err := v.AddEntities(project, []Entity{same, same, same})
	if err != nil {
		t.Fatalf("AddEntities: %v", err)
	}
	if n != 1 {
		t.Fatalf("appended = %d, want 1", n)
	}
	got, err := v.ListEntities(project)
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListEntities = %d, want 1", len(got))
	}
}

// TestAddEntitiesReaddIsZeroAndWritesNothing is the property the batch exists
// to turn on: re-importing an export that is already filed must add nothing AND
// must not rewrite the file. A whole-file rewrite here is what made a re-run
// cost the same as the first run.
func TestAddEntitiesReaddIsZeroAndWritesNothing(t *testing.T) {
	v := testVault(t)
	const project = "proj"
	batch := entityBatchOf("ent", 12)

	if n, err := v.AddEntities(project, batch); err != nil || n != 12 {
		t.Fatalf("first AddEntities = (%d, %v), want (12, nil)", n, err)
	}

	path := entitiesPath(t, v, project)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	n, err := v.AddEntities(project, batch)
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if n != 0 {
		t.Fatalf("re-add appended = %d, want 0", n)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("all-duplicate batch rewrote the entities file (%d bytes -> %d bytes)",
			len(before), len(after))
	}
}

func TestAddEntitiesEmptyBatch(t *testing.T) {
	v := testVault(t)
	n, err := v.AddEntities("proj", nil)
	if err != nil {
		t.Fatalf("AddEntities(nil): %v", err)
	}
	if n != 0 {
		t.Fatalf("appended = %d, want 0", n)
	}
	path := entitiesPath(t, v, "proj")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty batch created %s (stat err = %v)", path, err)
	}
}

func TestAddEntitiesInvalidSlug(t *testing.T) {
	v := testVault(t)
	if _, err := v.AddEntities("BAD PROJECT", entityBatchOf("ent", 1)); err == nil {
		t.Error("AddEntities with invalid project slug should return error")
	}
}

// TestAddEntityWrapperKeepsDuplicateError pins the contract the MCP kg_add tool
// depends on: the n=1 entry point still ERRORS on a duplicate, with the exact
// "already exists" substring, even though the batch entry point reports a count
// instead. Three production call sites match on that substring.
func TestAddEntityWrapperKeepsDuplicateError(t *testing.T) {
	v := testVault(t)
	e := Entity{ID: "kai", Name: "Kai", Type: "person", CreatedAt: "2026-08-25T00:00:00Z"}

	if err := v.AddEntity("proj", e); err != nil {
		t.Fatalf("first AddEntity: %v", err)
	}
	err := v.AddEntity("proj", e)
	if err == nil {
		t.Fatal("duplicate AddEntity should return error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %q, want it to contain %q", err, "already exists")
	}
	if !strings.Contains(err.Error(), e.ID) {
		t.Fatalf("error = %q, want it to name the entity ID %q", err, e.ID)
	}
}

// TestAddEntitiesHealsTornFinalLine is the append primitive's characteristic
// failure mode. atomicfile.Write renamed a complete file into place and could
// never leave a partial line; an O_APPEND write can. Without the heal, the new
// batch's FIRST record is concatenated onto the torn bytes and both are lost.
//
// 🔴 The blast radius is bigger here than for drawers: readDrawerFile SKIPS a
// malformed line, but ListEntities returns an error on it — so the torn line
// alone makes the whole graph unreadable through the normal reader. The heal
// cannot fix that; what it does is confine the damage to the ONE record that
// was genuinely torn instead of taking a good new record down with it. This
// test therefore asserts the raw file, line by line, and pins the error that
// ListEntities actually returns.
func TestAddEntitiesHealsTornFinalLine(t *testing.T) {
	v := testVault(t)
	const project = "proj"

	if _, err := v.AddEntities(project, entityBatchOf("good", 3)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	path := entitiesPath(t, v, project)

	// Simulate a torn append: a truncated JSON object with no closing brace and
	// no trailing newline.
	const torn = `{"id":"deadbeef","name":"tor`
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(torn); err != nil {
		t.Fatal(err)
	}
	f.Close()

	n, err := v.AddEntities(project, entityBatchOf("after", 2))
	if err != nil {
		t.Fatalf("AddEntities over a torn line: %v", err)
	}
	if n != 2 {
		t.Fatalf("appended = %d, want 2", n)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("file has %d lines, want 6 (3 good + 1 torn + 2 new): %q", len(lines), lines)
	}
	// The torn record stays exactly as torn as it was. If the heal is removed,
	// this line becomes torn+`{"id":"after-0"...}` and BOTH records are lost.
	if lines[3] != torn {
		t.Fatalf("torn line = %q, want %q — the new record was swallowed by it", lines[3], torn)
	}
	// Every intact record still parses on its own, including both new ones.
	wantIDs := []string{"good-0", "good-1", "good-2", "", "after-0", "after-1"}
	for i, line := range lines {
		if wantIDs[i] == "" {
			continue // the torn record; it is the ONE that is lost
		}
		var e Entity
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %d does not parse: %v (%q)", i+1, err, line)
		}
		if e.ID != wantIDs[i] {
			t.Errorf("line %d id = %q, want %q", i+1, e.ID, wantIDs[i])
		}
	}

	// And the reader's actual behaviour, stated rather than wished for:
	// ListEntities hard-errors on the torn record, so the record that is lost
	// is "deadbeef" and it takes the whole listing with it. The dedup scan on
	// the WRITE path tolerates it (the append above succeeded), which is why a
	// torn line does not also wedge every future write.
	if _, err := v.ListEntities(project); err == nil {
		t.Error("ListEntities returned no error over a torn line — " +
			"if this reader was made tolerant, update this test and the AddEntities comment")
	} else if !strings.Contains(err.Error(), "parse entity line") {
		t.Errorf("ListEntities error = %q, want it to name the unparseable line", err)
	}
}

// TestAddEntitiesLongLineDoesNotFailTheScan proves scanner.Buffer is set on the
// dedup scan. bufio.Scanner's 64 KB default turns a longer existing line into
// bufio.ErrTooLong, and because the scan is on the WRITE path that error fails
// EVERY subsequent add for the project — not just the one oversized record.
// Delete the scanner.Buffer line in AddEntities and this goes red.
func TestAddEntitiesLongLineDoesNotFailTheScan(t *testing.T) {
	v := testVault(t)
	const project = "proj"

	// One valid entity whose marshalled line is well over 64 KB and well under
	// maxEntityLine (1 MiB).
	big := Entity{
		ID:         "big",
		Name:       "Big",
		Type:       "concept",
		Properties: map[string]string{"note": strings.Repeat("x", 200*1024)},
		CreatedAt:  "2026-08-25T00:00:00Z",
	}
	if _, err := v.AddEntities(project, []Entity{big}); err != nil {
		t.Fatalf("seed big entity: %v", err)
	}
	path := entitiesPath(t, v, project)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 64*1024 {
		t.Fatalf("seeded line is %d bytes, want > 64 KiB for this test to mean anything", len(raw))
	}
	if len(raw) >= maxEntityLine {
		t.Fatalf("seeded line is %d bytes, want < maxEntityLine (%d)", len(raw), maxEntityLine)
	}

	// The scan must get past that line rather than failing the whole call.
	n, err := v.AddEntities(project, entityBatchOf("after", 1))
	if err != nil {
		t.Fatalf("AddEntities over a >64 KiB existing line: %v", err)
	}
	if n != 1 {
		t.Fatalf("appended = %d, want 1", n)
	}
	// And the big line must still dedup: it was scanned, so re-adding it is 0.
	if n, err := v.AddEntities(project, []Entity{big}); err != nil || n != 0 {
		t.Fatalf("re-add of the big entity = (%d, %v), want (0, nil) — "+
			"a skipped scan would have re-appended it", n, err)
	}
	got, err := v.ListEntities(project)
	if err != nil {
		t.Fatalf("ListEntities over a >64 KiB line: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListEntities = %d, want 2", len(got))
	}
}

// TestAddEntitiesOneReadOneWrite is the structural half of the fix, and it is a
// source assertion on purpose: the defect this unit closes is not a wrong
// RESULT, it is a right result computed O(entities) times. A behavioural test
// cannot see the difference between one read and twenty-five, so the shape is
// pinned here — exactly one os.ReadFile and exactly one appendUnderLock in the
// body, neither inside a loop, and no whole-file replace at all.
func TestAddEntitiesOneReadOneWrite(t *testing.T) {
	fn := findFuncDecl(t, "knowledge_graph.go", "AddEntities")

	if reads := countCalls(fn, "os", "ReadFile"); reads != 1 {
		t.Errorf("AddEntities makes %d os.ReadFile calls, want exactly 1", reads)
	}
	if appends := countCalls(fn, "v", "appendUnderLock"); appends != 1 {
		t.Errorf("AddEntities makes %d appendUnderLock calls, want exactly 1", appends)
	}
	if callsInsideLoop(fn, "os", "ReadFile") {
		t.Error("AddEntities reads the entities file inside a loop — the quadratic term is back")
	}
	if callsInsideLoop(fn, "v", "appendUnderLock") {
		t.Error("AddEntities writes inside a loop — the quadratic term is back")
	}
	// The whole-file replace must be gone from this path: routing back to
	// atomicfile.Write reintroduces the copy-the-whole-buffer cost per add.
	if countCalls(fn, "atomicfile", "Write") != 0 {
		t.Error("AddEntities calls atomicfile.Write — it must append via F4, not replace the file")
	}
}

// TestAddEntityIsAThinWrapper pins that the n=1 entry point does not carry a
// second copy of the read-and-scan: if it grows one, every caller that was
// moved onto the batch path is paying for it again.
func TestAddEntityIsAThinWrapper(t *testing.T) {
	fn := findFuncDecl(t, "knowledge_graph.go", "AddEntity")

	if n := countCalls(fn, "os", "ReadFile"); n != 0 {
		t.Errorf("AddEntity makes %d os.ReadFile calls, want 0 — it must delegate to AddEntities", n)
	}
	if n := countCalls(fn, "v", "AddEntities"); n != 1 {
		t.Errorf("AddEntity makes %d AddEntities calls, want exactly 1", n)
	}
	if n := countCalls(fn, "atomicfile", "Write"); n != 0 {
		t.Errorf("AddEntity makes %d atomicfile.Write calls, want 0", n)
	}
}
