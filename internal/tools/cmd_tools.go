// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
)

// cmdParams is the input for vp_cmd and vp_skill.
type cmdParams struct {
	Name    string `json:"name,omitempty"`
	Project string `json:"project,omitempty"`
	Wing    string `json:"wing,omitempty"`
	Room    string `json:"room,omitempty"`
}

// commandSummary is re-exported from internal/commands so MCP responses and
// the `vp commands list` CLI share one shape.
type commandSummary = commands.Summary

// commandAlias returns the "vpc-<name>" trigger token for a command.
func commandAlias(name string) string {
	return commands.Alias(name)
}

var cmdSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"name": {
			"type": "string",
			"description": "Command name (e.g. 'restart'). Omit to list available commands."
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
	}
}`)

var skillCmdSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"name": {
			"type": "string",
			"description": "Skill name. Omit to list available skills."
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
	}
}`)

// CmdTool returns the MCP tool for vp_cmd.
func CmdTool(resolver *vpctx.Resolver) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_cmd",
		Description: "Execute a command by name, or list available commands when called with no arguments. Commands are instructions for you to follow immediately.",
		Schema:      cmdSchema,
		Handler:     cmdExecHandler(resolver, "command"),
	}
}

// SkillCmdTool returns the MCP tool for vp_skill.
func SkillCmdTool(resolver *vpctx.Resolver) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_skill",
		Description: "Activate a skill by name, or list available skills when called with no arguments. Skills are behavioral guidelines to apply during this session.",
		Schema:      skillCmdSchema,
		Handler:     skillExecHandler(resolver),
	}
}

// skillExecHandler is the directory-form `vp_skill` handler. It resolves
// a skill via ResolveSkillDir so the emitted frame inlines the stripped
// SKILL.md body (frontmatter removed) and appends the list of
// references callers can fetch on-demand via vp_get_skill_section.
func skillExecHandler(resolver *vpctx.Resolver) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p cmdParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}

		name := strings.TrimSpace(p.Name)
		if name == "" {
			return buildDiscoveryList(resolver, p.Project, p.Wing, p.Room, "skill")
		}

		sd, source, err := resolver.ResolveSkillDir(name, p.Project, p.Wing, p.Room)
		if err != nil {
			// Caller fault: name is wrong. Point at discovery so the next turn
			// can list skills without reading vp.log. apperr.Caller keeps this
			// out of health amber-wash (correct friction, not an internal fault).
			return nil, apperr.Caller(fmt.Errorf("%w — call vp_skill with no name to list available skills", err))
		}

		return buildSkillFrame(frameParams{
			Name:         name,
			Project:      p.Project,
			Wing:         p.Wing,
			Room:         p.Room,
			Source:       source,
			Content:      string(sd.SkillMDBody),
			ResourceType: "skill",
		}, sd.ReferenceNames), nil
	}
}

// buildSkillFrame extends buildExecutionFrame with a trailing
// references list (only when non-empty). Reference names are sorted
// alphabetically by ResolveSkillDir; we re-sort defensively to keep
// output stable regardless of resolver changes.
func buildSkillFrame(p frameParams, refs []string) string {
	base := buildExecutionFrame(p)
	if len(refs) == 0 {
		return base
	}
	sorted := append([]string(nil), refs...)
	sort.Strings(sorted)
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\nReferences (fetch on demand via vp_get_skill_section):\n")
	for _, r := range sorted {
		fmt.Fprintf(&b, "  - %s\n", r)
	}
	return b.String()
}

