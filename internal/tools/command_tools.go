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
	Wing    string `json:"wing,omitempty"`
	Room    string `json:"room,omitempty"`
}

// getResourceResult is the response from vp_get_command and vp_get_skill.
//
// It gains the recovery handle that was MINTED and never emitted: mcp.CommandURI
// and mcp.SkillURI existed, both templates were registered in resources.go, and
// vp_read_resource served them — but neither tool ever told the caller the
// address of what it was returning. Measured 2026-08-12 on the live vault,
// vp_get_command on `wrap` returned 33,546 bytes against a host's 19,968-byte
// flat inline cap (1.7x), so the body was arriving as a prefix with no way to
// say so and nowhere to go for the rest. vp_get_skill measures 16,435 — under
// the cap, but it shares this struct and the same class of unbounded body.
//
// 🔴 THE HANDLE IS DECLARED ABOVE THE BODY IT RESCUES. encoding/json emits
// struct fields in declaration order and nothing on the response path
// re-serializes through a map, so declaration order is wire order is cut order;
// a handle under the bulk arrives only in the results that did not need it.
type getResourceResult struct {
	Name string `json:"name"`
	// ContentURI addresses this exact body for vp_read_resource.
	//
	// omitempty, and set only for an UNSCOPED project-level lookup: the command
	// and skill URI templates take {project}/{name} only, and their resolvers in
	// resources.go re-resolve with wing="" room="" (see resourceDefs). A URI
	// minted from a wing/room-scoped call would therefore address DIFFERENT
	// bytes than the ones returned here, and a recovery handle that silently
	// fetches a different document is worse than none. Absent likewise when no
	// project was given, since the template has nowhere to put one.
	ContentURI string `json:"content_uri,omitempty"`
	Source     string `json:"source"`
	// Content is the resolved markdown body — the only unbounded field, and so
	// the one a host cut is allowed to land in.
	Content string `json:"content"`

	// 🔴 THE TERMINAL SENTINEL — last field, no omitempty, always true on a
	// successful return. Its ABSENCE is the signal: present ⇒ every byte
	// arrived, absent ⇒ the host cut the document, whether or not it said so
	// (Claude Code truncates silently). This matters more here than almost
	// anywhere: a command body is a set of INSTRUCTIONS, and an agent that
	// silently receives the first two thirds of a procedure executes it
	// believing it is done. No omitempty because a false bool would vanish and
	// make "cut" and "whole" serialize identically. Anything declared after
	// this field re-opens the hole.
	Complete bool `json:"complete"`
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
	"required": ["name"]
}`)

var listCommandsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug for project-level commands."
		},
		"wing": {
			"type": "string",
			"description": "Wing slug for wing/room-scoped commands."
		},
		"room": {
			"type": "string",
			"description": "Room slug for room-scoped commands (requires wing)."
		}
	}
}`)

var listSkillsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug for project-level skills."
		},
		"wing": {
			"type": "string",
			"description": "Wing slug for wing/room-scoped skills."
		},
		"room": {
			"type": "string",
			"description": "Room slug for room-scoped skills (requires wing)."
		}
	}
}`)

// GetCommandTool returns the MCP tool for vp_get_command.
func GetCommandTool(resolver *vpctx.Resolver) mcp.Tool {
	return mcp.Tool{
		Name: "vp_get_command",
		Description: "Get a command's full markdown content by name, resolved via precedence. The result LEADS with `content_uri` and ENDS with `complete`: " +
			"if you do not see `complete: true`, your host truncated the body — a command body is a PROCEDURE, so do not execute a prefix of it; read it in pages via `content_uri` with vp_read_resource.",
		Schema:  getCommandSchema,
		Handler: getResourceHandler(resolver, "command"),
	}
}

// GetSkillTool returns the MCP tool for vp_get_skill.
func GetSkillTool(resolver *vpctx.Resolver) mcp.Tool {
	return mcp.Tool{
		Name: "vp_get_skill",
		Description: "Get a skill's full markdown content by name, resolved via precedence. The result LEADS with `content_uri` and ENDS with `complete`: " +
			"if you do not see `complete: true`, your host truncated the body — read it in pages via `content_uri` with vp_read_resource.",
		Schema:  getSkillSchema,
		Handler: getResourceHandler(resolver, "skill"),
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
		content, source, err := resolver.ResolveScoped(resource, p.Project, p.Wing, p.Room)
		if err != nil {
			return nil, err
		}

		return getResourceResult{
			Name:       p.Name,
			ContentURI: resourceContentURI(resourceType, p),
			Source:     source,
			Content:    content,
			Complete:   true,
		}, nil
	}
}

// resourceContentURI returns the vibe-palace:// address of the body
// getResourceHandler is about to return, or "" when no URI addresses exactly
// those bytes.
//
// The gate is deliberately conservative. The command and skill URI templates
// carry {project}/{name} and nothing else, and their resolvers re-resolve with
// wing="" room="" — so for a wing- or room-scoped call the URI would name a
// DIFFERENT resolution than the one being returned. Emitting it anyway would
// hand the caller a handle that quietly fetches the wrong document, which is a
// worse failure than having no handle: a missing hatch is visible, a lying one
// is not.
func resourceContentURI(resourceType string, p getResourceParams) string {
	if p.Project == "" || p.Wing != "" || p.Room != "" {
		return ""
	}
	switch resourceType {
	case "command":
		return mcp.CommandURI(p.Project, p.Name)
	case "skill":
		return mcp.SkillURI(p.Project, p.Name)
	}
	return ""
}

// listParams is shared by list commands/skills handlers.
type listParams struct {
	Project string `json:"project,omitempty"`
	Wing    string `json:"wing,omitempty"`
	Room    string `json:"room,omitempty"`
}

func listResourceHandler(resolver *vpctx.Resolver, resourceType string) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p listParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}

		resources, err := resolver.ListResourcesScoped(resourceType, p.Project, p.Wing, p.Room)
		if err != nil {
			return nil, err
		}

		return listResourceResult{Resources: resources}, nil
	}
}
