// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package plugin

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// InstallClaudePlugin deploys vibe-palace as a Claude Code plugin: it generates
// the marketplace files, registers the marketplace + enabled plugin in
// settings.json, and mirrors the manifests into Claude Code's plugin cache and
// registry files (belt-and-suspenders). It does NOT touch any existing
// mcpServers or hook entries — they coexist safely.
//
// Idempotent: when the plugin is already configured in settings.json the
// function reports that and returns without rewriting. Progress is written to
// out (pass os.Stderr from a CLI; nil discards).
func InstallClaudePlugin(version string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if !claudeDetected() {
		return fmt.Errorf("~/.claude/ not found — Claude Code not detected")
	}

	mktDir, err := Generate(version)
	if err != nil {
		return fmt.Errorf("generate plugin: %w", err)
	}

	path, err := settingsPath()
	if err != nil {
		return err
	}

	settings, err := loadSettings(path)
	if err != nil {
		return err
	}

	if isPluginInstalled(settings) {
		fmt.Fprintf(out, "vibe-palace plugin already configured in %s\n", compressHome(path))
		return nil
	}

	if err := backupSettings(path); err != nil {
		return err
	}

	addPluginMarketplace(settings, mktDir)
	addPluginEnabled(settings)

	if err := storeSettings(path, settings); err != nil {
		return err
	}

	// Mirror into Claude Code's internal cache + registries (best effort).
	var cacheDetail string
	installPath, cacheErr := InstallToCache(version)
	if cacheErr != nil {
		fmt.Fprintf(out, "  warning: cache install: %v\n", cacheErr)
		cacheDetail = "(cache install failed)"
	} else {
		if err := RegisterKnownMarketplace(mktDir); err != nil {
			fmt.Fprintf(out, "  warning: known_marketplaces: %v\n", err)
		}
		if err := RegisterInstalledPlugin(installPath, version); err != nil {
			fmt.Fprintf(out, "  warning: installed_plugins: %v\n", err)
		}
		cacheDetail = compressHome(installPath)
	}

	fmt.Fprintf(out, "vibe-palace plugin installed:\n")
	fmt.Fprintf(out, "  Plugin files: %s\n", compressHome(mktDir))
	fmt.Fprintf(out, "  Plugin cache: %s\n", cacheDetail)
	fmt.Fprintf(out, "  Settings:     %s\n", compressHome(path))
	fmt.Fprintf(out, "Restart Claude Code to activate.\n")
	return nil
}

// UninstallClaudePlugin removes the vibe-palace plugin configuration and files.
// Idempotent: reports "not found" when nothing was installed.
func UninstallClaudePlugin(out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	path, err := settingsPath()
	if err != nil {
		return err
	}

	settings, err := loadSettings(path)
	if err != nil {
		return err
	}

	hadPlugin := isPluginInstalled(settings)
	if hadPlugin {
		if err := backupSettings(path); err != nil {
			return err
		}
		removePluginMarketplace(settings)
		removePluginEnabled(settings)
		if err := storeSettings(path, settings); err != nil {
			return err
		}
	}

	if err := Remove(); err != nil {
		return fmt.Errorf("remove plugin directory: %w", err)
	}

	// Clean up Claude Code internal files (best effort).
	_ = UnregisterInstalledPlugin()
	_ = UnregisterKnownMarketplace()
	_ = RemoveFromCache()

	if !hadPlugin && !IsInstalled() {
		fmt.Fprintf(out, "vibe-palace plugin not found in %s\n", compressHome(path))
		return nil
	}

	fmt.Fprintf(out, "vibe-palace plugin removed from %s\n", compressHome(path))
	return nil
}

// --- settings.json plumbing ---

// claudeDetected reports whether ~/.claude/ exists.
func claudeDetected() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(home, ".claude"))
	return err == nil && info.IsDir()
}

// settingsPath returns ~/.claude/settings.json.
func settingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// loadSettings reads settings.json into a map, returning an empty map when the
// file does not exist.
func loadSettings(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return make(map[string]any), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if data == nil {
		data = make(map[string]any)
	}
	return data, nil
}

// storeSettings writes settings.json as pretty-printed JSON with 0o644 (it is
// human-edited, so not 0o600).
func storeSettings(path string, data map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// backupSettings copies path to path+".vp.bak" before mutation. No-op when the
// file does not yet exist.
func backupSettings(path string) error {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s for backup: %w", path, err)
	}
	return os.WriteFile(path+".vp.bak", raw, 0o644)
}

// compressHome replaces the home-directory prefix of p with "~" for display.
func compressHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}

// --- settings.json marketplace/enabledPlugins helpers ---

func isPluginInstalled(settings map[string]any) bool {
	return isPluginMarketplaceInstalled(settings) && isPluginEnabled(settings)
}

func isPluginMarketplaceInstalled(settings map[string]any) bool {
	mkts, ok := settings["extraKnownMarketplaces"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = mkts[MarketplaceName]
	return ok
}

func addPluginMarketplace(settings map[string]any, marketplaceDir string) {
	mkts, ok := settings["extraKnownMarketplaces"].(map[string]any)
	if !ok {
		mkts = make(map[string]any)
		settings["extraKnownMarketplaces"] = mkts
	}
	mkts[MarketplaceName] = map[string]any{
		"source": map[string]any{
			"source": "directory",
			"path":   marketplaceDir,
		},
	}
}

func removePluginMarketplace(settings map[string]any) {
	mkts, ok := settings["extraKnownMarketplaces"].(map[string]any)
	if !ok {
		return
	}
	delete(mkts, MarketplaceName)
	if len(mkts) == 0 {
		delete(settings, "extraKnownMarketplaces")
	}
}

func isPluginEnabled(settings map[string]any) bool {
	plugins, ok := settings["enabledPlugins"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = plugins[QualifiedName]
	return ok
}

func addPluginEnabled(settings map[string]any) {
	plugins, ok := settings["enabledPlugins"].(map[string]any)
	if !ok {
		plugins = make(map[string]any)
		settings["enabledPlugins"] = plugins
	}
	plugins[QualifiedName] = true
}

func removePluginEnabled(settings map[string]any) {
	plugins, ok := settings["enabledPlugins"].(map[string]any)
	if !ok {
		return
	}
	delete(plugins, QualifiedName)
	if len(plugins) == 0 {
		delete(settings, "enabledPlugins")
	}
}
