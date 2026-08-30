// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// wingMapping maps known MemPalace wing names to their vault equivalents.
var wingMapping = map[string]string{
	"emotions":      "emotions",
	"consciousness": "consciousness",
	"memory":        "memory",
	"technical":     "technical",
	"identity":      "identity",
	"family":        "family",
	"creative":      "creative",
}

// memPalaceExport is the top-level JSON structure produced by the Python
// export script.
type memPalaceExport struct {
	ExportedAt string            `json:"exported_at"`
	Drawers    []memPalaceDrawer `json:"drawers"`
	Entities   []memPalaceEntity `json:"entities"`
	Triples    []memPalaceTriple `json:"triples"`
}

type memPalaceDrawer struct {
	ID         string `json:"id"`
	Wing       string `json:"wing"`
	Room       string `json:"room"`
	Content    string `json:"content"`
	SourceFile string `json:"source_file"`
	ChunkIndex int    `json:"chunk_index"`
	AddedBy    string `json:"added_by"`
	FiledAt    string `json:"filed_at"`
}

type memPalaceEntity struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Properties map[string]string `json:"properties"`
	CreatedAt  string            `json:"created_at"`
}

type memPalaceTriple struct {
	Subject       string  `json:"subject"`
	Predicate     string  `json:"predicate"`
	Object        string  `json:"object"`
	ValidFrom     string  `json:"valid_from"`
	ValidTo       *string `json:"valid_to"`
	Confidence    float64 `json:"confidence"`
	SourceSession string  `json:"source_session"`
}

const embedBatchSize = 32

