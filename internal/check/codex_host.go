// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// CheckCodexHost reports only the Codex configuration Vibe-Palace can inspect;
// Codex does not expose effective hook trust through a stable CLI API.
func CheckCodexHost() []Result {
	home, err := os.UserHomeDir()
	if err != nil {
		return []Result{{Name: "Codex host", Status: Info, Summary: "home directory unavailable: " + err.Error()}}
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	return checkCodexHostAt(filepath.Join(codexHome, "config.toml"), filepath.Join(codexHome, "hooks.json"), filepath.Join(home, ".agents", "skills"), "")
}

func checkCodexHostAt(configPath, hooksPath, skillsPath, projectConfigPath string) []Result {
	r := Result{Name: "Codex host", Status: Info}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		r.Summary = "Codex MCP config unavailable"
	} else {
		var doc map[string]any
		if _, err := toml.Decode(string(raw), &doc); err != nil {
			r.Details = append(r.Details, "MCP config: malformed TOML")
		} else if servers, ok := doc["mcp_servers"].(map[string]any); !ok || servers["vibe-palace"] == nil {
			r.Details = append(r.Details, "MCP config: vibe-palace not configured")
		} else if server, ok := servers["vibe-palace"].(map[string]any); ok {
			command, _ := server["command"].(string)
			args, _ := server["args"].([]any)
			if filepath.Base(command) != "vp" && filepath.Base(command) != "vp.exe" {
				r.Details = append(r.Details, "MCP config: command conflict (not a recognized vp executable)")
			} else if len(args) == 0 || args[0] != "mcp" {
				r.Details = append(r.Details, "MCP config: transport conflict (expected vp mcp)")
			} else if enabled, exists := server["enabled"].(bool); exists && !enabled {
				r.Details = append(r.Details, "MCP config: vibe-palace disabled")
			} else {
				r.Details = append(r.Details, "MCP config: vibe-palace enabled/configured")
			}
		}
	}
	hooksDisabled := false
	for _, path := range []string{configPath, projectConfigPath} {
		var doc map[string]any
		if _, err := toml.DecodeFile(path, &doc); err == nil {
			if features, ok := doc["features"].(map[string]any); ok {
				if enabled, ok := features["hooks"].(bool); ok && !enabled {
					hooksDisabled = true
				}
			}
		}
	}
	if hooksDisabled {
		r.Details = append(r.Details, "hooks: disabled by visible features.hooks=false")
	} else if hookOwned(hooksPath) {
		r.Details = append(r.Details, "hooks: Vibe-Palace SessionStart/SessionEnd entries configured; trust pending/unobservable")
	} else {
		r.Details = append(r.Details, "hooks: Vibe-Palace user hooks entry missing; trust state unobservable")
	}
	if entries, err := os.ReadDir(skillsPath); err == nil {
		owned := 0
		for _, entry := range entries {
			if entry.IsDir() {
				raw, _ := os.ReadFile(filepath.Join(skillsPath, entry.Name(), "agents", "openai.yaml"))
				if strings.Contains(string(raw), "allow_implicit_invocation: false") {
					owned++
				}
			}
		}
		r.Details = append(r.Details, fmt.Sprintf("global Codex skills: %d marked Vibe-Palace skills", owned))
	} else {
		r.Details = append(r.Details, "global Codex skills: directory unavailable")
	}
	r.Summary = "Codex config inspected; hook trust is not observable"
	return []Result{r}
}

func hookOwned(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	text := string(raw)
	return strings.Contains(text, "codex hook session-start") && strings.Contains(text, "codex hook session-end")
}
