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
	// No seed: both CwdProject and VaultProject return Skip.
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

// --- Phase 3: TemplateTree drift prompt tests ---

// syncPromptSetup runs vp init, then edits one materialized template
// (picked by embeddedRel) so vaultSHA ≠ lockSHA. It also installs an
// EmbeddedSHA override so the reconciler's embedded SHA differs from
// the lock entry (simulating a binary bump). Returns vault path and
// the absolute target path of the edited template. The override is
// cleaned up by t.Cleanup.
func syncPromptSetup(t *testing.T, embeddedRel string) (vaultPath, target, userEdit string) {
	t.Helper()
	configDir := initTestEnv(t, false)
	_ = configDir

	projDir := t.TempDir()
	markProjectDir(t, projDir)
	vaultPath = filepath.Join(t.TempDir(), "vault")

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	if code := cmd.Run([]string{projDir, "--name", "sync-tpl", "--vault-path", vaultPath, "--no-git"}); code != cli.ExitOK {
		t.Fatalf("init exit code = %d", code)
	}

	target = filepath.Join(vaultPath, "Templates", filepath.FromSlash(embeddedRel))
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected materialized %s: %v", target, err)
	}
	// User edit: distinct bytes so vaultSHA ≠ lockSHA.
	userEdit = "USER EDIT: " + embeddedRel + "\n"
	if err := os.WriteFile(target, []byte(userEdit), 0o644); err != nil {
		t.Fatalf("write user edit: %v", err)
	}

	// Embedded SHA override: flip one character of the real SHA so
	// it differs from both the lock entry and the real embedded bytes
	// but still passes any length checks.
	orig := templates.EmbeddedSHA
	templates.EmbeddedSHA = func(rel string) (string, bool) {
		if rel == embeddedRel {
			return strings.Repeat("a", 64), true
		}
		return orig(rel)
	}
	t.Cleanup(func() { templates.EmbeddedSHA = orig })

	// chdir into projDir so DetectProject works for CwdProject tier.
	cwd, _ := os.Getwd()
	if err := os.Chdir(projDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	return vaultPath, target, userEdit
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

// syncRelockSetup inits a vault, then corrupts a single lock entry's
// EmbeddedSHA to a stale value while leaving the materialized file bytes
// untouched — the false-positive TemplateTree drift state. Unlike
// syncPromptSetup it does NOT override templates.EmbeddedSHA: the heal
// must work against the real embedded corpus.
func syncRelockSetup(t *testing.T, embeddedRel string) (vaultPath, target, key, freshSHA string) {
	t.Helper()
	_ = initTestEnv(t, false)

	projDir := t.TempDir()
	markProjectDir(t, projDir)
	vaultPath = filepath.Join(t.TempDir(), "vault")

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	if code := cmd.Run([]string{projDir, "--name", "relock-tpl", "--vault-path", vaultPath, "--no-git"}); code != cli.ExitOK {
		t.Fatalf("init exit code = %d", code)
	}

	target = filepath.Join(vaultPath, "Templates", filepath.FromSlash(embeddedRel))
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected materialized %s: %v", target, err)
	}

	key = "Templates/" + embeddedRel
	lock, err := templates.ReadLock(vaultPath)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	entry, ok := lock.Entries[key]
	if !ok {
		t.Fatalf("lock missing entry for %q", key)
	}
	freshSHA = entry.EmbeddedSHA
	entry.EmbeddedSHA = strings.Repeat("0", 64)
	lock.Entries[key] = entry
	if err := templates.WriteLock(vaultPath, lock); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}

	cwd, _ := os.Getwd()
	if err := os.Chdir(projDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	return vaultPath, target, key, freshSHA
}

