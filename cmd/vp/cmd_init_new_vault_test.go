// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// vaultShape lists every path under root (slash-separated, dirs with a trailing
// slash) with the bytes of each regular file. .git and .vp-locks are recorded
// as present and not descended into: .git's internals carry timestamps and
// hashes that differ between any two repositories, and each .vp-locks file is
// named by a hash of an absolute path, so two vaults at two paths never share
// one.
func vaultShape(t *testing.T, root string) (paths []string, files map[string][]byte) {
	t.Helper()
	files = map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			paths = append(paths, rel+"/")
			if d.Name() == ".git" || d.Name() == ".vp-locks" {
				return filepath.SkipDir
			}
			return nil
		}
		paths = append(paths, rel)
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		files[rel] = b
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(paths)
	return paths, files
}

// TestInitVaultPathOnExistingInstallCreatesTheSameVault: `vp init --vault-path
// NEWDIR` on a host whose global config already exists creates NEWDIR as the
// same fully formed vault the first-install path creates — git repository,
// canonical .gitignore, data-format stamp — through the same step, not a bare
// directory and not an error. Before the per-project vault config retired, the
// directory appeared only as a side effect of writing that file into it; after,
// init exited 2 on the missing vault root.
func TestInitVaultPathOnExistingInstallCreatesTheSameVault(t *testing.T) {
	// Reference: the first-install path (no global config yet).
	initTestEnv(t, false)
	refVault := filepath.Join(t.TempDir(), "ref-vault")
	refProj := t.TempDir()
	markProjectDir(t, refProj)
	if code := cmdInit(cli.BuildInfo{Version: "test"}).Run(
		[]string{refProj, "--name", "shape", "--vault-path", refVault}); code != cli.ExitOK {
		t.Fatalf("first-install init: exit %d", code)
	}

	// Subject: an existing install, and an explicit --vault-path that does not
	// exist yet.
	initTestEnv(t, true)
	newVault := filepath.Join(t.TempDir(), "new-vault")
	proj := t.TempDir()
	markProjectDir(t, proj)
	var code int
	out := captureStdout(t, func() {
		code = cmdInit(cli.BuildInfo{Version: "test"}).Run(
			[]string{proj, "--name", "shape", "--vault-path", newVault})
	})
	if code != cli.ExitOK {
		t.Fatalf("existing-install init --vault-path NEWDIR: exit %d, want ExitOK\n%s", code, out)
	}
	if !strings.Contains(out, "[pass] Vault") {
		t.Errorf("existing-install init did not report the vault it created:\n%s", out)
	}

	refPaths, refFiles := vaultShape(t, refVault)
	gotPaths, gotFiles := vaultShape(t, newVault)
	if strings.Join(refPaths, "\n") != strings.Join(gotPaths, "\n") {
		t.Errorf("vault shape differs from the first-install vault\n--- first install ---\n%s\n--- --vault-path on an existing install ---\n%s",
			strings.Join(refPaths, "\n"), strings.Join(gotPaths, "\n"))
	}
	for rel, want := range refFiles {
		if got, ok := gotFiles[rel]; ok && !bytes.Equal(got, want) {
			t.Errorf("%s differs from the first-install vault:\n got: %q\nwant: %q", rel, got, want)
		}
	}
	// The three artifacts named explicitly, so a shape that lost all of them
	// in BOTH vaults cannot pass the comparison above.
	must := []string{".gitignore"}
	if storage.GitAvailable() {
		must = append(must, ".git/")
	}
	for _, rel := range must {
		if !containsString(gotPaths, rel) {
			t.Errorf("new vault is missing %s", rel)
		}
	}
	if !containsString(gotPaths, ".vibe-palace/vault.toml") {
		t.Errorf("new vault carries no data-format stamp (.vibe-palace/vault.toml): %v", gotPaths)
	}
}

// TestInitVaultPathOnExistingInstallHonoursGitDisabledHost: on an existing
// install the host config's git_enabled is the operator's policy, so a vault
// created for --vault-path NEWDIR on a git_enabled = false host gets no git
// repository — and the Vault row says why — while still getting the rest of
// the vault (.gitignore, format stamp).
func TestInitVaultPathOnExistingInstallHonoursGitDisabledHost(t *testing.T) {
	configDir, _ := initTestEnv(t, false)
	if err := os.MkdirAll(filepath.Join(configDir, "vibe-palace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "vibe-palace", "config.toml"),
		[]byte(`vault_path = "`+t.TempDir()+`"`+"\ngit_enabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newVault := filepath.Join(t.TempDir(), "new-vault")
	proj := t.TempDir()
	markProjectDir(t, proj)
	var code int
	out := captureStdout(t, func() {
		code = cmdInit(cli.BuildInfo{Version: "test"}).Run(
			[]string{proj, "--name", "nogit", "--vault-path", newVault})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit %d, want ExitOK\n%s", code, out)
	}
	if _, err := os.Lstat(filepath.Join(newVault, ".git")); !os.IsNotExist(err) {
		t.Errorf("a git_enabled = false host got a git repository in the new vault (lstat err: %v)", err)
	}
	if !strings.Contains(foldSpace(out), "git_enabled = false in the host config") {
		t.Errorf("the Vault row does not say why there is no repository:\n%s", out)
	}
	for _, rel := range []string{".gitignore", filepath.Join(".vibe-palace", "vault.toml")} {
		if _, err := os.Stat(filepath.Join(newVault, rel)); err != nil {
			t.Errorf("new vault is missing %s: %v", rel, err)
		}
	}
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
