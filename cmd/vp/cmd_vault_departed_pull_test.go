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

// departedPullVault builds the stale-host state on the CLI's configured vault:
// Projects/old/ seeded and pushed, renamed to Projects/new/ by a second clone
// that pushes, and an unpushed commit under Projects/old/ here.
func departedPullVault(t *testing.T) string {
	t.Helper()
	vault := setupVaultWithOrigin(t)
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	put := func(dir, rel, body string) {
		t.Helper()
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put(vault, "Projects/old/resume.md", "old\n")
	git(vault, "add", "-A")
	git(vault, "commit", "-m", "seed old")
	git(vault, "push", "origin", "main")

	bare := git(vault, "remote", "get-url", "origin")
	other := t.TempDir()
	git(other, "clone", "-q", "-b", "main", bare, ".")
	git(other, "config", "user.email", "other@test.com")
	git(other, "config", "user.name", "Other")
	git(other, "mv", "Projects/old", "Projects/new")
	put(other, "Audits/departures/old.json",
		`{"format":"vp-departure/1","slug":"old","kind":"renamed","to":"new","date":"2026-09-23"}`+"\n")
	git(other, "add", "-A")
	git(other, "commit", "-m", "rename old -> new")
	git(other, "push", "origin", "main")

	put(vault, "Projects/old/x.md", "stale host work\n")
	git(vault, "add", "-A")
	git(vault, "commit", "-m", "stale host work")
	return vault
}

// T13. `vp vault pull` from the stale host exits non-zero, names the path,
// prints the hold-branch remedy against the real vault root, and merges nothing.
func TestVaultPullCLIPrintsTheRemedy(t *testing.T) {
	vault := departedPullVault(t)
	var code int
	stderr := captureStderr(t, func() { code = cmdVaultPull().Run(nil) })
	if code == cli.ExitOK {
		t.Fatalf("vp vault pull must refuse; stderr:\n%s", stderr)
	}
	for _, want := range []string{"Projects/old/x.md", `renamed to "new"`, "git -C " + vault + " branch hold/departed-old-"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr must contain %q:\n%s", want, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(vault, "Projects", "new")); err == nil {
		t.Error("the rename was merged")
	}
}
