// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0
//
// VP FORK ADDITION — tests for Validate (see validate.go).

package hnsw

import (
	"math"
	"testing"
)

// buildLayer0 returns a one-layer graph with the given keys, each a 2-d finite
// vector, fully cross-linked so a well-formed graph validates.
func buildLayer0(keys ...uint64) *Graph[uint64] {
	nodes := make(map[uint64]*layerNode[uint64], len(keys))
	for _, k := range keys {
		nodes[k] = &layerNode[uint64]{
			Node:      Node[uint64]{Key: k, Value: Vector{1, 0}},
			neighbors: map[uint64]*layerNode[uint64]{},
		}
	}
	for _, a := range keys {
		for _, b := range keys {
			if a != b {
				nodes[a].neighbors[b] = nodes[b]
			}
		}
	}
	return &Graph[uint64]{layers: []*layer[uint64]{{nodes: nodes}}}
}

func TestValidate_WellFormed(t *testing.T) {
	if err := buildLayer0(1, 2, 3).Validate(); err != nil {
		t.Fatalf("well-formed graph: %v", err)
	}
	if err := (&Graph[uint64]{}).Validate(); err != nil {
		t.Fatalf("empty graph: %v", err)
	}
}

func TestValidate_NilLayer(t *testing.T) {
	g := buildLayer0(1, 2)
	g.layers = append(g.layers, nil) // a nil *layer slot
	if err := g.Validate(); err == nil {
		t.Fatal("want error for a nil *layer, got nil (would panic on a naked range)")
	}
}

func TestValidate_NilNeighbourPointer(t *testing.T) {
	g := buildLayer0(1, 2)
	g.layers[0].nodes[1].neighbors[99] = nil // dangling, nil pointer
	if err := g.Validate(); err == nil {
		t.Fatal("want error for a nil neighbour pointer")
	}
}

func TestValidate_NeighbourAbsentFromLayer(t *testing.T) {
	g := buildLayer0(1, 2)
	// A non-nil pointer to a node that is not in this layer's node map.
	stray := &layerNode[uint64]{Node: Node[uint64]{Key: 42, Value: Vector{1, 0}}}
	g.layers[0].nodes[1].neighbors[42] = stray
	if err := g.Validate(); err == nil {
		t.Fatal("want error for a neighbour absent from the layer")
	}
}

func TestValidate_NeighbourPointerWrongNode(t *testing.T) {
	g := buildLayer0(1, 2, 3)
	// Neighbour key 2 points at the node for key 3 (a crossed pointer).
	g.layers[0].nodes[1].neighbors[2] = g.layers[0].nodes[3]
	if err := g.Validate(); err == nil {
		t.Fatal("want error for a neighbour pointer that is the wrong node for its key")
	}
}

func TestValidate_NonFinite(t *testing.T) {
	g := buildLayer0(1)
	g.layers[0].nodes[1].Value = Vector{float32(math.Inf(1)), 0}
	if err := g.Validate(); err == nil {
		t.Fatal("want error for a non-finite value")
	}
}

func TestValidate_HigherLayerKeyMissingFromBase(t *testing.T) {
	g := buildLayer0(1, 2)
	hi := &layerNode[uint64]{Node: Node[uint64]{Key: 7, Value: Vector{1, 0}}, neighbors: map[uint64]*layerNode[uint64]{}}
	g.layers = append(g.layers, &layer[uint64]{nodes: map[uint64]*layerNode[uint64]{7: hi}})
	if err := g.Validate(); err == nil {
		t.Fatal("want error for a layer-1 key absent from layer 0")
	}
}
