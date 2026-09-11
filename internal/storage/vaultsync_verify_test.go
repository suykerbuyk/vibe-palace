// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// acceptOnly returns a verifier that accepts exactly the listed bodies (as the
// vp mirror bytes) and records the last HEAD-or-remote blob it was shown per
// path in seen.
func acceptOnly(seen map[string][]byte, bodies ...string) PruneVerifier {
	ok := map[string]bool{}
	for _, b := range bodies {
		ok[b] = true
	}
	return PruneVerifier{
		Accept: func(rel string, content []byte) bool {
			if seen != nil {
				seen[rel] = append([]byte(nil), content...)
			}
			return ok[string(content)]
		},
		Message: func(committed []string) string {
			return "pruned: " + strings.Join(committed, ",")
		},
	}
}

// committedRepo is a repo whose second commit tracks each file in files.
func committedRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := initTestRepo(t)
	for rel, body := range files {
		writeFile(t, dir, rel, body)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "tracked files")
	return dir
}

func readBody(t *testing.T, dir, rel string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

func cleanT(t *testing.T, dir string) {
	t.Helper()
	if st := gitRun(t, dir, "status", "--porcelain", "--", "T/"); st != "" {
		t.Errorf("T/ not clean: %q", st)
	}
}

func TestPruneMirrorsVerified_RemovesAndCommitsAMirror(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mirror\n"})
	res, out, err := PruneMirrorsVerified(dir, []string{"T/wrap.md"}, false, acceptOnly(nil, "mirror\n"))
	if err != nil {
		t.Fatal(err)
	}
	if res.CommitSHA == "" || len(out.Removed) != 1 || len(out.Committed) != 1 {
		t.Fatalf("res=%+v out=%+v", res, out)
	}
	if _, present := readBody(t, dir, "T/wrap.md"); present {
		t.Error("mirror not removed")
	}
	if got := gitRun(t, dir, "log", "-1", "--format=%s"); got != "pruned: T/wrap.md" {
		t.Errorf("message = %q", got)
	}
	cleanT(t, dir)
}

// TestPruneMirrorsVerified_RestoresInsteadOfRemoving is the H1 fix: a mirror in
// the worktree over operator content in HEAD is never removed — HEAD's copy is
// put back, and nothing is committed.
func TestPruneMirrorsVerified_RestoresInsteadOfRemoving(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "operator\n"})
	head := gitRun(t, dir, "rev-parse", "HEAD")
	writeFile(t, dir, "T/wrap.md", "mirror\n")
	res, out, err := PruneMirrorsVerified(dir, []string{"T/wrap.md"}, false, acceptOnly(nil, "mirror\n"))
	if err != nil {
		t.Fatal(err)
	}
	if res.CommitSHA != "" || gitRun(t, dir, "rev-parse", "HEAD") != head {
		t.Error("committed")
	}
	if len(out.Restored) != 1 || len(out.Removed) != 0 {
		t.Errorf("out = %+v", out)
	}
	if got, _ := readBody(t, dir, "T/wrap.md"); got != "operator\n" {
		t.Errorf("worktree = %q", got)
	}
	cleanT(t, dir)
}

func TestPruneMirrorsVerified_CommitsOnlyAccepted(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/a.md": "mirror\n", "T/b.md": "operator\n"})
	writeFile(t, dir, "T/b.md", "mirror\n")
	res, out, err := PruneMirrorsVerified(dir, []string{"T/a.md", "T/b.md"}, false, acceptOnly(nil, "mirror\n"))
	if err != nil || res.CommitSHA == "" {
		t.Fatalf("%v %+v", err, res)
	}
	if names := gitRun(t, dir, "show", "--name-status", "--format=", "HEAD"); names != "D\tT/a.md" {
		t.Errorf("commit carries %q", names)
	}
	if len(out.Restored) != 1 || out.Restored[0] != "T/b.md" {
		t.Errorf("Restored = %v", out.Restored)
	}
	cleanT(t, dir)
}

