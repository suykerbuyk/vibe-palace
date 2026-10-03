// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// ---- fixtures --------------------------------------------------------------

func ownedChunk(content, wing, room string) OwnedChunk {
	return OwnedChunk{
		Chunk:     Chunk{ID: index.ChunkID(content), Content: content, Wing: wing, Room: room, Hall: "facts"},
		Ownership: Ownership{SourceType: "transcript"},
	}
}

func withDay(recs []OwnedChunk, day string) []OwnedChunk {
	out := slices.Clone(recs)
	for i := range out {
		out[i].Day = day
	}
	return out
}

// commitOf builds an archive commit for session with contents as chunks, each
// with a vector and a KG record, and source_ref naming the archive.
func commitOf(session, sha, day string, contents ...string) ArchiveCommit {
	c := ArchiveCommit{
		SessionID: session, SHA: sha, ArchivePath: "Projects/alpha/transcripts/" + session + ".jsonl.zst",
		StartDay: day, StartDaySource: DayFromTranscript, Vectors: map[string][]float32{},
	}
	for i, content := range contents {
		oc := ownedChunk(content, "alpha", "general")
		oc.SourceRef = "ref-" + sha
		oc.ChunkIndex = i
		c.Chunks = append(c.Chunks, oc)
		c.Vectors[oc.ID] = []float32{float32(i), 1}
		c.KG = append(c.KG, KGRecord{ID: "kg-" + oc.ID, Payload: []byte(fmt.Sprintf(`{"s":%q}`, content))})
	}
	return c
}

// recordingVW is a VectorWriter that records its puts in a shared event log.
type recordingVW struct {
	mu     sync.Mutex
	events *[]string
	puts   map[string]int
}

func newRecordingVW(events *[]string) *recordingVW {
	return &recordingVW{events: events, puts: map[string]int{}}
}

func (r *recordingVW) Put(project, id string, vec []float32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.puts[id]++
	if r.events != nil {
		*r.events = append(*r.events, "put")
	}
	return nil
}

// withTx runs f under alpha's commit lock and commits.
func withTx(t *testing.T, v *storage.Vault, f func(tx *Tx) error) error {
	t.Helper()
	tx := mustLock(t)(Lock(context.Background(), v, "alpha", NoTimeout))
	err := f(tx)
	if cerr := tx.Release(); err == nil {
		err = cerr
	}
	return err
}

func mustTx(t *testing.T, v *storage.Vault, f func(tx *Tx) error) {
	t.Helper()
	if err := withTx(t, v, f); err != nil {
		t.Fatal(err)
	}
}

// fakeArchives makes EnsureLedger and Discard see these archives.
func fakeArchives(t *testing.T, shas ...string) {
	t.Helper()
	old := listArchivesFn
	listArchivesFn = func(string, string) ([]*archive.Entry, error) {
		var out []*archive.Entry
		for _, s := range shas {
			out = append(out, &archive.Entry{ArchivePath: s + ".jsonl.zst", Manifest: &archive.Manifest{SourceSHA256: s}})
		}
		return out, nil
	}
	t.Cleanup(func() { listArchivesFn = old })
}

func ledgered(t *testing.T, v *storage.Vault) *Store {
	t.Helper()
	s, err := ReadStore(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// isLive reports whether a session's latest ledger record is live.
func isLive(l *Ledger, session string) bool {
	s, ok := l.Session(session)
	return ok && s.State == StateLive
}

// baselineOf is the ledger's baseline set, sorted.
func baselineOf(l *Ledger) []string {
	out := []string{}
	for s := range l.baseline {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// genStep is how far one commit moves gen: 1 for a commit that only
// appended, 2 for one that did more (Tx.beginDestructive bumps before the
// first destructive step, and the commit bumps again).
func genStep(moreThanAppend bool) uint64 {
	if moreThanAppend {
		return 2
	}
	return 1
}

func ids(cs []StoredChunk) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.ID)
	}
	sort.Strings(out)
	return out
}

func kgIDs(ks []StoredKG) []string {
	var out []string
	for _, k := range ks {
		out = append(out, k.ID)
	}
	sort.Strings(out)
	return out
}

func idOf(content string) string { return index.ChunkID(content) }

func sorted(ss ...string) []string { sort.Strings(ss); return ss }

func fileLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

var errKill = errors.New("killed")

// killAt makes the commit stop after the named step, as a kill would.
func killAt(t *testing.T, step string) {
	t.Helper()
	old := commitStep
	commitStep = func(s string) error {
		if s == step {
			return errKill
		}
		return nil
	}
	t.Cleanup(func() { commitStep = old })
}

// ---- tests -----------------------------------------------------------------

// Two contents whose legacy 32-bit drawer ids collide are two chunks in the
// store: both stored, both listed.
func TestCollidingDrawerIDsAreTwoStoreChunks(t *testing.T) {
	v := newVault(t)
	seen := map[string]string{}
	var a, b string
	for i := 0; a == ""; i++ {
		c := fmt.Sprintf("chunk content %d", i)
		if prev, ok := seen[storage.DrawerID("alpha", c)]; ok {
			a, b = prev, c
		}
		seen[storage.DrawerID("alpha", c)] = c
	}
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(commitOf("s1", "sha1", "2026-05-13", a, b), newRecordingVW(nil))
	})
	if got, want := ids(ledgered(t, v).Chunks(true)), sorted(idOf(a), idOf(b)); !slices.Equal(got, want) {
		t.Fatalf("listed %v, want both colliding contents %v", got, want)
	}
}

// The per-archive write order: vectors, then chunks, then KG, then the ledger
// record, which carries the chunk count and start day. A kill after each step
// leaves the session pending and its records invisible; a rerun completes it
// with no duplicate line.
func TestPerArchiveWriteOrderAndKills(t *testing.T) {
	var events []string
	old := commitStep
	commitStep = func(s string) error { events = append(events, s); return nil }
	v := newVault(t)
	c := commitOf("s1", "sha1", "2026-05-13", "x", "y")
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(c, newRecordingVW(&events))
	})
	commitStep = old
	if want := []string{"put", "put", "vectors", "chunks", "kg", "ledger"}; !slices.Equal(events, want) {
		t.Fatalf("commit steps %v, want %v", events, want)
	}
	sr, _ := ledgered(t, v).Ledger().Session("s1")
	if sr.State != StateLive || sr.ChunkCount != 2 || sr.StartDay != "2026-05-13" || sr.StartDaySource != DayFromTranscript || sr.Generation != 1 {
		t.Fatalf("ledger record %+v", sr)
	}

	for _, step := range []string{"vectors", "chunks", "kg"} {
		t.Run("kill after "+step, func(t *testing.T) {
			v := newVault(t)
			mustTx(t, v, func(tx *Tx) error { _, err := tx.EnsureLedger(nil); return err })
			killAt(t, step)
			if err := withTx(t, v, func(tx *Tx) error { return tx.CommitArchive(c, newRecordingVW(nil)) }); !errors.Is(err, errKill) {
				t.Fatalf("err=%v", err)
			}
			s := ledgered(t, v)
			if p := s.Ledger().Pending([]ArchiveRef{{SessionID: "s1", SHA: "sha1"}}); len(p) != 1 {
				t.Fatalf("killed after %s: the session is not pending", step)
			}
			if n := len(s.Chunks(true)) + len(s.KG(true)); n != 0 {
				t.Fatalf("killed after %s: %d records are visible", step, n)
			}
			commitStep = func(string) error { return nil }
			mustTx(t, v, func(tx *Tx) error { return tx.CommitArchive(c, newRecordingVW(nil)) })
			s = ledgered(t, v)
			if len(s.Chunks(true)) != 2 || len(s.KG(true)) != 2 {
				t.Fatalf("after the rerun: %d chunks, %d KG records visible, want 2 and 2", len(s.Chunks(true)), len(s.KG(true)))
			}
			pf, _ := filesFor(v, "alpha")
			if n := len(fileLines(t, pf.chunks)); n != 4 {
				t.Fatalf("chunks.jsonl has %d lines after the rerun, want 4 (2 chunks + 2 owner lines)", n)
			}
			if n := len(fileLines(t, pf.kg)); n != 2 {
				t.Fatalf("KG file has %d lines after the rerun, want 2", n)
			}
		})
	}
}

