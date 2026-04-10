// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"crypto/md5"
	"encoding/hex"
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
	vault    *storage.Vault
	engine   *search.Engine
	embedder embedder.Embedder
	config   storage.Config
}

// NewIndexer creates a transcript indexer.
// Chunk settings are read from config (chunker.max_chars, chunker.overlap).
func NewIndexer(vault *storage.Vault, engine *search.Engine, emb embedder.Embedder, cfg storage.Config) *Indexer {
	return &Indexer{
		vault:    vault,
		engine:   engine,
		embedder: emb,
		config:   cfg,
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
		room := palace.DetectRoom(chunk, "", idx.config.PalaceRoomKeywords)
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
		d.ID = drawerID(wing, room, chunk)

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

	// Batch embed all chunks.
	if idx.embedder != nil && idx.engine != nil {
		vecs, err := idx.embedder.EmbedBatch(ctx, chunks)
		if err != nil {
			return fmt.Errorf("batch embed: %w", err)
		}

		for i, loc := range locs {
			if err := idx.engine.IndexDrawerWithVec(project, wing, loc.room, loc.drawer, vecs[i]); err != nil {
				return fmt.Errorf("index drawer %d: %w", i, err)
			}
		}
	}

	// Best-effort entity extraction and KG population.
	idx.extractEntities(project, sessionID, transcript, now)

	return nil
}

// extractEntities runs regex-based entity extraction and writes to the KG.
// All errors are silently ignored (best-effort per PRD).
func (idx *Indexer) extractEntities(project, sessionID, transcript, timestamp string) {
	entities := ExtractEntities(transcript)
	for _, ent := range entities {
		if err := idx.vault.AddEntity(project, storage.Entity{
			ID:        slug.Slugify(ent.Type + "-" + ent.Name),
			Name:      ent.Name,
			Type:      ent.Type,
			CreatedAt: timestamp,
		}); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				slog.Debug("entity extraction: duplicate entity skipped", "entity", ent.Name)
			} else {
				slog.Warn("entity extraction: add entity failed", "entity", ent.Name, "err", err)
			}
		}
		if err := idx.vault.AddTriple(project, storage.Triple{
			Subject:       ent.Name,
			Predicate:     "mentioned_in",
			Object:        sessionID,
			SourceSession: sessionID,
			ExtractedAt:   timestamp,
			Confidence:    0.8,
		}); err != nil {
			slog.Warn("entity extraction: add triple failed", "entity", ent.Name, "err", err)
		}
	}

	// Phase 7: person/project/concept/tool detection.
	detected := kg.DetectEntities(transcript)
	for _, d := range detected {
		if err := idx.vault.AddEntity(project, storage.Entity{
			ID:        slug.Slugify(string(d.Type) + "-" + d.Name),
			Name:      d.Name,
			Type:      string(d.Type),
			CreatedAt: timestamp,
		}); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				slog.Debug("kg: duplicate entity skipped", "entity", d.Name)
			} else {
				slog.Warn("kg: add detected entity failed", "entity", d.Name, "err", err)
			}
		}
		if err := idx.vault.AddTriple(project, storage.Triple{
			Subject:       d.Name,
			Predicate:     "mentioned_in",
			Object:        sessionID,
			SourceSession: sessionID,
			ExtractedAt:   timestamp,
			Confidence:    d.Confidence,
		}); err != nil {
			slog.Warn("kg: add mentioned_in triple failed", "entity", d.Name, "err", err)
		}
	}

	// Phase 7: relationship extraction.
	today := timestamp[:10] // extract YYYY-MM-DD from RFC3339
	triples := kg.ExtractTriples(transcript, detected, today)
	for _, tr := range triples {
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

// drawerID mirrors storage.drawerID: first 8 hex chars of MD5(wing+room+content).
func drawerID(wing, room, content string) string {
	h := md5.Sum([]byte(wing + room + content))
	return hex.EncodeToString(h[:])[:8]
}
