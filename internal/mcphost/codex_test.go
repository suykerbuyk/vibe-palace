// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcphost

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCodexInstallEnablesExpectedStdioServer(t *testing.T) {
	h, config := newTestCodexHost(t, "")
	project := t.TempDir()
	firstOutput := strings.Builder{}
	if err := h.Install("test", project, &firstOutput); err != nil {
		t.Fatal(err)
	}
	// The install that actually wrote the file must say the hooks need trust.
	if !strings.Contains(firstOutput.String(), "CODEX HOOKS REWRITTEN") {
		t.Fatalf("hook was reported active without telling the operator to trust it: %s", firstOutput.String())
	}
	// The repeat install is a byte-level no-op, so it must say nothing: a
	// warning that fires on every run is the cry-wolf this replaced.
	installOutput := strings.Builder{}
	if err := h.Install("test", project, &installOutput); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(installOutput.String(), "CODEX HOOKS REWRITTEN") {
		t.Fatalf("no-op install emitted a re-trust warning: %s", installOutput.String())
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
	// The real os.Executable() here is the .test binary, which is not the vp
	// fixture PATH resolves to, so the pin would refuse. Point selfExe at the
	// fixture: that is the state a real install has, and it keeps this test
	// about which lookup feeds which consumer.
	h.selfExe = func() (string, error) { return vpPath, nil }
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
	// The command is bare now, so the vp lookup no longer shows up as a path in
	// the file; what it feeds is the pin, which this Install just passed. The
	// codex fixture must still be nowhere near the hooks.
	hooks := string(mustRead(t, filepath.Join(configDir, "hooks.json")))
	if !strings.Contains(hooks, `"vp codex hook session-start"`) ||
		!strings.Contains(hooks, `"vp codex hook session-end"`) {
		t.Fatalf("hooks do not invoke a bare vp:\n%s", hooks)
	}
	if strings.Contains(hooks, vpPath) {
		t.Fatalf("hook command embeds the resolved vp path %q instead of a bare name:\n%s", vpPath, hooks)
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
			h.selfExe = func() (string, error) { return realVP, nil }
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
	// selfExe matches so the pin PASSES: this test is about rejecting a foreign
	// command already in the config, and must not be satisfied by the pin
	// refusing first for an unrelated reason.
	h.selfExe = func() (string, error) { return realVP, nil }

	err := h.Install("test", t.TempDir(), nil)
	if err == nil {
		t.Fatal("foreign same-basename executable was accepted")
	}
	// Assert WHICH refusal. Accepting any error let this test keep passing
	// while the pin refused first for an unrelated reason, so it silently
	// stopped measuring foreign-command ownership at all.
	if !strings.Contains(err.Error(), "foreign Codex MCP server") {
		t.Fatalf("refused for the wrong reason; this test is about foreign command ownership: %v", err)
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
		// selfExe must agree with vpLookPath or pinVPOnPath refuses every
		// Install below: under `go test` the real os.Executable() is the
		// package's .test binary. Tests that exercise the pin override both.
		selfExe: func() (string, error) { return "/usr/bin/vp", nil },
		runner:  func(...string) ([]byte, error) { return nil, nil },
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

// assertCodexInstallWroteNothing is the half of a pin-refusal assertion that
// actually matters: the pin must refuse BEFORE anything is written, so an error
// alone would also be satisfied by a pin that fires too late.
func assertCodexInstallWroteNothing(t *testing.T, config, original string) {
	t.Helper()
	if got := string(mustRead(t, config)); got != original {
		t.Fatalf("refused install changed config bytes\nwant:\n%s\ngot:\n%s", original, got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(config), "hooks.json")); !os.IsNotExist(err) {
		t.Fatalf("refused install created hooks.json (err=%v)", err)
	}
}

// TestCodexInstallRefusesWhenPathVPIsADifferentBinary is the measured hijack:
// PATH's first vp is some other executable, so a bare hook command would run
// that program instead. Codex reports nothing when a hook misbehaves, so an
// install that proceeded here would be silently wrong.
func TestCodexInstallRefusesWhenPathVPIsADifferentBinary(t *testing.T) {
	onPath := writeLookupFixture(t, searchableTempDir(t), "vp")
	self := writeLookupFixture(t, searchableTempDir(t), "vp")
	if onPath == self {
		t.Fatal("fixture paths unexpectedly match")
	}
	original := "[mcp_servers.other]\ncommand = \"x\"\n"
	h, config := newTestCodexHost(t, original)
	h.vpLookPath = func() (string, error) { return onPath, nil }
	h.selfExe = func() (string, error) { return self, nil }

	err := h.Install("test", t.TempDir(), nil)
	if err == nil {
		t.Fatal("install accepted a PATH vp that is a different binary")
	}
	for _, want := range []string{onPath, self, "first on PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal does not mention %q, so the operator cannot act on it: %v", want, err)
		}
	}
	assertCodexInstallWroteNothing(t, config, original)
}

// TestCodexInstallRefusesWhenNoVPIsOnPath covers the other resolution failure:
// a bare command cannot be written at all if the name resolves to nothing.
func TestCodexInstallRefusesWhenNoVPIsOnPath(t *testing.T) {
	original := "[mcp_servers.other]\ncommand = \"x\"\n"
	h, config := newTestCodexHost(t, original)
	h.vpLookPath = func() (string, error) { return "", exec.ErrNotFound }

	err := h.Install("test", t.TempDir(), nil)
	if err == nil {
		t.Fatal("install accepted a PATH with no vp on it")
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("refusal does not name PATH as the problem: %v", err)
	}
	assertCodexInstallWroteNothing(t, config, original)
}

// TestCodexInstallRefusesWhenLookupsAreNil proves a host that cannot answer the
// pin's question refuses instead of passing. An unanswerable pin that returned
// nil would be worse than no pin: it would look like verification.
func TestCodexInstallRefusesWhenLookupsAreNil(t *testing.T) {
	for name, breakHost := range map[string]func(*CodexHost){
		"nil vpLookPath": func(h *CodexHost) { h.vpLookPath = nil },
		"nil selfExe":    func(h *CodexHost) { h.selfExe = nil },
	} {
		t.Run(name, func(t *testing.T) {
			original := "[mcp_servers.other]\ncommand = \"x\"\n"
			h, config := newTestCodexHost(t, original)
			breakHost(h)
			if err := h.Install("test", t.TempDir(), nil); err == nil {
				t.Fatal("install proceeded without being able to verify the pin")
			}
			assertCodexInstallWroteNothing(t, config, original)
		})
	}
}

// TestCodexInstallPinAcceptsSymlinkAliasOfSelf is the control for the three
// refusals above. Without it they would all pass against a pin that compared
// raw strings, which would reject the ordinary case of PATH finding a symlink
// to the installed binary.
func TestCodexInstallPinAcceptsSymlinkAliasOfSelf(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not reliably available on Windows CI")
	}
	self := writeLookupFixture(t, searchableTempDir(t), "vp")
	alias := filepath.Join(t.TempDir(), "vp-alias")
	if err := os.Symlink(self, alias); err != nil {
		t.Fatalf("create vp alias: %v", err)
	}
	h, _ := newTestCodexHost(t, "")
	h.vpLookPath = func() (string, error) { return alias, nil }
	h.selfExe = func() (string, error) { return self, nil }

	if err := h.Install("test", t.TempDir(), nil); err != nil {
		t.Fatalf("pin refused a symlink alias of the installing binary: %v", err)
	}
}

// TestCodexHookCommandIsBareVP pins the written command exactly. Containment
// checks would pass on the old absolute form too, so this compares the whole
// string and then proves no path leaked in beside it.
func TestCodexHookCommandIsBareVP(t *testing.T) {
	fixtureDir := searchableTempDir(t)
	vpPath := writeLookupFixture(t, fixtureDir, "vp")
	h, config := newTestCodexHost(t, "")
	h.vpLookPath = func() (string, error) { return vpPath, nil }
	h.selfExe = func() (string, error) { return vpPath, nil }
	if err := h.Install("test", t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}

	raw := mustRead(t, filepath.Join(filepath.Dir(config), "hooks.json"))
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	hooks := doc["hooks"].(map[string]any)
	for event, want := range map[string]string{
		"SessionStart": "vp codex hook session-start",
		"SessionEnd":   "vp codex hook session-end",
	} {
		rows := hooks[event].([]any)
		got := rows[len(rows)-1].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
		if got != want {
			t.Fatalf("%s command = %q, want exactly %q", event, got, want)
		}
	}
	if strings.Contains(string(raw), fixtureDir) {
		t.Fatalf("a resolved path leaked into the hook file:\n%s", raw)
	}
	if strings.Contains(string(raw), `\"`) {
		t.Fatalf("the bare command was quoted; only the unquoted form is measured to run:\n%s", raw)
	}
}

// TestCodexInstallPinRefusesEvenWhenHooksDisabled locks the pin's position at
// the top of Install. The MCP server entry is written as the same bare "vp",
// so it needs the pin too; a pin that lived inside installCodexHooks would
// skip this branch entirely and still look correct everywhere else.
func TestCodexInstallPinRefusesEvenWhenHooksDisabled(t *testing.T) {
	onPath := writeLookupFixture(t, searchableTempDir(t), "vp")
	self := writeLookupFixture(t, searchableTempDir(t), "vp")
	original := "[features]\nhooks = false\n"
	h, config := newTestCodexHost(t, original)
	h.vpLookPath = func() (string, error) { return onPath, nil }
	h.selfExe = func() (string, error) { return self, nil }

	if err := h.Install("test", t.TempDir(), nil); err == nil {
		t.Fatal("install wrote the MCP entry on a hooks-disabled host without pinning vp")
	}
	assertCodexInstallWroteNothing(t, config, original)
}

// TestCodexInstallNoticeIsNotCategoricalOnAFormattingOnlyRewrite pins the
// distinction between the notice's TRIGGER and its CLAIM.
//
// The trigger is a byte change, which is deliberately wider than a trust
// change: Codex hashes the parsed hook, so reformatting the file flips the
// bytes while command, timeout and matcher are untouched and trust survives.
// The notice must therefore still fire — the operator should know vp rewrote
// the file — but it must NOT assert that the hooks have stopped, because on
// this path they have not.
//
// Measured before this test was written: on the pre-fix build the same
// sequence printed "THESE HOOKS WILL NOT RUN UNTIL YOU REVIEW AND TRUST THEM
// IN CODEX", which is categorically false here.
func TestCodexInstallNoticeIsNotCategoricalOnAFormattingOnlyRewrite(t *testing.T) {
	h, config := newTestCodexHost(t, "")
	project := t.TempDir()
	if err := h.Install("test", project, nil); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(filepath.Dir(config), "hooks.json")

	// Re-serialise the same document compactly: different bytes, identical
	// parsed hooks, so Codex's trust is unaffected.
	var doc map[string]any
	if err := json.Unmarshal(mustRead(t, hooksPath), &doc); err != nil {
		t.Fatal(err)
	}
	compact, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, compact, 0o600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := h.Install("test", project, &out); err != nil {
		t.Fatal(err)
	}
	notice := out.String()
	if !strings.Contains(notice, "CODEX HOOKS REWRITTEN") {
		t.Fatalf("a formatting-only rewrite changed the bytes but printed no notice; the changed-trigger regressed:\n%s", notice)
	}
	headline := strings.SplitN(notice, "\n", 2)[0]
	if strings.Contains(headline, "WILL NOT RUN") {
		t.Fatalf("notice headline asserts the hooks stopped, which is false after a formatting-only rewrite: %q", headline)
	}
	for _, want := range []string{"cannot tell you", "cannot grant trust"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice does not state what vp cannot determine (%q missing), so it reads as a certainty vp cannot establish:\n%s", want, notice)
		}
	}
}

