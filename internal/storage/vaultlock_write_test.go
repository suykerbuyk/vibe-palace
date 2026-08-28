// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

// TestDeleteDrawerAppendDrawerInterlock drives concurrent AppendDrawer and
// DeleteDrawer against the SAME drawers file. The per-path vaultlock must make
// every read→rewrite (DeleteDrawer) interlock with every append (AppendDrawer)
// so the file stays valid JSONL with no lost updates: appended-and-kept IDs all
// survive and every deleted ID is gone. Without the lock this races and either
// corrupts a line or drops an append. Run under -race.
func TestDeleteDrawerAppendDrawerInterlock(t *testing.T) {
	v := testVault(t)
	const project, wing, room = "proj", "wing-a", "room-1"

	// Seed pre-existing drawers; remember their deterministic IDs.
	const seedN = 12
	seedIDs := make([]string, seedN)
	for i := range seedN {
		content := fmt.Sprintf("seed-%d", i)
		if err := v.AppendDrawer(project, wing, room, Drawer{
			Hall: "facts", Content: content, SourceType: "session", FiledAt: "2026-06-06T00:00:00Z",
		}); err != nil {
			t.Fatalf("seed AppendDrawer %d: %v", i, err)
		}
		seedIDs[i] = DrawerID(wing, content)
	}

	// Half the seeds get concurrently deleted.
	deleted := seedIDs[:seedN/2]
	kept := seedIDs[seedN/2:]

	// Concurrent fresh appends (distinct content => distinct IDs).
	const appendN = 40
	appendIDs := make([]string, appendN)
	for i := range appendN {
		appendIDs[i] = DrawerID(wing, fmt.Sprintf("append-%d", i))
	}

	var wg sync.WaitGroup
	for i := range appendN {
		wg.Go(func() {
			content := fmt.Sprintf("append-%d", i)
			if err := v.AppendDrawer(project, wing, room, Drawer{
				Hall: "facts", Content: content, SourceType: "session", FiledAt: "2026-06-06T00:00:00Z",
			}); err != nil {
				t.Errorf("AppendDrawer %d: %v", i, err)
			}
		})
	}
	for _, id := range deleted {
		wg.Go(func() {
			if err := v.DeleteDrawer(project, wing, room, id); err != nil {
				t.Errorf("DeleteDrawer %s: %v", id, err)
			}
		})
	}
	wg.Wait()

	// Read raw and assert every line is valid JSON; collect surviving IDs.
	path, err := v.DrawerFile(project, wing, room)
	if err != nil {
		t.Fatalf("DrawerFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read drawer file: %v", err)
	}
	present := make(map[string]bool)
	for line := range strings.SplitSeq(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var d Drawer
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			t.Fatalf("corrupt JSONL line %q: %v", line, err)
		}
		if present[d.ID] {
			t.Errorf("duplicate drawer ID in file: %s", d.ID)
		}
		present[d.ID] = true
	}

	for _, id := range appendIDs {
		if !present[id] {
			t.Errorf("appended drawer %s missing (lost update)", id)
		}
	}
	for _, id := range kept {
		if !present[id] {
			t.Errorf("kept seed drawer %s missing", id)
		}
	}
	for _, id := range deleted {
		if present[id] {
			t.Errorf("deleted drawer %s still present", id)
		}
	}
}

// TestConcurrentInvalidateTriple drives concurrent InvalidateTriple RMWs. The
// same-triple case asserts the file is never corrupted and the ValidTo set wins
// deterministically; the distinct-triple case asserts each independent RMW lands
// correctly. Run under -race.
func TestConcurrentInvalidateTriple(t *testing.T) {
	v := testVault(t)
	const project = "proj"

	// Same triple, N concurrent invalidations with the SAME ended value:
	// regardless of interleaving the final ValidTo must equal that value and
	// the file must remain parseable JSON.
	const ended = "2026-06-06"
	if err := v.AddTriple(project, Triple{
		Subject: "alice", Predicate: "uses", Object: "go", ValidFrom: "2026-01-01",
	}); err != nil {
		t.Fatalf("AddTriple: %v", err)
	}

	const sameN = 32
	var wg sync.WaitGroup
	for range sameN {
		wg.Go(func() {
			if err := v.InvalidateTriple(project, "alice", "uses", "go", ended); err != nil {
				t.Errorf("InvalidateTriple (same): %v", err)
			}
		})
	}
	wg.Wait()

	got := tripleAt(t, v, project, "alice", "uses", "go")
	if got.ValidTo != ended {
		t.Errorf("ValidTo = %q, want %q", got.ValidTo, ended)
	}

	// Distinct triples invalidated concurrently, each with its own ended date.
	const distinctN = 32
	for i := range distinctN {
		if err := v.AddTriple(project, Triple{
			Subject: fmt.Sprintf("s%d", i), Predicate: "p", Object: "o", ValidFrom: "2026-01-01",
		}); err != nil {
			t.Fatalf("AddTriple %d: %v", i, err)
		}
	}
	for i := range distinctN {
		wg.Go(func() {
			end := fmt.Sprintf("2026-06-%02d", (i%28)+1)
			if err := v.InvalidateTriple(project, fmt.Sprintf("s%d", i), "p", "o", end); err != nil {
				t.Errorf("InvalidateTriple %d: %v", i, err)
			}
		})
	}
	wg.Wait()

	for i := range distinctN {
		tr := tripleAt(t, v, project, fmt.Sprintf("s%d", i), "p", "o")
		want := fmt.Sprintf("2026-06-%02d", (i%28)+1)
		if tr.ValidTo != want {
			t.Errorf("triple %d ValidTo = %q, want %q", i, tr.ValidTo, want)
		}
	}
}

