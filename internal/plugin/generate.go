// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Generate creates the Claude Code plugin directory structure. The two-level
// layout matches the official marketplace convention:
//
//	~/.local/share/vibe-palace/claude-plugin/      ← marketplace root
//	  .claude-plugin/marketplace.json              ← marketplace manifest
//	  vibe-palace/                                 ← plugin subdirectory
//	    .claude-plugin/plugin.json                 ← plugin manifest
//	    .mcp.json                                  ← MCP server config
//
// Returns the marketplace root path on success. See mcpServerEntry for why the
// MCP command is PATH-relative.
func Generate(version string) (string, error) {
	mktDir := MarketplaceDir()
	plugDir := PluginDir()

	for _, dir := range []string{
		filepath.Join(mktDir, ".claude-plugin"),
		filepath.Join(plugDir, ".claude-plugin"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	marketplace := map[string]any{
		"$schema":     "https://anthropic.com/claude-code/marketplace.schema.json",
		"name":        MarketplaceName,
		"description": "Local vibe-palace plugin marketplace",
		"owner":       map[string]any{"name": "vibe-palace", "email": "noreply@vibe-palace.dev"},
		"plugins": []any{
			map[string]any{
				"name":        pluginName,
				"description": "MCP server for session capture and project context",
				"source":      "./" + pluginName,
			},
		},
	}
	if err := writeJSON(MarketplaceManifestPath(), marketplace); err != nil {
		return "", fmt.Errorf("write marketplace manifest: %w", err)
	}

	plugin := map[string]any{
		"name":        pluginName,
		"version":     version,
		"description": pluginDescription,
		"author":      map[string]any{"name": "vibe-palace"},
	}
	if err := writeJSON(PluginManifestPath(), plugin); err != nil {
		return "", fmt.Errorf("write plugin manifest: %w", err)
	}

	// MCP config via the shared MCPServerEntry helper so this writer and
	// InstallToCache cannot drift. See plugin.go for the env-passthrough and
	// PATH-relative-command rationale.
	mcpConfig := map[string]any{
		pluginName: MCPServerEntry(),
	}
	if err := writeJSON(MCPConfigPath(), mcpConfig); err != nil {
		return "", fmt.Errorf("write MCP config: %w", err)
	}

	return mktDir, nil
}

// Remove deletes the entire marketplace directory tree. Idempotent: returns nil
// if the directory does not exist.
func Remove() error {
	mktDir := MarketplaceDir()
	if _, err := os.Stat(mktDir); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(mktDir)
}

// writeJSON marshals v as pretty-printed JSON and writes it with 0o644.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
