// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestGitRemotes(t *testing.T) {
	// Create a temp git repo.
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("remote", "add", "origin", "https://example.com/repo.git")
	run("remote", "add", "backup", "https://backup.example.com/repo.git")

	remotes, err := gitRemotes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(remotes) != 2 {
		t.Errorf("expected 2 remotes, got %d: %v", len(remotes), remotes)
	}
}

func TestGitRemotesNoRemotes(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	_, err := gitRemotes(dir)
	if err == nil {
		t.Error("expected error for no remotes")
	}
}

func TestPullAllDryRun(t *testing.T) {
	code := pullAll("/nonexistent", []string{"origin"}, true)
	if code != cli.ExitOK {
		t.Errorf("dry run should succeed: exit code = %d", code)
	}
}

func TestPushAllDirtyState(t *testing.T) {
	// Create a git repo with uncommitted changes.
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")

	// Create a file and add it but don't commit → dirty state.
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644)
	run("add", "file.txt")

	code := pushAll(dir, []string{"origin"}, false)
	if code != cli.ExitUser {
		t.Errorf("expected ExitUser for dirty state, got %d", code)
	}
}

func TestPushAllDryRun(t *testing.T) {
	// Create clean git repo.
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644)
	run("add", "file.txt")
	run("commit", "-m", "init")

	code := pushAll(dir, []string{"origin"}, true)
	if code != cli.ExitOK {
		t.Errorf("dry run should succeed: exit code = %d", code)
	}
}

