// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/project"
)

// C1: a FOUND marker that cannot be read or parsed is a typed refusal, so the
// hook captures nothing instead of falling back to the global (live) vault.
// MUTATION CONTRACT: return the marker error untyped and every subtest goes RED.
func TestResolveRefusesABrokenMarker(t *testing.T) {
	f := newBindFixture(t)
	quantum := makeTestVault(t, filepath.Join(f.home, "quantum-vault"))
	bodies := map[string]string{
		"syntax_error":      "[project]\nname = \"qa\"\ntags = [\n",
		"wrong_typed_vault": "vault_path = 5\n[project]\nname = \"qa\"\n",
	}
	for _, withBinding := range []bool{true, false} {
		extra := ""
		if withBinding {
			extra = "\n[project_vaults]\nqa = \"" + quantum + "\"\n"
		}
		f.writeConfig(t, extra)
		for name, body := range bodies {
			t.Run(name, func(t *testing.T) {
				dir := f.checkout(t, name, body)
				err := mustReject(t, dir)
				if !strings.Contains(err.Error(), "parse") {
					t.Errorf("refusal does not say the marker failed to parse: %v", err)
				}
				if _, _, cerr := CwdMarker(dir); !errors.Is(cerr, ErrVaultBindingRejected) {
					t.Errorf("CwdMarker error is untyped: %v", cerr)
				}
			})
		}
	}
	t.Run("unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a mode-000 file")
		}
		f.writeConfig(t, "\n[project_vaults]\nqa = \""+quantum+"\"\n")
		dir := f.checkout(t, "eacces", "[project]\nname = \"qa\"\n")
		marker := filepath.Join(dir, ".vibe-palace.toml")
		if err := os.Chmod(marker, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(marker, 0o644) })
		mustReject(t, dir)
	})
}

// C2: when this host binds projects and the git-origin lookup cannot run, a
// checkout that names no project is refused (the answer decides between a
// refusal and the live vault), and a named one resolves with a warning.
// MUTATION CONTRACT: treat a lookup error as "no slug" and the first subtest
// goes RED.
func TestResolveWhenGitCannotRuleOutABinding(t *testing.T) {
	f := newBindFixture(t)
	quantum := makeTestVault(t, filepath.Join(f.home, "quantum-vault"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+quantum+"\"\n")
	t.Setenv("PATH", "")

	t.Run("unnamed_marker_refuses", func(t *testing.T) {
		dir := f.checkout(t, "unnamed", "[meta]\nversion_major = 1\n")
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := mustReject(t, dir)
		if !strings.Contains(err.Error(), "could not be read") {
			t.Errorf("refusal does not say the git origin could not be read: %v", err)
		}
	})
	t.Run("named_marker_warns", func(t *testing.T) {
		dir := f.checkout(t, "named", "[project]\nname = \"other\"\n")
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		r := mustResolve(t, dir)
		if r.Path != f.global || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "could not be read") {
			t.Errorf("got (%q, %q), want the global vault with a could-not-check warning", r.Path, r.Warnings)
		}
	})
	t.Run("not_a_repo_needs_no_git", func(t *testing.T) {
		dir := f.checkout(t, "norepo", "[meta]\nversion_major = 1\n")
		if r := mustResolve(t, dir); r.Path != f.global {
			t.Errorf("Path = %q, want the global vault", r.Path)
		}
	})
}

// C3: a relative binding target would resolve against the process cwd, so it
// is refused even when a vault happens to exist at that relative path.
// MUTATION CONTRACT: drop the IsAbs check and this goes RED.
func TestResolveRefusesARelativeBindingTarget(t *testing.T) {
	f := newBindFixture(t)
	makeTestVault(t, filepath.Join(f.home, "vaults", "quantum"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \"vaults/quantum\"\n")
	dir := f.checkout(t, "qa", "[project]\nname = \"qa\"\n")
	t.Chdir(f.home)
	err := mustReject(t, dir)
	if !strings.Contains(err.Error(), "not an absolute path") {
		t.Errorf("refusal does not say the target is relative: %v", err)
	}
}

// C4: an ABSENT or dangling-symlink global config is "no bindings", and tier
// 3 reports the missing config exactly as before [project_vaults] existed.
func TestResolveAbsentOrDanglingConfigIsNoBindings(t *testing.T) {
	for _, dangling := range []bool{false, true} {
		name := map[bool]string{false: "absent", true: "dangling_symlink"}[dangling]
		t.Run(name, func(t *testing.T) {
			f := newBindFixture(t)
			if err := os.Remove(f.cfg); err != nil {
				t.Fatal(err)
			}
			if dangling {
				if err := os.Symlink(filepath.Join(f.home, "nowhere.toml"), f.cfg); err != nil {
					t.Skipf("symlink: %v", err)
				}
			}
			dir := f.checkout(t, "qa", "[project]\nname = \"qa\"\n")
			_, err := ResolveVaultBinding(dir)
			if err == nil {
				t.Fatal("resolution succeeded with no global config")
			}
			if errors.Is(err, ErrVaultBindingRejected) || errors.Is(err, ErrHostConfigUnreadable) {
				t.Errorf("a missing config is reported as a refusal: %v", err)
			}
			if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("a missing config no longer reads as not-configured: %v", err)
			}
		})
	}
}

