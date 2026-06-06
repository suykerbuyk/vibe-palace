// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// isolate points HOME and XDG_DATA_HOME at temp dirs so plugin paths resolve
// under the test sandbox.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "") // default to ~/.local/share
	return home
}

func readJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

func TestDataDir_Default(t *testing.T) {
	home := isolate(t)
	want := filepath.Join(home, ".local", "share", "vibe-palace")
	if got := dataDir(); got != want {
		t.Errorf("dataDir() = %q, want %q", got, want)
	}
}

func TestDataDir_XDG(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	want := filepath.Join(xdg, "vibe-palace")
	if got := dataDir(); got != want {
		t.Errorf("dataDir() = %q, want %q", got, want)
	}
}

func TestGenerate_CreatesAllFiles(t *testing.T) {
	isolate(t)
	if _, err := Generate("1.2.3"); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, p := range []string{MarketplaceManifestPath(), PluginManifestPath(), MCPConfigPath()} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected file %s: %v", p, err)
		}
	}
	if !IsInstalled() {
		t.Error("IsInstalled() = false after Generate")
	}
}

func TestGenerate_MarketplaceSchema(t *testing.T) {
	isolate(t)
	if _, err := Generate("1.0.0"); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	m := readJSONMap(t, MarketplaceManifestPath())
	if m["name"] != MarketplaceName {
		t.Errorf("marketplace name = %v, want %q", m["name"], MarketplaceName)
	}
	plugins, ok := m["plugins"].([]any)
	if !ok || len(plugins) != 1 {
		t.Fatalf("plugins = %v, want one entry", m["plugins"])
	}
	p0 := plugins[0].(map[string]any)
	if p0["name"] != pluginName {
		t.Errorf("plugin name = %v, want %q", p0["name"], pluginName)
	}
	if p0["source"] != "./"+pluginName {
		t.Errorf("plugin source = %v, want %q", p0["source"], "./"+pluginName)
	}
}

func TestGenerate_TwoLevelStructure(t *testing.T) {
	isolate(t)
	if _, err := Generate("1.0.0"); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// marketplace manifest sits at the root; plugin manifest one level down.
	if filepath.Dir(filepath.Dir(MarketplaceManifestPath())) != MarketplaceDir() {
		t.Error("marketplace manifest not under marketplace root/.claude-plugin")
	}
	if filepath.Dir(PluginDir()) != MarketplaceDir() {
		t.Error("plugin dir not directly under marketplace root")
	}
}

func TestGenerate_PluginHasVersionAndAuthor(t *testing.T) {
	isolate(t)
	if _, err := Generate("9.9.9"); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	m := readJSONMap(t, PluginManifestPath())
	if m["version"] != "9.9.9" {
		t.Errorf("plugin version = %v, want 9.9.9", m["version"])
	}
	author, ok := m["author"].(map[string]any)
	if !ok || author["name"] != "vibe-palace" {
		t.Errorf("author = %v, want {name: vibe-palace}", m["author"])
	}
}

func TestGenerate_Idempotent(t *testing.T) {
	isolate(t)
	if _, err := Generate("1.0.0"); err != nil {
		t.Fatalf("Generate 1: %v", err)
	}
	first, err := os.ReadFile(MCPConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Generate("1.0.0"); err != nil {
		t.Fatalf("Generate 2: %v", err)
	}
	second, err := os.ReadFile(MCPConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("re-Generate with same version changed .mcp.json bytes")
	}
}

func TestGenerate_UpdatesVersion(t *testing.T) {
	isolate(t)
	if _, err := Generate("1.0.0"); err != nil {
		t.Fatalf("Generate 1: %v", err)
	}
	if _, err := Generate("2.0.0"); err != nil {
		t.Fatalf("Generate 2: %v", err)
	}
	if got := readJSONMap(t, PluginManifestPath())["version"]; got != "2.0.0" {
		t.Errorf("version after re-Generate = %v, want 2.0.0", got)
	}
}

func TestGenerate_PathRelativeBinary(t *testing.T) {
	isolate(t)
	if _, err := Generate("1.0.0"); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	m := readJSONMap(t, MCPConfigPath())
	entry, ok := m[pluginName].(map[string]any)
	if !ok {
		t.Fatalf("missing %q entry in .mcp.json", pluginName)
	}
	if entry["command"] != "vp" {
		t.Errorf("command = %v, want PATH-relative \"vp\"", entry["command"])
	}
	args, ok := entry["args"].([]any)
	if !ok || len(args) != 1 || args[0] != "mcp" {
		t.Errorf("args = %v, want [\"mcp\"]", entry["args"])
	}
	env, ok := entry["env"].(map[string]any)
	if !ok {
		t.Fatalf("missing env block")
	}
	if env["XAI_API_KEY"] != "${XAI_API_KEY}" {
		t.Errorf("XAI_API_KEY = %v, want ${XAI_API_KEY}", env["XAI_API_KEY"])
	}
}

func TestRemove_Cleans(t *testing.T) {
	isolate(t)
	if _, err := Generate("1.0.0"); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if IsInstalled() {
		t.Error("IsInstalled() = true after Remove")
	}
	if _, err := os.Stat(MarketplaceDir()); !os.IsNotExist(err) {
		t.Errorf("marketplace dir still present after Remove: %v", err)
	}
}

func TestRemove_NotInstalled(t *testing.T) {
	isolate(t)
	if err := Remove(); err != nil {
		t.Errorf("Remove on absent dir = %v, want nil", err)
	}
}

func TestIsInstalled_Partial(t *testing.T) {
	isolate(t)
	if _, err := Generate("1.0.0"); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := os.Remove(MCPConfigPath()); err != nil {
		t.Fatal(err)
	}
	if IsInstalled() {
		t.Error("IsInstalled() = true with .mcp.json missing")
	}
}