// Search loads only ledgered records: an archive killed before its ledger
// record is invisible, a note-owned chunk is visible, and a batch is visible
// only once its batch record is written.
func TestSearchLoadsOnlyLedgeredRecords(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error { _, err := tx.EnsureLedger(nil); return err })
	killAt(t, "kg")
	_ = withTx(t, v, func(tx *Tx) error {
		return tx.CommitArchive(commitOf("s1", "sha1", "2026-05-13", "x"), newRecordingVW(nil))
	})
	commitStep = func(string) error { return nil }
	mustTx(t, v, func(tx *Tx) error {
		return tx.Append(NoteOwner("notes/a.md"), withDay([]OwnedChunk{ownedChunk("decision d", "alpha", "decisions")}, "2026-05-20"))
	})
	batch := BatchCommit{BatchID: "batch:m", StartDay: "2026-04-02", Chunks: []OwnedChunk{ownedChunk("imported b", "alpha", "general")},
		KG: []KGRecord{{ID: "kg-b", Payload: []byte(`{}`)}}}
	killAt(t, "kg")
	_ = withTx(t, v, func(tx *Tx) error { return tx.CommitBatch(batch, nil) })
	if got, want := ids(ledgered(t, v).Chunks(true)), []string{idOf("decision d")}; !slices.Equal(got, want) {
		t.Fatalf("visible %v, want only the note's chunk %v", got, want)
	}
	if got := ledgered(t, v).KG(true); len(got) != 0 {
		t.Fatalf("visible KG %v, want none", got)
	}
	if got := ids(ledgered(t, v).Chunks(false)); len(got) != 3 {
		t.Fatalf("all chunks %v, want 3 stored", got)
	}
	commitStep = func(string) error { return nil }
	mustTx(t, v, func(tx *Tx) error { return tx.CommitBatch(batch, nil) })
	if got, want := ids(ledgered(t, v).Chunks(true)), sorted(idOf("decision d"), idOf("imported b")); !slices.Equal(got, want) {
		t.Fatalf("visible %v, want the note's and the batch's %v", got, want)
	}
	if got := kgIDs(ledgered(t, v).KG(true)); !slices.Equal(got, []string{"kg-b"}) {
		t.Fatalf("visible KG %v, want [kg-b]", got)
	}
}

// Supersede: S is live with A (chunks X, Y), X is also owned by session B, and
// decision chunk D has a note owner. After the supersede to A′ (chunks X, Z),
// Y is gone, X and D remain, S's generation is 2 and the epoch changed. A kill
// after step 1 or step 2 leaves S pending, and a rerun ends in the same state.
func TestSupersedeSteps(t *testing.T) {
	setup := func(t *testing.T) *storage.Vault {
		v := newVault(t)
		mustTx(t, v, func(tx *Tx) error {
			if _, err := tx.EnsureLedger(nil); err != nil {
				return err
			}
			if err := tx.CommitArchive(commitOf("S", "A", "2026-05-13", "X", "Y"), newRecordingVW(nil)); err != nil {
				return err
			}
			if err := tx.CommitArchive(commitOf("B", "Bsha", "2026-05-14", "X"), newRecordingVW(nil)); err != nil {
				return err
			}
			return tx.Append(NoteOwner("notes/s.md"), withDay([]OwnedChunk{ownedChunk("D", "alpha", "decisions")}, "2026-05-13"))
		})
		return v
	}
	newer := commitOf("S", "A2", "2026-05-15", "X", "Z")
	check := func(t *testing.T, v *storage.Vault) {
		t.Helper()
		s := ledgered(t, v)
		if got, want := ids(s.Chunks(false)), sorted(idOf("X"), idOf("Z"), idOf("D")); !slices.Equal(got, want) {
			t.Fatalf("stored %v, want X, Z and D (Y deleted)", got)
		}
		sr, _ := s.Ledger().Session("S")
		if sr.State != StateLive || sr.SHA != "A2" || sr.Generation != 2 || sr.StartDay != "2026-05-15" {
			t.Fatalf("S = %+v, want live A2 generation 2", sr)
		}
		for _, c := range s.Chunks(false) {
			for _, o := range c.Owners {
				if o == ArchiveOwner("A") {
					t.Fatalf("chunk %s still owned by A", c.Content)
				}
			}
		}
		if got := kgIDs(s.KG(false)); !slices.Equal(got, sorted("kg-"+idOf("X"), "kg-"+idOf("Z"))) {
			t.Fatalf("KG %v, want X's and Z's", got)
		}
	}

	v := setup(t)
	before := readGen(t, v, "alpha")
	mustTx(t, v, func(tx *Tx) error { return tx.Supersede(newer, newRecordingVW(nil)) })
	check(t, v)
	if after := readGen(t, v, "alpha"); after.Epoch == before.Epoch {
		t.Fatal("a supersede did not change the epoch")
	}

	for _, step := range []string{"superseding", "chunks"} {
		t.Run("kill after "+step, func(t *testing.T) {
			v := setup(t)
			killAt(t, step)
			if err := withTx(t, v, func(tx *Tx) error { return tx.Supersede(newer, newRecordingVW(nil)) }); !errors.Is(err, errKill) {
				t.Fatalf("err=%v", err)
			}
			sr, _ := ledgered(t, v).Ledger().Session("S")
			if sr.State != StateSuperseding {
				t.Fatalf("after a kill at %s the session reads %+v, want superseding", step, sr)
			}
			if p := ledgered(t, v).Ledger().Pending([]ArchiveRef{{SessionID: "S", SHA: "A2"}, {SessionID: "S", SHA: "A"}}); len(p) != 1 || p[0].SHA != "A2" {
				t.Fatalf("pending %v, want only A2", p)
			}
			commitStep = func(string) error { return nil }
			mustTx(t, v, func(tx *Tx) error { return tx.Supersede(newer, newRecordingVW(nil)) })
			check(t, v)
		})
	}

	// An ingest of the older archive that another process commits first is
	// seen under the lock, and A's ownership does not survive the supersede;
	// one that arrives after is refused.
	t.Run("concurrent commit of A", func(t *testing.T) {
		v := setup(t)
		mustTx(t, v, func(tx *Tx) error {
			return tx.CommitArchive(commitOf("S", "A", "2026-05-13", "X", "Y", "W"), newRecordingVW(nil))
		})
		mustTx(t, v, func(tx *Tx) error { return tx.Supersede(newer, newRecordingVW(nil)) })
		check(t, v)
		err := withTx(t, v, func(tx *Tx) error {
			return tx.CommitArchive(commitOf("S", "A", "2026-05-13", "X", "Y"), newRecordingVW(nil))
		})
		if !errors.Is(err, ErrSuperseded) {
			t.Fatalf("a late commit of A: err=%v, want ErrSuperseded", err)
		}
		check(t, v)
	})
}

