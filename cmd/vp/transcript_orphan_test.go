// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

const cliOrphanRel = "Projects/test-proj/transcripts/orphan.jsonl.zst"

// orphanedVault is a vault with an origin and one aged lone archive.
func orphanedVault(t *testing.T) string {
	t.Helper()
	vault := setupVaultWithOrigin(t)
	abs := filepath.Join(vault, filepath.FromSlash(cliOrphanRel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("zst\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(abs, old, old); err != nil {
		t.Fatal(err)
	}
	return vault
}

// T7a: `vp vault tidy` prints the orphan notice, naming the path, on STDOUT
// (printTidyReported writes there — asserting stderr here would be an arm that
// cannot fail).
func TestVaultTidyCLIPrintsTheOrphan(t *testing.T) {
	orphanedVault(t)
	var code int
	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = cmdVaultTidy().Run([]string{"--no-push"}) })
	})
	if code != cli.ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "ORPHANED") || !strings.Contains(stdout, cliOrphanRel) {
		t.Errorf("tidy stdout does not show the orphan:\n%s", stdout)
	}
}

// T7b: `vp vault sync` prints it too, on stdout, and the orphan does not make
// the sync fail.
func TestVaultSyncCLIPrintsTheOrphan(t *testing.T) {
	orphanedVault(t)
	var code int
	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = cmdVaultSync().Run(nil) })
	})
	if code != cli.ExitOK {
		t.Fatalf("sync failed on an orphan: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "ORPHANED") || !strings.Contains(stdout, cliOrphanRel) {
		t.Errorf("sync stdout does not show the orphan:\n%s", stdout)
	}
}
