// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fileURL is the file:// form of a local bare repository, so the tests use a
// real transport (and its filter negotiation) rather than git's local copy.
func fileURL(p string) string { return "file://" + filepath.ToSlash(p) }

// lcSource builds a vault-shaped repo with two projects and pushes it to a
// fresh bare remote. It returns the working clone and the bare remote.
func lcSource(t *testing.T) (dir, bare string) {
	t.Helper()
	dir = initTestRepo(t)
	bare = initBareRemote(t)
	gitRun(t, bare, "config", "uploadpack.allowFilter", "true")
	gitRun(t, bare, "config", "uploadpack.allowAnySHA1InWant", "true")
	writeFile(t, dir, "Projects/p/resume.md", "resume of p\n")
	writeFile(t, dir, "Projects/p/sessions/s1.md", "session 1\n")
	writeFile(t, dir, "Projects/p/.surface", "surface = 7\n")
	writeFile(t, dir, "palace/p/drawers.jsonl", "{}\n")
	writeFile(t, dir, "Projects/q/resume.md", "resume of q\n")
	writeFile(t, dir, "Projects/orch/resume.md", "projects-only\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "two projects")
	gitRun(t, dir, "remote", "add", "origin", fileURL(bare))
	gitRun(t, dir, "push", "-q", "origin", "main")
	return dir, bare
}

func TestLsRemoteHead_BranchAndTip(t *testing.T) {
	dir, bare := lcSource(t)
	h, err := lsRemoteHead(fileURL(bare))
	if err != nil {
		t.Fatal(err)
	}
	if h.Branch != "main" || h.Tip != gitRun(t, dir, "rev-parse", "HEAD") {
		t.Fatalf("got %+v", h)
	}
	empty := initBareRemote(t)
	if _, err := lsRemoteHead(fileURL(empty)); !errors.Is(err, ErrRemoteHasNoHead) {
		t.Fatalf("empty remote: err = %v, want ErrRemoteHasNoHead", err)
	}
	if _, err := lsRemoteHead(fileURL(filepath.Join(t.TempDir(), "absent.git"))); err == nil {
		t.Fatal("unreachable remote: no error")
	}
}

func TestRemoteSnapshot_FootprintOnlyCheckout(t *testing.T) {
	dir, bare := lcSource(t)
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	// An ignored file in the SOURCE working tree must never reach a snapshot.
	writeFile(t, dir, ".gitignore", "*.bak\n")
	writeFile(t, dir, "Projects/p/x.manifest.json.1.bak", "ignored\n")

	s, err := newRemoteSnapshot(fileURL(bare), "main")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s.Dir, filepath.Join(cache, "vibe-palace", "snap")) {
		t.Fatalf("snapshot %s is not under the user cache dir %s", s.Dir, cache)
	}
	if err := s.fetchBlobs(s.Tip, append(ProjectTrees("p"), ProjectTrees("orch")...)); err != nil {
		t.Fatal(err)
	}
	wt, err := s.checkoutFootprint(s.Tip, []string{"p", "orch"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	_ = filepath.WalkDir(wt, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(wt, p)
			got = append(got, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(got)
	want := []string{"Projects/orch/resume.md", "Projects/p/.surface", "Projects/p/resume.md", "Projects/p/sessions/s1.md", "palace/p/drawers.jsonl"}
	if !slices.Equal(got, want) {
		t.Fatalf("checkout = %v\nwant       %v", got, want)
	}
	root := filepath.Dir(s.Dir)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("Close left %s behind", root)
	}
}

// The copy commit sits on a merge's second parent, as `vp vault pull` leaves
// it when a host had a local commit. An all-ancestor walk finds it; a
// first-parent walk does not.
func TestCommitsWithTrailer_FindsCommitOffFirstParent(t *testing.T) {
	b := initTestRepo(t)
	base := gitRun(t, b, "rev-parse", "HEAD")
	gitRun(t, b, "checkout", "-q", "-b", "copy")
	writeFile(t, b, "Projects/p/resume.md", "p\n")
	gitRun(t, b, "add", "-A")
	gitRun(t, b, "commit", "-q", "-m", "vault copy: p\n\nVp-Copy-Project: p\nVp-Copy-Source: git@example.com:a/v.git\nVp-Copy-Footprint: p v1:abc")
	copySHA := gitRun(t, b, "rev-parse", "HEAD")
	gitRun(t, b, "checkout", "-q", "main")
	gitRun(t, b, "reset", "-q", "--hard", base)
	writeFile(t, b, "Projects/other/x.md", "host-local work\n")
	gitRun(t, b, "add", "-A")
	gitRun(t, b, "commit", "-q", "-m", "host local")
	gitRun(t, b, "merge", "-q", "--no-edit", "copy")
	if fp := gitRun(t, b, "log", "--first-parent", "--format=%H"); strings.Contains(fp, copySHA) {
		t.Fatal("fixture is wrong: the copy commit is on the first-parent line")
	}
	got, err := commitsWithTrailer(b, "HEAD", "Vp-Copy-Project")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SHA != copySHA {
		t.Fatalf("got %+v, want only %s", got, copySHA)
	}
	tr := got[0].Trailers
	if tr["Vp-Copy-Project"][0] != "p" || tr["Vp-Copy-Source"][0] != "git@example.com:a/v.git" || tr["Vp-Copy-Footprint"][0] != "p v1:abc" {
		t.Fatalf("trailers = %v", tr)
	}
}

func TestFootprintHash(t *testing.T) {
	a, _ := lcSource(t)
	fa, n, err := footprintHash(a, "HEAD", "p")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fa, "v1:") || n != 3 {
		t.Fatalf("F = %s over %d files, want v1: over 3 (.surface excluded)", fa, n)
	}
	// .surface and a retired config.toml are not content: changing them
	// leaves F alone.
	writeFile(t, a, "Projects/p/.surface", "surface = 8\n")
	writeFile(t, a, "Projects/p/config.toml", "x = 1\n")
	gitRun(t, a, "add", "-A")
	gitRun(t, a, "commit", "-q", "-m", "non-content")
	if f2, _, _ := footprintHash(a, "HEAD", "p"); f2 != fa {
		t.Fatalf("non-content change moved F: %s -> %s", fa, f2)
	}
	// Another vault holding the same bytes at the same paths, on a different
	// history, has the same F; a rebase changes shas, not blob oids.
	b := initTestRepo(t)
	writeFile(t, b, "unrelated.md", "other history\n")
	writeFile(t, b, "Projects/p/resume.md", "resume of p\n")
	writeFile(t, b, "Projects/p/sessions/s1.md", "session 1\n")
	writeFile(t, b, "palace/p/drawers.jsonl", "{}\n")
	gitRun(t, b, "add", "-A")
	gitRun(t, b, "commit", "-q", "-m", "same bytes")
	if fb, _, _ := footprintHash(b, "HEAD", "p"); fb != fa {
		t.Fatalf("same bytes, different F: %s vs %s", fa, fb)
	}
	// A content change moves F.
	writeFile(t, b, "Projects/p/sessions/s1.md", "session 1, edited\n")
	gitRun(t, b, "commit", "-q", "-am", "edit")
	if fb, _, _ := footprintHash(b, "HEAD", "p"); fb == fa {
		t.Fatal("a content change did not move F")
	}
	// commit-log.anchor names a project-repo commit and travels with the
	// project (U1), so it is content: changing it moves F.
	writeFile(t, b, "Projects/p/commit-log.anchor", "9b6da95804608aeb80a7dc3ec22f7a79ba01efc8\n")
	gitRun(t, b, "add", "-A")
	gitRun(t, b, "commit", "-q", "-m", "anchor")
	fAnchor, nAnchor, _ := footprintHash(b, "HEAD", "p")
	writeFile(t, b, "Projects/p/commit-log.anchor", "0000000000000000000000000000000000000001\n")
	gitRun(t, b, "commit", "-q", "-am", "anchor moves")
	if fMoved, _, _ := footprintHash(b, "HEAD", "p"); fMoved == fAnchor {
		t.Fatal("a commit-log.anchor change did not move F")
	}
	if nAnchor != 4 {
		t.Fatalf("anchor not counted as content: %d content files, want 4", nAnchor)
	}
	// A project that is absent, or has only non-content files, has no
	// content files: the caller refuses it.
	if _, n, _ := footprintHash(b, "HEAD", "absent"); n != 0 {
		t.Fatalf("absent project has %d content files", n)
	}
}

// The operator's own ssh command, key included, reaches every lifecycle git
// call; BatchMode is appended to it.
func TestLifecycleGitEnv_KeepsOperatorSSHCommand(t *testing.T) {
	dir := initTestRepo(t)
	op := "ssh -i /home/u/.ssh/id_mdh -o IdentitiesOnly=yes -o IdentityAgent=none"
	t.Setenv("GIT_SSH_COMMAND", op)
	extra, pre := lifecycleGitExtra(dir)
	env := SafeGitEnv(extra...)
	var got string // os/exec keeps the last value of a duplicated key
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GIT_SSH_COMMAND="); ok {
			got = v
		}
	}
	if got != op+" -o BatchMode=yes" || len(pre) != 0 {
		t.Fatalf("GIT_SSH_COMMAND = %q, pre = %v", got, pre)
	}
	if !slices.Contains(env, "GIT_TERMINAL_PROMPT=0") {
		t.Fatal("GIT_TERMINAL_PROMPT=0 missing")
	}
}

