// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// marked makes dir a project root (a .vibe-palace.toml marker) and returns it
// symlink-resolved, which is the form ClaimDir reports.
func marked(t *testing.T, dir string) string {
	t.Helper()
	writeVibeMarker(t, dir)
	r, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mkdir(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClaimDir_Table(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := marked(t, t.TempDir())
	sub := mkdir(t, filepath.Join(root, "internal", "pkg"))
	want := filepath.Join(root, ".vibe-palace")

	for name, cwd := range map[string]string{"root": root, "sub": filepath.Join(root, "internal"), "deep sub": sub} {
		if got := ClaimDir(cwd); got != want {
			t.Errorf("%s: ClaimDir(%q) = %q, want %q", name, cwd, got, want)
		}
	}

	// A nested marker is its own root (a worktree or scratch copy): the NEAREST wins.
	nested := marked(t, mkdir(t, filepath.Join(root, "wt")))
	inner := mkdir(t, filepath.Join(nested, "a", "b"))
	if got, w := ClaimDir(inner), filepath.Join(nested, ".vibe-palace"); got != w {
		t.Errorf("nested marker: got %q, want %q", got, w)
	}
	// A dir under the parent with no marker of its own resolves UP to the parent root.
	if got := ClaimDir(mkdir(t, filepath.Join(root, "wt2"))); got != want {
		t.Errorf("unmarked sibling dir: got %q, want %q", got, want)
	}

	// A symlinked cwd resolves to the same, symlink-resolved root.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(sub, link); err != nil {
		t.Fatal(err)
	}
	if got := ClaimDir(link); got != want {
		t.Errorf("symlinked cwd: got %q, want %q", got, want)
	}
}

func TestClaimDir_NoMarkerFallsBackToCwd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	if got, want := ClaimDir(cwd), filepath.Join(cwd, ".vibe-palace"); got != want {
		t.Errorf("no marker: got %q, want %q", got, want)
	}
}

// An empty cwd must take the raw-join fallback WITHOUT consulting FindMarker,
// whose filepath.Abs("") resolves the PROCESS cwd — a claim dir nobody asked for.
func TestClaimDir_EmptyCwd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := marked(t, t.TempDir())
	t.Chdir(root) // the process cwd IS a marked root: a FindMarker("") would find it.
	if got, want := ClaimDir(""), ".vibe-palace"; got != want {
		t.Errorf("ClaimDir(\"\") = %q, want the raw join %q", got, want)
	}
}

// The hook run from a subdirectory must sweep, read and write the ROOT's claim
// dir, and must not create a .vibe-palace in the subdirectory.
func TestRun_FromSubdirUsesRootClaimDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	vaultRoot := t.TempDir()
	testutil.InitProject(t, vaultRoot, "test-project")
	root := marked(t, t.TempDir())
	sub := mkdir(t, filepath.Join(root, "internal", "pkg"))
	rootClaims := filepath.Join(root, ".vibe-palace")

	// Sweep: a dead session's claim at the root must be purged by a hook that
	// started in the subdir (the current session's own claim is protected).
	if err := WriteClaim(rootClaims, "dead-session", "n1"); err != nil {
		t.Fatal(err)
	}
	if err := WriteClaim(rootClaims, "cur-session", "n2"); err != nil {
		t.Fatal(err)
	}

	res, err := Run(context.Background(), Payload{
		SessionID: "cur-session", CWD: sub, HookEventName: "SessionEnd",
	}, RunOptions{VaultRoot: vaultRoot, ProjectSlug: "test-project", VPVersion: "test"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.ClaimedSkip {
		t.Error("the hook in a subdir did not see the claim at the project root")
	}
	if IsClaimed(rootClaims, "dead-session") {
		t.Error("the sweep did not reach the root's claim dir from a subdir")
	}
	if !IsClaimed(rootClaims, "cur-session") {
		t.Error("the sweep purged the current session's claim")
	}
	if _, err := os.Stat(filepath.Join(sub, ".vibe-palace")); err == nil {
		t.Error("the hook created a stray .vibe-palace in the subdirectory")
	}
}
