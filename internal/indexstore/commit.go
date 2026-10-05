// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"errors"
	"fmt"
	"slices"

	"github.com/suykerbuyk/vibe-palace/internal/index"
)

// Errors a commit step returns instead of writing.
var (
	// ErrNoLedger: the project has no ledger on this host. The caller creates
	// it with Tx.EnsureLedger, naming the archive that triggered it, and
	// retries. Every caller of CommitArchive, Supersede, CommitBatch and the
	// Record and Clear methods must handle it: a ledger can vanish under a
	// caller (a chunk discard removes it first, so one killed part-way leaves
	// none).
	ErrNoLedger = errors.New("indexstore: the project has no ledger; call EnsureLedger first")
	// ErrOtherArchive: the session's ledger names a different archive. The
	// ingester decides between Supersede and RecordSuperseded.
	ErrOtherArchive = errors.New("indexstore: the session's ledger names a different archive")
	// ErrSuperseded: the archive was replaced, or recorded superseded on
	// arrival, so nothing of it may be ingested.
	ErrSuperseded = errors.New("indexstore: the archive was superseded")
	// ErrBatchIsSession: a batch id that names a ledgered session.
	ErrBatchIsSession = errors.New("indexstore: the batch id names a session")
)

// commitStep is called after each durable step of a commit, with the step's
// name. A TEST SEAM: a test records the order of the steps, and returns an
// error to stop the commit there, which leaves the files exactly as a process
// killed at that point would.
var commitStep = func(step string) error { return nil }

// ArchiveCommit is one transcript archive's computed output, ready to commit:
// everything was embedded, chunked and extracted outside the lock.
type ArchiveCommit struct {
	SessionID   string
	SHA         string // the archive's source_sha256
	ArchivePath string // for display only
	// CapturedAt is the archive manifest's captured_at, recorded on the
	// session record (optional).
	CapturedAt string
	// StartDay is the session's UTC start day (archive.Manifest.SessionDay), and
	// StartDaySource how it was found: DayFromTranscript or DayFromCapturedAt.
	StartDay       string
	StartDaySource string
	Vectors        map[string][]float32 // by chunk id
	Chunks         []OwnedChunk
	KG             []KGRecord
	// Retarget is for Supersede only: the source_sha256 of the archive the
	// session is superseding TO, which this commit's archive replaces
	// before that supersede could finish: it is no longer on disk (the hook
	// rewrote it), or a newer archive of the session arrived. The caller
	// names it, and Supersede re-targets the session to this commit's
	// archive only when it matches the ledger. Empty everywhere else.
	Retarget string
}

// SupersedeCommit is a newer archive of a session whose ledger records an
// older one; it has the same fields as ArchiveCommit.
type SupersedeCommit = ArchiveCommit

// BatchCommit is one mempalace import batch, ready to commit.
type BatchCommit struct {
	BatchID  string
	StartDay string // the earliest drawer filed_at in the export, else 2000-01-01
	Vectors  map[string][]float32
	Chunks   []OwnedChunk
	KG       []KGRecord
}

func (c *ArchiveCommit) validate() error {
	if c.SessionID == "" || c.SHA == "" {
		return errors.New("indexstore: an archive commit needs a session id and a source_sha256")
	}
	if c.Retarget == c.SHA {
		return errors.New("indexstore: an archive cannot re-target a supersede to itself")
	}
	if c.StartDaySource != DayFromTranscript && c.StartDaySource != DayFromCapturedAt {
		return fmt.Errorf("indexstore: start day source %q is not %q or %q", c.StartDaySource, DayFromTranscript, DayFromCapturedAt)
	}
	if err := validateDay(c.StartDay); err != nil {
		return err
	}
	return validateChunks(c.Chunks, c.Vectors)
}

// validateChunks checks every chunk's id against index.ChunkID, and that
// every vector belongs to a chunk of this commit (the .vec rule: a vector is
// written only in the Tx that makes its id durable).
func validateChunks(chunks []OwnedChunk, vecs map[string][]float32) error {
	ids := make(map[string]bool, len(chunks))
	for _, c := range chunks {
		if want := index.ChunkID(c.Content); c.ID != want {
			return fmt.Errorf("indexstore: chunk id %q is not index.ChunkID of its content (%q)", c.ID, want)
		}
		ids[c.ID] = true
	}
	for id := range vecs {
		if !ids[id] {
			return fmt.Errorf("indexstore: vector %q belongs to no chunk of this commit", id)
		}
	}
	return nil
}