// TestConcurrentAppendDrawerDedup proves the duplicate-ID guard still fires
// after the atomic-write conversion: many goroutines race to append the same
// deterministic drawer ID and exactly one wins, leaving a single well-formed
// JSONL line. Run under -race.
func TestConcurrentAppendDrawerDedup(t *testing.T) {
	v := testVault(t)
	const project, wing, room = "proj", "wing-a", "room-1"
	const n = 32

	var wg sync.WaitGroup
	var wins int
	var mu sync.Mutex
	for range n {
		wg.Go(func() {
			d := Drawer{Hall: "facts", Content: "same content", SourceType: "session", FiledAt: "2026-06-06T00:00:00Z"}
			if err := v.AppendDrawer(project, wing, room, d); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			} else if !strings.Contains(err.Error(), "already exists") {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	wg.Wait()

	if wins != 1 {
		t.Errorf("got %d successful appends, want 1 (dedup must hold)", wins)
	}
	drawers, err := v.ListDrawers(project, wing, room)
	if err != nil {
		t.Fatalf("ListDrawers (file not well-formed?): %v", err)
	}
	if len(drawers) != 1 {
		t.Fatalf("drawer count = %d, want 1", len(drawers))
	}
}

// TestConcurrentAppendIterationOwned mints N iteration numbers concurrently on
// one iterations file. Because AppendIterationOwned derives max+1 UNDER the same
// lock it appends within, the N results must be exactly {1..N} — distinct and
// gapless. The superseded flow (derive from an unlocked read via a separate
// tool, then append) could hand two concurrent callers the same number — the
// counter corruption this task closes. Run under -race.
func TestConcurrentAppendIterationOwned(t *testing.T) {
	v := testVault(t)
	const project = "proj"
	const n = 50

	got := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			num, _, err := v.AppendIterationOwned(project, fmt.Sprintf("title-%d", i), "body", nil)
			if err != nil {
				t.Errorf("AppendIterationOwned %d: %v", i, err)
				return
			}
			got[i] = num
		})
	}
	wg.Wait()

	// Every number in 1..n must appear exactly once — no duplicate, no gap.
	seen := make(map[int]int)
	for _, num := range got {
		seen[num]++
	}
	for want := 1; want <= n; want++ {
		if seen[want] != 1 {
			t.Errorf("iteration number %d minted %d times, want exactly 1 (duplicate or gap)", want, seen[want])
		}
	}
	if len(seen) != n {
		t.Errorf("distinct numbers = %d, want %d", len(seen), n)
	}

	// The file must physically carry N canonical headers, one per number.
	path, err := v.IterationsFile(project)
	if err != nil {
		t.Fatalf("IterationsFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read iterations: %v", err)
	}
	content := string(data)
	for want := 1; want <= n; want++ {
		header := fmt.Sprintf("## Iteration %d — ", want)
		if c := strings.Count(content, header); c != 1 {
			t.Errorf("header %q appears %d times, want 1", header, c)
		}
	}
}

// TestConcurrentAddEntity adds N distinct entities concurrently to the same
// entities JSONL file; all must survive and the file must parse as well-formed
// JSONL (ListEntities errors on any malformed line). Run under -race.
func TestConcurrentAddEntity(t *testing.T) {
	v := testVault(t)
	const project = "proj"
	const n = 50

	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			e := Entity{ID: fmt.Sprintf("e%d", i), Name: fmt.Sprintf("name-%d", i), Type: "concept", CreatedAt: "2026-06-06T00:00:00Z"}
			if err := v.AddEntity(project, e); err != nil {
				t.Errorf("AddEntity %d: %v", i, err)
			}
		})
	}
	wg.Wait()

	entities, err := v.ListEntities(project)
	if err != nil {
		t.Fatalf("ListEntities (file not well-formed?): %v", err)
	}
	if len(entities) != n {
		t.Fatalf("entity count = %d, want %d (lost update)", len(entities), n)
	}
	seen := make(map[string]bool, n)
	for _, e := range entities {
		seen[e.ID] = true
	}
	for i := range n {
		if !seen[fmt.Sprintf("e%d", i)] {
			t.Errorf("entity e%d missing", i)
		}
	}
}
