// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0
//
// VP FORK ADDITION — NOT part of upstream github.com/coder/hnsw.
//
// Validate is a permanent vibe-palace addition (task
// hnsw-graph-file-envelope-and-warm-start, Scope 4). The envelope loader runs
// it on a freshly Imported graph before trusting it, so an attacker-crafted or
// corrupt hnsw.idx is rejected as corrupt instead of poisoning a live search.
// It reads unexported graph internals (layers, nodes, neighbors) that no
// exported API exposes, which is why it lives inside the vendored copy. It is
// permanent and independent of any upstream change (e.g. removal of a Windows
// carve-out); nothing here blocks an upstream resync.

package hnsw

import (
	"fmt"
	"math"
)

// Validate reports the first structural defect in the graph, or nil for a
// well-formed (or empty) graph. It NEVER panics on a malformed graph — a nil
// *layer, dangling neighbour pointers, a key stored under the wrong map key,
// non-finite values, mismatched value lengths — so a loader can safely run it
// on untrusted bytes. It deliberately does NOT call Dims, (*layer).entry or
// Search, each of which assumes a well-formed graph and can panic.
func (h *Graph[K]) Validate() error {
	if len(h.layers) == 0 {
		return nil // an empty graph is structurally valid
	}
	// A nil *layer slot would panic the moment its .nodes is ranged, so reject
	// it first (a hand-built, non-Import graph can have one).
	for i, l := range h.layers {
		if l == nil {
			return fmt.Errorf("hnsw.Validate: layer %d is a nil *layer", i)
		}
	}
	base := h.layers[0] // layer 0 is the base layer: every key lives here.

	// Layer 0: keys well-formed, values finite and uniform length.
	dim := -1
	for key, node := range base.nodes {
		if node == nil {
			return fmt.Errorf("hnsw.Validate: layer 0 key %v has a nil node", key)
		}
		if node.Key != key {
			return fmt.Errorf("hnsw.Validate: layer 0 node stored under key %v but node.Key is %v", key, node.Key)
		}
		if dim < 0 {
			dim = len(node.Value)
		} else if len(node.Value) != dim {
			return fmt.Errorf("hnsw.Validate: layer 0 key %v has value length %d, want %d", key, len(node.Value), dim)
		}
		for _, v := range node.Value {
			if f := float64(v); math.IsNaN(f) || math.IsInf(f, 0) {
				return fmt.Errorf("hnsw.Validate: layer 0 key %v has a non-finite value", key)
			}
		}
	}

	// Every layer: no nil node, key matches its map slot, higher-layer keys are
	// present in layer 0, and every neighbour pointer is non-nil and refers to a
	// node in the SAME layer.
	for i, l := range h.layers {
		for key, node := range l.nodes {
			if node == nil {
				return fmt.Errorf("hnsw.Validate: layer %d key %v has a nil node", i, key)
			}
			if node.Key != key {
				return fmt.Errorf("hnsw.Validate: layer %d node stored under key %v but node.Key is %v", i, key, node.Key)
			}
			if i > 0 {
				if _, ok := base.nodes[key]; !ok {
					return fmt.Errorf("hnsw.Validate: layer %d key %v is absent from layer 0", i, key)
				}
			}
			for nk, nb := range node.neighbors {
				if nb == nil {
					return fmt.Errorf("hnsw.Validate: layer %d key %v has a nil neighbour pointer for %v", i, key, nk)
				}
				want, ok := l.nodes[nk]
				if !ok {
					return fmt.Errorf("hnsw.Validate: layer %d key %v has neighbour %v absent from the same layer", i, key, nk)
				}
				if nb != want {
					return fmt.Errorf("hnsw.Validate: layer %d key %v neighbour %v points at the wrong node", i, key, nk)
				}
			}
		}
	}
	return nil
}