// distinctIDs is the chunk count the ledger records: the number of distinct
// chunk ids, so a transcript that repeats a chunk never reads as a shortfall.
func distinctIDs(chunks []OwnedChunk) int {
	seen := map[string]bool{}
	for _, c := range chunks {
		seen[c.ID] = true
	}
	return len(seen)
}

// state returns the Tx's view of the store, loading it if needed: the
// process's cached state when it matches the counter Lock found and the store
// files' sizes, else a fresh read.
func (tx *Tx) state() (*state, error) {
	if tx.st != nil {
		return tx.st, nil
	}
	if s := cachedState(tx.vault.Root, tx.project, tx.lockGen, tx.files); s != nil {
		tx.st = s
		return s, nil
	}
	s, err := loadState(tx.files, tx.lockGen)
	if err != nil {
		return nil, err
	}
	tx.st = s
	return s, nil
}

// fail marks the Tx broken (its cached state is dropped at the end) and
// returns err.
func (tx *Tx) fail(err error) error {
	if err != nil {
		tx.mu.Lock()
		tx.broken = true
		tx.mu.Unlock()
	}
	return err
}

// Ledger is the project's folded ledger as this Tx sees it.
func (tx *Tx) Ledger() (*Ledger, error) {
	s, err := tx.state()
	if err != nil {
		return nil, err
	}
	return s.ledger, nil
}

// appendLines appends data to path after cutting any torn final line off it.
func (tx *Tx) appendLines(path string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	torn, err := tailIsTorn(path)
	if err != nil {
		return err
	}
	if torn {
		if err := tx.beginDestructive(); err != nil {
			return err
		}
		if _, err := truncateTornTail(path); err != nil {
			return err
		}
	}
	if err := appendFile(path, data); err != nil {
		return err
	}
	tx.noteWrite(false)
	return nil
}

// putVectors writes each vector through vw, then flushes vw once if it is a
// VectorFlusher, so a batch costs one directory fsync, not one per vector.
func (tx *Tx) putVectors(vw VectorWriter, vecs map[string][]float32) error {
	if len(vecs) == 0 {
		return nil
	}
	if vw == nil {
		return errors.New("indexstore: vectors to write but no VectorWriter")
	}
	ids := make([]string, 0, len(vecs))
	for id := range vecs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := vw.Put(tx.project, id, vecs[id]); err != nil {
			return fmt.Errorf("indexstore: write vector %s: %w", id, err)
		}
	}
	if f, ok := vw.(VectorFlusher); ok {
		if err := f.Flush(); err != nil {
			return fmt.Errorf("indexstore: flush vectors: %w", err)
		}
	}
	tx.noteWrite(false)
	return nil
}

// appendChunks appends the chunk lines and owner lines that are new for
// owner, and nothing else: an existing owner line is never rewritten here.
func (tx *Tx) appendChunks(s *state, owner Owner, recs []OwnedChunk) error {
	var lines []chunkLine
	newIDs := map[string]struct{}{}
	newPairs := map[string]struct{}{}
	for _, r := range recs {
		if _, ok := s.ids[r.ID]; !ok {
			if _, dup := newIDs[r.ID]; !dup {
				newIDs[r.ID] = struct{}{}
				lines = append(lines, chunkLine{Kind: lineChunk, ID: r.ID, Content: r.Content, Wing: r.Wing, Room: r.Room, Hall: r.Hall})
			}
		}
		pk := pairKey(r.ID, owner)
		if _, ok := s.ownerPairs[pk]; ok {
			continue
		}
		if _, dup := newPairs[pk]; dup {
			continue
		}
		newPairs[pk] = struct{}{}
		ol := r.Ownership
		if owner.Kind != OwnerNote {
			ol.Day = ""
		}
		o := owner
		lines = append(lines, chunkLine{Kind: lineOwner, ID: r.ID, Owner: &o, Ownership: ol})
	}
	data, err := encodeLines(lines)
	if err != nil {
		return err
	}
	if err := tx.appendLines(tx.files.chunks, data); err != nil {
		return err
	}
	for id := range newIDs {
		s.ids[id] = struct{}{}
	}
	for pk := range newPairs {
		s.ownerPairs[pk] = struct{}{}
	}
	return nil
}

