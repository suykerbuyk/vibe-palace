// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func codexCheckFixture(t *testing.T, config, hooks string) ([]Result, string) {
	t.Helper()
	root := t.TempDir()
	configPath, hooksPath := filepath.Join(root, "config.toml"), filepath.Join(root, "hooks.json")
	if config != "" {
		if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if hooks != "" {
		if err := os.WriteFile(hooksPath, []byte(hooks), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	skills := filepath.Join(root, "skills")
	if err := os.MkdirAll(skills, 0o700); err != nil {
		t.Fatal(err)
	}
	rows := checkCodexHostAt(configPath, hooksPath, skills, filepath.Join(root, "project-config.toml"))
	return rows, skills
}

func TestCheckCodexHostReportsEnabledDisabledAndConflictSeparately(t *testing.T) {
	rows, _ := codexCheckFixture(t, "[mcp_servers.vibe-palace]\ncommand = \"vp\"\nargs = [\"mcp\"]\nenabled = true\n", "")
	if !strings.Contains(strings.Join(rows[0].Details, " "), "enabled/configured") {
		t.Fatalf("enabled details: %+v", rows[0])
	}
	rows, _ = codexCheckFixture(t, "[mcp_servers.vibe-palace]\ncommand = \"vp\"\nargs = [\"mcp\"]\nenabled = false\n", "")
	if !strings.Contains(strings.Join(rows[0].Details, " "), "disabled") {
		t.Fatalf("disabled details: %+v", rows[0])
	}
	rows, _ = codexCheckFixture(t, "[mcp_servers.vibe-palace]\ncommand = \"other\"\nargs = [\"mcp\"]\n", "")
	if !strings.Contains(strings.Join(rows[0].Details, " "), "command conflict") {
		t.Fatalf("conflict details: %+v", rows[0])
	}
}

func TestCheckCodexHostReportsHooksDisabledAndTrustPending(t *testing.T) {
	rows, _ := codexCheckFixture(t, "[mcp_servers.vibe-palace]\ncommand = \"vp\"\nargs=[\"mcp\"]\n", `{"hooks":{"SessionStart":[{"hooks":[{"command":"vp codex hook session-start"}]}],"SessionEnd":[{"hooks":[{"command":"vp codex hook session-end"}]}]}}`)
	joined := strings.Join(rows[0].Details, " ")
	if !strings.Contains(joined, "trust pending/unobservable") {
		t.Fatalf("trust uncertainty not reported: %+v", rows[0])
	}
	root := t.TempDir()
	projectCfg := filepath.Join(root, "project-config.toml")
	if err := os.WriteFile(projectCfg, []byte("[features]\nhooks=false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rows = checkCodexHostAt(filepath.Join(root, "config.toml"), filepath.Join(root, "hooks.json"), filepath.Join(root, "skills"), projectCfg)
	if !strings.Contains(strings.Join(rows[0].Details, " "), "disabled by visible features.hooks=false") {
		t.Fatalf("hooks disabled not reported: %+v", rows[0])
	}
}

func TestCheckCodexHostDoesNotClaimOtherHookLayers(t *testing.T) {
	rows, _ := codexCheckFixture(t, "[mcp_servers.vibe-palace]\nenabled=true\n", `{"hooks":{"SessionStart":[{"hooks":[{"command":"echo project-layer"}]}]}}`)
	joined := strings.Join(rows[0].Details, " ")
	if strings.Contains(joined, "Vibe-Palace SessionStart/SessionEnd entries configured") {
		t.Fatalf("foreign hook layer claimed as owned: %+v", rows[0])
	}
}

func TestCheckCodexHostIgnoresUnmarkedGlobalSkills(t *testing.T) {
	rows, skills := codexCheckFixture(t, "", "")
	if err := os.MkdirAll(filepath.Join(skills, "vpc-custom"), 0o700); err != nil {
		t.Fatal(err)
	}
	rows = checkCodexHostAt(filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "hooks.json"), skills, "")
	if !strings.Contains(strings.Join(rows[0].Details, " "), "0 marked Vibe-Palace skills") {
		t.Fatalf("unmarked custom skill counted as owned: %+v", rows[0])
	}
}

func TestCodexHostSelectorWiring(t *testing.T) {
	if _, ok := Producers["codex-host"]; !ok {
		t.Fatal("codex-host selector missing")
	}
	for _, name := range ProducerOrder {
		if name == "codex-host" {
			return
		}
	}
	t.Fatal("codex-host omitted from ProducerOrder")
}
