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
)

// installTestEnv sets up an isolated $HOME and a fresh vault dir for an install
// test. Returns (vaultPath, homeDir).
func installTestEnv(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	vault := t.TempDir()
	t.Setenv("HOME", home)
	return vault, home
}

func TestEnsureMergeDriverInstalled_FreshVault(t *testing.T) {
	vault, home := installTestEnv(t)

	installed, err := EnsureMergeDriverInstalled(vault)
	if err != nil {
		t.Fatalf("EnsureMergeDriverInstalled: %v", err)
	}
	if !installed {
		t.Fatalf("installed = false, want true (fresh vault)")
	}

	gaData, err := os.ReadFile(filepath.Join(vault, ".gitattributes"))
	if err != nil {
		t.Fatalf("read .gitattributes: %v", err)
	}
	if !strings.Contains(string(gaData), "*.surface merge=vp-surface") {
		t.Errorf(".gitattributes missing vp-surface entry:\n%s", gaData)
	}

	gcData, err := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if err != nil {
		t.Fatalf("read .gitconfig: %v", err)
	}
	gc := string(gcData)
	if !strings.Contains(gc, `[merge "vp-surface"]`) {
		t.Errorf(".gitconfig missing [merge \"vp-surface\"] section:\n%s", gc)
	}
	if !strings.Contains(gc, "driver = vp vault merge-driver %O %A %B") {
		t.Errorf(".gitconfig missing driver line:\n%s", gc)
	}
}

func TestEnsureMergeDriverInstalled_AlreadyInstalled(t *testing.T) {
	vault, home := installTestEnv(t)

	if err := os.WriteFile(
		filepath.Join(vault, ".gitattributes"),
		[]byte("*.surface merge=vp-surface\n"),
		0o644,
	); err != nil {
		t.Fatalf("seed .gitattributes: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(home, ".gitconfig"),
		[]byte("[merge \"vp-surface\"]\n\tdriver = vp vault merge-driver %O %A %B\n"),
		0o644,
	); err != nil {
		t.Fatalf("seed .gitconfig: %v", err)
	}

	installed, err := EnsureMergeDriverInstalled(vault)
	if err != nil {
		t.Fatalf("EnsureMergeDriverInstalled: %v", err)
	}
	if installed {
		t.Fatalf("installed = true, want false (already installed)")
	}
}

func TestEnsureMergeDriverInstalled_PartialInstall(t *testing.T) {
	vault, home := installTestEnv(t)

	// Only the .gitattributes side is pre-installed.
	if err := os.WriteFile(
		filepath.Join(vault, ".gitattributes"),
		[]byte("*.surface merge=vp-surface\n"),
		0o644,
	); err != nil {
		t.Fatalf("seed .gitattributes: %v", err)
	}

	installed, err := EnsureMergeDriverInstalled(vault)
	if err != nil {
		t.Fatalf("EnsureMergeDriverInstalled: %v", err)
	}
	if !installed {
		t.Fatalf("installed = false, want true (gitconfig side not yet installed)")
	}

	gcData, err := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if err != nil {
		t.Fatalf("read .gitconfig: %v", err)
	}
	if !strings.Contains(string(gcData), `[merge "vp-surface"]`) {
		t.Errorf(".gitconfig missing section after partial install:\n%s", gcData)
	}

	// .gitattributes must not have grown a duplicate line.
	gaData, err := os.ReadFile(filepath.Join(vault, ".gitattributes"))
	if err != nil {
		t.Fatalf("read .gitattributes: %v", err)
	}
	if count := strings.Count(string(gaData), "*.surface merge=vp-surface"); count != 1 {
		t.Errorf(".gitattributes has %d copies of the entry, want 1:\n%s", count, gaData)
	}
}

func TestEnsureMergeDriverInstalled_PreservesExistingContent(t *testing.T) {
	vault, home := installTestEnv(t)

	gaSeed := "# vault-managed gitattributes\n*.md text\n*.png binary\n"
	gcSeed := "[user]\n\temail = test@example.com\n[core]\n\tautocrlf = false\n"

	if err := os.WriteFile(filepath.Join(vault, ".gitattributes"), []byte(gaSeed), 0o644); err != nil {
		t.Fatalf("seed .gitattributes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gcSeed), 0o644); err != nil {
		t.Fatalf("seed .gitconfig: %v", err)
	}

	if _, err := EnsureMergeDriverInstalled(vault); err != nil {
		t.Fatalf("EnsureMergeDriverInstalled: %v", err)
	}

	gaData, _ := os.ReadFile(filepath.Join(vault, ".gitattributes"))
	for _, want := range []string{
		"# vault-managed gitattributes", "*.md text", "*.png binary",
		"*.surface merge=vp-surface",
	} {
		if !strings.Contains(string(gaData), want) {
			t.Errorf(".gitattributes missing %q after install:\n%s", want, gaData)
		}
	}

	gcData, _ := os.ReadFile(filepath.Join(home, ".gitconfig"))
	for _, want := range []string{
		"[user]", "email = test@example.com", "[core]", "autocrlf = false",
		`[merge "vp-surface"]`, "driver = vp vault merge-driver %O %A %B",
	} {
		if !strings.Contains(string(gcData), want) {
			t.Errorf(".gitconfig missing %q after install:\n%s", want, gcData)
		}
	}
}

// TestEnsureMergeDriverInstalled_Idempotent runs the install twice and asserts
// each file ends with exactly one entry (the core idempotency guarantee for
// the live pull/sync auto-invoke).
func TestEnsureMergeDriverInstalled_Idempotent(t *testing.T) {
	vault, home := installTestEnv(t)

	installed1, err := EnsureMergeDriverInstalled(vault)
	if err != nil || !installed1 {
		t.Fatalf("first install: installed=%v err=%v", installed1, err)
	}
	installed2, err := EnsureMergeDriverInstalled(vault)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if installed2 {
		t.Fatalf("second install reported installed=true, want false")
	}

	gaData, _ := os.ReadFile(filepath.Join(vault, ".gitattributes"))
	if c := strings.Count(string(gaData), "*.surface merge=vp-surface"); c != 1 {
		t.Errorf(".gitattributes has %d entries after two installs, want 1:\n%s", c, gaData)
	}
	gcData, _ := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if c := strings.Count(string(gcData), `[merge "vp-surface"]`); c != 1 {
		t.Errorf(".gitconfig has %d sections after two installs, want 1:\n%s", c, gcData)
	}
}

func TestUserHome_HomeEnvSet(t *testing.T) {
	t.Setenv("HOME", "/tmp/some-fake-home")
	got, err := userHome()
	if err != nil {
		t.Fatalf("userHome: %v", err)
	}
	if got != "/tmp/some-fake-home" {
		t.Errorf("userHome = %q, want %q", got, "/tmp/some-fake-home")
	}
}

func TestUserHome_FallsBackWhenHomeUnset(t *testing.T) {
	t.Setenv("HOME", "")
	got, err := userHome()
	if err != nil {
		return // acceptable: host with no resolvable home; fallback branch ran
	}
	if got == "" {
		t.Errorf("userHome with HOME unset returned empty path and nil error")
	}
}

func TestEnsureGitattributesEntry_EmptyVaultPath(t *testing.T) {
	installed, err := ensureGitattributesEntry("")
	if err != nil {
		t.Fatalf("ensureGitattributesEntry(\"\"): %v", err)
	}
	if installed {
		t.Errorf("ensureGitattributesEntry(\"\") = true, want false")
	}
}

func TestEnsureGitattributesEntry_ReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitattributes")
	if err := os.WriteFile(path, []byte("anything"), 0o000); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ensureGitattributesEntry(dir); err == nil {
		t.Errorf("ensureGitattributesEntry on unreadable file: err = nil, want non-nil")
	}
}

func TestEnsureGitconfigSection_ReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode permissions")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".gitconfig")
	if err := os.WriteFile(path, []byte("anything"), 0o000); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ensureGitconfigSection(); err == nil {
		t.Errorf("ensureGitconfigSection on unreadable file: err = nil, want non-nil")
	}
}

