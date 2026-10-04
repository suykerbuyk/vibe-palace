// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"encoding/json"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// chunks.jsonl holds two kinds of line (ADR-014 decision 7, "Re-archived
// sessions: supersede"):
//   - a chunk line, written once per id: the content-derived fields;
//   - an owner line, one per (id, owner): what that owner says about it.
//
// Appends never rewrite a line. Removing ownership, deleting a chunk, rewriting
// an owner line and relabelling all rewrite the whole file atomically.
const (
	lineChunk = "chunk"
	lineOwner = "owner"
)

type chunkLine struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Content string `json:"content,omitempty"`
	Wing    string `json:"wing,omitempty"`
	Room    string `json:"room,omitempty"`
	Hall    string `json:"hall,omitempty"`
	Owner   *Owner `json:"owner,omitempty"`
	Ownership
}

func validChunkLine(l *chunkLine) bool {
	switch l.Kind {
	case lineChunk:
		return l.ID != ""
	case lineOwner:
		return l.ID != "" && l.Owner != nil && l.Owner.validate() == nil
	}
	return false
}

// ownerLine is one owner of one chunk, and what it says.
type ownerLine struct {
	Owner Owner
	Ownership
}

// chunkFold is chunks.jsonl folded by id: owners by union.
//
// It keeps the FIRST chunk line per id. That is deterministic because every
// field of a chunk line is derived from the content, and the id is a hash of
// the content alone (index.ChunkID): two lines for one id carry the same
// content. Wing, room and hall are the classifier's output for that content,
// so they agree too, under one classifier. The assumption this rests on: a
// classifier change (new room keywords) is covered by the chunk fingerprint,
// which makes the store stale and is rebuilt whole, so lines classified by two
// classifiers never meet in one store. A relabel rewrites the chunk line in
// place (Tx.Rewrite) rather than appending a second one.
type chunkFold struct {
	order  []string
	chunks map[string]Chunk
	owners map[string][]ownerLine
}

func newChunkFold() *chunkFold {
	return &chunkFold{chunks: map[string]Chunk{}, owners: map[string][]ownerLine{}}
}

func foldChunkLines(lines []chunkLine) *chunkFold {
	f := newChunkFold()
	for _, l := range lines {
		switch l.Kind {
		case lineChunk:
			if _, ok := f.chunks[l.ID]; !ok {
				f.order = append(f.order, l.ID)
				f.chunks[l.ID] = Chunk{ID: l.ID, Content: l.Content, Wing: l.Wing, Room: l.Room, Hall: l.Hall}
			}
		case lineOwner:
			f.setOwner(l.ID, ownerLine{Owner: *l.Owner, Ownership: l.Ownership})
		}
	}
	// An owner line whose chunk line is missing (a hand edit) has nothing to
	// own; it is dropped from the fold.
	for id := range f.owners {
		if _, ok := f.chunks[id]; !ok {
			delete(f.owners, id)
		}
	}
	return f
}

// setOwner adds or replaces id's line for ol.Owner. It reports whether
// anything changed, and whether an existing line was rewritten.
func (f *chunkFold) setOwner(id string, ol ownerLine) (changed, rewrote bool) {
	k := ol.Owner.key()
	for i, cur := range f.owners[id] {
		if cur.Owner.key() == k {
			if cur.Ownership == ol.Ownership {
				return false, false
			}
			f.owners[id][i] = ol
			return true, true
		}
	}
	f.owners[id] = append(f.owners[id], ol)
	return true, false
}

// addChunk adds a chunk line if the id is new.
func (f *chunkFold) addChunk(c Chunk) {
	if _, ok := f.chunks[c.ID]; !ok {
		f.order = append(f.order, c.ID)
		f.chunks[c.ID] = c
	}
}

