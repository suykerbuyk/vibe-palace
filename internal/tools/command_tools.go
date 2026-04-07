// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
)

// getResourceParams is the input for vp_get_command and vp_get_skill.
type getResourceParams struct {
	Name    string `json:"name"`
	Project string `json:"project,omitempty"`
}

// getResourceResult is the response from vp_get_command and vp_get_skill.
type getResourceResult struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Source  string `json:"source"`
}

// listResourceResult is the response from vp_list_commands and vp_list_skills.
type listResourceResult struct {
	Resources []vpctx.ResourceInfo `json:"resources"`
}

var getCommandSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"name": {
			"type": "string",
			"description": "Command name (e.g. 'restart', 'wrap')."
		},
		"project": {
			"type": "string",
			"description": "Project slug for project-level overrides."
		}
	},
	"required": ["name"]
}`)

var getSkillSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"name": {
			"type": "string",
			"description": "Skill name."
		},
		"project": {
			"type": "string",
			"description": "Project slug for project-level overrides."
		}
	},
	"required": ["name"]
}`)

var listCommandsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug for project-level commands."
		}
	}
}`)

var listSkillsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug for project-level skills."
		}
	}
}`)

// GetCommandTool returns the MCP tool for vp_get_command.
func GetCommandTool(resolver *vpctx.Resolver) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_get_command",
		Description: "Get a command's full markdown content by name, resolved via precedence.",
		Schema:      getCommandSchema,
		Handler:     getResourceHandler(resolver, "command"),
	}
}

// GetSkillTool returns the MCP tool for vp_get_skill.
func GetSkillTool(resolver *vpctx.Resolver) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_get_skill",
		Description: "Get a skill's full markdown content by name, resolved via precedence.",
		Schema:      getSkillSchema,
		Handler:     getResourceHandler(resolver, "skill"),
	}
}

// ListCommandsTool returns the MCP tool for vp_list_commands.
func ListCommandsTool(resolver *vpctx.Resolver) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_list_commands",
		Description: "List all available commands, merged across precedence levels.",
		Schema:      listCommandsSchema,
		Handler:     listResourceHandler(resolver, "command"),
	}
}

// ListSkillsTool returns the MCP tool for vp_list_skills.
func ListSkillsTool(resolver *vpctx.Resolver) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_list_skills",
		Description: "List all available skills, merged across precedence levels.",
		Schema:      listSkillsSchema,
		Handler:     listResourceHandler(resolver, "skill"),
	}
}

func getResourceHandler(resolver *vpctx.Resolver, resourceType string) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p getResourceParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}

		resource := fmt.Sprintf("%s:%s", resourceType, p.Name)
		content, source, err := resolver.Resolve(resource, p.Project)
		if err != nil {
			return nil, err
		}

		return getResourceResult{
			Name:    p.Name,
			Content: content,
			Source:  source,
		}, nil
	}
}

func listResourceHandler(resolver *vpctx.Resolver, resourceType string) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p struct {
			Project string `json:"project,omitempty"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}

		resources, err := resolver.ListResources(resourceType, p.Project)
		if err != nil {
			return nil, err
		}

		return listResourceResult{Resources: resources}, nil
	}
}
