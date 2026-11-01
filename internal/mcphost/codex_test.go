// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcphost

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestNewCodexHostUsesDistinctExecutableLookups(t *testing.T) {
	fixtureDir := searchableTempDir(t)
	codexPath := writeLookupFixture(t, fixtureDir, "codex")
	vpPath := writeLookupFixture(t, fixtureDir, "vp")
	configDir := t.TempDir()
	t.Setenv("CODEX_HOME", configDir)
	t.Setenv("PATH", fixtureDir)

	h := NewCodexHost()
	h.homeDir = func() (string, error) { return t.TempDir(), nil }
	projectRoot := t.TempDir()
	if err := h.Install("test", projectRoot, nil); err != nil {
		t.Fatal(err)
	}

	raw := mustRead(t, filepath.Join(configDir, "config.toml"))
	server, err := readCodexServer(raw)
	if err != nil {
		t.Fatal(err)
	}
	if server.Command != "vp" || strings.Join(server.Args, ",") != "mcp" || server.Enabled == nil || !*server.Enabled {
		t.Fatalf("installed server = %#v", server)
	}
	hooks := string(mustRead(t, filepath.Join(configDir, "hooks.json")))
	if !strings.Contains(hooks, `\"`+vpPath+`\" codex hook session-start`) ||
		!strings.Contains(hooks, `\"`+vpPath+`\" codex hook session-end`) {
		t.Fatalf("hooks do not invoke the vp fixture %q:\n%s", vpPath, hooks)
	}
	if strings.Contains(hooks, codexPath) {
		t.Fatalf("Codex CLI fixture %q was installed as a hook executable:\n%s", codexPath, hooks)
	}
}

func TestNewCodexHostDetectedUsesCodexLookupWhenConfigDirIsAbsent(t *testing.T) {
	t.Run("codex fixture is detected", func(t *testing.T) {
		fixtureDir := searchableTempDir(t)
		writeLookupFixture(t, fixtureDir, "codex")
		t.Setenv("PATH", fixtureDir)
		missingHome := filepath.Join(t.TempDir(), "missing-codex-home")
		t.Setenv("CODEX_HOME", missingHome)

		h := NewCodexHost()
		if _, err := os.Stat(filepath.Dir(h.configPath)); !os.IsNotExist(err) {
			t.Fatalf("fixture config directory unexpectedly exists: %v", err)
		}
		if !h.Detected() {
			t.Fatal("Codex was not detected from its PATH fixture")
		}
	})

	t.Run("vp fixture alone is not detected as Codex", func(t *testing.T) {
		fixtureDir := searchableTempDir(t)
		writeLookupFixture(t, fixtureDir, "vp")
		t.Setenv("PATH", fixtureDir)
		missingHome := filepath.Join(t.TempDir(), "missing-codex-home")
		t.Setenv("CODEX_HOME", missingHome)

		h := NewCodexHost()
		if h.Detected() {
			t.Fatal("vp-only PATH was mistaken for a Codex CLI")
		}
	})
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

func TestCodexInstallAcceptsOwnedAbsolutePathAndPreservesConfigBytes(t *testing.T) {
	realDir := searchableTempDir(t)
	realVP := writeLookupFixture(t, realDir, "vp")
	paths := []struct {
		name string
		path string
	}{
		{name: "real path", path: realVP},
	}
	if runtime.GOOS != "windows" {
		alias := filepath.Join(t.TempDir(), "vp-alias")
		if err := os.Symlink(realVP, alias); err != nil {
			t.Fatalf("create vp alias: %v", err)
		}
		paths = append(paths, struct {
			name string
			path string
		}{name: "symlink alias", path: alias})
	}

	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			original := strings.Replace(codexLiveFixture(),
				`command = "/old/path/vp" # command comment`,
				`command = "`+tc.path+`" # command comment`, 1)
			original = strings.Replace(original, `args = ["mcp"]`, `args = ["old"]`, 1)

			installedFixture := strings.Replace(original, "enabled = false", "enabled = true", 1)
			installedHost, _ := newTestCodexHost(t, installedFixture)
			installedHost.vpLookPath = func() (string, error) { return realVP, nil }
			installed, err := installedHost.Installed()
			if err != nil || !installed {
				t.Fatalf("Installed() = %v, %v for %q", installed, err, tc.path)
			}

			h, config := newTestCodexHost(t, original)
			h.vpLookPath = func() (string, error) { return realVP, nil }
			if err := h.Install("test", t.TempDir(), nil); err != nil {
				t.Fatal(err)
			}

			want := strings.Replace(original,
				`command = "`+tc.path+`" # command comment`,
				`command = "vp" # command comment`, 1)
			want = strings.Replace(want, `args = ["old"]`, `args = ["mcp"]`, 1)
			want = strings.Replace(want, "enabled = false", "enabled = true", 1)
			got := string(mustRead(t, config))
			if got != want {
				t.Fatalf("Install changed bytes outside owned command/args/enabled values\nwant:\n%s\ngot:\n%s", want, got)
			}
		})
	}
}

func TestCodexInstallRefusesForeignSameBasenameWithoutMutation(t *testing.T) {
	realDir := searchableTempDir(t)
	realVP := writeLookupFixture(t, realDir, "vp")
	foreignDir := searchableTempDir(t)
	foreignVP := writeLookupFixture(t, foreignDir, "vp")
	if realVP == foreignVP {
		t.Fatal("fixture paths unexpectedly match")
	}
	original := strings.Replace(codexLiveFixture(),
		`command = "/old/path/vp" # command comment`,
		`command = "`+foreignVP+`" # command comment`, 1)
	h, config := newTestCodexHost(t, original)
	h.vpLookPath = func() (string, error) { return realVP, nil }

	if err := h.Install("test", t.TempDir(), nil); err == nil {
		t.Fatal("foreign same-basename executable was accepted")
	}
	if got := string(mustRead(t, config)); got != original {
		t.Fatalf("refused install changed config bytes\nwant:\n%s\ngot:\n%s", original, got)
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
	h := &CodexHost{vpLookPath: func() (string, error) { return alias, nil }}
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
		configPath:    config,
		homeDir:       func() (string, error) { return home, nil },
		codexLookPath: func() (string, error) { return "/usr/bin/codex", nil },
		vpLookPath:    func() (string, error) { return "/usr/bin/vp", nil },
		runner:        func(...string) ([]byte, error) { return nil, nil },
	}, config
}

func searchableTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatalf("make PATH fixture searchable: %v", err)
		}
	}
	return dir
}

func writeLookupFixture(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("lookup-only executable fixture\n"), 0o755); err != nil {
		t.Fatalf("write executable fixture %q: %v", path, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatalf("chmod executable fixture %q: %v", path, err)
		}
	}
	return path
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