// A′ written at the SAME archive path as A, with different bytes: owners are
// keyed by source_sha256, so the supersede removes Y (only in A), keeps X and
// adds Z, each owned by A′ alone. The result is the same whichever order the
// chunks arrive in.
func TestSamePathSupersede(t *testing.T) {
	run := func(reverse bool) []StoredChunk {
		v := newVault(t)
		a := commitOf("S", "shaA", "2026-05-13", "X", "Y")
		a2 := commitOf("S", "shaA2", "2026-05-13", "X", "Z")
		a2.ArchivePath = a.ArchivePath
		if reverse {
			slices.Reverse(a2.Chunks)
		}
		mustTx(t, v, func(tx *Tx) error {
			if _, err := tx.EnsureLedger(nil); err != nil {
				return err
			}
			if err := tx.CommitArchive(a, newRecordingVW(nil)); err != nil {
				return err
			}
			return tx.Supersede(a2, newRecordingVW(nil))
		})
		cs := ledgered(t, v).Chunks(true)
		sort.Slice(cs, func(i, j int) bool { return cs[i].ID < cs[j].ID })
		return cs
	}
	cs := run(false)
	if got, want := ids(cs), sorted(idOf("X"), idOf("Z")); !slices.Equal(got, want) {
		t.Fatalf("visible %v, want X and Z", got)
	}
	for _, c := range cs {
		if len(c.Owners) != 1 || c.Owners[0] != ArchiveOwner("shaA2") {
			t.Fatalf("%s owned by %v, want only A′", c.Content, c.Owners)
		}
	}
	show := func(cs []StoredChunk) string {
		var b strings.Builder
		for _, c := range cs {
			fmt.Fprintf(&b, "%v %v %v %v %s|", c.Chunk, c.Owners, *c.Selected, c.Ownership, c.FiledAt)
		}
		return b.String()
	}
	if rev := run(true); show(rev) != show(cs) {
		t.Fatalf("chunk order changed the result:\n%v\n%v", rev, cs)
	}
}

// The pending fold: S1 live with a; S2 live with b; S3 superseding to c.
// Tracked: S1/a, S2/b′, S3/c, S4/d. Pending is exactly S2/b′, S3/c and S4/d.
func TestPendingFold(t *testing.T) {
	n := 1
	l := foldLedger([]ledgerRecord{
		{Kind: recBaseline},
		{Kind: recSession, SessionID: "S1", State: StateLive, SHA: "a", ChunkCount: &n, Generation: 1},
		{Kind: recSession, SessionID: "S2", State: StateLive, SHA: "b", ChunkCount: &n, Generation: 1},
		{Kind: recSession, SessionID: "S3", State: StateLive, SHA: "c0", ChunkCount: &n, Generation: 1},
		{Kind: recSession, SessionID: "S3", State: StateSuperseding, SHA: "c", SupersedingFrom: "c0", Generation: 1},
	})
	got := l.Pending([]ArchiveRef{{"S1", "a", ""}, {"S2", "b2", ""}, {"S3", "c", ""}, {"S3", "c0", ""}, {"S4", "d", ""}})
	var shas []string
	for _, a := range got {
		shas = append(shas, a.SHA)
	}
	if want := []string{"b2", "c", "d"}; !slices.Equal(shas, want) {
		t.Fatalf("pending %v, want %v", shas, want)
	}
}

// An archive the ledger records as replaced is never pending, though it is
// still a tracked archive: after a completed supersede A -> B, and after
// RecordSuperseded of an older archive met late.
func TestReplacedArchivesAreNeverPending(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		if err := tx.CommitArchive(commitOf("S", "A", "2026-05-13", "x"), newRecordingVW(nil)); err != nil {
			return err
		}
		if err := tx.Supersede(commitOf("S", "B", "2026-05-14", "y"), newRecordingVW(nil)); err != nil {
			return err
		}
		if err := tx.CommitArchive(commitOf("T", "T2", "2026-05-14", "z"), newRecordingVW(nil)); err != nil {
			return err
		}
		return tx.RecordSuperseded("T", "T1", "")
	})
	tracked := []ArchiveRef{{"S", "A", ""}, {"S", "B", ""}, {"T", "T1", ""}, {"T", "T2", ""}}
	if p := ledgered(t, v).Ledger().Pending(tracked); len(p) != 0 {
		t.Fatalf("pending %v; a superseded archive (A, or T1 recorded superseded) must never be pending", p)
	}
}

// Supersede refuses an archive the ledger records as replaced: superseding the
// session back to it would leave it live on an archive CommitArchive then
// refuses forever.
func TestSupersedeRefusesAReplacedArchive(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		if err := tx.CommitArchive(commitOf("S", "B", "2026-05-14", "y"), newRecordingVW(nil)); err != nil {
			return err
		}
		return tx.RecordSuperseded("S", "A", "")
	})
	err := withTx(t, v, func(tx *Tx) error { return tx.Supersede(commitOf("S", "A", "2026-05-13", "x"), newRecordingVW(nil)) })
	if !errors.Is(err, ErrSuperseded) {
		t.Fatalf("Supersede back to a superseded archive: err=%v, want ErrSuperseded", err)
	}
	if sr, _ := ledgered(t, v).Ledger().Session("S"); sr.State != StateLive || sr.SHA != "B" {
		t.Fatalf("session S = %+v, want still live on B", sr)
	}
}

// A chunk discard killed at any point never leaves a ledger that says an
// archive is ingested over a store without its chunks: the ledger goes first,
// so after a kill at each step no session is live and the next commit step
// finds no ledger, which the caller recreates.
func TestDiscardChunksKilledPartWay(t *testing.T) {
	for _, step := range []string{"discard-ledger", "discard-chunks", "discard-kg", "discard-graph"} {
		t.Run(step, func(t *testing.T) {
			v := newVault(t)
			mustTx(t, v, func(tx *Tx) error {
				if _, err := tx.EnsureLedger(nil); err != nil {
					return err
				}
				if err := tx.CommitArchive(commitOf("S", "A", "2026-05-13", "x"), newRecordingVW(nil)); err != nil {
					return err
				}
				return tx.WriteGraph([]byte("g"))
			})
			killAt(t, step)
			if err := withTx(t, v, func(tx *Tx) error { return tx.Discard(DiscardChunks) }); !errors.Is(err, errKill) {
				t.Fatalf("err=%v", err)
			}
			s := ledgered(t, v)
			if isLive(s.Ledger(), "S") {
				t.Fatalf("killed after %s: session S is still live", step)
			}
			if n := len(s.Chunks(true)) + len(s.KG(true)); n != 0 {
				t.Fatalf("killed after %s: %d records visible", step, n)
			}
			if p := s.Ledger().Pending([]ArchiveRef{{"S", "A", ""}}); len(p) != 1 {
				t.Fatalf("killed after %s: A is not pending", step)
			}
			commitStep = func(string) error { return nil }
			err := withTx(t, v, func(tx *Tx) error {
				return tx.CommitArchive(commitOf("S", "A", "2026-05-13", "x"), newRecordingVW(nil))
			})
			if !errors.Is(err, ErrNoLedger) {
				t.Fatalf("killed after %s: the next commit step: err=%v, want ErrNoLedger", step, err)
			}
		})
	}
}

