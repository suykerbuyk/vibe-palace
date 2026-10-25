// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// These tests pin `vp init --vault-path X` on an EXISTING-config host: it writes
// a durable [project_vaults].<slug> = X binding through storage.BindProjectVaults
// (never a tracked marker vault_path, ADR-012 rule 2), or refuses and points at
// `vp config bind`. The fresh-host path is unchanged.

// existingHostEnv sets the hermetic sandbox with a pre-existing global config
// whose default vault is a real git repo at a path OTHER than any --vault-path
// a test will pass. Returns ($HOME, config path, default vault).
func existingHostEnv(t *testing.T) (home, cfg, def string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	configDir := t.TempDir()
	home = t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("APPDATA", configDir)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for k, v := range testCommitterIdentity {
		t.Setenv(k, v)
	}
	forbidVaultEmbedder(t)
	def = t.TempDir()
	makeGitVaultWithOrigin(t, def)
	vpDir := filepath.Join(configDir, "vibe-palace")
	if err := os.MkdirAll(vpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg = filepath.Join(vpDir, "config.toml")
	writeTestFile(t, cfg, "vault_path = \""+def+"\"\ngit_enabled = true\n")
	return home, cfg, def
}

func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

// makeBareGitRepo creates a standalone git repo at dir with one commit but NO
// surface stamp — i.e. a git vault that was never `vp vault init`ed. It is the
// tier-2 entry (VaultGitOK) that the unstamped pre-check must refuse.
func makeBareGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.email", "test@test.com")
	gitRun(t, dir, "config", "user.name", "Test")
	writeTestFile(t, filepath.Join(dir, "seed.txt"), "seed\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "seed")
}

func samePath(a, b string) bool {
	ea, err := filepath.EvalSymlinks(a)
	if err != nil {
		ea = filepath.Clean(a)
	}
	eb, err := filepath.EvalSymlinks(b)
	if err != nil {
		eb = filepath.Clean(b)
	}
	return ea == eb
}

// TestInitVaultPath_WritesProjectVaultsBindingOnExistingHost runs the FIRST-INIT
// ordering (N2): projDir has a project signal but NO marker; onboarding writes
// the identity-only marker, then the bind phase keys on it. X is absent, so
// initGlobal scaffolds it as a git vault first.
func TestInitVaultPath_WritesProjectVaultsBindingOnExistingHost(t *testing.T) {
	home, cfg, def := existingHostEnv(t)
	x := filepath.Join(t.TempDir(), "quantum") // absent: init scaffolds it
	projDir := filepath.Join(home, "code", "alpha")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	markProjectDir(t, projDir) // go.mod signal, no marker yet

	code, out := runInit(t, projDir, "--name", "alpha", "--vault-path", x)
	if code != cli.ExitOK {
		t.Fatalf("exit = %d\n%s", code, out)
	}

	body, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "alpha = \""+x+"\"") {
		t.Errorf("[project_vaults].alpha not written to config:\n%s", body)
	}
	// The resolver now routes the checkout to X through tier 2.
	r, err := storage.ResolveVaultBinding(projDir)
	if err != nil || !samePath(r.Path, x) {
		t.Errorf("ResolveVaultBinding(%s) = %q, %v; want %q", projDir, r.Path, err, x)
	}
	if !strings.HasPrefix(r.Source, "binding:") {
		t.Errorf("source = %q, want a binding: source", r.Source)
	}
	// Rule 2: the tracked marker is identity only — no ACTIVE vault_path (the
	// template keeps a commented "# vault_path = ..." example, which is fine).
	marker, err := os.ReadFile(filepath.Join(projDir, ".vibe-palace.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(marker), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "vault_path") {
			t.Errorf("marker must be identity-only (no active vault_path) on an existing host:\n%s", marker)
			break
		}
	}
	_ = def
}

// TestInitVaultPath_RefusesWhenMarkerVaultPathDiffers targets the PRE-CHECK, not
// the writer: a committed marker vault_path that disagrees with X is refused
// before BindProjectVaults, which does not catch it.
func TestInitVaultPath_RefusesWhenMarkerVaultPathDiffers(t *testing.T) {
	home, _, _ := existingHostEnv(t)
	x := t.TempDir()
	makeGitVaultWithOrigin(t, x) // exists + stamped, so only the marker conflict can refuse
	z := filepath.Join(t.TempDir(), "other")
	projDir := filepath.Join(home, "code", "alpha")
	writeTestFile(t, filepath.Join(projDir, ".vibe-palace.toml"),
		"vault_path = \""+z+"\"\n\n[project]\nname = \"alpha\"\n")

	code, out := runInit(t, projDir, "--name", "alpha", "--vault-path", x)
	if code == cli.ExitOK {
		t.Fatalf("want a non-zero exit for a conflicting marker vault_path\n%s", out)
	}
	if f := flat(out); !strings.Contains(f, "differs from") || !strings.Contains(f, "vp config bind") {
		t.Errorf("refusal should name the conflict and point at `vp config bind`:\n%s", out)
	}
}

