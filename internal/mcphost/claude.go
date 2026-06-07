// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcphost

import (
	"io"

	"github.com/suykerbuyk/vibe-palace/internal/plugin"
)

// ClaudeHost adapts the existing internal/plugin marketplace flow to the Host
// interface. It is a pure delegate: the marketplace generation, settings.json
// registration, and cache mirroring are unchanged, so the Claude install path
// behaves exactly as it did before the registry existed (no regression).
//
// Unlike the Grok/Zed hosts, ClaudeHost does NOT touch AGENTS.md on install —
// Claude's behavioral surface comes from the plugin's bundled command/skill
// shims, and adding AGENTS.md wiring here would change long-standing behavior.
type ClaudeHost struct{}

func (ClaudeHost) Name() string { return "claude" }
func (ClaudeHost) Flag() string { return "--claude-plugin" }

func (ClaudeHost) Detected() bool { return plugin.Detected() }

func (ClaudeHost) Installed() (bool, error) { return plugin.IsInstalled(), nil }

func (ClaudeHost) Install(version, _ string, out io.Writer) error {
	return plugin.InstallClaudePlugin(version, out)
}

func (ClaudeHost) Uninstall(out io.Writer) error {
	return plugin.UninstallClaudePlugin(out)
}
