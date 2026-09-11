// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

// A vault whose Templates/ files pass through a clean/smudge filter — git-crypt,
// LFS, any filter driver — stores one form in git and checks out another. The
// prune used to hash HEAD's RAW blob against the worktree's smudged bytes, so on
// such a vault every mirror's committed copy looked like operator content: it
// was "restored" in place and re-planned on every sync, forever. And a filter
// that cannot run, and is not marked required, makes git hand back the cleaned
// bytes with exit 0 — so the restore could write ciphertext over the mirror.

const rot13Filter = "tr A-Za-z N-ZA-Mn-za-m"

// filteredMirrorVault is host A with the embedded wrap.md committed through a
// rot13 clean/smudge filter: HEAD and the origin store the rot13 form, and the
// worktree holds the plain mirror. Returns the vault, project dir, mirror path
// and origin.
func filteredMirrorVault(t *testing.T) (vaultPath, projDir, mirror, origin string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the rot13 filter fixture needs a POSIX shell and tr")
	}
	if _, err := exec.LookPath("tr"); err != nil {
		t.Skip("tr not on PATH")
	}
	vaultPath, projDir = overrideVault(t)
	mirror = putVaultFile(t, vaultPath, "Templates/commands/wrap.md", string(embeddedTemplateBytes(t, "commands/wrap.md")))
	origin = gitifyVault(t, vaultPath)
	gitInVault(t, vaultPath, "config", "filter.rot.clean", rot13Filter)
	gitInVault(t, vaultPath, "config", "filter.rot.smudge", rot13Filter)
	putVaultFile(t, vaultPath, ".gitattributes", "Templates/** filter=rot\n")
	gitInVault(t, vaultPath, "add", "--renormalize", ".")
	gitInVault(t, vaultPath, "add", ".gitattributes")
	gitInVault(t, vaultPath, "commit", "-m", "store Templates/ through the rot filter")
	gitInVault(t, vaultPath, "push", "-q", "origin", "main")

	raw := gitInVault(t, vaultPath, "cat-file", "blob", "HEAD:Templates/commands/wrap.md")
	if raw == string(embeddedTemplateBytes(t, "commands/wrap.md")) {
		t.Fatal("fixture: HEAD should store the rot13 form of the mirror")
	}
	return vaultPath, projDir, mirror, origin
}

// TestConfigSyncPrunesMirrorOnFilteredVault: the mirror's committed copy is
// the mirror, read as git checks it out. It is pruned, committed and pushed
// once, and the next sync has nothing to do.
func TestConfigSyncPrunesMirrorOnFilteredVault(t *testing.T) {
	vaultPath, projDir, mirror, origin := filteredMirrorVault(t)

	out := syncVault(t, projDir, "", "--yes")
	if strings.Contains(out, "restored") {
		t.Errorf("the mirror was restored as operator content:\n%s", out)
	}
	if _, err := os.Stat(mirror); !os.IsNotExist(err) {
		t.Errorf("mirror still present (err=%v)\n%s", err, out)
	}
	if subj := strings.TrimSpace(gitInVault(t, vaultPath, "log", "-1", "--format=%s")); !strings.HasPrefix(subj, "chore(templates): prune") {
		t.Errorf("HEAD is %q, not a prune commit\n%s", subj, out)
	}
	head := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD"))
	if tip := strings.TrimSpace(gitInVault(t, origin, "rev-parse", "main")); tip != head {
		t.Errorf("origin tip %s, HEAD %s: the prune was not pushed", tip, head)
	}

	again := syncVault(t, projDir, "")
	if strings.Contains(again, "restored") || !strings.Contains(again, "Nothing to do") {
		t.Errorf("the second sync planned work:\n%s", again)
	}
	if st := gitInVault(t, vaultPath, "status", "--porcelain", "--", "Templates/"); strings.TrimSpace(st) != "" {
		t.Errorf("Templates/ is dirty: %q", st)
	}
}

// TestConfigSyncNeverRestoresThroughAFailingFilter: the smudge command is
// missing and the filter is not required. git would exit 0 with the cleaned
// (rot13) bytes, the committed copy would read as operator content, and the
// "restore" would check that form out — over the plain mirror when the index's
// stat data is stale. The prune must defer instead: exit 2, nothing written.
func TestConfigSyncNeverRestoresThroughAFailingFilter(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "fresh stat"
		if stale {
			name = "stale stat"
		}
		t.Run(name, func(t *testing.T) {
			vaultPath, projDir, mirror, _ := filteredMirrorVault(t)
			gitInVault(t, vaultPath, "config", "filter.rot.smudge", "/nonexistent/vp-test-smudge")
			if stale {
				old := time.Now().Add(-time.Hour)
				if err := os.Chtimes(mirror, old, old); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(mirror)
			if err != nil {
				t.Fatal(err)
			}
			head := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD"))

			out, code := runSyncWithStdin(t, "", []string{"--project-root", projDir, "--tier", "vault", "--yes"})
			if code != cli.ExitSystem {
				t.Errorf("exit = %d, want %d (a prune git cannot verify is an error to fix)\n%s", code, cli.ExitSystem, out)
			}
			if strings.Contains(out, "restored") {
				t.Errorf("restored through a failing filter:\n%s", out)
			}
			if !strings.Contains(out, "[Skip] TemplateTree:Templates: Templates/commands/wrap.md — prune deferred") ||
				!strings.Contains(out, "cat-file --filters") {
				t.Errorf("no deferred row naming the filtered read:\n%s", out)
			}
			assertFileBytes(t, mirror, string(before))
			if st := gitInVault(t, vaultPath, "status", "--porcelain", "--", "Templates/"); strings.TrimSpace(st) != "" {
				t.Errorf("Templates/ is dirty: %q", st)
			}
			if got := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD")); got != head {
				t.Errorf("a commit was made: %s", gitInVault(t, vaultPath, "log", "-1", "--stat"))
			}
			if _, err := os.Stat(filepath.Join(vaultPath, "Templates", "commands", "wrap.md")); err != nil {
				t.Errorf("mirror removed: %v", err)
			}
		})
	}
}
