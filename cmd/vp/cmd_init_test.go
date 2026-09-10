// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/context"
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

// initTestEnv isolates an init test from the developer's real machine.
//
// Three host-global roots must be redirected, because os.UserHomeDir and
// os.UserConfigDir each resolve differently per GOOS and .goreleaser.yml
// builds linux, darwin and windows:
//
//   - HOME redirects os.UserHomeDir on Unix, which is how `vp init` reaches
//     hook.Install() and rewrites ~/.claude/settings.json. USERPROFILE is
//     the Windows spelling of the same thing.
//   - XDG_CONFIG_HOME redirects the global config on Linux ONLY. On macOS
//     os.UserConfigDir reads ~/Library/Application Support and on Windows
//     %AppData%; neither consults XDG. macOS is covered anyway because that
//     path hangs off HOME — Windows is not.
//   - APPDATA is therefore required for the Windows global config. See
//     internal/integration/vaultlock_crossprocess_test.go, which sets it
//     after all 16 children escaped an XDG-only sandbox on 2026-07-26.
//
// Every var is set unconditionally; the ones that do not apply to the
// running GOOS are inert rather than wrong.
//
// Scope: this closes the ~/.claude and global-config routes. It does NOT
// make cmd/vp hermetic outright — cmd_check_test.go spawns the real `grok`
// binary, which still writes the developer's ~/.grok. That is tracked by
// check-tests-spawn-the-real-grok-binary-and-write-host-state.
//
// Both dirs are returned. Tests whose premise is "run vp init at a path
// equal to $HOME" must use the returned homeDir rather than installing a
// home of their own: two homes disagreeing would silently flip them onto the
// opposite branch while still passing.
//
// If preCreateConfig is true, writes a minimal config so global init is
// skipped (tests that focus on project init).
func initTestEnv(t *testing.T, preCreateConfig bool) (configDir, homeDir string) {
	t.Helper()
	configDir = t.TempDir()
	homeDir = t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("APPDATA", configDir)
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	if preCreateConfig {
		vpDir := filepath.Join(configDir, "vibe-palace")
		os.MkdirAll(vpDir, 0o755)
		vaultDir := t.TempDir()
		content := `vault_path = "` + vaultDir + `"` + "\ngit_enabled = true\n"
		os.WriteFile(filepath.Join(vpDir, "config.toml"), []byte(content), 0o644)
	}
	return configDir, homeDir
}

// realHome is the process's actual home directory, captured at package init
// — i.e. before any test's t.Setenv can shadow it. Nothing in cmd/vp reads
// HOME at init time, so evaluating it here is safe.
var realHome, _ = os.UserHomeDir()

