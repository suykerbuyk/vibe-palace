// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// initEnv isolates HOME and git config, with a committer identity and a host
// init.defaultBranch of master, so the test proves vault init still lands on
// main.
func initEnv(t *testing.T) {
	t.Helper()
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
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

// testScaffold is the vault scaffold the tests use: a stand-in for
// reconcile.ScaffoldNewVault (which this package cannot import) with the same
// outputs — directory, git init, .gitignore, data-format stamp. The real
// scaffold is exercised by the cmd/vp test.
func testScaffold(_ context.Context, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	if err := GitInit(dest); err != nil {
		return err
	}
	if err := ReconcileVaultGitignore(dest); err != nil {
		return err
	}
	return surface.WriteFormat(dest, surface.RequiredDataFormat)
}

func twoEmptyRemotes(t *testing.T) (origin, github string) {
	t.Helper()
	return initBareRemote(t), initBareRemote(t)
}

func initReq(path, origin, github string) InitVaultRequest {
	return InitVaultRequest{
		Path:     path,
		Remotes:  []RecordedRemote{{Name: "origin", URL: fileURL(origin)}, {Name: "github", URL: fileURL(github)}},
		Scaffold: testScaffold,
	}
}

func bareHasRefs(t *testing.T, bare string) bool {
	t.Helper()
	return gitRun(t, bare, "for-each-ref") != ""
}

// Admin step 1 of § Admin procedure, against two file:// bare remotes.
func TestInitVault_AdminStep1TwoRemotes(t *testing.T) {
	initEnv(t)
	origin, github := twoEmptyRemotes(t)
	path := filepath.Join(t.TempDir(), "quantum-vibe-palace-vault")

	rep, err := InitVault(context.Background(), initReq(path, origin, github))
	if err != nil {
		t.Fatal(err)
	}
	if b := gitRun(t, path, "rev-parse", "--abbrev-ref", "HEAD"); b != "main" {
		t.Fatalf("branch = %s, want main (host default is master)", b)
	}
	if n := gitRun(t, path, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("%s commits, want exactly one", n)
	}
	files := strings.Split(gitRun(t, path, "ls-files"), "\n")
	slices.Sort(files)
	if want := []string{".gitignore", ".vibe-palace/remotes.toml", ".vibe-palace/vault.toml", "Audits/.surface"}; !slices.Equal(files, want) {
		t.Fatalf("tracked = %v, want %v", files, want)
	}
	if f, _ := surface.ReadFormat(path); f != surface.RequiredDataFormat {
		t.Fatalf("data format %d, want %d", f, surface.RequiredDataFormat)
	}
	rt, _ := os.ReadFile(filepath.Join(path, ".vibe-palace", "remotes.toml"))
	if i, j := strings.Index(string(rt), `name = "origin"`), strings.Index(string(rt), `name = "github"`); i < 0 || j < i {
		t.Fatalf("remotes.toml does not record origin then github:\n%s", rt)
	}
	order, err := lifecycleRemoteOrder(path)
	if err != nil || !slices.Equal(order, []string{"origin", "github"}) {
		t.Fatalf("recorded order = %v, %v", order, err)
	}
	for name, bare := range map[string]string{"origin": origin, "github": github} {
		if got := gitRun(t, bare, "rev-parse", "main"); got != rep.Commit {
			t.Fatalf("%s main = %s, want the init commit %s", name, got, rep.Commit)
		}
	}
	if up := gitRun(t, path, "rev-parse", "--abbrev-ref", "main@{upstream}"); up != "origin/main" {
		t.Fatalf("upstream = %s, want origin/main", up)
	}
	if gitRun(t, path, "rev-parse", "github/main") != rep.Commit {
		t.Fatal("github tracking ref missing")
	}
	if st := gitRun(t, path, "status", "--porcelain"); st != "" {
		t.Fatalf("vault not clean: %s", st)
	}
	// The vault is ready for the next command: HEAD equals every remote tip.
	h := lockDir(t, path)
	if _, err := requireHeadAtEveryRemote(h, "main", order); err != nil {
		t.Fatalf("not at ahead 0 after init: %v", err)
	}
}

func TestInitVault_DryRunWritesNothing(t *testing.T) {
	initEnv(t)
	origin, github := twoEmptyRemotes(t)
	path := filepath.Join(t.TempDir(), "v")
	req := initReq(path, origin, github)
	req.DryRun = true
	rep, err := InitVault(context.Background(), req)
	if err != nil || !rep.DryRun {
		t.Fatalf("dry run: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("dry run created the path")
	}
	if bareHasRefs(t, origin) || bareHasRefs(t, github) {
		t.Fatal("dry run published")
	}
}

// Each refusal writes nothing: no path, and no remote gains a ref.
func TestInitVault_Refusals(t *testing.T) {
	initEnv(t)
	cases := []struct {
		name  string
		setup func(t *testing.T) InitVaultRequest
		want  error
	}{
		{"existing empty dir", func(t *testing.T) InitVaultRequest {
			o, g := twoEmptyRemotes(t)
			p := filepath.Join(t.TempDir(), "v")
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatal(err)
			}
			return initReq(p, o, g)
		}, ErrInitPathExists},
		{"inside a vault", func(t *testing.T) InitVaultRequest {
			o, g := twoEmptyRemotes(t)
			outer := t.TempDir()
			if err := surface.WriteFormat(outer, surface.RequiredDataFormat); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(outer, "Projects"), 0o755); err != nil {
				t.Fatal(err)
			}
			return initReq(filepath.Join(outer, "Projects", "v"), o, g)
		}, ErrInitNested},
		{"inside a git work tree", func(t *testing.T) InitVaultRequest {
			o, g := twoEmptyRemotes(t)
			repo := t.TempDir()
			gitRun(t, repo, "init", "-q")
			return initReq(filepath.Join(repo, "v"), o, g)
		}, ErrInitNested},
		{"non-empty remote", func(t *testing.T) InitVaultRequest {
			o, g := twoEmptyRemotes(t)
			pushFromOtherHostInit(t, g)
			return initReq(filepath.Join(t.TempDir(), "v"), o, g)
		}, ErrInitRemoteNotEmpty},
		{"unreachable remote", func(t *testing.T) InitVaultRequest {
			o, _ := twoEmptyRemotes(t)
			return initReq(filepath.Join(t.TempDir(), "v"), o, filepath.Join(t.TempDir(), "absent.git"))
		}, ErrInitRemoteUnreached},
		{"no remote", func(t *testing.T) InitVaultRequest {
			return InitVaultRequest{Path: filepath.Join(t.TempDir(), "v"), Scaffold: testScaffold}
		}, ErrInitNoRemote},
		{"repeated name", func(t *testing.T) InitVaultRequest {
			o, g := twoEmptyRemotes(t)
			r := initReq(filepath.Join(t.TempDir(), "v"), o, g)
			r.Remotes[1].Name = "origin"
			return r
		}, ErrInitRemoteBad},
		{"same repository twice", func(t *testing.T) InitVaultRequest {
			o, _ := twoEmptyRemotes(t)
			r := initReq(filepath.Join(t.TempDir(), "v"), o, o)
			return r
		}, ErrInitRemoteBad},
		{"bad name", func(t *testing.T) InitVaultRequest {
			o, g := twoEmptyRemotes(t)
			r := initReq(filepath.Join(t.TempDir(), "v"), o, g)
			r.Remotes[0].Name = "-x"
			return r
		}, ErrInitRemoteBad},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.setup(t)
			_, statErr := os.Lstat(req.Path)
			existed := statErr == nil
			_, err := InitVault(context.Background(), req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if !existed {
				if _, err := os.Lstat(req.Path); !os.IsNotExist(err) {
					t.Fatal("a refusal created the path")
				}
			}
			for _, r := range req.Remotes {
				bare := strings.TrimPrefix(r.URL, "file://")
				if tc.want != ErrInitRemoteNotEmpty && tc.want != ErrInitRemoteUnreached {
					if st, err := os.Stat(bare); err == nil && st.IsDir() && bareHasRefs(t, bare) {
						t.Fatalf("a refusal published to %s", r.Name)
					}
				}
			}
		})
	}
	if _, err := InitVault(context.Background(), InitVaultRequest{Path: "relative/v", Remotes: []RecordedRemote{{Name: "o", URL: "file:///x"}}}); err == nil {
		t.Fatal("a relative path was accepted")
	}
}

