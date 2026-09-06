// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/memorytestutil"
)

// gitOut runs git and returns its output. The package's gitRun discards it.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

// TestHarvestDoesNotSweepAPreStagedIndex is the memory harvest's share of the
// pathspec fix.
//
// Harvest is the third caller of storage.CommitAndPushPaths and it scopes its
// paths carefully — Projects/<p>/memory plus Projects/<p>/.surface, both probed
// for existence first. That scoping bought it nothing while the primitive
// committed with no pathspec: `git commit -m <msg>` records the whole index, so
// anything a human had already staged rode out under the harvest's own message.
//
// This is not a hypothetical staging. Harvest runs at SessionEnd, on a vault a
// human may be working in at that moment — an Obsidian save plus a `git add` in
// another terminal is all it takes.
func TestHarvestDoesNotSweepAPreStagedIndex(t *testing.T) {
	vaultRoot := newGitVault(t)
	nativeDir := filepath.Join(t.TempDir(), "memory")
	if err := memorytestutil.WriteNativeMemoryFixture(nativeDir); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// Content the harvest was never asked to commit, already in the index.
	const decoy = "Knowledge/human-secret.md"
	full := filepath.Join(vaultRoot, filepath.FromSlash(decoy))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("an unreviewed human edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, vaultRoot, "add", "--", decoy)
	// Premise, asserted rather than assumed.
	if out := gitOut(t, vaultRoot, "diff", "--cached", "--name-only"); !strings.Contains(out, decoy) {
		t.Fatalf("test premise broken: %s is not staged; index: %q", decoy, out)
	}

	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir, Push: false})
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	if res.CommitSHA == "" {
		t.Fatalf("harvest did not commit; nothing is being measured: %#v", res)
	}

	tree := gitOut(t, vaultRoot, "ls-tree", "-r", "--name-only", "HEAD")
	if !strings.Contains(tree, "Projects/"+testProject+"/memory/") {
		t.Errorf("the harvested memory is missing from HEAD; tree:\n%s", tree)
	}
	if strings.Contains(tree, decoy) {
		t.Errorf("the harvest committed a pre-staged human file under %q; tree:\n%s",
			gitOut(t, vaultRoot, "log", "-1", "--pretty=%s"), tree)
	}

	// Still the human's to review.
	if out := gitOut(t, vaultRoot, "diff", "--cached", "--name-only"); !strings.Contains(out, decoy) {
		t.Errorf("the pre-staged file is no longer staged after the harvest; index: %q", out)
	}
}