// appendKG appends the (id, owner) KG lines that are new.
func (tx *Tx) appendKG(s *state, owner Owner, recs []KGRecord) error {
	var lines []kgLine
	newPairs := map[string]struct{}{}
	for _, r := range recs {
		pk := pairKey(r.ID, owner)
		if _, ok := s.kgPairs[pk]; ok {
			continue
		}
		if _, dup := newPairs[pk]; dup {
			continue
		}
		newPairs[pk] = struct{}{}
		lines = append(lines, kgLine{ID: r.ID, Owner: owner, Payload: r.Payload})
	}
	data, err := encodeLines(lines)
	if err != nil {
		return err
	}
	if err := tx.appendLines(tx.files.kg, data); err != nil {
		return err
	}
	for pk := range newPairs {
		s.kgPairs[pk] = struct{}{}
	}
	return nil
}

// appendLedger appends ledger records. It refuses (ErrNoLedger) to create a
// ledger whose first record is not the baseline set: a ledger begun by any
// other record would read as existing, EnsureLedger would never run, and the
// project's whole history would count as new archives for automatic ingest.
// The check reads the file, not the cached state, so a cache that missed a
// removal (a writer that died part-way) cannot get past it.
func (tx *Tx) appendLedger(s *state, recs ...ledgerRecord) error {
	if len(recs) == 0 {
		return nil
	}
	if recs[0].Kind != recBaseline {
		empty, err := fileEmpty(tx.files.ledger)
		if err != nil {
			return err
		}
		if empty {
			return ErrNoLedger
		}
	}
	data, err := encodeLines(recs)
	if err != nil {
		return err
	}
	if err := tx.appendLines(tx.files.ledger, data); err != nil {
		return err
	}
	s.addLedger(recs...)
	return nil
}

// CommitArchive is the per-archive commit step, shared by the ingester and
// the rebuild (ADR-014 decision 7, "Write order for each archive"). Each step
// is durable before the next:
//
//  1. the vectors, through vw (the embed cache's writer writes each one
//     atomically: temp file, fsync, rename);
//  2. the chunks and their owner lines, appended and fsynced;
//  3. the KG records and their owner lines, appended and fsynced;
//  4. the ledger's session record, live, with the chunk count (the distinct
//     ids the archive owns), the start day and its source, fsynced.
//
// A run killed part-way leaves the archive out of the ledger: the session is
// still pending, its records are invisible to search, and a rerun completes it
// without duplicating a record.
//
// It refuses (ErrOtherArchive) when the session's ledger names a different
// archive, and (ErrSuperseded) when this archive was replaced. Committing the
// session's live archive again is a repair: missing records are added, and the
// ledger record is rewritten only if its chunk count changed.
func (tx *Tx) CommitArchive(c ArchiveCommit, vw VectorWriter) error {
	if err := c.validate(); err != nil {
		return err
	}
	if c.Retarget != "" {
		return errors.New("indexstore: Retarget is for Supersede only")
	}
	s, err := tx.state()
	if err != nil {
		return err
	}
	l := s.ledger
	if !l.Exists() {
		return ErrNoLedger
	}
	if _, gone := l.superseded[c.SHA]; gone {
		return fmt.Errorf("%w: %s", ErrSuperseded, c.SHA)
	}
	gen := 1
	prev, has := l.Session(c.SessionID)
	if has {
		if prev.SHA != c.SHA || prev.State != StateLive {
			return fmt.Errorf("%w: session %s records %s (%s), not %s", ErrOtherArchive, c.SessionID, prev.SHA, prev.State, c.SHA)
		}
		gen = prev.Generation
	}
	if err := tx.ensureChunkFingerprint(); err != nil {
		return err
	}
	owner := ArchiveOwner(c.SHA)
	if err := tx.fail(tx.putVectors(vw, c.Vectors)); err != nil {
		return err
	}
	if err := tx.fail(commitStep("vectors")); err != nil {
		return err
	}
	if err := tx.fail(tx.appendChunks(s, owner, c.Chunks)); err != nil {
		return err
	}
	if err := tx.fail(commitStep("chunks")); err != nil {
		return err
	}
	if err := tx.fail(tx.appendKG(s, owner, c.KG)); err != nil {
		return err
	}
	if err := tx.fail(commitStep("kg")); err != nil {
		return err
	}
	n := distinctIDs(c.Chunks)
	if has && prev.ChunkCount == n && prev.StartDay == c.StartDay && prev.StartDaySource == c.StartDaySource && prev.CapturedAt == c.CapturedAt {
		return tx.fail(commitStep("ledger"))
	}
	rec := ledgerRecord{
		Kind: recSession, SessionID: c.SessionID, State: StateLive, SHA: c.SHA, ArchivePath: c.ArchivePath, CapturedAt: c.CapturedAt,
		StartDay: c.StartDay, StartDaySource: c.StartDaySource, ChunkCount: &n, Generation: gen,
	}
	if err := tx.fail(tx.appendLedger(s, rec)); err != nil {
		return err
	}
	return tx.fail(commitStep("ledger"))
}