// pushFromOtherHostInit gives bare a history, as a remote that is already
// somebody's vault would have.
func pushFromOtherHostInit(t *testing.T, bare string) {
	t.Helper()
	other := t.TempDir()
	gitRun(t, other, "init", "-q", "-b", "main")
	writeFile(t, other, "x.md", "someone else's\n")
	gitRun(t, other, "add", "-A")
	gitRun(t, other, "commit", "-q", "-m", "history")
	gitRun(t, other, "push", "-q", fileURL(bare), "main")
}

// The first remote refuses the commit: nothing is published anywhere, so the
// new vault is removed again.
func TestInitVault_FirstPushFailsRemovesTheNewVault(t *testing.T) {
	initEnv(t)
	origin, github := twoEmptyRemotes(t)
	path := filepath.Join(t.TempDir(), "v")
	withPush(t, func(vp, r, sha, b string) error { return errors.New("connection refused") })
	if _, err := InitVault(context.Background(), initReq(path, origin, github)); err == nil {
		t.Fatal("no error")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("an unpublished new vault was left behind")
	}
	if bareHasRefs(t, origin) || bareHasRefs(t, github) {
		t.Fatal("something was published")
	}
}

// The first remote took the commit, a later one did not: the vault stays,
// the report names only the remote that holds it, and re-running the same
// command finishes the publish.
func TestInitVault_LaterPushFailsThenReRunFinishes(t *testing.T) {
	initEnv(t)
	origin, github := twoEmptyRemotes(t)
	path := filepath.Join(t.TempDir(), "v")
	withPush(t, func(vp, r, sha, b string) error {
		if r == "github" {
			return errors.New("ssh: connection timed out")
		}
		return realPush(vp, r, sha, b)
	})
	req := initReq(path, origin, github)
	rep, err := InitVault(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "re-run the same command") {
		t.Fatalf("err = %v, want the re-run instruction", err)
	}
	if rep == nil || !slices.Equal(rep.PublishedTo, []string{"origin"}) {
		t.Fatalf("PublishedTo = %v, want only origin", rep.PublishedTo)
	}
	commit := rep.Commit
	withPush(t, realPush)
	rep2, err := InitVault(context.Background(), req)
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if !rep2.Resumed || gitRun(t, github, "rev-parse", "main") != commit || markerFound(t, path) {
		t.Fatalf("resumed=%v github=%s marker=%v", rep2.Resumed, gitRun(t, github, "rev-parse", "main"), markerFound(t, path))
	}
	if up := gitRun(t, path, "rev-parse", "--abbrev-ref", "main@{upstream}"); up != "origin/main" {
		t.Fatalf("upstream = %s", up)
	}
}

