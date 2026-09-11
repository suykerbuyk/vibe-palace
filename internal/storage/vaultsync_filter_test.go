// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// A vault whose Templates/ files pass through a clean/smudge filter (git-crypt,
// LFS, any filter driver) stores one form in git and checks out another. The
// verified prune compares the worktree bytes (smudged) with HEAD's and each
// remote tip's copy, so it must read those in the same, checked-out form — and
// a filter that cannot run must be an error that keeps the file, never a
// silent pass-through of the stored bytes.

const rot13 = "tr A-Za-z N-ZA-Mn-za-m"

// needFilterTools skips where the rot13 fixture cannot run.
func needFilterTools(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the rot13 filter fixture needs a POSIX shell and tr")
	}
	if _, err := exec.LookPath("tr"); err != nil {
		t.Skip("tr not on PATH")
	}
}

// configureRot sets a local rot13 clean/smudge filter named rot.
func configureRot(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "config", "filter.rot.clean", rot13)
	gitRun(t, dir, "config", "filter.rot.smudge", rot13)
}

// rotRepo is a repository whose files under T/ are stored rot13 and checked
// out plain. Each file in files is committed through the filter.
func rotRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	needFilterTools(t)
	dir := initTestRepo(t)
	configureRot(t, dir)
	writeFile(t, dir, ".gitattributes", "T/** filter=rot\n")
	for rel, body := range files {
		writeFile(t, dir, rel, body)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "filtered files")
	return dir
}

// rawBlob is HEAD's stored copy of rel, no filter applied.
func rawBlob(t *testing.T, dir, rel string) string {
	t.Helper()
	b, found, err := ReadCommittedBlob(dir, rel)
	if err != nil || !found {
		t.Fatalf("ReadCommittedBlob(%s): found=%v err=%v", rel, found, err)
	}
	return string(b)
}

