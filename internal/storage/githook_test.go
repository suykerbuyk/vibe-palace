// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// hookRepo builds a throwaway git repo in t.TempDir() with one commit already
// on it, and returns its root.
//
// Identity is set LOCALLY and the host's global/system config is neutralized —
// the same discipline throwawayConflictedRepo uses in git_error_test.go. Here it
// carries extra weight: an operator with core.hooksPath set globally would
// otherwise make every install test take the shared-hooksPath refusal branch and
// pass for the wrong reason.
func hookRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", ".")
	gitIn(t, dir, "config", "user.name", "hook-test")
	gitIn(t, dir, "config", "user.email", "hook@test.invalid")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "f.txt")
	gitIn(t, dir, "commit", "-q", "-m", "seed")
	return dir
}

// gitEnv is the neutralized environment every git call in this file runs under.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_EDITOR=true",
	)
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitInSoft(dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return out
}

func gitInSoft(dir string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = gitEnv()
	out, err := c.CombinedOutput()
	return string(out), err
}

// dirtyTree makes an uncommitted change so a commit has something to record.
func dirtyTree(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", "f.txt")
}

// normalizingMessage is a commit message that `git commit -F` will REWRITE:
// line 3 carries trailing spaces and there are two consecutive blank lines
// before the last paragraph. Both are normalized by --cleanup=whitespace, which
// is what `git commit -F` applies. This exact shape is what makes the
// stripspace-vs-raw mutation observable.
const normalizingMessage = "feat(x): a subject line\n" +
	"\n" +
	"a body line with trailing spaces   \n" +
	"another body line\n" +
	"\n" +
	"\n" +
	"a paragraph after two blank lines\n"

