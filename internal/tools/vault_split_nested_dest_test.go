// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func splitGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = storage.SafeGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// TestVaultSplitApply_RefusesADestinationInsideAnotherRepository: the vault
// reconciler rightly skips git init for a nested vault, so a destination inside
// another work tree would get no repository of its own, and verify's remote
// check would answer for the enclosing one. Apply must refuse before creating
// anything, and must still accept a destination under a plain directory.
func TestVaultSplitApply_RefusesADestinationInsideAnotherRepository(t *testing.T) {
	if !storage.GitAvailable() {
		t.Skip("git unavailable")
	}
	root := splitFixtureVault(t, "alpha")

	outer := filepath.Join(t.TempDir(), "outer")
	if err := os.MkdirAll(outer, 0o755); err != nil {
		t.Fatal(err)
	}
	splitGit(t, outer, "init", "-q")
	dest := filepath.Join(outer, "sub", "new-vault")

	p := splitPlannedParams(t, root, dest, "alpha")
	p.Action = "apply"
	_, err := callSplit(t, root, p)
	if err == nil {
		t.Fatal("apply must refuse a destination inside another git repository, and it succeeded")
	}
	if !apperr.IsCaller(err) {
		t.Errorf("want a caller error, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "inside the git repository at") {
		t.Errorf("refusal must name the enclosing repository, got: %v", err)
	}
	if _, serr := os.Stat(dest); !os.IsNotExist(serr) {
		t.Errorf("a refused apply must create nothing at the destination (stat err: %v)", serr)
	}

	// The same call against a destination outside any work tree still works.
	plain := splitDest(t)
	q := splitPlannedParams(t, root, plain, "alpha")
	q.Action = "apply"
	if _, err := callSplit(t, root, q); err != nil {
		t.Fatalf("apply into a plain directory must still succeed: %v", err)
	}
}

// TestVaultSplitVerify_RefusesADestinationThatIsNotItsOwnRepository covers a
// destination that stopped being its own repository after apply (or that an
// older, unguarded apply created nested). Verify must say so, must not blame the
// enclosing repository's remotes on the destination, and purge, which re-runs
// verify, must keep the source.
func TestVaultSplitVerify_RefusesADestinationThatIsNotItsOwnRepository(t *testing.T) {
	if !storage.GitAvailable() {
		t.Skip("git unavailable")
	}
	for _, withRemote := range []bool{false, true} {
		name := "no remote on the enclosing repository"
		if withRemote {
			name = "enclosing repository has a remote"
		}
		t.Run(name, func(t *testing.T) {
			root := splitFixtureVault(t, "alpha")
			outer := filepath.Join(t.TempDir(), "outer")
			dest := filepath.Join(outer, "new-vault")

			p := splitPlannedParams(t, root, dest, "alpha")
			p.Action = "apply"
			if _, err := callSplit(t, root, p); err != nil {
				t.Fatalf("apply: %v", err)
			}
			// Make the destination nested: drop its own repository and turn
			// its parent into one.
			if err := os.RemoveAll(filepath.Join(dest, ".git")); err != nil {
				t.Fatal(err)
			}
			splitGit(t, outer, "init", "-q")
			if withRemote {
				splitGit(t, outer, "remote", "add", "origin", "file:///nonexistent")
			}

			p.Action = "verify"
			_, err := callSplit(t, root, p)
			if err == nil {
				t.Fatal("verify must fail for a destination that is not its own repository")
			}
			if !strings.Contains(err.Error(), "not its own repository") {
				t.Errorf("verify must say the destination is not its own repository, got: %v", err)
			}
			if strings.Contains(err.Error(), "added outside the tool") {
				t.Errorf("verify must not blame the enclosing repository's remotes on the destination: %v", err)
			}

			p.Action = "purge"
			if _, err := callSplit(t, root, p); err == nil {
				t.Fatal("purge must refuse when the destination does not verify")
			}
			for _, rel := range []string{"Projects/alpha/resume.md", "palace/alpha/kg/entities.jsonl"} {
				if _, serr := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); serr != nil {
					t.Errorf("the source must survive a refused purge: %s: %v", rel, serr)
				}
			}
		})
	}
}