// Supersede against an ingest of the older archive in ANOTHER process. Forced
// order one: the helper commits A (with an extra chunk W) just before this
// process's Supersede takes the lock; the supersede re-reads ownership under
// the lock and A owns nothing afterwards, W included. Forced order two: the
// supersede commits first; the helper's late CommitArchive of A is refused
// with ErrSuperseded and changes nothing.
func TestSupersedeAgainstAnIngestInAnotherProcess(t *testing.T) {
	setup := func(t *testing.T) *storage.Vault {
		v := newVault(t)
		mustTx(t, v, func(tx *Tx) error {
			if _, err := tx.EnsureLedger(nil); err != nil {
				return err
			}
			return tx.CommitArchive(commitOf("S", "A", "2026-05-13", "X", "Y"), newRecordingVW(nil))
		})
		return v
	}
	newer := commitOf("S", "A2", "2026-05-15", "X", "Z")
	noA := func(t *testing.T, v *storage.Vault) {
		t.Helper()
		s := ledgered(t, v)
		for _, c := range s.Chunks(false) {
			for _, o := range c.Owners {
				if o == ArchiveOwner("A") {
					t.Fatalf("chunk %q is still owned by A", c.Content)
				}
			}
		}
		if got := ids(s.Chunks(false)); !slices.Equal(got, sorted(idOf("X"), idOf("Z"))) {
			t.Fatalf("stored %v, want X and Z only (Y and W deleted)", got)
		}
		if sr, _ := s.Ledger().Session("S"); sr.State != StateLive || sr.SHA != "A2" || sr.Generation != 2 {
			t.Fatalf("S = %+v, want live A2 generation 2", sr)
		}
	}

	t.Run("ingest first", func(t *testing.T) {
		v := setup(t)
		h := startHelper(t, v, "commit-A-W")
		h.ready(t)
		// Just before this Supersede waits for the lock, let the helper commit
		// and wait for it to finish, so its write lands first.
		commitWaitHook = func() {
			touch(t, h.file("go"))
			if !waitFile(h.file("done")) {
				t.Errorf("helper never finished; output:\n%s", h.out.String())
			}
		}
		defer func() { commitWaitHook = nil }()
		mustTx(t, v, func(tx *Tx) error { return tx.Supersede(newer, newRecordingVW(nil)) })
		commitWaitHook = nil
		h.wait(t)
		if got, _ := os.ReadFile(h.file("result")); string(got) != "ok" {
			t.Fatalf("the helper's commit of A: %q, want ok", got)
		}
		noA(t, v)
	})

	t.Run("supersede first", func(t *testing.T) {
		v := setup(t)
		mustTx(t, v, func(tx *Tx) error { return tx.Supersede(newer, newRecordingVW(nil)) })
		h := startHelper(t, v, "commit-A-W")
		h.ready(t)
		touch(t, h.file("go"))
		h.wait(t)
		if got, _ := os.ReadFile(h.file("result")); string(got) != "superseded" {
			t.Fatalf("the helper's late commit of A: %q, want superseded", got)
		}
		noA(t, v)
	})
}

// A failure record is never an ingest: a session with only failure records is
// not ledgered, its archive is pending, it has no start day, and the chunks it
// wrote before failing do not load.
func TestAFailureRecordIsNeverAnIngest(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error { _, err := tx.EnsureLedger(nil); return err })
	killAt(t, "kg")
	_ = withTx(t, v, func(tx *Tx) error {
		return tx.CommitArchive(commitOf("S5", "e", "2026-05-13", "x"), newRecordingVW(nil))
	})
	commitStep = func(string) error { return nil }
	for i := 0; i < 2; i++ {
		mustTx(t, v, func(tx *Tx) error { return tx.RecordFailure("S5", "e", errors.New("boom")) })
	}
	l := ledgered(t, v).Ledger()
	if isLive(l, "S5") {
		t.Fatal("a session with only failure records is ledgered")
	}
	if p := l.Pending([]ArchiveRef{{SessionID: "S5", SHA: "e"}}); len(p) != 1 {
		t.Fatal("the failed archive is not pending")
	}
	if _, ok := l.StartDay("S5"); ok {
		t.Fatal("a failed session has a start day")
	}
	if n := len(ledgered(t, v).Chunks(true)); n != 0 {
		t.Fatalf("%d chunks of a failed archive load", n)
	}
}

// Failure counts: two failures of a count 2; a later successful commit of a
// leaves the count at 2. b fails in a rebuild run whose proof lists it:
// ClearFailures resets a to 0 and keeps b at 1. An invalid proof is refused.
func TestFailureCount(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		if err := tx.RecordFailure("Sa", "a", errors.New("x")); err != nil {
			return err
		}
		return tx.RecordFailure("Sa", "a", errors.New("y"))
	})
	if n := ledgered(t, v).Ledger().FailureCount("a"); n != 2 {
		t.Fatalf("count %d, want 2", n)
	}
	mustTx(t, v, func(tx *Tx) error {
		return tx.CommitArchive(commitOf("Sa", "a", "2026-05-13", "x"), newRecordingVW(nil))
	})
	if l := ledgered(t, v).Ledger(); !isLive(l, "Sa") || l.FailureCount("a") != 2 {
		t.Fatalf("after a successful commit: ledgered=%v count=%d, want true and 2", isLive(l, "Sa"), l.FailureCount("a"))
	}

	rl := mustTryRunLock(t)(TryRunLock(v, KindRebuild, "alpha"))
	mustTx(t, v, func(tx *Tx) error { return tx.RecordFailure("Sb", "b", errors.New("z")) })
	proof, err := rl.CompletedRebuild("alpha", []string{"b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []RebuildProof{{}, {vaultRoot: v.Root, project: "beta", ok: true}} {
		if err := withTx(t, v, func(tx *Tx) error { return tx.ClearFailures(bad) }); err == nil {
			t.Fatalf("ClearFailures accepted an invalid proof %+v", bad)
		}
	}
	mustTx(t, v, func(tx *Tx) error { return tx.ClearFailures(proof) })
	_ = rl.Release()
	if l := ledgered(t, v).Ledger(); l.FailureCount("a") != 0 || l.FailureCount("b") != 1 {
		t.Fatalf("after ClearFailures: a=%d b=%d, want 0 and 1", l.FailureCount("a"), l.FailureCount("b"))
	}

	ing := mustTryRunLock(t)(TryRunLock(v, KindIngest, "alpha"))
	if _, err := ing.CompletedRebuild("alpha", nil); err == nil {
		t.Fatal("an ingest run produced a rebuild proof")
	}
	_ = ing.Release()
	if _, err := rl.CompletedRebuild("alpha", nil); err == nil {
		t.Fatal("a released run lock produced a rebuild proof")
	}
}

// The baseline set: created from every archive present minus the creating
// trigger's; additions add; a valid rebuild proof empties it and an invalid one
// is refused; a chunk discard recreates it from the archives then present; and
// an addition on a project with no ledger creates no ledger.
func TestBaselineSet(t *testing.T) {
	v := newVault(t)
	fakeArchives(t, "a", "b", "c")
	created := false
	mustTx(t, v, func(tx *Tx) error { var err error; created, err = tx.EnsureLedger([]string{"c"}); return err })
	l := ledgered(t, v).Ledger()
	if !created || !slices.Equal(baselineOf(l), []string{"a", "b"}) || !l.InBaseline("a") || l.InBaseline("c") {
		t.Fatalf("created=%v baseline=%v, want a and b", created, baselineOf(l))
	}
	fakeArchives(t, "a", "b", "c", "d")
	mustTx(t, v, func(tx *Tx) error { var err error; created, err = tx.EnsureLedger(nil); return err })
	if created || ledgered(t, v).Ledger().InBaseline("d") {
		t.Fatal("a second EnsureLedger recomputed the set: d, which arrived later, is in the backlog")
	}
	mustTx(t, v, func(tx *Tx) error { return tx.AddToBaseline([]string{"e"}) })
	if !ledgered(t, v).Ledger().InBaseline("e") {
		t.Fatal("AddToBaseline did not add e")
	}
	if err := withTx(t, v, func(tx *Tx) error { return tx.ClearBaseline(RebuildProof{}) }); err == nil {
		t.Fatal("ClearBaseline accepted the zero proof")
	}
	rl := mustTryRunLock(t)(TryRunLock(v, KindRebuild, "alpha"))
	proof, _ := rl.CompletedRebuild("alpha", nil)
	mustTx(t, v, func(tx *Tx) error { return tx.ClearBaseline(proof) })
	_ = rl.Release()
	if b := baselineOf(ledgered(t, v).Ledger()); len(b) != 0 {
		t.Fatalf("baseline after a rebuild %v, want empty", b)
	}
	mustTx(t, v, func(tx *Tx) error { return tx.Discard(DiscardChunks) })
	if b := baselineOf(ledgered(t, v).Ledger()); !slices.Equal(b, []string{"a", "b", "c", "d"}) {
		t.Fatalf("baseline after a discard %v, want every archive now present", b)
	}

	if err := os.MkdirAll(v.Root+"/Projects/beta", 0o755); err != nil {
		t.Fatal(err)
	}
	tx := mustLock(t)(Lock(context.Background(), v, "beta", NoTimeout))
	if err := tx.AddToBaseline([]string{"f"}); err != nil {
		t.Fatal(err)
	}
	_ = tx.Release()
	pf, _ := filesFor(v, "beta")
	if _, err := os.Stat(pf.ledger); !os.IsNotExist(err) {
		t.Fatalf("AddToBaseline created a ledger for a project that had none: %v", err)
	}
}

