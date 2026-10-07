// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// --- Schemas ---

var palaceStatusSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug."
		},
		"sections": {
			"type": "array",
			"items": {"type": "string", "enum": ["stats", "per_wing", "tunnels"]},
			"description": "Optional subset of sections to return: \"stats\" (aggregate counts), \"per_wing\" (per-wing breakdown), and/or \"tunnels\" (cross-wing rooms). Default/empty returns all. Unselected sections are ZEROED (present-but-empty, not computed) — because stats is a struct of counts, a suppressed stats is byte-indistinguishable from a genuinely empty palace, so do not read a suppressed section's fields as real data. The graph is built in full regardless; this only trims the payload."
		}
	},
	"required": ["project"]
}`)

var listWingsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug."
		}
	},
	"required": ["project"]
}`)

var listRoomsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug."
		},
		"wing": {
			"type": "string",
			"description": "Wing slug to list rooms for."
		}
	},
	"required": ["project", "wing"]
}`)

var traverseSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug."
		},
		"start": {
			"type": "string",
			"description": "Start node in wing/room format (e.g. 'alpha/api')."
		},
		"max_hops": {
			"type": "integer",
			"description": "Maximum BFS hops (default 3, max 10)."
		}
	},
	"required": ["project", "start"]
}`)

var findTunnelsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug."
		}
	},
	"required": ["project"]
}`)

// --- Tool constructors ---

// PalaceStatusTool returns the MCP tool for vp_palace_status.
func PalaceStatusTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_palace_status",
		Description: "Overview of palace structure: wing/room/drawer counts, tunnels, and per-wing breakdown. Built from this host's local chunk store; empty counts can mean the index is unbuilt on this host rather than that the palace is empty — run vp_index_coverage to tell them apart.",
		Schema:      palaceStatusSchema,
		Handler:     palaceStatusHandler(vault),
	}
}

// ListWingsTool returns the MCP tool for vp_list_wings.
func ListWingsTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_list_wings",
		Description: "List all wings in a project with room and drawer counts. Read from this host's local chunk store; an empty list can mean the index is unbuilt on this host — run vp_index_coverage.",
		Schema:      listWingsSchema,
		Handler:     listWingsHandler(vault),
	}
}

// ListRoomsTool returns the MCP tool for vp_list_rooms.
func ListRoomsTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_list_rooms",
		Description: "List rooms in a wing with drawer counts and hall distribution. Read from this host's local chunk store; an empty list can mean the index is unbuilt on this host — run vp_index_coverage.",
		Schema:      listRoomsSchema,
		Handler:     listRoomsHandler(vault),
	}
}

// TraverseTool returns the MCP tool for vp_traverse.
func TraverseTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_traverse",
		Description: "BFS graph traversal from a starting room, returning reachable rooms with hop distances. The graph is built from this host's local chunk store; an empty or missing start can mean the index is unbuilt on this host — run vp_index_coverage.",
		Schema:      traverseSchema,
		Handler:     traverseHandler(vault),
	}
}

// FindTunnelsTool returns the MCP tool for vp_find_tunnels.
func FindTunnelsTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_find_tunnels",
		Description: "Find rooms that span multiple wings (cross-wing connections). Built from this host's local chunk store; an empty result can mean the index is unbuilt on this host — run vp_index_coverage.",
		Schema:      findTunnelsSchema,
		Handler:     findTunnelsHandler(vault),
	}
}

// --- Param types ---

type projectParams struct {
	Project string `json:"project"`
}

type palaceStatusParams struct {
	Project  string   `json:"project"`
	Sections []string `json:"sections,omitempty"`
}

type wingParams struct {
	Project string `json:"project"`
	Wing    string `json:"wing"`
}

type traverseParams struct {
	Project string `json:"project"`
	Start   string `json:"start"`
	MaxHops int    `json:"max_hops,omitempty"`
}

// --- Result types ---

type palaceStatusResult struct {
	Stats   palace.PalaceStats `json:"stats"`
	PerWing []wingDetail       `json:"per_wing"`
	Tunnels []palace.Tunnel    `json:"tunnels"`
}

type wingDetail struct {
	Wing    string `json:"wing"`
	Rooms   int    `json:"rooms"`
	Drawers int    `json:"drawers"`
}

type roomDetail struct {
	Room             string         `json:"room"`
	Drawers          int            `json:"drawers"`
	HallDistribution map[string]int `json:"hall_distribution"`
}

type traverseResult struct {
	Wing    string `json:"wing"`
	Room    string `json:"room"`
	Drawers int    `json:"drawers"`
	HopDist int    `json:"hop_distance"`
}

// --- Handlers ---

func palaceStatusHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p palaceStatusParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}

		g, err := palace.BuildGraph(vault, p.Project)
		if err != nil {
			return nil, fmt.Errorf("build graph: %w", err)
		}

		result := palaceStatusResult{
			Stats:   g.Stats(),
			PerWing: buildWingDetails(g),
			Tunnels: g.FindTunnels(),
		}

		// Optional post-filter: when a narrowing sections selector is supplied,
		// ZERO the unselected section's field (present-but-empty, not an absent
		// key, and never omitempty on the result struct). Default/empty returns
		// the full result byte-for-byte unchanged. BuildGraph always runs in
		// full — this trims the payload, not the graph work.
		if len(p.Sections) > 0 {
			var wantStats, wantPerWing, wantTunnels bool
			for _, s := range p.Sections {
				switch s {
				case "stats":
					wantStats = true
				case "per_wing":
					wantPerWing = true
				case "tunnels":
					wantTunnels = true
				default:
					return nil, fmt.Errorf("invalid section %q: must be one of [stats per_wing tunnels]", s)
				}
			}
			if !wantStats {
				result.Stats = palace.PalaceStats{}
			}
			if !wantPerWing {
				result.PerWing = nil
			}
			if !wantTunnels {
				result.Tunnels = nil
			}
		}

		return result, nil
	}
}

func listWingsHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p projectParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}

		g, err := palace.BuildGraph(vault, p.Project)
		if err != nil {
			return nil, fmt.Errorf("build graph: %w", err)
		}

		return buildWingDetails(g), nil
	}
}

func listRoomsHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p wingParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if p.Wing == "" {
			return nil, fmt.Errorf("wing is required")
		}

		// Read the wing's rooms from the host-local chunk store (with the
		// pre-marker tracked-drawer fallback) through the one navigation reader,
		// so list_rooms cannot disagree with the graph about a room's drawers or
		// hall distribution (Scope 1/2).
		hits, err := palace.QueryDrawers(vault, storage.DrawerQuery{Project: p.Project, Wing: p.Wing})
		if err != nil {
			return nil, fmt.Errorf("list rooms: %w", err)
		}

		type roomAgg struct {
			count int
			halls map[string]int
		}
		byRoom := make(map[string]*roomAgg)
		for _, h := range hits {
			a := byRoom[h.Room]
			if a == nil {
				a = &roomAgg{halls: make(map[string]int)}
				byRoom[h.Room] = a
			}
			a.count++
			a.halls[h.Hall]++
		}

		details := make([]roomDetail, 0, len(byRoom))
		for room, a := range byRoom {
			details = append(details, roomDetail{
				Room:             room,
				Drawers:          a.count,
				HallDistribution: a.halls,
			})
		}

		sort.Slice(details, func(i, j int) bool {
			return details[i].Room < details[j].Room
		})
		return details, nil
	}
}

func traverseHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p traverseParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if p.Start == "" {
			return nil, fmt.Errorf("start is required")
		}

		maxHops := p.MaxHops
		if maxHops <= 0 {
			maxHops = 3
		}
		if maxHops > 10 {
			maxHops = 10
		}

		g, err := palace.BuildGraph(vault, p.Project)
		if err != nil {
			return nil, fmt.Errorf("build graph: %w", err)
		}

		nodes, err := g.Traverse(p.Start, maxHops)
		if err != nil {
			return nil, fmt.Errorf("traverse: %w", err)
		}

		results := make([]traverseResult, len(nodes))
		for i, n := range nodes {
			results[i] = traverseResult{
				Wing:    n.Wing,
				Room:    n.Room,
				Drawers: n.Drawers,
				HopDist: n.HopDist,
			}
		}
		return results, nil
	}
}

func findTunnelsHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p projectParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}

		g, err := palace.BuildGraph(vault, p.Project)
		if err != nil {
			return nil, fmt.Errorf("build graph: %w", err)
		}

		tunnels := g.FindTunnels()
		if tunnels == nil {
			tunnels = []palace.Tunnel{}
		}
		return tunnels, nil
	}
}

// --- Helpers ---

func buildWingDetails(g *palace.PalaceGraph) []wingDetail {
	wingMap := make(map[string]*wingDetail)
	for _, node := range g.Nodes {
		wd, ok := wingMap[node.Wing]
		if !ok {
			wd = &wingDetail{Wing: node.Wing}
			wingMap[node.Wing] = wd
		}
		wd.Rooms++
		wd.Drawers += node.Drawers
	}

	details := make([]wingDetail, 0, len(wingMap))
	for _, wd := range wingMap {
		details = append(details, *wd)
	}
	sort.Slice(details, func(i, j int) bool {
		return details[i].Wing < details[j].Wing
	})
	return details
}
