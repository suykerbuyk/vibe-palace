// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// RegisterAll registers all context tools with the MCP registry.
func RegisterAll(reg *mcp.Registry, resolver *vpctx.Resolver, vault *storage.Vault) {
	reg.MustRegister(BootstrapContextTool(resolver, vault))
	reg.MustRegister(GetCommandTool(resolver))
	reg.MustRegister(GetSkillTool(resolver))
	reg.MustRegister(ListCommandsTool(resolver))
	reg.MustRegister(ListSkillsTool(resolver))
}