// dropOwner removes o from every chunk, except the ids in keep. It reports
// whether anything was removed.
func (f *chunkFold) dropOwner(o Owner, keep map[string]bool) bool {
	k := o.key()
	removed := false
	for id, ols := range f.owners {
		if keep[id] {
			continue
		}
		out := ols[:0]
		for _, ol := range ols {
			if ol.Owner.key() == k {
				removed = true
				continue
			}
			out = append(out, ol)
		}
		f.owners[id] = out
	}
	return removed
}

// dropOwnersWhere removes every owner line for which drop is true, from every
// chunk not in keep, in one pass. It reports whether it removed any.
func (f *chunkFold) dropOwnersWhere(drop func(Owner) bool, keep map[string]bool) bool {
	removed := false
	for id, ols := range f.owners {
		if keep[id] {
			continue
		}
		out := ols[:0]
		for _, ol := range ols {
			if drop(ol.Owner) {
				removed = true
				continue
			}
			out = append(out, ol)
		}
		f.owners[id] = out
	}
	return removed
}

// prune deletes every chunk left with no owner of any kind.
func (f *chunkFold) prune() bool {
	out := f.order[:0]
	removed := false
	for _, id := range f.order {
		if len(f.owners[id]) == 0 {
			delete(f.chunks, id)
			delete(f.owners, id)
			removed = true
			continue
		}
		out = append(out, id)
	}
	f.order = out
	return removed
}

// lines serialises the fold: each chunk line followed by its owner lines.
func (f *chunkFold) lines() []chunkLine {
	var out []chunkLine
	for _, id := range f.order {
		c := f.chunks[id]
		out = append(out, chunkLine{Kind: lineChunk, ID: id, Content: c.Content, Wing: c.Wing, Room: c.Room, Hall: c.Hall})
		for _, ol := range f.owners[id] {
			o := ol.Owner
			out = append(out, chunkLine{Kind: lineOwner, ID: id, Owner: &o, Ownership: ol.Ownership})
		}
	}
	return out
}

// countOwned is the number of distinct chunk ids o owns.
func (f *chunkFold) countOwned(o Owner) int {
	k, n := o.key(), 0
	for _, ols := range f.owners {
		for _, ol := range ols {
			if ol.Owner.key() == k {
				n++
				break
			}
		}
	}
	return n
}

// stored folds one chunk against the ledger: the owner-derived fields come
// from the live owner with the earliest day; ties go to the smaller kind, then
// the smaller key. An archive's or batch's day is read from the ledger, never
// from its owner line; a note's day is its owner line's.
func (f *chunkFold) stored(id string, l *Ledger) StoredChunk {
	sc := StoredChunk{Chunk: f.chunks[id]}
	var best *ownerLine
	var bestDay string
	for i := range f.owners[id] {
		ol := &f.owners[id][i]
		sc.Owners = append(sc.Owners, ol.Owner)
		day, live := l.liveDay(ol.Owner, ol.Day)
		if !live {
			continue
		}
		if best == nil || ownerBefore(day, ol.Owner, bestDay, best.Owner) {
			best, bestDay = ol, day
		}
	}
	if best != nil {
		o := best.Owner
		sc.Selected = &o
		sc.Ownership = best.Ownership
		sc.Ownership.Day = ""
		sc.FiledAt = bestDay
	}
	return sc
}

