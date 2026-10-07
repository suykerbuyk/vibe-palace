// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"fmt"
	"sort"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// GraphNode represents a room in the palace graph.
type GraphNode struct {
	Wing     string   `json:"wing"`
	Room     string   `json:"room"`
	Drawers  int      `json:"drawers"`
	Adjacent []string `json:"adjacent,omitempty"` // "wing/room" keys
	HopDist  int      `json:"hop_distance"`
}

// Tunnel represents a room slug that appears in two or more wings.
type Tunnel struct {
	Room  string   `json:"room"`
	Wings []string `json:"wings"`
	Count int      `json:"total_drawers"`
}

// PalaceStats summarizes the graph.
type PalaceStats struct {
	Wings     int `json:"wings"`
	Rooms     int `json:"rooms"`      // unique room slugs
	RoomNodes int `json:"room_nodes"` // total wing/room combinations
	Drawers   int `json:"drawers"`
	Tunnels   int `json:"tunnels"`
	Edges     int `json:"edges"`
}

// PalaceGraph is the in-memory palace structure built from vault drawers.
type PalaceGraph struct {
	Nodes   map[string]*GraphNode // key: "wing/room"
	tunnels []Tunnel
	stats   PalaceStats
}

// nodeKey returns the canonical key for a node.
func nodeKey(wing, room string) string {
	return wing + "/" + room
}

// BuildGraph constructs a PalaceGraph from the host-local chunk store (ADR-014:
// wing and room are chunk-store metadata, not directory names), with the
// tracked-drawer fallback before the migration marker. Both reach it through
// visibleDrawers, the one navigation reader. A (wing, room) node exists only
// while it holds at least one visible drawer.
//
// Adjacency rules:
//   - Rooms within the same wing are adjacent to each other.
//   - Rooms with the same slug across different wings are adjacent (tunnels).
func BuildGraph(vault *storage.Vault, project string) (*PalaceGraph, error) {
	hits, err := visibleDrawers(vault, project)
	if err != nil {
		return nil, err
	}

	g := &PalaceGraph{
		Nodes: make(map[string]*GraphNode),
	}

	// roomToKeys tracks which node keys belong to each room slug; wingKeys the
	// node keys of each wing (both in first-seen order, which is deterministic
	// because visibleDrawers sorts its hits by wing, room, then id).
	roomToKeys := make(map[string][]string)
	wingKeys := make(map[string][]string)
	wingSet := make(map[string]bool)
	uniqueRooms := make(map[string]bool)
	totalDrawers := 0

	for _, h := range hits {
		wingSet[h.Wing] = true
		uniqueRooms[h.Room] = true
		key := nodeKey(h.Wing, h.Room)
		node, ok := g.Nodes[key]
		if !ok {
			node = &GraphNode{Wing: h.Wing, Room: h.Room}
			g.Nodes[key] = node
			wingKeys[h.Wing] = append(wingKeys[h.Wing], key)
			roomToKeys[h.Room] = append(roomToKeys[h.Room], key)
		}
		node.Drawers++
		totalDrawers++
	}

	// Intra-wing adjacency: all rooms in the same wing are adjacent.
	for _, keys := range wingKeys {
		for i, a := range keys {
			for _, b := range keys[i+1:] {
				g.Nodes[a].Adjacent = append(g.Nodes[a].Adjacent, b)
				g.Nodes[b].Adjacent = append(g.Nodes[b].Adjacent, a)
			}
		}
	}

	// Cross-wing adjacency: same room slug in different wings.
	edgeCount := 0
	for _, keys := range roomToKeys {
		for i, a := range keys {
			for _, b := range keys[i+1:] {
				if g.Nodes[a].Wing != g.Nodes[b].Wing {
					g.Nodes[a].Adjacent = append(g.Nodes[a].Adjacent, b)
					g.Nodes[b].Adjacent = append(g.Nodes[b].Adjacent, a)
				}
			}
		}
	}

	// Sort adjacency lists for deterministic output.
	for _, node := range g.Nodes {
		sort.Strings(node.Adjacent)
		edgeCount += len(node.Adjacent)
	}
	edgeCount /= 2 // each edge counted twice

	// Build tunnels.
	var tunnels []Tunnel
	for room, keys := range roomToKeys {
		if len(keys) < 2 {
			continue
		}
		var tunnelWings []string
		drawerSum := 0
		for _, k := range keys {
			n := g.Nodes[k]
			tunnelWings = append(tunnelWings, n.Wing)
			drawerSum += n.Drawers
		}
		sort.Strings(tunnelWings)
		tunnels = append(tunnels, Tunnel{
			Room:  room,
			Wings: tunnelWings,
			Count: drawerSum,
		})
	}
	sort.Slice(tunnels, func(i, j int) bool {
		return tunnels[i].Room < tunnels[j].Room
	})
	g.tunnels = tunnels

	g.stats = PalaceStats{
		Wings:     len(wingSet),
		Rooms:     len(uniqueRooms),
		RoomNodes: len(g.Nodes),
		Drawers:   totalDrawers,
		Tunnels:   len(tunnels),
		Edges:     edgeCount,
	}

	return g, nil
}

// Traverse performs a BFS from startKey ("wing/room") up to maxHops.
// Returns reachable nodes ordered by hop distance, then alphabetically.
func (g *PalaceGraph) Traverse(startKey string, maxHops int) ([]GraphNode, error) {
	if _, ok := g.Nodes[startKey]; !ok {
		return nil, fmt.Errorf("start node %q not found in graph", startKey)
	}
	if maxHops < 0 {
		maxHops = 0
	}

	visited := map[string]int{startKey: 0}
	queue := []string{startKey}
	var result []GraphNode

	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		node := g.Nodes[key]
		dist := visited[key]

		out := *node
		out.HopDist = dist
		result = append(result, out)

		if dist >= maxHops {
			continue
		}
		for _, adj := range node.Adjacent {
			if _, seen := visited[adj]; !seen {
				visited[adj] = dist + 1
				queue = append(queue, adj)
			}
		}
	}

	// Sort by hop distance, then alphabetically by key.
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].HopDist != result[j].HopDist {
			return result[i].HopDist < result[j].HopDist
		}
		ki := nodeKey(result[i].Wing, result[i].Room)
		kj := nodeKey(result[j].Wing, result[j].Room)
		return ki < kj
	})

	return result, nil
}

// FindTunnels returns rooms that span two or more wings.
func (g *PalaceGraph) FindTunnels() []Tunnel {
	return g.tunnels
}

// Stats returns aggregate palace statistics.
func (g *PalaceGraph) Stats() PalaceStats {
	return g.stats
}