// C4: a config that is there and unreadable fails resolution closed (it may
// hide a binding) with ErrHostConfigUnreadable, never as a binding refusal.
// MUTATION CONTRACT (G6): treat a non-ENOENT Lstat error as absent and the
// eacces subtest goes RED.
func TestResolveUnreadableConfigFailsClosed(t *testing.T) {
	t.Run("unparseable", func(t *testing.T) {
		f := newBindFixture(t)
		f.writeConfig(t, "\n[project_vaults\n")
		dir := f.checkout(t, "qa", "[project]\nname = \"qa\"\n")
		_, err := ResolveVaultBinding(dir)
		if !errors.Is(err, ErrHostConfigUnreadable) || errors.Is(err, ErrVaultBindingRejected) {
			t.Errorf("err = %v, want ErrHostConfigUnreadable and not ErrVaultBindingRejected", err)
		}
	})
	// Lstat succeeds and the read fails: the read-error branch, not the stat
	// one. MUTATION CONTRACT: treat a non-ENOENT read error as "no bindings"
	// and this goes RED.
	t.Run("unreadable_file", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a mode-000 file")
		}
		f := newBindFixture(t)
		dir := f.checkout(t, "qa", "[project]\nname = \"qa\"\n")
		if err := os.Chmod(f.cfg, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(f.cfg, 0o644) })
		_, err := ResolveVaultBinding(dir)
		if !errors.Is(err, ErrHostConfigUnreadable) {
			t.Errorf("err = %v, want ErrHostConfigUnreadable", err)
		}
	})
	t.Run("eacces", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		f := newBindFixture(t)
		dir := f.checkout(t, "qa", "[project]\nname = \"qa\"\n")
		cfgDir := filepath.Dir(f.cfg)
		if err := os.Chmod(cfgDir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(cfgDir, 0o755) })
		_, err := ResolveVaultBinding(dir)
		if !errors.Is(err, ErrHostConfigUnreadable) {
			t.Errorf("err = %v, want ErrHostConfigUnreadable", err)
		}
	})
}

// G5 and G2: an empty value and a file target are each refused as what they
// are, not as a generic "not a vault".
func TestResolveRefusesEmptyValueAndFileTargetAsSuch(t *testing.T) {
	f := newBindFixture(t)
	dir := f.checkout(t, "qa", "[project]\nname = \"qa\"\n")

	f.writeConfig(t, "\n[project_vaults]\nqa = \"  \"\n")
	if err := mustReject(t, dir); !strings.Contains(err.Error(), "empty string") {
		t.Errorf("empty value not refused as such: %v", err)
	}

	file := filepath.Join(f.home, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+file+"\"\n")
	if err := mustReject(t, dir); !strings.Contains(err.Error(), "is not a directory") {
		t.Errorf("file target not refused as a non-directory: %v", err)
	}
}

// C6: ONE marker walk and ONE reader. The vault binding and the project label
// come from the same file and the same name, including a padded name and a
// checkout reached through a symlink whose logical and physical parents hold
// different markers. MUTATION CONTRACT: give the resolver its own logical walk
// (or its own untrimmed name) and this goes RED.
func TestResolverAndDetectionShareOneMarker(t *testing.T) {
	f := newBindFixture(t)
	vaultA := makeTestVault(t, filepath.Join(f.home, "vault-a"))
	vaultB := makeTestVault(t, filepath.Join(f.home, "vault-b"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+vaultA+"\"\nbee = \""+vaultB+"\"\n")

	t.Run("padded_name", func(t *testing.T) {
		dir := f.checkout(t, "padded", "[project]\nname = \" qa \"\n")
		r := mustResolve(t, dir)
		label, err := project.DetectProjectHighConfidence(dir)
		if err != nil {
			t.Fatal(err)
		}
		if r.Path != vaultA || label != "qa" {
			t.Errorf("resolver bound %q, detection labelled %q; want vault-a and qa", r.Path, label)
		}
	})
	t.Run("symlinked_checkout", func(t *testing.T) {
		// Physical: <home>/a/proj under a marker naming qa.
		// Logical:  <home>/b/link -> <home>/a/proj, under a marker naming bee.
		physParent := filepath.Join(f.home, "a")
		logParent := filepath.Join(f.home, "b")
		proj := filepath.Join(physParent, "proj")
		for _, d := range []string{proj, logParent} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(physParent, ".vibe-palace.toml"), []byte("[project]\nname = \"qa\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(logParent, ".vibe-palace.toml"), []byte("[project]\nname = \"bee\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(logParent, "link")
		if err := os.Symlink(proj, link); err != nil {
			t.Skipf("symlink: %v", err)
		}
		r := mustResolve(t, link)
		label, err := project.DetectProjectHighConfidence(link)
		if err != nil {
			t.Fatal(err)
		}
		wantVault := map[string]string{"qa": vaultA, "bee": vaultB}[label]
		if r.Path != wantVault {
			t.Errorf("resolver bound %q while detection labelled %q (whose vault is %q): two markers", r.Path, label, wantVault)
		}
	})
}
