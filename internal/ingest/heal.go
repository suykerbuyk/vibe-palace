// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"context"

	"github.com/suykerbuyk/vibe-palace/internal/search"
)

// GraphHealer is the graph-fingerprint self-heal seam (Scope 4, "The
// graph-fingerprint self-heal"). hnsw-graph-file-envelope-and-warm-start's
// search.GraphHealer implements it; this package declares both the interface
// and its result because that child depends, through the rebuild, on this
// one. On a graph-fingerprint mismatch the healer rebuilds the HNSW graph from
// the cached vectors and local chunks with no embedding; a corrupt hnsw.idx is
// deleted. Neither is a discard, and a graph mismatch is never stale.
type GraphHealer interface {
	HealGraph(ctx context.Context, vault, project string) (HealResult, error)
}

// HealResult is what one HealGraph did.
type HealResult struct {
	Rebuilt bool   // the graph was rebuilt
	Deleted bool   // a corrupt hnsw.idx was removed
	Reason  string // why, for vp.log
}

// graphHealEngine is what the adapter needs of the search engine: the
// graph-heal method. *search.Engine satisfies it. Its result type lives in
// search because internal/search cannot import internal/ingest (coverage.go);
// this package imports search and maps search.HealOutcome to HealResult, the
// same direction CacheEngine takes (its return types live in search too).
type graphHealEngine interface {
	HealGraph(ctx context.Context, vaultRoot, project string) (search.HealOutcome, error)
}

// engineHealer adapts a graphHealEngine to the GraphHealer interface so a
// *search.Engine can be dropped into Deps.GraphHealer.
type engineHealer struct{ eng graphHealEngine }

// NewEngineHealer wraps a search engine as the graph-heal seam.
func NewEngineHealer(eng graphHealEngine) GraphHealer { return engineHealer{eng: eng} }

// HealGraph forwards to the search engine and maps its result.
func (h engineHealer) HealGraph(ctx context.Context, vault, project string) (HealResult, error) {
	out, err := h.eng.HealGraph(ctx, vault, project)
	if err != nil {
		return HealResult{}, err
	}
	return HealResult{Rebuilt: out.Rebuilt, Deleted: out.Deleted, Reason: out.Reason}, nil
}

var _ GraphHealer = engineHealer{}