// R1: kill -9 mid-publish — origin holds main, github is empty. The re-run
// (same command) finishes the publish to github with the same commit.
func TestInitVault_KilledMidPublishReRunFinishes(t *testing.T) {
	initEnv(t)
	origin, github := twoEmptyRemotes(t)
	path := filepath.Join(t.TempDir(), "v")
	seamInit(t, &initAfterCommit, func() error {
		gitRun(t, path, "push", "-q", "origin", "HEAD:refs/heads/main")
		return errKilledInit
	})
	req := initReq(path, origin, github)
	if _, err := InitVault(context.Background(), req); !errors.Is(err, errKilledInit) {
		t.Fatalf("err = %v", err)
	}
	commit := gitRun(t, path, "rev-parse", "HEAD")
	if gitRun(t, origin, "rev-parse", "main") != commit || bareHasRefs(t, github) {
		t.Fatal("fixture: want origin at the commit, github empty")
	}
	initAfterCommit = func() error { return nil }
	rep, err := InitVault(context.Background(), req)
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if !rep.Resumed || gitRun(t, github, "rev-parse", "main") != commit || markerFound(t, path) {
		t.Fatal("the re-run did not finish the same commit on github")
	}
}

// Killed after the commit, before any push: nothing is published, so the
// re-run removes the unpublished vault and starts over.
func TestInitVault_KilledBeforePushReRunStartsOver(t *testing.T) {
	initEnv(t)
	origin, github := twoEmptyRemotes(t)
	path := filepath.Join(t.TempDir(), "v")
	seamInit(t, &initAfterCommit, func() error { return errKilledInit })
	req := initReq(path, origin, github)
	if _, err := InitVault(context.Background(), req); !errors.Is(err, errKilledInit) {
		t.Fatalf("err = %v", err)
	}
	initAfterCommit = func() error { return nil }
	rep, err := InitVault(context.Background(), req)
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if gitRun(t, origin, "rev-parse", "main") != rep.Commit || gitRun(t, github, "rev-parse", "main") != rep.Commit {
		t.Fatal("the fresh run did not publish")
	}
}