func writeCommitMsg(t *testing.T, root, body string) string {
	t.Helper()
	p := filepath.Join(root, "commit.msg")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPostCommitHookReapsAConsumedMessage is the DoD, stated as a test: a real
// `git commit -F commit.msg` typed WITHOUT `&& rm` must leave no commit.msg.
//
// The message is deliberately the normalizing one — proving the positive case
// fires on THIS project's multi-paragraph shape, not only on a single line that
// happens to survive cleanup untouched.
func TestPostCommitHookReapsAConsumedMessage(t *testing.T) {
	root := hookRepo(t)
	if rep := InstallPostCommitHook(root); rep.Status != HookInstalled {
		t.Fatalf("install: got %v (%s)", rep.Status, rep.Detail)
	}
	msg := writeCommitMsg(t, root, normalizingMessage)
	dirtyTree(t, root, "one\n")

	// No `&& rm` anywhere in this call. That is the whole point.
	gitIn(t, root, "commit", "-q", "-F", "commit.msg")

	if _, err := os.Stat(msg); !os.IsNotExist(err) {
		data, _ := os.ReadFile(msg)
		t.Fatalf("commit.msg survived a commit that consumed it (err=%v):\n%s", err, data)
	}
}

// TestPostCommitHookIgnoredExitCannotBlockACommit pins the safety property the
// whole design rests on: git ignores post-commit's exit status, so a failing
// reap can never fail, block, or slow a commit. Re-derived rather than cited.
func TestPostCommitHookIgnoredExitCannotBlockACommit(t *testing.T) {
	root := hookRepo(t)
	hookPath := installedHookPath(t, root)
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 17\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dirtyTree(t, root, "two\n")

	if out, err := gitInSoft(root, "commit", "-q", "-m", "still lands"); err != nil {
		t.Fatalf("a post-commit hook exiting 17 failed the commit: %v\n%s", err, out)
	}
}

// TestPostCommitHookKeepsAnUnrelatedMessage is the M2 objection, tested: an
// authored-but-uncommitted commit.msg must survive an unrelated `git commit -m`.
// Unconditional truncation would destroy it; proof-of-consumption does not.
func TestPostCommitHookKeepsAnUnrelatedMessage(t *testing.T) {
	root := hookRepo(t)
	InstallPostCommitHook(root)
	msg := writeCommitMsg(t, root, normalizingMessage)
	dirtyTree(t, root, "three\n")

	gitIn(t, root, "commit", "-q", "-m", "an unrelated typo fix")

	if _, err := os.Stat(msg); err != nil {
		t.Fatalf("an unrelated commit destroyed an unsent commit.msg: %v", err)
	}
}

// TestPostCommitHookKeepsAnEditedMessage covers `git commit -e -F`: the operator
// opened the editor and changed the text, so HEAD no longer matches the file.
// The residue must STAY — it is what vp_preflight_wrap reports as
// commit_msg_unconsumed, and reaping it would hide a real finding.
func TestPostCommitHookKeepsAnEditedMessage(t *testing.T) {
	root := hookRepo(t)
	InstallPostCommitHook(root)
	msg := writeCommitMsg(t, root, normalizingMessage)
	dirtyTree(t, root, "four\n")

	editor := filepath.Join(t.TempDir(), "editor.sh")
	if err := os.WriteFile(editor,
		[]byte("#!/bin/sh\nprintf 'an edit the operator made\\n' >> \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	c := exec.Command("git", "commit", "-q", "-e", "-F", "commit.msg")
	c.Dir = root
	c.Env = append(gitEnv(), "GIT_EDITOR="+editor)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git commit -e -F: %v\n%s", err, out)
	}

	if _, err := os.Stat(msg); err != nil {
		t.Fatalf("an EDITED message was reaped; the unconsumed-residue signal is lost: %v", err)
	}
}

// TestPostCommitHookMutationRawCompareStopsTheReap is the mutation the task's
// DoD names: drop `git stripspace` from both sides and the reap must STOP.
//
// This is what makes the normalization rule load-bearing rather than decorative.
// `git commit -F` applies --cleanup=whitespace, so HEAD's message differs from
// the file by trailing whitespace and a collapsed blank line — a raw comparison
// never matches, and a raw-comparing hook is a no-op that LOOKS installed. If
// this test ever passes with the mutant, the real hook is not doing the work its
// comment claims.
func TestPostCommitHookMutationRawCompareStopsTheReap(t *testing.T) {
	mutant := strings.Replace(PostCommitHookScript,
		`head=$(git log -1 --format=%B 2>/dev/null | git stripspace)`,
		`head=$(git log -1 --format=%B 2>/dev/null)`, 1)
	if mutant == PostCommitHookScript {
		t.Fatal("mutation did not apply to the HEAD side — the script changed shape; update this test")
	}
	before := mutant
	mutant = strings.Replace(mutant,
		`file=$(git stripspace <"$msg" 2>/dev/null)`,
		`file=$(cat "$msg" 2>/dev/null)`, 1)
	if mutant == before {
		t.Fatal("mutation did not apply to the file side — the script changed shape; update this test")
	}
	if strings.Contains(mutant, "git stripspace") {
		t.Fatal("mutant still calls git stripspace; the mutation is not exercising a raw compare")
	}

	root := hookRepo(t)
	hookPath := installedHookPath(t, root)
	if err := os.WriteFile(hookPath, []byte(mutant), 0o755); err != nil {
		t.Fatal(err)
	}
	msg := writeCommitMsg(t, root, normalizingMessage)
	dirtyTree(t, root, "five\n")

	gitIn(t, root, "commit", "-q", "-F", "commit.msg")

	if _, err := os.Stat(msg); err != nil {
		t.Fatalf("the raw-compare mutant STILL reaped the message — stripspace is not what makes "+
			"the positive case fire, so the normalization rule is untested: %v", err)
	}
}

// installedHookPath installs the real hook and returns its path, failing the
// test if the install did not take.
func installedHookPath(t *testing.T, root string) string {
	t.Helper()
	rep := InstallPostCommitHook(root)
	if rep.Status != HookInstalled {
		t.Fatalf("install: got %v (%s)", rep.Status, rep.Detail)
	}
	return rep.Path
}

// TestInstallPostCommitHookIsIdempotent: a second install reports Current and
// rewrites nothing.
func TestInstallPostCommitHookIsIdempotent(t *testing.T) {
	root := hookRepo(t)
	first := InstallPostCommitHook(root)
	if first.Status != HookInstalled {
		t.Fatalf("first install: got %v (%s)", first.Status, first.Detail)
	}
	info1, err := os.Stat(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info1.Mode().Perm()&0o111 == 0 {
		t.Fatalf("hook is not executable (%v) — git silently ignores a non-executable hook", info1.Mode().Perm())
	}

	second := InstallPostCommitHook(root)
	if second.Status != HookCurrent {
		t.Fatalf("second install: got %v (%s), want HookCurrent", second.Status, second.Detail)
	}
	data, err := os.ReadFile(second.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != PostCommitHookScript {
		t.Fatal("hook body drifted from PostCommitHookScript after a second install")
	}
}

// TestInstallPostCommitHookRefusesAForeignHook: a post-commit this package did
// not write is never clobbered and never appended to.
func TestInstallPostCommitHookRefusesAForeignHook(t *testing.T) {
	root := hookRepo(t)
	hooks := filepath.Join(root, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := "#!/bin/sh\n# somebody else's hook\necho hi\n"
	path := filepath.Join(hooks, "post-commit")
	if err := os.WriteFile(path, []byte(foreign), 0o755); err != nil {
		t.Fatal(err)
	}

	rep := InstallPostCommitHook(root)
	if rep.Status != HookForeign {
		t.Fatalf("got %v (%s), want HookForeign", rep.Status, rep.Detail)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != foreign {
		t.Fatalf("a foreign post-commit hook was modified:\n%s", data)
	}
}

// TestInstallPostCommitHookRefusesSharedHooksPath: core.hooksPath set means the
// hooks directory is not this repo's to write into. Detect and refuse — writing
// would install a vibe-palace hook into every repo sharing that directory.
func TestInstallPostCommitHookRefusesSharedHooksPath(t *testing.T) {
	root := hookRepo(t)
	shared := t.TempDir()
	gitIn(t, root, "config", "core.hooksPath", shared)

	rep := InstallPostCommitHook(root)
	if rep.Status != HookSharedHooksPath {
		t.Fatalf("got %v (%s), want HookSharedHooksPath", rep.Status, rep.Detail)
	}
	if !strings.Contains(rep.Detail, shared) {
		t.Fatalf("refusal does not name the shared directory: %s", rep.Detail)
	}
	entries, err := os.ReadDir(shared)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrote into the shared hooks dir: %v", entries)
	}
	// And the repo's own hooks dir must not have been used as a fallback.
	if _, err := os.Stat(filepath.Join(root, ".git", "hooks", "post-commit")); !os.IsNotExist(err) {
		t.Fatal("fell back to .git/hooks after refusing a shared core.hooksPath")
	}
}

// TestInspectPostCommitHookReportsMissing is the state `vp check` reports on an
// existing clone that never re-ran `vp init`.
func TestInspectPostCommitHookReportsMissing(t *testing.T) {
	root := hookRepo(t)
	rep := InspectPostCommitHook(root)
	if rep.Status != HookMissing {
		t.Fatalf("got %v (%s), want HookMissing", rep.Status, rep.Detail)
	}
	if rep.Path == "" {
		t.Fatal("HookMissing must still carry the resolved path")
	}
}

// TestInspectPostCommitHookReportsStale: ours by marker, but the body has moved
// on. Inspect says stale; install rewrites it.
func TestInspectPostCommitHookReportsStale(t *testing.T) {
	root := hookRepo(t)
	path := installedHookPath(t, root)
	if err := os.WriteFile(path,
		[]byte("#!/bin/sh\n# "+PostCommitHookMarker+" v0\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if rep := InspectPostCommitHook(root); rep.Status != HookStale {
		t.Fatalf("inspect: got %v (%s), want HookStale", rep.Status, rep.Detail)
	}
	if rep := InstallPostCommitHook(root); rep.Status != HookInstalled {
		t.Fatalf("install over stale: got %v (%s), want HookInstalled", rep.Status, rep.Detail)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != PostCommitHookScript {
		t.Fatal("a stale hook was not rewritten to the current script")
	}
}

// TestInstallPostCommitHookOutsideARepo degrades to a report, never an error or
// a stray file.
func TestInstallPostCommitHookOutsideARepo(t *testing.T) {
	dir := t.TempDir()
	rep := InstallPostCommitHook(dir)
	if rep.Status != HookNoRepo {
		t.Fatalf("got %v (%s), want HookNoRepo", rep.Status, rep.Detail)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrote into a non-repo directory: %v", entries)
	}
	if rep := InstallPostCommitHook(""); rep.Status != HookNoRepo {
		t.Fatalf("empty root: got %v (%s), want HookNoRepo", rep.Status, rep.Detail)
	}
}

// TestInstallPostCommitHookFromASubdirectory pins the `--git-path` trap: run
// from a subdirectory, `git rev-parse --git-path hooks` answers `../.git/hooks`
// — RELATIVE TO THE GIT PROCESS CWD. Joining that against the repo root would
// land outside the repo. Resolution must go through the toplevel first.
func TestInstallPostCommitHookFromASubdirectory(t *testing.T) {
	root := hookRepo(t)
	sub := filepath.Join(root, "internal", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	rep := InstallPostCommitHook(sub)
	if rep.Status != HookInstalled {
		t.Fatalf("got %v (%s), want HookInstalled", rep.Status, rep.Detail)
	}
	want := filepath.Join(root, ".git", "hooks", "post-commit")
	if rep.Path != want {
		t.Fatalf("hook path %q, want %q", rep.Path, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("hook not written at the repo's own hooks dir: %v", err)
	}
}

// TestPostCommitHookScriptNeverShellsOutToVp: the hook must be self-contained sh
// + git. A `vp`-shelling hook couples every commit to PATH and to a current
// binary, and fails on exactly the IDE and GUI commits that skip wrap.
func TestPostCommitHookScriptNeverShellsOutToVp(t *testing.T) {
	for _, line := range strings.Split(PostCommitHookScript, "\n") {
		code := line
		if i := strings.Index(code, "#"); i >= 0 {
			code = code[:i]
		}
		for _, f := range strings.Fields(code) {
			if f == "vp" {
				t.Fatalf("the hook invokes `vp`: %q", line)
			}
		}
	}
	if !strings.HasPrefix(PostCommitHookScript, "#!/bin/sh\n") {
		t.Fatal("the hook must be /bin/sh, not bash")
	}
	if !strings.Contains(PostCommitHookScript, PostCommitHookMarker) {
		t.Fatal("the hook body must carry the marker that identifies it as ours")
	}
}
