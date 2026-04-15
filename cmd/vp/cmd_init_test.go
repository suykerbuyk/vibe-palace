// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// markProjectDir writes a minimal go.mod in dir so DetectSignal classifies
// it as a project. Use this for tests that work with fresh tmpdirs and
// expect vp init to create .vibe-palace.toml.
func markProjectDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644); err != nil {
		t.Fatalf("mark project dir: %v", err)
	}
}

// initTestEnv sets up an isolated XDG_CONFIG_HOME so init tests don't
// touch the real config. If preCreateConfig is true, writes a minimal
// config so global init is skipped (tests that focus on project init).
func initTestEnv(t *testing.T, preCreateConfig bool) string {
	t.Helper()
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	if preCreateConfig {
		vpDir := filepath.Join(configDir, "vibe-palace")
		os.MkdirAll(vpDir, 0o755)
		vaultDir := t.TempDir()
		content := `vault_path = "` + vaultDir + `"` + "\ngit_enabled = true\n"
		os.WriteFile(filepath.Join(vpDir, "config.toml"), []byte(content), 0o644)
	}
	return configDir
}

func TestInitCreatesConfig(t *testing.T) {
	initTestEnv(t, true) // skip global init
	dir := t.TempDir()
	markProjectDir(t, dir)
	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	code := cmd.Run([]string{dir, "--name", "test-proj"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	configPath := filepath.Join(dir, project.ConfigFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config not created: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `name = "test-proj"`) {
		t.Errorf("config missing project name: %s", content)
	}
	// The richer template carries a [meta] schema-version block.
	if !strings.Contains(content, "[meta]") {
		t.Errorf("cwd config missing [meta] block: %s", content)
	}
	if !strings.Contains(content, "version_major = 1") {
		t.Errorf("cwd config missing version_major: %s", content)
	}
	// vault_path example line must remain commented when flag not passed.
	if !strings.Contains(content, `# vault_path = "~/work-palace-vault"`) {
		t.Errorf("cwd config missing commented vault_path example: %s", content)
	}
}

// After a successful init, the vault-project config.toml exists and
// carries the [meta] block. This covers the Fix 1b wiring in cmd_init.
func TestInitWritesVaultProjectConfig(t *testing.T) {
	configDir := initTestEnv(t, true)
	// Read the vault path set by initTestEnv.
	globalData, err := os.ReadFile(filepath.Join(configDir, "vibe-palace", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// Crude extract — the helper wrote: vault_path = "<path>"
	vaultDir := ""
	for _, line := range strings.Split(string(globalData), "\n") {
		if strings.HasPrefix(line, "vault_path = ") {
			vaultDir = strings.Trim(strings.TrimPrefix(line, "vault_path = "), `"`)
			break
		}
	}
	if vaultDir == "" {
		t.Fatal("could not determine vault path from global config")
	}

	dir := t.TempDir()
	markProjectDir(t, dir)
	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	code := cmd.Run([]string{dir, "--name", "vp-init-proj"})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	vpCfg := filepath.Join(vaultDir, "Projects", "vp-init-proj", "config.toml")
	data, err := os.ReadFile(vpCfg)
	if err != nil {
		t.Fatalf("vault-project config not created at %s: %v", vpCfg, err)
	}
	content := string(data)
	if !strings.Contains(content, "[meta]") {
		t.Errorf("vault-project config missing [meta]: %s", content)
	}
	if !strings.Contains(content, `kind = "vault-project"`) && !strings.Contains(content, `# kind = "vault-project"`) {
		t.Errorf("vault-project config missing kind marker: %s", content)
	}
}

func TestInitWithDomainAndTags(t *testing.T) {
	initTestEnv(t, true)
	dir := t.TempDir()
	markProjectDir(t, dir)
	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	code := cmd.Run([]string{dir, "--name", "myapp", "--domain", "work", "--tags", "go,cli"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	data, _ := os.ReadFile(filepath.Join(dir, project.ConfigFileName))
	content := string(data)
	if !strings.Contains(content, `domain = "work"`) {
		t.Errorf("missing domain: %s", content)
	}
	if !strings.Contains(content, "tags") {
		t.Errorf("missing tags: %s", content)
	}
}

func TestInitRefusesOverwrite(t *testing.T) {
	initTestEnv(t, true)
	dir := t.TempDir()
	// Create existing project config.
	os.WriteFile(filepath.Join(dir, project.ConfigFileName), []byte("exists"), 0o644)

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	code := cmd.Run([]string{dir, "--name", "test"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d (should skip existing project)", code, cli.ExitOK)
	}
}

func TestInitInvalidName(t *testing.T) {
	initTestEnv(t, true)
	dir := t.TempDir()
	markProjectDir(t, dir)
	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	code := cmd.Run([]string{dir, "--name", "INVALID NAME!"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (invalid name)", code, cli.ExitUser)
	}
}

func TestInitAutoDetectsName(t *testing.T) {
	initTestEnv(t, true)
	dir := filepath.Join(t.TempDir(), "my-project")
	os.MkdirAll(dir, 0o755)
	markProjectDir(t, dir)

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	code := cmd.Run([]string{dir})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	data, _ := os.ReadFile(filepath.Join(dir, project.ConfigFileName))
	if !strings.Contains(string(data), "my-project") {
		t.Errorf("expected auto-detected name: %s", data)
	}
}

func TestInitBadFlags(t *testing.T) {
	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	code := cmd.Run([]string{"--unknown-flag"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (bad flags)", code, cli.ExitUser)
	}
}

func TestInitGlobalAndProject(t *testing.T) {
	configDir := initTestEnv(t, false) // no pre-created config
	projDir := t.TempDir()
	markProjectDir(t, projDir)
	vaultDir := filepath.Join(t.TempDir(), "vault")

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	code := cmd.Run([]string{projDir, "--name", "myapp", "--vault-path", vaultDir})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	// Global config must exist.
	globalConfig := filepath.Join(configDir, "vibe-palace", "config.toml")
	if _, err := os.Stat(globalConfig); err != nil {
		t.Errorf("global config not created: %v", err)
	}

	// Project config must exist.
	projConfig := filepath.Join(projDir, project.ConfigFileName)
	if _, err := os.Stat(projConfig); err != nil {
		t.Errorf("project config not created: %v", err)
	}
}

func TestInitSkipsExistingConfig(t *testing.T) {
	configDir := initTestEnv(t, true) // pre-create config

	// Read the existing config content.
	globalConfig := filepath.Join(configDir, "vibe-palace", "config.toml")
	before, _ := os.ReadFile(globalConfig)

	projDir := t.TempDir()
	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	cmd.Run([]string{projDir, "--name", "test"})

	// Config must not have been overwritten.
	after, _ := os.ReadFile(globalConfig)
	if string(before) != string(after) {
		t.Error("global config was overwritten, should have been skipped")
	}
}

func TestInitSkipsProjectInHomeDir(t *testing.T) {
	initTestEnv(t, true)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	code := cmd.Run([]string{home, "--name", "test"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	// Must NOT create .vibe-palace.toml in home.
	if _, err := os.Stat(filepath.Join(home, project.ConfigFileName)); err == nil {
		// Clean up if accidentally created.
		os.Remove(filepath.Join(home, project.ConfigFileName))
		t.Error("should not create project config in $HOME")
	}
}

func TestInitVaultPathFlag(t *testing.T) {
	configDir := initTestEnv(t, false)
	vaultDir := filepath.Join(t.TempDir(), "custom-vault")

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	// Run from a temp dir (not home) so project init is attempted but fails gracefully.
	projDir := t.TempDir()
	markProjectDir(t, projDir)
	code := cmd.Run([]string{projDir, "--vault-path", vaultDir, "--name", "test"})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	// Verify config has custom vault path.
	data, _ := os.ReadFile(filepath.Join(configDir, "vibe-palace", "config.toml"))
	if !strings.Contains(string(data), vaultDir) {
		t.Errorf("config does not contain custom vault path %s: %s", vaultDir, data)
	}

	// Vault directory must have been created.
	if _, err := os.Stat(vaultDir); err != nil {
		t.Errorf("vault directory not created: %v", err)
	}

	// The cwd .vibe-palace.toml must also carry the vault_path override.
	cwdFile := filepath.Join(projDir, project.ConfigFileName)
	cwdData, err := os.ReadFile(cwdFile)
	if err != nil {
		t.Fatalf("cwd config not created: %v", err)
	}
	if !strings.Contains(string(cwdData), "vault_path = \""+vaultDir+"\"") {
		t.Errorf("cwd config missing vault_path override:\n%s", cwdData)
	}
}

// When the global config already exists, passing --vault-path to vp init
// records the override only in the cwd .vibe-palace.toml, leaving global
// untouched (matching the work/personal split use case).
func TestInitVaultPathWritesCwdOverride(t *testing.T) {
	configDir := initTestEnv(t, true) // global config already exists
	altVault := filepath.Join(t.TempDir(), "alt-vault")

	projDir := t.TempDir()
	markProjectDir(t, projDir)
	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	code := cmd.Run([]string{projDir, "--vault-path", altVault, "--name", "alt-proj"})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	// Global config must NOT have been rewritten to the new path.
	globalData, _ := os.ReadFile(filepath.Join(configDir, "vibe-palace", "config.toml"))
	if strings.Contains(string(globalData), altVault) {
		t.Errorf("global config unexpectedly contains override path: %s", globalData)
	}

	// Cwd file must carry the override.
	cwdData, err := os.ReadFile(filepath.Join(projDir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("cwd config not created: %v", err)
	}
	if !strings.Contains(string(cwdData), "vault_path = \""+altVault+"\"") {
		t.Errorf("cwd config missing vault_path override:\n%s", cwdData)
	}

	// And resolving from the project dir picks up the override.
	path, source, err := storage.ResolveVaultPath(projDir)
	if err != nil {
		t.Fatalf("ResolveVaultPath: %v", err)
	}
	if path != altVault {
		t.Errorf("resolved path = %q, want %q", path, altVault)
	}
	if !strings.HasPrefix(source, "cwd:") {
		t.Errorf("source = %q, want cwd: prefix", source)
	}
}

func TestInitNoGitFlag(t *testing.T) {
	configDir := initTestEnv(t, false)
	projDir := t.TempDir()
	vaultDir := filepath.Join(t.TempDir(), "vault")

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	code := cmd.Run([]string{projDir, "--no-git", "--name", "test", "--vault-path", vaultDir})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	data, _ := os.ReadFile(filepath.Join(configDir, "vibe-palace", "config.toml"))
	if !strings.Contains(string(data), "git_enabled = false") {
		t.Errorf("config should have git_enabled = false: %s", data)
	}
}

// captureStdout runs fn while redirecting os.Stdout to a pipe and returns
// the captured bytes. It is the minimum plumbing needed to assert on the
// status table that `vp init` renders at end-of-run.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 4096)
		for {
			n, err := r.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
			}
			if err != nil {
				break
			}
		}
		done <- string(buf)
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

// TestInitStatusTableRendered verifies that vp init writes a status table
// with the expected [pass|skip] rows and summary line to stdout.
func TestInitStatusTableRendered(t *testing.T) {
	initTestEnv(t, false)
	projDir := t.TempDir()
	markProjectDir(t, projDir)
	vaultDir := filepath.Join(t.TempDir(), "vault")

	out := captureStdout(t, func() {
		cmd := cmdInit(cli.BuildInfo{Version: "test"})
		if code := cmd.Run([]string{projDir, "--name", "alpha", "--vault-path", vaultDir, "--no-git"}); code != cli.ExitOK {
			t.Fatalf("exit code = %d", code)
		}
	})

	wantFragments := []string{
		"vp init — vibe-palace test",
		"[pass] Global config",
		"[pass] Vault",
		"[pass] Project config",
		"go.mod detected",
		// No agent files in this fresh tmpdir — agent wiring reports skip
		// with the "create CLAUDE.md/AGENTS.md" hint (plus the .github/
		// copilot skip line).
		"[skip] Agent wiring",
		"no agent file found",
		"Summary:",
		"it is idempotent",
	}
	for _, frag := range wantFragments {
		if !strings.Contains(out, frag) {
			t.Errorf("missing %q in output:\n%s", frag, out)
		}
	}
}

// TestInitFreshThenIdempotent proves the three-stage sequence from the
// Phase 1 plan: a fresh init creates all artifacts, a re-run on the same
// directory is idempotent (no overwrites, still ExitOK), and the status
// table on the second run reports [info] for the already-present rows.
func TestInitFreshThenIdempotent(t *testing.T) {
	initTestEnv(t, false)
	projDir := t.TempDir()
	markProjectDir(t, projDir)
	vaultDir := filepath.Join(t.TempDir(), "vault")
	args := []string{projDir, "--name", "alpha", "--vault-path", vaultDir, "--no-git"}

	// Stage 1: fresh run creates the full fileset.
	out1 := captureStdout(t, func() {
		cmd := cmdInit(cli.BuildInfo{Version: "test"})
		if code := cmd.Run(args); code != cli.ExitOK {
			t.Fatalf("stage 1 exit = %d", code)
		}
	})
	if !strings.Contains(out1, "[pass] Project config") {
		t.Errorf("stage 1 missing [pass] Project config row:\n%s", out1)
	}

	cwdCfg := filepath.Join(projDir, project.ConfigFileName)
	before, err := os.ReadFile(cwdCfg)
	if err != nil {
		t.Fatalf("stage 1 did not create %s: %v", cwdCfg, err)
	}

	// Stage 2: idempotent re-run — exit OK, no overwrite, both global and
	// project configs report [info] (already exists).
	out2 := captureStdout(t, func() {
		cmd := cmdInit(cli.BuildInfo{Version: "test"})
		if code := cmd.Run(args); code != cli.ExitOK {
			t.Fatalf("stage 2 exit = %d", code)
		}
	})
	after, _ := os.ReadFile(cwdCfg)
	if string(before) != string(after) {
		t.Error("stage 2 overwrote existing project config")
	}
	if !strings.Contains(out2, "[info] Global config") {
		t.Errorf("stage 2 missing [info] Global config row:\n%s", out2)
	}
	if !strings.Contains(out2, "[info] Project config") {
		t.Errorf("stage 2 missing [info] Project config row:\n%s", out2)
	}
}

// TestInitMatchesConfigSyncDryRun proves the Phase 3 acceptance criterion:
// after `vp init`, running `vp config sync --dry-run` reports no actionable
// drift across the config tiers — the two commands' reconcilers see the
// same world.
func TestInitMatchesConfigSyncDryRun(t *testing.T) {
	initTestEnv(t, false)
	projDir := t.TempDir()
	markProjectDir(t, projDir)
	vaultDir := filepath.Join(t.TempDir(), "vault")

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	if code := cmd.Run([]string{projDir, "--name", "parity", "--vault-path", vaultDir, "--no-git"}); code != cli.ExitOK {
		t.Fatalf("init exit = %d", code)
	}

	out := captureStdout(t, func() {
		if code := runConfigSync([]string{"--project-root", projDir, "--dry-run"}); code != cli.ExitOK {
			t.Fatalf("config sync --dry-run exit = %d", code)
		}
	})
	if strings.Contains(out, "[Create]") || strings.Contains(out, "[Update]") {
		t.Errorf("config sync after init reported actionable drift:\n%s", out)
	}
}

// TestInitDetectsManifestOnlyProject confirms that vp init treats a
// directory with only a go.mod (no .git) as a project — the multi-manifest
// heuristic added in Phase 1.
func TestInitDetectsManifestOnlyProject(t *testing.T) {
	initTestEnv(t, true)
	projDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projDir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Explicitly no .git dir.

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	if code := cmd.Run([]string{projDir, "--name", "manifest-only"}); code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	if _, err := os.Stat(filepath.Join(projDir, project.ConfigFileName)); err != nil {
		t.Errorf("project config not created on manifest-only dir: %v", err)
	}
}

// TestInitForceSkipInSandboxedHome verifies that when cwd resolves to
// the user's $HOME, project init is force-skipped regardless of any
// project signals present. We sandbox $HOME to a tmpdir so the test
// does not touch the developer's real home.
func TestInitForceSkipInSandboxedHome(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	initTestEnv(t, true)

	// Even with a go.mod present, running init *at* $HOME must skip.
	if err := os.WriteFile(filepath.Join(fakeHome, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		cmd := cmdInit(cli.BuildInfo{Version: "test"})
		if code := cmd.Run([]string{fakeHome, "--name", "should-not-exist"}); code != cli.ExitOK {
			t.Fatalf("exit code = %d", code)
		}
	})

	if _, err := os.Stat(filepath.Join(fakeHome, project.ConfigFileName)); err == nil {
		t.Error("project config should not be created at $HOME")
	}
	if !strings.Contains(out, "[skip] Project config") {
		t.Errorf("expected [skip] Project config row for $HOME, got:\n%s", out)
	}
}

// TestInitEmitsShims verifies that vp init writes a vpc-<name>.md shim into
// .claude/commands/ for every command the resolver reports, that the files
// carry the shim marker, and that re-running init is a no-op (Apply records
// every file as Unchanged).
func TestInitEmitsShims(t *testing.T) {
	configDir := initTestEnv(t, false)
	projDir := t.TempDir()
	markProjectDir(t, projDir)
	vaultDir := filepath.Join(t.TempDir(), "vault")

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	if code := cmd.Run([]string{projDir, "--name", "shimtest", "--vault-path", vaultDir}); code != cli.ExitOK {
		t.Fatalf("first init exit code = %d", code)
	}
	_ = configDir // silence unused

	shimDir := filepath.Join(projDir, ".claude", "commands")
	entries, err := os.ReadDir(shimDir)
	if err != nil {
		t.Fatalf("read shim dir: %v", err)
	}
	var shimFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "vpc-") && strings.HasSuffix(e.Name(), ".md") {
			shimFiles = append(shimFiles, e.Name())
		}
	}
	if len(shimFiles) == 0 {
		t.Fatalf("no vpc-*.md shims emitted in %s", shimDir)
	}

	// Spot-check: the first shim must carry the marker.
	first := filepath.Join(shimDir, shimFiles[0])
	body, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("read shim %s: %v", first, err)
	}
	if !strings.Contains(string(body), "vibe-palace:shim v=") {
		t.Errorf("shim %s missing marker:\n%s", first, body)
	}

	// Status row appears in init output.
	out := captureStdout(t, func() {
		cmd2 := cmdInit(cli.BuildInfo{Version: "test"})
		if code := cmd2.Run([]string{projDir, "--name", "shimtest"}); code != cli.ExitOK {
			t.Fatalf("second init exit code = %d", code)
		}
	})
	if !strings.Contains(out, "Slash-command shims") {
		t.Errorf("expected Slash-command shims status row in:\n%s", out)
	}
	// Second run must be all-unchanged (idempotent).
	if !strings.Contains(out, "added 0") || !strings.Contains(out, "updated 0") {
		t.Errorf("second init should be idempotent; got:\n%s", out)
	}
}

// TestInitShimsCustomFileLeftAlone verifies that a user-authored
// .claude/commands/vpc-mine.md without our marker is reported as custom
// and never overwritten by init.
func TestInitShimsCustomFileLeftAlone(t *testing.T) {
	initTestEnv(t, false)
	projDir := t.TempDir()
	markProjectDir(t, projDir)
	vaultDir := filepath.Join(t.TempDir(), "vault")

	shimDir := filepath.Join(projDir, ".claude", "commands")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	customPath := filepath.Join(shimDir, "vpc-mine.md")
	customBody := "# my hand-rolled shim — keep me\n"
	if err := os.WriteFile(customPath, []byte(customBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	if code := cmd.Run([]string{projDir, "--name", "customtest", "--vault-path", vaultDir}); code != cli.ExitOK {
		t.Fatalf("init exit code = %d", code)
	}

	got, err := os.ReadFile(customPath)
	if err != nil {
		t.Fatalf("read custom shim: %v", err)
	}
	if string(got) != customBody {
		t.Errorf("custom shim was modified:\n got: %q\nwant: %q", got, customBody)
	}
}

// TestInitShimsSkippedWhenNoProject verifies initShimWiring emits no row
// when the project config could not be created (e.g. running at $HOME).
// The agent-wiring skip row is the user-visible signal; the shim row must
// stay silent rather than echo a redundant skip.
func TestInitShimsSkippedWhenNoProject(t *testing.T) {
	initTestEnv(t, true)
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	out := captureStdout(t, func() {
		cmd := cmdInit(cli.BuildInfo{Version: "test"})
		if code := cmd.Run([]string{fakeHome, "--name", "x"}); code != cli.ExitOK {
			t.Fatalf("exit code = %d", code)
		}
	})
	if strings.Contains(out, "Slash-command shims") {
		t.Errorf("did not expect Slash-command shims row when project init skipped:\n%s", out)
	}
}

// TestInitMaterializesTemplates verifies Phase 3.1: after vp init the
// vault's Templates/ tree contains embedded resources (byte-identical),
// templates.lock exists with an entry for each, and the vault
// .gitignore carries the canonical *.bak / *.new patterns.
func TestInitMaterializesTemplates(t *testing.T) {
	initTestEnv(t, false)
	projDir := t.TempDir()
	markProjectDir(t, projDir)
	vaultDir := filepath.Join(t.TempDir(), "vault")

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	code := cmd.Run([]string{projDir, "--name", "tpl-test", "--vault-path", vaultDir, "--no-git"})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	// Walk the embedded corpus and assert each resource is on disk
	// with matching bytes.
	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded: %v", err)
	}
	if len(resources) == 0 {
		t.Fatal("WalkEmbedded returned zero resources")
	}
	cmdCount := 0
	for _, res := range resources {
		target := filepath.Join(vaultDir, "Templates", filepath.FromSlash(res.RelPath))
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("materialized file missing %s: %v", target, err)
		}
		if string(got) != string(res.Bytes) {
			t.Errorf("bytes mismatch for %s", res.RelPath)
		}
		if strings.HasPrefix(res.RelPath, "commands/") {
			cmdCount++
		}
	}
	if cmdCount == 0 {
		t.Error("expected at least one command materialized under Templates/commands/")
	}

	// Lock file must exist and cover every materialized resource.
	lock, err := templates.ReadLock(vaultDir)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	for _, res := range resources {
		key := "Templates/" + res.RelPath
		entry, ok := lock.Entries[key]
		if !ok {
			t.Errorf("lock missing entry for %s", key)
			continue
		}
		if entry.EmbeddedSHA != res.SHA256 {
			t.Errorf("lock sha mismatch for %s: got %s, want %s",
				key, entry.EmbeddedSHA, res.SHA256)
		}
	}

	// .gitignore must contain every canonical pattern (including
	// *.bak / *.new added for the template reconciler).
	giData, err := os.ReadFile(filepath.Join(vaultDir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	gi := string(giData)
	for _, want := range storage.CanonicalGitignorePatterns {
		if !strings.Contains(gi, want) {
			t.Errorf(".gitignore missing pattern %q; got:\n%s", want, gi)
		}
	}
	// Spot-check the two added in Phase 3.
	for _, want := range []string{"*.bak", "*.new"} {
		if !strings.Contains(gi, want) {
			t.Errorf(".gitignore missing %q; got:\n%s", want, gi)
		}
	}
}

// TestInitScaffoldsCurrentProject (Phase 4) verifies that vp init
// creates Projects/<slug>/commands/ and Projects/<slug>/skills/ with
// README stubs rendered via templates.RenderReadmeStub.
func TestInitScaffoldsCurrentProject(t *testing.T) {
	configDir := initTestEnv(t, true)
	globalData, _ := os.ReadFile(filepath.Join(configDir, "vibe-palace", "config.toml"))
	vaultDir := ""
	for _, line := range strings.Split(string(globalData), "\n") {
		if strings.HasPrefix(line, "vault_path = ") {
			vaultDir = strings.Trim(strings.TrimPrefix(line, "vault_path = "), `"`)
			break
		}
	}
	if vaultDir == "" {
		t.Fatal("could not determine vault path")
	}

	dir := t.TempDir()
	markProjectDir(t, dir)
	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	if code := cmd.Run([]string{dir, "--name", "scaffold-proj"}); code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	projBase := filepath.Join(vaultDir, "Projects", "scaffold-proj")
	for _, kind := range []string{"commands", "skills"} {
		subDir := filepath.Join(projBase, kind)
		if info, err := os.Stat(subDir); err != nil || !info.IsDir() {
			t.Errorf("missing dir %s: %v", subDir, err)
			continue
		}
		readmePath := filepath.Join(subDir, "README.md")
		body, err := os.ReadFile(readmePath)
		if err != nil {
			t.Errorf("missing README %s: %v", readmePath, err)
			continue
		}
		if string(body) != templates.RenderReadmeStub(kind) {
			t.Errorf("%s README body mismatch", kind)
		}
	}
}

// TestInitScaffoldPreservesUserOverrides (Phase 4): a user-authored
// override file in Projects/<slug>/commands/foo.md must survive vp
// init. Scaffold mode is write-if-absent.
func TestInitScaffoldPreservesUserOverrides(t *testing.T) {
	configDir := initTestEnv(t, true)
	globalData, _ := os.ReadFile(filepath.Join(configDir, "vibe-palace", "config.toml"))
	vaultDir := ""
	for _, line := range strings.Split(string(globalData), "\n") {
		if strings.HasPrefix(line, "vault_path = ") {
			vaultDir = strings.Trim(strings.TrimPrefix(line, "vault_path = "), `"`)
			break
		}
	}
	// Pre-create the override before running init.
	overridePath := filepath.Join(vaultDir, "Projects", "pre-existing", "commands", "foo.md")
	if err := os.MkdirAll(filepath.Dir(overridePath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const userBody = "# user command override\nhello\n"
	if err := os.WriteFile(overridePath, []byte(userBody), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}

	dir := t.TempDir()
	markProjectDir(t, dir)
	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	if code := cmd.Run([]string{dir, "--name", "pre-existing"}); code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	got, err := os.ReadFile(overridePath)
	if err != nil {
		t.Fatalf("override gone: %v", err)
	}
	if string(got) != userBody {
		t.Errorf("override clobbered: got %q", got)
	}
}

// captureStderr swaps os.Stderr for a pipe, runs fn, and returns whatever
// fn wrote to stderr. Restores os.Stderr on return.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		chunk := make([]byte, 4096)
		for {
			n, rerr := r.Read(chunk)
			if n > 0 {
				sb.Write(chunk[:n])
			}
			if rerr != nil {
				break
			}
		}
		done <- sb.String()
	}()

	fn()
	_ = w.Close()
	return <-done
}

// TestInitBogusPositionalFailsBeforeWrites proves that Fix 1 aborts before
// any filesystem writes when a user-supplied positional doesn't exist.
// This is the regression lock: if the existence check ever slides back down
// into initProject, the short-circuit assertions below will fail because
// initGlobal would have already written the config + vault.
func TestInitBogusPositionalFailsBeforeWrites(t *testing.T) {
	configDir := initTestEnv(t, false) // no pre-existing global config
	// Redirect HOME so the default-vault fallback can't clobber the
	// developer's real $HOME if the fix ever regresses.
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	bogus := filepath.Join(t.TempDir(), "does-not-exist")

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	var code int
	stderr := captureStderr(t, func() {
		code = cmd.Run([]string{bogus})
	})

	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (ExitUser)", code, cli.ExitUser)
	}
	if !strings.Contains(stderr, "--vault-path") {
		t.Errorf("stderr missing --vault-path hint: %q", stderr)
	}
	if !strings.Contains(stderr, "does not exist") {
		t.Errorf("stderr missing 'does not exist': %q", stderr)
	}

	// Protective assertions: no side effects.
	if _, err := os.Stat(filepath.Join(configDir, "vibe-palace", "config.toml")); !os.IsNotExist(err) {
		t.Errorf("global config was created despite bogus positional: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(fakeHome, "vibe-palace-vault")); !os.IsNotExist(err) {
		t.Errorf("default vault was created despite bogus positional: err=%v", err)
	}
}

// TestInitPositionalPermissionDeniedReturnsSystemExit checks that non-ENOENT
// stat errors (here: an unreadable parent dir) surface as ExitSystem
// without the "did you mean" hint. The hint would mis-attribute a real
// filesystem problem to user error.
func TestInitPositionalPermissionDeniedReturnsSystemExit(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root — mode 0o000 does not deny root")
	}
	initTestEnv(t, false)
	t.Setenv("HOME", t.TempDir())

	// Build an unreadable parent with an inaccessible child path.
	parent := filepath.Join(t.TempDir(), "locked")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	child := filepath.Join(parent, "proj")
	// Drop execute perms on parent so stat(child) returns EACCES.
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatalf("chmod parent: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	var code int
	stderr := captureStderr(t, func() {
		code = cmd.Run([]string{child})
	})

	if code != cli.ExitSystem {
		t.Errorf("exit code = %d, want %d (ExitSystem)", code, cli.ExitSystem)
	}
	if strings.Contains(stderr, "--vault-path") {
		t.Errorf("stderr should not carry 'did you mean --vault-path' hint for permission errors: %q", stderr)
	}
}

// TestInitPositionalValidDirStillWorks regression-locks the happy path.
// A user-supplied positional pointing at a real, project-marked dir must
// continue to succeed exactly as before Fix 1.
func TestInitPositionalValidDirStillWorks(t *testing.T) {
	initTestEnv(t, true) // pre-created global config, skips initGlobal writes
	dir := t.TempDir()
	markProjectDir(t, dir)

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	code := cmd.Run([]string{dir, "--name", "happy-path"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want ExitOK", code)
	}

	if _, err := os.Stat(filepath.Join(dir, project.ConfigFileName)); err != nil {
		t.Errorf(".vibe-palace.toml missing on happy-path init: %v", err)
	}
}
