// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// HookCommand is the command string emitted into settings.json.
const HookCommand = "vp hook"

// LegacyHookCommand is the vibe-vault hook we replace during install.
const LegacyHookCommand = "vv hook"

// HookTimeout is the timeout in seconds for the hook.
const HookTimeout = 30

// HookEvents are the events we register hooks for.
var HookEvents = []string{"SessionEnd", "Stop", "PreCompact"}

// HookStatus describes the current state of hook entries.
type HookStatus struct {
	Installed     bool   // vp hook is present and current
	Stale         bool   // vp hook is present but with different config
	LegacyPresent bool   // vv hook entries found
	Detail        string // human-readable detail
}

// Status checks the current state of vp hook entries in ~/.claude/settings.json.
func Status() (HookStatus, error) {
	path, err := defaultSettingsPath()
	if err != nil {
		return HookStatus{}, err
	}
	return StatusAt(path)
}

// Install reads ~/.claude/settings.json, replaces vv hook entries with
// vp hook entries on SessionEnd/Stop/PreCompact, preserves all other
// user hooks and settings, writes back.
func Install() (bool, error) {
	path, err := defaultSettingsPath()
	if err != nil {
		return false, err
	}
	return InstallAt(path)
}

// Uninstall removes vp hook entries from ~/.claude/settings.json.
func Uninstall() (bool, error) {
	path, err := defaultSettingsPath()
	if err != nil {
		return false, err
	}
	return UninstallAt(path)
}

// StatusAt checks the current state of vp hook entries at the given path.
func StatusAt(path string) (HookStatus, error) {
	data, err := readSettings(path)
	if err != nil {
		if os.IsNotExist(err) {
			return HookStatus{Detail: "settings.json does not exist"}, nil
		}
		return HookStatus{}, err
	}

	hooks := getHooksMap(data)
	if hooks == nil {
		return HookStatus{Detail: "no hooks key in settings.json"}, nil
	}

	var status HookStatus
	vpFound := 0
	vpStale := 0

	for _, event := range HookEvents {
		groups, ok := getMatcherGroups(hooks, event)
		if !ok {
			continue
		}
		for _, group := range groups {
			gm, ok := group.(map[string]any)
			if !ok {
				continue
			}
			hookEntries, ok := getHookEntries(gm)
			if !ok {
				continue
			}
			for _, entry := range hookEntries {
				em, ok := entry.(map[string]any)
				if !ok {
					continue
				}
				cmd, _ := em["command"].(string)
				switch cmd {
				case HookCommand:
					vpFound++
					timeout, hasTimeout := em["timeout"]
					if !hasTimeout || toInt(timeout) != HookTimeout {
						vpStale++
					}
				case LegacyHookCommand:
					status.LegacyPresent = true
				}
			}
		}
	}

	if vpFound > 0 {
		status.Installed = true
		if vpStale > 0 {
			status.Stale = true
			status.Detail = fmt.Sprintf("vp hook installed (%d stale)", vpStale)
		} else {
			status.Detail = "vp hook installed and current"
		}
	} else {
		status.Detail = "vp hook not installed"
	}
	if status.LegacyPresent {
		status.Detail += "; legacy vv hook present"
	}
	return status, nil
}

