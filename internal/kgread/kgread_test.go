// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package kgread

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// TestMain doubles as the helper process for the multi-process row: with
// KGREAD_HELPER=commit it commits one large archive and exits.
func TestMain(m *testing.M) {
	if os.Getenv("KGREAD_HELPER") == "commit" {
		os.Exit(helperCommit())
	}
	os.Exit(m.Run())
}

const project = "alpha"

// newVault returns a vault at the binary's data format with projects alpha
// and beta.
func newVault(t *testing.T) *storage.Vault {
	t.Helper()
	root := t.TempDir()
	for _, p := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(root, "Projects", p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := surface.WriteFormat(root, surface.RequiredDataFormat); err != nil {
		t.Fatal(err)
	}
	return storage.NewVault(root)
}

// extraction is one session's extractor output, before ING builds records.
type extraction struct {
	triples  []storage.Triple
	entities []storage.Entity
}

// records builds the store records ING would commit for an extraction:
// through the builders, which keep identity fields only.
func records(x extraction) []indexstore.KGRecord {
	var out []indexstore.KGRecord
	for _, t := range x.triples {
		id, p := index.TripleRecord(t.Subject, t.Predicate, t.Object, t.ValidTo)
		out = append(out, indexstore.KGRecord{ID: id, Payload: p})
	}
	for _, e := range x.entities {
		id, p := index.EntityRecord(e.ID, e.Name, e.Type)
		out = append(out, indexstore.KGRecord{ID: id, Payload: p})
	}
	return out
}

// testRecipe is the chunk recipe every test Tx records, so a test that writes
// chunks meets chunks.fingerprint's precondition (Tx.UseRecipe).
var testRecipe = index.ChunkRecipe{IndexerVersion: index.IndexerVersion}

func withTx(t *testing.T, v *storage.Vault, p string, f func(tx *indexstore.Tx) error) error {
	t.Helper()
	tx, err := indexstore.Lock(context.Background(), v, p, indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	tx.UseRecipe(testRecipe)
	ferr := f(tx)
	if rerr := tx.Release(); ferr == nil {
		ferr = rerr
	}
	return ferr
}

func mustTx(t *testing.T, v *storage.Vault, p string, f func(tx *indexstore.Tx) error) {
	t.Helper()
	if err := withTx(t, v, p, f); err != nil {
		t.Fatal(err)
	}
}

func archiveCommit(session, sha, day string, kg []indexstore.KGRecord) indexstore.ArchiveCommit {
	return indexstore.ArchiveCommit{
		SessionID: session, SHA: sha, ArchivePath: "Projects/alpha/transcripts/" + day + "-" + session + ".jsonl.zst",
		StartDay: day, StartDaySource: indexstore.DayFromTranscript, KG: kg,
	}
}

func commitArchive(t *testing.T, v *storage.Vault, p string, c indexstore.ArchiveCommit) {
	t.Helper()
	mustTx(t, v, p, func(tx *indexstore.Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(c, nil)
	})
}

func commitBatch(t *testing.T, v *storage.Vault, p string, c indexstore.BatchCommit) {
	t.Helper()
	mustTx(t, v, p, func(tx *indexstore.Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitBatch(c, nil)
	})
}

func supersede(t *testing.T, v *storage.Vault, p string, c indexstore.ArchiveCommit, vw indexstore.VectorWriter) error {
	t.Helper()
	return withTx(t, v, p, func(tx *indexstore.Tx) error { return tx.Supersede(c, vw) })
}

// kgLine is one line of the store's KG file, as written.
type kgLine struct {
	ID      string           `json:"id"`
	Owner   indexstore.Owner `json:"owner"`
	Payload json.RawMessage  `json:"payload"`
}

func kgFile(t *testing.T, v *storage.Vault, p string) string {
	t.Helper()
	dir, err := v.IndexDir(p)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "kg", "records.jsonl")
}

func readKGLines(t *testing.T, v *storage.Vault, p string) []kgLine {
	t.Helper()
	f, err := os.Open(kgFile(t, v, p))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []kgLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var l kgLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("bad KG line %q: %v", sc.Text(), err)
		}
		out = append(out, l)
	}
	return out
}

// ownersOf maps each record id to its owner keys, from the file as written.
func ownersOf(lines []kgLine) map[string][]string {
	out := map[string][]string{}
	for _, l := range lines {
		k := l.Owner.Kind + ":" + l.Owner.SHA + l.Owner.ID
		out[l.ID] = append(out[l.ID], k)
	}
	return out
}

// ---- the shared-record fixture ------------------------------------------------

// S1 (2026-05-10, sha aa11) and S2 (2026-05-03, sha bb22) both yield triple T
// and entity E, with different per-owner fields. The earliest day (S2) has the
// LARGER sha, so a pick by owner key alone gives the wrong owner.
var (
	tripleT = [3]string{"alice", "works_on", "atlas"}
	entityE = [3]string{"person-alice", "Alice", "person"}
)

func s1() extraction {
	return extraction{
		triples: []storage.Triple{{Subject: tripleT[0], Predicate: tripleT[1], Object: tripleT[2],
			Confidence: 0.6, ValidFrom: "2026-05-10", SourceSession: "S1", ExtractedAt: "2026-05-10T09:00:00Z"}},
		entities: []storage.Entity{{ID: entityE[0], Name: entityE[1], Type: entityE[2],
			CreatedAt: "2026-05-10T09:00:00Z", Properties: map[string]string{"seen": "s1"}}},
	}
}

