// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func tempSettingsPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, ".claude", "settings.json")
}

func writeJSON(t *testing.T, path string, data map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestInstallFreshNoFile(t *testing.T) {
	path := tempSettingsPath(t)

	changed, err := InstallAt(path)
	if err != nil {
		t.Fatalf("InstallAt: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true for fresh install")
	}

	data := readJSON(t, path)
	hooks, ok := data["hooks"].(map[string]any)
	if !ok {
		t.Fatal("expected hooks key")
	}

	for _, event := range HookEvents {
		groups, ok := hooks[event].([]any)
		if !ok || len(groups) == 0 {
			t.Fatalf("expected groups for %s", event)
		}
		gm := groups[0].(map[string]any)
		entries := gm["hooks"].([]any)
		if len(entries) != 1 {
			t.Fatalf("expected 1 hook entry for %s, got %d", event, len(entries))
		}
		em := entries[0].(map[string]any)
		if em["command"] != HookCommand {
			t.Errorf("expected command=%q, got %v", HookCommand, em["command"])
		}
		if toInt(em["timeout"]) != HookTimeout {
			t.Errorf("expected timeout=%d, got %v", HookTimeout, em["timeout"])
		}
	}
}

func TestInstallCoexistsWithVVHook(t *testing.T) {
	path := tempSettingsPath(t)

	// Start with vv hook entries.
	data := map[string]any{
		"someOtherKey": "preserved",
		"hooks": map[string]any{
			"SessionEnd": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": LegacyHookCommand,
						},
					},
				},
			},
			"Stop": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": LegacyHookCommand,
						},
					},
				},
			},
			"PreCompact": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": LegacyHookCommand,
						},
					},
				},
			},
		},
	}
	writeJSON(t, path, data)

	changed, err := InstallAt(path)
	if err != nil {
		t.Fatalf("InstallAt: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}

	result := readJSON(t, path)
	// Verify other keys preserved.
	if result["someOtherKey"] != "preserved" {
		t.Error("expected someOtherKey to be preserved")
	}

	// Verify vv hook is preserved alongside vp hook.
	hooks := result["hooks"].(map[string]any)
	for _, event := range HookEvents {
		groups := hooks[event].([]any)
		foundVV := false
		foundVP := false
		for _, g := range groups {
			gm := g.(map[string]any)
			entries := gm["hooks"].([]any)
			for _, e := range entries {
				em := e.(map[string]any)
				switch em["command"] {
				case LegacyHookCommand:
					foundVV = true
				case HookCommand:
					foundVP = true
				}
			}
		}
		if !foundVV {
			t.Errorf("vv hook should be preserved in %s", event)
		}
		if !foundVP {
			t.Errorf("vp hook should be added to %s", event)
		}
	}
}

func TestInstallPreservesUserHooks(t *testing.T) {
	path := tempSettingsPath(t)

	data := map[string]any{
		"hooks": map[string]any{
			"SessionEnd": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": LegacyHookCommand,
						},
						map[string]any{
							"type":    "command",
							"command": "my-custom-hook",
						},
					},
				},
			},
		},
	}
	writeJSON(t, path, data)

	changed, err := InstallAt(path)
	if err != nil {
		t.Fatalf("InstallAt: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}

	result := readJSON(t, path)
	hooks := result["hooks"].(map[string]any)
	groups := hooks["SessionEnd"].([]any)

	// The original group should still have the custom hook + vp hook was added.
	gm := groups[0].(map[string]any)
	entries := gm["hooks"].([]any)

	foundCustom := false
	foundVV := false
	for _, e := range entries {
		em := e.(map[string]any)
		switch em["command"] {
		case "my-custom-hook":
			foundCustom = true
		case LegacyHookCommand:
			foundVV = true
		}
	}
	if !foundCustom {
		t.Error("custom hook should be preserved")
	}
	if !foundVV {
		t.Error("vv hook should be preserved")
	}
	// vp hook is added in a separate matcher group
	foundVP := false
	for _, g := range groups {
		gm := g.(map[string]any)
		ents := gm["hooks"].([]any)
		for _, e := range ents {
			em := e.(map[string]any)
			if em["command"] == HookCommand {
				foundVP = true
			}
		}
	}
	if !foundVP {
		t.Error("expected vp hook to be added")
	}
}

func TestInstallIdempotent(t *testing.T) {
	path := tempSettingsPath(t)

	changed1, err := InstallAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed1 {
		t.Fatal("first install should change")
	}

	changed2, err := InstallAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed2 {
		t.Fatal("second install should not change")
	}
}

func TestUninstall(t *testing.T) {
	path := tempSettingsPath(t)

	// Install first.
	if _, err := InstallAt(path); err != nil {
		t.Fatal(err)
	}

	changed, err := UninstallAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}

	data := readJSON(t, path)
	// hooks key should be removed (all events had only vp hook).
	if _, ok := data["hooks"]; ok {
		t.Error("expected hooks key to be removed after uninstall")
	}

	// Second uninstall should be no-op.
	changed2, err := UninstallAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed2 {
		t.Fatal("second uninstall should not change")
	}
}

