// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// InstallToCache writes plugin.json and .mcp.json into the Claude Code plugin
// cache directory (belt-and-suspenders alongside the marketplace source).
// Returns the install path on success.
func InstallToCache(version string) (string, error) {
	installDir := CacheInstallDir(version)
	pluginMetaDir := filepath.Join(installDir, ".claude-plugin")

	if err := os.MkdirAll(pluginMetaDir, 0o755); err != nil {
		return "", fmt.Errorf("create cache directory: %w", err)
	}

	manifest := map[string]any{
		"name":        pluginName,
		"version":     version,
		"description": pluginDescription,
		"author":      map[string]any{"name": "vibe-palace"},
	}
	if err := writeJSON(filepath.Join(pluginMetaDir, "plugin.json"), manifest); err != nil {
		return "", fmt.Errorf("write cache plugin.json: %w", err)
	}

	mcpConfig := map[string]any{
		pluginName: MCPServerEntry(),
	}
	if err := writeJSON(filepath.Join(installDir, ".mcp.json"), mcpConfig); err != nil {
		return "", fmt.Errorf("write cache .mcp.json: %w", err)
	}

	return installDir, nil
}

// RemoveFromCache removes the vibe-palace-local cache directory. Idempotent:
// returns nil if the directory does not exist.
func RemoveFromCache() error {
	cacheDir := filepath.Join(ClaudePluginsDir(), "cache", MarketplaceName)
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(cacheDir)
}

// RegisterKnownMarketplace adds or updates our entry in known_marketplaces.json,
// preserving all existing entries.
func RegisterKnownMarketplace(marketplaceDir string) error {
	path := KnownMarketplacesPath()

	data, err := readJSONFile(path)
	if err != nil {
		return err
	}

	data[MarketplaceName] = map[string]any{
		"source":          map[string]any{"source": "directory", "path": marketplaceDir},
		"installLocation": marketplaceDir,
		"lastUpdated":     time.Now().UTC().Format(time.RFC3339Nano),
	}

	return writeJSONSecure(path, data)
}

// UnregisterKnownMarketplace removes our entry from known_marketplaces.json.
// Idempotent: returns nil if the entry or file does not exist.
func UnregisterKnownMarketplace() error {
	path := KnownMarketplacesPath()

	data, err := readJSONFile(path)
	if err != nil {
		return nil // file doesn't exist or unreadable — nothing to remove
	}

	if _, ok := data[MarketplaceName]; !ok {
		return nil
	}

	delete(data, MarketplaceName)
	return writeJSONSecure(path, data)
}

// RegisterInstalledPlugin adds or updates our entry in installed_plugins.json,
// preserving the schema version field and all other plugin entries.
func RegisterInstalledPlugin(installPath, version string) error {
	path := InstalledPluginsPath()

	data, err := readJSONFile(path)
	if err != nil {
		return err
	}

	if _, ok := data["version"]; !ok {
		data["version"] = float64(2)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	data[QualifiedName] = []any{
		map[string]any{
			"scope":        "user",
			"installPath":  installPath,
			"version":      version,
			"installedAt":  now,
			"lastUpdated":  now,
			"gitCommitSha": "",
		},
	}

	return writeJSONSecure(path, data)
}

// UnregisterInstalledPlugin removes our entry from installed_plugins.json.
// Idempotent: returns nil if the entry or file does not exist.
func UnregisterInstalledPlugin() error {
	path := InstalledPluginsPath()

	data, err := readJSONFile(path)
	if err != nil {
		return nil // file doesn't exist or unreadable — nothing to remove
	}

	if _, ok := data[QualifiedName]; !ok {
		return nil
	}

	delete(data, QualifiedName)
	return writeJSONSecure(path, data)
}

// readJSONFile reads a JSON file into a map. Returns an empty map if the file
// does not exist.
func readJSONFile(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return make(map[string]any), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

// writeJSONSecure writes a map as pretty-printed JSON with 0o600 permissions,
// creating parent directories as needed.
func writeJSONSecure(path string, v map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
