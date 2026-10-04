// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import "context"

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