// The chunk count is the number of distinct chunk ids: 5 chunks, two equal,
// give 4, with no shortfall; deleting 2 of the archive's chunk records by hand
// makes CountChunks report 2.
func TestChunkCountIsDistinctIDs(t *testing.T) {
	v := newVault(t)
	c := commitOf("S", "sha", "2026-05-13", "p", "q", "r", "s", "p")
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(c, newRecordingVW(nil))
	})
	s := ledgered(t, v)
	sr, _ := s.Ledger().Session("S")
	if sr.ChunkCount != 4 || s.CountChunks(ArchiveOwner("sha")) != 4 {
		t.Fatalf("chunk_count %d, CountChunks %d, want 4 and 4", sr.ChunkCount, s.CountChunks(ArchiveOwner("sha")))
	}
	pf, _ := filesFor(v, "alpha")
	var keep []string
	for _, l := range fileLines(t, pf.chunks) {
		if strings.Contains(l, idOf("p")) || strings.Contains(l, idOf("q")) {
			continue
		}
		keep = append(keep, l)
	}
	if err := os.WriteFile(pf.chunks, []byte(strings.Join(keep, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := ledgered(t, v).CountChunks(ArchiveOwner("sha")); n != 2 {
		t.Fatalf("CountChunks after deleting 2 records = %d, want 2", n)
	}
}

// Start days: the session record carries the day and its source, a supersede
// moves StartDay to the newer archive's day, an unledgered session or batch has
// none, a batch's day comes from its batch record, and a batch whose id names a
// ledgered session is refused.
func TestStartDay(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(commitOf("S", "a", "2026-05-13", "x"), newRecordingVW(nil))
	})
	if d, ok := ledgered(t, v).Ledger().StartDay("S"); !ok || d != "2026-05-13" {
		t.Fatalf("StartDay(S) = %q, %v", d, ok)
	}
	c2 := commitOf("S", "a2", "2026-05-14", "x")
	c2.StartDaySource = DayFromCapturedAt
	mustTx(t, v, func(tx *Tx) error { return tx.Supersede(c2, newRecordingVW(nil)) })
	l := ledgered(t, v).Ledger()
	if d, ok := l.StartDay("S"); !ok || d != "2026-05-14" {
		t.Fatalf("StartDay(S) after the supersede = %q, %v, want 2026-05-14", d, ok)
	}
	if sr, _ := l.Session("S"); sr.StartDaySource != DayFromCapturedAt {
		t.Fatalf("start day source %q", sr.StartDaySource)
	}
	if _, ok := l.StartDay("never"); ok {
		t.Fatal("an unledgered session has a start day")
	}
	mustTx(t, v, func(tx *Tx) error {
		return tx.CommitBatch(BatchCommit{BatchID: "batch:m", StartDay: "2026-04-02", Chunks: []OwnedChunk{ownedChunk("b", "alpha", "general")}}, nil)
	})
	if d, ok := ledgered(t, v).Ledger().StartDay("batch:m"); !ok || d != "2026-04-02" {
		t.Fatalf("StartDay(batch:m) = %q, %v", d, ok)
	}
	if _, ok := ledgered(t, v).Ledger().StartDay("batch:other"); ok {
		t.Fatal("an unledgered batch has a start day")
	}
	err := withTx(t, v, func(tx *Tx) error {
		return tx.CommitBatch(BatchCommit{BatchID: "S", StartDay: "2026-04-02"}, nil)
	})
	if !errors.Is(err, ErrBatchIsSession) {
		t.Fatalf("a batch named after a ledgered session: err=%v", err)
	}
	for _, bad := range []ArchiveCommit{
		func() ArchiveCommit { c := commitOf("T", "t", "13-05-2026", "x"); return c }(),
		func() ArchiveCommit { c := commitOf("T", "t", "2026-05-13", "x"); c.StartDaySource = ""; return c }(),
	} {
		if err := withTx(t, v, func(tx *Tx) error { return tx.CommitArchive(bad, nil) }); err == nil {
			t.Fatalf("a commit with day %q from %q was accepted", bad.StartDay, bad.StartDaySource)
		}
	}
}

// Shared-record dating: a chunk owned by several sources takes its owner fields
// from the live owner with the earliest day, the same in either ingest order;
// equal days go to the smaller key; superseding the earliest owner away moves
// the date; a batch's day comes from the ledger, not its owner line.
func TestSharedRecordDating(t *testing.T) {
	type step func(tx *Tx) error
	a := func(tx *Tx) error {
		return tx.CommitArchive(commitOf("A", "shaA", "2026-05-14", "X"), newRecordingVW(nil))
	}
	b := func(tx *Tx) error {
		return tx.CommitArchive(commitOf("B", "shaB", "2026-05-13", "X"), newRecordingVW(nil))
	}
	x := func(steps ...step) StoredChunk {
		t.Helper()
		v := newVault(t)
		mustTx(t, v, func(tx *Tx) error {
			if _, err := tx.EnsureLedger(nil); err != nil {
				return err
			}
			for _, s := range steps {
				if err := s(tx); err != nil {
					return err
				}
			}
			return nil
		})
		for _, c := range ledgered(t, v).Chunks(true) {
			if c.Content == "X" {
				return c
			}
		}
		t.Fatal("chunk X is not visible")
		return StoredChunk{}
	}
	ab, ba := x(a, b), x(b, a)
	if ab.FiledAt != "2026-05-13" || ab.SourceRef != "ref-shaB" || fmt.Sprint(ab.Ownership, ab.FiledAt) != fmt.Sprint(ba.Ownership, ba.FiledAt) {
		t.Fatalf("A then B: %+v; B then A: %+v; want both dated 2026-05-13 from B", ab, ba)
	}

	sameDayA := func(tx *Tx) error {
		return tx.CommitArchive(commitOf("A", "shaA", "2026-05-13", "X"), newRecordingVW(nil))
	}
	if got := x(sameDayA, b); got.SourceRef != "ref-shaA" {
		t.Fatalf("equal days: source_ref %q, want the smaller sha's (shaA)", got.SourceRef)
	}

	supersedeB := func(tx *Tx) error {
		return tx.Supersede(commitOf("B", "shaB2", "2026-05-13", "other"), newRecordingVW(nil))
	}
	if got := x(a, b, supersedeB); got.FiledAt != "2026-05-14" || got.SourceRef != "ref-shaA" {
		t.Fatalf("after B is superseded away: %+v, want A's day and source_ref", got)
	}

	batch := func(tx *Tx) error {
		oc := ownedChunk("X", "alpha", "general")
		oc.SourceRef, oc.Day = "ref-m", "1999-01-01" // a day on a batch owner line is ignored
		return tx.CommitBatch(BatchCommit{BatchID: "batch:m", StartDay: "2026-04-02", Chunks: []OwnedChunk{oc}}, nil)
	}
	for _, order := range [][]step{{a, b, batch}, {batch, b, a}} {
		if got := x(order...); got.FiledAt != "2026-04-02" || got.SourceRef != "ref-m" {
			t.Fatalf("with batch m: %+v, want the ledger's 2026-04-02 and m's source_ref", got)
		}
	}
}

