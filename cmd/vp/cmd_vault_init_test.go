// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func vaultInitEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	gc := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(gc, []byte("[user]\n\tname = Admin\n\temail = admin@example.com\n[init]\n\tdefaultBranch = master\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gc)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func bare(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	gitOut(t, d, "init", "-q", "--bare", "-b", "main")
	return d
}

// Admin step 1, through the real CLI command and the real scaffold
// (reconcile.ScaffoldNewVault): dry run first, then the real run.
func TestVaultInitCommand_AdminStep1(t *testing.T) {
	vaultInitEnv(t)
	origin, github := bare(t), bare(t)
	path := filepath.Join(t.TempDir(), "quantum-vibe-palace-vault")
	args := []string{path, "--remote", "origin=file://" + origin, "--remote", "github=file://" + github}

	var code int
	out := captureStdout(t, func() { code = cmdVaultInit().Run(append(args, "--dry-run")) })
	if code != cli.ExitOK || !strings.Contains(out, "dry run") {
		t.Fatalf("dry run: code %d, out %q", code, out)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("the dry run created the vault")
	}
	var errOut0 string
	out = captureStdout(t, func() { errOut0 = captureStderr(t, func() { code = cmdVaultInit().Run(args) }) })
	if code != cli.ExitOK {
		t.Fatalf("real run: code %d, out %q", code, out)
	}
	if strings.Contains(errOut0, "unrecognized path") {
		t.Fatalf("vault init warned about its own remotes.toml: %q", errOut0)
	}
	if f, err := surface.ReadFormat(path); err != nil || f != surface.RequiredDataFormat {
		t.Fatalf("the real scaffold did not stamp the format: %d, %v", f, err)
	}
	commit := gitOut(t, path, "rev-parse", "HEAD")
	for _, b := range []string{origin, github} {
		if gitOut(t, b, "rev-parse", "main") != commit {
			t.Fatalf("%s does not hold main at the init commit", b)
		}
	}
	if gitOut(t, path, "rev-parse", "--abbrev-ref", "HEAD") != "main" {
		t.Fatal("not on main")
	}
	if !strings.Contains(out, "origin") || !strings.Contains(out, "(upstream)") {
		t.Fatalf("report does not name the upstream: %q", out)
	}
	// It onboarded no project: no marker file was written anywhere near the
	// vault, unlike `vp init`.
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), ".vibe-palace.toml")); err == nil {
		t.Fatal("vault init wrote a project marker")
	}
	// A second run refuses: the path exists.
	errOut := captureStderr(t, func() { code = cmdVaultInit().Run(args) })
	if code != cli.ExitUser || !strings.Contains(errOut, "already exists") {
		t.Fatalf("re-run: code %d, stderr %q", code, errOut)
	}
}

// `vp vault init` and `vp init` are distinct commands with distinct flags.
func TestVaultInitCommand_DoesNotCollideWithInit(t *testing.T) {
	reg := cli.NewRegistry(cli.BuildInfo{})
	registerAll(reg, cli.BuildInfo{})
	vi, ok := reg.Lookup("vault init")
	if !ok {
		t.Fatal("vault init is not registered")
	}
	in, ok := reg.Lookup("init")
	if !ok {
		t.Fatal("init is not registered")
	}
	if vi == in || vi.Name == in.Name {
		t.Fatal("vault init resolves to init")
	}
	if !strings.Contains(vi.Description, "NOT `vp init`") {
		t.Fatal("vault init's help does not say it is not vp init")
	}
	for _, f := range vi.Flags {
		if f.Name == "--vault-path" || f.Name == "--name" {
			t.Fatalf("vault init carries vp init's flag %s", f.Name)
		}
	}
}

// Every refusal maps to ExitUser; anything else is not a refusal.
func TestVaultInitCommand_ExitCodeMap(t *testing.T) {
	for _, e := range []error{storage.ErrInitPathExists, storage.ErrInitNested, storage.ErrInitRemoteNotEmpty,
		storage.ErrInitRemoteBad, storage.ErrInitNoRemote, storage.ErrInitRemoteUnreached, storage.ErrGitDisabled} {
		if !isInitRefusal(fmt.Errorf("vault init: %w", e)) {
			t.Fatalf("%v is not mapped to a refusal", e)
		}
	}
	if isInitRefusal(errors.New("disk full")) {
		t.Fatal("an arbitrary error was mapped to a refusal")
	}
}

// A partial publish reports only the remotes that hold the commit.
func TestVaultInitCommand_PartialReportNamesOnlyPublished(t *testing.T) {
	rep := &storage.InitVaultReport{Path: "/v", Branch: "main", Commit: "abc", Upstream: "origin",
		Remotes:     []storage.RecordedRemote{{Name: "origin", URL: "file:///o"}, {Name: "github", URL: "file:///g"}},
		PublishedTo: []string{"origin"}}
	out := captureStdout(t, func() { printVaultInitReport(rep) })
	if !strings.Contains(out, "NOT published to: github") || strings.Contains(out, "  github") {
		t.Fatalf("partial report:\n%s", out)
	}
}

// `vp vault init` is not gated by the served vault's surface: it never writes it.
func TestVaultInitCommand_NotGatedByTheServedVault(t *testing.T) {
	if cmdVaultInit().MutatesVault {
		t.Fatal("vault init carries the served-vault surface fail-stop")
	}
}
