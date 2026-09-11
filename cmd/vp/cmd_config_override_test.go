// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// `vp config sync` must never lose an operator's vault Templates/ override.
//
// Until 2026-09-10 it did, by a chain reproduced on a git vault with a remote:
// --yes (or an `o` answer) overwrote a diverged override with the embedded
// copy, the next sync — even an answerless one — classified the result as a
// reconciler-owned mirror and pruned it, the prune's .bak step overwrote the
// only local copy, and the prune commit pushed the deletion to every host.
// These tests pin every link of that chain shut through runConfigSync; the
// integration package pins the same through the real binary.

const (
	myWrap     = "# my wrap override\n"
	myWorkflow = "# my workflow override\n"
)

// overrideVault inits a --no-git vault for a fresh project and chdirs into
// the project, so `--tier vault` syncs resolve it.
func overrideVault(t *testing.T) (vaultPath, projDir string) {
	t.Helper()
	_, _ = initTestEnv(t, false)
	projDir = t.TempDir()
	markProjectDir(t, projDir)
	vaultPath = filepath.Join(t.TempDir(), "vault")
	if code := cmdInit(cli.BuildInfo{Version: "test"}).Run(
		[]string{projDir, "--name", "ovr", "--vault-path", vaultPath, "--no-git"}); code != cli.ExitOK {
		t.Fatalf("init exit code = %d", code)
	}
	cwd, _ := os.Getwd()
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	return vaultPath, projDir
}

