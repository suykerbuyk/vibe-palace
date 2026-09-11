// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// gitShim puts a `git` first on PATH that runs the real git and, for the
// argument named (a subcommand, or a flag only one call passes), also writes stderr (and, with fail, exits 128 instead of
// running it). It is how a test makes one git call noisy or broken while every
// other call behaves.
func gitShim(t *testing.T, sub, stderr string, fail bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the git shim is a POSIX shell script")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	action := `exec "` + real + `" "$@"`
	if fail {
		action = "exit 128"
	}
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = \"" + sub + "\" ]; then\n    printf '" + stderr + "' >&2\n    " +
		action + "\n  fi\ndone\nexec \"" + real + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func keysOf(m map[string][]byte) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func rm(t *testing.T, dir, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

func TestUncommittedRemovals(t *testing.T) {
	t.Run("only unstaged removals HEAD and the index agree on", func(t *testing.T) {
		dir := committedRepo(t, map[string]string{
			"T/gone.md":     "gone\n\n",
			"T/staged.md":   "staged\n",
			"T/au.md":       "au\n",
			"T/sw.md":       "sw\n",
			"T/md.md":       "md\n",
			"T/kept.md":     "kept\n",
			"Other/gone.md": "outside dir\n",
		})
		rm(t, dir, "T/gone.md")
		rm(t, dir, "Other/gone.md")
		gitRun(t, dir, "rm", "-q", "T/staged.md") // staged deletion
		gitRun(t, dir, "update-index", "--assume-unchanged", "T/au.md")
		rm(t, dir, "T/au.md")
		gitRun(t, dir, "update-index", "--skip-worktree", "T/sw.md")
		rm(t, dir, "T/sw.md")
		writeFile(t, dir, "T/md.md", "staged modification\n") // MD: staged change, then removed
		gitRun(t, dir, "add", "T/md.md")
		rm(t, dir, "T/md.md")
		writeFile(t, dir, "T/new.md", "index only\n") // in the index, never in HEAD
		gitRun(t, dir, "add", "T/new.md")
		rm(t, dir, "T/new.md")

		got, err := UncommittedRemovals(dir, "T", nil)
		if err != nil {
			t.Fatal(err)
		}
		if want := map[string][]byte{"T/gone.md": []byte("gone\n\n")}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("HEAD's copy in checkout form", func(t *testing.T) {
		dir := rotRepo(t, map[string]string{"T/wrap.md": "Mirror\n"})
		rm(t, dir, "T/wrap.md")
		got, err := UncommittedRemovals(dir, "T", nil)
		if err != nil {
			t.Fatal(err)
		}
		if string(got["T/wrap.md"]) != "Mirror\n" || len(got) != 1 {
			t.Errorf("got %q, want the smudged bytes", got)
		}
	})

	t.Run("want applies before any read", func(t *testing.T) {
		needFilterTools(t)
		dir := initTestRepo(t)
		configureRot(t, dir)
		writeFile(t, dir, ".gitattributes", "T/broken/** filter=rot\n")
		writeFile(t, dir, "T/broken/x.md", "filtered\n")
		writeFile(t, dir, "T/a.md", "plain\n")
		gitRun(t, dir, "add", "-A")
		gitRun(t, dir, "commit", "-m", "x")
		gitRun(t, dir, "config", "filter.rot.smudge", "/nonexistent/vp-test-smudge")
		rm(t, dir, "T/broken/x.md")
		rm(t, dir, "T/a.md")

		if _, err := UncommittedRemovals(dir, "T", nil); err == nil {
			t.Fatal("fixture: reading the broken-filter path should fail the list")
		}
		got, err := UncommittedRemovals(dir, "T", func(rel string) bool { return rel != "T/broken/x.md" })
		if err != nil {
			t.Fatalf("a path the caller never acts on failed the list: %v", err)
		}
		if want := []string{"T/a.md"}; !reflect.DeepEqual(keysOf(got), want) {
			t.Errorf("keys = %v, want %v", keysOf(got), want)
		}
	})

	t.Run("stderr noise is never a path", func(t *testing.T) {
		dir := committedRepo(t, map[string]string{"T/gone.md": "gone\n", "T/present.md": "present\n"})
		rm(t, dir, "T/gone.md")
		gitShim(t, "--deleted", `R T/present.md\0hint: sparse index noise\n`, false)
		got, err := UncommittedRemovals(dir, "T", nil)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"T/gone.md"}; !reflect.DeepEqual(keysOf(got), want) {
			t.Errorf("keys = %v, want %v", keysOf(got), want)
		}
	})

	t.Run("nested vault keys are vault-relative", func(t *testing.T) {
		root := committedRepo(t, map[string]string{"vault/T/x.md": "x\n", "T/x.md": "outside the vault\n"})
		vault := filepath.Join(root, "vault")
		rm(t, vault, "T/x.md")
		rm(t, root, "T/x.md")
		got, err := UncommittedRemovals(vault, "T", nil)
		if err != nil {
			t.Fatal(err)
		}
		if want := map[string][]byte{"T/x.md": []byte("x\n")}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("unborn HEAD", func(t *testing.T) {
		if !GitAvailable() {
			t.Skip("git not in PATH")
		}
		dir := t.TempDir()
		gitRun(t, dir, "init", "-q", "-b", "main")
		writeFile(t, dir, "T/a.md", "a\n")
		gitRun(t, dir, "add", "-A")
		rm(t, dir, "T/a.md")
		got, err := UncommittedRemovals(dir, "T", nil)
		if err != nil || len(got) != 0 {
			t.Errorf("got %q err=%v, want empty", got, err)
		}
	})

	t.Run("nothing pending", func(t *testing.T) {
		dir := committedRepo(t, map[string]string{"T/a.md": "a\n"})
		got, err := UncommittedRemovals(dir, "T", nil)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("got %v err=%v, want an empty, non-nil map", got, err)
		}
	})

	t.Run("a corrupt index is an error", func(t *testing.T) {
		dir := committedRepo(t, map[string]string{"T/a.md": "a\n"})
		if err := os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("not an index"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := UncommittedRemovals(dir, "T", nil); err == nil {
			t.Errorf("got %v with no error", got)
		}
	})

	t.Run("a git fault after listing is an error", func(t *testing.T) {
		dir := committedRepo(t, map[string]string{"T/a.md": "a\n"})
		rm(t, dir, "T/a.md")
		gitShim(t, "ls-tree", `fatal: simulated\n`, true)
		if got, err := UncommittedRemovals(dir, "T", nil); err == nil {
			t.Errorf("got %v with no error", got)
		}
	})
}

func TestRetiredTemplatesLock(t *testing.T) {
	const lockBody = "[entries]\n"
	writeLock := func(t *testing.T, dir string) {
		t.Helper()
		writeFile(t, dir, RetiredTemplatesLockRel, lockBody)
	}
	removable := func(t *testing.T, dir string, want bool) {
		t.Helper()
		content, ok, err := RetiredTemplatesLock(dir)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if ok != want {
			t.Errorf("removable = %v, want %v", ok, want)
		}
		if ok && string(content) != lockBody {
			t.Errorf("content = %q", content)
		}
	}

	t.Run("untracked and not ignored", func(t *testing.T) {
		dir := initTestRepo(t)
		writeLock(t, dir)
		removable(t, dir, true)
	})
	t.Run("absent", func(t *testing.T) {
		dir := initTestRepo(t)
		content, ok, err := RetiredTemplatesLock(dir)
		if err != nil || ok || content != nil {
			t.Errorf("content=%q ok=%v err=%v", content, ok, err)
		}
	})
	t.Run("tracked", func(t *testing.T) {
		dir := initTestRepo(t)
		writeLock(t, dir)
		gitRun(t, dir, "add", "-A")
		gitRun(t, dir, "commit", "-m", "track the lock")
		removable(t, dir, false)
	})
	t.Run("removed from the index but still in HEAD", func(t *testing.T) {
		dir := initTestRepo(t)
		writeLock(t, dir)
		gitRun(t, dir, "add", "-A")
		gitRun(t, dir, "commit", "-m", "track the lock")
		gitRun(t, dir, "rm", "-q", "--cached", RetiredTemplatesLockRel)
		removable(t, dir, false)
	})
	t.Run("staged, never committed", func(t *testing.T) {
		dir := initTestRepo(t)
		writeLock(t, dir)
		gitRun(t, dir, "add", "-A")
		removable(t, dir, false)
	})
	t.Run("ignored", func(t *testing.T) {
		dir := initTestRepo(t)
		writeFile(t, dir, ".gitignore", ".vibe-palace/\n")
		writeLock(t, dir)
		removable(t, dir, false)
	})
	t.Run("unborn HEAD", func(t *testing.T) {
		if !GitAvailable() {
			t.Skip("git not in PATH")
		}
		dir := t.TempDir()
		gitRun(t, dir, "init", "-q", "-b", "main")
		writeLock(t, dir)
		removable(t, dir, true)
	})
	t.Run("a directory where the lock belongs", func(t *testing.T) {
		dir := initTestRepo(t)
		if err := os.MkdirAll(filepath.Join(dir, ".vibe-palace", "templates.lock"), 0o755); err != nil {
			t.Fatal(err)
		}
		removable(t, dir, false)
	})
	t.Run("reached through a symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need a privilege on Windows")
		}
		dir := initTestRepo(t)
		writeFile(t, dir, "elsewhere/templates.lock", lockBody)
		if err := os.MkdirAll(filepath.Join(dir, ".vibe-palace"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "elsewhere", "templates.lock"), filepath.Join(dir, ".vibe-palace", "templates.lock")); err != nil {
			t.Fatal(err)
		}
		removable(t, dir, false)
	})
	errCase := func(t *testing.T, dir string) {
		t.Helper()
		_, ok, err := RetiredTemplatesLock(dir)
		if err == nil || ok {
			t.Errorf("ok=%v err=%v, want an error and never removable", ok, err)
		}
	}
	t.Run("a corrupt index is an error", func(t *testing.T) {
		dir := initTestRepo(t)
		writeLock(t, dir)
		if err := os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("not an index"), 0o644); err != nil {
			t.Fatal(err)
		}
		errCase(t, dir)
	})
	t.Run("check-ignore failing is an error", func(t *testing.T) {
		dir := initTestRepo(t)
		writeLock(t, dir)
		gitShim(t, "check-ignore", `fatal: simulated\n`, true)
		errCase(t, dir)
	})
	t.Run("ls-tree failing is an error", func(t *testing.T) {
		dir := initTestRepo(t)
		writeLock(t, dir)
		gitShim(t, "ls-tree", `fatal: simulated\n`, true)
		errCase(t, dir)
	})
	t.Run("rev-parse failing is an error", func(t *testing.T) {
		dir := initTestRepo(t)
		writeLock(t, dir)
		gitShim(t, "rev-parse", `fatal: simulated\n`, true)
		errCase(t, dir)
	})
	t.Run("an unresolvable vault is an error", func(t *testing.T) {
		errCase(t, filepath.Join(t.TempDir(), "gone"))
	})
	t.Run("an unreadable lock is an error", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("unreadable-file fixture needs a non-root POSIX user")
		}
		dir := initTestRepo(t)
		writeLock(t, dir)
		p := filepath.Join(dir, ".vibe-palace", "templates.lock")
		if err := os.Chmod(p, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
		errCase(t, dir)
	})
}

func TestHeadExists(t *testing.T) {
	dir := initTestRepo(t)
	if born, err := headExists(dir); err != nil || !born {
		t.Errorf("born repo: %v %v", born, err)
	}
	unborn := t.TempDir()
	gitRun(t, unborn, "init", "-q", "-b", "main")
	if born, err := headExists(unborn); err != nil || born {
		t.Errorf("unborn repo: %v %v", born, err)
	}
	notRepo := t.TempDir()
	if err := os.WriteFile(filepath.Join(notRepo, ".git"), []byte("gitdir: /nonexistent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if born, err := headExists(notRepo); err == nil || born {
		t.Errorf("broken repo: %v %v", born, err)
	}
	if !strings.Contains(RetiredTemplatesLockRel, "templates.lock") {
		t.Error(RetiredTemplatesLockRel)
	}
}