// Supersede replaces a session's ledgered archive with a newer one (ADR-014
// decision 7, "Re-archived sessions: supersede"), in three commit steps:
//
//  1. a session record in state superseding, naming the old and the new
//     source_sha256, fsynced;
//  2. the newer archive's vectors, then chunks.jsonl and the KG rewritten
//     atomically: the newer archive's records and ownership added, the older
//     archive's ownership removed, and every record left with no owner of any
//     kind deleted. A record a batch or a note owns never loses an owner here;
//  3. the session record, live, for the newer archive, with its chunk count,
//     its own start day, and the session's generation incremented.
//
// An archive the ledger records as replaced is refused (ErrSuperseded), as in
// CommitArchive: superseding a session back to it would roll the session back
// to an archive nothing may ingest again.
//
// The ledger and the ownership are re-read under the lock first, so an ingest
// of the older archive that another run committed just before is seen and its
// ownership removed. A crash after step 1 or 2 leaves the session superseding,
// which is pending; calling Supersede again with the same commit repeats steps
// 2 and 3, which are idempotent.
//
// Re-target. A session superseding to an archive B that is no longer on disk
// (the hook rewrote it to C before the crashed supersede was resumed) can
// never finish towards B. The caller passes C with Retarget = B. Step 1 then
// records B superseded and re-marks the session superseding from the same
// older archive to C; step 2 removes the ownership of both the older archive
// and B (whatever part of B step 2 had already added), and step 3 records C
// live. Retarget must name the ledger's target, or the call is refused.
func (tx *Tx) Supersede(c SupersedeCommit, vw VectorWriter) error {
	if err := c.validate(); err != nil {
		return err
	}
	s, err := tx.state()
	if err != nil {
		return err
	}
	if !s.ledger.Exists() {
		return ErrNoLedger
	}
	prev, ok := s.ledger.Session(c.SessionID)
	if !ok {
		return fmt.Errorf("indexstore: session %s is not in the ledger; nothing to supersede", c.SessionID)
	}
	// A revert: the session's superseding target vanished with nothing newer
	// on disk, and the caller re-targets it back to the archive it was
	// superseding FROM (plan revision R1). That archive reads superseded
	// until this commit makes it current again.
	revert := prev.State == StateSuperseding && c.Retarget != "" && c.Retarget == prev.SHA && c.SHA == prev.SupersedingFrom
	if _, gone := s.ledger.superseded[c.SHA]; gone && !revert {
		return fmt.Errorf("%w: %s", ErrSuperseded, c.SHA)
	}
	if err := tx.ensureChunkFingerprint(); err != nil {
		return err
	}
	var old, vanished string
	var prevGen int
	switch {
	case prev.State == StateLive && prev.SHA != c.SHA:
		old, prevGen = prev.SHA, prev.Generation
		mark := ledgerRecord{
			Kind: recSession, SessionID: c.SessionID, State: StateSuperseding, SHA: c.SHA,
			SupersedingFrom: old, ArchivePath: c.ArchivePath, Generation: prevGen,
		}
		if err := tx.fail(tx.appendLedger(s, mark)); err != nil {
			return err
		}
	case prev.State == StateSuperseding && prev.SHA == c.SHA:
		old, prevGen = prev.SupersedingFrom, prev.Generation
	case prev.State == StateSuperseding && c.Retarget != "" && prev.SHA == c.Retarget:
		old, prevGen, vanished = prev.SupersedingFrom, prev.Generation, prev.SHA
		gone := ledgerRecord{Kind: recSuperseded, SessionID: c.SessionID, SHA: vanished}
		mark := ledgerRecord{
			Kind: recSession, SessionID: c.SessionID, State: StateSuperseding, SHA: c.SHA,
			SupersedingFrom: old, ArchivePath: c.ArchivePath, Generation: prevGen,
		}
		if err := tx.fail(tx.appendLedger(s, gone, mark)); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: session %s records %s (%s); cannot supersede it with %s",
			ErrOtherArchive, c.SessionID, prev.SHA, prev.State, c.SHA)
	}
	if err := tx.fail(commitStep("superseding")); err != nil {
		return err
	}

	if err := tx.fail(tx.putVectors(vw, c.Vectors)); err != nil {
		return err
	}
	if err := tx.fail(commitStep("vectors")); err != nil {
		return err
	}
	newOwner, oldOwner := ArchiveOwner(c.SHA), ArchiveOwner(old)
	// An archive the ledger records as superseded owns nothing: this also
	// removes a vanished re-target's partial records when the re-targeted
	// supersede is itself resumed after a crash.
	// (The archive being made live is never in that set: the fold drops the
	// archive a session record names, which covers a revert's own archive.)
	supersededOwner := func(o Owner) bool { return o.Kind == OwnerArchive && s.ledger.Superseded(o.SHA) }
	// On a revert the archive being superseded FROM is the one being made
	// live: its fresh records must not be dropped as the old owner's.
	dropOld := old != c.SHA
	if err := tx.fail(tx.rewriteChunks(s, func(f *chunkFold) {
		keep := map[string]bool{}
		for _, r := range c.Chunks {
			f.addChunk(r.Chunk)
			ol := r.Ownership
			ol.Day = ""
			f.setOwner(r.ID, ownerLine{Owner: newOwner, Ownership: ol})
		}
		if dropOld {
			f.dropOwner(oldOwner, keep)
		}
		f.dropOwnersWhere(supersededOwner, keep)
		f.prune()
	})); err != nil {
		return err
	}
	if err := tx.fail(commitStep("chunks")); err != nil {
		return err
	}
	if err := tx.fail(tx.rewriteKG(s, func(f *kgFold) {
		for _, r := range c.KG {
			f.add(r.ID, newOwner, r.Payload)
		}
		if dropOld {
			f.dropOwner(oldOwner)
		}
		f.dropOwnersWhere(supersededOwner)
	})); err != nil {
		return err
	}
	if err := tx.fail(commitStep("kg")); err != nil {
		return err
	}
	n := distinctIDs(c.Chunks)
	live := ledgerRecord{
		Kind: recSession, SessionID: c.SessionID, State: StateLive, SHA: c.SHA, ArchivePath: c.ArchivePath, CapturedAt: c.CapturedAt,
		StartDay: c.StartDay, StartDaySource: c.StartDaySource, ChunkCount: &n, Generation: prevGen + 1,
	}
	if err := tx.fail(tx.appendLedger(s, live)); err != nil {
		return err
	}
	return tx.fail(commitStep("ledger"))
}

