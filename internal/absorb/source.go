// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package absorb

import (
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
)

// Source describes one legacy agent-context file type. The managed-block
// marker pattern is deliberately absent — it's a package-wide constant in
// internal/agentfile.
type Source interface {
	// DisplayName returns the repo-relative filename (e.g. "CLAUDE.md").
	DisplayName() string
	// Supported reports whether this adapter is ready to absorb. False
	// adapters trigger a "recognized but not yet supported" message.
	Supported() bool
	// SplitStrategy selects the splitter the planner should use.
	SplitStrategy() SplitStrategy
	// FixedDestination is consulted only for WholeFile strategies.
	// Returns Destination{} for heading-based adapters.
	FixedDestination() Destination
	// Preamble is the short post-rewrite preamble comment placed above
	// the managed block in the rewritten source file. LF line endings;
	// the writer translates to CRLF if the host file used CRLF.
	Preamble() string
}

// AllSources returns one Source per supported agent-context file type in
// the same order agentfile.Detect enumerates them.
func AllSources() []Source {
	return []Source{
		claudeMd{},
		agentsMd{},
		cursorrules{},
		rulesFile{},
		copilotMd{},
	}
}

// SourceForDisplayName returns the adapter matching name, or nil when
// there is no adapter. name should be the rel-path stored in
// agentfile.Target.DisplayName (e.g. "CLAUDE.md", ".cursorrules",
// ".github/copilot-instructions.md").
func SourceForDisplayName(name string) Source {
	for _, s := range AllSources() {
		if s.DisplayName() == name {
			return s
		}
	}
	// An agentfile.Target may pick a different primary via symlink dedup;
	// fall back to basename match for ".github/copilot-instructions.md"
	// vs plain "copilot-instructions.md" and similar edge cases.
	base := filepath.Base(name)
	for _, s := range AllSources() {
		if filepath.Base(s.DisplayName()) == base {
			return s
		}
	}
	return nil
}

const preambleNotice = "<!-- This file is managed by vibe-palace.\n" +
	"     Project knowledge lives in the palace vault. Edit vault files\n" +
	"     instead of adding content here; `vp check` will warn on drift. -->\n"

type claudeMd struct{}

func (claudeMd) DisplayName() string           { return "CLAUDE.md" }
func (claudeMd) Supported() bool               { return true }
func (claudeMd) SplitStrategy() SplitStrategy  { return SplitHeading }
func (claudeMd) FixedDestination() Destination { return Destination{} }
func (claudeMd) Preamble() string              { return preambleNotice }

type agentsMd struct{}

func (agentsMd) DisplayName() string           { return "AGENTS.md" }
func (agentsMd) Supported() bool               { return true }
func (agentsMd) SplitStrategy() SplitStrategy  { return SplitHeading }
func (agentsMd) FixedDestination() Destination { return Destination{} }
func (agentsMd) Preamble() string              { return preambleNotice }

type copilotMd struct{}

func (copilotMd) DisplayName() string           { return ".github/copilot-instructions.md" }
func (copilotMd) Supported() bool               { return true }
func (copilotMd) SplitStrategy() SplitStrategy  { return SplitHeading }
func (copilotMd) FixedDestination() Destination { return Destination{} }
func (copilotMd) Preamble() string              { return preambleNotice }

type cursorrules struct{}

func (cursorrules) DisplayName() string           { return ".cursorrules" }
func (cursorrules) Supported() bool               { return true }
func (cursorrules) SplitStrategy() SplitStrategy  { return SplitWholeFile }
func (cursorrules) FixedDestination() Destination { return DestKnowledge }
func (cursorrules) Preamble() string              { return preambleNotice }

type rulesFile struct{}

func (rulesFile) DisplayName() string           { return ".rules" }
func (rulesFile) Supported() bool               { return true }
func (rulesFile) SplitStrategy() SplitStrategy  { return SplitWholeFile }
func (rulesFile) FixedDestination() Destination { return DestKnowledge }
func (rulesFile) Preamble() string              { return preambleNotice }

// Compile-time guarantee that every adapter here matches an agentfile
// candidate. If agentfile adds a new candidate, AllSources must grow.
var _ = agentfile.Detect
