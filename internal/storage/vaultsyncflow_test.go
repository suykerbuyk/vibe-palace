// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"strings"
	"testing"
)

// syncSeedRemote initializes a repo with one bare remote and pushes the seed so
// the remote has a main ref to pull/push against. Returns (dir, bare).
func syncSeedRemote(t *testing.T) (dir, bare string) {
	t.Helper()
	dir = initTestRepo(t)
	// Match production: a real vault gitignores .vp-locks/ (transient
	// advisory-lock sidecars, via CanonicalGitignorePatterns), so the repo-root
	// commit lock's persistent sidecar never surfaces as dirt in a status scan.
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatalf("reconcile vault gitignore: %v", err)
	}
	gitRun(t, dir, "add", ".gitignore")
	gitRun(t, dir, "commit", "-m", "seed vault .gitignore")
	bare = initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")
	return dir, bare
}

// TestSyncVault_CleanArtifacts drives the happy path: a vault dirty with only a
// sweepable capture artifact + one configured remote. SyncVault must commit the
// artifact locally, pull, and push it — returning a nil error with Committed
// true, the artifact commit present on the bare remote, and a clean local tree.
func TestSyncVault_CleanArtifacts(t *testing.T) {
	dir, bare := syncSeedRemote(t)
	writeFile(t, dir, "Projects/foo/sessions/x.md", "session body\n")

	res, err := SyncVault(dir, []string{"origin"})
	if err != nil {
		t.Fatalf("SyncVault: %v", err)
	}
	if res.Refused {
		t.Error("clean artifact must not be refused")
	}
	if !res.Committed {
		t.Errorf("expected Committed=true, got result %#v", res)
	}
	if len(res.GenuineDirt) != 0 {
		t.Errorf("expected no genuine dirt, got %#v", res.GenuineDirt)
	}
	// The artifact commit must be present on the bare remote.
	tree := gitRun(t, bare, "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(tree, "Projects/foo/sessions/x.md") {
		t.Errorf("artifact not present on bare remote, tree: %q", tree)
	}
	// Local tree must be clean afterward.
	if status := gitRun(t, dir, "status", "--porcelain"); status != "" {
		t.Errorf("local tree should be clean after sync, got: %q", status)
	}
}

// TestSyncVault_GenuineDirtRefusesBeforeNetwork covers finding L1: a vault dirty
// with a NON-artifact file must be refused BEFORE any network I/O. The bare
// remote ref must be UNCHANGED (nothing pushed), Pull must be nil, and Committed
// must be false.
func TestSyncVault_GenuineDirtRefusesBeforeNetwork(t *testing.T) {
	dir, bare := syncSeedRemote(t)
	beforeSHA := gitRun(t, bare, "rev-parse", "main")

	writeFile(t, dir, "some-random-file.txt", "not an artifact\n")

	res, err := SyncVault(dir, []string{"origin"})
	if err == nil {
		t.Fatal("expected refusal error for genuine dirt")
	}
	if !res.Refused {
		t.Error("expected Refused=true")
	}
	if len(res.GenuineDirt) != 1 || res.GenuineDirt[0] != "some-random-file.txt" {
		t.Errorf("expected GenuineDirt=[some-random-file.txt], got %#v", res.GenuineDirt)
	}
	if !strings.Contains(err.Error(), "some-random-file.txt") {
		t.Errorf("error should name the offending path, got %v", err)
	}
	if res.Committed {
		t.Error("must not commit when refusing")
	}
	if res.Pull != nil {
		t.Error("Pull must be nil when refusing before network I/O")
	}
	// The bare remote ref must be untouched — nothing was pushed.
	if afterSHA := gitRun(t, bare, "rev-parse", "main"); afterSHA != beforeSHA {
		t.Errorf("bare ref changed despite refusal: before=%s after=%s", beforeSHA, afterSHA)
	}
}

// TestSyncVault_MemoryDoesNotBlock covers decision 7: user memory under
// Projects/<slug>/memory/ is expected pending content, NOT genuine dirt. A vault
// dirty with only a memory file must NOT be refused; the sync succeeds and the
// memory file remains uncommitted/dirty (neither swept nor blocked).
func TestSyncVault_MemoryDoesNotBlock(t *testing.T) {
	dir, _ := syncSeedRemote(t)
	writeFile(t, dir, "Projects/foo/memory/note.md", "a personal note\n")

	res, err := SyncVault(dir, []string{"origin"})
	if err != nil {
		t.Fatalf("SyncVault must not fail on memory-only dirt: %v", err)
	}
	if res.Refused {
		t.Error("memory must not be refused")
	}
	if len(res.GenuineDirt) != 0 {
		t.Errorf("memory must not count as genuine dirt, got %#v", res.GenuineDirt)
	}
	if res.Committed {
		t.Error("memory must not be swept/committed by sync")
	}
	// The memory file remains dirty in the working tree.
	status := gitRun(t, dir, "status", "--porcelain", "-uall")
	if !strings.Contains(status, "Projects/foo/memory/note.md") {
		t.Errorf("memory file should remain dirty, status: %q", status)
	}
}

