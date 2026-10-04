// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Indexer orchestrates transcript chunking, classification, embedding, and storage.
type Indexer struct {
	vault    *storage.Vault
	engine   *search.Engine
	embedder embedder.Embedder
	config   storage.Config
}

// IndexStats reports per-call counts of newly-written artifacts.
// Counts exclude artifacts skipped due to dedup: the batch writers report the
// skips as the gap between the batch size and the returned count, and
// AddTriple reports them as an error containing "already exists".
// Callers that don't accumulate may discard with `_, err := ...`.
type IndexStats struct {
	Drawers  int // drawers appended via vault.AppendDrawer (excludes dedup skips)
	Entities int // entities added via vault.AddEntities (excludes dedup skips)
	Triples  int // triples added via vault.AddTriple (mentioned_in + relationship; excludes dedup skips)
}

// add accumulates another IndexStats into this one.
func (s *IndexStats) add(o IndexStats) {
	s.Drawers += o.Drawers
	s.Entities += o.Entities
	s.Triples += o.Triples
}

// NewIndexer creates a transcript indexer. Chunking and classification follow
// the project's own recipe (palace.ProjectIndexing), read per call, never cfg:
// the chunks it writes and the recipe chunks.fingerprint records must agree.
func NewIndexer(vault *storage.Vault, engine *search.Engine, emb embedder.Embedder, cfg storage.Config) *Indexer {
	return &Indexer{
		vault:    vault,
		engine:   engine,
		embedder: emb,
		config:   cfg,
	}
}

// indexerNow is IndexTranscript's one clock read, a seam so the golden
// fixture of its output (internal/palace/testdata/prepare_golden.json) can be
// captured with a pinned date.
var indexerNow = time.Now

// IndexTranscript chunks a raw transcript, classifies each chunk, embeds them
// in batch, stores drawers, indexes vectors, and optionally extracts entities.
// Returns IndexStats with per-call counts of newly-written artifacts; dedup
// skips do not increment.
func (idx *Indexer) IndexTranscript(ctx context.Context, sessionID, project, transcript string) (IndexStats, error) {
	var stats IndexStats

	if strings.TrimSpace(transcript) == "" {
		return stats, nil
	}

	// The write-free prepare step, with the project's own recipe (chunk
	// settings, classifier and room keywords from palace.ProjectIndexing, the
	// one assembler), and the indexer's clock as the date of every record.
	ix, err := palace.ProjectIndexing(idx.vault, project)
	if err != nil {
		return stats, err
	}
	prep, err := palace.Prepare(ix, palace.PrepareInput{
		Project:   project,
		SourceRef: sessionID,
		Date:      indexerNow(),
		Text:      transcript,
	})
	if err != nil {
		return stats, err
	}
	if len(prep.Chunks) == 0 {
		return stats, nil
	}

	wing := prep.Chunks[0].Wing
	chunks := make([]string, len(prep.Chunks))

	// Build drawers and collect texts for batch embedding.
	type drawerLoc struct {
		drawer storage.Drawer
		room   string
	}
	locs := make([]drawerLoc, len(prep.Chunks))

	for i, pc := range prep.Chunks {
		chunks[i] = pc.Content
		d := storage.Drawer{
			Hall:       pc.Hall,
			Content:    pc.Content,
			SourceType: "session",
			SourceRef:  pc.SourceRef,
			ChunkIndex: pc.ChunkIndex,
			FiledAt:    pc.FiledAt,
			AddedBy:    "capture",
		}
		// Pre-compute the ID the same way storage does, so we can use it
		// for vector indexing even before AppendDrawer fills it in. Tracked
		// drawers keep the 32-bit drawer id; the host-local store uses
		// pc.ID (index.ChunkID).
		d.ID = storage.DrawerID(pc.Wing, pc.Content)

		locs[i] = drawerLoc{drawer: d, room: pc.Room}
	}

	// Store drawers in the vault (JSONL), ONE BATCH PER ROOM.
	//
	// 🔴 NOT a per-chunk loop over AppendDrawer, and that is the point of this
	// shape. AppendDrawer reads and scans the entire room file to check for a
	// duplicate ID, so calling it per chunk costs O(chunks × drawers already in
	// the room) — MEASURED at 33 ms per chunk against this project's 19 MB
	// `general` room, which is why a backfill of the archives never finished.
	// Grouping first turns one archive into at most one read and one append per
	// room it touches. Duplicates stay a SILENT SKIP, exactly as they were when
	// this loop matched on "already exists": AppendDrawers reports the count it
	// actually appended rather than erroring, so re-indexing an archive that is
	// already filed is a no-op that writes nothing.
	roomOrder := make([]string, 0, len(locs))
	byRoom := make(map[string][]storage.Drawer, len(locs))
	for i := range locs {
		room := locs[i].room
		if _, ok := byRoom[room]; !ok {
			roomOrder = append(roomOrder, room)
		}
		byRoom[room] = append(byRoom[room], locs[i].drawer)
	}
	for _, room := range roomOrder {
		n, err := idx.vault.AppendDrawers(project, wing, room, byRoom[room])
		if err != nil {
			return stats, fmt.Errorf("append drawers to %s: %w", room, err)
		}
		stats.Drawers += n
	}

	// Every chunk in this call already existed: there is nothing new to embed
	// or extract entities from, and EmbedBatch's output would be discarded
	// anyway (IndexDrawers/AddEntities/AddTriple dedup-skip on unchanged
	// content). Skipping here is what stops a re-index of an already-indexed
	// archive from paying full embedder + KG-extraction cost for zero result.
	if stats.Drawers == 0 {
		return stats, nil
	}

	// Batch embed all chunks and index them in a single engine call.
	if idx.embedder != nil && idx.engine != nil {
		vecs, err := idx.embedder.EmbedBatch(ctx, chunks)
		if err != nil {
			return stats, fmt.Errorf("batch embed: %w", err)
		}
		if len(vecs) != len(chunks) {
			return stats, fmt.Errorf("batch embed: got %d vecs for %d chunks", len(vecs), len(chunks))
		}

		batch := make([]search.DrawerInput, len(locs))
		for i, loc := range locs {
			batch[i] = search.DrawerInput{
				Project: project,
				Wing:    wing,
				Room:    loc.room,
				Drawer:  loc.drawer,
				Vec:     vecs[i],
			}
		}
		if err := idx.engine.IndexDrawers(ctx, batch); err != nil {
			return stats, fmt.Errorf("index drawers: %w", err)
		}
	}

	// Best-effort KG population from the prepared entities and triples.
	stats.add(idx.writeKG(project, prep))

	return stats, nil
}