func TestConfigSyncRelocksStaleLock(t *testing.T) {
	vaultPath, target, key, freshSHA := syncRelockSetup(t, "commands/wrap.md")

	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}

	// --yes is non-interactive; the relock auto-applies regardless since it
	// is never prompted. No stdin needed.
	out, code := runSyncWithStdin(t, "", []string{
		"--project-root", filepath.Dir(target), "--tier", "vault", "--yes",
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if !strings.Contains(out, "relocked=1") {
		t.Errorf("summary missing relocked=1:\n%s", out)
	}

	// File bytes unchanged; no .bak emitted.
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("re-read target: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("relock mutated file bytes")
	}
	if _, err := os.Stat(target + ".bak"); err == nil {
		t.Error("relock unexpectedly created a .bak sidecar")
	}

	// Lock refreshed to the real embedded SHA.
	healed, err := templates.ReadLock(vaultPath)
	if err != nil {
		t.Fatalf("ReadLock (healed): %v", err)
	}
	if healed.Entries[key].EmbeddedSHA != freshSHA {
		t.Errorf("lock SHA = %q, want %q", healed.Entries[key].EmbeddedSHA, freshSHA)
	}

	// Idempotent: a second sync relocks nothing.
	out2, code2 := runSyncWithStdin(t, "", []string{
		"--project-root", filepath.Dir(target), "--tier", "vault", "--yes",
	})
	if code2 != cli.ExitOK {
		t.Fatalf("second sync exit code = %d\n%s", code2, out2)
	}
	if !strings.Contains(out2, "relocked=0") {
		t.Errorf("second sync should report relocked=0:\n%s", out2)
	}
}

func TestConfigSyncTemplateDriftSkip(t *testing.T) {
	vaultPath, target, userEdit := syncPromptSetup(t, "commands/wrap.md")

	_, code := runSyncWithStdin(t, "s\n", []string{
		"--project-root", filepath.Dir(target), "--tier", "vault",
	})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != userEdit {
		t.Errorf("skip: target bytes changed — got %q want %q", got, userEdit)
	}
	if _, err := os.Stat(target + ".bak"); err == nil {
		t.Error("skip: unexpected .bak present")
	}
	if _, err := os.Stat(target + ".new"); err == nil {
		t.Error("skip: unexpected .new present")
	}
	_ = vaultPath
}

func TestConfigSyncTemplateDriftOverwrite(t *testing.T) {
	_, target, userEdit := syncPromptSetup(t, "commands/wrap.md")

	// Capture embedded bytes for assertion.
	var embBytes []byte
	if rs, err := templates.WalkEmbedded(); err == nil {
		for _, res := range rs {
			if res.RelPath == "commands/wrap.md" {
				embBytes = res.Bytes
				break
			}
		}
	}
	if embBytes == nil {
		t.Fatal("could not locate commands/wrap.md in embedded corpus")
	}

	_, code := runSyncWithStdin(t, "o\n", []string{
		"--project-root", filepath.Dir(target), "--tier", "vault",
	})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != string(embBytes) {
		t.Error("overwrite: target bytes != embedded bytes")
	}
	bak, err := os.ReadFile(target + ".bak")
	if err != nil {
		t.Fatalf(".bak missing: %v", err)
	}
	if string(bak) != userEdit {
		t.Errorf(".bak should contain user edit; got %q want %q", bak, userEdit)
	}
	if _, err := os.Stat(target + ".new"); err == nil {
		t.Error("overwrite: unexpected .new present")
	}
}

func TestConfigSyncTemplateDriftNew(t *testing.T) {
	_, target, userEdit := syncPromptSetup(t, "commands/wrap.md")

	var embBytes []byte
	if rs, err := templates.WalkEmbedded(); err == nil {
		for _, res := range rs {
			if res.RelPath == "commands/wrap.md" {
				embBytes = res.Bytes
				break
			}
		}
	}

	_, code := runSyncWithStdin(t, "n\n", []string{
		"--project-root", filepath.Dir(target), "--tier", "vault",
	})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != userEdit {
		t.Errorf("new: target bytes changed — got %q want %q", got, userEdit)
	}
	newPath := target + ".new"
	newBytes, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf(".new missing: %v", err)
	}
	if string(newBytes) != string(embBytes) {
		t.Error(".new: should contain embedded bytes")
	}
}

