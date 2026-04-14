// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"github.com/suykerbuyk/vibe-palace/internal/kg"
)

// ExtractedEntity represents an entity found via regex in transcript text.
//
// Deprecated in-package: prefer kg.ExtractAll, which runs this extractor
// alongside the KG entity/triple extractors in a single dedup/frequency
// pass. This wrapper is retained for existing callers and tests; the
// regex definitions now live in internal/kg/extract_all.go.
type ExtractedEntity struct {
	Name string
	Type string // "file", "url"
}

// ExtractEntities returns deduplicated file-path and URL entities found in
// text via regex patterns. It is a thin adapter over kg.ExtractFilesAndURLs.
func ExtractEntities(text string) []ExtractedEntity {
	raw := kg.ExtractFilesAndURLs(text)
	out := make([]ExtractedEntity, 0, len(raw))
	for _, r := range raw {
		out = append(out, ExtractedEntity{Name: r.Name, Type: r.Type})
	}
	return out
}