// Killed right after the marker, before the commit: the re-run starts over.
func TestInitVault_KilledBeforeCommitReRunStartsOver(t *testing.T) {
	initEnv(t)
	origin, github := twoEmptyRemotes(t)
	path := filepath.Join(t.TempDir(), "v")
	seamInit(t, &initAfterMarker, func() error { return errKilledInit })
	req := initReq(path, origin, github)
	if _, err := InitVault(context.Background(), req); !errors.Is(err, errKilledInit) {
		t.Fatalf("err = %v", err)
	}
	initAfterMarker = func() error { return nil }
	if _, err := InitVault(context.Background(), req); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if !bareHasRefs(t, origin) || !bareHasRefs(t, github) {
		t.Fatal("the fresh run did not publish")
	}
}

// A re-run is recovery only for the SAME init: other remotes, or a directory
// with no init marker, refuse as an existing path.
func TestInitVault_ReRunRefusesAnythingButItsOwnInit(t *testing.T) {
	initEnv(t)
	origin, github := twoEmptyRemotes(t)
	path := filepath.Join(t.TempDir(), "v")
	seamInit(t, &initAfterCommit, func() error { return errKilledInit })
	if _, err := InitVault(context.Background(), initReq(path, origin, github)); !errors.Is(err, errKilledInit) {
		t.Fatalf("err = %v", err)
	}
	initAfterCommit = func() error { return nil }
	other := initBareRemote(t)
	if _, err := InitVault(context.Background(), initReq(path, origin, other)); !errors.Is(err, ErrInitPathExists) {
		t.Fatalf("different remotes: err = %v", err)
	}
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, plain, "init", "-q")
	if _, err := InitVault(context.Background(), initReq(plain, origin, github)); !errors.Is(err, ErrInitPathExists) {
		t.Fatalf("no marker: err = %v", err)
	}
}

// R2: a symlinked parent pointing into a vault is still "inside a vault".
func TestInitVault_SymlinkedParentIntoAVault(t *testing.T) {
	initEnv(t)
	origin, github := twoEmptyRemotes(t)
	outer := t.TempDir()
	if err := surface.WriteFormat(outer, surface.RequiredDataFormat); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(outer, "Projects")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "looks-outside")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := InitVault(context.Background(), initReq(filepath.Join(link, "v"), origin, github)); !errors.Is(err, ErrInitNested) {
		t.Fatalf("err = %v, want ErrInitNested", err)
	}
}

