// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"slices"
)

// Ledger record kinds, one JSON line each in index/<p>/ledger.jsonl (ADR-014
// decision 2 and decision 7).
const (
	recBaseline      = "baseline"       // always the first line: the backlog set
	recBaselineAdd   = "baseline_add"   // archives copy, merge or import added to the set
	recBaselineClear = "baseline_clear" // a completed rebuild emptied the set
	recSession       = "session"        // a session's archive: live, or superseding
	recSuperseded    = "superseded"     // an older archive recorded but never ingested
	recBatch         = "batch"          // a mempalace import batch
	recFailure       = "failure"        // an archive's failure count
	recFailureClear  = "failure_clear"  // a completed rebuild cleared failure counts
)

// Session states.
const (
	StateLive        = "live"
	StateSuperseding = "superseding"
)

// ledgerRecord is one ledger line; which fields are set depends on Kind.
type ledgerRecord struct {
	Kind            string   `json:"kind"`
	Archives        []string `json:"archives,omitempty"`
	CreatedAt       string   `json:"created_at,omitempty"`
	Keep            []string `json:"keep,omitempty"`
	SessionID       string   `json:"session_id,omitempty"`
	State           string   `json:"state,omitempty"`
	SHA             string   `json:"source_sha256,omitempty"`
	SupersedingFrom string   `json:"superseding_from,omitempty"`
	ArchivePath     string   `json:"archive_path,omitempty"`
	StartDay        string   `json:"start_day,omitempty"`
	StartDaySource  string   `json:"start_day_source,omitempty"`
	ChunkCount      *int     `json:"chunk_count,omitempty"`
	Generation      int      `json:"generation,omitempty"`
	BatchID         string   `json:"batch_id,omitempty"`
	Count           int      `json:"count,omitempty"`
	LastError       string   `json:"last_error,omitempty"`
}

func validLedgerRecord(r *ledgerRecord) bool {
	switch r.Kind {
	case recBaseline, recBaselineAdd, recBaselineClear, recFailureClear:
		return true
	case recSession:
		return r.SessionID != "" && r.SHA != "" && (r.State == StateLive || r.State == StateSuperseding)
	case recSuperseded:
		return r.SessionID != "" && r.SHA != ""
	case recBatch:
		return r.BatchID != ""
	case recFailure:
		return r.SHA != ""
	}
	return false
}

// SessionRecord is the latest ledger record for one session.
type SessionRecord struct {
	SessionID       string
	State           string // StateLive or StateSuperseding
	SHA             string // the live archive, or the one being superseded TO
	SupersedingFrom string // in StateSuperseding: the archive being replaced
	ArchivePath     string // for display only; nothing opens an archive through it
	StartDay        string
	StartDaySource  string
	ChunkCount      int
	Generation      int // 1 when first ledgered, +1 per supersede
}

type batchRecord struct {
	chunkCount int
	startDay   string
}

// Ledger is the folded ingest ledger of one project.
//
// The fold rule (ADR-014 decision 7):
//   - the latest session record per session wins, and a session is ledgered
//     when that record is live;
//   - a failure record is never an ingest: it never makes a session ledgered,
//     and never makes its records visible;
//   - an archive's failure count is its latest failure record after the last
//     failure_clear that does not keep it.
type Ledger struct {
	exists     bool
	baseline   map[string]struct{}
	sessions   map[string]SessionRecord
	superseded map[string]struct{} // archives replaced or recorded superseded
	liveDays   map[string]string   // live archive sha -> its session's start day
	liveSess   map[string]string   // live archive sha -> its session id
	batches    map[string]batchRecord
	failures   map[string]int
}

func foldLedger(recs []ledgerRecord) *Ledger {
	l := &Ledger{
		exists:     len(recs) > 0,
		baseline:   map[string]struct{}{},
		sessions:   map[string]SessionRecord{},
		superseded: map[string]struct{}{},
		liveDays:   map[string]string{},
		liveSess:   map[string]string{},
		batches:    map[string]batchRecord{},
		failures:   map[string]int{},
	}
	for _, r := range recs {
		switch r.Kind {
		case recBaseline, recBaselineAdd:
			for _, s := range r.Archives {
				l.baseline[s] = struct{}{}
			}
		case recBaselineClear:
			l.baseline = map[string]struct{}{}
		case recSession:
			n := 0
			if r.ChunkCount != nil {
				n = *r.ChunkCount
			}
			l.sessions[r.SessionID] = SessionRecord{
				SessionID: r.SessionID, State: r.State, SHA: r.SHA, SupersedingFrom: r.SupersedingFrom,
				ArchivePath: r.ArchivePath, StartDay: r.StartDay, StartDaySource: r.StartDaySource,
				ChunkCount: n, Generation: r.Generation,
			}
			if r.SupersedingFrom != "" {
				l.superseded[r.SupersedingFrom] = struct{}{}
			}
		case recSuperseded:
			l.superseded[r.SHA] = struct{}{}
		case recBatch:
			n := 0
			if r.ChunkCount != nil {
				n = *r.ChunkCount
			}
			l.batches[r.BatchID] = batchRecord{chunkCount: n, startDay: r.StartDay}
		case recFailure:
			l.failures[r.SHA] = r.Count
		case recFailureClear:
			for sha := range l.failures {
				if !slices.Contains(r.Keep, sha) {
					delete(l.failures, sha)
				}
			}
		}
	}
	for _, s := range l.sessions {
		if s.State == StateLive {
			l.liveDays[s.SHA] = s.StartDay
			l.liveSess[s.SHA] = s.SessionID
		}
	}
	return l
}