// RecordSuperseded records an older archive met after its session's newer
// one: it is never ingested, and is never pending again.
func (tx *Tx) RecordSuperseded(sessionID, sha, archivePath string) error {
	if sessionID == "" || sha == "" {
		return errors.New("indexstore: RecordSuperseded needs a session id and a source_sha256")
	}
	s, err := tx.state()
	if err != nil {
		return err
	}
	if !s.ledger.Exists() {
		return ErrNoLedger
	}
	return tx.fail(tx.appendLedger(s, ledgerRecord{Kind: recSuperseded, SessionID: sessionID, SHA: sha, ArchivePath: archivePath}))
}

// CommitBatch commits one mempalace import batch in the order of
// CommitArchive (vectors, chunks, KG, then the ledger), owned by the batch,
// with a batch ledger record carrying its distinct-id chunk count and its
// start day (start_day_source import). A batch is not a session, so a batch id
// that names a session is refused.
func (tx *Tx) CommitBatch(c BatchCommit, vw VectorWriter) error {
	if c.BatchID == "" {
		return errors.New("indexstore: a batch commit needs a batch id")
	}
	if err := validateDay(c.StartDay); err != nil {
		return err
	}
	if err := validateChunks(c.Chunks, c.Vectors); err != nil {
		return err
	}
	s, err := tx.state()
	if err != nil {
		return err
	}
	if !s.ledger.Exists() {
		return ErrNoLedger
	}
	if _, isSession := s.ledger.Session(c.BatchID); isSession {
		return fmt.Errorf("%w: %s", ErrBatchIsSession, c.BatchID)
	}
	if err := tx.ensureChunkFingerprint(); err != nil {
		return err
	}
	owner := BatchOwner(c.BatchID)
	if err := tx.fail(tx.putVectors(vw, c.Vectors)); err != nil {
		return err
	}
	if err := tx.fail(commitStep("vectors")); err != nil {
		return err
	}
	if err := tx.fail(tx.Append(owner, c.Chunks)); err != nil {
		return err
	}
	if err := tx.fail(commitStep("chunks")); err != nil {
		return err
	}
	if err := tx.fail(tx.appendKG(s, owner, c.KG)); err != nil {
		return err
	}
	if err := tx.fail(commitStep("kg")); err != nil {
		return err
	}
	n := distinctIDs(c.Chunks)
	rec := ledgerRecord{Kind: recBatch, BatchID: c.BatchID, ChunkCount: &n, StartDay: c.StartDay, StartDaySource: DayFromImport}
	if err := tx.fail(tx.appendLedger(s, rec)); err != nil {
		return err
	}
	return tx.fail(commitStep("ledger"))
}