func ownerBefore(dayA string, a Owner, dayB string, b Owner) bool {
	if dayA != dayB {
		return dayA < dayB
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	return a.key() < b.key()
}

// kgLine is one line of index/<p>/kg/records.jsonl: one record id, one owner,
// and the record's opaque payload.
type kgLine struct {
	ID      string          `json:"id"`
	Owner   Owner           `json:"owner"`
	Payload json.RawMessage `json:"payload"`
}

func validKGLine(l *kgLine) bool { return l.ID != "" && l.Owner.validate() == nil }

// kgFold is the KG file folded by id. It keeps the first payload per id: a KG
// record's id is derived from the record itself
// (authored-and-extracted-knowledge-graph-records defines it), so two lines
// for one id carry the same payload, under one extractor; an extractor change
// is covered by the chunk fingerprint, as for chunk lines.
type kgFold struct {
	order    []string
	payloads map[string]json.RawMessage
	owners   map[string][]Owner
}

func foldKGLines(lines []kgLine) *kgFold {
	f := &kgFold{payloads: map[string]json.RawMessage{}, owners: map[string][]Owner{}}
	for _, l := range lines {
		f.add(l.ID, l.Owner, l.Payload)
	}
	return f
}

// add adds (id, owner); it reports whether the pair is new.
func (f *kgFold) add(id string, o Owner, payload json.RawMessage) bool {
	if _, ok := f.payloads[id]; !ok {
		f.order = append(f.order, id)
		f.payloads[id] = payload
	}
	for _, cur := range f.owners[id] {
		if cur.key() == o.key() {
			return false
		}
	}
	f.owners[id] = append(f.owners[id], o)
	return true
}

// dropOwner removes o from every record, deletes the records left with no
// owner, and reports whether anything was removed.
func (f *kgFold) dropOwner(o Owner) bool {
	k := o.key()
	return f.dropOwnersWhere(func(cur Owner) bool { return cur.key() == k })
}

// dropOwnersWhere removes every owner for which drop is true, and deletes
// every record left with no owner.
func (f *kgFold) dropOwnersWhere(drop func(Owner) bool) bool {
	removed := false
	for id, os := range f.owners {
		out := os[:0]
		for _, cur := range os {
			if !drop(cur) {
				out = append(out, cur)
			} else {
				removed = true
			}
		}
		f.owners[id] = out
	}
	order := f.order[:0]
	for _, id := range f.order {
		if len(f.owners[id]) == 0 {
			delete(f.owners, id)
			delete(f.payloads, id)
			continue
		}
		order = append(order, id)
	}
	f.order = order
	return removed
}

func (f *kgFold) lines() []kgLine {
	var out []kgLine
	for _, id := range f.order {
		for _, o := range f.owners[id] {
			out = append(out, kgLine{ID: id, Owner: o, Payload: f.payloads[id]})
		}
	}
	return out
}

// Store is a lock-free snapshot of one project's host-local index: the
// folded ledger, chunks and KG records.
//
// The reader rule (ReadGeneration): read the change counter BEFORE taking the
// snapshot, and remember that counter, so a write that lands during the read
// is picked up by the next comparison.
type Store struct {
	ledgerRecs []ledgerRecord
	ledger     *Ledger
	chunks     *chunkFold
	kg         *kgFold
}

// ReadStore reads a project's index without any lock. A torn final line in
// any file is skipped, and a malformed interior line is skipped with a
// warning. A project with no index reads as empty.
func ReadStore(vault *storage.Vault, project string) (*Store, error) {
	pf, err := filesFor(vault, project)
	if err != nil {
		return nil, err
	}
	return readStoreFiles(pf)
}

// readStoreFiles reads the ledger, then the chunks, then the KG records. It is
// also the writer cache's loader (loadState), so that order stays.
func readStoreFiles(pf projectFiles) (*Store, error) {
	recs, err := readLedgerFile(pf.ledger)
	if err != nil {
		return nil, err
	}
	cl, _, _, err := readLines(pf.chunks)
	if err != nil {
		return nil, err
	}
	kg, err := readKGFile(pf.kg)
	if err != nil {
		return nil, err
	}
	return &Store{
		ledgerRecs: recs,
		ledger:     foldLedger(recs),
		chunks:     foldChunkLines(decodeLines(pf.chunks, cl, validChunkLine)),
		kg:         kg,
	}, nil
}

// readKGFile reads and folds the KG records file.
func readKGFile(path string) (*kgFold, error) {
	kl, _, _, err := readLines(path)
	if err != nil {
		return nil, err
	}
	return foldKGLines(decodeLines(path, kl, validKGLine)), nil
}

// KGSnapshot is a lock-free snapshot of one project's ledger and KG records,
// for a reader that needs the knowledge graph and nothing else: it never
// reads chunks.jsonl. Its KG view is exactly a Store's, because a KG record's
// liveness depends on the ledger alone.
//
// The reader rule is Store's: read the change counter (ReadGeneration) BEFORE
// the snapshot, so a commit that lands during the read makes the next
// comparison reload. Liveness is always judged against the snapshot's own
// ledger, so a record shows only if that ledger makes it live; a read that
// races a commit can miss some of that commit's records, for that one call.
// The read order, ledger then KG (as ReadStore, and as a writer appends KG
// lines before the ledger line), only changes which racing records are
// missed.
type KGSnapshot struct {
	ledger *Ledger
	kg     *kgFold
}

// ReadKG reads a project's ledger and KG records without any lock, never its
// chunks. Torn and malformed lines are handled as ReadStore handles them. A
// project with no index reads as empty.
func ReadKG(vault *storage.Vault, project string) (*KGSnapshot, error) {
	pf, err := filesFor(vault, project)
	if err != nil {
		return nil, err
	}
	recs, err := readLedgerFile(pf.ledger)
	if err != nil {
		return nil, err
	}
	kg, err := readKGFile(pf.kg)
	if err != nil {
		return nil, err
	}
	return &KGSnapshot{ledger: foldLedger(recs), kg: kg}, nil
}

// Ledger is the snapshot's folded ledger.
func (s *KGSnapshot) Ledger() *Ledger { return s.ledger }

// KG returns every KG record; with ledgeredOnly, only those with a live owner,
// as Store.KG.
func (s *KGSnapshot) KG(ledgeredOnly bool) []StoredKG { return kgRecords(s.kg, s.ledger, ledgeredOnly) }

func readLedgerFile(path string) ([]ledgerRecord, error) {
	lines, _, _, err := readLines(path)
	if err != nil {
		return nil, err
	}
	return decodeLines(path, lines, validLedgerRecord), nil
}

// Ledger is the snapshot's folded ledger.
func (s *Store) Ledger() *Ledger { return s.ledger }

// Chunks returns every chunk, folded. With ledgeredOnly, it returns only the
// chunks search may load: those with a live owner, meaning an archive that is
// a session's live archive in the ledger, a ledgered import batch, or a note.
func (s *Store) Chunks(ledgeredOnly bool) []StoredChunk {
	var out []StoredChunk
	for _, id := range s.chunks.order {
		sc := s.chunks.stored(id, s.ledger)
		if ledgeredOnly && sc.Selected == nil {
			continue
		}
		out = append(out, sc)
	}
	return out
}

// KG returns every KG record. With ledgeredOnly, only records with a live
// owner, by the same rule as Chunks.
func (s *Store) KG(ledgeredOnly bool) []StoredKG { return kgRecords(s.kg, s.ledger, ledgeredOnly) }

// kgRecords is the one KG view, shared by Store and KGSnapshot.
func kgRecords(kg *kgFold, l *Ledger, ledgeredOnly bool) []StoredKG {
	var out []StoredKG
	for _, id := range kg.order {
		rec := StoredKG{KGRecord: KGRecord{ID: id, Payload: kg.payloads[id]}, Owners: kg.owners[id]}
		if ledgeredOnly {
			live := false
			for _, o := range rec.Owners {
				if _, ok := l.liveDay(o, ""); ok {
					live = true
					break
				}
			}
			if !live {
				continue
			}
		}
		out = append(out, rec)
	}
	return out
}

// CountChunks is the number of distinct chunk ids o owns in the store. The
// repair pass compares it with the ledger's chunk count and re-ingests the
// archive on a shortfall.
func (s *Store) CountChunks(o Owner) int { return s.chunks.countOwned(o) }
