// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

func TestConfigUpgradeDryRun(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	// Write a config missing git_enabled.
	vpDir := filepath.Join(configDir, "vibe-palace")
	os.MkdirAll(vpDir, 0o755)
	configPath := filepath.Join(vpDir, "config.toml")
	content := `vault_path = "/tmp"
http_port = 7423
log_level = "info"

[embedder]
model = "test"
max_sequence_length = 256
batch_size = 32

[search]
default_limit = 10
structural_boost_wing = 0.12
structural_boost_hall = 0.24
structural_boost_room = 0.34

[chunker]
max_chars = 800
overlap = 100
`
	os.WriteFile(configPath, []byte(content), 0o644)

	cmd := cmdConfigUpgrade()
	code := cmd.Run([]string{"--dry-run"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}

	// Config should NOT have been modified.
	after, _ := os.ReadFile(configPath)
	if string(after) != content {
		t.Error("dry-run should not modify the config file")
	}

	// No backup should have been created.
	if _, err := os.Stat(configPath + ".bak"); err == nil {
		t.Error("dry-run should not create a backup")
	}
}

func TestConfigUpgradeWritesChanges(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	vpDir := filepath.Join(configDir, "vibe-palace")
	os.MkdirAll(vpDir, 0o755)
	configPath := filepath.Join(vpDir, "config.toml")
	content := `vault_path = "/tmp"
http_port = 7423
`
	os.WriteFile(configPath, []byte(content), 0o644)

	cmd := cmdConfigUpgrade()
	code := cmd.Run([]string{})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}

	// Config should have been modified.
	after, _ := os.ReadFile(configPath)
	if string(after) == content {
		t.Error("upgrade should modify the config")
	}

	// Backup should exist.
	if _, err := os.Stat(configPath + ".bak"); err != nil {
		t.Error("upgrade should create a backup")
	}
}

func TestConfigUpgradeUpToDate(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	vpDir := filepath.Join(configDir, "vibe-palace")
	os.MkdirAll(vpDir, 0o755)
	configPath := filepath.Join(vpDir, "config.toml")
	content := `vault_path = "/tmp"
git_enabled = true
http_port = 7423
log_level = "info"

[meta]
version_major = 1
version_minor = 0
kind = "global"

[embedder]
model = "test"
max_sequence_length = 256
batch_size = 32

[search]
default_limit = 10
structural_boost_wing = 0.12
structural_boost_hall = 0.24
structural_boost_room = 0.34

[chunker]
max_chars = 800
overlap = 100
`
	os.WriteFile(configPath, []byte(content), 0o644)

	cmd := cmdConfigUpgrade()
	code := cmd.Run([]string{})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}

	// No backup when nothing changed.
	if _, err := os.Stat(configPath + ".bak"); err == nil {
		t.Error("should not create backup when config is up to date")
	}
}

func TestConfigUpgradeNoConfig(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	// With no legacy path left, `vp config upgrade` is a pure alias for
	// `vp config sync --tier global --yes`, which is permissive when the
	// global config is missing (Plan emits Skip and Apply exits OK).
	// This test pins that contract so the alias cannot silently start
	// rejecting callers whose config doesn't exist yet.
	cmd := cmdConfigUpgrade()
	code := cmd.Run([]string{})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d (missing config should Skip, not fail)", code, cli.ExitOK)
	}
}

