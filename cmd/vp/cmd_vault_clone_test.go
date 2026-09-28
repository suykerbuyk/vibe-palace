// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// The CLI is thin over storage.CloneVault: argument shape, --bind taking
// several projects, and the dry run's printed real-run line.
func TestVaultCloneCLI(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runVaultClone([]string{"only-a-url"}, &out, &errOut); code != cli.ExitUser || !strings.Contains(errOut.String(), "<url> and <path>") {
		t.Fatalf("one argument: exit %d, %s", code, errOut.String())
	}
	errOut.Reset()
	if code := runVaultClone([]string{"u", "/p", "stray"}, &out, &errOut); code != cli.ExitUser || !strings.Contains(errOut.String(), "follow --bind") {
		t.Fatalf("a stray positional: exit %d, %s", code, errOut.String())
	}

	setupVaultWithOrigin(t)
	src, _ := newRepoWithOrigin(t)
	mkfile(t, src, ".vibe-palace/vault.toml", fmt.Sprintf("format = %d\n", surface.RequiredDataFormat))
	gitRun(t, src, "add", "-A")
	gitRun(t, src, "commit", "-m", "a vault")
	target := filepath.Join(t.TempDir(), "clone")
	out.Reset()
	errOut.Reset()
	if code := runVaultClone([]string{"file://" + src, target, "--dry-run"}, &out, &errOut); code != cli.ExitOK {
		t.Fatalf("dry run: exit %d\n%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "To run it:\n  vp vault clone file://"+src+" "+target+" --expect v1:") {
		t.Fatalf("the dry run does not print the real-run line:\n%s", out.String())
	}
}