// TestCodexHookTrustAdvisoryMakesNoReassuringClaim pins the notice's standing
// contract: it states only what vp did and what vp cannot determine, and never
// states that anything is unaffected, safe, or will not run.
//
// The contract exists because this notice has overclaimed three times, each
// time in a different sentence, so patching sentences one at a time was not
// working. The three real wordings that shipped and were wrong are included as
// explicit cases below, so none of them can come back.
//
// 🔴 Honest about what this is: a WORDLIST guard, not a proof. It cannot catch
// a reassuring claim phrased in words it does not know. It catches the class of
// regression that actually happened three times — a helpful-sounding promise
// about what the operator need not worry about — and it makes the contract
// executable rather than advisory. The doc comment on codexHookTrustAdvisory is
// the authority; this is the tripwire.
func TestCodexHookTrustAdvisoryMakesNoReassuringClaim(t *testing.T) {
	notice := codexHookTrustAdvisory("~/.codex/hooks.json")
	banned := []string{
		// Claims that trust survived.
		"unaffected",
		"nothing to do",
		"no action",
		"still trusted",
		"remains trusted",
		"is safe",
		"are safe",
		// Claims about a state vp cannot read.
		"will not run",
		"are now untrusted",
		"have been untrusted",
	}
	lower := strings.ToLower(notice)
	for _, phrase := range banned {
		if strings.Contains(lower, phrase) {
			t.Fatalf("notice claims %q, but vp cannot determine whether Codex trusts these hooks;\nthe contract is that it states only what vp did and what vp cannot determine:\n%s", phrase, notice)
		}
	}
	// The other half of the contract: it must still say what vp DID and what it
	// CANNOT determine, or an empty string would satisfy the bans above.
	for _, want := range []string{"Wrote ", "cannot read Codex's trust state", "cannot grant trust"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice omits %q, so it no longer states what vp did and what it cannot determine:\n%s", want, notice)
		}
	}
}