// Exists reports whether the project has a ledger on this host.
func (l *Ledger) Exists() bool { return l.exists }

// Session returns a session's latest record.
func (l *Ledger) Session(id string) (SessionRecord, bool) {
	s, ok := l.sessions[id]
	return s, ok
}

// StartDay returns the UTC start day of a ledgered session (from its latest
// record, which must be live) or of a ledgered import batch. A batch id is
// never a session id (CommitBatch refuses one). ok is false otherwise.
// decision-chunks-in-the-host-local-store dates decision chunks from it, and
// never opens an archive.
func (l *Ledger) StartDay(id string) (day string, ok bool) {
	if s, found := l.sessions[id]; found {
		if s.State == StateLive {
			return s.StartDay, true
		}
		return "", false
	}
	if b, found := l.batches[id]; found {
		return b.startDay, true
	}
	return "", false
}

// InBaseline reports whether an archive is in the baseline set.
func (l *Ledger) InBaseline(sha string) bool {
	_, ok := l.baseline[sha]
	return ok
}

// FailureCount is an archive's failure count.
func (l *Ledger) FailureCount(sha string) int { return l.failures[sha] }

// ArchiveRef names one tracked archive.
type ArchiveRef struct {
	SessionID string
	SHA       string // source_sha256
	Path      string
}

// Pending returns the archives, among those given, that still need an ingest:
//   - its session has no record;
//   - its session's latest record is live with a different source_sha256 (a
//     re-archive, which ING supersedes);
//   - its session is superseding to it (a supersede a crash interrupted).
//
// An archive the ledger records as replaced, by a supersede or as superseded
// on arrival, is never pending. Failure counts and the baseline set do not
// change what is pending: the ingester applies them.
//
// A ledger that does not exist (!Exists) has no baseline set yet, so every
// archive of the project reads as pending, the whole history included. Such a
// snapshot must never drive an ingest: the ingester computes pending under the
// commit lock, after Tx.EnsureLedger has created the ledger and its baseline.
func (l *Ledger) Pending(archives []ArchiveRef) []ArchiveRef {
	var out []ArchiveRef
	for _, a := range archives {
		if _, gone := l.superseded[a.SHA]; gone {
			continue
		}
		s, ok := l.sessions[a.SessionID]
		switch {
		case !ok:
			out = append(out, a)
		case s.State == StateSuperseding && s.SHA == a.SHA:
			out = append(out, a)
		case s.State == StateLive && s.SHA != a.SHA:
			out = append(out, a)
		}
	}
	return out
}

// LiveOwner reports a KG or chunk owner's session and start day when the owner
// is live, for a reader that derives a shared record's date and session from
// its earliest live owner (authored-and-extracted-knowledge-graph-records).
//
//   - An archive owner is live when some session's latest record is live with
//     that source_sha256: its session id and start day.
//   - A batch owner is live when its batch is ledgered: no session, and the
//     batch's start day.
//   - Anything else (an unledgered or superseded archive, a session
//     mid-supersede, a note owner) is not: ok is false.
func (l *Ledger) LiveOwner(o Owner) (sessionID, day string, ok bool) {
	switch o.Kind {
	case OwnerArchive:
		d, live := l.liveDays[o.SHA]
		if !live {
			return "", "", false
		}
		return l.liveSess[o.SHA], d, true
	case OwnerBatch:
		b, ledgered := l.batches[o.ID]
		if !ledgered {
			return "", "", false
		}
		return "", b.startDay, true
	}
	return "", "", false
}

// liveDay is the day of an owner that is live: an archive that is a session's
// live archive, a ledgered batch, or a note (whose day the caller supplies).
func (l *Ledger) liveDay(o Owner, noteDay string) (string, bool) {
	switch o.Kind {
	case OwnerArchive:
		d, ok := l.liveDays[o.SHA]
		return d, ok
	case OwnerBatch:
		b, ok := l.batches[o.ID]
		return b.startDay, ok
	case OwnerNote:
		return noteDay, true
	}
	return "", false
}
