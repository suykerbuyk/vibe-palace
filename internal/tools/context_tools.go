// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// BootstrapResult is the response from vp_bootstrap_context.
type BootstrapResult struct {
	Project            string             `json:"project"`
	Workflow           string             `json:"workflow"`
	Resume             string             `json:"resume"`
	ActiveTasks        []storage.TaskMeta `json:"active_tasks"`
	RecentSessions     []sessionSummary   `json:"recent_sessions,omitempty"`
	KGSnapshot         *storage.KGStats   `json:"kg_snapshot,omitempty"`
	AvailableCommands  []commandSummary   `json:"available_commands,omitempty"`
	CommandInvocation  string             `json:"command_invocation,omitempty"`
}

// commandInvocationDirective is the single-line instruction telling the AI
// how to interpret a "vpc-<name>" alias typed by the user.
const commandInvocationDirective = "When the user types `vpc-<name>`, call `vp_cmd` with `name=<name>` and follow the returned instructions."

// sessionSummary is a lightweight view of SessionMeta for the bootstrap response.
type sessionSummary struct {
	Date      string `json:"date"`
	Iteration int    `json:"iteration"`
	Title     string `json:"title,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Tag       string `json:"tag,omitempty"`
}

type bootstrapParams struct {
	Project   string `json:"project"`
	MaxTokens int    `json:"max_tokens,omitempty"`
	Wing      string `json:"wing,omitempty"`
	Room      string `json:"room,omitempty"`
}

var bootstrapSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug. Required."
		},
		"max_tokens": {
			"type": "integer",
			"description": "Token budget for response. Default: 8000."
		},
		"wing": {
			"type": "string",
			"description": "Wing slug for palace-scoped command discovery."
		},
		"room": {
			"type": "string",
			"description": "Room slug for palace-scoped command discovery (requires wing)."
		}
	},
	"required": ["project"]
}`)

// BootstrapContextTool returns the MCP tool definition for vp_bootstrap_context.
func BootstrapContextTool(resolver *vpctx.Resolver, vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_bootstrap_context",
		Description: "Single-call context restoration: workflow + resume + tasks + recent sessions + KG snapshot.",
		Schema:      bootstrapSchema,
		Handler:     bootstrapHandler(resolver, vault),
	}
}

// AssembleBootstrap builds context restoration payload.
// Used by both the MCP tool handler and the CLI inject command.
// When wing/room are provided, palace-scoped commands are included in discovery.
func AssembleBootstrap(resolver *vpctx.Resolver, vault *storage.Vault, project string, maxTokens int, wing, room string) BootstrapResult {
	if maxTokens == 0 {
		maxTokens = 8000
	}

	result := BootstrapResult{Project: project}

	// Workflow — graceful on error.
	if wf, _, err := resolver.Resolve("workflow", project); err == nil {
		result.Workflow = wf
	}

	// Resume — graceful on error.
	if resume, _, err := resolver.Resolve("resume", project); err == nil {
		result.Resume = resume
	}

	// Active tasks — graceful on error.
	if tasks, err := vault.ListTasks(project, false); err == nil {
		result.ActiveTasks = tasks
	}

	// Recent sessions (last 5, most-recent-first) — graceful on error.
	if sessions, err := vault.ListSessions(project, "", "", 0); err == nil {
		if len(sessions) > 5 {
			sessions = sessions[len(sessions)-5:]
		}
		// Reverse for most-recent-first.
		for i, j := 0, len(sessions)-1; i < j; i, j = i+1, j-1 {
			sessions[i], sessions[j] = sessions[j], sessions[i]
		}
		for _, s := range sessions {
			result.RecentSessions = append(result.RecentSessions, sessionSummary{
				Date:      s.Date,
				Iteration: s.Iteration,
				Title:     s.Title,
				Summary:   s.Summary,
				Tag:       s.Tag,
			})
		}
	}

	// KG snapshot — Phase 7 may not exist yet, graceful.
	if stats, err := vault.KGStats(project); err == nil {
		result.KGSnapshot = &stats
	}

	// Available commands for discovery (palace-scoped when wing/room provided).
	if commands, err := resolver.ListResourcesScoped("command", project, wing, room); err == nil {
		for _, cmd := range commands {
			cs := commandSummary{Name: cmd.Name, Alias: commandAlias(cmd.Name), Source: cmd.Source}
			if content, _, err := resolver.ResolveScoped(fmt.Sprintf("command:%s", cmd.Name), project, wing, room); err == nil {
				cs.Brief = extractBrief(content, 60)
			}
			result.AvailableCommands = append(result.AvailableCommands, cs)
		}
		if len(result.AvailableCommands) > 0 {
			result.CommandInvocation = commandInvocationDirective
		}
	}

	// Token budget truncation: rough estimate 4 chars per token.
	// Truncate sessions first, then KG, then commands.
	raw, err := json.Marshal(result)
	if err == nil {
		estimatedTokens := len(raw) / 4
		for estimatedTokens > maxTokens && len(result.RecentSessions) > 0 {
			result.RecentSessions = result.RecentSessions[:len(result.RecentSessions)-1]
			raw, _ = json.Marshal(result)
			estimatedTokens = len(raw) / 4
		}
		if estimatedTokens > maxTokens && result.KGSnapshot != nil {
			result.KGSnapshot = nil
			raw, _ = json.Marshal(result)
			estimatedTokens = len(raw) / 4
		}
		if estimatedTokens > maxTokens && len(result.AvailableCommands) > 0 {
			result.AvailableCommands = nil
			result.CommandInvocation = ""
		}
	}

	return result
}

func bootstrapHandler(resolver *vpctx.Resolver, vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p bootstrapParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return AssembleBootstrap(resolver, vault, p.Project, p.MaxTokens, p.Wing, p.Room), nil
	}
}