func s2() extraction {
	return extraction{
		triples: []storage.Triple{{Subject: tripleT[0], Predicate: tripleT[1], Object: tripleT[2],
			Confidence: 0.9, ValidFrom: "2026-05-03", SourceSession: "S2", ExtractedAt: "2026-05-03T09:00:00Z"}},
		entities: []storage.Entity{{ID: entityE[0], Name: entityE[1], Type: entityE[2],
			CreatedAt: "2026-05-03T09:00:00Z", Properties: map[string]string{"seen": "s2"}}},
	}
}

type ingest struct {
	session, sha, day string
	x                 extraction
}

func ingestAll(t *testing.T, v *storage.Vault, steps ...ingest) {
	t.Helper()
	for _, s := range steps {
		commitArchive(t, v, project, archiveCommit(s.session, s.sha, s.day, records(s.x)))
	}
}

// readerView is what the readers return, for comparing two stores.
type readerView struct {
	Query    []storage.Triple
	Timeline []storage.Triple
	Entities []storage.Entity
	Stats    storage.KGStats
}

func view(t *testing.T, v *storage.Vault, p string) readerView {
	t.Helper()
	q, err := QueryEntity(v, p, tripleT[0], "", "both")
	if err != nil {
		t.Fatal(err)
	}
	tl, err := Timeline(v, p, tripleT[0])
	if err != nil {
		t.Fatal(err)
	}
	es, _, err := ListEntities(v, p)
	if err != nil {
		t.Fatal(err)
	}
	st, err := KGStats(v, p)
	if err != nil {
		t.Fatal(err)
	}
	return readerView{q, tl, es, st}
}

// Order independence (operator ruling on C1): two stores ingest S1 and S2 in
// opposite orders. Payloads are byte-identical, every owner line carries those
// bytes, and the readers report the earliest live owner's date and session.
func TestOrderIndependence(t *testing.T) {
	p, q := newVault(t), newVault(t)
	a := ingest{"S1", "aa11", "2026-05-10", s1()}
	b := ingest{"S2", "bb22", "2026-05-03", s2()}
	ingestAll(t, p, a, b)
	ingestAll(t, q, b, a)

	// (a) byte-identical payloads, on every owner line.
	payloads := func(v *storage.Vault) map[string]string {
		st, err := indexstore.ReadStore(v, project)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, r := range st.KG(true) {
			out[r.ID] = string(r.Payload)
		}
		return out
	}
	pp, qp := payloads(p), payloads(q)
	if len(pp) != 2 || !reflect.DeepEqual(pp, qp) {
		t.Fatalf("payloads differ between ingest orders:\nP %v\nQ %v", pp, qp)
	}
	for name, v := range map[string]*storage.Vault{"P": p, "Q": q} {
		for _, l := range readKGLines(t, v, project) {
			if string(l.Payload) != pp[l.ID] {
				t.Errorf("%s: owner %v of %s carries %s, not the shared payload %s", name, l.Owner, l.ID, l.Payload, pp[l.ID])
			}
		}
	}

	// (b) the readers agree, dated from the earliest live owner (S2).
	pv, qv := view(t, p, project), view(t, q, project)
	if !reflect.DeepEqual(pv, qv) {
		t.Fatalf("readers differ between ingest orders:\nP %+v\nQ %+v", pv, qv)
	}
	if len(pv.Query) != 1 || len(pv.Entities) != 1 {
		t.Fatalf("want one triple and one entity, got %+v", pv)
	}
	tr, en := pv.Query[0], pv.Entities[0]
	if tr.ExtractedAt != "2026-05-03T00:00:00Z" || tr.SourceSession != "S2" || tr.Confidence != 0 || tr.ValidFrom != "" {
		t.Errorf("triple = %+v, want extracted_at 2026-05-03T00:00:00Z, session S2, no confidence, no valid_from", tr)
	}
	if en.CreatedAt != "2026-05-03T00:00:00Z" || len(en.Properties) != 0 {
		t.Errorf("entity = %+v, want created_at 2026-05-03T00:00:00Z and no properties", en)
	}

	// (c) a tie on the day goes to the smaller sha, in both orders.
	p2, q2 := newVault(t), newVault(t)
	c := ingest{"S1", "aa11", "2026-05-07", s1()}
	d := ingest{"S2", "bb22", "2026-05-07", s2()}
	ingestAll(t, p2, c, d)
	ingestAll(t, q2, d, c)
	for name, v := range map[string]*storage.Vault{"P": p2, "Q": q2} {
		got := view(t, v, project).Query
		if len(got) != 1 || got[0].SourceSession != "S1" {
			t.Errorf("%s: a same-day tie reports %+v, want session S1 (the smaller sha)", name, got)
		}
	}
}

