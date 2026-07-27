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

// registerOptions holds the optional, transport-specific knobs threaded into
// RegisterAll, as functional options rather than a trailing variadic.
//
// It once carried a second knob, WithBootstrapSlimDefault, which made the
// HTTP transport reduce every resume to a 4,000-byte prefix. It was justified
// by "that channel truncates large inline results" — a bound nobody ever
// measured. The only refusal on record is ~62,463 characters, 15x the cap, and
// payloads were under it either way. Knob and mechanism were deleted together.
type registerOptions struct {
	cfg storage.Config
}

// RegisterOption configures RegisterAll. The zero set of options preserves the
// default behaviour (empty config).
type RegisterOption func(*registerOptions)

// WithConfig supplies the storage.Config used to build the capture indexer.
// Mirrors the old trailing cfg argument.
func WithConfig(cfg storage.Config) RegisterOption {
	return func(o *registerOptions) { o.cfg = cfg }
}

// RegisterAll registers all tools with the MCP registry.
// If engine is nil, search tools and capture tools are not registered.
func RegisterAll(reg *mcp.Registry, resolver *vpctx.Resolver, vault *storage.Vault, engine *search.Engine, opts ...RegisterOption) {
	var o registerOptions
	for _, opt := range opts {
		opt(&o)
	}

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
	reg.MustRegister(GetDoctrineTool(resolver))
	reg.MustRegister(ManualTool(reg, resolver))
	reg.MustRegister(GetResumeTool(resolver))
	reg.MustRegister(UpdateResumeTool(vault))
	reg.MustRegister(GetKnowledgeTool(vault))
	reg.MustRegister(ListLearningsTool(vault))
	reg.MustRegister(GetLearningTool(vault))
	reg.MustRegister(ListProjectsTool(vault))
	reg.MustRegister(AuditVaultTool(vault))
	reg.MustRegister(ArchiveLinkTool(vault))
	reg.MustRegister(AppendIterationTool(vault))
	reg.MustRegister(ListTasksTool(vault))
	reg.MustRegister(GetTaskTool(vault))
	reg.MustRegister(ManageTaskTool(vault))
	reg.MustRegister(ReadResourceTool(resolver, vault))
	reg.MustRegister(InitProjectTool(vault))
	reg.MustRegister(VaultSyncTool(vault))
	reg.MustRegister(VaultTidyTool(vault))
	reg.MustRegister(VaultStatusTool(vault))
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
	reg.MustRegister(ArchiveCommitLogTool(vault))
	reg.MustRegister(CollectWrapStateTool(vault))
	reg.MustRegister(StampIterTool(vault))
	reg.MustRegister(PreflightWrapTool(vault))
	reg.MustRegister(SurfaceCheckTool(vault))
	reg.MustRegister(ResumeRefsTool(vault))
	reg.MustRegister(ScanPlansTool(vault))
	if engine != nil {
		reg.MustRegister(SearchTool(engine))
		reg.MustRegister(SearchCrossProjectTool(engine))
		indexer := capture.NewIndexer(vault, engine, engine.Embedder(), o.cfg)
		reg.MustRegister(CaptureSessionTool(vault, indexer))
		reg.MustRegister(FrictionTrendsTool(vault))
		reg.MustRegister(SearchSessionsTool(vault))
		reg.MustRegister(GetSessionDetailTool(vault))
		reg.MustRegister(GetProjectContextTool(vault, resolver))
		reg.MustRegister(GetEffectivenessTool(vault))
		reg.MustRegister(RefreshIndexTool(engine))
	}
}
