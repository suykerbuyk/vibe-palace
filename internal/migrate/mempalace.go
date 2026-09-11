// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
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

// embedText is the text a drawer contributes to embedding: its content with
// surrounding whitespace trimmed. A drawer whose embedText is empty is skipped
// by the import and needs no model — EmbeddableDrawers and the import's
// work-collection step both decide through this one method.
func (d memPalaceDrawer) embedText() string {
	return strings.TrimSpace(d.Content)
}

const embedBatchSize = 32

// MemPalaceExport is a parsed MemPalace JSON export, produced by
// LoadMemPalaceExport and consumed by ImportMemPalace. It is deliberately
// opaque: the JSON shape stays unexported, so a caller can only load it, ask
// how many drawers need the embedding model, and import it.
//
// Loading is split from importing so a caller can validate its input before
// paying for anything expensive: a missing or malformed export fails in
// LoadMemPalaceExport, before the caller has constructed an embedder.
type MemPalaceExport struct {
	data memPalaceExport
	path string
}

// LoadMemPalaceExport reads and parses the MemPalace JSON export at path.
//
// Error classes, which the CLI maps to exit codes:
//   - a missing file wraps fs.ErrNotExist;
//   - a directory wraps fs.ErrInvalid (checked by Stat, so it is portable where
//     os.ReadFile on a directory is not);
//   - malformed JSON wraps *json.SyntaxError or *json.UnmarshalTypeError;
//   - any other failure (permission, I/O) is returned wrapped as-is.
//
// Read failures carry the "read export file:" prefix and parse failures the
// "parse export JSON:" prefix.
func LoadMemPalaceExport(path string) (*MemPalaceExport, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read export file: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("read export file: %s is a directory: %w", path, fs.ErrInvalid)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read export file: %w", err)
	}
	var data memPalaceExport
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parse export JSON: %w", err)
	}
	return &MemPalaceExport{data: data, path: path}, nil
}

// EmbeddableDrawers counts the drawers ImportMemPalace would embed — those
// whose content is non-blank. Zero means a real import needs no embedding
// model: entities and triples are written without one.
func (e *MemPalaceExport) EmbeddableDrawers() int {
	n := 0
	for _, d := range e.data.Drawers {
		if d.embedText() != "" {
			n++
		}
	}
	return n
}

// ImportMemPalace imports a loaded MemPalace export's drawers, entities, and
// triples into the vault under the project "mempalace".
//
// engine and emb may be nil. A dry run embeds nothing and writes nothing, so
// it needs neither; a real run without them writes drawers unindexed.
func ImportMemPalace(
	ctx context.Context,
	vault *storage.Vault,
	engine *search.Engine,
	emb embedder.Embedder,
	mpExport *MemPalaceExport,
	opts ImportOptions,
) (ImportResult, error) {
	var result ImportResult
	export := mpExport.data

	// Step 1: collect non-empty drawer contents for batch embedding.
	type drawerWork struct {
		index int
		wing  string
		room  string
	}

	var texts []string
	var work []drawerWork
	for i, d := range export.Drawers {
		content := d.embedText()
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

	// Step 2: batch embed. A dry run never embeds: no dry-run count reads a
	// vector, so the vectors would be computed only to be thrown away.
	var allVecs [][]float32
	if len(texts) > 0 && emb != nil && !opts.DryRun {
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

	// Step 3: import drawers.
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
				File:    mpExport.path,
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
					File:    mpExport.path,
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

	// Step 4: import entities, in ONE batch.
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

	// Step 5: import triples.
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
