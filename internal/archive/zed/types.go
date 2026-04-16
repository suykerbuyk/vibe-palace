// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package zed provides a read-only port of the subset of
// github.com/johns/vibe-vault/internal/zed needed to synthesize a
// Claude-shape JSONL transcript from a Zed agent-panel thread for
// the vibe-palace archive ledger.
//
// This package is deliberately scope-limited: it reads Zed's threads.db
// SQLite file, validates the schema, and returns normalized thread
// content. Session-note prose, narrative generation, and semantic
// indexing (all of which vv does) live elsewhere.
package zed

import (
	"encoding/json"
	"time"
)

// SupportedThreadVersion is the single Zed thread payload version this
// port was tested against. Threads carrying any other version cause
// ParseDB / QueryThread to fail loud rather than silently emit a
// mis-synthesized archive. See doc/adr/001-transcript-archive.md.
const SupportedThreadVersion = "0.3.0"

// ExpectedSchemaColumns is the column set that must be present on the
// threads table for this reader to operate. Missing columns fail the
// schema check; extra columns are tolerated (additive upstream changes
// are backward-compatible).
var ExpectedSchemaColumns = []string{
	"id",
	"summary",
	"updated_at",
	"data_type",
	"data",
	"parent_id",
	"worktree_branch",
}

// Thread combines DB-scalar fields with the zstd-decompressed JSON
// payload from the data BLOB. Fields mirror vv's shape but trimmed
// to what JSONL synthesis reads.
type Thread struct {
	// DB-sourced scalars.
	ID        string
	Summary   string
	UpdatedAt time.Time

	// JSON payload.
	Title           string              `json:"title"`
	Model           *Model              `json:"model"`
	RawMessages     []json.RawMessage   `json:"messages"`
	Messages        []Message           `json:"-"`
	Version         string              `json:"version"`
	ProjectSnapshot *ProjectSnapshot    `json:"initial_project_snapshot,omitempty"`
}

// Model identifies the LLM used in a Zed thread.
type Model struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Message is a normalized user/assistant turn extracted from Zed's
// Rust-style enum envelope ({"User": {...}} / {"Agent": {...}}).
type Message struct {
	Role    string    // "user" or "assistant"
	Content []Content // ordered content blocks
}

// Content is a polymorphic block. Only the fields relevant to the
// declared Type are populated.
type Content struct {
	Type string // "text", "thinking", "tool_use", "tool_result", "mention", "image"

	// text, thinking
	Text string

	// tool_use
	ToolName string
	ToolID   string
	// ToolInput holds the raw JSON bytes from the source; we preserve
	// them verbatim to keep synthesis deterministic (re-encoding a
	// map[string]any would reorder keys).
	ToolInput json.RawMessage

	// tool_result
	ToolUseID      string
	ToolResultText string
	ToolIsError    bool

	// mention
	MentionURI  string
	MentionText string
}

// ProjectSnapshot captures the workspace state at thread creation.
// Only kept for future-proofing; synthesis does not use it today.
type ProjectSnapshot struct {
	WorktreeSnapshots []WorktreeSnapshot `json:"worktree_snapshots"`
	Timestamp         string             `json:"timestamp"`
}

// WorktreeSnapshot captures a single worktree's state.
type WorktreeSnapshot struct {
	WorktreePath string `json:"abs_path"`
	GitBranch    string `json:"branch"`
	GitSHA       string `json:"sha"`
}

// ModelString returns the plain model name, or empty string if the
// thread has no model attached.
func (t *Thread) ModelString() string {
	if t.Model == nil {
		return ""
	}
	return t.Model.Model
}
