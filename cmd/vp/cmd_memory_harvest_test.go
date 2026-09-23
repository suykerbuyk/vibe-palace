// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/memory"
	"github.com/suykerbuyk/vibe-palace/internal/memorytestutil"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// harvestCLIFixture binds a vault that is its own repository with one commit,
// a known project "harvp" whose directory is the cwd, and a native memory dir
// holding the three typed memories. It sets the host config's git_enabled
// line to gitLine and returns the vault and the config path.
func harvestCLIFixture(t *testing.T, gitLine string) (vault, cfgPath string) {
	t.Helper()
	vault = setupTestVaultEnv(t)
	t.Setenv("CLAUDE_HOME", t.TempDir())
	gitInVault(t, vault, "init", "-q", "-b", "main")
	gitInVault(t, vault, "config", "user.email", "test@test.com")
	gitInVault(t, vault, "config", "user.name", "Test")
	putVaultFile(t, vault, ".gitignore", strings.Join(storage.CanonicalGitignorePatterns, "\n")+"\n")
	putVaultFile(t, vault, "Projects/harvp/README.md", "seed\n")
	gitInVault(t, vault, "add", "-A")
	gitInVault(t, vault, "commit", "-qm", "seed")

	projDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projDir, ".vibe-palace.toml"), []byte("[project]\nname = \"harvp\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(projDir)
	nativeDir, err := memory.NativeDirFromCwd(projDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := memorytestutil.WriteNativeMemoryFixture(nativeDir); err != nil {
		t.Fatal(err)
	}

	cfgPath, err = storage.VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	body := `vault_path = "` + vault + `"` + "\n" + gitLine + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return vault, cfgPath
}

// TestMemoryHarvestCLIReportsASkippedCommit: on a host whose operator set
// git_enabled = false, `vp memory harvest` routes the memory, says the commit
// was skipped because of the setting (not "nothing to commit", which reads as
// a clean tree), exits 0, and leaves HEAD where it was.
func TestMemoryHarvestCLIReportsASkippedCommit(t *testing.T) {
	vault, _ := harvestCLIFixture(t, "git_enabled = false")
	head := strings.TrimSpace(gitInVault(t, vault, "rev-parse", "HEAD"))

	stdout, stderr, code := runVaultCmdCapturingBoth(t, cmdMemoryHarvest())
	if code != cli.ExitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "commit skipped: git_enabled = false on this host") {
		t.Errorf("stdout does not report the skipped commit:\n%s", stdout)
	}
	if strings.Contains(stdout, "nothing to commit") {
		t.Errorf("a skipped commit is reported as a clean tree:\n%s", stdout)
	}
	if !strings.Contains(stdout, "pref-foo.md") {
		t.Errorf("the routed memory is not listed:\n%s", stdout)
	}
	if got := strings.TrimSpace(gitInVault(t, vault, "rev-parse", "HEAD")); got != head {
		t.Errorf("HEAD moved under git_enabled = false")
	}
}

// TestMemoryHarvestCLIReportsAnUnreadableSetting: the memory is still routed
// and listed, and stderr says the commit was not attempted and names the
// config path. The exit code stays this command's existing error code.
func TestMemoryHarvestCLIReportsAnUnreadableSetting(t *testing.T) {
	vault, cfgPath := harvestCLIFixture(t, `git_enabled = "no"`)
	head := strings.TrimSpace(gitInVault(t, vault, "rev-parse", "HEAD"))

	stdout, stderr, code := runVaultCmdCapturingBoth(t, cmdMemoryHarvest())
	if code != cli.ExitSystem {
		t.Errorf("exit %d, want %d", code, cli.ExitSystem)
	}
	if !strings.Contains(stdout, "pref-foo.md") {
		t.Errorf("the routed memory is not listed, though it was written:\nstdout: %s", stdout)
	}
	if !strings.Contains(stderr, "the commit was not attempted") || !strings.Contains(stderr, cfgPath) {
		t.Errorf("stderr does not say the commit was not attempted, naming %s:\n%s", cfgPath, stderr)
	}
	if got := strings.TrimSpace(gitInVault(t, vault, "rev-parse", "HEAD")); got != head {
		t.Errorf("HEAD moved under an unreadable setting")
	}
}