func TestVaultPullBadFlags(t *testing.T) {
	cmd := cmdVaultPull()
	code := cmd.Run([]string{"--unknown"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

func TestVaultPushBadFlags(t *testing.T) {
	cmd := cmdVaultPush()
	code := cmd.Run([]string{"--unknown"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

func TestVaultSyncBadFlags(t *testing.T) {
	cmd := cmdVaultSync()
	code := cmd.Run([]string{"--unknown"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

func TestVaultTidyBadFlags(t *testing.T) {
	cmd := cmdVaultTidy()
	code := cmd.Run([]string{"--unknown"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

// TestVaultTidyDryRun verifies --dry-run prints the sweep/report split, makes
// clear nothing was committed, and creates no commit.
func TestVaultTidyDryRun(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", vaultDir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	os.WriteFile(filepath.Join(vaultDir, "seed.txt"), []byte("seed"), 0o644)
	run("add", "seed.txt")
	run("commit", "-m", "seed")

	headBefore := gitHead(t, vaultDir)

	// One sweepable artifact, one piece of non-artifact dirt.
	mkfile(t, vaultDir, "Projects/vibe-palace/sessions/2026-06-17.md", "session\n")
	mkfile(t, vaultDir, "Projects/vibe-palace/resume.md", "resume\n")

	var code int
	out := captureStdout(t, func() {
		code = cmdVaultTidy().Run([]string{"--dry-run"})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitOK)
	}
	for _, want := range []string{
		"Would sweep",
		"Projects/vibe-palace/sessions/2026-06-17.md",
		"Reported",
		"Projects/vibe-palace/resume.md",
		"nothing was committed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q; got:\n%s", want, out)
		}
	}
	if headAfter := gitHead(t, vaultDir); headAfter != headBefore {
		t.Errorf("dry-run created a commit: HEAD %s -> %s", headBefore, headAfter)
	}
}

// gitHead returns the current HEAD commit hash of dir.
func gitHead(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// mkfile writes rel (creating parent dirs) under dir.
func mkfile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVaultStatusBadFlags(t *testing.T) {
	cmd := cmdVaultStatus()
	code := cmd.Run([]string{"--unknown"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

// setupVaultWithOrigin builds an isolated temp vault wired to a bare origin and
// pushed in sync (origin/main tracking ref present). Returns the vault root.
func setupVaultWithOrigin(t *testing.T) string {
	t.Helper()
	vaultDir := setupTestVaultEnv(t)
	bare := t.TempDir()

	gitEnv := append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	bareRun := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", bare}, args...)...)
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", vaultDir}, args...)...)
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	bareRun("init", "--bare", "-b", "main")
	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	run("remote", "add", "origin", bare)
	os.WriteFile(filepath.Join(vaultDir, "seed.txt"), []byte("seed\n"), 0o644)
	run("add", "seed.txt")
	run("commit", "-m", "seed")
	run("push", "-u", "origin", "main")
	return vaultDir
}

// TestVaultStatusJSONInSync verifies the --json report decodes and reports an
// in-sync remote with a real (fetched) behind count.
func TestVaultStatusJSONInSync(t *testing.T) {
	setupVaultWithOrigin(t)

	var code int
	out := captureStdout(t, func() {
		code = cmdVaultStatus().Run([]string{"--json"})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitOK)
	}
	var rep storage.StatusReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, out)
	}
	if rep.Version != 1 {
		t.Errorf("Version = %d, want 1", rep.Version)
	}
	if rep.Branch != "main" {
		t.Errorf("Branch = %q, want main", rep.Branch)
	}
	if len(rep.Remotes) != 1 {
		t.Fatalf("Remotes = %d, want 1", len(rep.Remotes))
	}
	r := rep.Remotes[0]
	if r.Remote != "origin" {
		t.Errorf("Remote = %q, want origin", r.Remote)
	}
	if !r.Reachable {
		t.Error("Reachable = false, want true")
	}
	if r.Ahead != 0 || r.Unpushed {
		t.Errorf("ahead=%d unpushed=%v, want in sync", r.Ahead, r.Unpushed)
	}
	if !r.BehindKnown {
		t.Error("BehindKnown = false after fetch, want true")
	}
	if r.Behind != 0 {
		t.Errorf("Behind = %d, want 0", r.Behind)
	}
}

// TestVaultStatusLineLabels verifies printVaultRemoteLine never labels a remote
// we are behind on as "in sync" (the default-case bug), and renders the other
// states correctly.
func TestVaultStatusLineLabels(t *testing.T) {
	cases := []struct {
		name    string
		st      storage.RemoteStatusJSON
		want    string
		notWant string
	}{
		{
			name:    "behind known nonzero is flagged, not in sync",
			st:      storage.RemoteStatusJSON{Remote: "origin", Reachable: true, BehindKnown: true, Behind: 3},
			want:    "BEHIND 3",
			notWant: "in sync",
		},
		{
			name: "behind known zero is in sync",
			st:   storage.RemoteStatusJSON{Remote: "origin", Reachable: true, BehindKnown: true, Behind: 0},
			want: "in sync",
		},
		{
			name:    "unreachable is not in sync",
			st:      storage.RemoteStatusJSON{Remote: "vault", Reachable: false},
			want:    "unreachable",
			notWant: "in sync",
		},
		{
			name:    "diverged",
			st:      storage.RemoteStatusJSON{Remote: "origin", Reachable: true, Ahead: 2, BehindKnown: true, Behind: 5, Diverged: true},
			want:    "DIVERGED",
			notWant: "in sync",
		},
		{
			name:    "ahead unpushed",
			st:      storage.RemoteStatusJSON{Remote: "origin", Reachable: true, Ahead: 1, Unpushed: true, BehindKnown: true, Behind: 0},
			want:    "UNPUSHED",
			notWant: "in sync",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdout(t, func() { printVaultRemoteLine(tc.st) })
			if !strings.Contains(out, tc.want) {
				t.Errorf("output %q does not contain %q", out, tc.want)
			}
			if tc.notWant != "" && strings.Contains(out, tc.notWant) {
				t.Errorf("output %q must not contain %q", out, tc.notWant)
			}
		})
	}
}

// TestVaultStatusNoFetch verifies --no-fetch leaves behind_known=false.
func TestVaultStatusNoFetch(t *testing.T) {
	setupVaultWithOrigin(t)

	var code int
	out := captureStdout(t, func() {
		code = cmdVaultStatus().Run([]string{"--no-fetch", "--json"})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitOK)
	}
	var rep storage.StatusReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, out)
	}
	if len(rep.Remotes) != 1 {
		t.Fatalf("Remotes = %d, want 1", len(rep.Remotes))
	}
	if rep.Remotes[0].BehindKnown {
		t.Error("BehindKnown = true with --no-fetch, want false")
	}
}

// TestVaultStatusUnpushed verifies a local-only commit reports unpushed=true and
// ahead>=1.
func TestVaultStatusUnpushed(t *testing.T) {
	vaultDir := setupVaultWithOrigin(t)

	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", vaultDir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// A second local commit, never pushed → ahead of origin/main.
	os.WriteFile(filepath.Join(vaultDir, "more.txt"), []byte("more\n"), 0o644)
	run("add", "more.txt")
	run("commit", "-m", "local only")

	var code int
	out := captureStdout(t, func() {
		code = cmdVaultStatus().Run([]string{"--json"})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitOK)
	}
	var rep storage.StatusReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, out)
	}
	if len(rep.Remotes) != 1 {
		t.Fatalf("Remotes = %d, want 1", len(rep.Remotes))
	}
	r := rep.Remotes[0]
	if !r.Unpushed || r.Ahead < 1 {
		t.Errorf("unpushed=%v ahead=%d, want unpushed with ahead>=1", r.Unpushed, r.Ahead)
	}
}

func TestVaultRoot(t *testing.T) {
	setupTestVaultEnv(t)
	root, err := vaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	if root == "" {
		t.Error("root is empty")
	}
}

func TestFullVaultPullDryRun(t *testing.T) {
	// Create a vault dir with git repo.
	vaultDir := setupTestVaultEnv(t)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", vaultDir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("remote", "add", "origin", "https://example.com/repo.git")

	cmd := cmdVaultPull()
	code := cmd.Run([]string{"--dry-run"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
}

func TestFullVaultPushDryRun(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", vaultDir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	run("remote", "add", "origin", "https://example.com/repo.git")
	os.WriteFile(filepath.Join(vaultDir, "f.txt"), []byte("data"), 0o644)
	run("add", "f.txt")
	run("commit", "-m", "init")

	cmd := cmdVaultPush()
	code := cmd.Run([]string{"--dry-run"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
}

// lsRemoteMain returns the SHA that origin advertises for refs/heads/main, i.e.
// the tip actually present on the bare remote. Returns "" when the ref is absent.
func lsRemoteMain(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "ls-remote", "origin", "main")
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-remote origin main: %v", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// gitPorcelain returns `git status --porcelain` for dir (empty == clean tree).
func gitPorcelain(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain")
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git status --porcelain: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// gitTracksPath reports whether rel is tracked in HEAD's tree.
func gitTracksPath(t *testing.T, dir, rel string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "ls-tree", "-r", "--name-only", "HEAD")
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-tree HEAD: %v", err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == rel {
			return true
		}
	}
	return false
}

// TestVaultSyncTidyThenSyncHappyPath exercises the DEFAULT tidy-then-sync path:
// a vault dirty with only a sweepable capture artifact + a configured bare
// remote must return ExitOK, land the artifact commit on the bare remote, and
// leave the working tree clean.
func TestVaultSyncTidyThenSyncHappyPath(t *testing.T) {
	vaultDir := setupVaultWithOrigin(t)
	remoteBefore := lsRemoteMain(t, vaultDir)

	// Only a sweepable capture artifact is dirty — no genuine dirt.
	artifact := "Projects/vibe-palace/sessions/2026-07-19.md"
	mkfile(t, vaultDir, artifact, "session\n")

	code := cmdVaultSync().Run(nil)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitOK)
	}
	// Working tree must be clean: the artifact was committed, nothing left over.
	if p := gitPorcelain(t, vaultDir); p != "" {
		t.Errorf("working tree not clean after sync:\n%s", p)
	}
	// The artifact commit must be on the bare remote (origin/main advanced to the
	// local HEAD, which now tracks the artifact).
	remoteAfter := lsRemoteMain(t, vaultDir)
	if remoteAfter == remoteBefore {
		t.Errorf("origin/main did not advance: still %s", remoteBefore)
	}
	if head := gitHead(t, vaultDir); remoteAfter != head {
		t.Errorf("origin/main %s != local HEAD %s", remoteAfter, head)
	}
	if !gitTracksPath(t, vaultDir, artifact) {
		t.Errorf("artifact %s not committed to HEAD", artifact)
	}
}

// TestVaultSyncNoTidyRefusesDirt verifies --no-tidy is the raw pull+push: with a
// non-artifact file dirtying the tree the push guard fires (ExitUser) and nothing
// reaches the remote.
func TestVaultSyncNoTidyRefusesDirt(t *testing.T) {
	vaultDir := setupVaultWithOrigin(t)
	remoteBefore := lsRemoteMain(t, vaultDir)

	// Non-artifact dirt: --no-tidy never sweeps or commits it.
	mkfile(t, vaultDir, "notes.txt", "scratch\n")

	code := cmdVaultSync().Run([]string{"--no-tidy"})
	if code != cli.ExitUser {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitUser)
	}
	if remoteAfter := lsRemoteMain(t, vaultDir); remoteAfter != remoteBefore {
		t.Errorf("origin/main changed on refusal: %s -> %s", remoteBefore, remoteAfter)
	}
}

// TestVaultSyncNoTidyCleanPassthrough verifies a clean, in-sync vault under
// --no-tidy succeeds: raw pull+push both pass.
func TestVaultSyncNoTidyCleanPassthrough(t *testing.T) {
	setupVaultWithOrigin(t)

	code := cmdVaultSync().Run([]string{"--no-tidy"})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitOK)
	}
}

// TestVaultSyncDefaultRefusesGenuineDirt verifies the DEFAULT path refuses a tree
// carrying genuine (non-artifact) dirt: SyncVault sets Refused, the CLI maps that
// to ExitUser, and — because the refusal is before any network I/O — nothing is
// pushed.
func TestVaultSyncDefaultRefusesGenuineDirt(t *testing.T) {
	vaultDir := setupVaultWithOrigin(t)
	remoteBefore := lsRemoteMain(t, vaultDir)

	mkfile(t, vaultDir, "notes.txt", "scratch\n")

	code := cmdVaultSync().Run(nil)
	if code != cli.ExitUser {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitUser)
	}
	if remoteAfter := lsRemoteMain(t, vaultDir); remoteAfter != remoteBefore {
		t.Errorf("origin/main changed on refusal: %s -> %s", remoteBefore, remoteAfter)
	}
}

// TestVaultSyncDryRunPreviewSweep verifies the --dry-run tidy path previews the
// sweep ("Would sweep" + the artifact path) on stdout and commits NOTHING (local
// HEAD unchanged). Dry-run never touches the network, so a URL remote suffices.
//
// Exit code is ExitUser, not ExitOK: the preview is printed first, but the
// downstream dry-run pushAll checks `git status --porcelain` BEFORE its dry-run
// branch and refuses on the (still-dirty) tree. The preview + no-commit are the
// load-bearing assertions here; the exit code just records that pre-existing
// pushAll guard on a dirty dry-run.
func TestVaultSyncDryRunPreviewSweep(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", vaultDir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	run("remote", "add", "origin", "https://example.com/repo.git")
	os.WriteFile(filepath.Join(vaultDir, "seed.txt"), []byte("seed"), 0o644)
	run("add", "seed.txt")
	run("commit", "-m", "seed")

	headBefore := gitHead(t, vaultDir)

	artifact := "Projects/vibe-palace/sessions/2026-07-19.md"
	mkfile(t, vaultDir, artifact, "session\n")
	mkfile(t, vaultDir, "Projects/vibe-palace/resume.md", "resume\n")

	var code int
	out := captureStdout(t, func() {
		code = cmdVaultSync().Run([]string{"--dry-run"})
	})
	// A dry-run executes nothing, so it previews the classification and the
	// would-run pull/push and exits OK — it never fails on the dirty tree the
	// real run would sweep (the Reported section already surfaces genuine dirt).
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitOK)
	}
	for _, want := range []string{"Would sweep", artifact} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q; got:\n%s", want, out)
		}
	}
	if headAfter := gitHead(t, vaultDir); headAfter != headBefore {
		t.Errorf("dry-run created a commit: HEAD %s -> %s", headBefore, headAfter)
	}
}

func TestFullVaultSyncDryRun(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", vaultDir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	run("remote", "add", "origin", "https://example.com/repo.git")
	os.WriteFile(filepath.Join(vaultDir, "f.txt"), []byte("data"), 0o644)
	run("add", "f.txt")
	run("commit", "-m", "init")

	cmd := cmdVaultSync()
	code := cmd.Run([]string{"--dry-run"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
}