// ReplaceOwned: n1 and n2 own D, n1 also owns E. ReplaceOwned(n1,{D}) deletes
// E and keeps D; emptying both deletes D. Each call that removed an owner
// changes the epoch, one that only added does not; re-dating n2's line
// rewrites it and changes the epoch.
func TestReplaceOwned(t *testing.T) {
	v := newVault(t)
	D := withDay([]OwnedChunk{ownedChunk("D", "alpha", "decisions")}, "2026-05-20")
	E := withDay([]OwnedChunk{ownedChunk("E", "alpha", "decisions")}, "2026-05-20")
	n1, n2 := NoteOwner("notes/1.md"), NoteOwner("notes/2.md")
	call := func(o Owner, recs []OwnedChunk) (epochChanged bool) {
		t.Helper()
		before := readGen(t, v, "alpha")
		mustTx(t, v, func(tx *Tx) error { return tx.ReplaceOwned(o, recs) })
		after := readGen(t, v, "alpha")
		changed := after.Epoch != before.Epoch
		if want := before.Gen + genStep(changed); after.Gen != want {
			t.Fatalf("gen %d -> %d, want %d", before.Gen, after.Gen, want)
		}
		return changed
	}
	mustTx(t, v, func(tx *Tx) error { return nil })
	if call(n1, append(slices.Clone(D), E...)) || call(n2, D) {
		t.Fatal("a ReplaceOwned that only added ownership changed the epoch")
	}
	if !call(n1, D) {
		t.Fatal("removing n1 from E did not change the epoch")
	}
	s := ledgered(t, v)
	if got := ids(s.Chunks(false)); !slices.Equal(got, []string{idOf("D")}) {
		t.Fatalf("after ReplaceOwned(n1,{D}): %v, want only D", got)
	}
	if cs := s.Chunks(false); len(cs[0].Owners) != 2 {
		t.Fatalf("D owners %v, want n1 and n2", cs[0].Owners)
	}

	if !call(n2, withDay(D, "2026-05-13")) {
		t.Fatal("re-dating n2's owner line did not change the epoch")
	}
	if cs := ledgered(t, v).Chunks(true); cs[0].FiledAt != "2026-05-13" {
		t.Fatalf("D after the re-date: %+v, want 2026-05-13", cs[0])
	}
	call(n2, nil)
	call(n1, nil)
	if got := ledgered(t, v).Chunks(false); len(got) != 0 {
		t.Fatalf("after both notes let go, %d chunks remain", len(got))
	}
}

// The store counter across every kind of write: an append moves gen only;
// a failed write moves nothing; a relabel, a supersede, a torn-line cut and
// both discards move gen and the epoch; the counter outlives a discard.
func TestStoreCounterAcrossWrites(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(commitOf("S", "a", "2026-05-13", "x"), newRecordingVW(nil))
	})
	step := func(name string, wantEpoch bool, f func(tx *Tx) error) {
		t.Helper()
		before := readGen(t, v, "alpha")
		mustTx(t, v, f)
		after := readGen(t, v, "alpha")
		if after.Gen != before.Gen+genStep(wantEpoch) || (after.Epoch != before.Epoch) != wantEpoch {
			t.Fatalf("%s: %+v -> %+v, want gen+%d and epoch changed=%v", name, before, after, genStep(wantEpoch), wantEpoch)
		}
	}
	step("append", false, func(tx *Tx) error {
		return tx.Append(NoteOwner("n"), withDay([]OwnedChunk{ownedChunk("n1", "alpha", "d")}, "2026-05-13"))
	})
	step("relabel", true, func(tx *Tx) error { return tx.Rewrite(map[string]Labels{idOf("x"): {Room: "moved"}}) })
	step("supersede", true, func(tx *Tx) error { return tx.Supersede(commitOf("S", "a2", "2026-05-13", "y"), newRecordingVW(nil)) })
	pf, _ := filesFor(v, "alpha")
	f, err := os.OpenFile(pf.chunks, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"kind":"chunk","id":"tor`)
	_ = f.Close()
	step("torn-line cut", true, func(tx *Tx) error {
		return tx.Append(NoteOwner("n"), withDay([]OwnedChunk{ownedChunk("n2", "alpha", "d")}, "2026-05-13"))
	})
	cacheDir, _ := v.EmbedCacheDir("alpha")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, cacheDir+"/"+idOf("x")+".vec")
	step("vector discard", true, func(tx *Tx) error { return tx.Discard(DiscardVectors) })
	step("chunk discard", true, func(tx *Tx) error { return tx.Discard(DiscardChunks) })
	if _, err := os.Stat(pf.chunks); !os.IsNotExist(err) {
		t.Fatal("chunks survived the discard")
	}
	if g := readGen(t, v, "alpha"); g.Gen < 7 {
		t.Fatalf("the counter after the discard is %+v; it must keep counting", g)
	}

	// A failed append moves nothing: no record was written.
	before := readGen(t, v, "alpha")
	oldSync := fsyncFile
	fsyncFile = func(f *os.File) error {
		if f.Name() == pf.chunks {
			return errors.New("disk full")
		}
		return oldSync(f)
	}
	err = withTx(t, v, func(tx *Tx) error {
		return tx.Append(NoteOwner("n"), withDay([]OwnedChunk{ownedChunk("n3", "alpha", "d")}, "2026-05-13"))
	})
	fsyncFile = oldSync
	if err == nil {
		t.Fatal("the injected append error was not returned")
	}
	if g := readGen(t, v, "alpha"); g != before {
		t.Fatalf("a failed append moved the counter %+v -> %+v", before, g)
	}

	// A failed destructive write has already moved the counter, with a new
	// epoch, before its first step: every other process must reload.
	before = readGen(t, v, "alpha")
	old := writeFileFn
	writeFileFn = func(p string, d []byte) error {
		if p == pf.chunks {
			return errors.New("disk full")
		}
		return old(p, d)
	}
	err = withTx(t, v, func(tx *Tx) error { return tx.Rewrite(map[string]Labels{"nothing": {Room: "r"}}) })
	writeFileFn = old
	if err == nil {
		t.Fatal("the injected write error was not returned")
	}
	if g := readGen(t, v, "alpha"); g.Gen <= before.Gen || g.Epoch == before.Epoch {
		t.Fatalf("a failed destructive write left the counter %+v -> %+v; it must have moved, with a new epoch", before, g)
	}
}

// Graph writes: WriteGraph replaces hnsw.idx and moves gen only, leaving the
// chunks, ledger and KG byte-identical; a failed write leaves the old graph;
// DeleteGraph removes it and moves gen only.
func TestGraphWrites(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(commitOf("S", "a", "2026-05-13", "x"), newRecordingVW(nil))
	})
	pf, _ := filesFor(v, "alpha")
	snap := func() string {
		var b strings.Builder
		for _, p := range []string{pf.chunks, pf.ledger, pf.kg} {
			d, _ := os.ReadFile(p)
			b.Write(d)
		}
		return b.String()
	}
	files := snap()
	for i, op := range []func(tx *Tx) error{
		func(tx *Tx) error { return tx.WriteGraph([]byte("graph-1")) },
		func(tx *Tx) error { return tx.DeleteGraph() },
	} {
		before := readGen(t, v, "alpha")
		mustTx(t, v, op)
		after := readGen(t, v, "alpha")
		if after.Gen != before.Gen+1 || after.Epoch != before.Epoch {
			t.Fatalf("graph op %d: %+v -> %+v, want gen+1, same epoch", i, before, after)
		}
		if snap() != files {
			t.Fatalf("graph op %d changed chunks, ledger or KG", i)
		}
		if i == 0 {
			if d, _ := os.ReadFile(pf.graph); string(d) != "graph-1" {
				t.Fatalf("hnsw.idx = %q", d)
			}
			old := writeFileFn
			writeFileFn = func(string, []byte) error { return errors.New("killed before the rename") }
			_ = withTx(t, v, func(tx *Tx) error { return tx.WriteGraph([]byte("graph-2")) })
			writeFileFn = old
			if d, _ := os.ReadFile(pf.graph); string(d) != "graph-1" {
				t.Fatalf("a failed graph write left %q", d)
			}
		}
	}
	if _, err := os.Stat(pf.graph); !os.IsNotExist(err) {
		t.Fatal("DeleteGraph left hnsw.idx")
	}
}

