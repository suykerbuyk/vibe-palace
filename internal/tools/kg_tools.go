// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// --- KG Schemas ---

var kgQuerySchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project":   {"type": "string", "description": "Project slug."},
		"entity":    {"type": "string", "description": "Entity name to query."},
		"direction": {"type": "string", "description": "Query direction: out, in, or both (default: both)."},
		"as_of":     {"type": "string", "description": "Date filter (YYYY-MM-DD). Only returns facts valid at this date."}
	},
	"required": ["project", "entity"]
}`)

var kgAddSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project":    {"type": "string", "description": "Project slug."},
		"subject":    {"type": "string", "description": "Subject entity name."},
		"predicate":  {"type": "string", "description": "Relationship predicate (e.g. works_on, uses, depends_on)."},
		"object":     {"type": "string", "description": "Object entity name."},
		"confidence": {"type": "number", "description": "Confidence score 0-1 (default: 1.0)."},
		"valid_from": {"type": "string", "description": "Start date (YYYY-MM-DD)."}
	},
	"required": ["project", "subject", "predicate", "object"]
}`)

var kgInvalidateSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project":   {"type": "string", "description": "Project slug."},
		"subject":   {"type": "string", "description": "Subject entity name."},
		"predicate": {"type": "string", "description": "Relationship predicate."},
		"object":    {"type": "string", "description": "Object entity name."},
		"ended":     {"type": "string", "description": "End date (YYYY-MM-DD)."}
	},
	"required": ["project", "subject", "predicate", "object", "ended"]
}`)

var kgTimelineSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"entity":  {"type": "string", "description": "Entity name."}
	},
	"required": ["project", "entity"]
}`)

var kgStatsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."}
	},
	"required": ["project"]
}`)

// --- KG Param types ---

type kgQueryParams struct {
	Project   string `json:"project"`
	Entity    string `json:"entity"`
	Direction string `json:"direction,omitempty"`
	AsOf      string `json:"as_of,omitempty"`
}

type kgAddParams struct {
	Project    string  `json:"project"`
	Subject    string  `json:"subject"`
	Predicate  string  `json:"predicate"`
	Object     string  `json:"object"`
	Confidence float64 `json:"confidence,omitempty"`
	ValidFrom  string  `json:"valid_from,omitempty"`
}

type kgInvalidateParams struct {
	Project   string `json:"project"`
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
	Ended     string `json:"ended"`
}

type kgEntityParams struct {
	Project string `json:"project"`
	Entity  string `json:"entity"`
}

// --- KG Result types ---

type kgStatsResult struct {
	storage.KGStats
	TypeBreakdown map[string]int `json:"type_breakdown"`
}

// --- Tool constructors ---

// KGQueryTool returns the MCP tool for vp_kg_query.
func KGQueryTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_kg_query",
		Description: "Query the knowledge graph for facts about an entity.",
		Schema:      kgQuerySchema,
		Handler:     kgQueryHandler(vault),
	}
}

// KGAddTool returns the MCP tool for vp_kg_add.
func KGAddTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_kg_add",
		Description: "Add a fact (triple) to the knowledge graph, creating entities if needed.",
		Schema:      kgAddSchema,
		Handler:     kgAddHandler(vault),
	}
}

// KGInvalidateTool returns the MCP tool for vp_kg_invalidate.
func KGInvalidateTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_kg_invalidate",
		Description: "Mark a knowledge graph fact as no longer valid (sets end date).",
		Schema:      kgInvalidateSchema,
		Handler:     kgInvalidateHandler(vault),
	}
}

// KGTimelineTool returns the MCP tool for vp_kg_timeline.
func KGTimelineTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_kg_timeline",
		Description: "Get the chronological timeline of facts involving an entity.",
		Schema:      kgTimelineSchema,
		Handler:     kgTimelineHandler(vault),
	}
}

// KGStatsTool returns the MCP tool for vp_kg_stats.
func KGStatsTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_kg_stats",
		Description: "Get knowledge graph statistics: entity/triple counts, predicate types, type breakdown.",
		Schema:      kgStatsSchema,
		Handler:     kgStatsHandler(vault),
	}
}

// --- Handlers ---

func kgQueryHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p kgQueryParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if p.Entity == "" {
			return nil, fmt.Errorf("entity is required")
		}

		direction := p.Direction
		if direction == "" {
			direction = "both"
		}

		triples, err := vault.QueryEntity(p.Project, p.Entity, p.AsOf, direction)
		if err != nil {
			return nil, fmt.Errorf("query entity: %w", err)
		}
		if triples == nil {
			triples = []storage.Triple{}
		}
		return triples, nil
	}
}

func kgAddHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p kgAddParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if p.Subject == "" || p.Predicate == "" || p.Object == "" {
			return nil, fmt.Errorf("subject, predicate, and object are required")
		}

		confidence := p.Confidence
		if confidence <= 0 {
			confidence = 1.0
		}

		// Ensure both entities exist (catch "already exists" and continue).
		for _, name := range []string{p.Subject, p.Object} {
			err := vault.AddEntity(p.Project, storage.Entity{
				ID:   slugifyKG(name),
				Name: name,
				Type: "unknown",
			})
			if err != nil && !strings.Contains(err.Error(), "already exists") {
				return nil, fmt.Errorf("add entity %q: %w", name, err)
			}
		}

		triple := storage.Triple{
			Subject:    p.Subject,
			Predicate:  p.Predicate,
			Object:     p.Object,
			Confidence: confidence,
			ValidFrom:  p.ValidFrom,
		}
		if err := vault.AddTriple(p.Project, triple); err != nil {
			return nil, fmt.Errorf("add triple: %w", err)
		}

		return map[string]string{"status": "added"}, nil
	}
}

func kgInvalidateHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p kgInvalidateParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if p.Subject == "" || p.Predicate == "" || p.Object == "" || p.Ended == "" {
			return nil, fmt.Errorf("subject, predicate, object, and ended are required")
		}

		if err := vault.InvalidateTriple(p.Project, p.Subject, p.Predicate, p.Object, p.Ended); err != nil {
			return nil, fmt.Errorf("invalidate triple: %w", err)
		}

		return map[string]string{"status": "invalidated"}, nil
	}
}

func kgTimelineHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p kgEntityParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if p.Entity == "" {
			return nil, fmt.Errorf("entity is required")
		}

		triples, err := vault.Timeline(p.Project, p.Entity)
		if err != nil {
			return nil, fmt.Errorf("timeline: %w", err)
		}
		if triples == nil {
			triples = []storage.Triple{}
		}
		return triples, nil
	}
}

func kgStatsHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p projectParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}

		stats, err := vault.KGStats(p.Project)
		if err != nil {
			return nil, fmt.Errorf("kg stats: %w", err)
		}

		// Compute type breakdown from entities.
		entities, err := vault.ListEntities(p.Project)
		if err != nil {
			return nil, fmt.Errorf("list entities: %w", err)
		}

		breakdown := make(map[string]int)
		for _, e := range entities {
			breakdown[e.Type]++
		}

		return kgStatsResult{
			KGStats:       stats,
			TypeBreakdown: breakdown,
		}, nil
	}
}

// slugifyKG produces a slug from an entity name for use as entity ID.
func slugifyKG(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == ' ' || r == '_':
			b.WriteByte('-')
		}
	}
	result := b.String()
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	return strings.Trim(result, "-")
}
