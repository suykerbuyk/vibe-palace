// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func copyGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func copyWrite(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// copyRepo makes a vault-shaped repo with files, published to a new bare repo.
func copyRepo(t *testing.T, files map[string]string) (dir, bare string) {
	t.Helper()
	dir, bare = t.TempDir(), t.TempDir()
	copyGit(t, bare, "init", "-q", "--bare", "-b", "main")
	copyGit(t, bare, "config", "uploadpack.allowFilter", "true")
	copyGit(t, bare, "config", "uploadpack.allowAnySHA1InWant", "true")
	copyGit(t, dir, "init", "-q", "-b", "main")
	copyGit(t, dir, "config", "user.email", "t@example.com")
	copyGit(t, dir, "config", "user.name", "T")
	files[".vibe-palace/vault.toml"] = fmt.Sprintf("format = %d\n", surface.RequiredDataFormat)
	for rel, c := range files {
		copyWrite(t, dir, rel, c)
	}
	copyGit(t, dir, "add", "-A")
	copyGit(t, dir, "commit", "-q", "-m", "seed")
	copyGit(t, dir, "remote", "add", "origin", "file://"+bare)
	copyGit(t, dir, "push", "-q", "origin", "main")
	return dir, bare
}

// The CLI prints the exact real-run line on --dry-run, and that line, run as
// printed, copies and publishes.
func TestVaultCopyCLI_DryRunLineRunsAsPrinted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	_, srcBare := copyRepo(t, map[string]string{"Projects/p/resume.md": "p\n"})
	v, vBare := copyRepo(t, map[string]string{"Projects/resident/resume.md": "r\n"})

	var out, errOut bytes.Buffer
	code := runVaultCopy(cmdVaultCopy(), []string{"p", "--from", "file://" + srcBare, "--vault", v, "--dry-run"}, &out, &errOut)
	if code != cli.ExitOK {
		t.Fatalf("dry run exit %d: %s", code, errOut.String())
	}
	if _, err := os.Lstat(filepath.Join(v, "Projects/p")); err == nil {
		t.Fatal("the dry run wrote to the vault")
	}
	s := out.String()
	i := strings.Index(s, "To run it:\n  ")
	if i < 0 || !strings.Contains(s, "Projects/p/resume.md") {
		t.Fatalf("no file list or command line in:\n%s", s)
	}
	line := strings.TrimSpace(s[i+len("To run it:\n  "):])
	fields := strings.Fields(line)
	if len(fields) < 4 || strings.Join(fields[:3], " ") != "vp vault copy" {
		t.Fatalf("command line %q", line)
	}
	out.Reset()
	errOut.Reset()
	if code := runVaultCopy(cmdVaultCopy(), fields[3:], &out, &errOut); code != cli.ExitOK {
		t.Fatalf("the printed line failed (exit %d): %s", code, errOut.String())
	}
	head := copyGit(t, v, "rev-parse", "HEAD")
	if copyGit(t, vBare, "rev-parse", "main") != head || !strings.Contains(out.String(), "copied and published "+head) {
		t.Fatalf("not published: %s", out.String())
	}

	// A second run refuses as a caller error.
	if code := runVaultCopy(cmdVaultCopy(), fields[3:], &out, &errOut); code != cli.ExitUser {
		t.Fatalf("a second copy: exit %d, want %d", code, cli.ExitUser)
	}
}
