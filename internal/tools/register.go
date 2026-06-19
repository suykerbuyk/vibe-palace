// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"github.com/suykerbuyk/vibe-palace/internal/capture"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// RegisterAll registers all tools with the MCP registry.
// If engine is nil, search tools and capture tools are not registered.
func RegisterAll(reg *mcp.Registry, resolver *vpctx.Resolver, vault *storage.Vault, engine *search.Engine, cfg ...storage.Config) {
	reg.MustRegister(BootstrapContextTool(resolver, vault))
	reg.MustRegister(GetCommandTool(resolver))
	reg.MustRegister(GetSkillTool(resolver))
	reg.MustRegister(ListCommandsTool(resolver))
	reg.MustRegister(ListSkillsTool(resolver))
	reg.MustRegister(CmdTool(resolver))
	reg.MustRegister(SkillCmdTool(resolver))
	reg.MustRegister(GetSkillSectionTool(resolver))
	reg.MustRegister(PalaceStatusTool(vault))
	reg.MustRegister(ListWingsTool(vault))
	reg.MustRegister(ListRoomsTool(vault))
	reg.MustRegister(TraverseTool(vault))
	reg.MustRegister(FindTunnelsTool(vault))
	reg.MustRegister(HealthTool(vault))
	reg.MustRegister(KGQueryTool(vault))
	reg.MustRegister(KGAddTool(vault))
	reg.MustRegister(KGInvalidateTool(vault))
	reg.MustRegister(KGTimelineTool(vault))
	reg.MustRegister(KGStatsTool(vault))
	reg.MustRegister(GetWorkflowTool(resolver))
	reg.MustRegister(GetResumeTool(resolver))
	reg.MustRegister(UpdateResumeTool(vault))
	reg.MustRegister(GetKnowledgeTool(vault))
	reg.MustRegister(ListProjectsTool(vault))
	reg.MustRegister(AppendIterationTool(vault))
	reg.MustRegister(ListTasksTool(vault))
	reg.MustRegister(GetTaskTool(vault))
	reg.MustRegister(ManageTaskTool(vault))
	reg.MustRegister(InitProjectTool(vault))
	reg.MustRegister(VaultSyncTool(vault))
	reg.MustRegister(VaultTidyTool(vault))
	reg.MustRegister(VaultReadTool(vault))
	reg.MustRegister(VaultListTool(vault))
	reg.MustRegister(VaultExistsTool(vault))
	reg.MustRegister(VaultSha256Tool(vault))
	reg.MustRegister(VaultWriteTool(vault))
	reg.MustRegister(VaultEditTool(vault))
	reg.MustRegister(VaultDeleteTool(vault))
	reg.MustRegister(VaultMoveTool(vault))
	reg.MustRegister(MemoryWriteTool(vault))
	reg.MustRegister(MemoryReadTool(vault))
	reg.MustRegister(MemoryListTool(vault))
	reg.MustRegister(MemoryDeleteTool(vault))
	reg.MustRegister(MemoryHarvestTool(vault))
	reg.MustRegister(IngestCommitMsgTool(vault))
	reg.MustRegister(ThreadInsertTool(vault))
	reg.MustRegister(ThreadReplaceTool(vault))
	reg.MustRegister(ThreadRemoveTool(vault))
	reg.MustRegister(CarriedAddTool(vault))
	reg.MustRegister(CarriedRemoveTool(vault))
	reg.MustRegister(CarriedPromoteToTaskTool(vault))
	reg.MustRegister(CollectWrapStateTool(vault))
	reg.MustRegister(StampIterTool(vault))
	reg.MustRegister(PreflightWrapTool(vault))
	if engine != nil {
		reg.MustRegister(SearchTool(engine))
		reg.MustRegister(SearchCrossProjectTool(engine))
		var c storage.Config
		if len(cfg) > 0 {
			c = cfg[0]
		}
		indexer := capture.NewIndexer(vault, engine, engine.Embedder(), c)
		reg.MustRegister(CaptureSessionTool(vault, indexer))
		reg.MustRegister(FrictionTrendsTool(vault))
		reg.MustRegister(SearchSessionsTool(vault))
		reg.MustRegister(GetSessionDetailTool(vault))
		reg.MustRegister(GetProjectContextTool(vault, resolver))
		reg.MustRegister(GetEffectivenessTool(vault))
		reg.MustRegister(RefreshIndexTool(engine))
	}
}
