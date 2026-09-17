// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests for the knowledge-graph readers: one torn entity line must not
// delete a project's graph, and — deliberately — a malformed TRIPLE still must.
//
// These two rules look inconsistent and are not. The discriminator is the write
// path, not taste, and TestListTriplesStaysFailClosed exists to keep the
// difference from being tidied away.

// tornEntityLine is the shape an interrupted append leaves behind: a truncated
// JSON object with no closing brace. It is the exact specimen
// TestAddEntitiesHealsTornFinalLine plants.
const tornEntityLine = `{"id":"deadbeef","name":"tor`

// seedEntities writes n parseable entities and returns the vault plus the
// entities file path.
func seedEntities(t *testing.T, project string, n int) (*Vault, string) {
	t.Helper()
	v := testVault(t)
	var es []Entity
	for i := range n {
		es = append(es, Entity{
			ID: fmt.Sprintf("e%d", i), Name: fmt.Sprintf("name-%d", i),
			Type: "concept", CreatedAt: "2026-06-06T00:00:00Z",
		})
	}
	if _, err := v.AddEntities(project, es); err != nil {
		t.Fatalf("seed: %v", err)
	}
	path, err := v.KGEntitiesFile(project)
	if err != nil {
		t.Fatal(err)
	}
	return v, path
}

// appendRaw appends bytes with no trailing newline, the way a crashed append
// leaves them.
func appendRaw(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// TestListEntitiesSkipsATornLine is the test that reproduces the defect.
//
// Before this change, one torn line made the ENTIRE knowledge graph unreadable
// through the only reader anyone calls — and a torn line is a normal physical
// outcome of an interrupted append, not corruption.
//
// Break: restore `return nil, fmt.Errorf("parse entity line: %w", err)`.
func TestListEntitiesSkipsATornLine(t *testing.T) {
	v, path := seedEntities(t, "proj", 3)
	appendRaw(t, path, tornEntityLine)

	entities, skipped, err := v.ListEntities("proj")
	if err != nil {
		t.Fatalf("one torn line failed the WHOLE listing: %v\n"+
			"A torn append must not delete a project's graph", err)
	}
	if len(entities) != 3 {
		t.Errorf("wanted the 3 intact entities back, got %d — a torn line is taking good records "+
			"down with it", len(entities))
	}
	if len(skipped) != 1 {
		t.Fatalf("wanted exactly the 1 torn line reported, got %d: %v", len(skipped), skipped)
	}
}

// TestListEntitiesNamesWhatItSkipped pins the visibility half.
//
// A silently skipped record is the same disease as a fail-closed read, moved one
// layer down: the outage is replaced by a quietly short answer, which is harder
// to notice. The skip must name the record — the FILE AND THE LINE, because a
// path alone sends the reader hunting through a JSONL file by hand.
//
// Break: replace the RecordSkip append with a bare `continue`.
func TestListEntitiesNamesWhatItSkipped(t *testing.T) {
	v, path := seedEntities(t, "proj", 2)
	appendRaw(t, path, tornEntityLine)

	_, skipped, err := v.ListEntities("proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) == 0 {
		t.Fatal("a record was skipped and NOTHING said so")
	}
	s := skipped[0]
	if !strings.HasSuffix(s.Path, ":3") {
		t.Errorf("the skip must name the LINE, not just the file — a JSONL path alone leaves the "+
			"reader counting lines by hand. got %q, want a \":3\" suffix", s.Path)
	}
	if !strings.Contains(s.Path, "entities") {
		t.Errorf("the skip must name the file: %q", s.Path)
	}
	if filepath.IsAbs(s.Path) {
		t.Errorf("the skip path must be vault-relative, got an absolute host path: %q", s.Path)
	}
	if s.Reason == "" {
		t.Error("the skip must say WHY; a path with no reason sends the reader back to reproduce it")
	}
}

// TestListEntitiesStillFailsOnAnUnreadableFile pins the read-fatal/parse-skip
// asymmetry, which is easy to erase by accident while "making things
// consistent".
//
// A PARSE failure is scoped to one record and every other record is still true.
// A READ failure is a HOST problem — a bad mount, a permission change, a disk
// fault — and says nothing about content, so continuing would report a partial
// graph as though the vault were intact.
//
// Break: move the os.Open error into the skip list.
func TestListEntitiesStillFailsOnAnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unreadable file is still readable")
	}
	v, path := seedEntities(t, "proj", 1)
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}

	if _, _, err := v.ListEntities("proj"); err == nil {
		t.Fatal("an UNREADABLE entities file was tolerated like a torn line. A host-level read " +
			"failure says nothing about content, so continuing reports a partial graph as though " +
			"the vault were intact")
	}
}

