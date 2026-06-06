// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallToCache_CreatesFiles(t *testing.T) {
	isolate(t)
	installDir, err := InstallToCache("1.2.3")
	if err != nil {
		t.Fatalf("InstallToCache: %v", err)
	}
	if installDir != CacheInstallDir("1.2.3") {
		t.Errorf("installDir = %q, want %q", installDir, CacheInstallDir("1.2.3"))
	}
	for _, p := range []string{
		filepath.Join(installDir, ".claude-plugin", "plugin.json"),
		filepath.Join(installDir, ".mcp.json"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected cache file %s: %v", p, err)
		}
	}
}

func TestInstallToCache_PathRelativeBinary(t *testing.T) {
	isolate(t)
	installDir, err := InstallToCache("1.0.0")
	if err != nil {
		t.Fatalf("InstallToCache: %v", err)
	}
	m := readJSONMap(t, filepath.Join(installDir, ".mcp.json"))
	entry := m[pluginName].(map[string]any)
	if entry["command"] != "vp" {
		t.Errorf("cache command = %v, want \"vp\"", entry["command"])
	}
}

func TestRemoveFromCache_Cleans(t *testing.T) {
	isolate(t)
	if _, err := InstallToCache("1.0.0"); err != nil {
		t.Fatalf("InstallToCache: %v", err)
	}
	if err := RemoveFromCache(); err != nil {
		t.Fatalf("RemoveFromCache: %v", err)
	}
	cacheDir := filepath.Join(ClaudePluginsDir(), "cache", MarketplaceName)
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Errorf("cache dir present after RemoveFromCache: %v", err)
	}
}

func TestRemoveFromCache_NotPresent(t *testing.T) {
	isolate(t)
	if err := RemoveFromCache(); err != nil {
		t.Errorf("RemoveFromCache on absent dir = %v, want nil", err)
	}
}

func TestRegisterKnownMarketplace_Fresh(t *testing.T) {
	isolate(t)
	if err := RegisterKnownMarketplace("/some/mkt/dir"); err != nil {
		t.Fatalf("RegisterKnownMarketplace: %v", err)
	}
	m := readJSONMap(t, KnownMarketplacesPath())
	entry, ok := m[MarketplaceName].(map[string]any)
	if !ok {
		t.Fatalf("missing %q entry", MarketplaceName)
	}
	if entry["installLocation"] != "/some/mkt/dir" {
		t.Errorf("installLocation = %v, want /some/mkt/dir", entry["installLocation"])
	}
}

func TestRegisterKnownMarketplace_PreservesExisting(t *testing.T) {
	isolate(t)
	// Seed a foreign marketplace entry.
	if err := writeJSONSecure(KnownMarketplacesPath(), map[string]any{
		"other-local": map[string]any{"installLocation": "/other"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterKnownMarketplace("/some/mkt/dir"); err != nil {
		t.Fatalf("RegisterKnownMarketplace: %v", err)
	}
	m := readJSONMap(t, KnownMarketplacesPath())
	if _, ok := m["other-local"]; !ok {
		t.Error("foreign marketplace entry was dropped")
	}
	if _, ok := m[MarketplaceName]; !ok {
		t.Error("our marketplace entry missing")
	}
}

func TestUnregisterKnownMarketplace_Removes(t *testing.T) {
	isolate(t)
	if err := RegisterKnownMarketplace("/some/mkt/dir"); err != nil {
		t.Fatal(err)
	}
	if err := UnregisterKnownMarketplace(); err != nil {
		t.Fatalf("UnregisterKnownMarketplace: %v", err)
	}
	m := readJSONMap(t, KnownMarketplacesPath())
	if _, ok := m[MarketplaceName]; ok {
		t.Error("entry still present after Unregister")
	}
}

func TestUnregisterKnownMarketplace_NotPresent(t *testing.T) {
	isolate(t)
	if err := UnregisterKnownMarketplace(); err != nil {
		t.Errorf("Unregister with no file = %v, want nil", err)
	}
}

func TestRegisterInstalledPlugin_Fresh(t *testing.T) {
	isolate(t)
	if err := RegisterInstalledPlugin("/install/path", "1.2.3"); err != nil {
		t.Fatalf("RegisterInstalledPlugin: %v", err)
	}
	m := readJSONMap(t, InstalledPluginsPath())
	if m["version"] != float64(2) {
		t.Errorf("schema version = %v, want 2", m["version"])
	}
	entries, ok := m[QualifiedName].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("%q entries = %v, want one", QualifiedName, m[QualifiedName])
	}
	e0 := entries[0].(map[string]any)
	if e0["installPath"] != "/install/path" || e0["version"] != "1.2.3" {
		t.Errorf("entry = %v, want installPath=/install/path version=1.2.3", e0)
	}
	if e0["scope"] != "user" {
		t.Errorf("scope = %v, want user", e0["scope"])
	}
}

func TestRegisterInstalledPlugin_PreservesExisting(t *testing.T) {
	isolate(t)
	if err := writeJSONSecure(InstalledPluginsPath(), map[string]any{
		"version":            float64(2),
		"other@other-local": []any{map[string]any{"scope": "user"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterInstalledPlugin("/install/path", "1.0.0"); err != nil {
		t.Fatalf("RegisterInstalledPlugin: %v", err)
	}
	m := readJSONMap(t, InstalledPluginsPath())
	if _, ok := m["other@other-local"]; !ok {
		t.Error("foreign plugin entry was dropped")
	}
}

func TestUnregisterInstalledPlugin_Removes(t *testing.T) {
	isolate(t)
	if err := RegisterInstalledPlugin("/install/path", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := UnregisterInstalledPlugin(); err != nil {
		t.Fatalf("UnregisterInstalledPlugin: %v", err)
	}
	m := readJSONMap(t, InstalledPluginsPath())
	if _, ok := m[QualifiedName]; ok {
		t.Error("entry still present after Unregister")
	}
}

func TestUnregisterInstalledPlugin_NotPresent(t *testing.T) {
	isolate(t)
	if err := UnregisterInstalledPlugin(); err != nil {
		t.Errorf("Unregister with no file = %v, want nil", err)
	}
}
