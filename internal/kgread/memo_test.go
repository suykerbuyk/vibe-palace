// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package kgread

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The per-load memo: the collapsed triples and the sorted entities are built
// once per load, rebuilt on every reload, never aliased by a result, and safe
// for concurrent callers.

func countCollapses(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	old := collapseFn
	collapseFn = func(recs []localTriple) []localTriple {
		n.Add(1)
		return old(recs)
	}
	t.Cleanup(func() { collapseFn = old })
	return &n
}

// readAll calls every reader that uses the collapsed triples.
func readAll(t *testing.T, v *storage.Vault) {
	t.Helper()
	if _, err := KGStats(v, project); err != nil {
		t.Fatal(err)
	}
	if _, err := ListTriples(v, project); err != nil {
		t.Fatal(err)
	}
	if _, err := QueryEntity(v, project, tripleT[0], "", "both"); err != nil {
		t.Fatal(err)
	}
	if _, err := Timeline(v, project, tripleT[0]); err != nil {
		t.Fatal(err)
	}
}

// A warm read at one counter collapses nothing; the load collapsed once.
func TestWarmKGReadDoesNotRecollapse(t *testing.T) {
	v := newVault(t)
	ingestAll(t, v, ingest{"S1", "aa11", "2026-05-10", s1()})
	n := countCollapses(t)
	readAll(t, v)
	readAll(t, v)
	if got := n.Load(); got != 1 {
		t.Fatalf("two rounds of KGStats, ListTriples, QueryEntity and Timeline at one counter collapsed %d times, want 1 (the load)", got)
	}
	ingestAll(t, v, ingest{"S2", "bb22", "2026-05-03", s2()})
	readAll(t, v)
	readAll(t, v)
	if got := n.Load(); got != 2 {
		t.Fatalf("after a counter bump: %d collapses, want 2", got)
	}
}

// coldView is the readers' answer from a fresh load, the reference a warm
// answer after a reload must equal.
func coldView(t *testing.T, v *storage.Vault) readerView {
	t.Helper()
	dropLocalCache(v, project)
	return view(t, v, project)
}

// A reload rebuilds the derived sets from the new records: a triple that
// collapses with a held one, a new triple, a new entity, then a supersede
// that removes them again. The vault has no tracked KG, so every answer is
// the local memo's.
func TestDerivedSetsAreRebuiltOnEveryReload(t *testing.T) {
	v := newVault(t)
	ingestAll(t, v, ingest{"S1", "aa11", "2026-05-10", s1()})
	before := view(t, v, project) // loads and caches
	ended := tripleT
	more := extraction{
		triples: []storage.Triple{
			{Subject: ended[0], Predicate: ended[1], Object: ended[2], ValidTo: "2026-06-01"},
			{Subject: tripleT[0], Predicate: "knows", Object: "bob"},
		},
		entities: []storage.Entity{{ID: "person-aaron", Name: "Aaron", Type: "person"}},
	}
	ingestAll(t, v, ingest{"S3", "cc33", "2026-05-12", more})
	warm := view(t, v, project)
	if cold := coldView(t, v); !reflect.DeepEqual(warm, cold) {
		t.Fatalf("after a reload the warm answer is not a fresh load's:\n warm %+v\n cold %+v", warm, cold)
	}
	if reflect.DeepEqual(warm, before) {
		t.Fatal("the reload changed nothing: the memo is the previous load's")
	}
	if warm.Stats.TripleCount != 2 || warm.Stats.EntityCount != 2 {
		t.Errorf("stats after S3 = %+v, want 2 triples (T collapsed to one) and 2 entities", warm.Stats)
	}
	ents, _, err := ListEntities(v, project)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.IsSortedFunc(ents, func(a, b storage.Entity) int { return strings.Compare(a.ID, b.ID) }) || ents[0].ID != "person-aaron" {
		t.Errorf("entities not in sorted order: %+v", ents)
	}

	if err := supersede(t, v, project, archiveCommit("S3", "cc34", "2026-05-13", nil), nil); err != nil {
		t.Fatal(err)
	}
	after := view(t, v, project)
	if cold := coldView(t, v); !reflect.DeepEqual(after, cold) {
		t.Fatalf("after the supersede the warm answer is not a fresh load's:\n warm %+v\n cold %+v", after, cold)
	}
	if after.Stats.TripleCount != 1 || after.Stats.EntityCount != 1 {
		t.Errorf("stats after the supersede = %+v, want S3's triple and entity gone", after.Stats)
	}
}

