// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
)

// GitRemoteSlugChecked tells "no origin" apart from "could not ask git": the
// vault resolver refuses on the second and must not refuse on the first.
func TestGitRemoteSlugChecked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	git := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = gitenv.SafeGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	t.Run("not_a_repo", func(t *testing.T) {
		if slug, err := GitRemoteSlugChecked(t.TempDir()); slug != "" || err != nil {
			t.Errorf("got (%q, %v), want (\"\", nil)", slug, err)
		}
	})
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	t.Run("no_origin", func(t *testing.T) {
		if slug, err := GitRemoteSlugChecked(repo); slug != "" || err != nil {
			t.Errorf("got (%q, %v), want (\"\", nil)", slug, err)
		}
	})
	t.Run("origin", func(t *testing.T) {
		git(t, repo, "remote", "add", "origin", "git@example.com:team/Qa-Metabuild.git")
		if slug, err := GitRemoteSlugChecked(repo); slug != "qa-metabuild" || err != nil {
			t.Errorf("got (%q, %v), want (\"qa-metabuild\", nil)", slug, err)
		}
	})
	t.Run("git_missing", func(t *testing.T) {
		t.Setenv("PATH", "")
		if slug, err := GitRemoteSlugChecked(repo); err == nil {
			t.Errorf("got (%q, nil) with no git on PATH; want an error, not \"no origin\"", slug)
		}
		if GitRemoteSlug(repo) != "" {
			t.Error("GitRemoteSlug must still degrade to \"\" for detection")
		}
	})
}

// ReadMarker trims the name and reads it tolerantly; FindMarker walks the
// symlink-resolved path.
func TestReadMarkerTrimsAndTolerates(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ConfigFileName)
	for body, want := range map[string]string{
		"[project]\nname = \" qa \"\n": "qa",
		"[project]\nname = 3\n":        "",
		"project = \"x\"\n":            "",
	} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		m, err := ReadMarker(p)
		if err != nil || m.Name != want {
			t.Errorf("ReadMarker(%q) = (%q, %v), want %q", body, m.Name, err, want)
		}
	}
}

// FindMarker never returns $HOME's own marker, however the walk reaches home.
func TestFindMarkerStopsAtHome(t *testing.T) {
	root := t.TempDir()
	realHome := filepath.Join(root, "realhome")
	if err := os.MkdirAll(filepath.Join(realHome, "code", "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realHome, ConfigFileName), []byte("[project]\nname = \"home\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	homeLink := filepath.Join(root, "homelink")
	alias := filepath.Join(root, "alias")
	for _, l := range []string{homeLink, alias} {
		if err := os.Symlink(realHome, l); err != nil {
			t.Skipf("symlink: %v", err)
		}
	}
	t.Setenv("HOME", homeLink)

	// The cwd reaches home through a symlink that is neither $HOME nor its
	// target, so only the symlink-resolved walk meets the boundary.
	// MUTATION CONTRACT: drop EvalSymlinks from FindMarker and this goes RED.
	t.Run("home_reached_through_another_symlink", func(t *testing.T) {
		got, err := FindMarker(filepath.Join(alias, "code", "proj"))
		if err != nil || got != "" {
			t.Errorf("FindMarker = (%q, %v), want no marker ($HOME's own must never count)", got, err)
		}
	})
	// A deleted cwd cannot be resolved, so the walk climbs the logical path
	// and reaches $HOME as given. MUTATION CONTRACT: drop the unresolved-$HOME
	// boundary and this goes RED.
	t.Run("deleted_cwd_under_symlinked_home", func(t *testing.T) {
		got, err := FindMarker(filepath.Join(homeLink, "code", "gone"))
		if err != nil || got != "" {
			t.Errorf("FindMarker = (%q, %v), want no marker ($HOME's own must never count)", got, err)
		}
	})
}
