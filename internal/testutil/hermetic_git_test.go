// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testutil

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// hermeticGit gives a process with NO host identity (an empty HOME, no global
// config, as on a CI runner) the fixture identity, overrides an ambient
// GIT_AUTHOR_* identity, and still lets a repository's own user.email win.
func TestHermeticGitGivesEveryHostTheFixtureIdentity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	// t.Setenv first, so every variable hermeticGit sets or unsets is restored.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "")
	for _, k := range gitIdentityEnv {
		t.Setenv(k, "host-"+strings.ToLower(k)+"@example.com")
	}
	dir, err := fixtureDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := hermeticGit(dir); err != nil {
		t.Fatal(err)
	}
	for _, k := range gitIdentityEnv {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s still set to %q", k, v)
		}
	}
	if os.Getenv("GIT_CONFIG_NOSYSTEM") != "1" {
		t.Errorf("GIT_CONFIG_NOSYSTEM = %q, want 1", os.Getenv("GIT_CONFIG_NOSYSTEM"))
	}

	repo := t.TempDir()
	gitOut(t, repo, "init", "-q")
	if got := gitOut(t, repo, "var", "GIT_AUTHOR_IDENT"); !strings.HasPrefix(got, "vp test <test@vibe-palace.invalid>") {
		t.Errorf("author ident = %q, want the fixture identity", got)
	}
	gitOut(t, repo, "config", "user.email", "local@example.com")
	if got := gitOut(t, repo, "var", "GIT_AUTHOR_IDENT"); !strings.Contains(got, "<local@example.com>") {
		t.Errorf("author ident = %q, want the repository's own user.email to win", got)
	}
}
