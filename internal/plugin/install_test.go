// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package plugin

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateWithClaude isolates HOME/XDG and creates ~/.claude/ so claudeDetected
// passes. Returns the home dir.
func isolateWithClaude(t *testing.T) string {
	t.Helper()
	home := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir ~/.claude: %v", err)
	}
	return home
}

func settingsFile(t *testing.T) string {
	t.Helper()
	p, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInstallClaudePlugin_FreshSettings(t *testing.T) {
	isolateWithClaude(t)
	if err := InstallClaudePlugin("1.0.0", io.Discard); err != nil {
		t.Fatalf("InstallClaudePlugin: %v", err)
	}

	// Marketplace files generated.
	if !IsInstalled() {
		t.Error("IsInstalled() = false after install")
	}

	// settings.json carries both registration keys.
	s := readJSONMap(t, settingsFile(t))
	mkts, ok := s["extraKnownMarketplaces"].(map[string]any)
	if !ok || mkts[MarketplaceName] == nil {
		t.Errorf("extraKnownMarketplaces missing %q: %v", MarketplaceName, s["extraKnownMarketplaces"])
	}
	plugins, ok := s["enabledPlugins"].(map[string]any)
	if !ok || plugins[QualifiedName] != true {
		t.Errorf("enabledPlugins missing %q=true: %v", QualifiedName, s["enabledPlugins"])
	}

	// Cache + registries mirrored.
	if _, err := os.Stat(CacheInstallDir("1.0.0")); err != nil {
		t.Errorf("cache install dir missing: %v", err)
	}
	if km := readJSONMap(t, KnownMarketplacesPath()); km[MarketplaceName] == nil {
		t.Error("known_marketplaces.json missing our entry")
	}
	if ip := readJSONMap(t, InstalledPluginsPath()); ip[QualifiedName] == nil {
		t.Error("installed_plugins.json missing our entry")
	}
}

func TestInstallClaudePlugin_RequiresClaude(t *testing.T) {
	isolate(t) // no ~/.claude created
	err := InstallClaudePlugin("1.0.0", io.Discard)
	if err == nil {
		t.Fatal("expected error when ~/.claude absent, got nil")
	}
}

func TestInstallClaudePlugin_PreservesExistingSettings(t *testing.T) {
	isolateWithClaude(t)
	// Seed settings with a foreign hook + marketplace the install must keep.
	seed := map[string]any{
		"hooks": map[string]any{
			"Stop": []any{map[string]any{"matcher": "", "hooks": []any{
				map[string]any{"type": "command", "command": "vp hook"},
			}}},
		},
		"extraKnownMarketplaces": map[string]any{
			"other-local": map[string]any{"source": map[string]any{"source": "directory", "path": "/other"}},
		},
	}
	if err := storeSettings(settingsFile(t), seed); err != nil {
		t.Fatal(err)
	}

	if err := InstallClaudePlugin("1.0.0", io.Discard); err != nil {
		t.Fatalf("InstallClaudePlugin: %v", err)
	}

	s := readJSONMap(t, settingsFile(t))
	if s["hooks"] == nil {
		t.Error("existing hooks block dropped")
	}
	mkts := s["extraKnownMarketplaces"].(map[string]any)
	if mkts["other-local"] == nil {
		t.Error("foreign marketplace entry dropped")
	}
	if mkts[MarketplaceName] == nil {
		t.Error("our marketplace entry missing")
	}
}

func TestInstallClaudePlugin_BacksUp(t *testing.T) {
	isolateWithClaude(t)
	if err := storeSettings(settingsFile(t), map[string]any{"foo": "bar"}); err != nil {
		t.Fatal(err)
	}
	if err := InstallClaudePlugin("1.0.0", io.Discard); err != nil {
		t.Fatalf("InstallClaudePlugin: %v", err)
	}
	if _, err := os.Stat(settingsFile(t) + ".vp.bak"); err != nil {
		t.Errorf("backup file missing: %v", err)
	}
}

func TestInstallClaudePlugin_IdempotentSettings(t *testing.T) {
	// Second install must not rewrite settings.json when already enabled (C1:
	// only enablement is gated). Cache/marketplace files still refresh.
	isolateWithClaude(t)
	if err := InstallClaudePlugin("1.0.0", io.Discard); err != nil {
		t.Fatalf("install 1: %v", err)
	}
	before, err := os.ReadFile(settingsFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallClaudePlugin("1.0.0", io.Discard); err != nil {
		t.Fatalf("install 2: %v", err)
	}
	after, err := os.ReadFile(settingsFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("second install mutated settings.json (enablement must stay gated)")
	}
}

func TestInstallClaudePlugin_RefreshWhenAlreadyEnabled(t *testing.T) {
	// C1: already-enabled host still gets Generate + InstallToCache + register.
	// C2: a new stamp lands a new cache dir and prunes the old one.
	isolateWithClaude(t)
	if err := InstallClaudePlugin("stamp-a", io.Discard); err != nil {
		t.Fatalf("install stamp-a: %v", err)
	}
	oldCache := CacheInstallDir("stamp-a")
	if _, err := os.Stat(oldCache); err != nil {
		t.Fatalf("stamp-a cache missing: %v", err)
	}

	// Poison marketplace MCP config so a no-op refresh would leave it wrong.
	poison := []byte(`{"poisoned":true}` + "\n")
	if err := os.WriteFile(MCPConfigPath(), poison, 0o644); err != nil {
		t.Fatal(err)
	}

	var buf strings.Builder
	if err := InstallClaudePlugin("stamp-b", &buf); err != nil {
		t.Fatalf("install stamp-b: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "refreshed") {
		t.Errorf("expected refresh message, got %q", out)
	}
	if _, err := os.Stat(CacheInstallDir("stamp-b")); err != nil {
		t.Errorf("stamp-b cache missing after refresh: %v", err)
	}
	if _, err := os.Stat(oldCache); !os.IsNotExist(err) {
		t.Errorf("stamp-a cache still present after prune: %v", err)
	}
	// Generate rewrote marketplace MCP config (no longer poisoned).
	raw, err := os.ReadFile(MCPConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "poisoned") {
		t.Error("marketplace .mcp.json still poisoned — Generate did not refresh")
	}
	// installed_plugins points at the new stamp.
	ip := readJSONMap(t, InstalledPluginsPath())
	entries, ok := ip[QualifiedName].([]any)
	if !ok || len(entries) == 0 {
		t.Fatalf("installed_plugins missing %q: %v", QualifiedName, ip[QualifiedName])
	}
	entry := entries[0].(map[string]any)
	if entry["version"] != "stamp-b" {
		t.Errorf("installed version = %v, want stamp-b", entry["version"])
	}
	if entry["installPath"] != CacheInstallDir("stamp-b") {
		t.Errorf("installPath = %v, want %s", entry["installPath"], CacheInstallDir("stamp-b"))
	}
}

func TestUninstallClaudePlugin_Reverses(t *testing.T) {
	isolateWithClaude(t)
	if err := InstallClaudePlugin("1.0.0", io.Discard); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := UninstallClaudePlugin(io.Discard); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	s := readJSONMap(t, settingsFile(t))
	if s["extraKnownMarketplaces"] != nil {
		t.Errorf("extraKnownMarketplaces not cleaned: %v", s["extraKnownMarketplaces"])
	}
	if s["enabledPlugins"] != nil {
		t.Errorf("enabledPlugins not cleaned: %v", s["enabledPlugins"])
	}
	if IsInstalled() {
		t.Error("marketplace files still present after uninstall")
	}
	if km := readJSONMap(t, KnownMarketplacesPath()); km[MarketplaceName] != nil {
		t.Error("known_marketplaces.json entry not removed")
	}
}

func TestUninstallClaudePlugin_PreservesForeignEntries(t *testing.T) {
	isolateWithClaude(t)
	if err := InstallClaudePlugin("1.0.0", io.Discard); err != nil {
		t.Fatalf("install: %v", err)
	}
	// Add a foreign marketplace/plugin alongside ours.
	s := readJSONMap(t, settingsFile(t))
	s["extraKnownMarketplaces"].(map[string]any)["other-local"] = map[string]any{"x": 1}
	s["enabledPlugins"].(map[string]any)["other@other-local"] = true
	if err := storeSettings(settingsFile(t), s); err != nil {
		t.Fatal(err)
	}

	if err := UninstallClaudePlugin(io.Discard); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	s = readJSONMap(t, settingsFile(t))
	mkts, ok := s["extraKnownMarketplaces"].(map[string]any)
	if !ok || mkts["other-local"] == nil {
		t.Error("foreign marketplace dropped on uninstall")
	}
	if mkts[MarketplaceName] != nil {
		t.Error("our marketplace not removed")
	}
	plugins := s["enabledPlugins"].(map[string]any)
	if plugins["other@other-local"] != true {
		t.Error("foreign enabledPlugin dropped on uninstall")
	}
}

func TestUninstallClaudePlugin_Idempotent(t *testing.T) {
	isolateWithClaude(t)
	// Never installed — uninstall should be a clean no-op.
	if err := UninstallClaudePlugin(io.Discard); err != nil {
		t.Errorf("uninstall when absent = %v, want nil", err)
	}
}

func TestSettingsHelpers_AddRemove(t *testing.T) {
	s := map[string]any{}
	if isPluginInstalled(s) {
		t.Error("empty settings reported installed")
	}
	addPluginMarketplace(s, "/mkt")
	addPluginEnabled(s)
	if !isPluginInstalled(s) {
		t.Error("not reported installed after add")
	}
	removePluginMarketplace(s)
	removePluginEnabled(s)
	if isPluginInstalled(s) {
		t.Error("still reported installed after remove")
	}
	// Empty container maps are cleaned up.
	if _, ok := s["extraKnownMarketplaces"]; ok {
		t.Error("empty extraKnownMarketplaces not cleaned")
	}
	if _, ok := s["enabledPlugins"]; ok {
		t.Error("empty enabledPlugins not cleaned")
	}
}

func TestCompressHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := compressHome(filepath.Join(home, ".claude", "settings.json")); got != "~/.claude/settings.json" {
		t.Errorf("compressHome = %q, want ~/.claude/settings.json", got)
	}
	if got := compressHome("/etc/passwd"); got != "/etc/passwd" {
		t.Errorf("compressHome(/etc/passwd) = %q, want unchanged", got)
	}
}