func putVaultFile(t *testing.T, vaultPath, rel, body string) string {
	t.Helper()
	p := filepath.Join(vaultPath, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertFileBytes(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("%s: %v", path, err)
		return
	}
	if string(got) != want {
		t.Errorf("%s changed:\n got  %q\n want %q", path, got, want)
	}
}

func assertNoSidecars(t *testing.T, path string) {
	t.Helper()
	for _, side := range []string{".bak", ".new"} {
		if _, err := os.Stat(path + side); !os.IsNotExist(err) {
			t.Errorf("%s written (err=%v)", path+side, err)
		}
	}
}

func syncVault(t *testing.T, projDir, stdin string, extra ...string) string {
	t.Helper()
	out, code := runSyncWithStdin(t, stdin, append([]string{"--project-root", projDir, "--tier", "vault"}, extra...))
	if code != cli.ExitOK {
		t.Fatalf("sync exit = %d\n%s", code, out)
	}
	return out
}

// TestConfigSyncYesTwiceKeepsOverride is the filed chain without git: two
// --yes syncs over lock-less overrides of a command and of a root-level
// resource. --yes used to answer every diverged-override Prompt "overwrite".
func TestConfigSyncYesTwiceKeepsOverride(t *testing.T) {
	vaultPath, projDir := overrideVault(t)
	wrap := putVaultFile(t, vaultPath, "Templates/commands/wrap.md", myWrap)
	workflow := putVaultFile(t, vaultPath, "Templates/workflow.md", myWorkflow)

	for i := 1; i <= 2; i++ {
		out := syncVault(t, projDir, "", "--yes")
		if !strings.Contains(out, "updated=0") || !strings.Contains(out, "pruned=0") {
			t.Errorf("sync %d touched a template:\n%s", i, out)
		}
		for _, rel := range []string{"Templates/commands/wrap.md", "Templates/workflow.md"} {
			if !strings.Contains(out, "[keep] "+rel+" — ") {
				t.Errorf("sync %d printed no [keep] line for %s:\n%s", i, rel, out)
			}
		}
	}
	assertFileBytes(t, wrap, myWrap)
	assertFileBytes(t, workflow, myWorkflow)
	assertNoSidecars(t, wrap)
	assertNoSidecars(t, workflow)
}

// TestConfigSyncOAnswerIsNotAccepted replaces the old overwrite test: `o` is
// no longer an answer. The menu re-prompts, EOF then means keep, and the
// diverged override is untouched.
func TestConfigSyncOAnswerIsNotAccepted(t *testing.T) {
	vaultPath, target, userEdit := syncPromptSetup(t, "commands/wrap.md")
	before, _ := templates.ReadLock(vaultPath)

	out, code := runSyncWithStdin(t, "o\n", []string{
		"--project-root", filepath.Dir(target), "--tier", "vault",
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if !strings.Contains(out, "Please answer s, n, S, N, or q.") {
		t.Errorf("o was not rejected:\n%s", out)
	}
	if !strings.Contains(out, "[keep] Templates/commands/wrap.md — ") {
		t.Errorf("EOF after the rejected o did not keep the file:\n%s", out)
	}
	assertFileBytes(t, target, userEdit)
	assertNoSidecars(t, target)
	after, _ := templates.ReadLock(vaultPath)
	if after.Entries["Templates/commands/wrap.md"] != before.Entries["Templates/commands/wrap.md"] {
		t.Error("the lock entry for the kept override changed")
	}
}

// TestConfigSyncKeepsOverrideOnGitVaultWithRemote: the same two --yes syncs
// on a git vault with an origin. Nothing may be committed or pushed, and a
// fresh clone — another host — still carries the override.
func TestConfigSyncKeepsOverrideOnGitVaultWithRemote(t *testing.T) {
	vaultPath, projDir := overrideVault(t)
	putVaultFile(t, vaultPath, "Templates/commands/wrap.md", myWrap)
	putVaultFile(t, vaultPath, "Templates/workflow.md", myWorkflow)
	origin := gitifyVault(t, vaultPath)
	head := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD"))

	syncVault(t, projDir, "", "--yes")
	syncVault(t, projDir, "", "--yes")

	if got := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD")); got != head {
		t.Errorf("HEAD moved: %s -> %s\n%s", head, got, gitInVault(t, vaultPath, "log", "-1", "--stat"))
	}
	if got := strings.TrimSpace(gitInVault(t, origin, "rev-parse", "main")); got != head {
		t.Errorf("origin tip moved: %s -> %s", head, got)
	}
	hostB := cloneVault(t, origin)
	assertFileBytes(t, filepath.Join(hostB, "Templates", "commands", "wrap.md"), myWrap)
	assertFileBytes(t, filepath.Join(hostB, "Templates", "workflow.md"), myWorkflow)
}

// seedCommittedOverrideUnderMirror commits an override of embeddedRel, then
// puts the embedded bytes in the worktree with a lock entry at the embedded
// SHA: the state an old binary's `o`/--yes sync, or an upgrade reset, leaves.
func seedCommittedOverrideUnderMirror(t *testing.T, vaultPath, embeddedRel string) {
	t.Helper()
	sha, ok := templates.EmbeddedSHA(embeddedRel)
	if !ok {
		t.Fatalf("no embedded %s", embeddedRel)
	}
	seedTemplateOverride(t, vaultPath, embeddedRel, embeddedTemplateBytes(t, embeddedRel), sha)
}

// TestConfigSyncRestoresCommittedOverrideFromHEAD is the answerless path: no
// --yes, no answers, and still the committed override was deleted on every
// host, because the prune removed the worktree mirror and its commit removed
// HEAD's blob — the operator's. Now the commit checks the blob it removes and
// restores it instead.
func TestConfigSyncRestoresCommittedOverrideFromHEAD(t *testing.T) {
	vaultPath, projDir := overrideVault(t)
	target := putVaultFile(t, vaultPath, "Templates/commands/wrap.md", myWrap)
	origin := gitifyVault(t, vaultPath)
	head := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD"))
	seedCommittedOverrideUnderMirror(t, vaultPath, "commands/wrap.md")

	out := syncVault(t, projDir, "")

	if !strings.Contains(out, "restored Templates/commands/wrap.md from HEAD") {
		t.Errorf("no restored line:\n%s", out)
	}
	if !strings.Contains(out, "pruned=0") {
		t.Errorf("a restored path was counted as pruned:\n%s", out)
	}
	assertFileBytes(t, target, myWrap)
	if st := gitInVault(t, vaultPath, "status", "--porcelain", "--", "Templates/"); strings.TrimSpace(st) != "" {
		t.Errorf("Templates/ is dirty after the restore: %q", st)
	}
	// The restored override keeps its lock entry, so the next sync keeps it
	// silently (case 4) instead of prompting on every run.
	if l, _ := templates.ReadLock(vaultPath); l.Entries["Templates/commands/wrap.md"].EmbeddedSHA == "" {
		t.Error("the restored override lost its lock entry")
	}
	if again := syncVault(t, projDir, ""); strings.Contains(again, "Prompt") || !strings.Contains(again, "Nothing to do") {
		t.Errorf("the restored override is not a silent keep on the next sync:\n%s", again)
	}
	if got := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD")); got != head {
		t.Errorf("a commit was made: %s", gitInVault(t, vaultPath, "log", "-1", "--stat"))
	}
	if got := strings.TrimSpace(gitInVault(t, origin, "rev-parse", "main")); got != head {
		t.Errorf("origin tip moved to %s", got)
	}
	// The dirt a vault sync would refuse on holds no Templates/ path. (The
	// host-local templates.lock is still untracked dirt on a canonically
	// configured vault — recorded in template-provenance-manifest-retires-
	// the-host-local-lock — so the assertion is scoped to Templates/.)
	scan, err := storage.TidyScan(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range scan.GenuineDirt() {
		if strings.HasPrefix(p, "Templates/") {
			t.Errorf("vault sync would refuse on %s", p)
		}
	}
}

// TestConfigSyncPruneLeavesExistingBakUntouched: the prune used to write its
// .bak over whatever was there — the operator's bytes an earlier overwrite
// saved — leaving a .bak of embedded text.
func TestConfigSyncPruneLeavesExistingBakUntouched(t *testing.T) {
	_, target, _ := syncPruneSetup(t, "commands/wrap.md")
	const operator = "# the operator's override, saved by an earlier overwrite\n"
	if err := os.WriteFile(target+".bak", []byte(operator), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runSyncWithStdin(t, "", []string{"--project-root", filepath.Dir(target), "--tier", "vault", "--yes"})
	if code != cli.ExitOK || !strings.Contains(out, "pruned=1") {
		t.Fatalf("exit=%d\n%s", code, out)
	}
	assertFileBytes(t, target+".bak", operator)
}

// TestConfigSyncPruneCommitsOnlyVerifiedMirrors: of two pruned tracked paths,
// the one whose committed copy is a mirror is committed and the one whose
// committed copy is operator content is restored; the message lists only the
// first, with its basis.
func TestConfigSyncPruneCommitsOnlyVerifiedMirrors(t *testing.T) {
	vaultPath, projDir := overrideVault(t)
	wrap := putVaultFile(t, vaultPath, "Templates/commands/wrap.md", string(embeddedTemplateBytes(t, "commands/wrap.md")))
	restart := putVaultFile(t, vaultPath, "Templates/commands/restart.md", "# my restart override\n")
	origin := gitifyVault(t, vaultPath)
	seedCommittedOverrideUnderMirror(t, vaultPath, "commands/wrap.md")
	seedCommittedOverrideUnderMirror(t, vaultPath, "commands/restart.md")

	out := syncVault(t, projDir, "", "--yes")

	if !strings.Contains(out, "restored Templates/commands/restart.md from HEAD") {
		t.Errorf("restart.md not restored:\n%s", out)
	}
	if strings.Contains(out, "restored Templates/commands/wrap.md") {
		t.Errorf("the mirror was restored:\n%s", out)
	}
	if _, err := os.Stat(wrap); !os.IsNotExist(err) {
		t.Errorf("mirror still present (err=%v)", err)
	}
	assertFileBytes(t, restart, "# my restart override\n")

	names := strings.TrimSpace(gitInVault(t, vaultPath, "show", "--name-status", "--format=", "HEAD"))
	if names != "D\tTemplates/commands/wrap.md" {
		t.Errorf("prune commit carries %q, want only the mirror's deletion", names)
	}
	msg := gitInVault(t, vaultPath, "log", "-1", "--format=%B")
	if !strings.Contains(msg, "- Templates/commands/wrap.md (current embedded copy)") {
		t.Errorf("message does not list the mirror with its basis:\n%s", msg)
	}
	if strings.Contains(msg, "restart.md") {
		t.Errorf("message lists the restored path:\n%s", msg)
	}
	if strings.Contains(msg, "Tier 4 override") {
		t.Errorf("message still asserts the old unconditional claim:\n%s", msg)
	}
	if tip := strings.TrimSpace(gitInVault(t, origin, "rev-parse", "main")); tip != strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD")) {
		t.Errorf("the verified prune commit was not pushed")
	}
}

// TestConfigSyncSkipsPruneWhenGitUnavailable: a git vault whose committed
// copies cannot be read defers the prune — for a .git directory and for the
// .git FILE of a linked worktree or submodule alike.
func TestConfigSyncSkipsPruneWhenGitUnavailable(t *testing.T) {
	for _, kind := range []string{"dir", "file"} {
		t.Run(kind, func(t *testing.T) {
			vaultPath, target, _ := syncPruneSetup(t, "commands/wrap.md")
			marker := filepath.Join(vaultPath, ".git")
			if kind == "dir" {
				if err := os.Mkdir(marker, 0o755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(marker, []byte("gitdir: /nonexistent\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(target)
			t.Setenv("PATH", t.TempDir())

			out, code := runSyncWithStdin(t, "", []string{"--project-root", filepath.Dir(target), "--tier", "vault", "--yes"})
			if code != cli.ExitOK {
				t.Fatalf("exit=%d\n%s", code, out)
			}
			if !strings.Contains(out, "[Skip] TemplateTree:Templates: Templates/commands/wrap.md prune deferred") {
				t.Errorf("no deferred-prune row:\n%s", out)
			}
			if !strings.Contains(out, "pruned=0") {
				t.Errorf("a prune happened:\n%s", out)
			}
			assertFileBytes(t, target, string(before))
		})
	}
}

// TestConfigSyncVerifiesPruneOnLinkedWorktreeVault: a vault checked out as a
// linked worktree has a .git FILE. The prune commit used to test for a .git
// DIRECTORY, so on such a vault it skipped the commit — and the HEAD check —
// and left the deletion of a committed override in the worktree.
func TestConfigSyncVerifiesPruneOnLinkedWorktreeVault(t *testing.T) {
	_, _ = initTestEnv(t, false)
	projDir := t.TempDir()
	markProjectDir(t, projDir)

	mainRepo := t.TempDir()
	gitInVault(t, mainRepo, "init", "-b", "main")
	gitInVault(t, mainRepo, "config", "user.email", "test@test.com")
	gitInVault(t, mainRepo, "config", "user.name", "Test")
	putVaultFile(t, mainRepo, "Templates/commands/wrap.md", myWrap)
	putVaultFile(t, mainRepo, ".gitignore", ".vp-locks/\n*.bak\n")
	gitInVault(t, mainRepo, "add", "-A")
	gitInVault(t, mainRepo, "commit", "-m", "override")
	vaultPath := filepath.Join(t.TempDir(), "vault")
	gitInVault(t, mainRepo, "worktree", "add", "-b", "vault", vaultPath)
	if fi, err := os.Lstat(filepath.Join(vaultPath, ".git")); err != nil || fi.IsDir() {
		t.Fatalf("fixture: the vault's .git should be a file (err=%v)", err)
	}

	if code := cmdInit(cli.BuildInfo{Version: "test"}).Run(
		[]string{projDir, "--name", "ovr", "--vault-path", vaultPath, "--no-git"}); code != cli.ExitOK {
		t.Fatalf("init exit code = %d", code)
	}
	cwd, _ := os.Getwd()
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	seedCommittedOverrideUnderMirror(t, vaultPath, "commands/wrap.md")

	out := syncVault(t, projDir, "")
	if !strings.Contains(out, "restored Templates/commands/wrap.md from HEAD") {
		t.Errorf("no restore on a worktree vault:\n%s", out)
	}
	assertFileBytes(t, filepath.Join(vaultPath, "Templates", "commands", "wrap.md"), myWrap)
	if st := gitInVault(t, vaultPath, "status", "--porcelain", "--", "Templates/"); strings.TrimSpace(st) != "" {
		t.Errorf("Templates/ is dirty: %q", st)
	}
}

// runSyncAnsweringPrompt runs runConfigSync with stdin and stdout on pipes. The
// moment the Templates prompt appears on stdout, onPrompt runs and then answer
// is written to stdin — so onPrompt acts in exactly the window between Plan
// and Apply that a real operator's hesitation opens.
func runSyncAnsweringPrompt(t *testing.T, args []string, onPrompt func(), answer string) (string, int) {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()

	var mu sync.Mutex
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		answered := false
		deadline := time.AfterFunc(30*time.Second, func() { _ = inW.Close() })
		defer deadline.Stop()
		chunk := make([]byte, 4096)
		for {
			n, rerr := outR.Read(chunk)
			mu.Lock()
			buf.Write(chunk[:n])
			// Keyed on the menu's closing words, not its answers, so the test
			// drives the same window whatever the menu offers.
			seen := strings.Contains(buf.String(), "[q]uit: ")
			mu.Unlock()
			if seen && !answered {
				answered = true
				onPrompt()
				_, _ = inW.Write([]byte(answer))
				_ = inW.Close()
			}
			if rerr != nil {
				return
			}
		}
	}()
	code := runConfigSync(args)
	_ = outW.Close()
	<-done
	_ = inR.Close()
	mu.Lock()
	defer mu.Unlock()
	return buf.String(), code
}

// TestConfigSyncEditWhilePromptWaitsIsKept: a mirror planned for a prune is
// edited while the sync waits on another file's prompt. The prune re-hashes
// before it removes, so the edit is kept. Before, it was removed and survived
// only in the prune's .bak — which this change deletes, so without the
// re-hash the edit would be lost outright.
func TestConfigSyncEditWhilePromptWaitsIsKept(t *testing.T) {
	vaultPath, projDir := overrideVault(t)
	sha, _ := templates.EmbeddedSHA("commands/wrap.md")
	seedTemplateOverride(t, vaultPath, "commands/wrap.md", embeddedTemplateBytes(t, "commands/wrap.md"), sha)
	putVaultFile(t, vaultPath, "Templates/commands/restart.md", "# my restart override\n")
	wrap := filepath.Join(vaultPath, "Templates", "commands", "wrap.md")
	const edit = "# operator edit made while the prompt waited\n"

	out, code := runSyncAnsweringPrompt(t, []string{"--project-root", projDir, "--tier", "vault"},
		func() {
			if err := os.WriteFile(wrap, []byte(edit), 0o644); err != nil {
				t.Error(err)
			}
		}, "s\n")
	if code != cli.ExitOK {
		t.Fatalf("exit=%d\n%s", code, out)
	}
	if !strings.Contains(out, "Templates/commands/wrap.md changed since plan; kept") {
		t.Errorf("no changed-since-plan line:\n%s", out)
	}
	if !strings.Contains(out, "pruned=0") {
		t.Errorf("the edited file was pruned:\n%s", out)
	}
	assertFileBytes(t, wrap, edit)
}

// TestConfigSyncRefusedOnANewerVault pins the old-binary refusal the surface
// bump buys: a binary older than the vault's surface stamp cannot run the
// destructive template commands — `config sync` and the two reset verbs (the
// upgrade commands no longer write the vault). Simulated here by stamping one
// version above this binary's, and driven through the real dispatch so the
// preRun gate runs.
func TestConfigSyncRefusedOnANewerVault(t *testing.T) {
	vaultPath, _ := overrideVault(t)
	putVaultFile(t, vaultPath, "Templates/commands/wrap.md", myWrap)
	putVaultFile(t, vaultPath, "Templates/skills/chair/SKILL.md", "---\nname: chair\ndescription: mine\n---\nmy chair\n")
	gitifyVault(t, vaultPath)
	seedCommittedOverrideUnderMirror(t, vaultPath, "commands/restart.md")
	putVaultFile(t, vaultPath, "Templates/commands/restart.md", "# an uncommitted restart override\n")
	if err := surface.WriteStamp(filepath.Join(vaultPath, "Projects", "ovr"), surface.MCPSurfaceVersion+1, ""); err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD"))
	before := treeDigest(t, filepath.Join(vaultPath, "Templates"))

	for _, args := range [][]string{
		{"config", "sync", "--yes"},
		{"commands", "reset", "restart"},
		{"skills", "reset", "chair"},
	} {
		info := cli.BuildInfo{Version: "test"}
		reg := cli.NewRegistry(info)
		reg.SetPreRun(preRun)
		registerAll(reg, info)
		var code int
		errOut := captureStderr(t, func() {
			_ = captureStdout(t, func() { code = reg.Dispatch(args) })
		})
		if code != cli.ExitSystem {
			t.Errorf("vp %s: exit %d, want %d (ExitSystem)", strings.Join(args, " "), code, cli.ExitSystem)
		}
		if !strings.Contains(errOut, "git pull && make install") {
			t.Errorf("vp %s: no remediation in stderr:\n%s", strings.Join(args, " "), errOut)
		}
	}
	if got := treeDigest(t, filepath.Join(vaultPath, "Templates")); got != before {
		t.Error("Templates/ changed under a refused command")
	}
	if got := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD")); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
}

// treeDigest is every file under root with its bytes, for a byte-identity
// check of a whole tree.
func treeDigest(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, p)
		b.WriteString(rel + "\x00" + string(data) + "\x00")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// isolateGitIdentity makes git unable to find a committer identity for the
// vault: no global or system config, no identity variables, and
// user.useConfigOnly so git will not guess one from the hostname.
func isolateGitIdentity(t *testing.T, vaultPath string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	gitInVault(t, vaultPath, "config", "--unset", "user.email")
	gitInVault(t, vaultPath, "config", "--unset", "user.name")
	gitInVault(t, vaultPath, "config", "user.useConfigOnly", "true")
}

// TestConfigSyncGitFailureRemovesNothing is review H1: a git failure used to
// strike after the prune had removed the file, leaving a committed override
// deleted with its lock entry gone. Now nothing is removed until git has
// answered, a committed override is restored in place, and a mirror whose
// removal cannot be committed is kept — with a non-zero exit either way.
func TestConfigSyncGitFailureRemovesNothing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		breakGit func(t *testing.T, vaultPath string)
		want     string
	}{
		{"no-identity", isolateGitIdentity, "identity"},
		{"corrupt-index", func(t *testing.T, vaultPath string) {
			if err := os.WriteFile(filepath.Join(vaultPath, ".git", "index"), []byte("not an index"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "prune deferred"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vaultPath, projDir := overrideVault(t)
			putVaultFile(t, vaultPath, "Templates/commands/wrap.md", string(embeddedTemplateBytes(t, "commands/wrap.md")))
			gitifyVault(t, vaultPath)
			sha, _ := templates.EmbeddedSHA("commands/wrap.md")
			seedTemplateOverride(t, vaultPath, "commands/wrap.md", embeddedTemplateBytes(t, "commands/wrap.md"), sha)
			wrap := filepath.Join(vaultPath, "Templates", "commands", "wrap.md")
			tc.breakGit(t, vaultPath)

			var code int
			var out string
			errOut := captureStderr(t, func() {
				out, code = runSyncWithStdin(t, "", []string{"--project-root", projDir, "--tier", "vault", "--yes"})
			})
			if code == cli.ExitOK {
				t.Errorf("exit 0 on a prune git could not verify\n%s\n%s", out, errOut)
			}
			if !strings.Contains(out, "[Skip] TemplateTree:Templates: Templates/commands/wrap.md — ") || !strings.Contains(out+errOut, tc.want) {
				t.Errorf("no deferral row naming %q:\n%s\n%s", tc.want, out, errOut)
			}
			if !strings.Contains(out, "pruned=0") {
				t.Errorf("counted as pruned:\n%s", out)
			}
			if _, err := os.Stat(wrap); err != nil {
				t.Errorf("the file was removed: %v", err)
			}
			if l, _ := templates.ReadLock(vaultPath); l.Entries["Templates/commands/wrap.md"].EmbeddedSHA == "" {
				t.Error("the lock entry was dropped although nothing was pruned")
			}
		})
	}
}

// TestConfigSyncRemoteOverrideBlocksPrune is review M1's two-host scenario:
// host B pushed an override of a path this host still holds as a mirror. The
// sync fetches, sees operator content at the remote tip, and neither removes
// nor commits — so no local prune commit strands behind B's override, and no
// later merge can resolve B's override away.
func TestConfigSyncRemoteOverrideBlocksPrune(t *testing.T) {
	vaultPath, projDir := overrideVault(t)
	putVaultFile(t, vaultPath, "Templates/commands/wrap.md", string(embeddedTemplateBytes(t, "commands/wrap.md")))
	origin := gitifyVault(t, vaultPath)
	sha, _ := templates.EmbeddedSHA("commands/wrap.md")
	seedTemplateOverride(t, vaultPath, "commands/wrap.md", embeddedTemplateBytes(t, "commands/wrap.md"), sha)
	head := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD"))

	hostB := cloneVault(t, origin)
	gitInVault(t, hostB, "config", "user.email", "b@test.com")
	gitInVault(t, hostB, "config", "user.name", "B")
	putVaultFile(t, hostB, "Templates/commands/wrap.md", "# host B's override\n")
	gitInVault(t, hostB, "commit", "-qam", "override from host B")
	gitInVault(t, hostB, "push", "-q", "origin", "main")
	tip := strings.TrimSpace(gitInVault(t, origin, "rev-parse", "main"))

	out := syncVault(t, projDir, "", "--yes")
	if !strings.Contains(out, "origin/main holds operator content") {
		t.Errorf("no remote-override deferral:\n%s", out)
	}
	if got := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD")); got != head {
		t.Errorf("a prune commit was made behind host B's override: %s", gitInVault(t, vaultPath, "log", "-1", "--stat"))
	}
	if got := strings.TrimSpace(gitInVault(t, origin, "rev-parse", "main")); got != tip {
		t.Errorf("origin moved to %s", got)
	}
	if _, err := os.Stat(filepath.Join(vaultPath, "Templates", "commands", "wrap.md")); err != nil {
		t.Errorf("the mirror was removed: %v", err)
	}
}

// TestConfigSyncRejectedPushIsReported: a prune commit the remote refuses is
// reported — the commit, the paths, the rule that a remote's copy is operator
// content to keep — and the run exits non-zero instead of 0.
func TestConfigSyncRejectedPushIsReported(t *testing.T) {
	vaultPath, projDir := overrideVault(t)
	putVaultFile(t, vaultPath, "Templates/commands/wrap.md", string(embeddedTemplateBytes(t, "commands/wrap.md")))
	origin := gitifyVault(t, vaultPath)
	sha, _ := templates.EmbeddedSHA("commands/wrap.md")
	seedTemplateOverride(t, vaultPath, "commands/wrap.md", embeddedTemplateBytes(t, "commands/wrap.md"), sha)
	if err := os.WriteFile(filepath.Join(origin, "hooks", "pre-receive"), []byte("#!/bin/sh\necho refused >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var code int
	errOut := captureStderr(t, func() {
		_, code = runSyncWithStdin(t, "", []string{"--project-root", projDir, "--tier", "vault", "--yes"})
	})
	if code == cli.ExitOK {
		t.Error("exit 0 although the prune commit did not reach origin")
	}
	for _, want := range []string{"did not reach origin", "Templates/commands/wrap.md", "operator content: keep it"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut)
		}
	}
}

// TestConfigSyncRetriesAnUncommittedPrune is case 1b end to end: a mirror whose
// removal was never committed (the entry is kept for exactly this) has its
// removal committed by the next sync, rather than being forgotten.
func TestConfigSyncRetriesAnUncommittedPrune(t *testing.T) {
	vaultPath, projDir := overrideVault(t)
	wrap := putVaultFile(t, vaultPath, "Templates/commands/wrap.md", string(embeddedTemplateBytes(t, "commands/wrap.md")))
	gitifyVault(t, vaultPath)
	sha, _ := templates.EmbeddedSHA("commands/wrap.md")
	seedTemplateOverride(t, vaultPath, "commands/wrap.md", embeddedTemplateBytes(t, "commands/wrap.md"), sha)
	if err := os.Remove(wrap); err != nil {
		t.Fatal(err)
	}
	out := syncVault(t, projDir, "", "--yes")
	if names := strings.TrimSpace(gitInVault(t, vaultPath, "show", "--name-status", "--format=", "HEAD")); names != "D\tTemplates/commands/wrap.md" {
		t.Errorf("the pending removal was not committed (%q):\n%s", names, out)
	}
	if !strings.Contains(out, "pruned=0") {
		t.Errorf("an already-removed file was counted as pruned:\n%s", out)
	}
	if l, _ := templates.ReadLock(vaultPath); l.Entries["Templates/commands/wrap.md"].EmbeddedSHA != "" {
		t.Error("the entry survived a committed removal")
	}
}

// TestConfigSyncNeverWritesAnEnclosingRepo is re-verification RV-H1. A vault
// nested in a project (or dotfiles) repository with a remote and an unpushed
// commit used to make `vp config sync` rebase that repository's branch, commit
// a prune into it and push it — the operator's unpushed work included. The
// enclosing repository is not the vault's: vp may read it and restore a vault
// path from its HEAD, but its HEAD, index, branch position and remote must be
// exactly what they were.
//
// Both shapes are covered: the vault tracked by the enclosing repository (a
// tracked mirror, kept with a reason, beside a committed override restored in
// place), and the vault gitignored there (an untracked mirror, removed) with
// the enclosing branch ahead 1, behind 1 — the state the rebase fired on.
func TestConfigSyncNeverWritesAnEnclosingRepo(t *testing.T) {
	for _, tracked := range []bool{true, false} {
		name := "vault-gitignored"
		if tracked {
			name = "vault-tracked"
		}
		t.Run(name, func(t *testing.T) {
			_, _ = initTestEnv(t, false)
			projDir := t.TempDir()
			markProjectDir(t, projDir)
			parent := t.TempDir()
			gitInVault(t, parent, "init", "-q", "-b", "main")
			gitInVault(t, parent, "config", "user.email", "test@test.com")
			gitInVault(t, parent, "config", "user.name", "Test")
			vaultPath := filepath.Join(parent, "notes", "vault")
			emb := string(embeddedTemplateBytes(t, "commands/wrap.md"))
			if tracked {
				putVaultFile(t, vaultPath, "Templates/commands/wrap.md", myWrap)
				putVaultFile(t, vaultPath, "Templates/commands/restart.md", string(embeddedTemplateBytes(t, "commands/restart.md")))
				putVaultFile(t, parent, ".gitignore", ".vp-locks/\n*.bak\n")
			} else {
				putVaultFile(t, parent, ".gitignore", "notes/vault/\n")
			}
			putVaultFile(t, parent, "README.md", "project\n")
			gitInVault(t, parent, "add", "-A")
			gitInVault(t, parent, "commit", "-qm", "project")
			origin := filepath.Join(t.TempDir(), "origin.git")
			gitInVault(t, parent, "init", "-q", "--bare", "-b", "main", origin)
			gitInVault(t, parent, "remote", "add", "origin", origin)
			gitInVault(t, parent, "push", "-q", "-u", "origin", "main")
			// The remote advances (another clone), and the project gains an
			// unpushed commit: ahead 1, behind 1 once fetched.
			other := cloneVault(t, origin)
			gitInVault(t, other, "config", "user.email", "o@test.com")
			gitInVault(t, other, "config", "user.name", "O")
			putVaultFile(t, other, "OTHER.md", "from elsewhere\n")
			gitInVault(t, other, "add", "-A")
			gitInVault(t, other, "commit", "-qm", "remote advance")
			gitInVault(t, other, "push", "-q", "origin", "main")
			putVaultFile(t, parent, "WIP.md", "not for push\n")
			gitInVault(t, parent, "add", "WIP.md")
			gitInVault(t, parent, "commit", "-qm", "LOCAL WIP - not for push")

			if code := cmdInit(cli.BuildInfo{Version: "test"}).Run(
				[]string{projDir, "--name", "ovr", "--vault-path", vaultPath, "--no-git"}); code != cli.ExitOK {
				t.Fatalf("init exit code = %d", code)
			}
			cwd, _ := os.Getwd()
			if err := os.Chdir(projDir); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chdir(cwd) })
			sha, _ := templates.EmbeddedSHA("commands/wrap.md")
			seedTemplateOverride(t, vaultPath, "commands/wrap.md", []byte(emb), sha)
			if tracked {
				rsha, _ := templates.EmbeddedSHA("commands/restart.md")
				seedTemplateOverride(t, vaultPath, "commands/restart.md", embeddedTemplateBytes(t, "commands/restart.md"), rsha)
			}

			state := func() string {
				return strings.Join([]string{
					gitInVault(t, parent, "rev-parse", "HEAD"),
					gitInVault(t, parent, "symbolic-ref", "HEAD"),
					gitInVault(t, parent, "ls-files", "-s"),
					gitInVault(t, parent, "for-each-ref"),
					gitInVault(t, origin, "rev-parse", "main"),
					gitInVault(t, parent, "reflog", "-n", "5"),
				}, "\n")
			}
			before := state()

			out := syncVault(t, projDir, "", "--yes")

			if after := state(); after != before {
				t.Errorf("the enclosing repository changed:\n--- before\n%s\n--- after\n%s\n--- sync\n%s", before, after, out)
			}
			wrap := filepath.Join(vaultPath, "Templates", "commands", "wrap.md")
			restart := filepath.Join(vaultPath, "Templates", "commands", "restart.md")
			if tracked {
				assertFileBytes(t, wrap, myWrap)
				if !strings.Contains(out, "restored Templates/commands/wrap.md from HEAD") {
					t.Errorf("the committed override was not restored:\n%s", out)
				}
				if _, err := os.Stat(restart); err != nil {
					t.Errorf("a tracked mirror was removed in an enclosing repository: %v", err)
				}
				if !strings.Contains(out, "inside another repository") {
					t.Errorf("no [Skip] row saying why:\n%s", out)
				}
			} else if _, err := os.Stat(wrap); !os.IsNotExist(err) {
				t.Errorf("the untracked mirror was not removed (err=%v)", err)
			}
		})
	}
}

// TestConfigSyncCommitFailureSelfHeals is RV-M1: a prune commit that fails
// (a pre-commit hook) exits 2 with the restore command and leaves nothing
// staged, so the next sync — once the hook passes — commits the removal
// instead of calling vp's own deletion someone else's staged change.
func TestConfigSyncCommitFailureSelfHeals(t *testing.T) {
	vaultPath, projDir := overrideVault(t)
	putVaultFile(t, vaultPath, "Templates/commands/wrap.md", string(embeddedTemplateBytes(t, "commands/wrap.md")))
	gitifyVault(t, vaultPath)
	sha, _ := templates.EmbeddedSHA("commands/wrap.md")
	seedTemplateOverride(t, vaultPath, "commands/wrap.md", embeddedTemplateBytes(t, "commands/wrap.md"), sha)
	hook := filepath.Join(vaultPath, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var code int
	errOut := captureStderr(t, func() {
		_, code = runSyncWithStdin(t, "", []string{"--project-root", projDir, "--tier", "vault", "--yes"})
	})
	if code == cli.ExitOK {
		t.Error("exit 0 on a failed prune commit")
	}
	if !strings.Contains(errOut, "checkout HEAD -- Templates/commands/wrap.md") {
		t.Errorf("no restore command:\n%s", errOut)
	}
	if staged := strings.TrimSpace(gitInVault(t, vaultPath, "diff", "--cached", "--name-only")); staged != "" {
		t.Errorf("vp's deletion is still staged: %q", staged)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	syncVault(t, projDir, "", "--yes")
	if names := strings.TrimSpace(gitInVault(t, vaultPath, "show", "--name-status", "--format=", "HEAD")); names != "D\tTemplates/commands/wrap.md" {
		t.Errorf("the next sync did not commit the removal: %q", names)
	}
}

// TestConfigSyncUnreachableRemoteDefersPrune is RV-M2: offline, the remote
// tip cannot be checked for a newer override, so the tracked prune is kept
// ("remote not verified") and the run exits non-zero — never judged against a
// stale tracking ref.
func TestConfigSyncUnreachableRemoteDefersPrune(t *testing.T) {
	vaultPath, projDir := overrideVault(t)
	putVaultFile(t, vaultPath, "Templates/commands/wrap.md", string(embeddedTemplateBytes(t, "commands/wrap.md")))
	gitifyVault(t, vaultPath)
	sha, _ := templates.EmbeddedSHA("commands/wrap.md")
	seedTemplateOverride(t, vaultPath, "commands/wrap.md", embeddedTemplateBytes(t, "commands/wrap.md"), sha)
	gitInVault(t, vaultPath, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "offline.git"))
	head := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD"))

	var code int
	var out string
	_ = captureStderr(t, func() {
		out, code = runSyncWithStdin(t, "", []string{"--project-root", projDir, "--tier", "vault", "--yes"})
	})
	if code == cli.ExitOK {
		t.Errorf("exit 0 with the remote unverified:\n%s", out)
	}
	if !strings.Contains(out, "remote not verified") {
		t.Errorf("no deferral row:\n%s", out)
	}
	if got := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD")); got != head {
		t.Errorf("a prune was committed offline: %s", gitInVault(t, vaultPath, "log", "-1", "--stat"))
	}
	if _, err := os.Stat(filepath.Join(vaultPath, "Templates", "commands", "wrap.md")); err != nil {
		t.Errorf("the mirror was removed: %v", err)
	}
}

// TestResolveTemplatePromptsEdges covers the resolver's non-happy paths: q
// aborts, an uppercase N writes a .new for every remaining Prompt, and a
// Prompt missing its embedded relpath (or naming no resource) is skipped
// without writing anything.
func TestResolveTemplatePromptsEdges(t *testing.T) {
	dir := t.TempDir()
	tt := reconcile.NewTemplateTree(dir, "Templates", reconcile.TemplateTreeSeed{Mode: reconcile.TemplateModeMaterialize})
	prompt := func(name string, details ...string) reconcile.Action {
		return reconcile.Action{Kind: reconcile.ActionPrompt, Target: filepath.Join(dir, "Templates", name), Summary: "Templates/" + name + " diverged", Details: details}
	}
	var w bytes.Buffer
	batch := ""
	if _, abort := resolveTemplatePrompts(tt, []reconcile.Action{prompt("a.md")}, bufio.NewReader(strings.NewReader("q\n")), &batch, dir, &w); !abort {
		t.Error("q did not abort")
	}

	batch = ""
	actions := []reconcile.Action{
		prompt("commands/wrap.md", "embedded_relpath=commands/wrap.md"),
		prompt("commands/restart.md", "embedded_relpath=commands/restart.md"),
		prompt("commands/none.md"),
		prompt("commands/ghost.md", "embedded_relpath=commands/no-such-template.md"),
		{Kind: reconcile.ActionUnchanged, Target: "x"},
	}
	var errOut string
	var resolved []reconcile.Action
	errOut = captureStderr(t, func() {
		resolved, _ = resolveTemplatePrompts(tt, actions, bufio.NewReader(strings.NewReader("N\n")), &batch, dir, &w)
	})
	if len(resolved) != 1 || batch != "n" {
		t.Errorf("resolved=%v batch=%q", resolved, batch)
	}
	for _, name := range []string{"wrap.md", "restart.md"} {
		if _, err := os.Stat(filepath.Join(dir, "Templates", "commands", name+".new")); err != nil {
			t.Errorf("N did not write %s.new: %v", name, err)
		}
	}
	for _, name := range []string{"none.md", "ghost.md"} {
		if _, err := os.Stat(filepath.Join(dir, "Templates", "commands", name+".new")); err == nil {
			t.Errorf("wrote a sidecar for %s", name)
		}
	}
	if !strings.Contains(errOut, "missing embedded_relpath") || !strings.Contains(errOut, "no embedded resource") {
		t.Errorf("stderr = %q", errOut)
	}
	if vaultRelOf(dir, "/elsewhere/x.md") != "/elsewhere/x.md" || vaultRelOf("", "y") != "y" {
		t.Error("vaultRelOf rewrote a path outside the vault")
	}
}