// ImportMemPalace reads a JSON export file produced by the MemPalace Python
// export script and imports its drawers, entities, and triples into the vault
// under the project "mempalace".
func ImportMemPalace(
	ctx context.Context,
	vault *storage.Vault,
	engine *search.Engine,
	emb embedder.Embedder,
	exportPath string,
	opts ImportOptions,
) (ImportResult, error) {
	var result ImportResult

	// Step 1: read and unmarshal the export file.
	data, err := os.ReadFile(exportPath)
	if err != nil {
		return result, fmt.Errorf("read export file: %w", err)
	}

	var export memPalaceExport
	if err := json.Unmarshal(data, &export); err != nil {
		return result, fmt.Errorf("parse export JSON: %w", err)
	}

	// Step 2: collect non-empty drawer contents for batch embedding.
	type drawerWork struct {
		index int
		wing  string
		room  string
	}

	var texts []string
	var work []drawerWork
	for i, d := range export.Drawers {
		content := strings.TrimSpace(d.Content)
		if content == "" {
			continue
		}
		texts = append(texts, content)
		wing := mapWing(d.Wing)
		room := d.Room
		if room == "" {
			room = "general"
		}
		work = append(work, drawerWork{index: i, wing: wing, room: room})
	}

	// Step 3: batch embed.
	var allVecs [][]float32
	if len(texts) > 0 && emb != nil {
		for start := 0; start < len(texts); start += embedBatchSize {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			end := min(start+embedBatchSize, len(texts))
			batch, embErr := emb.EmbedBatch(ctx, texts[start:end])
			if embErr != nil {
				return result, fmt.Errorf("embed batch: %w", embErr)
			}
			allVecs = append(allVecs, batch...)
		}
	}

	// Step 4: import drawers.
	for wi, w := range work {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		d := export.Drawers[w.index]
		hall := palace.DetectHall(d.Content)

		sd := storage.Drawer{
			ID:         d.ID,
			Hall:       hall,
			Content:    d.Content,
			SourceType: "mempalace",
			SourceRef:  d.ID,
			ChunkIndex: d.ChunkIndex,
			FiledAt:    d.FiledAt,
			AddedBy:    d.AddedBy,
		}

		if opts.DryRun {
			result.DrawersCreated++
			progress(opts, ProgressEvent{
				Type:    ProgressSessionDone,
				Project: "mempalace",
				Message: fmt.Sprintf("dry-run drawer %s", d.ID),
				Current: wi + 1,
				Total:   len(work),
			})
			continue
		}

		appendErr := vault.AppendDrawer("mempalace", w.wing, w.room, sd)
		if appendErr != nil {
			if strings.Contains(appendErr.Error(), "already exists") {
				progress(opts, ProgressEvent{
					Type:    ProgressSessionSkip,
					Project: "mempalace",
					Message: fmt.Sprintf("drawer %s already exists", d.ID),
					Current: wi + 1,
					Total:   len(work),
				})
				continue
			}
			result.Errors = append(result.Errors, ImportError{
				Project: "mempalace",
				File:    exportPath,
				Err:     appendErr,
			})
			progress(opts, ProgressEvent{
				Type:    ProgressError,
				Project: "mempalace",
				Message: appendErr.Error(),
			})
			continue
		}

		// Index with vector if engine is available.
		if engine != nil && wi < len(allVecs) {
			if idxErr := engine.IndexDrawers(ctx, []search.DrawerInput{{
				Project: "mempalace",
				Wing:    w.wing,
				Room:    w.room,
				Drawer:  sd,
				Vec:     allVecs[wi],
			}}); idxErr != nil {
				result.Errors = append(result.Errors, ImportError{
					Project: "mempalace",
					File:    exportPath,
					Err:     idxErr,
				})
			}
		}

		result.DrawersCreated++
		progress(opts, ProgressEvent{
			Type:    ProgressSessionDone,
			Project: "mempalace",
			Message: fmt.Sprintf("imported drawer %s", d.ID),
			Current: wi + 1,
			Total:   len(work),
		})
	}

	// Step 5: import entities, in ONE batch.
	//
	// This is an unbounded bulk import, and the per-entity AddEntity reads and
	// scans the whole entities file to dedup — so a loop over it cost O(N²)
	// for an export of N entities. AddEntities pays the scan once and skips
	// duplicates silently, so the "already exists" branch this loop used to
	// carry is now just the gap between len(ents) and the returned count.
	//
	// The tradeoff, stated rather than hidden: a marshal failure inside the
	// batch aborts the WHOLE batch, where the per-entity loop recorded one
	// ImportError and carried on with the rest. storage.Entity marshals a
	// string/string map and four strings, so there is no value that can fail
	// json.Marshal here, but the batch shape is the reason that is worth
	// saying out loud.
	if opts.DryRun {
		result.EntitiesCreated += len(export.Entities)
	} else {
		ents := make([]storage.Entity, 0, len(export.Entities))
		for _, e := range export.Entities {
			ents = append(ents, storage.Entity{
				ID:         e.ID,
				Name:       e.Name,
				Type:       e.Type,
				Properties: e.Properties,
				CreatedAt:  e.CreatedAt,
			})
		}
		added, addErr := vault.AddEntities("mempalace", ents)
		if addErr != nil {
			result.Errors = append(result.Errors, ImportError{
				Project: "mempalace",
				Err:     addErr,
			})
		}
		// EntitiesCreated stays "newly created": the batch count already
		// excludes both the entities already in the graph and any duplicate
		// IDs inside the export itself.
		result.EntitiesCreated += added
	}

	// Step 6: import triples.
	now := time.Now().UTC().Format(time.RFC3339)
	for _, t := range export.Triples {
		if opts.DryRun {
			result.TriplesCreated++
			continue
		}
		validTo := ""
		if t.ValidTo != nil {
			validTo = *t.ValidTo
		}
		st := storage.Triple{
			Subject:       t.Subject,
			Predicate:     t.Predicate,
			Object:        t.Object,
			ValidFrom:     t.ValidFrom,
			ValidTo:       validTo,
			Confidence:    t.Confidence,
			SourceSession: t.SourceSession,
			ExtractedAt:   now,
		}
		addErr := vault.AddTriple("mempalace", st)
		if addErr != nil {
			if strings.Contains(addErr.Error(), "already exists") {
				continue
			}
			result.Errors = append(result.Errors, ImportError{
				Project: "mempalace",
				Err:     addErr,
			})
			continue
		}
		result.TriplesCreated++
	}

	return result, nil
}

// mapWing maps a MemPalace wing name to its vault equivalent using the
// known mapping table, falling back to slug.Slugify for unknown wings.
func mapWing(original string) string {
	if mapped, ok := wingMapping[original]; ok {
		return mapped
	}
	return slug.Slugify(original)
}