// TestListEntitiesStillFailsOnAnOverLongLine pins the other thing that stays
// fatal, and it is a limit rather than a choice.
//
// bufio.Scanner CANNOT RESUME after ErrTooLong — there is no way to skip past
// the offending line and keep reading — so a line over maxEntityLine truncates
// the listing with no report. Returning the error is the only honest outcome;
// per-record tolerance is available exactly where the scanner can continue.
//
// Break: swallow scanner.Err() and return the partial slice. The listing then
// comes back SHORT with a nil error, which is the silent-truncation failure this
// whole unit exists to remove.
func TestListEntitiesStillFailsOnAnOverLongLine(t *testing.T) {
	v, path := seedEntities(t, "proj", 2)
	appendRaw(t, path, "\n"+strings.Repeat("x", maxEntityLine+1)+"\n")

	entities, _, err := v.ListEntities("proj")
	if err == nil {
		t.Fatalf("an over-long line returned NO error and %d entities. bufio.Scanner stops at "+
			"ErrTooLong and cannot resume, so a nil error here means the listing was silently "+
			"truncated", len(entities))
	}
	if !strings.Contains(err.Error(), "scan entities file") {
		t.Errorf("the error must name the scan that failed, got: %v", err)
	}
}

// 🔴 TestListTriplesStaysFailClosed PINS A DELIBERATE NON-CHANGE, and it is the
// most important test in this file.
//
// ListEntities (same package, and for entities the same FILE as a writer that
// already skips) is tolerant; ListTriples is not. A reader who notices that will
// reasonably want to unify them. Do not. The discriminator is the WRITE PATH and
// it is checkable rather than a matter of taste:
//
//   - Entities are written by appendUnderLock (family F4, append). An
//     interrupted append leaves a TORN FINAL LINE. That is a normal physical
//     outcome, so tolerating it is reading the file the way it is actually
//     written.
//   - Triples are written by atomicfile.Write — temp file plus rename
//     (knowledge_graph.go, AddTriple). A partially-written triple file CANNOT
//     EXIST. So a malformed triple means corruption, hand-editing or a bug, and
//     skipping it would turn "something is wrong with your graph" into "that
//     edge does not exist".
//
// # The consumer that makes this decisive
//
// kg_migrate.go's spotCheckBothSides iterates exactly the triples ListTriples
// returned and verifies each is queryable from both directions. A tolerant
// ListTriples would hand it only the survivors — so a migration that CORRUPTED
// triple files would sample the intact ones and VERIFY ITSELF GREEN. Tolerance
// in a verifier defeats the verifier. That is not a symmetry worth having.
//
// Break: make ListTriples skip malformed files the way ListEntities skips torn
// lines. This test goes red, and it is the only thing that would notice.
func TestListTriplesStaysFailClosed(t *testing.T) {
	v := testVault(t)
	const project = "proj"

	tr := Triple{Subject: "a", Predicate: "uses", Object: "b", ValidFrom: "2026-06-06"}
	if err := v.AddTriple(project, tr); err != nil {
		t.Fatalf("seed triple: %v", err)
	}
	dir, err := v.KGTriplesDir(project)
	if err != nil {
		t.Fatal(err)
	}
	// A triple file atomicfile.Write could never have produced.
	if err := os.WriteFile(filepath.Join(dir, "x--y--z.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	triples, err := v.ListTriples(project)
	if err == nil {
		t.Fatalf("ListTriples TOLERATED a malformed triple file and returned %d triple(s).\n"+
			"Triples are written with atomicfile.Write, so a partial file cannot occur — a malformed "+
			"one is corruption, and skipping it reports a corrupted graph as a smaller one.\n"+
			"Worse, kg_migrate.spotCheckBothSides verifies exactly the triples this returns, so a "+
			"tolerant reader lets a migration that corrupted files pass its own spot check.",
			len(triples))
	}
	if !strings.Contains(err.Error(), "parse triple") {
		t.Errorf("the error must name the unparseable triple, got: %v", err)
	}
}

// TestKGStatsCarriesEntitySkips pins that the count stops lying quietly.
//
// EntityCount is a PARSE count while TripleCount is a GLOB count, so once
// ListEntities skips, a lower EntityCount is indistinguishable from a smaller
// graph. SkippedRecords is what keeps the difference visible.
//
// Break: stop copying skips into stats.SkippedRecords.
func TestKGStatsCarriesEntitySkips(t *testing.T) {
	v, path := seedEntities(t, "proj", 3)
	appendRaw(t, path, tornEntityLine)

	stats, err := v.KGStats("proj")
	if err != nil {
		t.Fatalf("KGStats failed over a torn line: %v", err)
	}
	if stats.EntityCount != 3 {
		t.Errorf("entity_count = %d, want 3", stats.EntityCount)
	}
	if len(stats.SkippedRecords) == 0 {
		t.Fatal("entity_count dropped to the legible records and the stats block said nothing. " +
			"A count that silently means \"how many were readable today\" is worse than an error")
	}
	if !strings.Contains(stats.SkippedRecords[0], ":4") {
		t.Errorf("the stats block must name the skipped record: %v", stats.SkippedRecords)
	}
}

// TestLiveVaultKnowledgeGraphIsReadable is the live-corpus half.
//
// A bug that passes every fixture and dies on the real corpus is this project's
// signature failure. The fixtures above prove the mechanism; this proves the
// mechanism is pointed at the real graph — 18k entity lines and 60k triple files
// across every project.
//
// 🔴 IT ASSERTS NO COUNT. "Exactly zero bad lines" is true today and would rot
// the moment a torn append happens; worse, it would go red on a real event
// rather than on a regression. What is pinned is the property: a project with an
// entities file yields a non-empty listing, and whatever is skipped is NAMED.
//
// It reads the vault and writes nothing.
//
// Break: restore fail-closed in ListEntities — this stays green today because
// the live corpus is clean, and that is stated rather than hidden. Its job is to
// catch the day it is not.
func TestLiveVaultKnowledgeGraphIsReadable(t *testing.T) {
	root := liveVaultRoot(t)
	v := NewVault(root)

	projects, err := os.ReadDir(filepath.Join(root, "Projects"))
	if err != nil {
		t.Skipf("no Projects dir: %v", err)
	}

	checkedEntities, checkedTriples, totalSkips := 0, 0, 0
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		proj := p.Name()

		if ep, perr := v.KGEntitiesFile(proj); perr == nil {
			if _, serr := os.Stat(ep); serr == nil {
				checkedEntities++
				entities, skipped, lerr := v.ListEntities(proj)
				if lerr != nil {
					t.Errorf("%s: the entity listing failed outright: %v", proj, lerr)
				} else if len(entities) == 0 && len(skipped) > 0 {
					t.Errorf("%s: an entities file exists and the listing came back EMPTY with %d "+
						"skip(s) — the graph is unreadable, not small", proj, len(skipped))
				}
				for _, s := range skipped {
					totalSkips++
					if s.Path == "" || s.Reason == "" {
						t.Errorf("%s: a skip was reported without a path or a reason: %+v", proj, s)
					}
					t.Logf("%s: skipped %s (%s)", proj, s.Path, s.Reason)
				}
			}
		}

		if td, derr := v.KGTriplesDir(proj); derr == nil {
			if ms, _ := filepath.Glob(filepath.Join(td, "*.json")); len(ms) > 0 {
				checkedTriples++
				if _, lerr := v.ListTriples(proj); lerr != nil {
					// Fail-closed is correct here, but it is still worth knowing.
					t.Errorf("%s: ListTriples failed — a triple file is corrupt on disk: %v", proj, lerr)
				}
			}
		}
	}

	if checkedEntities == 0 && checkedTriples == 0 {
		t.Fatal("no project with a knowledge graph was checked at all — the walk is not seeing the " +
			"vault, and a passing verdict here would be vacuous")
	}
	t.Logf("live corpus: %d project(s) with entities, %d with triples, %d entity record(s) skipped",
		checkedEntities, checkedTriples, totalSkips)
}

// entityJSONLine is a helper for tests that need to plant a specific record.
func entityJSONLine(t *testing.T, e Entity) string {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