// Lstat, not Stat: a dangling symlink at the path is an existing path.
func TestInitVault_DanglingSymlinkIsAnExistingPath(t *testing.T) {
	initEnv(t)
	origin, github := twoEmptyRemotes(t)
	path := filepath.Join(t.TempDir(), "v")
	if err := os.Symlink(filepath.Join(t.TempDir(), "nowhere"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := InitVault(context.Background(), initReq(path, origin, github)); !errors.Is(err, ErrInitPathExists) {
		t.Fatalf("err = %v, want ErrInitPathExists", err)
	}
}

// A scaffold that does not stamp the data format is refused, and the new
// directory removed.
func TestInitVault_UnstampedScaffoldRefused(t *testing.T) {
	initEnv(t)
	origin, github := twoEmptyRemotes(t)
	path := filepath.Join(t.TempDir(), "v")
	req := initReq(path, origin, github)
	req.Scaffold = func(_ context.Context, dest string) error {
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		if err := GitInit(dest); err != nil {
			return err
		}
		return ReconcileVaultGitignore(dest)
	}
	if _, err := InitVault(context.Background(), req); err == nil || !strings.Contains(err.Error(), "did not stamp data format") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("the unstamped vault was left behind")
	}
	if bareHasRefs(t, origin) {
		t.Fatal("published")
	}
}

// URL checks: normalised duplicates, credentials (never echoed), control
// characters (all of them), and the userinfo allowed.
func TestInitVault_RemoteURLChecks(t *testing.T) {
	two := func(a, b string) []RecordedRemote {
		return []RecordedRemote{{Name: "origin", URL: a}, {Name: "github", URL: b}}
	}
	if err := initCheckRemotes(two("ssh://git@example.com/q/v.git", "git@example.com:q/v.git")); !errors.Is(err, ErrInitRemoteBad) {
		t.Fatalf("normalised duplicate accepted: %v", err)
	}
	for _, bad := range []string{
		"https://alice:secrettoken@example.com/q/v.git",
		"https://secrettoken@example.com/q/v.git",
		"ssh://alice@example.com/q/v.git",
		"ssh://git:secrettoken@example.com/q/v.git",
		"alice@example.com:q/v.git",
	} {
		err := initCheckRemotes([]RecordedRemote{{Name: "origin", URL: bad}})
		if !errors.Is(err, ErrInitRemoteBad) {
			t.Fatalf("%s accepted: %v", redactURL(bad), err)
		}
		if strings.Contains(err.Error(), "secrettoken") || strings.Contains(err.Error(), "alice") {
			t.Fatalf("the refusal leaks the credential: %v", err)
		}
	}
	for _, ok := range []string{"git@example.com:q/v.git", "ssh://git@example.com/q/v.git", "https://example.com/q/v.git", "file:///srv/v.git"} {
		if err := initCheckRemotes([]RecordedRemote{{Name: "origin", URL: ok}}); err != nil {
			t.Fatalf("%s refused: %v", ok, err)
		}
	}
	// Every control character, in all three URL shapes: a URL, scp-style,
	// and a bare path (the last two never reach net/url, which would reject
	// some on its own).
	for c := rune(0); c < 0x20; c++ {
		for _, u := range []string{"file:///srv/v" + string(c) + ".git", "git@example.com:q/v" + string(c) + ".git", "/srv/v" + string(c) + ".git"} {
			if err := initCheckRemotes([]RecordedRemote{{Name: "origin", URL: u}}); !errors.Is(err, ErrInitRemoteBad) {
				t.Fatalf("control character %#x accepted in %q", c, u)
			}
		}
	}
	if err := initCheckRemotes([]RecordedRemote{{Name: "origin", URL: `file:///srv/v\x7f.git`}}); err != nil {
		t.Fatalf("a backslash sequence is not a control character: %v", err)
	}
	for _, u := range []string{"file:///srv/v" + string(rune(0x7f)) + ".git", "git@example.com:q/v" + string(rune(0x7f)) + ".git", "/srv/v" + string(rune(0x7f)) + ".git"} {
		if err := initCheckRemotes([]RecordedRemote{{Name: "origin", URL: u}}); !errors.Is(err, ErrInitRemoteBad) {
			t.Fatalf("DEL accepted in %q", u)
		}
	}
}

// git_enabled = false refuses before anything is written.
func TestInitVault_RefusesWhenGitDisabled(t *testing.T) {
	initEnv(t)
	origin, github := twoEmptyRemotes(t)
	cfg, err := VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("git_enabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "v")
	if _, err := InitVault(context.Background(), initReq(path, origin, github)); !errors.Is(err, ErrGitDisabled) {
		t.Fatalf("err = %v, want ErrGitDisabled", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("created the path")
	}
}

var errKilledInit = errors.New("killed (test)")

func seamInit(t *testing.T, p *func() error, v func() error) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}
