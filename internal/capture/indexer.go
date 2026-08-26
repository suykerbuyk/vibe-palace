// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/chunk"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/kg"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Indexer orchestrates transcript chunking, classification, embedding, and storage.
type Indexer struct {
	vault      *storage.Vault
	engine     *search.Engine
	embedder   embedder.Embedder
	config     storage.Config
	classifier *palace.RoomClassifier
}

// IndexStats reports per-call counts of newly-written artifacts.
// Counts exclude artifacts skipped due to dedup ("already exists").
// Callers that don't accumulate may discard with `_, err := ...`.
type IndexStats struct {
	Drawers  int // drawers appended via vault.AppendDrawer (excludes dedup skips)
	Entities int // entities added via vault.AddEntity (excludes dedup skips)
	Triples  int // triples added via vault.AddTriple (mentioned_in + relationship; excludes dedup skips)
}

// add accumulates another IndexStats into this one.
func (s *IndexStats) add(o IndexStats) {
	s.Drawers += o.Drawers
	s.Entities += o.Entities
	s.Triples += o.Triples
}

// NewIndexer creates a transcript indexer.
// Chunk settings are read from config (chunker.max_chars, chunker.overlap).
func NewIndexer(vault *storage.Vault, engine *search.Engine, emb embedder.Embedder, cfg storage.Config) *Indexer {
	return &Indexer{
		vault:      vault,
		engine:     engine,
		embedder:   emb,
		config:     cfg,
		classifier: palace.BuildClassifierFromConfig(cfg),
	}
}

// IndexTranscript chunks a raw transcript, classifies each chunk, embeds them
// in batch, stores drawers, indexes vectors, and optionally extracts entities.
// Returns IndexStats with per-call counts of newly-written artifacts; dedup
// skips do not increment.
func (idx *Indexer) IndexTranscript(ctx context.Context, sessionID, project, transcript string) (IndexStats, error) {
	var stats IndexStats

	if strings.TrimSpace(transcript) == "" {
		return stats, nil
	}

	chunks := chunk.Chunk(transcript, idx.chunkConfig())
	if len(chunks) == 0 {
		return stats, nil
	}

	wing := palace.DetectWing(project, "")
	now := time.Now().UTC().Format(time.RFC3339)

	// Build drawers and collect texts for batch embedding.
	type drawerLoc struct {
		drawer storage.Drawer
		room   string
	}
	locs := make([]drawerLoc, len(chunks))

	for i, chunk := range chunks {
		room := idx.classifier.Classify(chunk, "", idx.config.PalaceRoomKeywords)
		hall := palace.DetectHall(chunk)

		d := storage.Drawer{
			Hall:       hall,
			Content:    chunk,
			SourceType: "session",
			SourceRef:  sessionID,
			ChunkIndex: i,
			FiledAt:    now,
			AddedBy:    "capture",
		}
		// Pre-compute the ID the same way storage does, so we can use it
		// for vector indexing even before AppendDrawer fills it in.
		d.ID = storage.DrawerID(wing, chunk)

		locs[i] = drawerLoc{drawer: d, room: room}
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
	// anyway (IndexDrawers/AddEntity/AddTriple dedup-skip on unchanged
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

	// Best-effort entity extraction and KG population.
	stats.add(idx.extractEntities(project, sessionID, transcript, now))

	return stats, nil
}

// extractEntities runs the unified kg.ExtractAll pass and writes every
// deduplicated entity + relationship to the KG in a single loop. Returns
// per-call counts of newly-written entities and triples; dedup skips
// (errors containing "already exists") do not increment.
//
// KG writes are best-effort per PRD: failures do not propagate to the
// caller, but every failure is captured via slog.Warn so maintainers have
// a full audit trail. The originating extractor name is attached to each
// log entry (via the Source field on ExtractedEntityRef) so post-hoc log
// analysis can still tell which extractor produced a problematic write.
func (idx *Indexer) extractEntities(project, sessionID, transcript, timestamp string) IndexStats {
	var stats IndexStats

	today := ""
	if len(timestamp) >= 10 {
		today = timestamp[:10] // YYYY-MM-DD from RFC3339
	}

	result := kg.ExtractAll(transcript, today, kg.ExtractAllOptions{})

	for _, ent := range result.Entities {
		err := idx.vault.AddEntity(project, storage.Entity{
			ID:        slug.Slugify(ent.Type + "-" + ent.Name),
			Name:      ent.Name,
			Type:      ent.Type,
			CreatedAt: timestamp,
		})
		switch {
		case err == nil:
			stats.Entities++
		case strings.Contains(err.Error(), "already exists"):
			slog.Debug("kg: duplicate entity skipped",
				"entity", ent.Name, "source", ent.Source)
		default:
			slog.Warn("kg: add entity failed",
				"entity", ent.Name, "source", ent.Source, "err", err)
		}

		err = idx.vault.AddTriple(project, storage.Triple{
			Subject:       ent.Name,
			Predicate:     "mentioned_in",
			Object:        sessionID,
			SourceSession: sessionID,
			ExtractedAt:   timestamp,
			Confidence:    ent.Confidence,
		})
		switch {
		case err == nil:
			stats.Triples++
		case strings.Contains(err.Error(), "already exists"):
			slog.Debug("kg: duplicate mentioned_in triple skipped",
				"entity", ent.Name, "source", ent.Source)
		default:
			slog.Warn("kg: add mentioned_in triple failed",
				"entity", ent.Name, "source", ent.Source, "err", err)
		}
	}

	for _, tr := range result.Triples {
		err := idx.vault.AddTriple(project, storage.Triple{
			Subject:       tr.Subject,
			Predicate:     tr.Predicate,
			Object:        tr.Object,
			Confidence:    tr.Confidence,
			SourceSession: sessionID,
			ValidFrom:     tr.ValidFrom,
			ExtractedAt:   timestamp,
		})
		switch {
		case err == nil:
			stats.Triples++
		case strings.Contains(err.Error(), "already exists"):
			slog.Debug("kg: duplicate relationship triple skipped",
				"subject", tr.Subject, "predicate", tr.Predicate)
		default:
			slog.Warn("kg: add relationship triple failed",
				"subject", tr.Subject, "predicate", tr.Predicate, "err", err)
		}
	}

	return stats
}

// chunkConfig returns a ChunkConfig from the indexer's config, falling back to defaults.
func (idx *Indexer) chunkConfig() chunk.ChunkConfig {
	cfg := chunk.DefaultChunkConfig()
	if idx.config.ChunkMaxChars > 0 {
		cfg.MaxChars = idx.config.ChunkMaxChars
	}
	if idx.config.ChunkOverlap > 0 {
		cfg.Overlap = idx.config.ChunkOverlap
	}
	return cfg
}