func TestUninstallNoFile(t *testing.T) {
	path := tempSettingsPath(t)

	changed, err := UninstallAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected changed=false for nonexistent file")
	}
}

func TestStatusNotInstalled(t *testing.T) {
	path := tempSettingsPath(t)

	status, err := StatusAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if status.Installed {
		t.Error("expected not installed")
	}
}

func TestStatusInstalled(t *testing.T) {
	path := tempSettingsPath(t)
	if _, err := InstallAt(path); err != nil {
		t.Fatal(err)
	}

	status, err := StatusAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed {
		t.Error("expected installed")
	}
	if status.Stale {
		t.Error("expected not stale")
	}
	if status.LegacyPresent {
		t.Error("expected no legacy")
	}
}

func TestStatusLegacy(t *testing.T) {
	path := tempSettingsPath(t)
	data := map[string]any{
		"hooks": map[string]any{
			"SessionEnd": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": LegacyHookCommand,
						},
					},
				},
			},
		},
	}
	writeJSON(t, path, data)

	status, err := StatusAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if status.Installed {
		t.Error("expected not installed")
	}
	if !status.LegacyPresent {
		t.Error("expected legacy present")
	}
}

func TestStatusStale(t *testing.T) {
	path := tempSettingsPath(t)
	data := map[string]any{
		"hooks": map[string]any{
			"SessionEnd": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": HookCommand,
							"timeout": 10, // wrong timeout
						},
					},
				},
			},
		},
	}
	writeJSON(t, path, data)

	status, err := StatusAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed {
		t.Error("expected installed")
	}
	if !status.Stale {
		t.Error("expected stale")
	}
}

func TestInstallFixesStaleTimeout(t *testing.T) {
	path := tempSettingsPath(t)
	data := map[string]any{
		"hooks": map[string]any{
			"SessionEnd": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": HookCommand,
							"timeout": 10,
						},
					},
				},
			},
		},
	}
	writeJSON(t, path, data)

	changed, err := InstallAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true for stale timeout fix")
	}

	result := readJSON(t, path)
	hooks := result["hooks"].(map[string]any)
	groups := hooks["SessionEnd"].([]any)
	gm := groups[0].(map[string]any)
	entries := gm["hooks"].([]any)
	em := entries[0].(map[string]any)
	if toInt(em["timeout"]) != HookTimeout {
		t.Errorf("expected timeout=%d, got %v", HookTimeout, em["timeout"])
	}
}

func TestInstallWithLegacyOnlyGroup(t *testing.T) {
	path := tempSettingsPath(t)
	// Group with ONLY legacy hook -> vv hook preserved, vp hook added in new group.
	data := map[string]any{
		"hooks": map[string]any{
			"SessionEnd": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": LegacyHookCommand,
						},
					},
				},
			},
		},
	}
	writeJSON(t, path, data)

	changed, err := InstallAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed")
	}

	result := readJSON(t, path)
	hooks := result["hooks"].(map[string]any)
	groups := hooks["SessionEnd"].([]any)
	// Should have 2 groups: original with vv hook + new with vp hook.
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	// First group keeps vv hook.
	gm0 := groups[0].(map[string]any)
	entries0 := gm0["hooks"].([]any)
	em0 := entries0[0].(map[string]any)
	if em0["command"] != LegacyHookCommand {
		t.Errorf("first group: expected vv hook, got %v", em0["command"])
	}
	// Second group has vp hook.
	gm1 := groups[1].(map[string]any)
	entries1 := gm1["hooks"].([]any)
	em1 := entries1[0].(map[string]any)
	if em1["command"] != HookCommand {
		t.Errorf("second group: expected vp hook, got %v", em1["command"])
	}
}