// Discard scope: DiscardChunks removes chunks, ledger, KG and the graph,
// recreates the baseline set, and leaves vectors and lock files; DiscardVectors
// removes this project's vectors only, keeping the embed cache's sidecar.
func TestDiscardScope(t *testing.T) {
	v := newVault(t)
	fakeArchives(t, "a")
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		if err := tx.CommitArchive(commitOf("S", "a", "2026-05-13", "x"), newRecordingVW(nil)); err != nil {
			return err
		}
		return tx.WriteGraph([]byte("g"))
	})
	cache := func(p string) string { d, _ := v.EmbedCacheDir(p); return d }
	for _, p := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(cache(p), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"v1.vec", storage.EmbedCacheFingerprintFile, "keep.txt"} {
			touch(t, cache(p)+"/"+f)
		}
	}
	pf, _ := filesFor(v, "alpha")
	mustTx(t, v, func(tx *Tx) error { return tx.Discard(DiscardChunks) })
	for _, p := range []string{pf.chunks, pf.kg, pf.graph} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s survived DiscardChunks", p)
		}
	}
	if b := baselineOf(ledgered(t, v).Ledger()); !slices.Equal(b, []string{"a"}) {
		t.Fatalf("baseline after DiscardChunks %v", b)
	}
	if _, err := os.Stat(cache("alpha") + "/v1.vec"); err != nil {
		t.Fatal("DiscardChunks removed a vector")
	}
	lock, _ := v.IndexCommitLockPath("alpha")
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("DiscardChunks removed a lock file")
	}

	ledgerBefore, _ := os.ReadFile(pf.ledger)
	mustTx(t, v, func(tx *Tx) error { return tx.Discard(DiscardVectors) })
	if _, err := os.Stat(cache("alpha") + "/v1.vec"); !os.IsNotExist(err) {
		t.Fatal("v1.vec survived DiscardVectors")
	}
	for _, f := range []string{"v1.vec", storage.EmbedCacheFingerprintFile} {
		if _, err := os.Stat(cache("beta") + "/" + f); err != nil {
			t.Fatalf("DiscardVectors on alpha removed beta's %s", f)
		}
	}
	// The sidecar stays: a running embed cache that already checked it would
	// not write it again, and a fresh process would then wipe every new vector.
	for _, f := range []string{storage.EmbedCacheFingerprintFile, "keep.txt"} {
		if _, err := os.Stat(cache("alpha") + "/" + f); err != nil {
			t.Fatalf("DiscardVectors removed alpha's %s; only vectors go", f)
		}
	}
	if after, _ := os.ReadFile(pf.ledger); string(after) != string(ledgerBefore) {
		t.Fatal("DiscardVectors changed the ledger")
	}
}

// A torn final line: readers return every complete record and an offset that
// stops before the tail; the next append first cuts the tail off, so the file
// holds only whole records with the new one last.
func TestTornFinalLine(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(commitOf("S", "a", "2026-05-13", "x"), newRecordingVW(nil))
	})
	pf, _ := filesFor(v, "alpha")
	whole, _ := os.ReadFile(pf.chunks)
	for _, p := range []string{pf.chunks, pf.ledger} {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(`{"kind":"session","session_id":"T","st`)
		_ = f.Close()
	}
	s := ledgered(t, v)
	if len(s.Chunks(true)) != 1 || !isLive(s.Ledger(), "S") {
		t.Fatal("a torn tail hid the complete records")
	}
	if s.ChunksOffset != int64(len(whole)) {
		t.Fatalf("offset %d, want %d (just before the torn line)", s.ChunksOffset, len(whole))
	}
	mustTx(t, v, func(tx *Tx) error {
		return tx.CommitArchive(commitOf("S2", "b", "2026-05-13", "y"), newRecordingVW(nil))
	})
	for _, p := range []string{pf.chunks, pf.ledger} {
		for i, l := range fileLines(t, p) {
			if strings.Contains(l, `"st`) && !strings.HasSuffix(l, "}") {
				t.Fatalf("%s line %d is still torn: %s", p, i, l)
			}
		}
	}
	if l := ledgered(t, v).Ledger(); !isLive(l, "S2") || !isLive(l, "S") {
		t.Fatal("the append after the cut lost a record")
	}
}

// Appending 10,000 chunks one commit at a time in one process reads
// chunks.jsonl at most once: the id set is kept, not rescanned.
func TestAppendDoesNotRescan(t *testing.T) {
	if testing.Short() {
		t.Skip("10,000 commits")
	}
	v := newVault(t)
	pf, _ := filesFor(v, "alpha")
	var mu sync.Mutex
	reads := 0
	old := readFileFn
	readFileFn = func(p string) ([]byte, error) {
		if p == pf.chunks {
			mu.Lock()
			reads++
			mu.Unlock()
		}
		return old(p)
	}
	defer func() { readFileFn = old }()
	for i := 0; i < 10000; i++ {
		mustTx(t, v, func(tx *Tx) error {
			return tx.Append(NoteOwner("n"), withDay([]OwnedChunk{ownedChunk(fmt.Sprintf("c%d", i), "alpha", "d")}, "2026-05-13"))
		})
	}
	if reads > 1 {
		t.Fatalf("chunks.jsonl was read %d times for 10,000 appends; want at most once", reads)
	}
}

// Two processes append the same 1,000 ids through Lock, Append and Commit:
// the file folds to exactly 1,000 records, every line parses, and the owner
// line of each id is there once.
func TestTwoProcessAppendSameIDs(t *testing.T) {
	twoProcessAppend(t, "append-same", "", "", 1000, 2000)
}

// Two processes append 1,000 distinct ids each: 2,000 records, none torn.
func TestTwoProcessAppendDistinctIDs(t *testing.T) {
	twoProcessAppend(t, "append-distinct", "VP_INDEXSTORE_PREFIX=one ", "VP_INDEXSTORE_PREFIX=two ", 2000, 4000)
}

func twoProcessAppend(t *testing.T, mode, env1, env2 string, wantChunks, wantLines int) {
	v := newVault(t)
	var h1, h2 *helper
	if env1 == "" {
		h1, h2 = startHelper(t, v, mode), startHelper(t, v, mode)
	} else {
		h1, h2 = startHelper(t, v, mode, env1), startHelper(t, v, mode, env2)
	}
	h1.ready(t)
	h2.ready(t)
	touch(t, h1.file("go"))
	touch(t, h2.file("go"))
	h1.wait(t)
	h2.wait(t)
	pf, _ := filesFor(v, "alpha")
	lines, _, torn, err := readLines(pf.chunks)
	if err != nil || torn {
		t.Fatalf("torn=%v err=%v", torn, err)
	}
	if decoded := decodeLines(pf.chunks, lines, validChunkLine); len(decoded) != len(lines) || len(lines) != wantLines {
		t.Fatalf("%d lines, %d parse; want %d", len(lines), len(decoded), wantLines)
	}
	if n := len(ledgered(t, v).Chunks(false)); n != wantChunks {
		t.Fatalf("%d chunks, want %d", n, wantChunks)
	}
}