// writeKG writes the prepared entities and triples to the tracked KG.
// Entities go out in ONE AddEntities batch; triples still go one AddTriple at
// a time. Returns per-call counts of newly-written entities and triples; dedup
// skips do not increment — the batch reports them as a count, and AddTriple
// reports them as an error containing "already exists".
//
// KG writes are best-effort per PRD: failures do not propagate to the
// caller, but every failure is captured via slog.Warn so maintainers have
// a full audit trail.
func (idx *Indexer) writeKG(project string, prep palace.Prepared) IndexStats {
	var stats IndexStats

	// Entities are written in ONE batch, hoisted out of a per-entity loop.
	// AddEntity reads and scans the whole entities file to dedup, so calling
	// it once per extracted entity cost O(entities in the graph) per entity —
	// the quadratic term this batch removes. The triples still go one at a
	// time because AddTriple is a different shape: one JSON file per triple,
	// deduped by path collision, with no whole-file scan to amortize.
	ents := make([]storage.Entity, 0, len(prep.Entities))
	for _, ent := range prep.Entities {
		ents = append(ents, storage.Entity{
			ID:        ent.ID,
			Name:      ent.Name,
			Type:      ent.Type,
			CreatedAt: ent.CreatedAt,
			Origin:    storage.OriginExtracted,
		})
	}
	added, err := idx.vault.AddEntities(project, ents)
	if err != nil {
		slog.Warn("kg: add entities failed", "project", project,
			"batch", len(ents), "err", err)
	} else {
		// stats.Entities is documented as excluding dedup skips, and the batch
		// count is exactly the newly-written ones.
		stats.Entities += added
		if skipped := len(ents) - added; skipped > 0 {
			slog.Debug("kg: duplicate entities skipped",
				"project", project, "skipped", skipped, "batch", len(ents))
		}
	}

	for _, tr := range prep.Triples {
		err := idx.vault.AddTriple(project, storage.Triple{
			Subject:       tr.Subject,
			Predicate:     tr.Predicate,
			Object:        tr.Object,
			Confidence:    tr.Confidence,
			SourceSession: tr.SourceSession,
			ValidFrom:     tr.ValidFrom,
			ExtractedAt:   tr.ExtractedAt,
			Origin:        storage.OriginExtracted,
		})
		switch {
		case err == nil:
			stats.Triples++
		case strings.Contains(err.Error(), "already exists"):
			slog.Debug("kg: duplicate triple skipped",
				"subject", tr.Subject, "predicate", tr.Predicate)
		default:
			slog.Warn("kg: add triple failed",
				"subject", tr.Subject, "predicate", tr.Predicate, "err", err)
		}
	}

	return stats
}
