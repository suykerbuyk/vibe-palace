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
	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// D2 (split-and-sweep-reporting-defects-found-by-rehearsal-a2): on a vault with
// something to sweep and an uncommitted departure record, `vp vault tidy
// --dry-run` must predict the real run — the same refusal and exit code — while
// still printing what it would have swept. `vp vault sync --dry-run` stops at
// the same point a real sync's tidy step does, before any network preview.
func pendingDepartureCLIVault(t *testing.T) (string, string) {
	t.Helper()
	vaultDir := setupTestVaultEnv(t)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", append([]string{"-C", vaultDir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mkfile(t, vaultDir, "seed.txt", "seed")
	for _, args := range [][]string{{"add", "seed.txt"}, {"commit", "-q", "-m", "seed"}} {
		cmd := exec.Command("git", append([]string{"-C", vaultDir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	artifact := "Projects/vibe-palace/sessions/2026-09-27.md"
	mkfile(t, vaultDir, artifact, "session\n")
	b, err := (departure.Record{Slug: "alpha", Kind: departure.MovedToVault, To: "q"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(vaultDir, departure.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	mkfile(t, vaultDir, departure.RelPath("alpha"), string(b))
	return vaultDir, artifact
}

func TestVaultTidyDryRunPredictsTheCommitGuardRefusal(t *testing.T) {
	vaultDir, artifact := pendingDepartureCLIVault(t)
	head := gitHead(t, vaultDir)

	var dryCode int
	var dryOut string
	dryErr := captureStderr(t, func() {
		dryOut = captureStdout(t, func() { dryCode = cmdVaultTidy().Run([]string{"--dry-run"}) })
	})
	var realCode int
	realErr := captureStderr(t, func() {
		captureStdout(t, func() { realCode = cmdVaultTidy().Run([]string{"--no-push"}) })
	})

	if realCode != cli.ExitSystem || !strings.Contains(realErr, "refusing to commit") {
		t.Fatalf("test premise: a real tidy refuses with exit %d; got %d:\n%s", cli.ExitSystem, realCode, realErr)
	}
	if dryCode != realCode {
		t.Errorf("dry-run exit %d, real run %d: the preview must predict the real verdict", dryCode, realCode)
	}
	if !strings.Contains(dryErr, "a real run would refuse: refusing to commit") {
		t.Errorf("dry-run stderr must carry the real run's refusal:\n%s", dryErr)
	}
	for _, want := range []string{"Would sweep", artifact, "dry run: nothing was committed"} {
		if !strings.Contains(dryOut, want) {
			t.Errorf("dry-run stdout missing %q:\n%s", want, dryOut)
		}
	}
	if gitHead(t, vaultDir) != head {
		t.Error("HEAD moved")
	}
}

func TestVaultSyncDryRunStopsAtTheCommitGuard(t *testing.T) {
	vaultDir, _ := pendingDepartureCLIVault(t)
	cmd := exec.Command("git", "-C", vaultDir, "remote", "add", "origin", "https://example.com/repo.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("remote add: %v\n%s", err, out)
	}
	var code int
	var out string
	errOut := captureStderr(t, func() {
		out = captureStdout(t, func() { code = cmdVaultSync().Run([]string{"--dry-run"}) })
	})
	if code != cli.ExitSystem || !strings.Contains(errOut, "vp vault sync: a real run would refuse: refusing to commit") {
		t.Fatalf("sync --dry-run exit %d, want %d with the refusal; stderr:\n%s\nstdout:\n%s", code, cli.ExitSystem, errOut, out)
	}
	// pullAll/pushAll's dry-run preview prints "would run: git -C <root> pull|push ...".
	if strings.Contains(out+errOut, "would run:") {
		t.Errorf("the preview went on to the network after the refusal:\n%s%s", out, errOut)
	}
}
