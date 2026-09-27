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
// still printing what it would have swept.
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

// SF1 (code review round 1): `vp vault sync --dry-run` must predict the real
// sync's verdict and exit code on the same state. A pending departure record is
// genuine dirt, so the real sync refuses on dirt with ExitUser before any
// network I/O — never at the U1 guard — and the preview must say exactly that.
func TestVaultSyncDryRunPredictsTheRealSyncVerdict(t *testing.T) {
	sync := func(t *testing.T, args ...string) (int, string, string) {
		t.Helper()
		var code int
		var out string
		errOut := captureStderr(t, func() {
			out = captureStdout(t, func() { code = cmdVaultSync().Run(args) })
		})
		return code, out, errOut
	}
	t.Run("pending record: both refuse on genuine dirt", func(t *testing.T) {
		vaultDir := setupVaultWithOrigin(t)
		mkfile(t, vaultDir, "Projects/vibe-palace/sessions/2026-09-27.md", "session\n")
		b, err := (departure.Record{Slug: "alpha", Kind: departure.MovedToVault, To: "q"}).Encode()
		if err != nil {
			t.Fatal(err)
		}
		mkfile(t, vaultDir, departure.RelPath("alpha"), string(b))

		dryCode, dryOut, dryErr := sync(t, "--dry-run")
		realCode, _, realErr := sync(t)
		if realCode != cli.ExitUser || !strings.Contains(realErr, "vp vault sync: refusing to sync: 1 uncommitted non-artifact file(s)") {
			t.Fatalf("test premise: a real sync refuses on dirt with exit %d; got %d:\n%s", cli.ExitUser, realCode, realErr)
		}
		if dryCode != realCode {
			t.Errorf("dry-run exit %d, real sync %d", dryCode, realCode)
		}
		realMsg := strings.TrimSpace(strings.TrimPrefix(realErr[strings.Index(realErr, "vp vault sync: "):], "vp vault sync: "))
		if !strings.Contains(dryErr, "vp vault sync: a real run would refuse: "+realMsg) {
			t.Errorf("dry-run must predict the real refusal %q; got:\n%s", realMsg, dryErr)
		}
		// pullAll/pushAll's dry-run preview prints "would run: git -C <root> pull|push ...".
		if strings.Contains(dryOut+dryErr, "would run:") {
			t.Errorf("the preview went on to the network after the refusal:\n%s%s", dryOut, dryErr)
		}
	})
	t.Run("only a sweepable artifact: neither refuses", func(t *testing.T) {
		vaultDir := setupVaultWithOrigin(t)
		mkfile(t, vaultDir, "Projects/vibe-palace/sessions/2026-09-27.md", "session\n")
		dryCode, _, dryErr := sync(t, "--dry-run")
		realCode, _, realErr := sync(t)
		if dryCode != cli.ExitOK || realCode != cli.ExitOK {
			t.Errorf("dry-run exit %d (%s), real sync %d (%s); want both %d", dryCode, dryErr, realCode, realErr, cli.ExitOK)
		}
	})
}
