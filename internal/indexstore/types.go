// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// Owner kinds. Every owner is a typed key, never a path (ADR-014 decision 7,
// "Re-archived sessions: supersede").
const (
	// OwnerArchive is a transcript archive, keyed by its source_sha256. That
	// key is unique per archive's bytes, so a same-path re-archive is a
	// different owner, and it survives a project rename.
	OwnerArchive = "archive"
	// OwnerBatch is a mempalace import batch, keyed by its batch id.
	OwnerBatch = "batch"
	// OwnerNote is a session note owning its decision chunks, keyed by a
	// project-relative note key (decision-chunks-in-the-host-local-store).
	OwnerNote = "note"
)

// Owner is one source that owns a chunk or a KG record.
type Owner struct {
	Kind string `json:"kind"`
	SHA  string `json:"sha,omitempty"` // OwnerArchive
	ID   string `json:"id,omitempty"`  // OwnerBatch, OwnerNote
}

// ArchiveOwner returns the owner for the archive with this source_sha256.
func ArchiveOwner(sha string) Owner { return Owner{Kind: OwnerArchive, SHA: sha} }

// BatchOwner returns the owner for a mempalace import batch.
func BatchOwner(id string) Owner { return Owner{Kind: OwnerBatch, ID: id} }

// NoteOwner returns the owner for a session note.
func NoteOwner(key string) Owner { return Owner{Kind: OwnerNote, ID: key} }

// key is the owner's identity: equal keys are the same owner.
func (o Owner) key() string {
	if o.Kind == OwnerArchive {
		return o.Kind + ":" + o.SHA
	}
	return o.Kind + ":" + o.ID
}

func (o Owner) validate() error {
	switch o.Kind {
	case OwnerArchive:
		if o.SHA == "" || o.ID != "" {
			return fmt.Errorf("indexstore: archive owner needs a sha and no id: %+v", o)
		}
	case OwnerBatch, OwnerNote:
		if o.ID == "" || o.SHA != "" {
			return fmt.Errorf("indexstore: %s owner needs an id and no sha: %+v", o.Kind, o)
		}
	default:
		return fmt.Errorf("indexstore: unknown owner kind %q", o.Kind)
	}
	return nil
}

// Chunk is a chunk's content-derived record: equal for every owner, because
// the id is a hash of the content alone (index.ChunkID).
type Chunk struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Wing    string `json:"wing"`
	Room    string `json:"room"`
	Hall    string `json:"hall"`
}

// Ownership is the owner-derived part of a chunk: what one owner says about
// it. Day is stored only for a note owner; an archive or batch owner's day
// comes from the ledger, so a Day given for one is ignored.
type Ownership struct {
	SourceRef  string `json:"source_ref,omitempty"`
	SourceType string `json:"source_type,omitempty"`
	ChunkIndex int    `json:"chunk_index,omitempty"`
	AddedBy    string `json:"added_by,omitempty"`
	Day        string `json:"day,omitempty"`
}

// OwnedChunk is a chunk together with what its owner says about it: the unit
// every write takes.
type OwnedChunk struct {
	Chunk
	Ownership
}

// StoredChunk is a chunk as the store folds it: the content-derived fields,
// every owner, and the owner-derived fields taken from ONE owner, the live
// owner with the earliest day (ties: the smaller kind, then the smaller key).
// The fold runs at read time, so the result does not depend on ingest order,
// and a supersede that removes an owner recomputes it.
//
// Selected is nil, and the owner-derived fields are empty, when no owner is
// live.
type StoredChunk struct {
	Chunk
	Owners   []Owner
	Selected *Owner
	Ownership
	// FiledAt is the selected owner's day.
	FiledAt string
}

// KGRecord is one extracted KG record. Its format is
// authored-and-extracted-knowledge-graph-records'; the store keeps the payload
// opaque and owns only the id and the ownership.
type KGRecord struct {
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
}

// StoredKG is a KG record and every owner the store holds for it.
type StoredKG struct {
	KGRecord
	Owners []Owner
}

// Labels relabels a chunk (Tx.Rewrite). An empty field is left as it is.
type Labels struct {
	Wing string
	Room string
}

// VectorWriter writes one chunk's vector into the embed cache. It is injected
// because the embed cache lives in internal/search, which imports this
// package. The only production implementation is the writer
// (*search.EmbedCache).Writer returns for a held Tx: it writes each vector
// atomically (temp file, fsync, rename), checks the cache's embedding regime
// under the lock, and refuses once its Tx is finished. *search.EmbedCache
// itself does not satisfy this interface, so no vector is written outside the
// index commit lock.
type VectorWriter interface {
	Put(project, id string, vec []float32) error
}

// VectorFlusher is an optional VectorWriter extension: putVectors calls Flush
// once after a batch, so the writer can make the whole batch durable with one
// directory fsync instead of one per vector.
type VectorFlusher interface {
	Flush() error
}

// Day sources, recorded on ledger records.
const (
	DayFromTranscript = "transcript"
	DayFromCapturedAt = "captured_at"
	DayFromImport     = "import"
)

var dayPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func validateDay(day string) error {
	if !dayPattern.MatchString(day) {
		return fmt.Errorf("indexstore: day %q is not YYYY-MM-DD", day)
	}
	return nil
}
