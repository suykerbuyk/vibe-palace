// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/suykerbuyk/vibe-palace/internal/chunk"
	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/kg"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Indexing is how one project's content is chunked and classified into the
// host-local index, and the recipe that records it. A writer takes its
// classifier and chunk settings from the SAME value it records the recipe from,
// so chunks.fingerprint can never describe something the store does not hold.
type Indexing struct {
	// Recipe is what chunks.fingerprint records (indexstore.ReadFingerprint,
	// Tx.UseRecipe).
	Recipe index.ChunkRecipe
	// Classifier scores transcript chunks into rooms.
	Classifier *RoomClassifier
	// CustomRoomKeywords is the first tier Classify consults.
	CustomRoomKeywords map[string][]string
	// Transcript is the normalised effective chunk config for transcripts.
	Transcript chunk.ChunkConfig
}

// ProjectIndexing loads project's resolved config, v.LoadConfig(project), which
// includes the host-local project layer that can override palace.scoring per
// project, and builds the recipe, the classifier and the chunk settings from it.
// It takes the vault and slug rather than a storage.Config so no caller can pass
// another project's config, or the process-wide one.
func ProjectIndexing(v *storage.Vault, project string) (Indexing, error) {
	cfg, err := v.LoadConfig(project)
	if err != nil {
		return Indexing{}, fmt.Errorf("index recipe for %s: %w", project, err)
	}
	return indexingFromConfig(cfg), nil
}

// indexingFromConfig is ProjectIndexing over an already-resolved config.
func indexingFromConfig(cfg storage.Config) Indexing {
	transcript := chunk.DefaultChunkConfig()
	if cfg.ChunkMaxChars > 0 {
		transcript.MaxChars = cfg.ChunkMaxChars
	}
	if cfg.ChunkOverlap > 0 {
		transcript.Overlap = cfg.ChunkOverlap
	}
	transcript = transcript.Normalized()
	// The iteration and note chunkers read the compiled default and ignore
	// config (internal/search/iterations.go, notes.go).
	fixed := chunk.DefaultChunkConfig().Normalized()

	classifier := BuildClassifierFromConfig(cfg)
	return Indexing{
		Recipe: index.ChunkRecipe{
			IndexerVersion: index.IndexerVersion,
			Transcript: index.TranscriptRecipe{
				MaxChars:           transcript.MaxChars,
				Overlap:            transcript.Overlap,
				ClassifierDigest:   classifier.Digest(),
				CustomRoomKeywords: cfg.PalaceRoomKeywords,
				ExtractorVersion:   kg.ExtractorVersion,
			},
			Iteration: index.ChunkSettings{MaxChars: fixed.MaxChars, Overlap: fixed.Overlap},
			Note:      index.ChunkSettings{MaxChars: fixed.MaxChars, Overlap: fixed.Overlap},
		},
		Classifier:         classifier,
		CustomRoomKeywords: cfg.PalaceRoomKeywords,
		Transcript:         transcript,
	}
}

// Digest returns a stable hash of what the classifier scores with: every room
// in table order with each keyword's raw text and weight, and the minimum score.
// The table order is deterministic (NewRoomClassifier merges overrides in sorted
// key order). The filename rules are left out: the transcript writer classifies
// with an empty path, so they never reach stored content. The table itself stays
// unexported.
func (rc *RoomClassifier) Digest() string {
	entries, minScore := defaultRoomKeywords, float64(minRoomScore)
	if rc != nil && len(rc.entries) > 0 {
		entries, minScore = rc.entries, rc.minScore
	}
	h := sha256.New()
	fmt.Fprintf(h, "min_score=%s\n", strconv.FormatFloat(minScore, 'g', -1, 64))
	for _, e := range entries {
		fmt.Fprintf(h, "room %s\n", strconv.Quote(e.room))
		for _, kw := range e.keywords {
			fmt.Fprintf(h, "kw %s %s\n", strconv.Quote(kw.raw), strconv.FormatFloat(kw.weight, 'g', -1, 64))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