func TestContainsLine(t *testing.T) {
	cases := []struct {
		name string
		data string
		line string
		want bool
	}{
		{"empty data", "", "x", false},
		{"exact match", "a\nb\nc", "b", true},
		{"trimmed match", "  hello  \nworld\n", "hello", true},
		{"prefix only no match", "*.surface merge=vp-surface-other", "*.surface merge=vp-surface", false},
		{"trailing newline", "*.surface merge=vp-surface\n", "*.surface merge=vp-surface", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := containsLine([]byte(tc.data), tc.line); got != tc.want {
				t.Errorf("containsLine(%q, %q) = %v, want %v", tc.data, tc.line, got, tc.want)
			}
		})
	}
}

// gitInTest runs a git command in dir with the test's isolated env, failing
// the test on error.
func gitInTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestVaultPull_ConfiguredRemote_AutoInstallsMergeDriver is the PRIMARY
// auto-invoke test (per the 2nd-review correction): with a remote configured,
// `vp vault pull` reaches gitRemotes() successfully and the auto-invoke fires
// on the live path, installing the driver into the vault .gitattributes and the
// (temp) ~/.gitconfig. A second pull proves idempotency across repeated real
// pulls — exactly one entry in each file.
func TestVaultPull_ConfiguredRemote_AutoInstallsMergeDriver(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Isolate git from any system/global config beyond temp HOME.
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	// A local bare repo stands in for the live remote so the pull succeeds
	// offline (no network).
	bare := t.TempDir()
	gitInTest(t, bare, "init", "--bare", "--initial-branch=main")

	// vaultDir is both the configured vault (via XDG config) and a git repo
	// with "origin" pointing at the bare remote.
	vaultDir := setupTestVaultEnv(t)
	gitInTest(t, vaultDir, "init", "--initial-branch=main")
	gitInTest(t, vaultDir, "config", "user.email", "test@example.com")
	gitInTest(t, vaultDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(vaultDir, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	gitInTest(t, vaultDir, "add", "seed.txt")
	gitInTest(t, vaultDir, "commit", "-m", "seed")
	gitInTest(t, vaultDir, "remote", "add", "origin", bare)
	gitInTest(t, vaultDir, "push", "-u", "origin", "main")

	// First live pull → up to date, and the merge driver auto-installs.
	if code := cmdVaultPull().Run(nil); code != cli.ExitOK {
		t.Fatalf("first pull exit = %d, want ExitOK", code)
	}

	gaPath := filepath.Join(vaultDir, ".gitattributes")
	gcPath := filepath.Join(home, ".gitconfig")
	gaData, err := os.ReadFile(gaPath)
	if err != nil {
		t.Fatalf("read .gitattributes after pull: %v", err)
	}
	if !strings.Contains(string(gaData), "*.surface merge=vp-surface") {
		t.Errorf(".gitattributes missing entry after configured-remote pull:\n%s", gaData)
	}
	gcData, err := os.ReadFile(gcPath)
	if err != nil {
		t.Fatalf("read .gitconfig after pull: %v", err)
	}
	if !strings.Contains(string(gcData), `[merge "vp-surface"]`) {
		t.Errorf(".gitconfig missing section after configured-remote pull:\n%s", gcData)
	}

	// Second live pull → idempotent: exactly one entry in each file.
	if code := cmdVaultPull().Run(nil); code != cli.ExitOK {
		t.Fatalf("second pull exit = %d, want ExitOK", code)
	}
	gaData2, _ := os.ReadFile(gaPath)
	if c := strings.Count(string(gaData2), "*.surface merge=vp-surface"); c != 1 {
		t.Errorf(".gitattributes has %d entries after two pulls, want 1:\n%s", c, gaData2)
	}
	gcData2, _ := os.ReadFile(gcPath)
	if c := strings.Count(string(gcData2), `[merge "vp-surface"]`); c != 1 {
		t.Errorf(".gitconfig has %d sections after two pulls, want 1:\n%s", c, gcData2)
	}
}

// TestVaultPull_NoRemote_NoInstall is the defensive no-remote case: with no
// remote configured, `vp vault pull` returns at gitRemotes() before the
// auto-invoke, so neither the vault .gitattributes nor ~/.gitconfig is touched.
func TestVaultPull_NoRemote_NoInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	vaultDir := setupTestVaultEnv(t)
	gitInTest(t, vaultDir, "init", "--initial-branch=main")

	if code := cmdVaultPull().Run(nil); code != cli.ExitSystem {
		t.Fatalf("no-remote pull exit = %d, want ExitSystem", code)
	}
	if _, err := os.Stat(filepath.Join(vaultDir, ".gitattributes")); !os.IsNotExist(err) {
		t.Errorf(".gitattributes should not exist after no-remote pull (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gitconfig")); !os.IsNotExist(err) {
		t.Errorf(".gitconfig should not exist after no-remote pull (err=%v)", err)
	}
}

// TestVaultPull_NoInstallOptOut verifies --no-install-merge-driver suppresses
// the auto-invoke even on the configured-remote path.
func TestVaultPull_NoInstallOptOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	bare := t.TempDir()
	gitInTest(t, bare, "init", "--bare", "--initial-branch=main")

	vaultDir := setupTestVaultEnv(t)
	gitInTest(t, vaultDir, "init", "--initial-branch=main")
	gitInTest(t, vaultDir, "config", "user.email", "test@example.com")
	gitInTest(t, vaultDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(vaultDir, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	gitInTest(t, vaultDir, "add", "seed.txt")
	gitInTest(t, vaultDir, "commit", "-m", "seed")
	gitInTest(t, vaultDir, "remote", "add", "origin", bare)
	gitInTest(t, vaultDir, "push", "-u", "origin", "main")

	if code := cmdVaultPull().Run([]string{"--no-install-merge-driver"}); code != cli.ExitOK {
		t.Fatalf("opt-out pull exit = %d, want ExitOK", code)
	}
	if _, err := os.Stat(filepath.Join(vaultDir, ".gitattributes")); !os.IsNotExist(err) {
		t.Errorf(".gitattributes should not exist with --no-install-merge-driver (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gitconfig")); !os.IsNotExist(err) {
		t.Errorf(".gitconfig should not exist with --no-install-merge-driver (err=%v)", err)
	}
}