// Every store write lands under palace/.local/, which the canonical ignore
// file covers: after locks, a holder record, the counter, chunks, ledger, KG
// and a graph exist, `git status --porcelain -uall` of the vault is empty.
func TestStoreWritesLeaveTheTreeClean(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	v := newVault(t)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(gitBin, append([]string{"-C", v.Root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	if err := os.WriteFile(v.Root+"/.gitignore", []byte(strings.Join(storage.CanonicalGitignorePatterns, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".gitignore")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "ignore")

	rl := mustTryRunLock(t)(TryRunLock(v, KindIngest, "alpha"))
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		if err := tx.CommitArchive(commitOf("S", "a", "2026-05-13", "x"), newRecordingVW(nil)); err != nil {
			return err
		}
		return tx.WriteGraph([]byte("g"))
	})
	if _, err := os.Stat(v.IndexRunHolderPath()); err != nil {
		t.Fatal("no holder record to check")
	}
	if out := git("status", "--porcelain", "-uall"); out != "" {
		t.Fatalf("store writes left the vault tree dirty:\n%s", out)
	}
	_ = rl.Release()
}

// A chunk whose id is not index.ChunkID of its content is refused, as is a
// vector for a chunk the commit does not carry (the .vec rule); nothing is
// written by either.
func TestCommitRefusesForeignIDsAndVectors(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error { _, err := tx.EnsureLedger(nil); return err })
	bad := commitOf("S", "a", "2026-05-13", "x")
	bad.Chunks[0].ID = storage.DrawerID("alpha", "x")
	bad.Vectors = nil
	stray := commitOf("S", "a", "2026-05-13", "x")
	stray.Vectors["not-a-chunk"] = []float32{1}
	for name, c := range map[string]ArchiveCommit{"foreign id": bad, "stray vector": stray} {
		vw := newRecordingVW(nil)
		if err := withTx(t, v, func(tx *Tx) error { return tx.CommitArchive(c, vw) }); err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if len(vw.puts) != 0 || len(ledgered(t, v).Chunks(false)) != 0 {
			t.Fatalf("%s: something was written", name)
		}
	}
}

// A process's cached id set is reloaded when another process wrote the store:
// after a helper appends chunk c2, this process appending c2 again writes no
// second chunk line for it.
func TestCachedStateReloadsAfterAnotherProcessWrites(t *testing.T) {
	v := newVault(t)
	app := func(content string) {
		t.Helper()
		mustTx(t, v, func(tx *Tx) error {
			return tx.Append(NoteOwner("n"), withDay([]OwnedChunk{ownedChunk(content, "alpha", "d")}, "2026-05-13"))
		})
	}
	app("c1") // this process now caches the id set
	h := startHelper(t, v, "append-one", "VP_INDEXSTORE_CONTENT=c2")
	h.wait(t)
	app("c2")
	pf, _ := filesFor(v, "alpha")
	if n := len(fileLines(t, pf.chunks)); n != 4 {
		t.Fatalf("chunks.jsonl has %d lines, want 4: this process appended c2 again from a stale id set", n)
	}
}

// A write that lands and then reports an error (a directory fsync failing
// after the rename) leaves the cached state behind the files, so the cache is
// dropped: chunk E, deleted by such a write, is stored again by a later
// Append instead of being skipped as already present.
func TestCacheIsDroppedAfterAFailedWrite(t *testing.T) {
	v := newVault(t)
	n1 := NoteOwner("n1")
	E := withDay([]OwnedChunk{ownedChunk("E", "alpha", "d")}, "2026-05-13")
	D := withDay([]OwnedChunk{ownedChunk("D", "alpha", "d")}, "2026-05-13")
	mustTx(t, v, func(tx *Tx) error { return tx.Append(n1, append(slices.Clone(D), E...)) })
	old := writeFileFn
	writeFileFn = func(p string, d []byte) error {
		if err := old(p, d); err != nil {
			return err
		}
		if strings.HasSuffix(p, chunksFile) {
			return errors.New("sync dir: input/output error")
		}
		return nil
	}
	err := withTx(t, v, func(tx *Tx) error { return tx.ReplaceOwned(n1, D) })
	writeFileFn = old
	if err == nil {
		t.Fatal("the injected error was not returned")
	}
	mustTx(t, v, func(tx *Tx) error { return tx.Append(n1, E) })
	if got := ids(ledgered(t, v).Chunks(false)); !slices.Equal(got, sorted(idOf("D"), idOf("E"))) {
		t.Fatalf("stored %v, want D and E: the cache kept E after the write that deleted it failed", got)
	}
}

// Every append is fsynced before it returns: chunks, KG and ledger.
func TestAppendsAreFsynced(t *testing.T) {
	v := newVault(t)
	var synced []string
	old := fsyncFile
	fsyncFile = func(f *os.File) error { synced = append(synced, filepath.Base(f.Name())); return old(f) }
	defer func() { fsyncFile = old }()
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(commitOf("S", "a", "2026-05-13", "x"), newRecordingVW(nil))
	})
	for _, f := range []string{ledgerFile, chunksFile, kgFile} {
		if !slices.Contains(synced, f) {
			t.Fatalf("appends synced %v; %s was not", synced, f)
		}
	}
}

// The crashed-discard probe: another process dies in the middle of a chunk
// discard, after removing the ledger and chunks. This process cached the
// store's state before that. Because the discard bumped the counter before
// its first step, this process reloads: CommitArchive finds no ledger, writes
// nothing, and leaves no owner line without its chunk line.
func TestACrashedDiscardInAnotherProcessInvalidatesTheCache(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(commitOf("S", "A", "2026-05-13", "x", "y"), newRecordingVW(nil))
	})
	h := startHelper(t, v, "discard-crash")
	h.wait(t)
	pf, _ := filesFor(v, "alpha")
	for _, p := range []string{pf.ledger, pf.chunks} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("fixture: the crashed discard left %s", p)
		}
	}
	err := withTx(t, v, func(tx *Tx) error {
		return tx.CommitArchive(commitOf("S2", "B", "2026-05-14", "x", "z"), newRecordingVW(nil))
	})
	if !errors.Is(err, ErrNoLedger) {
		t.Fatalf("CommitArchive after another process's crashed discard: err=%v, want ErrNoLedger", err)
	}
	if _, err := os.Stat(pf.chunks); !os.IsNotExist(err) {
		lines := fileLines(t, pf.chunks)
		t.Fatalf("CommitArchive wrote chunks.jsonl from a stale cache:\n%s", strings.Join(lines, "\n"))
	}
}

// A ledger is never created by anything but its baseline set: with the ledger
// removed behind this process's back (no counter bump at all), CommitArchive
// is refused with ErrNoLedger and no ledger file begins with a session record.
func TestALedgerIsNeverBegunByASessionRecord(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(commitOf("S", "A", "2026-05-13", "x"), newRecordingVW(nil))
	})
	pf, _ := filesFor(v, "alpha")
	if err := os.Remove(pf.ledger); err != nil {
		t.Fatal(err)
	}
	err := withTx(t, v, func(tx *Tx) error {
		return tx.CommitArchive(commitOf("S2", "B", "2026-05-14", "z"), newRecordingVW(nil))
	})
	if !errors.Is(err, ErrNoLedger) {
		t.Fatalf("err=%v, want ErrNoLedger", err)
	}
	if data, err := os.ReadFile(pf.ledger); err == nil {
		t.Fatalf("a ledger was begun without its baseline:\n%s", data)
	}
}

// ReplaceOwned's append branch appends against the fold it decided from, not
// a cached id set: with chunks.jsonl emptied behind this process's back, a
// ReplaceOwned that only adds still writes D's chunk line.
func TestReplaceOwnedAppendsAgainstTheFileItRead(t *testing.T) {
	v := newVault(t)
	n1 := NoteOwner("n1")
	D := withDay([]OwnedChunk{ownedChunk("D", "alpha", "d")}, "2026-05-13")
	mustTx(t, v, func(tx *Tx) error { return tx.Append(n1, D) })
	pf, _ := filesFor(v, "alpha")
	if err := os.WriteFile(pf.chunks, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mustTx(t, v, func(tx *Tx) error { return tx.ReplaceOwned(n1, D) })
	if got := ids(ledgered(t, v).Chunks(false)); !slices.Equal(got, []string{idOf("D")}) {
		t.Fatalf("stored %v, want D", got)
	}
}