func TestFilterDrivers(t *testing.T) {
	dir := initTestRepo(t)
	if d, err := filterDrivers(dir); err != nil || len(d) != 0 {
		t.Fatalf("no filter configured: drivers=%v err=%v", d, err)
	}

	gitRun(t, dir, "config", "filter.cleanonly.clean", "cat")
	gitRun(t, dir, "config", "filter.smudgeonly.smudge", "cat")
	gitRun(t, dir, "config", "filter.lfs.process", "git-lfs filter-process")
	gitRun(t, dir, "config", "filter.my.dotted.name.smudge", "cat")
	gitRun(t, dir, "config", "filter.both.clean", "cat")
	gitRun(t, dir, "config", "filter.both.smudge", "cat")
	got, err := filterDrivers(dir)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{"both", "lfs", "my.dotted.name", "smudgeonly"}
	if !slices.Equal(got, want) {
		t.Errorf("drivers = %v, want %v (a clean-only driver changes nothing on checkout)", got, want)
	}

	// A name git -c cannot address is refused, not silently left un-required.
	gitRun(t, dir, "config", "filter.a=b.smudge", "cat")
	if _, err := filterDrivers(dir); err == nil || !strings.Contains(err.Error(), "cannot be required") {
		t.Errorf("a driver named with '=' was accepted: %v", err)
	}

	// Not a repository git can read: an error, never "no drivers".
	notRepo := t.TempDir()
	if err := os.WriteFile(filepath.Join(notRepo, ".git"), []byte("gitdir: /nonexistent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := filterDrivers(notRepo); err == nil {
		t.Error("a broken repository listed no drivers without an error")
	}
}

func TestGitCheckoutContent_FilterFailureIsAnError(t *testing.T) {
	needFilterTools(t)
	const body = "Mirror bytes\n"
	cases := []struct {
		name    string
		driver  string // "" = no filter attribute at all
		smudge  string
		wantErr bool
		want    string
	}{
		{name: "no filter", want: body},
		{name: "working filter", driver: "rot", smudge: rot13, want: "Zveebe olgrf\n"},
		{name: "missing smudge, not required", driver: "rot", smudge: "/nonexistent/vp-test-smudge", wantErr: true},
		{name: "smudge exits 1", driver: "rot", smudge: "false", wantErr: true},
		{name: "stderr on exit 0", driver: "rot", smudge: "sh -c 'echo filter warning >&2; cat'", wantErr: true},
		{name: "dotted driver, missing smudge", driver: "my.rot", smudge: "/nonexistent/vp-test-smudge", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := initTestRepo(t)
			if tc.driver != "" {
				writeFile(t, dir, ".gitattributes", "T/** filter="+tc.driver+"\n")
			}
			// No clean command: the blob is stored as written.
			writeFile(t, dir, "T/a.md", body)
			gitRun(t, dir, "add", "-A")
			gitRun(t, dir, "commit", "-m", "a")
			if tc.driver != "" {
				gitRun(t, dir, "config", "filter."+tc.driver+".smudge", tc.smudge)
			}
			drivers, err := filterDrivers(dir)
			if err != nil {
				t.Fatal(err)
			}
			got, err := gitCheckoutContent(dir, "HEAD", "T/a.md", drivers)
			if tc.wantErr {
				var ge *GitError
				if err == nil || !errors.As(err, &ge) {
					t.Fatalf("got %q, err=%v; want a *GitError", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("content = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPruneMirrorsVerified_ComparesCheckedOutContent: HEAD stores the rot13
// form of the mirror. Compared raw it is "operator content", and the mirror is
// restored in place on every sync; compared checked out, it is the mirror, and
// the prune removes and commits it.
func TestPruneMirrorsVerified_ComparesCheckedOutContent(t *testing.T) {
	dir := rotRepo(t, map[string]string{"T/wrap.md": "mirror\n"})
	if raw := rawBlob(t, dir, "T/wrap.md"); raw != "zveebe\n" {
		t.Fatalf("fixture: HEAD should store the rot13 form, got %q", raw)
	}
	seen := map[string][]byte{}
	res, out, err := PruneMirrorsVerified(dir, []string{"T/wrap.md"}, false, acceptOnly(seen, "mirror\n"))
	if err != nil {
		t.Fatalf("err=%v out=%+v", err, out)
	}
	if len(out.Restored) != 0 || len(out.Removed) != 1 || len(out.Committed) != 1 || res.CommitSHA == "" {
		t.Fatalf("out=%+v res=%+v", out, res)
	}
	if string(seen["T/wrap.md"]) != "mirror\n" {
		t.Errorf("the verifier was shown %q, not the checked-out form", seen["T/wrap.md"])
	}
	if _, present := readBody(t, dir, "T/wrap.md"); present {
		t.Error("mirror not removed")
	}
	cleanT(t, dir)
}

// TestPruneMirrorsVerified_BrokenFilterNeverRestores: a smudge that cannot
// run, and is not marked required, makes git exit 0 with the CLEANED bytes.
// Read that way, HEAD's copy is "operator content" and the restore writes the
// rot13 form over the mirror — when the index's stat data is stale. Forced
// required, the read fails, and the path is deferred: kept, never restored.
func TestPruneMirrorsVerified_BrokenFilterNeverRestores(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "fresh stat"
		if stale {
			name = "stale stat"
		}
		t.Run(name, func(t *testing.T) {
			dir := rotRepo(t, map[string]string{"T/wrap.md": "mirror\n"})
			gitRun(t, dir, "config", "filter.rot.smudge", "/nonexistent/vp-test-smudge")
			if stale {
				touchStale(t, filepath.Join(dir, "T", "wrap.md"))
			}
			head := gitRun(t, dir, "rev-parse", "HEAD")
			_, out, err := PruneMirrorsVerified(dir, []string{"T/wrap.md"}, false, acceptOnly(nil, "mirror\n"))
			if err == nil {
				t.Fatal("a filter that cannot run was not an error")
			}
			if len(out.Restored) != 0 || len(out.Removed) != 0 || len(out.Kept) != 1 {
				t.Fatalf("out=%+v", out)
			}
			if !strings.Contains(out.Kept[0].Reason, "prune deferred") {
				t.Errorf("kept reason = %q", out.Kept[0].Reason)
			}
			if body, _ := readBody(t, dir, "T/wrap.md"); body != "mirror\n" {
				t.Errorf("worktree bytes changed to %q", body)
			}
			if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
				t.Error("a commit was made")
			}
			cleanT(t, dir)
		})
	}
}

// touchStale moves a file's mtime so the index's stat data no longer matches
// and git must re-read it — the state an editor's save of identical bytes, a
// file-sync touch or `touch` leaves.
func touchStale(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	old := fi.ModTime().Add(-3600e9)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteAllows_ComparesCheckedOutContent(t *testing.T) {
	dir := rotRepo(t, map[string]string{"T/wrap.md": "mirror\n", "T/over.md": "mirror\n"})
	remote := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", remote)
	gitRun(t, dir, "push", "-q", "origin", "main")
	// The remote's copy of over.md becomes operator content.
	other := filepath.Join(t.TempDir(), "other")
	// The filter is configured before the clone checks anything out.
	gitRun(t, filepath.Dir(other), "clone", "-q", "-c", "filter.rot.clean="+rot13, "-c", "filter.rot.smudge="+rot13, remote, other)
	gitRun(t, other, "config", "user.email", "o@example.com")
	gitRun(t, other, "config", "user.name", "Other")
	writeFile(t, other, "T/over.md", "operator\n")
	gitRun(t, other, "commit", "-qam", "override")
	gitRun(t, other, "push", "-q", "origin", "main")
	gitRun(t, dir, "fetch", "-q", "origin")

	drivers, err := filterDrivers(dir)
	if err != nil {
		t.Fatal(err)
	}
	v := acceptOnly(nil, "mirror\n")
	var out PruneOutcome
	if !remoteAllows(dir, []string{"origin"}, "main", "T/wrap.md", drivers, v, &out) {
		t.Errorf("the remote's rot13-stored mirror was not recognised: %+v", out)
	}
	if remoteAllows(dir, []string{"origin"}, "main", "T/over.md", drivers, v, &out) {
		t.Error("the remote's operator content allowed the prune")
	}
	if len(out.Kept) != 1 || !strings.Contains(out.Kept[0].Reason, "holds operator content") {
		t.Errorf("kept = %+v", out.Kept)
	}

	// A filter that cannot run defers, never allows.
	gitRun(t, dir, "config", "filter.rot.smudge", "/nonexistent/vp-test-smudge")
	out = PruneOutcome{}
	if remoteAllows(dir, []string{"origin"}, "main", "T/wrap.md", drivers, v, &out) {
		t.Error("a broken filter allowed the prune")
	}
	if len(out.Errors) != 1 {
		t.Errorf("errors = %v", out.Errors)
	}
}

func TestReadCommittedContent(t *testing.T) {
	t.Run("unfiltered, trailing newlines exact", func(t *testing.T) {
		dir := committedRepo(t, map[string]string{"T/a.md": "a\n\n"})
		if b, found, err := ReadCommittedContent(dir, "T/a.md"); err != nil || !found || string(b) != "a\n\n" {
			t.Errorf("got %q found=%v err=%v", b, found, err)
		}
	})
	t.Run("absent", func(t *testing.T) {
		dir := committedRepo(t, map[string]string{"T/a.md": "a\n"})
		if b, found, err := ReadCommittedContent(dir, "T/none.md"); err != nil || found || b != nil {
			t.Errorf("got %q found=%v err=%v", b, found, err)
		}
	})
	t.Run("filtered", func(t *testing.T) {
		dir := rotRepo(t, map[string]string{"T/a.md": "Plain\n"})
		if b, found, err := ReadCommittedContent(dir, "T/a.md"); err != nil || !found || string(b) != "Plain\n" {
			t.Errorf("got %q found=%v err=%v", b, found, err)
		}
	})
	t.Run("broken filter", func(t *testing.T) {
		dir := rotRepo(t, map[string]string{"T/a.md": "Plain\n"})
		gitRun(t, dir, "config", "filter.rot.smudge", "/nonexistent/vp-test-smudge")
		if b, found, err := ReadCommittedContent(dir, "T/a.md"); err == nil || found {
			t.Errorf("got %q found=%v err=%v", b, found, err)
		}
	})
	t.Run("git fault", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /nonexistent\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, found, err := ReadCommittedContent(dir, "T/a.md"); err == nil || found {
			t.Errorf("found=%v err=%v", found, err)
		}
	})
	t.Run("filter listing fails", func(t *testing.T) {
		dir := committedRepo(t, map[string]string{"T/a.md": "a\n"})
		gitRun(t, dir, "config", "filter.a=b.smudge", "cat")
		if _, found, err := ReadCommittedContent(dir, "T/a.md"); err == nil || found {
			t.Errorf("found=%v err=%v", found, err)
		}
	})
	// A vault one level below its repository's root, with an attribute
	// anchored at that root: `HEAD:T/a.md` would name the wrong path, and
	// --path= would return the cleaned bytes with exit 0.
	t.Run("nested vault, root-anchored attribute", func(t *testing.T) {
		needFilterTools(t)
		root := initTestRepo(t)
		configureRot(t, root)
		writeFile(t, root, ".gitattributes", "/vault/T/** filter=rot\n")
		writeFile(t, root, "vault/T/a.md", "Plain\n")
		gitRun(t, root, "add", "-A")
		gitRun(t, root, "commit", "-m", "nested")
		vault := filepath.Join(root, "vault")
		if raw := rawBlob(t, vault, "T/a.md"); raw != "Cynva\n" {
			t.Fatalf("fixture: HEAD should store the rot13 form, got %q", raw)
		}
		if b, found, err := ReadCommittedContent(vault, "T/a.md"); err != nil || !found || string(b) != "Plain\n" {
			t.Errorf("got %q found=%v err=%v", b, found, err)
		}
	})
}
