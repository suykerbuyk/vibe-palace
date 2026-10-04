// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
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
	// sha256 is the hex sha256 of the export file's bytes: the import batch
	// ids are mempalace:<sha256>:<n>, so the same export always maps to the
	// same batches and a re-import is skipped.
	sha256 string
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
	sum := sha256.Sum256(raw)
	return &MemPalaceExport{data: data, path: path, sha256: hex.EncodeToString(sum[:])}, nil
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

// mapWing maps a MemPalace wing name to its vault equivalent using the
// known mapping table, falling back to slug.Slugify for unknown wings.
func mapWing(original string) string {
	if mapped, ok := wingMapping[original]; ok {
		return mapped
	}
	return slug.Slugify(original)
}