// End to end: the ssh a real lifecycle git call spawns receives the
// operator's key AND BatchMode. A fake ssh records its argv and fails.
func TestLifecycleGit_SSHSeesOperatorKeyAndBatchMode(t *testing.T) {
	initTestRepo(t) // isolates HOME and git config
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	fake := filepath.Join(dir, "fake-ssh")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argv + "\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", fake+" -i /home/u/.ssh/id_mdh -o IdentitiesOnly=yes")
	if _, err := lsRemoteHead("ssh://git@example.invalid/vault.git"); err == nil {
		t.Fatal("fake ssh fails, so ls-remote must fail")
	}
	data, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("fake ssh never ran: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, want := range []string{"/home/u/.ssh/id_mdh", "IdentitiesOnly=yes", "BatchMode=yes"} {
		if !slices.Contains(got, want) {
			t.Fatalf("ssh argv %q lacks %q", got, want)
		}
	}
}

// GIT_SSH (a program) without GIT_SSH_COMMAND gets no BatchMode, so the
// refusal says so and names the fix.
func TestLifecycleGit_GitSSHCaseNamedInRefusal(t *testing.T) {
	initTestRepo(t)
	fake := filepath.Join(t.TempDir(), "plink-like")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", "")
	os.Unsetenv("GIT_SSH_COMMAND")
	t.Setenv("GIT_SSH", fake)
	_, err := lsRemoteHead("ssh://git@example.invalid/vault.git")
	if err == nil || !strings.Contains(err.Error(), "GIT_SSH is set") || !strings.Contains(err.Error(), "GIT_SSH_COMMAND=") {
		t.Fatalf("err = %v, want the GIT_SSH case named with the fix", err)
	}
}