func TestConfigUpgradeBadFlags(t *testing.T) {
	cmd := cmdConfigUpgrade()
	code := cmd.Run([]string{"--bogus"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

func TestConfigUpgradeIdempotent(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	vpDir := filepath.Join(configDir, "vibe-palace")
	os.MkdirAll(vpDir, 0o755)
	configPath := filepath.Join(vpDir, "config.toml")
	os.WriteFile(configPath, []byte(`vault_path = "/tmp"`+"\n"), 0o644)

	// First upgrade.
	cmd := cmdConfigUpgrade()
	cmd.Run([]string{})
	first, _ := os.ReadFile(configPath)

	// Remove backup so we can check if second run creates one.
	os.Remove(configPath + ".bak")

	// Second upgrade — should be no-op.
	cmd2 := cmdConfigUpgrade()
	code := cmd2.Run([]string{})
	if code != cli.ExitOK {
		t.Errorf("second upgrade exit code = %d", code)
	}

	second, _ := os.ReadFile(configPath)
	if string(first) != string(second) {
		t.Error("second upgrade should not modify config")
	}
	if _, err := os.Stat(configPath + ".bak"); err == nil {
		t.Error("second upgrade should not create backup (no changes)")
	}
}

func TestInitGlobalCreatesVaultWithGit(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	vaultDir := filepath.Join(t.TempDir(), "vault")

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	code := cmd.Run([]string{t.TempDir(), "--vault-path", vaultDir, "--name", "test"})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	// Vault should have .git directory.
	if _, err := os.Stat(filepath.Join(vaultDir, ".git")); err != nil {
		t.Error("vault should have .git directory when git_enabled=true")
	}

	// Vault should have .gitignore.
	data, err := os.ReadFile(filepath.Join(vaultDir, ".gitignore"))
	if err != nil {
		t.Error("vault should have .gitignore")
	} else if !strings.Contains(string(data), "palace/.local/") {
		t.Error(".gitignore should exclude palace/.local/")
	}
}

func TestInitGlobalNoGitSkipsRepo(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	vaultDir := filepath.Join(t.TempDir(), "vault")

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	code := cmd.Run([]string{t.TempDir(), "--vault-path", vaultDir, "--no-git", "--name", "test"})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	// Vault should NOT have .git directory.
	if _, err := os.Stat(filepath.Join(vaultDir, ".git")); err == nil {
		t.Error("vault should NOT have .git when --no-git is used")
	}
}

// --- Fix 1e: --cwd and --project flag tests ---

func TestConfigUpgradeCwd_AddsMissingMeta(t *testing.T) {
	// Start from a minimal hand-written cwd file — no [meta], no
	// vault_path comment. Upgrade should add [meta] as active keys.
	// Isolate XDG_CONFIG_HOME so the vault never resolves to the operator's
	// real vault (a `--cwd` sync otherwise scaffolds Projects/p there).
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	cwdFile := filepath.Join(dir, ".vibe-palace.toml")
	os.WriteFile(cwdFile, []byte(`[project]
name = "p"
`), 0o644)

	cmd := cmdConfigUpgrade()
	code := cmd.Run([]string{"--cwd", dir})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	data, err := os.ReadFile(cwdFile)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "[meta]") {
		t.Errorf("upgrade did not add [meta]: %s", content)
	}
	if !strings.Contains(content, "version_major = 1") {
		t.Errorf("upgrade did not add active version_major: %s", content)
	}

	// Backup must be present.
	if _, err := os.Stat(cwdFile + ".bak"); err != nil {
		t.Errorf("backup not created: %v", err)
	}
}

func TestConfigUpgradeCwd_UpToDate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	cwdFile := filepath.Join(dir, ".vibe-palace.toml")
	// Write a cwd file that already has all canonical keys (generated
	// from the template).
	content := storage.GenerateCwdProjectTOML("p", "", nil, "")
	os.WriteFile(cwdFile, []byte(content), 0o644)

	cmd := cmdConfigUpgrade()
	code := cmd.Run([]string{"--cwd", dir})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if _, err := os.Stat(cwdFile + ".bak"); err == nil {
		t.Error("no backup should be created when up to date")
	}
}

func TestConfigUpgradeCwd_DryRun(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	cwdFile := filepath.Join(dir, ".vibe-palace.toml")
	os.WriteFile(cwdFile, []byte("[project]\nname = \"p\"\n"), 0o644)

	cmd := cmdConfigUpgrade()
	code := cmd.Run([]string{"--cwd", dir, "--dry-run"})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	// File must not have been modified.
	data, _ := os.ReadFile(cwdFile)
	if strings.Contains(string(data), "[meta]") {
		t.Error("dry-run should not modify file")
	}
}

func TestConfigUpgradeProject_AddsMissingMeta(t *testing.T) {
	// Set up XDG + vault pointing at temp dirs.
	configDir := t.TempDir()
	vaultDir := filepath.Join(t.TempDir(), "vault")
	os.MkdirAll(filepath.Join(configDir, "vibe-palace"), 0o755)
	os.WriteFile(filepath.Join(configDir, "vibe-palace", "config.toml"),
		[]byte(`vault_path = "`+vaultDir+`"`+"\n"), 0o644)
	t.Setenv("XDG_CONFIG_HOME", configDir)

	// Sparse vault-project config: just a [palace.scoring] block a user
	// might have written via `vp tune rooms`.
	projectDir := filepath.Join(vaultDir, "Projects", "alpha")
	os.MkdirAll(projectDir, 0o755)
	projectCfg := filepath.Join(projectDir, "config.toml")
	os.WriteFile(projectCfg, []byte(`[palace.scoring]
min_score = 0.5
`), 0o644)

	cmd := cmdConfigUpgrade()
	code := cmd.Run([]string{"--project", "alpha"})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	data, _ := os.ReadFile(projectCfg)
	content := string(data)
	if !strings.Contains(content, "[meta]") {
		t.Errorf("upgrade did not add [meta]: %s", content)
	}
	// User's existing scoring override must be preserved.
	if !strings.Contains(content, "min_score = 0.5") {
		t.Errorf("upgrade clobbered user's scoring override: %s", content)
	}
}

func TestConfigUpgradeMutuallyExclusiveFlags(t *testing.T) {
	cmd := cmdConfigUpgrade()
	code := cmd.Run([]string{"--cwd", ".", "--project", "foo"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser", code)
	}
}

func TestConfigUpgradeProject_InvalidSlug(t *testing.T) {
	cmd := cmdConfigUpgrade()
	code := cmd.Run([]string{"--project", "Bad Slug"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser", code)
	}
}

// --- vp config sync --------------------------------------------------------

// seedFreshVault creates a minimal XDG + vault layout that config sync
// will treat as "in sync": global config points at vaultPath (which exists
// with a .gitignore), and there's no project config in projectDir.
func seedFreshVault(t *testing.T) (configDir, vaultPath, projectDir string) {
	t.Helper()
	configDir = t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	vpDir := filepath.Join(configDir, "vibe-palace")
	if err := os.MkdirAll(vpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	vaultPath = filepath.Join(configDir, "vault")
	if err := os.MkdirAll(vaultPath, 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed global config with canonical defaults so no drift surfaces,
	// then fill in the placeholder vault_path with our tempdir vault.
	defaultsText, err := storage.DefaultsTomlContent()
	if err != nil {
		t.Fatal(err)
	}
	seeded := strings.Replace(defaultsText,
		"vault_path = \"\"",
		"vault_path = \""+vaultPath+"\"", 1)
	if !strings.Contains(seeded, "vault_path = \""+vaultPath+"\"") {
		t.Fatalf("seedFreshVault: failed to substitute vault_path in defaults.toml")
	}
	if err := os.WriteFile(filepath.Join(vpDir, "config.toml"), []byte(seeded), 0o644); err != nil {
		t.Fatal(err)
	}
	// Vault .gitignore so Vault reconciler reports Unchanged.
	if err := storage.ReconcileVaultGitignore(vaultPath); err != nil {
		t.Fatal(err)
	}

	projectDir = t.TempDir()
	return configDir, vaultPath, projectDir
}

func TestConfigSync_InSync(t *testing.T) {
	_, _, projectDir := seedFreshVault(t)
	code := runConfigSync([]string{"--project-root", projectDir, "--yes"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want ExitOK", code)
	}
}

func TestConfigSync_Fresh_CreatesNothingWithoutSeeds(t *testing.T) {
	// With nothing set up at all and no seeds (sync mode), Plan reports
	// Skip actions and Apply is a no-op.
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	projectDir := t.TempDir()

	code := runConfigSync([]string{"--project-root", projectDir, "--dry-run"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want ExitOK", code)
	}
	// Nothing must have been written.
	if _, err := os.Stat(filepath.Join(configDir, "vibe-palace", "config.toml")); err == nil {
		t.Error("--dry-run on empty env should not create global config")
	}
}

func TestConfigSync_DriftInGlobalTier(t *testing.T) {
	configDir, _, projectDir := seedFreshVault(t)

	// Introduce drift by truncating the global config to one key.
	cfgPath := filepath.Join(configDir, "vibe-palace", "config.toml")
	if err := os.WriteFile(cfgPath,
		[]byte("vault_path = \""+filepath.Join(configDir, "vault")+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// --yes accepts and Apply should fill in missing keys.
	code := runConfigSync([]string{
		"--project-root", projectDir, "--tier", "global", "--yes",
	})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want ExitOK", code)
	}
	after, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(after), "[embedder]") {
		t.Errorf("expected upgrade to add [embedder] block, got:\n%s", after)
	}
	// Backup should have been created by applyUpgrade.
	if _, err := os.Stat(cfgPath + ".bak"); err != nil {
		t.Errorf("expected .bak backup: %v", err)
	}
}

func TestConfigSync_DryRunDoesNotModify(t *testing.T) {
	configDir, _, projectDir := seedFreshVault(t)
	cfgPath := filepath.Join(configDir, "vibe-palace", "config.toml")
	if err := os.WriteFile(cfgPath,
		[]byte("vault_path = \""+filepath.Join(configDir, "vault")+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(cfgPath)
	code := runConfigSync([]string{
		"--project-root", projectDir, "--tier", "global", "--dry-run",
	})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	after, _ := os.ReadFile(cfgPath)
	if string(after) != string(original) {
		t.Error("--dry-run must not modify the config file")
	}
	if _, err := os.Stat(cfgPath + ".bak"); err == nil {
		t.Error("--dry-run must not create a backup")
	}
}

func TestConfigSync_UnknownTierRejected(t *testing.T) {
	code := runConfigSync([]string{"--tier", "bogus"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser", code)
	}
}

func TestConfigSync_MutuallyExclusiveAddressing(t *testing.T) {
	code := runConfigSync([]string{"--cwd", ".", "--project", "foo"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser", code)
	}
}

func TestConfigSync_TierProjectScopeOnly(t *testing.T) {
	// With --tier project, only the project reconcilers run. Confirm that
	// a missing global config does not cause a failure — sync reports Skip.
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	projectDir := t.TempDir()
	// No vault is open here (vault == nil), so VaultProject Skips via the
	// vault==nil branch, not via any create-gate — see
	// TestConfigSync_VaultProjectSkipsInsideVault and
	// TestConfigSync_ExplicitAddressingSkipsVaultScaffold for the actual
	// create-gate.
	code := runConfigSync([]string{
		"--project-root", projectDir, "--tier", "project", "--dry-run",
	})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want ExitOK", code)
	}
}

func TestConfigSync_YesAcceptsWithoutStdin(t *testing.T) {
	configDir, _, projectDir := seedFreshVault(t)
	cfgPath := filepath.Join(configDir, "vibe-palace", "config.toml")
	if err := os.WriteFile(cfgPath,
		[]byte("vault_path = \""+filepath.Join(configDir, "vault")+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Close stdin to ensure --yes never reads from it.
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	_ = w.Close()
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin; _ = r.Close() })

	code := runConfigSync([]string{
		"--project-root", projectDir, "--tier", "global", "--yes",
	})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want ExitOK", code)
	}
}

// TestConfigSync_DevNullStdinRefuses is the regression test for
// commands-upgrade-treats-dev-null-stdin-as-a-terminal: `vp config sync` had
// no TTY gate at all — it prompted whenever there was an actionable change
// and --yes was not passed, regardless of whether stdin was a terminal, a
// pipe, or /dev/null. Deliberately uses a real os.Open(os.DevNull) file, not
// os.Pipe(), to also exercise the char-device-vs-real-terminal distinction
// the sibling `vp commands upgrade` fix relies on.
func TestConfigSync_DevNullStdinRefuses(t *testing.T) {
	configDir, _, projectDir := seedFreshVault(t)
	cfgPath := filepath.Join(configDir, "vibe-palace", "config.toml")
	if err := os.WriteFile(cfgPath,
		[]byte("vault_path = \""+filepath.Join(configDir, "vault")+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldStdin := os.Stdin
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	os.Stdin = devNull
	t.Cleanup(func() { os.Stdin = oldStdin; _ = devNull.Close() })

	var code int
	stderr := captureStderr(t, func() {
		code = runConfigSync([]string{
			"--project-root", projectDir, "--tier", "global",
		})
	})
	if code != cli.ExitUser {
		t.Fatalf("/dev/null stdin without --yes: exit=%d, want ExitUser\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "stdin is not a terminal and --yes was not set.") {
		t.Errorf("expected the non-terminal refusal, got:\n%s", stderr)
	}
	// Nothing must have been written — the refusal happens before any prompt.
	after, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(after), "[embedder]") {
		t.Errorf("refusal must not apply the drift fix:\n%s", after)
	}
}

// TestConfigSync_InteractivePathStillWorks proves the new upfront TTY gate
// only blocks a genuinely non-terminal stdin: with VP_ASSUME_TTY=1 (the same
// escape hatch `vp commands upgrade`'s integration tests already rely on,
// now shared via cli.IsTerminal), the interactive prompt loop still runs and
// an accept-all answer still applies the pending change. No prior test in
// this file exercises config sync's actual prompt loop — both existing
// runSyncWithStdin call sites pass --yes and never reach it.
func TestConfigSync_InteractivePathStillWorks(t *testing.T) {
	t.Setenv("VP_ASSUME_TTY", "1")
	configDir, _, projectDir := seedFreshVault(t)
	cfgPath := filepath.Join(configDir, "vibe-palace", "config.toml")
	if err := os.WriteFile(cfgPath,
		[]byte("vault_path = \""+filepath.Join(configDir, "vault")+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := runSyncWithStdin(t, "A\n", []string{
		"--project-root", projectDir, "--tier", "global",
	})
	if code != cli.ExitOK {
		t.Fatalf("interactive accept-all: exit=%d, want ExitOK\n%s", code, out)
	}
	after, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(after), "[embedder]") {
		t.Errorf("expected the accepted drift fix to add [embedder] block, got:\n%s", after)
	}
}

// --- Phase 3: TemplateTree override-only reconcile tests ---

// seedTemplateOverride writes data to the vault Templates/ target for
// embeddedRel and nothing else. Provenance is decided by the binary alone (the
// frozen shipped-version manifest), so no host-local state accompanies it: a
// copy of the embedded bytes is a mirror, anything else an override.
func seedTemplateOverride(t *testing.T, vaultPath, embeddedRel string, data []byte) {
	t.Helper()
	target := filepath.Join(vaultPath, "Templates", filepath.FromSlash(embeddedRel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}
}

// runSyncWithStdin pipes the given input to os.Stdin for the duration
// of a runConfigSync call. Captures stdout.
func runSyncWithStdin(t *testing.T, input string, args []string) (stdout string, code int) {
	t.Helper()
	oldStdin := os.Stdin
	rp, wp, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdin = rp
	t.Cleanup(func() { os.Stdin = oldStdin })
	go func() {
		_, _ = wp.Write([]byte(input))
		_ = wp.Close()
	}()
	stdout = captureStdout(t, func() {
		code = runConfigSync(args)
	})
	_ = rp.Close()
	return stdout, code
}

// syncPruneSetup inits a vault, then seeds a byte-identical mirror of
// embeddedRel (bytes == current embedded). Under Design B this redundant
// mirror is pruned on the next sync so the embedded floor serves it. It does
// NOT override templates.EmbeddedSHA — the prune must work against the real
// embedded corpus.
func syncPruneSetup(t *testing.T, embeddedRel string) (vaultPath, target, key string) {
	t.Helper()
	_, _ = initTestEnv(t, false)

	projDir := t.TempDir()
	markProjectDir(t, projDir)
	vaultPath = filepath.Join(t.TempDir(), "vault")

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	if code := cmd.Run([]string{projDir, "--name", "prune-tpl", "--vault-path", vaultPath, "--no-git"}); code != cli.ExitOK {
		t.Fatalf("init exit code = %d", code)
	}

	// Byte-identical mirror with an intentionally stale lock baseline.
	var embBytes []byte
	if rs, err := templates.WalkEmbedded(); err == nil {
		for _, res := range rs {
			if res.RelPath == embeddedRel {
				embBytes = res.Bytes
				break
			}
		}
	}
	if embBytes == nil {
		t.Fatalf("could not locate %q in embedded corpus", embeddedRel)
	}
	key = "Templates/" + embeddedRel
	target = filepath.Join(vaultPath, filepath.FromSlash(key))
	seedTemplateOverride(t, vaultPath, embeddedRel, embBytes)

	cwd, _ := os.Getwd()
	if err := os.Chdir(projDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	return vaultPath, target, key
}

// TestConfigSyncPrunesByteIdenticalMirror: a byte-identical mirror is pruned
// with no .bak (its bytes are the embedded copy), no templates.lock is
// written, and the resource resolves from the embedded floor.
func TestConfigSyncPrunesByteIdenticalMirror(t *testing.T) {
	vaultPath, target, _ := syncPruneSetup(t, "commands/wrap.md")

	// The prune auto-applies (never prompted); --yes just makes it silent.
	out, code := runSyncWithStdin(t, "", []string{
		"--project-root", filepath.Dir(target), "--tier", "vault", "--yes",
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if !strings.Contains(out, "pruned=1") {
		t.Errorf("summary missing pruned=1:\n%s", out)
	}
	if !strings.Contains(out, "prune Templates/commands/wrap.md (byte-identical, line endings aside, to the current embedded copy)") {
		t.Errorf("no current-copy prune row:\n%s", out)
	}

	// File removed, and no .bak written for it.
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("pruned file still present (err=%v)", err)
	}
	if _, err := os.Stat(target + ".bak"); !os.IsNotExist(err) {
		t.Errorf("prune wrote a .bak (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(vaultPath, ".vibe-palace", "templates.lock")); !os.IsNotExist(err) {
		t.Errorf("the sync wrote the retired templates.lock (err=%v)", err)
	}

	// Idempotent: a second sync prunes nothing.
	out2, code2 := runSyncWithStdin(t, "", []string{
		"--project-root", filepath.Dir(target), "--tier", "vault", "--yes",
	})
	if code2 != cli.ExitOK {
		t.Fatalf("second sync exit code = %d\n%s", code2, out2)
	}
	if !strings.Contains(out2, "pruned=0") {
		t.Errorf("second sync should report pruned=0:\n%s", out2)
	}
}

// phase4ConfigSyncSetup constructs an isolated environment with a
// global config pointing at a fresh vault, then returns (vaultDir, projDir).
// No Projects/ exist yet — callers create whatever slugs they want.
func phase4ConfigSyncSetup(t *testing.T) (vaultDir, projDir string) {
	t.Helper()
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	vpDir := filepath.Join(configDir, "vibe-palace")
	if err := os.MkdirAll(vpDir, 0o755); err != nil {
		t.Fatalf("mkdir vp: %v", err)
	}
	vaultDir = filepath.Join(t.TempDir(), "vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatalf("mkdir vault: %v", err)
	}
	if err := os.WriteFile(filepath.Join(vpDir, "config.toml"),
		[]byte("vault_path = \""+vaultDir+"\"\ngit_enabled = false\n"), 0o644); err != nil {
		t.Fatalf("write global: %v", err)
	}

	projDir = t.TempDir()
	markProjectDir(t, projDir)
	return vaultDir, projDir
}

// TestConfigSync_VaultProjectSkipsInsideVault reproduces the task's own
// reported shape across two runs: a first run that would otherwise create
// Projects/<slug>/config.toml (VaultProjectReconciler's own creation path),
// and a second run that would otherwise enumerate the now-existing directory
// and scaffold commands/skills READMEs into it (Phase 4's default/enumerate
// path). Uses --project-root equal to the vault root with no --cwd/--project,
// so Phase 4 takes the enumerate branch, not the explicit-addressing branch —
// see TestConfigSync_ExplicitAddressingSkipsVaultScaffold for that one.
func TestConfigSync_VaultProjectSkipsInsideVault(t *testing.T) {
	vaultDir, _ := phase4ConfigSyncSetup(t)
	slug := filepath.Base(vaultDir) // "vault" — basename fallback, same as the reported repro

	code := runConfigSync([]string{
		"--project-root", vaultDir, "--tier", "project", "--yes",
	})
	if code != cli.ExitOK {
		t.Fatalf("first run: exit code = %d, want ExitOK", code)
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "Projects", slug, "config.toml")); err == nil {
		t.Errorf("Projects/%s/config.toml should not have been created on the first run", slug)
	}

	code = runConfigSync([]string{
		"--project-root", vaultDir, "--tier", "project", "--yes",
	})
	if code != cli.ExitOK {
		t.Fatalf("second run: exit code = %d, want ExitOK", code)
	}
	for _, kind := range []string{"commands", "skills"} {
		if _, err := os.Stat(filepath.Join(vaultDir, "Projects", slug, kind, "README.md")); err == nil {
			t.Errorf("Projects/%s/%s/README.md should not have been created on the second run", slug, kind)
		}
	}
}

// TestConfigSync_ExplicitAddressingSkipsVaultScaffold proves the Phase-4
// explicit-addressing creation path (--project SLUG or --cwd DIR, as opposed
// to the default/enumerate path TestConfigSync_VaultProjectSkipsInsideVault
// exercises) is also gated. --project-root, --tier project and --project are
// all set to the vault itself, so projectSlug bypasses DetectProject and
// projectDir == vaultDir == vault.Root exactly, tripping the
// self-inclusive-equality behavior of RefuseDestinationInsideVault. One run
// is enough — the explicit branch does not depend on a prior run's leftover
// directory the way the enumerate branch does.
func TestConfigSync_ExplicitAddressingSkipsVaultScaffold(t *testing.T) {
	vaultDir, _ := phase4ConfigSyncSetup(t)
	slug := filepath.Base(vaultDir) // "vault"

	code := runConfigSync([]string{
		"--project-root", vaultDir, "--tier", "project", "--project", slug, "--yes",
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want ExitOK", code)
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "Projects", slug, "config.toml")); err == nil {
		t.Errorf("Projects/%s/config.toml should not have been created", slug)
	}
	for _, kind := range []string{"commands", "skills"} {
		if _, err := os.Stat(filepath.Join(vaultDir, "Projects", slug, kind, "README.md")); err == nil {
			t.Errorf("Projects/%s/%s/README.md should not have been created via explicit --project addressing", slug, kind)
		}
	}
}

// TestConfigSync_VaultProjectFailsClosedOnResolutionError constructs a
// genuine path-resolution error for RefuseDestinationInsideVault, distinct
// from the ordinary "resolves inside the vault" finding the tests above
// exercise: the global config's vault_path names a directory that does not
// exist, as if deleted or unmounted since the vault was last opened.
// storage.OpenVaultFromCwd / ResolveVaultPath perform no existence check on
// vault_path, so vault != nil with vault.Root pointing at a missing path, and
// filepath.EvalSymlinks(vault.Root) inside RefuseDestinationInsideVault
// genuinely fails with an ordinary resolution error — not
// ErrDestinationInsideVault. This proves the gate fails CLOSED (skips
// project-directory creation) rather than OPEN on that error, matching
// guardExportDestination's (cmd/vp/export_guard.go) established convention
// for this same predicate. A fail-open gate would fall through to
// VaultProjectReconciler's normal Create path, whose Apply calls
// os.MkdirAll for the missing vault root's Projects/<slug>/tasks
// directories — silently resurrecting the deleted vault root. This test
// asserts that never happens.
func TestConfigSync_VaultProjectFailsClosedOnResolutionError(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	vpDir := filepath.Join(configDir, "vibe-palace")
	if err := os.MkdirAll(vpDir, 0o755); err != nil {
		t.Fatalf("mkdir vp: %v", err)
	}
	// Deliberately never created — simulates a vault_path whose target has
	// been deleted or unmounted since the vault was last opened.
	missingVault := filepath.Join(t.TempDir(), "gone", "vault")
	if err := os.WriteFile(filepath.Join(vpDir, "config.toml"),
		[]byte("vault_path = \""+missingVault+"\"\ngit_enabled = false\n"), 0o644); err != nil {
		t.Fatalf("write global: %v", err)
	}

	projDir := t.TempDir()
	markProjectDir(t, projDir)

	code := runConfigSync([]string{
		"--project-root", projDir, "--tier", "project", "--yes",
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want ExitOK", code)
	}
	if _, err := os.Stat(missingVault); err == nil {
		t.Errorf("vault root %s should not have been recreated by a fail-open gate", missingVault)
	}
}

// TestConfigSyncDefaultScopeScaffoldsAllProjects verifies that the
// default scope enumerates every slug under <vault>/Projects/ and
// scaffolds commands/ + skills/ + README stubs for each one. Also
// asserts alphabetical ordering in the Plan output.
func TestConfigSyncDefaultScopeScaffoldsAllProjects(t *testing.T) {
	vaultDir, projDir := phase4ConfigSyncSetup(t)
	// Pre-create two empty project dirs.
	for _, slug := range []string{"beta", "alpha"} {
		if err := os.MkdirAll(filepath.Join(vaultDir, "Projects", slug), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", slug, err)
		}
	}

	stdout := captureStdout(t, func() {
		code := runConfigSync([]string{"--project-root", projDir, "--yes"})
		if code != cli.ExitOK {
			t.Errorf("exit code = %d", code)
		}
	})

	// Both projects must have their scaffolded dirs + READMEs.
	for _, slug := range []string{"alpha", "beta"} {
		for _, kind := range []string{"commands", "skills"} {
			readme := filepath.Join(vaultDir, "Projects", slug, kind, "README.md")
			body, err := os.ReadFile(readme)
			if err != nil {
				t.Errorf("missing README %s: %v", readme, err)
				continue
			}
			if string(body) != templates.RenderReadmeStub(kind) {
				t.Errorf("%s/%s README body mismatch", slug, kind)
			}
		}
	}
	// Alphabetical ordering: alpha's reconciler name must appear before beta's.
	iAlpha := strings.Index(stdout, "TemplateTree:Projects/alpha")
	iBeta := strings.Index(stdout, "TemplateTree:Projects/beta")
	if iAlpha < 0 || iBeta < 0 {
		t.Fatalf("expected both scaffold reconcilers in plan output:\n%s", stdout)
	}
	if iAlpha > iBeta {
		t.Errorf("alpha should precede beta in plan output:\n%s", stdout)
	}
}

// TestConfigSyncProjectFlagRestrictsScope verifies --project SLUG
// limits scaffolding to just that slug; sibling projects remain
// untouched.
func TestConfigSyncProjectFlagRestrictsScope(t *testing.T) {
	vaultDir, projDir := phase4ConfigSyncSetup(t)
	for _, slug := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(vaultDir, "Projects", slug), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", slug, err)
		}
	}

	code := runConfigSync([]string{
		"--project-root", projDir, "--yes",
		"--tier", "project", "--project", "beta",
	})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	// beta got scaffolded.
	if _, err := os.Stat(filepath.Join(vaultDir, "Projects", "beta", "commands", "README.md")); err != nil {
		t.Errorf("beta commands README missing: %v", err)
	}
	// alpha did NOT.
	if _, err := os.Stat(filepath.Join(vaultDir, "Projects", "alpha", "commands")); err == nil {
		t.Errorf("alpha commands/ should not have been scaffolded")
	}
}

// TestConfigSyncScaffoldIdempotent verifies that a second sync after a
// full scaffold emits only Unchanged for the project scaffolders — no
// duplicate Create rows.
func TestConfigSyncScaffoldIdempotent(t *testing.T) {
	vaultDir, projDir := phase4ConfigSyncSetup(t)
	if err := os.MkdirAll(filepath.Join(vaultDir, "Projects", "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}

	if code := runConfigSync([]string{"--project-root", projDir, "--yes"}); code != cli.ExitOK {
		t.Fatalf("first sync exit = %d", code)
	}
	// Second run: no Create actions for the scaffold reconciler.
	stdout := captureStdout(t, func() {
		if code := runConfigSync([]string{"--project-root", projDir, "--yes", "--dry-run"}); code != cli.ExitOK {
			t.Fatalf("second sync exit = %d", code)
		}
	})
	// Look for any [Create] line whose reconciler name is the alpha scaffold.
	for line := range strings.SplitSeq(stdout, "\n") {
		if strings.Contains(line, "TemplateTree:Projects/alpha") && strings.Contains(line, "[Create]") {
			t.Errorf("unexpected Create on second sync:\n%s", line)
		}
	}
}

// TestConfigSyncSkipsDotAndUnderscoreProjects verifies that directory
// entries under Projects/ whose names begin with '.' or '_' are
// skipped by the enumerator.
func TestConfigSyncSkipsDotAndUnderscoreProjects(t *testing.T) {
	vaultDir, projDir := phase4ConfigSyncSetup(t)
	for _, name := range []string{".hidden", "_wip", "real"} {
		if err := os.MkdirAll(filepath.Join(vaultDir, "Projects", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	code := runConfigSync([]string{"--project-root", projDir, "--yes"})
	if code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}

	// real/ got scaffolded.
	if _, err := os.Stat(filepath.Join(vaultDir, "Projects", "real", "commands", "README.md")); err != nil {
		t.Errorf("real/ should be scaffolded: %v", err)
	}
	// .hidden and _wip did not.
	for _, skipped := range []string{".hidden", "_wip"} {
		path := filepath.Join(vaultDir, "Projects", skipped, "commands")
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s should have been skipped", skipped)
		}
	}
}

// TestEnumerateVaultProjectSlugsSkipRules is a focused unit test for
// the enumerator helper — exercises the skip predicate without going
// through the full runConfigSync path.
func TestEnumerateVaultProjectSlugsSkipRules(t *testing.T) {
	vaultDir := t.TempDir()
	for _, name := range []string{"alpha", "beta", ".hidden", "_wip"} {
		if err := os.MkdirAll(filepath.Join(vaultDir, "Projects", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A regular file under Projects/ must also be skipped.
	if err := os.WriteFile(filepath.Join(vaultDir, "Projects", "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := enumerateVaultProjectSlugs(vaultDir)
	want := []string{"alpha", "beta"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("enumerate = %v, want %v", got, want)
	}

	// Missing Projects/ dir → nil, not error.
	if out := enumerateVaultProjectSlugs(t.TempDir()); out != nil {
		t.Errorf("empty vault: want nil, got %v", out)
	}
}

// TestUpgradeAliasParity pins the `vp config upgrade` → `vp config sync`
// alias translation contract established when HEALTH.md item 10 retired
// the TOML-parsing legacy path. A byte-identical run of `vp config
// upgrade` (with each addressing variant) and the equivalent
// `vp config sync --tier X --yes` invocation must produce the same
// config.toml and .bak bytes on the same fixture. Guards against
// accidental drift in aliasUpgradeToSync's flag translation.
//
// The pre-deletion version of this test compared the legacy
// TOML-parsing path (--legacy) against the reconciler-based sync path
// on the same fixture and asserted byte-equality across all three
// target resolutions (global, cwd, project). That comparison passed
// cleanly on first run — no divergence — which is what authorized the
// legacy deletion.
func TestUpgradeAliasParity(t *testing.T) {
	// Each subtest runs alias-then-reset-then-sync inside ONE fixture so
	// the input bytes (and any embedded tempdir paths) are identical
	// across both invocations. Any byte-level difference therefore
	// reflects a real alias-translation divergence, not a fixture
	// artifact.
	t.Run("global", func(t *testing.T) {
		fx := seedLegacyFixture(t, "global", "")
		orig := mustRead(t, fx.target)

		aliasOut, aliasBak := runAliasAndCapture(t, fx, []string{})
		resetFixtureTarget(t, fx, orig)
		syncOut, syncBak := runSyncAndCapture(t, fx, []string{"--tier", "global", "--yes"})

		assertBytesEqual(t, "config.toml", aliasOut, syncOut)
		assertBytesEqual(t, "config.toml.bak", aliasBak, syncBak)
	})

	t.Run("cwd", func(t *testing.T) {
		fx := seedLegacyFixture(t, "cwd", "")
		orig := mustRead(t, fx.target)

		aliasOut, aliasBak := runAliasAndCapture(t, fx, []string{"--cwd", fx.projectDir})
		resetFixtureTarget(t, fx, orig)
		syncOut, syncBak := runSyncAndCapture(t, fx,
			[]string{"--tier", "project", "--cwd", fx.projectDir, "--project-root", fx.projectDir, "--yes"})

		assertBytesEqual(t, ".vibe-palace.toml", aliasOut, syncOut)
		assertBytesEqual(t, ".vibe-palace.toml.bak", aliasBak, syncBak)
	})

	t.Run("project", func(t *testing.T) {
		fx := seedLegacyFixture(t, "project", "alpha")
		orig := mustRead(t, fx.target)

		aliasOut, aliasBak := runAliasAndCapture(t, fx, []string{"--project", "alpha"})
		resetFixtureTarget(t, fx, orig)
		syncOut, syncBak := runSyncAndCapture(t, fx,
			[]string{"--tier", "project", "--project", "alpha", "--project-root", fx.projectDir, "--yes"})

		assertBytesEqual(t, "<vault>/Projects/alpha/config.toml", aliasOut, syncOut)
		assertBytesEqual(t, "<vault>/Projects/alpha/config.toml.bak", aliasBak, syncBak)
	})
}

// resetFixtureTarget restores the legacy fixture's target config file to
// its pre-upgrade bytes and removes any .bak left by the first run, so
// the second path operates on identical input.
func resetFixtureTarget(t *testing.T, fx legacyFixture, orig []byte) {
	t.Helper()
	if err := os.WriteFile(fx.target, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(fx.target + ".bak")
}

type legacyFixture struct {
	configDir  string // XDG_CONFIG_HOME
	vaultDir   string
	projectDir string
	target     string // absolute path to the config file under upgrade
}

// seedLegacyFixture materializes the minimum file layout each of the
// three legacy upgrade targets needs, and returns the absolute path to
// the config file the upgrade will modify.
func seedLegacyFixture(t *testing.T, kind, projectSlug string) legacyFixture {
	t.Helper()
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	vaultDir := filepath.Join(configDir, "vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Global config always exists so sync's vault-tier + project-tier
	// reconcilers can resolve the vault. Legacy global points through
	// this file too.
	vpDir := filepath.Join(configDir, "vibe-palace")
	if err := os.MkdirAll(vpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	globalCfg := filepath.Join(vpDir, "config.toml")

	projectDir := t.TempDir()
	fx := legacyFixture{
		configDir: configDir, vaultDir: vaultDir, projectDir: projectDir,
	}

	sparseGlobal := "vault_path = \"" + vaultDir + "\"\n" +
		"http_port = 7423\n"

	switch kind {
	case "global":
		if err := os.WriteFile(globalCfg, []byte(sparseGlobal), 0o644); err != nil {
			t.Fatal(err)
		}
		fx.target = globalCfg

	case "cwd":
		// Global must still exist and point at the vault, otherwise
		// sync's OpenVaultFromCwd fails and the two paths diverge for
		// unrelated reasons. Use canonical defaults so the global
		// tier stays Unchanged during a --tier project run.
		defaultsText, err := storage.DefaultsTomlContent()
		if err != nil {
			t.Fatal(err)
		}
		seeded := strings.Replace(defaultsText,
			"vault_path = \"\"", "vault_path = \""+vaultDir+"\"", 1)
		if err := os.WriteFile(globalCfg, []byte(seeded), 0o644); err != nil {
			t.Fatal(err)
		}
		cwdFile := filepath.Join(projectDir, ".vibe-palace.toml")
		if err := os.WriteFile(cwdFile, []byte("[project]\nname = \"p\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fx.target = cwdFile

	case "project":
		defaultsText, err := storage.DefaultsTomlContent()
		if err != nil {
			t.Fatal(err)
		}
		seeded := strings.Replace(defaultsText,
			"vault_path = \"\"", "vault_path = \""+vaultDir+"\"", 1)
		if err := os.WriteFile(globalCfg, []byte(seeded), 0o644); err != nil {
			t.Fatal(err)
		}
		projDir := filepath.Join(vaultDir, "Projects", projectSlug)
		if err := os.MkdirAll(projDir, 0o755); err != nil {
			t.Fatal(err)
		}
		projCfg := filepath.Join(projDir, "config.toml")
		if err := os.WriteFile(projCfg, []byte("[palace.scoring]\nmin_score = 0.5\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fx.target = projCfg

	default:
		t.Fatalf("unknown fixture kind %q", kind)
	}
	return fx
}

// runAliasAndCapture invokes the `vp config upgrade` alias and returns
// the resulting target file bytes and backup bytes.
func runAliasAndCapture(t *testing.T, fx legacyFixture, args []string) (cfgBytes, bakBytes []byte) {
	t.Helper()
	// Chdir to projectDir so any implicit os.Getwd() inside the sync
	// path (e.g. OpenVaultFromCwd) resolves deterministically.
	restoreCwd := chdir(t, fx.projectDir)
	defer restoreCwd()

	cmd := cmdConfigUpgrade()
	if code := cmd.Run(args); code != cli.ExitOK {
		t.Fatalf("upgrade alias exit = %d", code)
	}
	cfgBytes = mustRead(t, fx.target)
	if data, err := os.ReadFile(fx.target + ".bak"); err == nil {
		bakBytes = data
	}
	return cfgBytes, bakBytes
}

// runSyncAndCapture invokes the reconciler-based `runConfigSync` and
// returns the resulting target file bytes and backup bytes.
func runSyncAndCapture(t *testing.T, fx legacyFixture, args []string) (cfgBytes, bakBytes []byte) {
	t.Helper()
	restoreCwd := chdir(t, fx.projectDir)
	defer restoreCwd()

	if code := runConfigSync(args); code != cli.ExitOK {
		t.Fatalf("sync exit = %d", code)
	}
	cfgBytes = mustRead(t, fx.target)
	if data, err := os.ReadFile(fx.target + ".bak"); err == nil {
		bakBytes = data
	}
	return cfgBytes, bakBytes
}

func chdir(t *testing.T, dir string) func() {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chdir(prev) }
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func assertBytesEqual(t *testing.T, label string, want, got []byte) {
	t.Helper()
	if string(want) == string(got) {
		return
	}
	t.Errorf("%s: bytes differ between legacy and sync paths\n--- legacy ---\n%s\n--- sync ---\n%s",
		label, want, got)
}
