// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package plugin generates and registers vibe-palace as a Claude Code plugin
// served from a local marketplace. Registering the vibe-palace MCP server this
// way (rather than as a bare mcpServers entry) mirrors the vibe-vault plugin
// and makes the vp_* tools and the /vpc-* command shims available in every
// Claude Code session, not just when a project-local .mcp.json happens to be
// present.
package plugin

import (
	"os"
	"path/filepath"
)

const (
	// MarketplaceName is the key used in settings.json extraKnownMarketplaces
	// and known_marketplaces.json.
	MarketplaceName = "vibe-palace-local"

	// QualifiedName is the enabledPlugins / installed_plugins key, in
	// "plugin@marketplace" form.
	QualifiedName = "vibe-palace@vibe-palace-local"

	// pluginName is the plugin subdirectory name inside the marketplace.
	pluginName = "vibe-palace"

	pluginDescription = "Session capture, knowledge management, and project context for AI coding agents"
)

// mcpEnvPassthroughKeys lists environment variables propagated from the
// operator's shell into the MCP server subprocess via .mcp.json's env block.
// Claude Code expands ${VAR} references against the parent process env at spawn
// time, so the subprocess sees the operator's live key. Without this, LLM-backed
// handlers (session enrichment / synthesis) cannot reach the provider.
//
// XAI_API_KEY matters for the default config: vibe-palace routes enrichment via
// the OpenAI-compatible provider against https://api.x.ai/v1 (grok), so omitting
// it silently breaks enrichment on fresh installs. The other three are listed
// preemptively so an operator who switches providers gets the env-fallback path
// without editing .mcp.json; they expand to empty and are harmless when unset.
var mcpEnvPassthroughKeys = []string{
	"ANTHROPIC_API_KEY",
	"OPENAI_API_KEY",
	"GOOGLE_API_KEY",
	"XAI_API_KEY",
}

// MCPServerEntry returns the canonical .mcp.json entry for the vibe-palace MCP
// server. It is the single source of truth shared by Generate (marketplace
// source), InstallToCache (Claude Code plugin cache), and the cross-host
// registry in internal/mcphost (Grok/Zed) so the configs can never drift.
//
// The command is "vp" (PATH-relative) rather than an absolute binary path so
// the plugin always invokes whichever vp resolves first in PATH at session
// start. Baking an absolute path here is brittle: if the install was triggered
// by a stale binary, os.Executable would pin the plugin to it across rebuilds.
// PATH lookup converges on the operator's installed binary automatically.
func MCPServerEntry() map[string]any {
	env := make(map[string]any, len(mcpEnvPassthroughKeys))
	for _, key := range mcpEnvPassthroughKeys {
		env[key] = "${" + key + "}"
	}
	return map[string]any{
		"command": "vp",
		"args":    []any{"mcp"},
		"env":     env,
	}
}

// MCPEnvPassthroughKeys returns a copy of the provider-env keys propagated into
// the MCP server subprocess, in their canonical order. The registry uses this
// to build per-host env declarations (e.g. Grok's `--env KEY=${KEY}` flags)
// from the same source of truth as MCPServerEntry.
func MCPEnvPassthroughKeys() []string {
	return append([]string(nil), mcpEnvPassthroughKeys...)
}

// Detected reports whether Claude Code is present on this host (~/.claude/
// exists). Exported for the cross-host registry's status reporting.
func Detected() bool { return claudeDetected() }

// dataDir returns the vibe-palace data directory, honoring XDG_DATA_HOME and
// falling back to ~/.local/share/vibe-palace.
func dataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "vibe-palace")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "vibe-palace")
}

// MarketplaceDir returns the marketplace root directory. This is the directory
// registered in settings.json extraKnownMarketplaces.
func MarketplaceDir() string {
	return filepath.Join(dataDir(), "claude-plugin")
}

// PluginDir returns the plugin subdirectory inside the marketplace.
func PluginDir() string {
	return filepath.Join(MarketplaceDir(), pluginName)
}

// MarketplaceManifestPath returns the path to the marketplace manifest.
func MarketplaceManifestPath() string {
	return filepath.Join(MarketplaceDir(), ".claude-plugin", "marketplace.json")
}

// PluginManifestPath returns the path to the plugin manifest.
func PluginManifestPath() string {
	return filepath.Join(PluginDir(), ".claude-plugin", "plugin.json")
}

// MCPConfigPath returns the path to the plugin's MCP config.
func MCPConfigPath() string {
	return filepath.Join(PluginDir(), ".mcp.json")
}

// ClaudePluginsDir returns the Claude Code plugins directory (~/.claude/plugins/).
func ClaudePluginsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "plugins")
}

// CacheInstallDir returns the Claude Code cache install directory for a version.
func CacheInstallDir(version string) string {
	return filepath.Join(ClaudePluginsDir(), "cache", MarketplaceName, pluginName, version)
}

// KnownMarketplacesPath returns the path to known_marketplaces.json.
func KnownMarketplacesPath() string {
	return filepath.Join(ClaudePluginsDir(), "known_marketplaces.json")
}

// InstalledPluginsPath returns the path to installed_plugins.json.
func InstalledPluginsPath() string {
	return filepath.Join(ClaudePluginsDir(), "installed_plugins.json")
}

// IsInstalled returns true when all three generated marketplace files exist.
func IsInstalled() bool {
	for _, p := range []string{
		MarketplaceManifestPath(),
		PluginManifestPath(),
		MCPConfigPath(),
	} {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	return true
}