// TestConfigSyncTemplateDriftNewCollision verifies that when a stale
// <target>.new already exists prior to the 'n' prompt answer, the old
// .new is rotated to <target>.new.bak before the fresh sidecar is
// written. Phase 5 explicitly requires preserving the previous sidecar
// body — the ".new is a transient review artifact" contract keeps a
// one-level undo for that artifact too.
func TestConfigSyncTemplateDriftNewCollision(t *testing.T) {
	_, target, _ := syncPromptSetup(t, "commands/wrap.md")

	// Plant a pre-existing .new with distinct bytes.
	newPath := target + ".new"
	priorNew := []byte("STALE .new CONTENT — must be rotated\n")
	if err := os.WriteFile(newPath, priorNew, 0o644); err != nil {
		t.Fatalf("plant prior .new: %v", err)
	}

	var embBytes []byte
	if rs, err := templates.WalkEmbedded(); err == nil {
		for _, res := range rs {
			if res.RelPath == "commands/wrap.md" {
				embBytes = res.Bytes
				break
			}
		}
	}
	if embBytes == nil {
		t.Fatal("could not locate commands/wrap.md in embedded corpus")
	}

	_, code := runSyncWithStdin(t, "n\n", []string{
		"--project-root", filepath.Dir(target), "--tier", "vault",
	})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	got, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("read %s: %v", newPath, err)
	}
	if string(got) != string(embBytes) {
		t.Error(".new: expected fresh embedded bytes")
	}
	bak, err := os.ReadFile(newPath + ".bak")
	if err != nil {
		t.Fatalf("read %s.bak: %v", newPath, err)
	}
	if string(bak) != string(priorNew) {
		t.Errorf(".new.bak: expected prior .new bytes\n got: %q\nwant: %q", bak, priorNew)
	}
}

// TestConfigSyncTemplateBatchUppercase verifies that S/O/N letters set
// a batch mode honored for remaining Prompt actions. We drift two
// resources and feed a single uppercase letter.
func TestConfigSyncTemplateBatchUppercase(t *testing.T) {
	vaultPath, target1, _ := syncPromptSetup(t, "commands/wrap.md")

	// Drift a second file by editing it and extending the SHA override
	// to cover it as well.
	target2 := filepath.Join(vaultPath, "Templates", "commands", "restart.md")
	if _, err := os.Stat(target2); err != nil {
		t.Skipf("second resource not materialized: %v", err)
	}
	if err := os.WriteFile(target2, []byte("USER EDIT 2\n"), 0o644); err != nil {
		t.Fatalf("edit target2: %v", err)
	}
	orig := templates.EmbeddedSHA
	templates.EmbeddedSHA = func(rel string) (string, bool) {
		switch rel {
		case "commands/wrap.md", "commands/restart.md":
			return strings.Repeat("b", 64), true
		}
		return orig(rel)
	}
	t.Cleanup(func() { templates.EmbeddedSHA = orig })

	// Single uppercase 'S' answer — both Prompt actions should skip.
	_, code := runSyncWithStdin(t, "S\n", []string{
		"--project-root", filepath.Dir(target1), "--tier", "vault",
	})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	// Both files must retain user-edit bytes.
	b1, _ := os.ReadFile(target1)
	b2, _ := os.ReadFile(target2)
	if !strings.HasPrefix(string(b1), "USER EDIT") {
		t.Errorf("target1 not preserved: %s", b1)
	}
	if !strings.HasPrefix(string(b2), "USER EDIT") {
		t.Errorf("target2 not preserved: %s", b2)
	}
	if _, err := os.Stat(target1 + ".bak"); err == nil {
		t.Error("target1 .bak unexpectedly present after S")
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