// R10: a record carries no project. A byte copy of the store into another
// project's index directory reads back identical there.
func TestNoProjectSlugByteCopy(t *testing.T) {
	v := newVault(t)
	ingestAll(t, v, ingest{"S1", "aa11", "2026-05-10", s1()})
	src, _ := v.IndexDir("alpha")
	dst, _ := v.IndexDir("beta")
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	if a, b := view(t, v, "alpha"), view(t, v, "beta"); !reflect.DeepEqual(a, b) || len(a.Query) != 1 || len(a.Entities) != 1 {
		t.Errorf("the copied store reads differently under beta, or not at all:\nalpha %+v\nbeta  %+v", a, b)
	}
	ta, err := ListTriples(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	tb, err := ListTriples(v, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if len(ta) != 1 || !reflect.DeepEqual(ta, tb) || ta[0].Subject != tripleT[0] {
		t.Errorf("triples differ after a byte copy:\nalpha %+v\nbeta  %+v", ta, tb)
	}
}

// C6: superseding the earliest owner away re-dates the record at the next
// read; the payload bytes do not change. Superseding the last owner away
// deletes it.
func TestSupersedeRedatesThroughTheRead(t *testing.T) {
	v := newVault(t)
	ingestAll(t, v, ingest{"S1", "aa11", "2026-05-10", s1()}, ingest{"S2", "bb22", "2026-05-03", s2()})
	before := readKGLines(t, v, project)

	if err := supersede(t, v, project, archiveCommit("S2", "ab33", "2026-05-04", nil), nil); err != nil {
		t.Fatal(err)
	}
	after := readKGLines(t, v, project)
	want := map[string]string{}
	for _, l := range before {
		want[l.ID] = string(l.Payload)
	}
	for _, l := range after {
		if string(l.Payload) != want[l.ID] {
			t.Errorf("%s: payload changed on supersede: %s -> %s", l.ID, want[l.ID], l.Payload)
		}
		if l.Owner.SHA != "aa11" {
			t.Errorf("%s: owner %v remains after S2 was superseded", l.ID, l.Owner)
		}
	}
	got := view(t, v, project)
	if len(got.Query) != 1 || got.Query[0].SourceSession != "S1" || got.Query[0].ExtractedAt != "2026-05-10T00:00:00Z" {
		t.Errorf("after superseding S2 the triple reads %+v, want S1 and 2026-05-10", got.Query)
	}
	if len(got.Entities) != 1 || got.Entities[0].CreatedAt != "2026-05-10T00:00:00Z" {
		t.Errorf("after superseding S2 the entity reads %+v, want 2026-05-10", got.Entities)
	}

	if err := supersede(t, v, project, archiveCommit("S1", "bc44", "2026-05-11", nil), nil); err != nil {
		t.Fatal(err)
	}
	got = view(t, v, project)
	if len(got.Query) != 0 || len(got.Entities) != 0 {
		t.Errorf("records with no owner left are still read: %+v", got)
	}
	if n := len(readKGLines(t, v, project)); n != 0 {
		t.Errorf("%d KG lines remain with no owner", n)
	}
}

// failVW fails every vector write: a supersede that reaches its vectors step
// stops there, after its superseding mark is durable, as a killed run would.
type failVW struct{}

func (failVW) Put(string, string, []float32) error { return errors.New("killed") }

// M2: a supersede killed after its superseding mark leaves S2 not live; the
// read already skips it, and Reap then drops it from the record. The payload
// bytes never change and the record is dated from S1.
func TestReapRedatesThroughTheRead(t *testing.T) {
	v := newVault(t)
	ingestAll(t, v, ingest{"S1", "aa11", "2026-05-10", s1()}, ingest{"S2", "bb22", "2026-05-03", s2()})
	before := readKGLines(t, v, project)

	content := "a chunk the killed supersede never got to write"
	id := index.ChunkID(content)
	c := archiveCommit("S2", "ab33", "2026-05-04", nil)
	c.Chunks = []indexstore.OwnedChunk{{Chunk: indexstore.Chunk{ID: id, Content: content, Wing: "alpha", Room: "general"}}}
	c.Vectors = map[string][]float32{id: {1, 0}}
	if err := supersede(t, v, project, c, failVW{}); err == nil {
		t.Fatal("the killed supersede reported success")
	}
	if got := view(t, v, project).Query; len(got) != 1 || got[0].SourceSession != "S1" {
		t.Errorf("mid-supersede the triple reads %+v, want S1 (S2 is not live)", got)
	}

	mustTx(t, v, project, func(tx *indexstore.Tx) error {
		_, err := tx.Reap(func() (map[string]bool, error) { return map[string]bool{}, nil })
		return err
	})
	after := readKGLines(t, v, project)
	payload := map[string]string{}
	for _, l := range before {
		payload[l.ID] = string(l.Payload)
	}
	if len(after) != 2 {
		t.Fatalf("after the reap the KG file holds %d lines, want 2 (T and E under S1)", len(after))
	}
	for _, l := range after {
		if l.Owner.SHA != "aa11" || string(l.Payload) != payload[l.ID] {
			t.Errorf("after the reap: %+v, want S1's owner and the unchanged payload", l)
		}
	}
	got := view(t, v, project)
	if len(got.Query) != 1 || got.Query[0].SourceSession != "S1" || got.Query[0].ExtractedAt != "2026-05-10T00:00:00Z" {
		t.Errorf("after the reap the triple reads %+v, want S1 and 2026-05-10", got.Query)
	}
}

// ---- M1: one local triple per subject/predicate/object -------------------------

// endedTripleWith returns a valid_to for which the ended record's id compares
// against the current record's id as wanted.
func endedTripleWith(t *testing.T, s, p, o string, endedIDLarger bool) string {
	t.Helper()
	cur, _ := index.TripleRecord(s, p, o, "")
	for i := 1; i < 1000; i++ {
		vt := fmt.Sprintf("2026-%02d-%02d", 1+i%12, 1+i%28)
		id, _ := index.TripleRecord(s, p, o, vt)
		if (id > cur) == endedIDLarger {
			return vt
		}
	}
	t.Fatal("no valid_to found for the fixture")
	return ""
}

// The readers and the stats show the record with an explicit end; invalidation
// picks the current one. The fixture gives the CURRENT record the smaller id,
// so a pick by id alone, or the two picks swapped, gives a visibly wrong
// answer in each half.
func TestOneLocalTriplePerSPO(t *testing.T) {
	s, p, o := "service-x", "depends_on", "lib-y"
	vt := endedTripleWith(t, s, p, o, true)
	endedID, _ := index.TripleRecord(s, p, o, vt)
	curID, _ := index.TripleRecord(s, p, o, "")
	if !(curID < endedID) {
		t.Fatal("fixture: the current record must have the smaller id")
	}
	ended := ingest{"S1", "aa11", "2026-05-10", extraction{triples: []storage.Triple{{Subject: s, Predicate: p, Object: o, ValidTo: vt}}}}
	current := ingest{"S2", "bb22", "2026-05-03", extraction{triples: []storage.Triple{{Subject: s, Predicate: p, Object: o}}}}

	vs := map[string]*storage.Vault{"ended first": newVault(t), "current first": newVault(t)}
	ingestAll(t, vs["ended first"], ended, current)
	ingestAll(t, vs["current first"], current, ended)
	var views []readerView
	for name, v := range vs {
		q, err := QueryEntity(v, project, s, "", "both")
		if err != nil {
			t.Fatal(err)
		}
		tl, err := Timeline(v, project, s)
		if err != nil {
			t.Fatal(err)
		}
		all, err := ListTriples(v, project)
		if err != nil {
			t.Fatal(err)
		}
		for what, got := range map[string][]storage.Triple{"QueryEntity": q, "Timeline": tl, "ListTriples": all} {
			if len(got) != 1 || got[0].ValidTo != vt || got[0].SourceSession != "S1" {
				t.Errorf("%s: %s = %+v, want the ended record once", name, what, got)
			}
		}
		st, err := KGStats(v, project)
		if err != nil {
			t.Fatal(err)
		}
		if st.TripleCount != 1 || st.CurrentFacts != 0 || st.ExpiredFacts != 1 {
			t.Errorf("%s: KGStats = %+v, want 1 triple, 0 current, 1 expired", name, st)
		}
		views = append(views, readerView{Query: q, Timeline: tl, Stats: st})
	}
	if !reflect.DeepEqual(views[0], views[1]) {
		t.Errorf("the collapse depends on commit order:\n%+v\n%+v", views[0], views[1])
	}

	// Invalidation picks the current record: its session (S2) is carried over.
	v := vs["ended first"]
	if err := InvalidateTriple(v, project, s, p, o, "2026-09-01"); err != nil {
		t.Fatal(err)
	}
	path, _ := v.KGTriplePath(project, s, p, o)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tracked storage.Triple
	if err := json.Unmarshal(b, &tracked); err != nil {
		t.Fatal(err)
	}
	if tracked.SourceSession != "S2" || tracked.ValidTo != "2026-09-01" || tracked.Origin != storage.OriginAuthored || tracked.ExtractedAt != "" {
		t.Errorf("invalidation wrote %+v, want the current record's session S2, valid_to 2026-09-01, authored, no extracted_at", tracked)
	}

	// Two ended records: both picks take the smaller id.
	vt2 := endedTripleWith(t, s, p, o, false)
	id1, _ := index.TripleRecord(s, p, o, vt)
	id2, _ := index.TripleRecord(s, p, o, vt2)
	a := localTriple{id: id1, t: storage.Triple{ValidTo: vt}}
	bb := localTriple{id: id2, t: storage.Triple{ValidTo: vt2}}
	smaller := a
	if bb.id < a.id {
		smaller = bb
	}
	for _, pair := range [][2]localTriple{{a, bb}, {bb, a}} {
		if got := pickForRead(pair[0], pair[1]); got.id != smaller.id {
			t.Errorf("pickForRead between two ended records = %s, want the smaller id %s", got.id, smaller.id)
		}
		if got := pickForInvalidate(pair[0], pair[1]); got.id != smaller.id {
			t.Errorf("pickForInvalidate between two ended records = %s, want the smaller id %s", got.id, smaller.id)
		}
	}
	two := newVault(t)
	ingestAll(t, two,
		ingest{"S1", "aa11", "2026-05-10", extraction{triples: []storage.Triple{{Subject: s, Predicate: p, Object: o, ValidTo: vt}}}},
		ingest{"S2", "bb22", "2026-05-03", extraction{triples: []storage.Triple{{Subject: s, Predicate: p, Object: o, ValidTo: vt2}}}})
	got, err := ListTriples(two, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ValidTo != smaller.t.ValidTo {
		t.Errorf("two ended records read as %+v, want the one with the smaller id (valid_to %s)", got, smaller.t.ValidTo)
	}
	if st, _ := KGStats(two, project); st.TripleCount != 1 || st.ExpiredFacts != 1 {
		t.Errorf("two ended records count as %+v, want 1 triple, 1 expired", st)
	}
}

// ---- ownership ------------------------------------------------------------

func tripleRec(s, p, o string) indexstore.KGRecord {
	id, pl := index.TripleRecord(s, p, o, "")
	return indexstore.KGRecord{ID: id, Payload: pl}
}

// R5, C3: superseding archive A removes A's ownership only. A record another
// archive or a batch owns stays; a record only A owned goes; a tracked record
// with the same triple is untouched and still read. No owner is a path.
func TestOwnershipByShaAndBatch(t *testing.T) {
	v := newVault(t)
	T, U, W, M := tripleRec("t", "p", "1"), tripleRec("u", "p", "1"), tripleRec("w", "p", "1"), tripleRec("m", "p", "1")
	commitArchive(t, v, project, archiveCommit("A", "a1", "2026-05-01", []indexstore.KGRecord{T, U, W}))
	commitArchive(t, v, project, archiveCommit("B", "b1", "2026-05-02", []indexstore.KGRecord{T}))
	commitBatch(t, v, project, indexstore.BatchCommit{BatchID: "mempalace:x:0", StartDay: "2026-04-01", KG: []indexstore.KGRecord{W, M}})
	if err := v.AddAuthoredTriple(project, storage.Triple{Subject: "u", Predicate: "p", Object: "1", Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	if err := supersede(t, v, project, archiveCommit("A", "a2", "2026-05-05", nil), nil); err != nil {
		t.Fatal(err)
	}
	owners := ownersOf(readKGLines(t, v, project))
	for id, want := range map[string][]string{
		T.ID: {"archive:b1"},
		W.ID: {"batch:mempalace:x:0"},
		M.ID: {"batch:mempalace:x:0"},
	} {
		if !reflect.DeepEqual(owners[id], want) {
			t.Errorf("owners of %s = %v, want %v", id, owners[id], want)
		}
	}
	if _, ok := owners[U.ID]; ok {
		t.Errorf("U, owned only by the superseded archive, survives: %v", owners[U.ID])
	}
	for _, l := range readKGLines(t, v, project) {
		if strings.Contains(l.Owner.SHA+l.Owner.ID, "/") {
			t.Errorf("owner %v is a path", l.Owner)
		}
	}
	got, err := QueryEntity(v, project, "u", "", "out")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Origin != storage.OriginAuthored {
		t.Errorf("the tracked X is not returned untouched: %+v", got)
	}
}

// 3-B1: a same-day re-archive shares the path and differs in sha; the
// supersede is keyed on the sha.
func TestSamePathSupersede(t *testing.T) {
	v := newVault(t)
	T, U, V := tripleRec("t", "p", "1"), tripleRec("u", "p", "1"), tripleRec("v", "p", "1")
	commitArchive(t, v, project, archiveCommit("A", "a1", "2026-05-01", []indexstore.KGRecord{T, U}))
	if err := supersede(t, v, project, archiveCommit("A", "a2", "2026-05-01", []indexstore.KGRecord{T, V}), nil); err != nil {
		t.Fatal(err)
	}
	owners := ownersOf(readKGLines(t, v, project))
	if _, ok := owners[U.ID]; ok {
		t.Error("U, only in the older archive, survives")
	}
	if !reflect.DeepEqual(owners[V.ID], []string{"archive:a2"}) || !reflect.DeepEqual(owners[T.ID], []string{"archive:a2"}) {
		t.Errorf("owners after a same-path supersede: %v", owners)
	}
}

// ---- visibility, reload, union --------------------------------------------------

func appendRawKGLine(t *testing.T, v *storage.Vault, p string, l kgLine) {
	t.Helper()
	path := kgFile(t, v, p)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

// Records of a commit killed after its KG step (written, not ledgered) and of
// a failed archive are never read; once the commits complete, they are read
// exactly once.
func TestOnlyLedgeredLocalRecordsAreRead(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, project, func(tx *indexstore.Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.RecordFailure("SZ", "zz99", errors.New("extract failed"))
	})
	arch := tripleRec("killed", "p", "archive")
	batch := tripleRec("killed", "p", "batch")
	ent, entPayload := index.EntityRecord("e-killed", "Killed", "thing")
	appendRawKGLine(t, v, project, kgLine{ID: arch.ID, Owner: indexstore.ArchiveOwner("zz99"), Payload: arch.Payload})
	appendRawKGLine(t, v, project, kgLine{ID: ent, Owner: indexstore.ArchiveOwner("zz99"), Payload: entPayload})
	appendRawKGLine(t, v, project, kgLine{ID: batch.ID, Owner: indexstore.BatchOwner("mempalace:q:0"), Payload: batch.Payload})

	countAll := func() (int, int, storage.KGStats) {
		q, err := QueryEntity(v, project, "killed", "", "both")
		if err != nil {
			t.Fatal(err)
		}
		tl, err := Timeline(v, project, "killed")
		if err != nil {
			t.Fatal(err)
		}
		all, err := ListTriples(v, project)
		if err != nil {
			t.Fatal(err)
		}
		es, _, err := ListEntities(v, project)
		if err != nil {
			t.Fatal(err)
		}
		st, err := KGStats(v, project)
		if err != nil {
			t.Fatal(err)
		}
		if len(q) != len(tl) || len(q) != len(all) {
			t.Fatalf("readers disagree: query %d, timeline %d, list %d", len(q), len(tl), len(all))
		}
		return len(q), len(es), st
	}
	if n, ne, st := countAll(); n != 0 || ne != 0 || st.TripleCount != 0 || st.EntityCount != 0 {
		t.Fatalf("unledgered records are read: %d triples, %d entities, stats %+v", n, ne, st)
	}

	commitArchive(t, v, project, archiveCommit("SZ", "zz99", "2026-05-01", []indexstore.KGRecord{arch, {ID: ent, Payload: entPayload}}))
	commitBatch(t, v, project, indexstore.BatchCommit{BatchID: "mempalace:q:0", StartDay: "2026-04-01", KG: []indexstore.KGRecord{batch}})
	if n, ne, st := countAll(); n != 2 || ne != 1 || st.TripleCount != 2 || st.EntityCount != 1 {
		t.Fatalf("after the resumed commits: %d triples, %d entities, stats %+v; want 2, 1", n, ne, st)
	}
}

// The local KG is reloaded in full on any counter change, and only then.
func TestLocalKGReloadIsFull(t *testing.T) {
	v := newVault(t)
	var reads atomic.Int64
	old := readStoreFn
	readStoreFn = func(v *storage.Vault, p string) (*indexstore.Store, error) {
		reads.Add(1)
		return old(v, p)
	}
	t.Cleanup(func() { readStoreFn = old })

	ingestAll(t, v, ingest{"S1", "aa11", "2026-05-10", s1()})
	query := func() int {
		got, err := QueryEntity(v, project, tripleT[0], "", "both")
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	if n := query(); n != 1 || reads.Load() != 1 {
		t.Fatalf("first query: %d triples, %d store reads", n, reads.Load())
	}
	if n := query(); n != 1 || reads.Load() != 1 {
		t.Fatalf("an unchanged counter re-read the store: %d reads", reads.Load())
	}
	commitArchive(t, v, project, archiveCommit("S3", "cc33", "2026-05-12",
		records(extraction{triples: []storage.Triple{{Subject: tripleT[0], Predicate: "knows", Object: "bob"}}})))
	if n := query(); n != 2 || reads.Load() != 2 {
		t.Fatalf("after gen grew: %d triples, %d reads; want 2, 2", n, reads.Load())
	}
	if err := supersede(t, v, project, archiveCommit("S3", "cc34", "2026-05-13", nil), nil); err != nil {
		t.Fatal(err)
	}
	if n := query(); n != 1 || reads.Load() != 3 {
		t.Fatalf("after the epoch changed: %d triples, %d reads; want 1, 3", n, reads.Load())
	}
}

// A tracked fact and a local fact are both returned; a tracked triple hides a
// local one with the same subject/predicate/object, whatever its valid_to; a
// tracked entity line hides a local record with its id; EntityCount counts
// distinct ids.
func TestUnionAndTrackedWins(t *testing.T) {
	v := newVault(t)
	if err := v.AddAuthoredTriple(project, storage.Triple{Subject: "alice", Predicate: "likes", Object: "tea", Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	if err := v.AddAuthoredTriple(project, storage.Triple{Subject: "alice", Predicate: "works_on", Object: "atlas", Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.AddEntities(project, []storage.Entity{{ID: "person-alice", Name: "Alice", Type: "unknown", Origin: storage.OriginAuthored}}); err != nil {
		t.Fatal(err)
	}
	ingestAll(t, v, ingest{"S1", "aa11", "2026-05-10", extraction{
		triples: []storage.Triple{
			{Subject: "alice", Predicate: "knows", Object: "bob"},
			{Subject: "alice", Predicate: "works_on", Object: "atlas", ValidTo: "2026-06-01"},
		},
		entities: []storage.Entity{{ID: "person-alice", Name: "Alice", Type: "person"}, {ID: "person-bob", Name: "Bob", Type: "person"}},
	}})
	got, err := QueryEntity(v, project, "alice", "", "out")
	if err != nil {
		t.Fatal(err)
	}
	byPred := map[string]storage.Triple{}
	for _, tr := range got {
		if _, dup := byPred[tr.Predicate]; dup {
			t.Errorf("predicate %s returned twice", tr.Predicate)
		}
		byPred[tr.Predicate] = tr
	}
	if len(byPred) != 3 || byPred["works_on"].ValidTo != "" || byPred["works_on"].Origin != storage.OriginAuthored {
		t.Errorf("union = %+v, want likes and works_on (tracked) and knows (local)", got)
	}
	es, _, err := ListEntities(v, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 || es[0].Type != "unknown" {
		t.Errorf("entities = %+v, want the tracked person-alice and the local person-bob", es)
	}
	st, err := KGStats(v, project)
	if err != nil {
		t.Fatal(err)
	}
	if st.EntityCount != 2 || st.TripleCount != 3 {
		t.Errorf("stats = %+v, want 2 entities and 3 triples", st)
	}
}

// Every entry point fails closed on a vault behind the binary, including
// ListEntities and InvalidateTriple, which storage leaves ungated.
func TestFormatGateOnEveryEntryPoint(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Projects", project), 0o755); err != nil {
		t.Fatal(err)
	}
	v := storage.NewVault(root) // no vault.toml: data format 0
	checks := map[string]func() error{
		"QueryEntity":      func() error { _, err := QueryEntity(v, project, "a", "", "both"); return err },
		"Timeline":         func() error { _, err := Timeline(v, project, "a"); return err },
		"KGStats":          func() error { _, err := KGStats(v, project); return err },
		"ListTriples":      func() error { _, err := ListTriples(v, project); return err },
		"ListEntities":     func() error { _, _, err := ListEntities(v, project); return err },
		"InvalidateTriple": func() error { return InvalidateTriple(v, project, "a", "p", "b", "2026-01-01") },
	}
	for name, f := range checks {
		var fe *surface.FormatIncompatibleError
		if err := f(); !errors.As(err, &fe) {
			t.Errorf("%s on a format-0 vault = %v, want a FormatIncompatibleError", name, err)
		}
	}
}

// 3-N2: invalidating a triple that exists only in the local store writes a
// tracked authored file, which then hides the local record.
func TestInvalidateLocalOnlyTriple(t *testing.T) {
	v := newVault(t)
	ingestAll(t, v, ingest{"S1", "aa11", "2026-05-10", s1()})
	if err := InvalidateTriple(v, project, tripleT[0], tripleT[1], tripleT[2], "2026-06-01"); err != nil {
		t.Fatal(err)
	}
	path, _ := v.KGTriplePath(project, tripleT[0], tripleT[1], tripleT[2])
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no tracked overlay was written: %v", err)
	}
	var tr storage.Triple
	if err := json.Unmarshal(b, &tr); err != nil {
		t.Fatal(err)
	}
	if tr.ValidTo != "2026-06-01" || tr.SourceSession != "S1" || tr.ExtractedAt != "" || tr.Origin != storage.OriginAuthored {
		t.Errorf("overlay = %+v", tr)
	}
	got, err := QueryEntity(v, project, tripleT[0], "2026-07-01", "both")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("as_of after valid_to still returns %+v", got)
	}
	if err := InvalidateTriple(v, project, "nobody", "p", "x", "2026-06-01"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("invalidating an unknown triple = %v, want fs.ErrNotExist", err)
	}
}

// ---- kg-N4: dedup across processes ---------------------------------------------

func bigCommit() indexstore.ArchiveCommit {
	var x extraction
	for i := range 500 {
		x.triples = append(x.triples, storage.Triple{Subject: fmt.Sprintf("s%d", i), Predicate: "p", Object: "o"})
	}
	for i := range 200 {
		x.entities = append(x.entities, storage.Entity{ID: fmt.Sprintf("e%d", i), Name: fmt.Sprintf("E%d", i), Type: "thing"})
	}
	return archiveCommit("SBIG", "big1", "2026-05-01", records(x))
}

func helperCommit() int {
	v := storage.NewVault(os.Getenv("KGREAD_VAULT"))
	tx, err := indexstore.Lock(context.Background(), v, project, indexstore.NoTimeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer tx.Release()
	tx.UseRecipe(testRecipe)
	if _, err := tx.EnsureLedger(nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := tx.CommitArchive(bigCommit(), nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := tx.Commit(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// Two processes commit the same archive at once: the store's (id, owner) key
// index, re-read under the commit lock, keeps one line per record.
func TestLocalKGDedupAcrossProcesses(t *testing.T) {
	v := newVault(t)
	var cmds []*exec.Cmd
	for range 2 {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), "KGREAD_HELPER=commit", "KGREAD_VAULT="+v.Root)
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("helper: %v", err)
		}
	}
	lines := readKGLines(t, v, project)
	if len(lines) != 700 {
		t.Fatalf("records.jsonl holds %d lines, want 700", len(lines))
	}
	for id, owners := range ownersOf(lines) {
		if len(owners) != 1 {
			t.Errorf("%s has owners %v, want one", id, owners)
		}
	}
}

// Invalidation picks the CURRENT record whichever record has the smaller id
// and whichever was committed first: the mirror of TestOneLocalTriplePerSPO's
// fixture, so "empty valid_to first" is pinned on its own and not only by id
// order.
func TestInvalidatePicksTheCurrentRecord(t *testing.T) {
	s, p, o := "service-x", "depends_on", "lib-y"
	for _, endedIDLarger := range []bool{true, false} {
		vt := endedTripleWith(t, s, p, o, endedIDLarger)
		ended := ingest{"S1", "aa11", "2026-05-10", extraction{triples: []storage.Triple{{Subject: s, Predicate: p, Object: o, ValidTo: vt}}}}
		current := ingest{"S2", "bb22", "2026-05-03", extraction{triples: []storage.Triple{{Subject: s, Predicate: p, Object: o}}}}
		for _, order := range [][2]ingest{{ended, current}, {current, ended}} {
			v := newVault(t)
			ingestAll(t, v, order[0], order[1])
			if err := InvalidateTriple(v, project, s, p, o, "2026-09-01"); err != nil {
				t.Fatal(err)
			}
			path, _ := v.KGTriplePath(project, s, p, o)
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var tracked storage.Triple
			if err := json.Unmarshal(b, &tracked); err != nil {
				t.Fatal(err)
			}
			if tracked.SourceSession != "S2" || tracked.ValidTo != "2026-09-01" {
				t.Errorf("ended id larger=%v, %s committed first: invalidation wrote %+v, want the current record's session S2",
					endedIDLarger, order[0].session, tracked)
			}
		}
	}
}

// The earliest live owner is chosen by day first, across kinds too: an
// archive and a batch on one day go to the archive (the smaller kind), and a
// batch on an earlier day beats a later archive.
func TestEarliestOwnerAcrossKinds(t *testing.T) {
	rec := records(extraction{triples: []storage.Triple{{Subject: "k", Predicate: "p", Object: "o"}}})
	for _, c := range []struct {
		name               string
		archiveDay, batchD string
		wantSession, wantD string
	}{
		{"same day: the archive kind wins", "2026-05-05", "2026-05-05", "SA", "2026-05-05T00:00:00Z"},
		{"an earlier batch beats a later archive", "2026-05-09", "2026-05-01", "", "2026-05-01T00:00:00Z"},
		{"an earlier archive beats a later batch", "2026-05-01", "2026-05-09", "SA", "2026-05-01T00:00:00Z"},
	} {
		for _, batchFirst := range []bool{false, true} {
			v := newVault(t)
			arch := func() { commitArchive(t, v, project, archiveCommit("SA", "zz99", c.archiveDay, rec)) }
			batch := func() {
				commitBatch(t, v, project, indexstore.BatchCommit{BatchID: "mempalace:m:0", StartDay: c.batchD, KG: rec})
			}
			if batchFirst {
				batch()
				arch()
			} else {
				arch()
				batch()
			}
			got, err := ListTriples(v, project)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].SourceSession != c.wantSession || got[0].ExtractedAt != c.wantD {
				t.Errorf("%s (batch first=%v): %+v, want session %q dated %s", c.name, batchFirst, got, c.wantSession, c.wantD)
			}
		}
	}
}

// The local half applies the direction filter as storage does for tracked
// files: out matches the subject, in matches the object, both matches either.
func TestLocalDirectionFilter(t *testing.T) {
	v := newVault(t)
	ingestAll(t, v, ingest{"S1", "aa11", "2026-05-10", extraction{triples: []storage.Triple{{Subject: "x", Predicate: "knows", Object: "y"}}}})
	for _, c := range []struct {
		name, direction string
		want            int
	}{
		{"x", "out", 1}, {"x", "in", 0}, {"y", "out", 0}, {"y", "in", 1}, {"x", "both", 1}, {"y", "both", 1},
	} {
		got, err := QueryEntity(v, project, c.name, "", c.direction)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != c.want {
			t.Errorf("QueryEntity(%s, %s) = %d triples, want %d", c.name, c.direction, len(got), c.want)
		}
	}
}

// One entity id with two local records (two names) is two rows in
// ListEntities and one entity in KGStats.
func TestEntityCountIsDistinctIDs(t *testing.T) {
	v := newVault(t)
	ingestAll(t, v, ingest{"S1", "aa11", "2026-05-10", extraction{entities: []storage.Entity{
		{ID: "tool-go", Name: "Go", Type: "tool"}, {ID: "tool-go", Name: "go", Type: "tool"}, {ID: "tool-rust", Name: "Rust", Type: "tool"},
	}}})
	es, _, err := ListEntities(v, project)
	if err != nil {
		t.Fatal(err)
	}
	st, err := KGStats(v, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 3 || st.EntityCount != 2 {
		t.Errorf("ListEntities = %d rows, EntityCount = %d; want 3 rows and 2 distinct ids", len(es), st.EntityCount)
	}
}

// The as_of rule matches storage's: a fact is no longer valid ON its valid_to
// day, and a tracked fact whose valid_from is after as_of is not yet valid.
func TestAsOfBoundaries(t *testing.T) {
	v := newVault(t)
	if err := v.AddAuthoredTriple(project, storage.Triple{Subject: "a", Predicate: "starts", Object: "later", ValidFrom: "2026-07-01"}); err != nil {
		t.Fatal(err)
	}
	ingestAll(t, v, ingest{"S1", "aa11", "2026-05-10", extraction{triples: []storage.Triple{{Subject: "a", Predicate: "ended", Object: "june", ValidTo: "2026-06-01"}}}})
	preds := func(asOf string) []string {
		got, err := QueryEntity(v, project, "a", asOf, "out")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tr := range got {
			out = append(out, tr.Predicate)
		}
		slices.Sort(out)
		return out
	}
	for asOf, want := range map[string][]string{
		"2026-05-31": {"ended"},
		"2026-06-01": nil,
		"2026-06-15": nil,
		"2026-07-01": {"starts"},
	} {
		if got := preds(asOf); !slices.Equal(got, want) {
			t.Errorf("as_of %s: %v, want %v", asOf, got, want)
		}
	}
}

// Chair ruling: a local triple, which has no valid_from, takes its place in a
// timeline by its read-time derived day, so a mixed timeline is chronological.
// Ties fall to extracted_at, then to subject, predicate and object.
func TestTimelineChronologyAndTieBreaks(t *testing.T) {
	v := newVault(t)
	for _, tr := range []storage.Triple{
		{Subject: "e", Predicate: "authored_mid", Object: "x", ValidFrom: "2026-05-05"},
		// Same valid_from: extracted_at decides (b before a), though a sorts first by name.
		{Subject: "e", Predicate: "a_late", Object: "x", ValidFrom: "2026-08-01", ExtractedAt: "2026-02-01T00:00:00Z", SourceSession: "s"},
		{Subject: "e", Predicate: "b_early", Object: "x", ValidFrom: "2026-08-01", ExtractedAt: "2026-01-01T00:00:00Z", SourceSession: "s"},
		// Same valid_from and extracted_at: subject/predicate/object decides
		// (upper-case B before a), the reverse of their file order.
		{Subject: "e", Predicate: "a", Object: "tie", ValidFrom: "2026-09-01"},
		{Subject: "e", Predicate: "B", Object: "tie", ValidFrom: "2026-09-01"},
	} {
		path, _ := v.KGTriplePath(project, tr.Subject, tr.Predicate, tr.Object)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(tr)
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ingestAll(t, v,
		ingest{"S1", "aa11", "2026-05-03", extraction{triples: []storage.Triple{{Subject: "e", Predicate: "local_early", Object: "x"}}}},
		ingest{"S2", "bb22", "2026-05-10", extraction{triples: []storage.Triple{{Subject: "e", Predicate: "local_late", Object: "x"}}}})
	got, err := Timeline(v, project, "e")
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, tr := range got {
		order = append(order, tr.Predicate)
	}
	want := []string{"local_early", "authored_mid", "local_late", "b_early", "a_late", "B", "a"}
	if !slices.Equal(order, want) {
		t.Errorf("timeline = %v, want %v", order, want)
	}
}