// TestSyncVault_PullConflictAbortsBeforePush is the critical FINDING A guard.
// Pull returns err == nil even on a merge conflict; the conflict lives only in
// PullResult.RemoteResults, and SyncVault must gate on the VERDICT, not the Go
// error. We create a genuine divergence on a tracked file so step 4's merge
// conflicts, then assert SyncVault returns a non-nil error AND the bare remote's
// ref was NOT advanced to the local HEAD — proving the conflicted local commit
// was never pushed.
func TestSyncVault_PullConflictAbortsBeforePush(t *testing.T) {
	dir, bare := syncSeedRemote(t)

	// Seed a tracked file on both sides from a common ancestor.
	writeFile(t, dir, "F.md", "SEED\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "seed F")
	gitRun(t, dir, "push", "origin", "main")

	// The remote advances the same line; the local repo commits a DIFFERENT
	// change to the same line — a genuine merge conflict when pulled.
	advanceRemote(t, bare, "F.md", "REMOTE\n")
	writeFile(t, dir, "F.md", "LOCAL\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "local divergent F")

	remoteBefore := gitRun(t, bare, "rev-parse", "main")
	localHEAD := gitRun(t, dir, "rev-parse", "HEAD")

	res, err := SyncVault(dir, []string{"origin"})
	if err == nil {
		t.Fatal("expected a non-nil error when the pull conflicts (verdict gate, not error gate)")
	}
	if res.Pull == nil {
		t.Fatal("Pull result should be recorded before the abort")
	}
	if res.Pull.RemoteResults["origin"] == nil {
		t.Error("the conflict must be recorded in Pull.RemoteResults[origin]")
	}
	if res.Push != nil {
		t.Error("push must never run after a conflicted pull")
	}
	// The bare ref must NOT have advanced to the conflicted local HEAD.
	remoteAfter := gitRun(t, bare, "rev-parse", "main")
	if remoteAfter != remoteBefore {
		t.Errorf("bare ref advanced despite conflict: before=%s after=%s", remoteBefore, remoteAfter)
	}
	if remoteAfter == localHEAD {
		t.Errorf("conflicted local HEAD %s was pushed to the bare remote — must not be", localHEAD)
	}
}

// TestSyncVault_DeferredInFlightTranscript proves a lone .jsonl.zst with no
// sibling manifest is Deferred: it is neither committed nor blocking, while a
// real artifact alongside it is committed and pushed. The sync must proceed
// (not refused).
func TestSyncVault_DeferredInFlightTranscript(t *testing.T) {
	dir, bare := syncSeedRemote(t)
	// A lone in-flight transcript half (no sibling manifest) + a real artifact.
	writeFile(t, dir, "Projects/foo/transcripts/x.jsonl.zst", "compressed bytes\n")
	writeFile(t, dir, "Projects/foo/sessions/y.md", "session\n")

	res, err := SyncVault(dir, []string{"origin"})
	if err != nil {
		t.Fatalf("SyncVault: %v", err)
	}
	if res.Refused {
		t.Error("a deferred transcript must not refuse the sync")
	}
	// The lone zst is deferred, not blocking.
	if len(res.Deferred) != 1 || res.Deferred[0] != "Projects/foo/transcripts/x.jsonl.zst" {
		t.Errorf("expected Deferred=[.../x.jsonl.zst], got %#v", res.Deferred)
	}
	if len(res.GenuineDirt) != 0 {
		t.Errorf("deferred transcript must not be genuine dirt, got %#v", res.GenuineDirt)
	}
	if !res.Committed {
		t.Error("the real artifact should be committed")
	}
	// The real artifact reached the bare remote; the deferred zst did not.
	tree := gitRun(t, bare, "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(tree, "Projects/foo/sessions/y.md") {
		t.Errorf("real artifact missing on bare remote, tree: %q", tree)
	}
	if strings.Contains(tree, "x.jsonl.zst") {
		t.Errorf("deferred transcript must NOT be committed/pushed, tree: %q", tree)
	}
	// The zst remains dirty locally.
	if status := gitRun(t, dir, "status", "--porcelain", "-uall"); !strings.Contains(status, "x.jsonl.zst") {
		t.Errorf("deferred transcript should remain uncommitted locally, status: %q", status)
	}
}

// TestSyncVault_ScanErrorReturnsNonNilResult guards the non-nil contract: when
// the initial classify fails (here, a directory that is not a git repo, so
// `git status` errors), SyncVault must still return a non-nil *SyncResult
// alongside the error — the front-ends dereference the result before checking
// err, so a nil here is a crash.
func TestSyncVault_ScanErrorReturnsNonNilResult(t *testing.T) {
	res, err := SyncVault(t.TempDir(), []string{"origin"})
	if err == nil {
		t.Fatal("expected an error scanning a non-git directory")
	}
	if res == nil {
		t.Fatal("SyncVault returned a nil *SyncResult on scan error — violates the non-nil contract")
	}
}