// Append adds chunk records and owner's ownership of them, and never removes
// or rewrites anything. A chunk id already stored keeps its record; an owner
// line already stored is left as it is.
func (tx *Tx) Append(owner Owner, recs []OwnedChunk) error {
	if err := owner.validate(); err != nil {
		return err
	}
	if err := validateChunks(recs, nil); err != nil {
		return err
	}
	if owner.Kind == OwnerNote {
		for _, r := range recs {
			if err := validateDay(r.Day); err != nil {
				return err
			}
		}
	}
	s, err := tx.state()
	if err != nil {
		return err
	}
	if err := tx.ensureChunkFingerprint(); err != nil {
		return err
	}
	return tx.fail(tx.appendChunks(s, owner, recs))
}

// ReplaceOwned makes recs exactly the set of chunks owner owns: it adds the
// missing records and ownership, rewrites an owner line whose fields differ
// (for a note owner, its day or source_ref), removes owner from every other
// chunk, and deletes every chunk left with no owner. A call that only adds
// ownership appends; any removal or rewrite rewrites the file and changes the
// epoch, so a running engine reloads instead of keeping an old date.
// decision-chunks-in-the-host-local-store calls it with a note owner.
func (tx *Tx) ReplaceOwned(owner Owner, recs []OwnedChunk) error {
	if err := owner.validate(); err != nil {
		return err
	}
	if err := validateChunks(recs, nil); err != nil {
		return err
	}
	if owner.Kind == OwnerNote {
		for _, r := range recs {
			if err := validateDay(r.Day); err != nil {
				return err
			}
		}
	}
	s, err := tx.state()
	if err != nil {
		return err
	}
	if err := tx.ensureChunkFingerprint(); err != nil {
		return err
	}
	f, err := tx.readChunkFold()
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	rewrite := false
	for _, r := range recs {
		keep[r.ID] = true
		ol := r.Ownership
		if owner.Kind != OwnerNote {
			ol.Day = ""
		}
		for _, have := range f.owners[r.ID] {
			if have.Owner.key() == owner.key() && have.Ownership != ol {
				rewrite = true
			}
		}
	}
	for id, ols := range f.owners {
		if keep[id] {
			continue
		}
		for _, ol := range ols {
			if ol.Owner.key() == owner.key() {
				rewrite = true
			}
		}
	}
	if !rewrite {
		// The decision was made from the file; append against the same view,
		// not against a cached id set that may predate it.
		s.indexChunks(f)
		return tx.fail(tx.appendChunks(s, owner, recs))
	}
	for _, r := range recs {
		f.addChunk(r.Chunk)
		ol := r.Ownership
		if owner.Kind != OwnerNote {
			ol.Day = ""
		}
		f.setOwner(r.ID, ownerLine{Owner: owner, Ownership: ol})
	}
	f.dropOwner(owner, keep)
	f.prune()
	return tx.fail(tx.writeChunkFold(s, f))
}

