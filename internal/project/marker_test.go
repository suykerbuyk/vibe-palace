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

// IsTrackedMarker fails CLOSED: no repo → honor (false,nil); committed → tracked;
// present-but-not-added → untracked; git unavailable in a repo → error.
func TestIsTrackedMarker(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	git := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = gitenv.SafeGitEnv("GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	t.Run("no_repo_is_untracked_not_error", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, ConfigFileName)
		if err := os.WriteFile(p, []byte("[project]\nname = \"x\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		tracked, err := IsTrackedMarker(p)
		if tracked || err != nil {
			t.Errorf("got (%v, %v), want (false, nil) for a non-git tree", tracked, err)
		}
	})

	repo := t.TempDir()
	git(t, repo, "init", "-q")
	p := filepath.Join(repo, ConfigFileName)
	if err := os.WriteFile(p, []byte("vault_path = \"/x\"\n[project]\nname = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("present_but_not_added_is_untracked", func(t *testing.T) {
		tracked, err := IsTrackedMarker(p)
		if tracked || err != nil {
			t.Errorf("got (%v, %v), want (false, nil) for a file not in the index", tracked, err)
		}
	})

	t.Run("committed_is_tracked", func(t *testing.T) {
		git(t, repo, "add", ConfigFileName)
		git(t, repo, "commit", "-q", "-m", "add marker")
		tracked, err := IsTrackedMarker(p)
		if !tracked || err != nil {
			t.Errorf("got (%v, %v), want (true, nil) for a committed marker", tracked, err)
		}
	})

	// F3: a committed marker NOT at the repo root — pins that the basename pathspec
	// plus cmd.Dir = the marker's directory handles a nested marker (exit 0), rather
	// than asking from the repo root where the basename pathspec would miss (exit 1).
	t.Run("nested_committed_marker_is_tracked", func(t *testing.T) {
		sub := filepath.Join(repo, "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		np := filepath.Join(sub, ConfigFileName)
		if err := os.WriteFile(np, []byte("[project]\nname = \"nested\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "add", filepath.Join("sub", ConfigFileName))
		git(t, repo, "commit", "-q", "-m", "add nested marker")
		tracked, err := IsTrackedMarker(np)
		if !tracked || err != nil {
			t.Errorf("got (%v, %v), want (true, nil) for a committed NESTED marker", tracked, err)
		}
	})

	t.Run("git_unavailable_in_a_repo_is_an_error", func(t *testing.T) {
		t.Setenv("PATH", "") // git is now unfindable; the .git dir still exists
		tracked, err := IsTrackedMarker(p)
		if err == nil {
			t.Errorf("got (%v, nil) with no git on PATH; want an error (fail closed), not \"untracked\"", tracked)
		}
		if tracked {
			t.Errorf("fail-closed must not report tracked=true: %v", err)
		}
	})

	// F3: an UNSTATABLE .git (the marker's directory is mode 000, so os.Stat of its
	// .git candidate returns EACCES, not ErrNotExist) must FAIL CLOSED — a non-nil
	// error, never (false, nil). RED against the old findFileUpward probe (which
	// treats EACCES as "keep climbing" → honor); GREEN after findGitTreeFailClosed.
	t.Run("unstatable_git_fails_closed", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory mode bits; EACCES cannot be provoked")
		}
		base := t.TempDir()
		child := filepath.Join(base, "child")
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		mp := filepath.Join(child, ConfigFileName)
		if err := os.WriteFile(mp, []byte("vault_path = \"/x\"\n[project]\nname = \"x\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(child, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(child, 0o755) }) // let TempDir cleanup succeed

		tracked, err := IsTrackedMarker(mp)
		if err == nil {
			t.Errorf("got (%v, nil) for an unstatable .git path; want a non-nil error (fail closed)", tracked)
		}
		if tracked {
			t.Errorf("fail-closed must not report tracked=true: %v", err)
		}
	})
}

// FindLocalOverride walks up for .vibe-palace/local.toml and stops at $HOME,
// exactly like FindMarker; LocateLocalOverride reads its vault_path.
func TestFindLocalOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, "code", "proj")
	sub := filepath.Join(proj, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("none_is_empty_not_error", func(t *testing.T) {
		got, err := FindLocalOverride(sub)
		if err != nil || got != "" {
			t.Errorf("FindLocalOverride = (%q, %v), want (\"\", nil)", got, err)
		}
		m, err := LocateLocalOverride(sub)
		if err != nil || m.Path != "" || m.VaultPath != "" {
			t.Errorf("LocateLocalOverride = (%+v, %v), want a zero Marker", m, err)
		}
	})

	want := filepath.Join(proj, LocalOverrideName)
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("vault_path = \"/tmp/ov\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("found_walking_up_and_read", func(t *testing.T) {
		got, err := FindLocalOverride(sub)
		if err != nil || got != want {
			t.Errorf("FindLocalOverride = (%q, %v), want %q", got, err, want)
		}
		m, err := LocateLocalOverride(sub)
		if err != nil || m.VaultPath != "/tmp/ov" {
			t.Errorf("LocateLocalOverride vault_path = (%q, %v), want /tmp/ov", m.VaultPath, err)
		}
	})

	t.Run("stops_at_home", func(t *testing.T) {
		// Plant $HOME/.vibe-palace/local.toml; a checkout under $HOME must not see it.
		hp := filepath.Join(home, LocalOverrideName)
		if err := os.MkdirAll(filepath.Dir(hp), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hp, []byte("vault_path = \"/tmp/home-leak\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		bare := filepath.Join(home, "code", "bare")
		if err := os.MkdirAll(bare, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := FindLocalOverride(bare)
		if err != nil || got != "" {
			t.Errorf("FindLocalOverride = (%q, %v), want no override ($HOME's own must never count)", got, err)
		}
	})
}
