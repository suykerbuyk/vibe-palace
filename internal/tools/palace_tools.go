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
		Description: "Overview of palace structure: wing/room/drawer counts, tunnels, and per-wing breakdown.",
		Schema:      palaceStatusSchema,
		Handler:     palaceStatusHandler(vault),
	}
}

// ListWingsTool returns the MCP tool for vp_list_wings.
func ListWingsTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_list_wings",
		Description: "List all wings in a project with room and drawer counts.",
		Schema:      listWingsSchema,
		Handler:     listWingsHandler(vault),
	}
}

// ListRoomsTool returns the MCP tool for vp_list_rooms.
func ListRoomsTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_list_rooms",
		Description: "List rooms in a wing with drawer counts and hall distribution.",
		Schema:      listRoomsSchema,
		Handler:     listRoomsHandler(vault),
	}
}

// TraverseTool returns the MCP tool for vp_traverse.
func TraverseTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_traverse",
		Description: "BFS graph traversal from a starting room, returning reachable rooms with hop distances.",
		Schema:      traverseSchema,
		Handler:     traverseHandler(vault),
	}
}

// FindTunnelsTool returns the MCP tool for vp_find_tunnels.
func FindTunnelsTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_find_tunnels",
		Description: "Find rooms that span multiple wings (cross-wing connections).",
		Schema:      findTunnelsSchema,
		Handler:     findTunnelsHandler(vault),
	}
}

// --- Param types ---

type projectParams struct {
	Project string `json:"project"`
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

		perWing := buildWingDetails(g)

		return palaceStatusResult{
			Stats:   g.Stats(),
			PerWing: perWing,
			Tunnels: g.FindTunnels(),
		}, nil
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

		rooms, err := vault.ListRooms(p.Project, p.Wing)
		if err != nil {
			return nil, fmt.Errorf("list rooms: %w", err)
		}

		var details []roomDetail
		for _, room := range rooms {
			drawers, err := vault.ListDrawers(p.Project, p.Wing, room)
			if err != nil {
				return nil, fmt.Errorf("list drawers for %s: %w", room, err)
			}
			halls := make(map[string]int)
			for _, d := range drawers {
				halls[d.Hall]++
			}
			details = append(details, roomDetail{
				Room:             room,
				Drawers:          len(drawers),
				HallDistribution: halls,
			})
		}

		sort.Slice(details, func(i, j int) bool {
			return details[i].Room < details[j].Room
		})

		if details == nil {
			details = []roomDetail{}
		}
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