func cmdExecHandler(resolver *vpctx.Resolver, resourceType string) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p cmdParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}

		name := strings.TrimSpace(p.Name)
		if name == "" {
			return buildDiscoveryList(resolver, p.Project, p.Wing, p.Room, resourceType)
		}

		resource := fmt.Sprintf("%s:%s", resourceType, name)
		content, source, err := resolver.ResolveScoped(resource, p.Project, p.Wing, p.Room)
		if err != nil {
			// Caller fault: name is wrong. Point at discovery so the next turn
			// can list commands without reading vp.log. apperr.Caller keeps this
			// out of health amber-wash (correct friction, not an internal fault).
			return nil, apperr.Caller(fmt.Errorf("%w — call vp_cmd with no name to list available commands", err))
		}

		return buildExecutionFrame(frameParams{
			Name:         name,
			Project:      p.Project,
			Wing:         p.Wing,
			Room:         p.Room,
			Source:       source,
			Content:      content,
			ResourceType: resourceType,
		}), nil
	}
}

// frameParams groups the arguments for buildExecutionFrame.
type frameParams struct {
	Name         string
	Project      string
	Wing         string
	Room         string
	Source       string
	Content      string
	ResourceType string
}

func buildExecutionFrame(p frameParams) string {
	proj := p.Project
	if proj == "" {
		proj = "(none)"
	}

	var label, verb, instructions string
	if p.ResourceType == "skill" {
		label = "ACTIVATE SKILL"
		verb = "skill"
		instructions = "The following describes a skill you should apply during this session.\nInternalize these guidelines and apply them when relevant."
	} else {
		label = "EXECUTE COMMAND"
		verb = "command"
		instructions = "The following are instructions for you to execute now. Follow each step.\nDo not merely summarize or describe these instructions — perform them."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "=== %s: %s ===\n", label, p.Name)

	// Build context line with optional wing/room.
	fmt.Fprintf(&b, "Project: %s", proj)
	if p.Wing != "" {
		fmt.Fprintf(&b, " | Wing: %s", p.Wing)
	}
	if p.Room != "" {
		fmt.Fprintf(&b, " | Room: %s", p.Room)
	}
	fmt.Fprintf(&b, " | Source: %s\n\n", p.Source)

	b.WriteString(instructions)
	b.WriteString("\n\n---\n\n")
	b.WriteString(p.Content)
	b.WriteString("\n\n---\n\n")
	fmt.Fprintf(&b, "End of %s: %s\n", verb, p.Name)

	return b.String()
}

func buildDiscoveryList(resolver *vpctx.Resolver, project, wing, room, resourceType string) (string, error) {
	resources, err := resolver.ListResourcesScoped(resourceType, project, wing, room)
	if err != nil {
		return "", err
	}

	if len(resources) == 0 {
		if resourceType == "skill" {
			return "No skills available.", nil
		}
		return "No commands available.", nil
	}

	summaries := make([]commandSummary, 0, len(resources))
	maxNameLen := 0
	for _, ri := range resources {
		cs := commandSummary{Name: ri.Name, Source: ri.Source}
		if resourceType == "command" {
			cs.Alias = commandAlias(ri.Name)
		}
		if content, _, err := resolver.ResolveScoped(fmt.Sprintf("%s:%s", resourceType, ri.Name), project, wing, room); err == nil {
			cs.Brief = extractBrief(content, 60)
		}
		summaries = append(summaries, cs)
		if len(ri.Name) > maxNameLen {
			maxNameLen = len(ri.Name)
		}
	}

	var b strings.Builder
	if project != "" {
		fmt.Fprintf(&b, "Available %ss for project %q:\n\n", resourceType, project)
	} else {
		fmt.Fprintf(&b, "Available %ss:\n\n", resourceType)
	}

	for _, cs := range summaries {
		fmt.Fprintf(&b, "  %-*s  [%-8s]  %s\n", maxNameLen, cs.Name, cs.Source, cs.Brief)
	}

	if resourceType == "skill" {
		b.WriteString("\nCall vp_skill with a skill name to activate it.\n")
	} else {
		b.WriteString("\nCall vp_cmd with a command name to execute it.\n")
	}

	return b.String(), nil
}

// extractBrief delegates to the shared helper in internal/commands so both
// MCP and CLI produce identical briefs.
func extractBrief(content string, maxLen int) string {
	return commands.ExtractBrief(content, maxLen)
}
