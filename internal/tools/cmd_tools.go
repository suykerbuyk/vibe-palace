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

// cmdParams is the input for vp_cmd and vp_skill.
type cmdParams struct {
	Name    string `json:"name,omitempty"`
	Project string `json:"project,omitempty"`
}

// commandSummary is a brief description of a command or skill for discovery
// and bootstrap responses.
type commandSummary struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Brief  string `json:"brief,omitempty"`
}

var cmdSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"name": {
			"type": "string",
			"description": "Command name (e.g. 'vp-restart'). Omit to list available commands."
		},
		"project": {
			"type": "string",
			"description": "Project slug for project-level resolution."
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
		Handler:     cmdExecHandler(resolver, "skill"),
	}
}

func cmdExecHandler(resolver *vpctx.Resolver, resourceType string) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p cmdParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}

		name := strings.TrimSpace(p.Name)
		if name == "" {
			return buildDiscoveryList(resolver, p.Project, resourceType)
		}

		resource := fmt.Sprintf("%s:%s", resourceType, name)
		content, source, err := resolver.Resolve(resource, p.Project)
		if err != nil {
			return nil, err
		}

		return buildExecutionFrame(name, p.Project, source, content, resourceType), nil
	}
}

func buildExecutionFrame(name, project, source, content, resourceType string) string {
	proj := project
	if proj == "" {
		proj = "(none)"
	}

	var label, verb, instructions string
	if resourceType == "skill" {
		label = "ACTIVATE SKILL"
		verb = "skill"
		instructions = "The following describes a skill you should apply during this session.\nInternalize these guidelines and apply them when relevant."
	} else {
		label = "EXECUTE COMMAND"
		verb = "command"
		instructions = "The following are instructions for you to execute now. Follow each step.\nDo not merely summarize or describe these instructions — perform them."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "=== %s: %s ===\n", label, name)
	fmt.Fprintf(&b, "Project: %s | Source: %s\n\n", proj, source)
	b.WriteString(instructions)
	b.WriteString("\n\n---\n\n")
	b.WriteString(content)
	b.WriteString("\n\n---\n\n")
	fmt.Fprintf(&b, "End of %s: %s\n", verb, name)

	return b.String()
}

func buildDiscoveryList(resolver *vpctx.Resolver, project, resourceType string) (string, error) {
	resources, err := resolver.ListResources(resourceType, project)
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
		if content, _, err := resolver.Resolve(fmt.Sprintf("%s:%s", resourceType, ri.Name), project); err == nil {
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

// extractBrief returns the first non-blank, non-heading line from content,
// truncated to maxLen characters.
func extractBrief(content string, maxLen int) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) > maxLen {
			return line[:maxLen]
		}
		return line
	}
	return "(no description)"
}