// Nothing a reader returns aliases the memo: mutating and appending to a
// result leaves the next answer unchanged.
func TestKGReadResultsDoNotAliasTheCache(t *testing.T) {
	v := newVault(t)
	ingestAll(t, v, ingest{"S1", "aa11", "2026-05-10", s1()}, ingest{"S2", "bb22", "2026-05-03", s2()})
	// The expected answers are frozen as JSON first: a result that aliased
	// the cache would otherwise change the expectation along with the cache.
	frozen := func(x any) string {
		t.Helper()
		b, err := json.Marshal(x)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	want := frozen(view(t, v, project))
	firstTriples, err := ListTriples(v, project)
	if err != nil {
		t.Fatal(err)
	}
	wantTriples := frozen(firstTriples)
	triples, err := ListTriples(v, project)
	if err != nil {
		t.Fatal(err)
	}
	ents, _, err := ListEntities(v, project)
	if err != nil {
		t.Fatal(err)
	}
	q, err := QueryEntity(v, project, tripleT[0], "", "both")
	if err != nil {
		t.Fatal(err)
	}
	tl, err := Timeline(v, project, tripleT[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, ts := range [][]storage.Triple{triples, q, tl} {
		for i := range ts {
			ts[i].Subject, ts[i].Object = "MUTATED", "MUTATED"
		}
		_ = append(ts[:0:0], storage.Triple{Subject: "APPENDED"})
		_ = append(ts, storage.Triple{Subject: "APPENDED"})
	}
	for i := range ents {
		ents[i].ID, ents[i].Name = "MUTATED", "MUTATED"
	}
	_ = append(ents, storage.Entity{ID: "APPENDED"})
	if got := frozen(view(t, v, project)); got != want {
		t.Fatalf("a mutated result reached the cache:\n got  %s\n want %s", got, want)
	}
	got, err := ListTriples(v, project)
	if err != nil {
		t.Fatal(err)
	}
	if g := frozen(got); g != wantTriples {
		t.Fatalf("a mutated ListTriples result reached the cache:\n got  %s\n want %s", g, wantTriples)
	}
}

// Concurrent readers across reloads: no race (run under -race), and every
// answer is one a sequential reader sees at some counter.
func TestConcurrentKGReadsAcrossAReload(t *testing.T) {
	v := newVault(t)
	ingestAll(t, v, ingest{"S1", "aa11", "2026-05-10", s1()})
	const commits = 20
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			lastT, lastE := 0, 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				stats, err := KGStats(v, project)
				if err != nil {
					t.Errorf("reader %d: KGStats: %v", g, err)
					return
				}
				triples, err := ListTriples(v, project)
				if err != nil {
					t.Errorf("reader %d: ListTriples: %v", g, err)
					return
				}
				ents, _, err := ListEntities(v, project)
				if err != nil {
					t.Errorf("reader %d: ListEntities: %v", g, err)
					return
				}
				for _, c := range []int{stats.TripleCount, stats.EntityCount, len(triples), len(ents)} {
					if c < 1 || c > 1+commits {
						t.Errorf("reader %d: count %d is no state the store passed through (1..%d)", g, c, 1+commits)
						return
					}
				}
				if len(triples) < lastT || len(ents) < lastE {
					t.Errorf("reader %d: counts went back: triples %d -> %d, entities %d -> %d", g, lastT, len(triples), lastE, len(ents))
					return
				}
				lastT, lastE = len(triples), len(ents)
			}
		}(g)
	}
	for i := 0; i < commits; i++ {
		x := extraction{
			triples:  []storage.Triple{{Subject: fmt.Sprintf("s%02d", i), Predicate: "p", Object: "o"}},
			entities: []storage.Entity{{ID: fmt.Sprintf("e%02d", i), Name: "n", Type: "t"}},
		}
		ingestAll(t, v, ingest{fmt.Sprintf("C%02d", i), fmt.Sprintf("%064x", i+1), "2026-06-01", x})
	}
	close(stop)
	wg.Wait()
	if stats, err := KGStats(v, project); err != nil || stats.TripleCount != 1+commits || stats.EntityCount != 1+commits {
		t.Fatalf("final stats = %+v, %v; want %d triples and entities", stats, err, 1+commits)
	}
}
