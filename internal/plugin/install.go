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

	"github.com/suykerbuyk/vibe-palace/internal/shims"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// InstallClaudePlugin deploys vibe-palace as a Claude Code plugin: it generates
// the marketplace files, mirrors them into Claude Code's plugin cache and
// registries, and ensures settings.json enables the marketplace plugin. It
// does NOT touch any existing mcpServers or hook entries — they coexist safely.
//
// stamp is the cache/plugin identity (prefer SurfaceStamp(version, commit)).
// Progress is written to out (pass os.Stderr from a CLI; nil discards).
//
// Refresh vs first-enable (Phase 0.5 / C1):
//   - Generate, InstallToCache, and registry writes ALWAYS run so a re-install
//     after `make install` refreshes on-disk plugin files for hosts that are
//     already configured.
//   - settings.json enablement is gated: when the plugin is already enabled we
//     skip mutating settings (no backup, no rewrite) and report a refresh.
func InstallClaudePlugin(stamp string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if !claudeDetected() {
		return fmt.Errorf("~/.claude/ not found — Claude Code not detected")
	}
	if stamp == "" {
		stamp = "dev"
	}

	mktDir, err := Generate(stamp)
	if err != nil {
		return fmt.Errorf("generate plugin: %w", err)
	}

	// Always mirror into Claude Code's internal cache + registries so an
	// already-enabled host still picks up refreshed plugin bytes (C1).
	var cacheDetail string
	installPath, cacheErr := InstallToCache(stamp)
	if cacheErr != nil {
		fmt.Fprintf(out, "  warning: cache install: %v\n", cacheErr)
		cacheDetail = "(cache install failed)"
		installPath = ""
	} else {
		if err := pruneOtherCacheStamps(stamp); err != nil {
			fmt.Fprintf(out, "  warning: prune old cache stamps: %v\n", err)
		}
		if err := RegisterKnownMarketplace(mktDir); err != nil {
			fmt.Fprintf(out, "  warning: known_marketplaces: %v\n", err)
		}
		if err := RegisterInstalledPlugin(installPath, stamp); err != nil {
			fmt.Fprintf(out, "  warning: installed_plugins: %v\n", err)
		}
		cacheDetail = compressHome(installPath)
	}

	// Phase 2: thin command/skill shims into the plugin package (Plan/Apply).
	emitClaudeSurfaces(out, PluginDir(), installPath)

	path, err := settingsPath()
	if err != nil {
		return err
	}

	settings, err := loadSettings(path)
	if err != nil {
		return err
	}

	if isPluginInstalled(settings) {
		// Refresh path: files and registries updated above; settings untouched.
		fmt.Fprintf(out, "vibe-palace plugin refreshed (already enabled in %s):\n", compressHome(path))
		fmt.Fprintf(out, "  Plugin files: %s\n", compressHome(mktDir))
		fmt.Fprintf(out, "  Plugin cache: %s\n", cacheDetail)
		fmt.Fprintf(out, "  Stamp:        %s\n", stamp)
		fmt.Fprintf(out, "Restart Claude Code if it was already running.\n")
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

	fmt.Fprintf(out, "vibe-palace plugin installed:\n")
	fmt.Fprintf(out, "  Plugin files: %s\n", compressHome(mktDir))
	fmt.Fprintf(out, "  Plugin cache: %s\n", cacheDetail)
	fmt.Fprintf(out, "  Settings:     %s\n", compressHome(path))
	fmt.Fprintf(out, "  Stamp:        %s\n", stamp)
	fmt.Fprintf(out, "Restart Claude Code to activate.\n")
	return nil
}

// emitClaudeSurfaces writes vpc-* / vps-* shims into the marketplace plugin
// and optional cache copy. Best-effort: failures are printed, not fatal.
// Vault root comes from global config only (machine-wide menu; M1).
func emitClaudeSurfaces(out io.Writer, marketplacePlugin, cachePlugin string) {
	vaultRoot, vaultSrc, _ := storage.ResolveGlobalVaultPath()
	if vaultSrc != "" {
		fmt.Fprintf(out, "  Vault for menu: %s (%s)\n", compressHome(vaultRoot), vaultSrc)
	}
	rep := shims.InstallGlobalSurfaces(shims.GlobalInstallOptions{
		VaultRoot:         vaultRoot,
		VaultSource:       vaultSrc,
		ClaudePluginRoot:  marketplacePlugin,
		ClaudeCacheRoot:   cachePlugin,
		AllowStaleRemoval: true,
	})
	for _, e := range rep.Errors {
		fmt.Fprintf(out, "  warning: host surfaces: %s\n", e)
	}
	// Prefer cache counts when present (operative copy); else marketplace.
	cmd, skl := rep.ClaudeCacheCmd, rep.ClaudeCacheSkl
	if cmd.Added+cmd.Updated+cmd.Unchanged+cmd.Removed == 0 {
		cmd, skl = rep.ClaudeCommands, rep.ClaudeSkills
	}
	fmt.Fprintf(out, "  %s\n", shims.FormatApplyCounts("Command shims", cmd))
	fmt.Fprintf(out, "  %s\n", shims.FormatApplyCounts("Skill shims", skl))
	if n := rep.RemovedTotal(); n > 0 {
		fmt.Fprintf(out, "  warning: removed %d stale managed shim file(s) from user-global trees\n", n)
	}
}

// pruneOtherCacheStamps removes sibling stamp directories under the Claude
// cache plugin tree, keeping only current. Best-effort: missing parent is fine.
func pruneOtherCacheStamps(keep string) error {
	parent := filepath.Join(ClaudePluginsDir(), "cache", MarketplaceName, pluginName)
	entries, err := os.ReadDir(parent)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if !ent.IsDir() || ent.Name() == keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(parent, ent.Name())); err != nil {
			return err
		}
	}
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
