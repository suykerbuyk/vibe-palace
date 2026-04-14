// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
)

// skillSectionParams is the input for vp_get_skill_section.
type skillSectionParams struct {
	Name    string `json:"name"`
	Section string `json:"section"`
	Project string `json:"project,omitempty"`
	Wing    string `json:"wing,omitempty"`
	Room    string `json:"room,omitempty"`
}

// skillSectionResult is the structured handler return. Keeping this
// separate from a raw string lets callers discriminate the reference
// body from the surrounding skill frame emitted by vp_skill.
type skillSectionResult struct {
	Content string `json:"content"`
	Source  string `json:"source"`
}

var skillSectionSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"name": {
			"type": "string",
			"description": "Skill name (the directory basename under skills/)."
		},
		"section": {
			"type": "string",
			"description": "Reference basename (e.g. 'capex-opex' for references/capex-opex.md)."
		},
		"project": {
			"type": "string",
			"description": "Project slug for project-level resolution."
		},
		"wing": {
			"type": "string",
			"description": "Wing slug for wing/room-scoped resolution."
		},
		"room": {
			"type": "string",
			"description": "Room slug for room-scoped resolution (requires wing)."
		}
	},
	"required": ["name", "section"]
}`)

// GetSkillSectionTool returns the MCP tool for vp_get_skill_section.
// Unlike vp_skill this tool returns only the raw reference body plus a
// source tag — no activation frame, because the reference is pulled on
// demand after the skill has already been activated.
func GetSkillSectionTool(resolver *vpctx.Resolver) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_get_skill_section",
		Description: "Fetch a single reference file from a directory-form skill (e.g. references/<section>.md). Use after vp_skill has activated the skill and listed available references.",
		Schema:      skillSectionSchema,
		Handler:     skillSectionHandler(resolver),
	}
}

func skillSectionHandler(resolver *vpctx.Resolver) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p skillSectionParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		name := strings.TrimSpace(p.Name)
		section := strings.TrimSpace(p.Section)
		if name == "" {
			return nil, fmt.Errorf("vp_get_skill_section: 'name' is required")
		}
		if section == "" {
			return nil, fmt.Errorf("vp_get_skill_section: 'section' is required")
		}
		data, source, err := resolver.ResolveSkillSection(name, section, p.Project, p.Wing, p.Room)
		if err != nil {
			return nil, fmt.Errorf("vp_get_skill_section: %w — call vp_skill %q to list available references", err, name)
		}
		return skillSectionResult{Content: string(data), Source: source}, nil
	}
}
