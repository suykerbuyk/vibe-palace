// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/chunk"
	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/kg"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
)

// PrepareInput is one source text to prepare: the project it belongs to, the
// reference its chunks and triples cite (a session id), the date its records
// carry, and the text.
type PrepareInput struct {
	Project   string
	SourceRef string
	Date      time.Time
	Text      string
}

// PreparedChunk is one chunk of a prepared text, classified.
type PreparedChunk struct {
	// ID is index.ChunkID(Content): a hash of the content alone, the id the
	// host-local store uses. Writers of tracked drawers derive their own
	// 32-bit storage.DrawerID from the wing and content.
	ID         string
	Wing       string
	Room       string
	Hall       string
	Content    string
	SourceRef  string
	ChunkIndex int
	FiledAt    string // the input date, RFC3339 UTC
}

// PreparedEntity is one extracted entity.
type PreparedEntity struct {
	ID         string // slug of "<type>-<name>"
	Name       string
	Type       string
	CreatedAt  string // the input date, RFC3339 UTC
	Confidence float64
}

// PreparedTriple is one extracted triple: a mentioned_in triple per entity
// (object = the source reference) and every relationship the extractor found.
type PreparedTriple struct {
	Subject       string
	Predicate     string
	Object        string
	ValidFrom     string // temporal relationships only, YYYY-MM-DD from the input date
	ExtractedAt   string // the input date, RFC3339 UTC
	SourceSession string
	Confidence    float64
}

// Prepared is what Prepare derives from one text.
type Prepared struct {
	Chunks   []PreparedChunk
	Entities []PreparedEntity
	Triples  []PreparedTriple
}

// Prepare is the write-free prepare step every index writer shares (task
// importers-write-the-frozen-tracked-corpus, Scope 1): it chunks the text with
// the project's transcript chunk settings, classifies each chunk into a room
// with the project's classifier and room keywords, detects its hall, and runs
// the knowledge-graph extraction. Every date it emits is in.Date, never the
// clock. It performs no I/O and embeds nothing.
//
// ix must come from ProjectIndexing, the one assembler, so the chunks a
// writer stores and the recipe it records (chunks.fingerprint) cannot differ.
func Prepare(ix Indexing, in PrepareInput) (Prepared, error) {
	var p Prepared
	if strings.TrimSpace(in.Text) == "" {
		return p, nil
	}
	date := in.Date.UTC().Format(time.RFC3339)
	wing := DetectWing(in.Project, "")
	for i, c := range chunk.Chunk(in.Text, ix.Transcript) {
		p.Chunks = append(p.Chunks, PreparedChunk{
			ID:         index.ChunkID(c),
			Wing:       wing,
			Room:       ix.Classifier.Classify(c, "", ix.CustomRoomKeywords),
			Hall:       DetectHall(c),
			Content:    c,
			SourceRef:  in.SourceRef,
			ChunkIndex: i,
			FiledAt:    date,
		})
	}

	result := kg.ExtractAll(in.Text, date[:10], kg.ExtractAllOptions{})
	for _, ent := range result.Entities {
		p.Entities = append(p.Entities, PreparedEntity{
			ID:         slug.Slugify(ent.Type + "-" + ent.Name),
			Name:       ent.Name,
			Type:       ent.Type,
			CreatedAt:  date,
			Confidence: ent.Confidence,
		})
		p.Triples = append(p.Triples, PreparedTriple{
			Subject:       ent.Name,
			Predicate:     "mentioned_in",
			Object:        in.SourceRef,
			ExtractedAt:   date,
			SourceSession: in.SourceRef,
			Confidence:    ent.Confidence,
		})
	}
	for _, tr := range result.Triples {
		p.Triples = append(p.Triples, PreparedTriple{
			Subject:       tr.Subject,
			Predicate:     tr.Predicate,
			Object:        tr.Object,
			ValidFrom:     tr.ValidFrom,
			ExtractedAt:   date,
			SourceSession: in.SourceRef,
			Confidence:    tr.Confidence,
		})
	}
	return p, nil
}