// Rewrite relabels chunks: for each id, a non-empty Wing or Room replaces the
// stored one. Ids are content hashes, so a relabel never changes an id. It
// rewrites the file and changes the epoch. Callers: the palace relabel
// (palace-navigation-over-the-host-local-chunk-store) and the project rename
// (index-fingerprints-project-lifecycle-and-migration-marker).
func (tx *Tx) Rewrite(changes map[string]Labels) error {
	if len(changes) == 0 {
		return nil
	}
	s, err := tx.state()
	if err != nil {
		return err
	}
	return tx.fail(tx.rewriteChunks(s, func(f *chunkFold) {
		for id, lb := range changes {
			c, ok := f.chunks[id]
			if !ok {
				continue
			}
			if lb.Wing != "" {
				c.Wing = lb.Wing
			}
			if lb.Room != "" {
				c.Room = lb.Room
			}
			f.chunks[id] = c
		}
	}))
}

// rewriteChunks reads chunks.jsonl whole under the lock, applies edit, and
// replaces the file atomically. It is more than an append: the epoch changes.
func (tx *Tx) rewriteChunks(s *state, edit func(*chunkFold)) error {
	f, err := tx.readChunkFold()
	if err != nil {
		return err
	}
	edit(f)
	return tx.writeChunkFold(s, f)
}

// readChunkFold reads chunks.jsonl whole and folds it.
func (tx *Tx) readChunkFold() (*chunkFold, error) {
	lines, _, _, err := readLines(tx.files.chunks)
	if err != nil {
		return nil, err
	}
	return foldChunkLines(decodeLines(tx.files.chunks, lines, validChunkLine)), nil
}

// writeChunkFold replaces chunks.jsonl with f, atomically, and re-indexes the
// cached state from it.
func (tx *Tx) writeChunkFold(s *state, f *chunkFold) error {
	data, err := encodeLines(f.lines())
	if err != nil {
		return err
	}
	if err := tx.beginDestructive(); err != nil {
		return err
	}
	if err := writeFile(tx.files.chunks, data); err != nil {
		return err
	}
	s.indexChunks(f)
	return nil
}

// rewriteKG is rewriteChunks for the KG file.
func (tx *Tx) rewriteKG(s *state, edit func(*kgFold)) error {
	f, err := tx.readKGFold()
	if err != nil {
		return err
	}
	edit(f)
	return tx.writeKGFold(s, f)
}

// readKGFold reads the KG file whole and folds it.
func (tx *Tx) readKGFold() (*kgFold, error) {
	lines, _, _, err := readLines(tx.files.kg)
	if err != nil {
		return nil, err
	}
	return foldKGLines(decodeLines(tx.files.kg, lines, validKGLine)), nil
}

// writeKGFold replaces the KG file with f, atomically, and re-indexes the
// cached state from it.
func (tx *Tx) writeKGFold(s *state, f *kgFold) error {
	data, err := encodeLines(f.lines())
	if err != nil {
		return err
	}
	if err := tx.beginDestructive(); err != nil {
		return err
	}
	if err := writeFile(tx.files.kg, data); err != nil {
		return err
	}
	s.indexKG(f)
	return nil
}