// TestInitTestEnvSandboxesHostGlobals is the regression lock on the harness
// itself: if initTestEnv ever stops sandboxing HOME, `vp init` in the tests
// below would reach hook.Install() and rewrite the developer's real
// ~/.claude/settings.json.
func TestInitTestEnvSandboxesHostGlobals(t *testing.T) {
	configDir, homeDir := initTestEnv(t, false)

	got, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if got != homeDir {
		t.Errorf("os.UserHomeDir() = %q, want the sandboxed %q", got, homeDir)
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != configDir {
		t.Errorf("XDG_CONFIG_HOME = %q, want the sandboxed %q", xdg, configDir)
	}
	// APPDATA is what os.UserConfigDir reads on Windows and USERPROFILE what
	// os.UserHomeDir reads there; neither XDG_CONFIG_HOME nor HOME reaches
	// them, so the lock has to cover both explicitly or the Windows half of
	// the sandbox can rot without any test noticing.
	if appData := os.Getenv("APPDATA"); appData != configDir {
		t.Errorf("APPDATA = %q, want the sandboxed %q", appData, configDir)
	}
	if userProfile := os.Getenv("USERPROFILE"); userProfile != homeDir {
		t.Errorf("USERPROFILE = %q, want the sandboxed %q", userProfile, homeDir)
	}
	if realHome == "" {
		t.Skip("real home dir unknown; cannot assert isolation from it")
	}
	if homeDir == realHome {
		t.Errorf("sandboxed home %q is the real home %q", homeDir, realHome)
	}
	if configDir == filepath.Join(realHome, ".config") {
		t.Errorf("sandboxed config dir %q is the real one", configDir)
	}
}

// TestSetupTestVaultEnvSandboxesHostGlobals is the same lock for the other
// cmd/vp helper. setupTestVaultEnv returns only the vault root, so the
// assertions read the environment rather than a returned path.
func TestSetupTestVaultEnvSandboxesHostGlobals(t *testing.T) {
	setupTestVaultEnv(t)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if realHome != "" && home == realHome {
		t.Errorf("os.UserHomeDir() = %q, the real home", home)
	}
	if os.Getenv("USERPROFILE") != home {
		t.Errorf("USERPROFILE = %q, want %q", os.Getenv("USERPROFILE"), home)
	}
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" || (realHome != "" && configDir == filepath.Join(realHome, ".config")) {
		t.Errorf("XDG_CONFIG_HOME = %q, want a sandboxed dir", configDir)
	}
	if os.Getenv("APPDATA") != configDir {
		t.Errorf("APPDATA = %q, want %q", os.Getenv("APPDATA"), configDir)
	}
	// The cache pin is the one deliberate exception — see hostCacheDir.
	if hostCacheDir != "" && os.Getenv("XDG_CACHE_HOME") != hostCacheDir {
		t.Errorf("XDG_CACHE_HOME = %q, want the pinned %q",
			os.Getenv("XDG_CACHE_HOME"), hostCacheDir)
	}
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
	configDir, _ := initTestEnv(t, true)
	// Read the vault path set by initTestEnv.
	globalData, err := os.ReadFile(filepath.Join(configDir, "vibe-palace", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// Crude extract — the helper wrote: vault_path = "<path>"
	vaultDir := ""
	for line := range strings.SplitSeq(string(globalData), "\n") {
		if after, ok := strings.CutPrefix(line, "vault_path = "); ok {
			vaultDir = strings.Trim(after, `"`)
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
	configDir, _ := initTestEnv(t, false) // no pre-created config
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
	configDir, _ := initTestEnv(t, true) // pre-create config

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

func TestInitVaultPathFlag(t *testing.T) {
	configDir, _ := initTestEnv(t, false)
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
	configDir, _ := initTestEnv(t, true) // global config already exists
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
	configDir, _ := initTestEnv(t, false)
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
//
// # The byte-compatibility promise was dropped when onboarding moved to
// # internal/onboard, and these are the rows that changed
//
// The step table made three things visible that the old inline orchestration
// hid, and each is asserted below rather than left to be discovered:
//
//  1. VAULT-PROJECT IS ITS OWN ROW. It used to be a Details line appended to
//     the Project config row, and ONLY on failure — a successful
//     vault-project write rendered nothing at all. It is a Step now, so it
//     gets a row in both directions.
//  2. PROJECT-SCAFFOLD GAINS A SKIP. It already had a "Project templates" row,
//     but when vault-project failed the old code simply did not render one
//     (cmd_init.go's `if vaultProjectOK` guard). With Needs it produces a Skip
//     naming the prerequisite.
//  3. A RE-INIT REPORTS THREE OMISSIONS. See TestInitFreshThenIdempotent.
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
		// CHANGED: promoted from a failure-only Details line on Project config
		// to a row of its own, present on success too.
		"[pass] Vault project",
		"Projects/alpha/config.toml",
		// Unchanged in the happy path — pinned here because change (2) above
		// gives this row a second, Skip spelling.
		"[pass] Project templates",
		// init manages AGENTS.md as a host-local bootstrap shim, so even a
		// fresh tmpdir reports an Agent wiring row for it. The copilot
		// candidate still produces a [skip] row (.github/ absent).
		"Agent wiring",
		"AGENTS.md",
		"[skip] Agent wiring",
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
	// CHANGED, deliberately: the re-init branch used to render ONE [info]
	// Project config row and nothing else about project config. The marker
	// gate now declares the three project-config steps as onboard Omissions
	// instead of returning early, so all three are ACCOUNTED FOR — each gets a
	// [skip] row naming the command, the host and the artifact. The behaviour
	// is identical (nothing is re-touched); what changed is that the run says
	// so out loud instead of producing a silently shorter table.
	for _, want := range []string{
		"[skip] Project config",
		"(already exists, skipped)",
		"[skip] Vault project",
		"[skip] Project templates",
		"vp config sync --tier project --cwd",
	} {
		if !strings.Contains(out2, want) {
			t.Errorf("stage 2 missing %q:\n%s", want, out2)
		}
	}
	// The five downstream steps still RUN on a re-init — that is what makes
	// `vp init` idempotent rather than inert.
	for _, want := range []string{"Agent wiring", "Slash-command shims", "Project .gitignore"} {
		if !strings.Contains(out2, want) {
			t.Errorf("stage 2 missing downstream row %q:\n%s", want, out2)
		}
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
// project signals present. initTestEnv sandboxes $HOME to a tmpdir so the
// test does not touch the developer's real home.
//
// This is the sole cover for the force-skip branch. A second test,
// TestInitSkipsProjectInHomeDir, was deleted once HOME became sandboxed:
// it planted no project signal, so it exercised the "no signal" skip and
// never the $HOME one its name claimed. Planting a signal to fix it would
// have made it a duplicate of this test, which asserts the stronger
// property — the skip fires even WITH a go.mod present.
func TestInitForceSkipInSandboxedHome(t *testing.T) {
	_, fakeHome := initTestEnv(t, true)

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
	// initTestEnv isolates HOME so live user-global surfaces do not trigger
	// the Phase 4b skip path (GlobalSurfacesHealthy).
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg"))
	configDir, _ := initTestEnv(t, false)
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

// TestInitManagesAgentsFile verifies that `vp init` treats AGENTS.md like a
// host-local vp-managed bootstrap shim: it is created (even when absent from a
// fresh project), carries the managed vibe-palace block, and is gitignored.
func TestInitManagesAgentsFile(t *testing.T) {
	initTestEnv(t, false)
	projDir := t.TempDir()
	markProjectDir(t, projDir)
	vaultDir := filepath.Join(t.TempDir(), "vault")

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	if code := cmd.Run([]string{projDir, "--name", "agentstest", "--vault-path", vaultDir}); code != cli.ExitOK {
		t.Fatalf("init exit code = %d", code)
	}

	// AGENTS.md created and carries the managed block.
	agents, err := os.ReadFile(filepath.Join(projDir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("AGENTS.md not created: %v", err)
	}
	if !strings.Contains(string(agents), "<!-- vibe-palace:begin ") {
		t.Errorf("AGENTS.md missing managed block:\n%s", agents)
	}

	// Project .gitignore ignores the host-local AGENTS.md shim.
	gi, err := os.ReadFile(filepath.Join(projDir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(gi), "/AGENTS.md") {
		t.Errorf(".gitignore missing /AGENTS.md:\n%s", gi)
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
// when the project config could not be created — here because the target dir
// carries no project signal. (It does NOT reach the $HOME force-skip: the
// sandboxed home is a bare tmpdir, so DetectSignal returns SignalNone from
// the marker walk, never from isForceSkipDir. TestInitForceSkipInSandboxedHome
// is the cover for that branch.) The agent-wiring skip row is the
// user-visible signal; the shim row must stay silent rather than echo a
// redundant skip.
func TestInitShimsSkippedWhenNoProject(t *testing.T) {
	_, fakeHome := initTestEnv(t, true)

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

// TestInitMaterializesTemplates verifies the Design B (override-only)
// contract: after vp init the vault's Templates/ tree holds NO mirror of the
// embedded corpus (the embedded floor is served directly over MCP), the
// templates.lock is empty (no reconciler-owned mirror is tracked), yet every
// resource still resolves from the embedded tier. The vault .gitignore still
// carries the canonical *.bak / *.new patterns.
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

	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded: %v", err)
	}
	if len(resources) == 0 {
		t.Fatal("WalkEmbedded returned zero resources")
	}

	// No embedded resource is mirrored into the vault Templates/ tree.
	for _, res := range resources {
		target := filepath.Join(vaultDir, "Templates", filepath.FromSlash(res.RelPath))
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Errorf("override-only init should not materialize %s (err=%v)", res.RelPath, err)
		}
	}

	// The lock is empty — nothing reconciler-owned to track.
	lock, err := templates.ReadLock(vaultDir)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if len(lock.Entries) != 0 {
		t.Errorf("templates.lock should be empty on a fresh override-only vault, got %d entries", len(lock.Entries))
	}

	// The embedded floor still resolves a command byte-for-byte.
	content, source, err := context.NewResolver(vaultDir).Resolve("command:wrap", "")
	if err != nil {
		t.Fatalf("resolve command:wrap: %v", err)
	}
	if source != "embedded" {
		t.Errorf("command:wrap resolved from %q, want embedded", source)
	}
	if content == "" {
		t.Error("command:wrap resolved empty from embedded floor")
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
	configDir, _ := initTestEnv(t, true)
	globalData, _ := os.ReadFile(filepath.Join(configDir, "vibe-palace", "config.toml"))
	vaultDir := ""
	for line := range strings.SplitSeq(string(globalData), "\n") {
		if after, ok := strings.CutPrefix(line, "vault_path = "); ok {
			vaultDir = strings.Trim(after, `"`)
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
	configDir, _ := initTestEnv(t, true)
	globalData, _ := os.ReadFile(filepath.Join(configDir, "vibe-palace", "config.toml"))
	vaultDir := ""
	for line := range strings.SplitSeq(string(globalData), "\n") {
		if after, ok := strings.CutPrefix(line, "vault_path = "); ok {
			vaultDir = strings.Trim(after, `"`)
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
	// initTestEnv redirects HOME so the default-vault fallback can't clobber
	// the developer's real $HOME if the fix ever regresses.
	configDir, fakeHome := initTestEnv(t, false) // no pre-existing global config

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
