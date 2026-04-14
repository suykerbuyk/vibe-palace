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
	"github.com/suykerbuyk/vibe-palace/internal/kg"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/search"
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
func (idx *Indexer) IndexTranscript(ctx context.Context, sessionID, project, transcript string) error {
	if strings.TrimSpace(transcript) == "" {
		return nil
	}

	chunks := Chunk(transcript, idx.chunkConfig())
	if len(chunks) == 0 {
		return nil
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

	// Store drawers in the vault (JSONL).
	for i := range locs {
		err := idx.vault.AppendDrawer(project, wing, locs[i].room, locs[i].drawer)
		if err != nil {
			// Duplicate drawers are expected on re-index — skip silently.
			if strings.Contains(err.Error(), "already exists") {
				continue
			}
			return fmt.Errorf("append drawer %d: %w", i, err)
		}
	}

	// Batch embed all chunks and index them in a single engine call.
	if idx.embedder != nil && idx.engine != nil {
		vecs, err := idx.embedder.EmbedBatch(ctx, chunks)
		if err != nil {
			return fmt.Errorf("batch embed: %w", err)
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
			return fmt.Errorf("index drawers: %w", err)
		}
	}

	// Best-effort entity extraction and KG population.
	idx.extractEntities(project, sessionID, transcript, now)

	return nil
}

// extractEntities runs the unified kg.ExtractAll pass and writes every
// deduplicated entity + relationship to the KG in a single loop.
//
// KG writes are best-effort per PRD: failures do not propagate to the
// caller, but every failure is captured via slog.Warn so maintainers have
// a full audit trail. The originating extractor name is attached to each
// log entry (via the Source field on ExtractedEntityRef) so post-hoc log
// analysis can still tell which extractor produced a problematic write.
func (idx *Indexer) extractEntities(project, sessionID, transcript, timestamp string) {
	today := ""
	if len(timestamp) >= 10 {
		today = timestamp[:10] // YYYY-MM-DD from RFC3339
	}

	result := kg.ExtractAll(transcript, today, kg.ExtractAllOptions{})

	for _, ent := range result.Entities {
		if err := idx.vault.AddEntity(project, storage.Entity{
			ID:        slug.Slugify(ent.Type + "-" + ent.Name),
			Name:      ent.Name,
			Type:      ent.Type,
			CreatedAt: timestamp,
		}); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				slog.Debug("kg: duplicate entity skipped",
					"entity", ent.Name, "source", ent.Source)
			} else {
				slog.Warn("kg: add entity failed",
					"entity", ent.Name, "source", ent.Source, "err", err)
			}
		}
		if err := idx.vault.AddTriple(project, storage.Triple{
			Subject:       ent.Name,
			Predicate:     "mentioned_in",
			Object:        sessionID,
			SourceSession: sessionID,
			ExtractedAt:   timestamp,
			Confidence:    ent.Confidence,
		}); err != nil {
			slog.Warn("kg: add mentioned_in triple failed",
				"entity", ent.Name, "source", ent.Source, "err", err)
		}
	}

	for _, tr := range result.Triples {
		if err := idx.vault.AddTriple(project, storage.Triple{
			Subject:       tr.Subject,
			Predicate:     tr.Predicate,
			Object:        tr.Object,
			Confidence:    tr.Confidence,
			SourceSession: sessionID,
			ValidFrom:     tr.ValidFrom,
			ExtractedAt:   timestamp,
		}); err != nil {
			slog.Warn("kg: add relationship triple failed",
				"subject", tr.Subject, "predicate", tr.Predicate, "err", err)
		}
	}
}

// chunkConfig returns a ChunkConfig from the indexer's config, falling back to defaults.
func (idx *Indexer) chunkConfig() ChunkConfig {
	cfg := DefaultChunkConfig()
	if idx.config.ChunkMaxChars > 0 {
		cfg.MaxChars = idx.config.ChunkMaxChars
	}
	if idx.config.ChunkOverlap > 0 {
		cfg.Overlap = idx.config.ChunkOverlap
	}
	return cfg
}