// TestInitVaultPath_RefusesUnstampedExistingDir: a standalone git repo that was
// never `vp vault init`ed is a tier-2 target (VaultGitOK) but carries no stamp,
// so the bind pre-check refuses it, pointing at `vp vault init`.
func TestInitVaultPath_RefusesUnstampedExistingDir(t *testing.T) {
	home, _, _ := existingHostEnv(t)
	x := t.TempDir()
	makeBareGitRepo(t, x) // own git repo, but no .vibe-palace/vault.toml stamp
	projDir := filepath.Join(home, "code", "alpha")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	markProjectDir(t, projDir)

	code, out := runInit(t, projDir, "--name", "alpha", "--vault-path", x)
	if code == cli.ExitOK {
		t.Fatalf("want a non-zero exit for an unstamped git-vault target\n%s", out)
	}
	if !strings.Contains(flat(out), "vp vault init") {
		t.Errorf("refusal should point at `vp vault init`:\n%s", out)
	}
}

// TestInitVaultPath_NoGitTargetFallsThroughToTier1: Correction 2 — a --no-git
// target is NOT a tier-2 bind; it keeps the existing tier-1 cwd-marker path
// (ADR-012 tier-1-for-untracked). So init succeeds, writes the marker
// vault_path, and writes NO [project_vaults] binding.
// TestInitVaultPath_NoGitTargetWritesLocalOverride (U5): a no-git `--vault-path`
// target writes the tier-1 override into the gitignored .vibe-palace/local.toml,
// NOT the tracked marker (ADR-012 Option (b): the committed marker stays identity
// only), and no [project_vaults] binding. MUTATION: drop maybeWriteLocalOverride's
// os.WriteFile and this goes red (no override file).
func TestInitVaultPath_NoGitTargetWritesLocalOverride(t *testing.T) {
	home, cfg, _ := existingHostEnv(t)
	x := filepath.Join(t.TempDir(), "throwaway") // absent; --no-git ⇒ tier 1
	projDir := filepath.Join(home, "code", "alpha")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	markProjectDir(t, projDir)

	code, out := runInit(t, projDir, "--name", "alpha", "--vault-path", x, "--no-git")
	if code != cli.ExitOK {
		t.Fatalf("a no-git target must write the tier-1 override (ExitOK); got %d\n%s", code, out)
	}
	// The override lives in the gitignored local.toml.
	override, err := os.ReadFile(filepath.Join(projDir, ".vibe-palace", "local.toml"))
	if err != nil {
		t.Fatalf("no-git target must write .vibe-palace/local.toml: %v", err)
	}
	if !strings.Contains(string(override), "vault_path = \""+x+"\"") {
		t.Errorf("local.toml override missing vault_path = %q:\n%s", x, override)
	}
	// The tracked marker stays identity-only — no active vault_path.
	marker, err := os.ReadFile(filepath.Join(projDir, ".vibe-palace.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(marker), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "vault_path") {
			t.Errorf("tracked marker carries an active vault_path (forbidden):\n%q", line)
		}
	}
	body, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "alpha = \"") {
		t.Errorf("no-git target must NOT write a [project_vaults] binding:\n%s", body)
	}
}

// TestInitVaultPath_RefusesWhenNoDefaultVaultPath: a bind needs a host default
// vault_path; without one it is a Fail row and a non-zero exit, not a silent
// skip.
func TestInitVaultPath_RefusesWhenNoDefaultVaultPath(t *testing.T) {
	home, cfg, _ := existingHostEnv(t)
	writeTestFile(t, cfg, "git_enabled = true\n") // config exists but has no vault_path
	x := t.TempDir()
	makeGitVaultWithOrigin(t, x)
	projDir := filepath.Join(home, "code", "alpha")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	markProjectDir(t, projDir)

	code, out := runInit(t, projDir, "--name", "alpha", "--vault-path", x)
	if code == cli.ExitOK {
		t.Fatalf("want a non-zero exit when there is no default vault_path to bind against\n%s", out)
	}
	if !strings.Contains(flat(out), "no default vault_path") {
		t.Errorf("refusal should name the missing default vault_path:\n%s", out)
	}
}