// TestMemoryHarvestCLICommitsWhenGitIsEnabled is the control: the same
// fixture with git enabled commits and says so.
func TestMemoryHarvestCLICommitsWhenGitIsEnabled(t *testing.T) {
	vault, _ := harvestCLIFixture(t, "git_enabled = true")
	head := strings.TrimSpace(gitInVault(t, vault, "rev-parse", "HEAD"))
	stdout, stderr, code := runVaultCmdCapturingBoth(t, cmdMemoryHarvest(), "--no-push")
	if code != cli.ExitOK || !strings.Contains(stdout, "Committed ") {
		t.Fatalf("exit %d, want a commit\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if got := strings.TrimSpace(gitInVault(t, vault, "rev-parse", "HEAD")); got == head {
		t.Error("an enabled harvest left HEAD unchanged")
	}
}

// TestMemoryHarvestCLIRefusesARemovedSlug is the stale-checkout case: the cwd
// marker still names "harvp", but the vault renamed Projects/harvp/ away. The
// marker arm used to authorize that and the harvest re-created
// Projects/harvp/memory/ — and deleted the native originals. It must refuse
// with nothing written and nothing deleted, whatever git_enabled says: the
// removal probe is a read, and the kill switch governs mutation.
func TestMemoryHarvestCLIRefusesARemovedSlug(t *testing.T) {
	for _, gitLine := range []string{"git_enabled = true", "git_enabled = false"} {
		t.Run(gitLine, func(t *testing.T) {
			vault, _ := harvestCLIFixture(t, gitLine)
			gitInVault(t, vault, "mv", "Projects/harvp", "Projects/harvp-renamed")
			gitInVault(t, vault, "commit", "-qm", "migrate: harvp -> harvp-renamed (1/2 rename)")
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			nativeDir, err := memory.NativeDirFromCwd(cwd)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadDir(nativeDir)

			stdout, stderr, code := runVaultCmdCapturingBoth(t, cmdMemoryHarvest())
			if code != cli.ExitUser {
				t.Fatalf("exit %d, want %d (refusal)\nstdout: %s\nstderr: %s", code, cli.ExitUser, stdout, stderr)
			}
			for _, want := range []string{"Projects/harvp/ was removed", "[project].name", "vp init"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr must contain %q:\n%s", want, stderr)
				}
			}
			if _, err := os.Stat(filepath.Join(vault, "Projects", "harvp")); err == nil {
				t.Error("Projects/harvp/ was resurrected")
			}
			after, _ := os.ReadDir(nativeDir)
			if len(after) != len(before) || len(before) == 0 {
				t.Errorf("native memory must be untouched: %d files before, %d after", len(before), len(after))
			}
		})
	}
}

// TestMemoryHarvestCLIRefusesARecordedDeparture: the same stale checkout, on a
// vault with NO git history to fall back on — only the departure record,
// written by storage.RecordDeparture, can refuse, and the refusal must say
// where the project went.
func TestMemoryHarvestCLIRefusesARecordedDeparture(t *testing.T) {
	vault, _ := harvestCLIFixture(t, "git_enabled = true")
	if err := os.RemoveAll(filepath.Join(vault, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(vault, "Projects", "harvp")); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.NewVault(vault).RecordDeparture("harvp", departure.Renamed, "harvp-renamed"); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	nativeDir, err := memory.NativeDirFromCwd(cwd)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadDir(nativeDir)

	stdout, stderr, code := runVaultCmdCapturingBoth(t, cmdMemoryHarvest())
	if code != cli.ExitUser {
		t.Fatalf("exit %d, want %d (refusal)\nstdout: %s\nstderr: %s", code, cli.ExitUser, stdout, stderr)
	}
	if !strings.Contains(stderr, `set [project].name = "harvp-renamed"`) {
		t.Errorf("stderr must carry the redirect:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(vault, "Projects", "harvp")); err == nil {
		t.Error("Projects/harvp/ was resurrected")
	}
	if after, _ := os.ReadDir(nativeDir); len(after) != len(before) || len(before) == 0 {
		t.Errorf("native memory must be untouched: %d files before, %d after", len(before), len(after))
	}
}
