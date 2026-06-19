// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// BootstrapResult is the response from vp_bootstrap_context.
type BootstrapResult struct {
	Project                   string             `json:"project"`
	Workflow                  string             `json:"workflow"`
	Resume                    string             `json:"resume"`
	ActiveTasks               []storage.TaskMeta `json:"active_tasks"`
	RecentSessions            []sessionSummary   `json:"recent_sessions,omitempty"`
	KGSnapshot                *storage.KGStats   `json:"kg_snapshot,omitempty"`
	Memory                    []memorySnapshot   `json:"memory,omitempty"`
	AvailableCommands         []commandSummary   `json:"available_commands,omitempty"`
	AvailableSkills           []skillSummary     `json:"available_skills,omitempty"`
	CommandInvocation         string             `json:"command_invocation,omitempty"`
	PostBootstrapInstructions string             `json:"post_bootstrap_instructions,omitempty"`
}

// skillSummary reuses the commandSummary shape; alias semantics differ
// (vps-<name> vs vpc-<name>) but the fields are identical.
type skillSummary = commandSummary

// commandInvocationDirective tells the AI how to interpret a "vpc-<name>" or
// "vps-<name>" alias typed by the user. References agentfile.CommandToolName
// so the block copy and this directive cannot drift.
var commandInvocationDirective = fmt.Sprintf(
	"When the user types `vpc-<name>`, call `%s` with `name=<name>` and follow the returned instructions. `vps-<name>` works the same way via `%s`.",
	agentfile.CommandToolName, agentfile.SkillToolName,
)

// memoryRecallCap bounds how many memory index entries the bootstrap surfaces.
// Recall is "index now, body on demand via vp_memory_read" — the cap keeps the
// curated index small so it sheds cheaply under a tight token budget.
const memoryRecallCap = 50

// memorySnapshot is a lightweight view of storage.MemoryMeta for the bootstrap
// response. It carries the index metadata only — never the body. Rel is
// included because vp_memory_read is keyed by rel, so the agent needs it to
// fetch a body on demand.
type memorySnapshot struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Rel         string `json:"rel"`
}

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
		Description: "Single-call context restoration: workflow + resume + tasks + recent sessions + KG snapshot + available commands + available skills + post-bootstrap capability-announcement directive.",
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

	// Memory index (capped) — bodies fetched on demand via vp_memory_read.
	// Graceful: a missing dir or read error must never hard-fail bootstrap.
	if mems, err := vault.ListMemories(project, memoryRecallCap); err == nil {
		for _, m := range mems {
			result.Memory = append(result.Memory, memorySnapshot{
				Name:        m.Name,
				Description: m.Description,
				Type:        m.Type,
				Rel:         m.Rel,
			})
		}
	}

	// Available commands for discovery (palace-scoped when wing/room provided).
	if cmds, err := resolver.ListResourcesScoped("command", project, wing, room); err == nil {
		for _, cmd := range cmds {
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

	// Available skills for discovery (palace-scoped when wing/room provided).
	if skills, err := resolver.ListResourcesScoped("skill", project, wing, room); err == nil {
		for _, sk := range skills {
			ss := skillSummary{Name: sk.Name, Alias: commands.SkillAlias(sk.Name), Source: sk.Source}
			if content, _, err := resolver.ResolveScoped(fmt.Sprintf("skill:%s", sk.Name), project, wing, room); err == nil {
				ss.Brief = extractBrief(content, 60)
			}
			result.AvailableSkills = append(result.AvailableSkills, ss)
		}
	}

	// PostBootstrapInstructions tells the model to announce capabilities after
	// the bootstrap summary. Populated server-side and excluded from truncation
	// so the directive fires even when the command list is shed — a degraded
	// "run vp_cmd to list commands" is still better than silent capability.
	result.PostBootstrapInstructions = renderPostBootstrapInstructions(result.AvailableCommands, result.AvailableSkills)

	// Token budget truncation: rough estimate 4 chars per token.
	// Shed order: sessions → memory → KG → commands+skills (as a pair).
	raw, err := json.Marshal(result)
	if err == nil {
		estimatedTokens := len(raw) / 4
		for estimatedTokens > maxTokens && len(result.RecentSessions) > 0 {
			result.RecentSessions = result.RecentSessions[:len(result.RecentSessions)-1]
			raw, _ = json.Marshal(result)
			estimatedTokens = len(raw) / 4
		}
		for estimatedTokens > maxTokens && len(result.Memory) > 0 {
			result.Memory = result.Memory[:len(result.Memory)-1]
			raw, _ = json.Marshal(result)
			estimatedTokens = len(raw) / 4
		}
		if estimatedTokens > maxTokens && result.KGSnapshot != nil {
			result.KGSnapshot = nil
			raw, _ = json.Marshal(result)
			estimatedTokens = len(raw) / 4
		}
		if estimatedTokens > maxTokens && (len(result.AvailableCommands) > 0 || len(result.AvailableSkills) > 0) {
			result.AvailableCommands = nil
			result.AvailableSkills = nil
			result.CommandInvocation = ""
			// PostBootstrapInstructions deliberately survives, but the examples
			// rendered pre-truncation point at aliases that just got shed.
			// Re-render so the directive degrades to "call vp_cmd / vp_skill
			// to list them" instead of dangling stale references.
			result.PostBootstrapInstructions = renderPostBootstrapInstructions(nil, nil)
		}
	}

	return result
}

// renderPostBootstrapInstructions returns a short directive telling the model
// to announce the available commands and skills after the bootstrap summary.
// Includes up to two live examples drawn from cmds (or a degraded fallback
// when nothing was enumerated) so the directive stays accurate without
// per-project hand-editing.
func renderPostBootstrapInstructions(cmds []commandSummary, skills []skillSummary) string {
	const base = "After presenting this bootstrap summary, tell the user in one or two lines which commands and skills are now available and how to invoke them (`vpc-<name>`, `vps-<name>`)."
	examples := make([]string, 0, 2)
	for i := 0; i < len(cmds) && len(examples) < 2; i++ {
		if cmds[i].Alias != "" {
			examples = append(examples, "`"+cmds[i].Alias+"`")
		}
	}
	if len(examples) == 0 {
		return base + " If no examples survived truncation, call `" + agentfile.CommandToolName + "` or `" + agentfile.SkillToolName + "` with no arguments to list them."
	}
	return base + " Examples from this project: " + joinExamples(examples) + "."
}

func joinExamples(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	default:
		return xs[0] + ", " + xs[1]
	}
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
