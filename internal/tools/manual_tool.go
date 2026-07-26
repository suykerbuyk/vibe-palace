// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
)

// ---------------------------------------------------------------------------
// vp_manual
// ---------------------------------------------------------------------------

// ManualResult is the self-describing capability report returned by vp_manual.
// It is a SUPERSET of vp_get_doctrine: the same resolved doctrine, plus the
// live tool inventory, the project's commands and skills, and the server-level
// bootstrap instructions a connecting client receives.
//
// Nothing here is hand-authored: the tool list is projected from
// Registry.List() at dispatch time (so it can never drift from what is
// actually registered), the doctrine is resolved through the same 3-tier
// precedence vp_get_doctrine uses, and server_instructions echoes the exact
// mcp.ServerInstructions string. The one place a new capability must be
// declared is its own reg.MustRegister call — this tool reports it for free.
type ManualResult struct {
	Doctrine           doctrineResult   `json:"doctrine"`
	Tools              []mcp.ToolInfo   `json:"tools"`
	Commands           []commandSummary `json:"commands"`
	Skills             []skillSummary   `json:"skills"`
	ServerInstructions string           `json:"server_instructions"`
}

type manualParams struct {
	Project string `json:"project"`
	Wing    string `json:"wing,omitempty"`
	Room    string `json:"room,omitempty"`
}

var manualSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug. Doctrine and the command/skill inventory are resolved for this project (a project may override the embedded copies)."},
		"wing": {"type": "string", "description": "Wing slug for palace-scoped command/skill discovery."},
		"room": {"type": "string", "description": "Room slug for palace-scoped command/skill discovery (requires wing)."}
	},
	"required": ["project"]
}`)

// ManualTool serves the whole MCP surface as one self-describing, READ-ONLY
// report: doctrine + tool inventory + commands + skills + server instructions.
// It is a superset of vp_get_doctrine (which stays, since the doctrine alone is
// the common fetch) intended for a client that wants to see everything the
// server can do in a single call.
//
// reg is captured in the handler CLOSURE so Registry.List() runs at DISPATCH
// time — after RegisterAll has registered every tool, including this one —
// rather than at construction time, when the registry is still half-built.
func ManualTool(reg *mcp.Registry, resolver *vpctx.Resolver) mcp.Tool {
	return mcp.Tool{
		Name: "vp_manual",
		Description: "Self-describing capability manual for the whole MCP surface: " +
			"the resolved operating doctrine, the live tool inventory (every " +
			"registered tool's name, description, input schema, and mutating flag), " +
			"the project's available commands and skills, and the server bootstrap " +
			"instructions. Read-only superset of vp_get_doctrine. Fetch it to see " +
			"everything the vibe-palace server can do in one call.",
		Schema:  manualSchema,
		Handler: manualHandler(reg, resolver),
	}
}

func manualHandler(reg *mcp.Registry, resolver *vpctx.Resolver) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p manualParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}

		// Doctrine — resolved exactly as vp_get_doctrine does: same 3-tier
		// precedence, same digest, same always-present resource URI.
		content, source, sha, err := resolver.ResolveDigest("doctrine", p.Project)
		if err != nil {
			return nil, fmt.Errorf("resolve doctrine: %w", err)
		}
		doctrine := doctrineResult{
			resolveResult: resolveResult{
				Project: p.Project,
				Content: content,
				Source:  source,
				Sha256:  sha,
			},
			DoctrineURI: mcp.DoctrineURI(p.Project),
		}

		// Tool inventory — the live registry snapshot. Called at dispatch time
		// so every registered tool (this one included) is present.
		toolsList := reg.List()
		if toolsList == nil {
			toolsList = []mcp.ToolInfo{}
		}

		// Commands and skills — the SAME enumeration path vp_bootstrap_context
		// and vp_list_commands/vp_list_skills use, with the same brief extraction,
		// so the inventory here can never diverge from what those tools report.
		manualCmds := []commandSummary{}
		if cmds, err := resolver.ListResourcesScoped("command", p.Project, p.Wing, p.Room); err == nil {
			for _, cmd := range cmds {
				cs := commandSummary{Name: cmd.Name, Alias: commandAlias(cmd.Name), Source: cmd.Source}
				if body, _, err := resolver.ResolveScoped(fmt.Sprintf("command:%s", cmd.Name), p.Project, p.Wing, p.Room); err == nil {
					cs.Brief = extractBrief(body, 60)
				}
				manualCmds = append(manualCmds, cs)
			}
		}

		manualSkills := []skillSummary{}
		if skills, err := resolver.ListResourcesScoped("skill", p.Project, p.Wing, p.Room); err == nil {
			for _, sk := range skills {
				ss := skillSummary{Name: sk.Name, Alias: commands.SkillAlias(sk.Name), Source: sk.Source}
				if body, _, err := resolver.ResolveScoped(fmt.Sprintf("skill:%s", sk.Name), p.Project, p.Wing, p.Room); err == nil {
					ss.Brief = extractBrief(body, 60)
				}
				manualSkills = append(manualSkills, ss)
			}
		}

		return ManualResult{
			Doctrine:           doctrine,
			Tools:              toolsList,
			Commands:           manualCmds,
			Skills:             manualSkills,
			ServerInstructions: mcp.ServerInstructions,
		}, nil
	}
}
