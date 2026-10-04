// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

func censusGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

// vp migrate kg-census exits 0 when every project matches its HEAD, and
// non-zero when a project's working tree has drifted from it.
func TestMigrateKGCensusExitsNonZeroOnMismatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	root := t.TempDir()
	censusGit(t, root, "init", "-q", "-b", "main")
	triples := filepath.Join(root, "palace", "proj-a", "kg", "triples")
	if err := os.MkdirAll(triples, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(triples, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.json", `{"subject":"a","predicate":"p","object":"b"}`)
	censusGit(t, root, "add", "-A")
	censusGit(t, root, "commit", "-q", "-m", "kg")

	if code := cmdMigrateKGCensus().Run([]string{root}); code != cli.ExitOK {
		t.Fatalf("a copy matching its HEAD exits %d, want %d", code, cli.ExitOK)
	}
	write("b.json", `{"subject":"c","predicate":"p","object":"d"}`)
	if code := cmdMigrateKGCensus().Run([]string{root}); code == cli.ExitOK {
		t.Fatal("a copy whose working tree differs from HEAD exits 0")
	}
}