func TestPruneMirrorsVerified_UntrackedMirrorRemovedWithoutCommit(t *testing.T) {
	dir := initTestRepo(t)
	head := gitRun(t, dir, "rev-parse", "HEAD")
	writeFile(t, dir, "T/new.md", "mirror\n")
	res, out, err := PruneMirrorsVerified(dir, []string{"T/new.md"}, false, acceptOnly(nil, "mirror\n"))
	if err != nil {
		t.Fatal(err)
	}
	if res.CommitSHA != "" || gitRun(t, dir, "rev-parse", "HEAD") != head {
		t.Error("an untracked removal was committed")
	}
	if len(out.Removed) != 1 {
		t.Errorf("out = %+v", out)
	}
}

func TestPruneMirrorsVerified_ChangedWorktreeIsKept(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mirror\n"})
	writeFile(t, dir, "T/wrap.md", "a fresh edit\n")
	_, out, err := PruneMirrorsVerified(dir, []string{"T/wrap.md"}, false, acceptOnly(nil, "mirror\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Kept) != 1 || !strings.Contains(out.Kept[0].Reason, "changed since plan") {
		t.Errorf("Kept = %+v", out.Kept)
	}
	if got, _ := readBody(t, dir, "T/wrap.md"); got != "a fresh edit\n" {
		t.Errorf("edit lost: %q", got)
	}
}

// TestPruneMirrorsVerified_ExactBytes is the gitCmd-trim trap: the HEAD blob
// shown to Accept keeps its trailing whitespace and newlines.
func TestPruneMirrorsVerified_ExactBytes(t *testing.T) {
	body := "  leading\ntrailing spaces   \n\n\n"
	dir := committedRepo(t, map[string]string{"T/ws.md": body})
	seen := map[string][]byte{}
	if _, _, err := PruneMirrorsVerified(dir, []string{"T/ws.md"}, false, acceptOnly(seen, body)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(seen["T/ws.md"], []byte(body)) {
		t.Errorf("Accept saw %q", seen["T/ws.md"])
	}
	if gitRun(t, dir, "log", "-1", "--format=%s") != "pruned: T/ws.md" {
		t.Error("exact-byte match was not accepted")
	}
}

// TestPruneMirrorsVerified_ChecksTheHeadAfterReconcile: the HEAD judged is the
// one the commit lands on, after reconcileIfAhead moved it onto the remote tip.
func TestPruneMirrorsVerified_ChecksTheHeadAfterReconcile(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mirror\n"})
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")
	writeFile(t, dir, "prior.txt", "stranded\n")
	gitRun(t, dir, "add", "prior.txt")
	gitRun(t, dir, "commit", "-m", "prior stranded commit")
	other := t.TempDir()
	gitRun(t, other, "clone", "-b", "main", bare, ".")
	gitRun(t, other, "config", "user.email", "o@example.com")
	gitRun(t, other, "config", "user.name", "O")
	writeFile(t, other, "remote.txt", "from other\n")
	gitRun(t, other, "add", "-A")
	gitRun(t, other, "commit", "-m", "remote advance")
	gitRun(t, other, "push", "origin", "main")

	var headHadRemote bool
	v := acceptOnly(nil, "mirror\n")
	inner := v.Accept
	v.Accept = func(rel string, b []byte) bool {
		headHadRemote = headHadRemote || exec.Command("git", "-C", dir, "cat-file", "-e", "HEAD:remote.txt").Run() == nil
		return inner(rel, b)
	}
	res, _, err := PruneMirrorsVerified(dir, []string{"T/wrap.md"}, true, v)
	if err != nil {
		t.Fatal(err)
	}
	if !headHadRemote {
		t.Error("the HEAD check ran before reconcileIfAhead moved HEAD")
	}
	if !res.AnyPushed() {
		t.Errorf("push results: %v", res.RemoteResults)
	}
}

// TestPruneMirrorsVerified_StagedChangeRemovesNothing closes review L4: a path
// with a staged change used to be skipped AFTER its removal, leaving the
// deletion behind. Nothing is removed now.
func TestPruneMirrorsVerified_StagedChangeRemovesNothing(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "operator\n"})
	writeFile(t, dir, "T/wrap.md", "mirror\n")
	gitRun(t, dir, "add", "T/wrap.md")
	_, out, err := PruneMirrorsVerified(dir, []string{"T/wrap.md"}, false, acceptOnly(nil, "mirror\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Kept) != 1 || !strings.Contains(out.Kept[0].Reason, "staged change") {
		t.Errorf("Kept = %+v", out.Kept)
	}
	if _, present := readBody(t, dir, "T/wrap.md"); !present {
		t.Error("a path with a staged change was removed")
	}
	if staged := gitRun(t, dir, "diff", "--cached", "--name-only"); staged != "T/wrap.md" {
		t.Errorf("staged entry disturbed: %q", staged)
	}
}

// isolateIdentity makes git unable to find a committer identity for dir.
func isolateIdentity(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	gitRun(t, dir, "config", "--unset", "user.email")
	gitRun(t, dir, "config", "--unset", "user.name")
	gitRun(t, dir, "config", "user.useConfigOnly", "true")
}

// TestPruneMirrorsVerified_NoIdentityRemovesNothing: a host that cannot commit
// removes nothing it would have to commit (review H1, first reproduction).
func TestPruneMirrorsVerified_NoIdentityRemovesNothing(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mirror\n"})
	isolateIdentity(t, dir)
	_, out, err := PruneMirrorsVerified(dir, []string{"T/wrap.md"}, false, acceptOnly(nil, "mirror\n"))
	if err == nil {
		t.Fatal("no error for a host without an identity")
	}
	if len(out.Kept) != 1 || !strings.Contains(out.Kept[0].Reason, "identity") {
		t.Errorf("Kept = %+v", out.Kept)
	}
	if got, _ := readBody(t, dir, "T/wrap.md"); got != "mirror\n" {
		t.Errorf("file touched: %q", got)
	}
}

// TestPruneMirrorsVerified_CorruptIndexRemovesNothing: the second reproduction.
// The index read fails, which is a deferral and an error — never "untracked".
func TestPruneMirrorsVerified_CorruptIndexRemovesNothing(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mirror\n"})
	if err := os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("not an index"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, out, err := PruneMirrorsVerified(dir, []string{"T/wrap.md"}, false, acceptOnly(nil, "mirror\n"))
	if err == nil {
		t.Fatal("no error for a corrupt index")
	}
	if len(out.Kept) != 1 || !strings.Contains(out.Kept[0].Reason, "prune deferred") {
		t.Errorf("Kept = %+v", out.Kept)
	}
	if _, present := readBody(t, dir, "T/wrap.md"); !present {
		t.Error("removed despite an unreadable index")
	}
}

// TestPruneMirrorsVerified_RemoteOperatorContentIsKept is review M1: a remote
// tip holding an override this host has not pulled keeps the path.
func TestPruneMirrorsVerified_RemoteOperatorContentIsKept(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mirror\n"})
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")
	other := t.TempDir()
	gitRun(t, other, "clone", "-b", "main", bare, ".")
	gitRun(t, other, "config", "user.email", "o@example.com")
	gitRun(t, other, "config", "user.name", "O")
	writeFile(t, other, "T/wrap.md", "operator override from host B\n")
	gitRun(t, other, "commit", "-am", "override")
	gitRun(t, other, "push", "origin", "main")
	head := gitRun(t, dir, "rev-parse", "HEAD")

	res, out, err := PruneMirrorsVerified(dir, []string{"T/wrap.md"}, true, acceptOnly(nil, "mirror\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Kept) != 1 || !strings.Contains(out.Kept[0].Reason, "origin/main holds operator content") {
		t.Errorf("Kept = %+v", out.Kept)
	}
	if res.CommitSHA != "" || gitRun(t, dir, "rev-parse", "HEAD") != head {
		t.Error("the prune was committed behind a newer remote override")
	}
	if got, _ := readBody(t, dir, "T/wrap.md"); got != "mirror\n" {
		t.Errorf("file = %q", got)
	}
}

// TestPruneMirrorsVerified_RejectedPushIsVisible: a remote that refuses the
// push leaves the commit local and says so in RemoteResults, which the caller
// reports.
func TestPruneMirrorsVerified_RejectedPushIsVisible(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mirror\n"})
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")
	hook := filepath.Join(bare, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho refused >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, _, err := PruneMirrorsVerified(dir, []string{"T/wrap.md"}, true, acceptOnly(nil, "mirror\n"))
	if err != nil {
		t.Fatal(err)
	}
	if res.CommitSHA == "" || res.RemoteResults["origin"] == nil || !res.Stranded() {
		t.Errorf("res = %+v", res)
	}
}

// TestPruneMirrorsVerified_AlreadyGone covers case 1b: an earlier removal whose
// commit failed is committed now; a removal of operator content somebody else
// made is left alone.
func TestPruneMirrorsVerified_AlreadyGone(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/mirror.md": "mirror\n", "T/theirs.md": "operator\n"})
	for _, rel := range []string{"T/mirror.md", "T/theirs.md"} {
		if err := os.Remove(filepath.Join(dir, rel)); err != nil {
			t.Fatal(err)
		}
	}
	_, out, err := PruneMirrorsVerified(dir, []string{"T/mirror.md", "T/theirs.md"}, false, acceptOnly(nil, "mirror\n"))
	if err != nil {
		t.Fatal(err)
	}
	if names := gitRun(t, dir, "show", "--name-status", "--format=", "HEAD"); names != "D\tT/mirror.md" {
		t.Errorf("commit carries %q", names)
	}
	if len(out.Removed) != 0 || len(out.Gone) != 2 {
		t.Errorf("out = %+v (an already-absent file is not counted as removed)", out)
	}
	if _, present := readBody(t, dir, "T/theirs.md"); present {
		t.Error("someone else's deletion was undone")
	}
}

// TestPruneMirrorsVerified_SecondGuardOnMovedHead: HEAD moving between the
// removal and the commit (a non-vp git operation) is re-judged; a path the new
// HEAD holds as operator content is restored, not committed.
func TestPruneMirrorsVerified_SecondGuardOnMovedHead(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mirror\n"})
	orig := pruneBeforeStageHook
	t.Cleanup(func() { pruneBeforeStageHook = orig })
	pruneBeforeStageHook = func() {
		other := t.TempDir()
		gitRun(t, other, "clone", "-q", dir, ".")
		gitRun(t, other, "config", "user.email", "o@example.com")
		gitRun(t, other, "config", "user.name", "O")
		writeFile(t, other, "T/wrap.md", "operator\n")
		gitRun(t, other, "commit", "-qam", "override")
		sha := gitRun(t, other, "rev-parse", "HEAD")
		gitRun(t, dir, "fetch", "-q", other, "main")
		gitRun(t, dir, "update-ref", "HEAD", sha)
		gitRun(t, dir, "reset", "-q", "--", "T/wrap.md")
	}
	res, out, err := PruneMirrorsVerified(dir, []string{"T/wrap.md"}, false, acceptOnly(nil, "mirror\n"))
	if err != nil {
		t.Fatal(err)
	}
	if res.CommitSHA != "" || len(out.Restored) != 1 {
		t.Errorf("res=%+v out=%+v", res, out)
	}
	if got, _ := readBody(t, dir, "T/wrap.md"); got != "operator\n" {
		t.Errorf("worktree = %q", got)
	}
}

func TestPruneMirrorsVerified_GitFaultsFailClosed(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/sub/wrap.md": "operator\n"})
	if err := os.RemoveAll(filepath.Join(dir, "T", "sub")); err != nil {
		t.Fatal(err)
	}
	_, out, err := PruneMirrorsVerified(dir, []string{"T/sub"}, false, acceptOnly(nil, "mirror\n"))
	if err == nil {
		t.Fatal("a tree at the path was not an error")
	}
	if len(out.Kept) != 1 || !strings.Contains(out.Kept[0].Reason, "holds a tree") {
		t.Errorf("Kept = %+v", out.Kept)
	}
	if _, err := gitBlob(dir, strings.Repeat("0", 40)); err == nil {
		t.Error("gitBlob read a nonexistent object")
	}
}

func TestPruneMirrorsVerified_GuardsItsInputs(t *testing.T) {
	if _, _, err := PruneMirrorsVerified(t.TempDir(), []string{"x"}, false, PruneVerifier{}); err == nil {
		t.Error("an empty verifier was accepted")
	}
	if _, _, err := PruneMirrorsVerified(t.TempDir(), nil, false, acceptOnly(nil)); err == nil {
		t.Error("no paths was accepted")
	}
}

func TestPruneMirrorsVerifiedWithDowngrade(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mirror\n"})
	res, _, downgraded, err := PruneMirrorsVerifiedWithDowngrade(dir, []string{"T/wrap.md"}, true, acceptOnly(nil, "mirror\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !downgraded || res.CommitSHA == "" || res.RemoteResults != nil {
		t.Errorf("downgraded=%v res=%+v", downgraded, res)
	}
}

func TestReadCommittedBlob(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/a.md": "a\n"})
	if b, found, err := ReadCommittedBlob(dir, "T/a.md"); err != nil || !found || string(b) != "a\n" {
		t.Errorf("got %q %v %v", b, found, err)
	}
	if _, found, err := ReadCommittedBlob(dir, "T/none.md"); err != nil || found {
		t.Errorf("absent: found=%v err=%v", found, err)
	}
}

// TestInspectVaultGit covers every way git can see a vault, including the
// nested vault (no .git at the vault root) that a root-only marker check
// mistook for an unversioned one (review L2).
func TestInspectVaultGit(t *testing.T) {
	plain := t.TempDir()
	if g, _ := InspectVaultGit(plain); g != VaultNotGit {
		t.Errorf("plain dir: %v", g)
	}
	repo := initTestRepo(t)
	if g, err := InspectVaultGit(repo); g != VaultGitOK {
		t.Errorf("repo: %v %v", g, err)
	}
	nested := filepath.Join(repo, "sub", "vault")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if g, err := InspectVaultGit(nested); g != VaultGitNested {
		t.Errorf("nested: %v %v", g, err)
	}
	dangling := t.TempDir()
	if err := os.WriteFile(filepath.Join(dangling, ".git"), []byte("gitdir: /nonexistent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if g, err := InspectVaultGit(dangling); g != VaultGitBroken || err == nil {
		t.Errorf("dangling .git file: %v %v", g, err)
	}
	t.Setenv("PATH", t.TempDir())
	if g, _ := InspectVaultGit(repo); g != VaultGitUnavailable {
		t.Errorf("git missing: %v", g)
	}
}

// TestPruneMirrorsInEnclosingRepo is RV-H1 at the storage layer: a vault
// nested in another repository is verified and restored, and an untracked
// mirror is removed, but a tracked mirror is kept — its removal could only be
// finished by a commit in a repository that is not the vault's — and the
// enclosing repository's HEAD and index are untouched.
func TestPruneMirrorsInEnclosingRepo(t *testing.T) {
	repo := committedRepo(t, map[string]string{"vault/T/wrap.md": "operator\n", "vault/T/m.md": "mirror\n"})
	vault := filepath.Join(repo, "vault")
	writeFile(t, vault, "T/wrap.md", "mirror\n")
	writeFile(t, vault, "T/untracked.md", "mirror\n")
	head := gitRun(t, repo, "rev-parse", "HEAD")
	index := gitRun(t, repo, "ls-files", "-s")

	out, err := PruneMirrorsInEnclosingRepo(vault, []string{"T/wrap.md", "T/m.md", "T/untracked.md"}, acceptOnly(nil, "mirror\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Restored) != 1 || len(out.Removed) != 1 || len(out.Committed) != 0 {
		t.Errorf("out = %+v", out)
	}
	if len(out.Kept) != 1 || out.Kept[0].Path != "T/m.md" || !strings.Contains(out.Kept[0].Reason, "inside another repository") {
		t.Errorf("Kept = %+v", out.Kept)
	}
	if gitRun(t, repo, "rev-parse", "HEAD") != head || gitRun(t, repo, "ls-files", "-s") != index {
		t.Error("the enclosing repository's HEAD or index changed")
	}
	if got, _ := readBody(t, vault, "T/wrap.md"); got != "operator\n" {
		t.Errorf("worktree = %q", got)
	}
	if _, present := readBody(t, vault, "T/m.md"); !present {
		t.Error("a tracked mirror was removed in an enclosing repository")
	}
}

// TestPruneMirrorsVerified_RemoteWithoutThePathAllows: a remote tip that no
// longer holds the path (another host pruned it) does not block the prune.
func TestPruneMirrorsVerified_RemoteWithoutThePathAllows(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mirror\n"})
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")
	other := t.TempDir()
	gitRun(t, other, "clone", "-b", "main", bare, ".")
	gitRun(t, other, "config", "user.email", "o@example.com")
	gitRun(t, other, "config", "user.name", "O")
	gitRun(t, other, "rm", "-q", "T/wrap.md")
	gitRun(t, other, "commit", "-qm", "pruned elsewhere")
	gitRun(t, other, "push", "origin", "main")

	res, out, err := PruneMirrorsVerified(dir, []string{"T/wrap.md"}, true, acceptOnly(nil, "mirror\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Committed) != 1 || len(out.Kept) != 0 || !res.AnyPushed() {
		t.Errorf("res=%+v out=%+v", res, out)
	}
}

// TestPruneMirrorsVerified_UnreachableRemoteDefers is re-verification RV-M2:
// a remote that cannot be fetched means a newer override there cannot be ruled
// out, so every tracked prune is deferred instead of trusting a stale
// tracking ref.
func TestPruneMirrorsVerified_UnreachableRemoteDefers(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mirror\n"})
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")
	gitRun(t, dir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	head := gitRun(t, dir, "rev-parse", "HEAD")

	res, out, err := PruneMirrorsVerified(dir, []string{"T/wrap.md"}, true, acceptOnly(nil, "mirror\n"))
	if err == nil {
		t.Error("no error although the remote could not be verified")
	}
	if len(out.Kept) != 1 || !strings.Contains(out.Kept[0].Reason, "remote not verified") {
		t.Errorf("Kept = %+v", out.Kept)
	}
	if res.CommitSHA != "" || gitRun(t, dir, "rev-parse", "HEAD") != head {
		t.Error("a prune was committed against an unverified remote")
	}
	if _, present := readBody(t, dir, "T/wrap.md"); !present {
		t.Error("removed")
	}
}

// TestPruneMirrorsVerified_CommitFailureUnstages is RV-M1: a commit that fails
// (here a pre-commit hook) must not leave vp's deletion staged, or every later
// sync reads it as someone else's staged change and never retries.
func TestPruneMirrorsVerified_CommitFailureUnstages(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mirror\n"})
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, out, err := PruneMirrorsVerified(dir, []string{"T/wrap.md"}, false, acceptOnly(nil, "mirror\n"))
	if err == nil || len(out.Failed) != 1 {
		t.Fatalf("err=%v out=%+v", err, out)
	}
	if staged := gitRun(t, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("vp's deletion is still staged: %q", staged)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	_, out, err = PruneMirrorsVerified(dir, []string{"T/wrap.md"}, false, acceptOnly(nil, "mirror\n"))
	if err != nil || len(out.Committed) != 1 {
		t.Fatalf("the retry did not commit: err=%v out=%+v", err, out)
	}
	if names := gitRun(t, dir, "show", "--name-status", "--format=", "HEAD"); names != "D\tT/wrap.md" {
		t.Errorf("commit carries %q", names)
	}
}
