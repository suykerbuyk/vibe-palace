// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// RegisterAll registers all tools with the MCP registry.
// If engine is nil, search tools are not registered.
func RegisterAll(reg *mcp.Registry, resolver *vpctx.Resolver, vault *storage.Vault, engine *search.Engine) {
	reg.MustRegister(BootstrapContextTool(resolver, vault))
	reg.MustRegister(GetCommandTool(resolver))
	reg.MustRegister(GetSkillTool(resolver))
	reg.MustRegister(ListCommandsTool(resolver))
	reg.MustRegister(ListSkillsTool(resolver))
	reg.MustRegister(CmdTool(resolver))
	reg.MustRegister(SkillCmdTool(resolver))
	if engine != nil {
		reg.MustRegister(SearchTool(engine))
		reg.MustRegister(SearchCrossProjectTool(engine))
	}
}
