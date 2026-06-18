// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

// ProgressType identifies the kind of progress event.
type ProgressType int

const (
	ProgressProjectStart ProgressType = iota
	ProgressSessionDone
	ProgressSessionSkip
	ProgressProjectDone
	ProgressError
)

// ProgressEvent is emitted during migration to report progress.
type ProgressEvent struct {
	Type      ProgressType
	Project   string
	SessionID string
	File      string
	Message   string
	Current   int
	Total     int
}

// ProgressFunc is a callback for receiving progress events.
type ProgressFunc func(ProgressEvent)

// ImportResult summarizes the outcome of a migration run.
type ImportResult struct {
	ProjectsScanned  int
	SessionsImported int
	SessionsSkipped  int
	DrawersCreated   int
	EntitiesCreated  int
	TriplesCreated   int
	Errors           []ImportError
	// SlugRemap records collision-resolved renames (originalSlug → finalSlug).
	// Empty when no collisions occurred.
	SlugRemap map[string]string
	// Agentctx aggregates the per-project agentctx file copy when
	// ImportOptions.WithAgentctx is set. Zero-valued otherwise.
	Agentctx AgentctxResult
}

// AgentctxResult summarizes the agentctx file copy (resume/iterations/
// workflow/knowledge/tasks/memory + the verbatim migrated/ archive).
// All counts aggregate across every project processed in a run.
type AgentctxResult struct {
	Copied  int
	Skipped int
	Bytes   int64
	// CopiedPaths and SkippedPaths are vault-relative dest paths
	// (e.g. "Projects/foo/resume.md") for reporting and dry-run preview.
	CopiedPaths  []string
	SkippedPaths []string
	// CrownJewelSkipped lists Tier-1 singletons (resume/iterations/
	// workflow/knowledge) skipped because the destination already existed.
	// Surfaced LOUDLY so a placeholder never silently shadows real history.
	CrownJewelSkipped []string
}

// ImportError records a single failure during migration.
type ImportError struct {
	Project   string
	SessionID string
	File      string
	Err       error
}

// ImportOptions configures a migration run.
type ImportOptions struct {
	DryRun bool
	// Strict aborts the import on the first frontmatter parse error.
	// When false (default), parse errors are recorded as parse_failed
	// markers and the loop continues with a ProgressSessionSkip event.
	Strict   bool
	Progress ProgressFunc
	// Resolver decides slug collisions during the scan phase.
	// If nil, ImportVibeVault uses an AutoResolver.
	Resolver SlugResolver
	// WithAgentctx copies each project's agentctx tree (resume,
	// iterations, workflow, knowledge, tasks, memory, and the verbatim
	// migrated/ archive) into the destination vault, remapping the
	// vibe-vault agentctx/ layout onto the vibe-palace project layout.
	// Off by default: a plain run still imports sessions only.
	WithAgentctx bool
	// Force overwrites existing agentctx destination files. Default is
	// copy-if-absent. Use with care: a re-run with Force discards any
	// vault-side edits made since the prior import.
	Force bool
	// SkipSessions skips session + knowledge.md indexing entirely. Paired
	// with WithAgentctx for an agentctx-only run that loads no embedder.
	SkipSessions bool
	// OnlyProjects, when non-empty, restricts the run to source projects
	// whose slug matches an entry — either the slugified source directory
	// name (pre-remap) or the final post-remap slug. Empty processes every
	// project. Scopes both session import and agentctx copy, so a targeted
	// retirement does not fan out across an entire shared source vault.
	OnlyProjects []string
}