// InstallAt reads the settings file at path, replaces vv hook entries with
// vp hook entries, and writes back.
func InstallAt(path string) (bool, error) {
	data, err := readSettings(path)
	if err != nil {
		if os.IsNotExist(err) {
			data = make(map[string]any)
		} else {
			return false, err
		}
	}

	changed := false
	hooks := getOrCreateHooksMap(data)

	for _, event := range HookEvents {
		groups, ok := getMatcherGroups(hooks, event)
		if !ok {
			groups = []any{}
		}

		vpFound := false
		var newGroups []any

		for _, group := range groups {
			gm, ok := group.(map[string]any)
			if !ok {
				newGroups = append(newGroups, group)
				continue
			}
			hookEntries, ok := getHookEntries(gm)
			if !ok {
				newGroups = append(newGroups, group)
				continue
			}

			var newEntries []any
			for _, entry := range hookEntries {
				em, ok := entry.(map[string]any)
				if !ok {
					newEntries = append(newEntries, entry)
					continue
				}
				cmd, _ := em["command"].(string)
				switch cmd {
				case LegacyHookCommand:
					// Keep legacy vv hook — both can coexist until
					// vibe-palace fully replaces vibe-vault's capture.
					newEntries = append(newEntries, entry)
				case HookCommand:
					vpFound = true
					// check timeout staleness
					timeout, hasTimeout := em["timeout"]
					if !hasTimeout || toInt(timeout) != HookTimeout {
						em["timeout"] = HookTimeout
						changed = true
					}
					newEntries = append(newEntries, em)
				default:
					newEntries = append(newEntries, entry)
				}
			}

			if len(newEntries) > 0 {
				gm["hooks"] = newEntries
				newGroups = append(newGroups, gm)
			} else {
				// empty group after removing legacy entries
				changed = true
			}
		}

		if !vpFound {
			newGroups = append(newGroups, map[string]any{
				"matcher": "",
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": HookCommand,
						"timeout": HookTimeout,
					},
				},
			})
			changed = true
		}

		hooks[event] = newGroups
	}

	data["hooks"] = hooks

	if !changed {
		return false, nil
	}
	return true, writeSettings(path, data)
}

// UninstallAt removes vp hook entries from the settings file at path.
func UninstallAt(path string) (bool, error) {
	data, err := readSettings(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	hooks := getHooksMap(data)
	if hooks == nil {
		return false, nil
	}

	changed := false
	for _, event := range HookEvents {
		groups, ok := getMatcherGroups(hooks, event)
		if !ok {
			continue
		}

		var newGroups []any
		for _, group := range groups {
			gm, ok := group.(map[string]any)
			if !ok {
				newGroups = append(newGroups, group)
				continue
			}
			hookEntries, ok := getHookEntries(gm)
			if !ok {
				newGroups = append(newGroups, group)
				continue
			}

			var newEntries []any
			for _, entry := range hookEntries {
				em, ok := entry.(map[string]any)
				if !ok {
					newEntries = append(newEntries, entry)
					continue
				}
				cmd, _ := em["command"].(string)
				if cmd == HookCommand {
					changed = true
					continue // drop
				}
				newEntries = append(newEntries, entry)
			}

			if len(newEntries) > 0 {
				gm["hooks"] = newEntries
				newGroups = append(newGroups, gm)
			} else {
				changed = true
			}
		}

		if len(newGroups) > 0 {
			hooks[event] = newGroups
		} else {
			delete(hooks, event)
			changed = true
		}
	}

	if !changed {
		return false, nil
	}

	// Clean up empty hooks map.
	if len(hooks) == 0 {
		delete(data, "hooks")
	} else {
		data["hooks"] = hooks
	}

	return true, writeSettings(path, data)
}

// --- helpers ---

func defaultSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

func readSettings(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return data, nil
}

func writeSettings(path string, data map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return os.WriteFile(path, out, 0o644)
}

func getHooksMap(data map[string]any) map[string]any {
	v, ok := data["hooks"]
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

func getOrCreateHooksMap(data map[string]any) map[string]any {
	if m := getHooksMap(data); m != nil {
		return m
	}
	m := make(map[string]any)
	data["hooks"] = m
	return m
}

func getMatcherGroups(hooks map[string]any, event string) ([]any, bool) {
	v, ok := hooks[event]
	if !ok {
		return nil, false
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	return arr, true
}

func getHookEntries(group map[string]any) ([]any, bool) {
	v, ok := group["hooks"]
	if !ok {
		return nil, false
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	return arr, true
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}
