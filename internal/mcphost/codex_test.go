// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcphost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexInstallEnablesExpectedStdioServer(t *testing.T) {
	h, config := newTestCodexHost(t, "")
	project := t.TempDir()
	if err := h.Install("test", project, nil); err != nil {
		t.Fatal(err)
	}
	installOutput := strings.Builder{}
	if err := h.Install("test", project, &installOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(installOutput.String(), "review/trust") {
		t.Fatalf("hook was reported active before operator trust: %s", installOutput.String())
	}
	hooks, err := os.ReadFile(filepath.Join(filepath.Dir(config), "hooks.json"))
	if err != nil || !strings.Contains(string(hooks), "codex hook session-start") || !strings.Contains(string(hooks), `"timeout": 3`) {
		t.Fatalf("Codex SessionStart hook registration missing: %s err=%v", hooks, err)
	}
	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	server, err := readCodexServer(raw)
	if err != nil {
		t.Fatal(err)
	}
	if server.Command != "vp" || strings.Join(server.Args, ",") != "mcp" || server.Enabled == nil || !*server.Enabled {
		t.Fatalf("server config = %#v", server)
	}
	if _, err := os.Stat(filepath.Join(project, "AGENTS.md")); err != nil {
		t.Fatalf("managed AGENTS.md missing: %v", err)
	}
	home, err := h.homeDir()
	if err != nil {
		t.Fatal(err)
	}
	globalSkill := filepath.Join(home, ".agents", "skills", "vpc-restart", "agents", "openai.yaml")
	policy, err := os.ReadFile(globalSkill)
	if err != nil || !strings.Contains(string(policy), "allow_implicit_invocation: false") {
		t.Fatalf("Codex global command skill policy missing/unsafe: %q err=%v", policy, err)
	}
}

func TestCodexInstallLeavesHooksDisabledWhenConfigSaysFalse(t *testing.T) {
	h, config := newTestCodexHost(t, "[features]\nhooks = false\n")
	var output strings.Builder
	if err := h.Install("test", t.TempDir(), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "disabled by features.hooks=false") {
		t.Fatalf("disabled hooks state was not reported: %s", output.String())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(config), "hooks.json")); !os.IsNotExist(err) {
		t.Fatalf("hooks=false config was overridden with a hook (err=%v)", err)
	}
	got := mustRead(t, config)
	if !strings.Contains(string(got), "hooks = false") {
		t.Fatalf("Codex features.hooks choice changed: %s", got)
	}
}

func TestCodexInstallIsIdempotentWithoutCallingCLIWriter(t *testing.T) {
	h, config := newTestCodexHost(t, "[mcp_servers.vibe-palace]\ncommand = \"vp\"\nargs = [\"mcp\"]\nenabled = true\n")
	calls := 0
	h.runner = func(...string) ([]byte, error) {
		calls++
		return nil, nil
	}
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Install("test", t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("idempotent install changed config\nbefore=%q\nafter=%q", before, after)
	}
	if calls != 0 {
		t.Fatalf("Codex CLI writer called %d times", calls)
	}
}

func TestCodexInstallRefusesForeignTransport(t *testing.T) {
	h, config := newTestCodexHost(t, "[mcp_servers.vibe-palace]\ncommand = \"python\"\nargs = [\"server.py\"]\n")
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Install("test", t.TempDir(), nil); err == nil {
		t.Fatal("foreign transport was overwritten")
	}
	after, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("refused install mutated config")
	}
}

func TestCodexInstalledUsesEnabledState(t *testing.T) {
	h, config := newTestCodexHost(t, "[mcp_servers.vibe-palace]\ncommand = \"vp\"\nargs = [\"mcp\"]\nenabled = false\n")
	installed, err := h.Installed()
	if err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Fatal("disabled Codex server reported installed")
	}
	enabled := true
	patched, err := patchCodexServerTable(mustRead(t, config), codexServerConfig{Command: "vp", Args: []string{"mcp"}, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, patched, 0o644); err != nil {
		t.Fatal(err)
	}
	installed, err = h.Installed()
	if err != nil || !installed {
		t.Fatalf("enabled server installed=%v err=%v", installed, err)
	}
}

func TestCodexUninstallDisablesWithoutDeletingPolicy(t *testing.T) {
	fixture := strings.Replace(codexLiveFixture(), "command = \"/old/path/vp\" # command comment", "command = \"vp\" # command comment", 1)
	fixture = strings.Replace(fixture, "enabled = false", "enabled = true", 1)
	h, config := newTestCodexHost(t, fixture)
	if err := h.Uninstall(nil); err != nil {
		t.Fatal(err)
	}
	home, err := h.homeDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "vpc-restart")); !os.IsNotExist(err) {
		t.Fatalf("Codex uninstall left a managed global skill directory (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(config), "hooks.json")); !os.IsNotExist(err) {
		t.Fatalf("Codex uninstall left SessionStart hook config (err=%v)", err)
	}
	got, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "enabled = false") || !strings.Contains(string(got), "default_tools_approval_mode = \"approve\"") {
		t.Fatalf("uninstall did not disable while preserving policy:\n%s", got)
	}
	assertOriginalLinesInOrder(t,
		strings.Split(strings.TrimSuffix(fixture, "\n"), "\n"),
		strings.Split(strings.TrimSuffix(string(got), "\n"), "\n"),
		map[string]bool{"enabled = true": true},
	)
}

func TestCodexCommandOwnershipAcceptsResolvedPathAliases(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "vp-real")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "vp")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	h := &CodexHost{lookPath: func() (string, error) { return alias, nil }}
	if !h.ownsCommand(real) || !h.ownsCommand(alias) {
		t.Fatal("resolved vp path aliases were not recognized")
	}
}

func newTestCodexHost(t *testing.T, contents string) (*CodexHost, string) {
	t.Helper()
	config := filepath.Join(t.TempDir(), "config.toml")
	if contents != "" {
		if err := os.WriteFile(config, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	return &CodexHost{
		configPath: config,
		homeDir:    func() (string, error) { return home, nil },
		lookPath:   func() (string, error) { return "/usr/bin/vp", nil },
		runner:     func(...string) ([]byte, error) { return nil, nil },
	}, config
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