// TestInitVaultPath_NameFlagVsExistingMarkerName: --name differs from the
// marker's [project].name, so Checkouts verification refuses — a binding the
// resolver would not consult is not written.
func TestInitVaultPath_NameFlagVsExistingMarkerName(t *testing.T) {
	home, _, _ := existingHostEnv(t)
	x := t.TempDir()
	makeGitVaultWithOrigin(t, x)
	projDir := filepath.Join(home, "code", "proj")
	writeTestFile(t, filepath.Join(projDir, ".vibe-palace.toml"),
		"[project]\nname = \"beta\"\n") // marker names beta

	code, out := runInit(t, projDir, "--name", "alpha", "--vault-path", x)
	if code == cli.ExitOK {
		t.Fatalf("want a non-zero exit when --name disagrees with the marker's project name\n%s", out)
	}
	if !strings.Contains(flat(out), "vp config bind") {
		t.Errorf("refusal should point at `vp config bind`:\n%s", out)
	}
}

// TestInitVaultPath_FreshHost_IdentityOnlyMarker: with no global config,
// --vault-path sets the host default (tier 3) and writes an IDENTITY-ONLY marker —
// NO vault_path in the tracked marker (ADR-012 Option (b) / U4, R3 fold-in) and NO
// [project_vaults] entry. Resolution still works because X is the host default.
//
// This UPDATES the former TestInitVaultPath_FreshHostUnchanged, which pinned the
// now-removed behavior of writing vault_path into the tracked marker. MUTATION:
// restore the vaultPathOverride write in cmd_init.go and this goes red.
func TestInitVaultPath_FreshHost_IdentityOnlyMarker(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	_, home := initTestEnv(t, false) // no pre-existing config
	x := filepath.Join(t.TempDir(), "vault")
	projDir := filepath.Join(home, "code", "alpha")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	markProjectDir(t, projDir)

	code, out := runInit(t, projDir, "--name", "alpha", "--vault-path", x)
	if code != cli.ExitOK {
		t.Fatalf("fresh-host init failed: exit %d\n%s", code, out)
	}
	cfg, err := storage.VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "vault_path = \""+x+"\"") {
		t.Errorf("fresh host must set the global vault_path to X:\n%s", body)
	}
	// The config template carries a COMMENTED "# [project_vaults]" example; a
	// real binding would be the uncommented "alpha = \"<x>\"" line.
	if strings.Contains(string(body), "alpha = \"") {
		t.Errorf("fresh host must NOT write a [project_vaults] binding:\n%s", body)
	}
	marker, err := os.ReadFile(filepath.Join(projDir, ".vibe-palace.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// ADR-012 Option (b) / U4 (R3 fold-in): vp init must NOT write an active
	// vault_path into the tracked marker. The template's COMMENTED "# vault_path"
	// example is fine; an uncommented `vault_path = ...` line is the hazard.
	for _, line := range strings.Split(string(marker), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "vault_path") {
			t.Errorf("fresh host wrote an ACTIVE vault_path into the tracked marker (ADR-012 Option (b) forbids it):\n%q\nfull marker:\n%s", line, marker)
		}
	}
	if !strings.Contains(string(marker), "alpha") {
		t.Errorf("the identity-only marker must still carry the project name:\n%s", marker)
	}
	// U5: --vault-path's tier-1 override lands in the gitignored local.toml (fresh
	// host is in the non-tier-2 set, so it gets the override too, redundant with the
	// global default it also set but faithful to the old marker-vault_path write).
	override, err := os.ReadFile(filepath.Join(projDir, ".vibe-palace", "local.toml"))
	if err != nil {
		t.Fatalf("U5 must write the override to .vibe-palace/local.toml: %v", err)
	}
	if !strings.Contains(string(override), "vault_path = \""+x+"\"") {
		t.Errorf("local.toml override missing vault_path = %q:\n%s", x, override)
	}
	_ = surface.RequiredDataFormat
}

// TestInitVaultPath_BindFailureIsNonZeroExit: a bind refusal is threaded to a
// non-zero process exit through projectCode, with a Project binding Fail row.
func TestInitVaultPath_BindFailureIsNonZeroExit(t *testing.T) {
	home, _, _ := existingHostEnv(t)
	x := t.TempDir()
	makeBareGitRepo(t, x) // tier-2 target (git repo) but unstamped → bind refuses
	projDir := filepath.Join(home, "code", "alpha")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	markProjectDir(t, projDir)

	code, out := runInit(t, projDir, "--name", "alpha", "--vault-path", x)
	if code == cli.ExitOK {
		t.Fatalf("a bind refusal must be a non-zero exit\n%s", out)
	}
	if !strings.Contains(flat(out), "Project binding") {
		t.Errorf("a bind refusal must render a Project binding row:\n%s", out)
	}
}
