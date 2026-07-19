// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"path/filepath"
	"slices"
	"testing"
)

// repoWithRemote builds a local repo (seed commit + identity) wired to a fresh
// bare remote named "origin" with main pushed, so origin/main is a real tracking
// ref. Returns the local working dir and the bare remote path.
func repoWithRemote(t *testing.T) (dir, bare string) {
	t.Helper()
	dir = initTestRepo(t)
	bare = initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")
	return dir, bare
}

// advanceRemote pushes a new commit to bare's main via a throwaway clone, so a
// local repo tracking the same bare is now genuinely behind.
func advanceRemote(t *testing.T, bare, file, content string) {
	t.Helper()
	other := t.TempDir()
	gitRun(t, other, "clone", "-b", "main", bare, ".")
	gitRun(t, other, "config", "user.email", "other@example.com")
	gitRun(t, other, "config", "user.name", "Other")
	writeFile(t, other, file, content)
	gitRun(t, other, "add", "-A")
	gitRun(t, other, "commit", "-m", "advance "+file)
	gitRun(t, other, "push", "origin", "main")
}

// commitLocal commits a new file on the local repo WITHOUT pushing, so HEAD is
// ahead of the tracking ref.
func commitLocal(t *testing.T, dir, file, content string) {
	t.Helper()
	writeFile(t, dir, file, content)
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "local "+file)
}

// TestBuildStatusReport_InSyncWithDirt drives the whole-report assembler: a
// repo in sync with its origin, plus one sweepable capture artifact and one
// non-artifact dirty file, must yield Version 2, the right per-remote flags, and
// the correct Swept/Reported dirt split.
func TestBuildStatusReport_InSyncWithDirt(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, _ := repoWithRemote(t)

	// A capture artifact (swept) and an ordinary dirty file (reported, never swept).
	writeFile(t, dir, "Projects/vibe-palace/sessions/2026-06-17.md", "session\n")
	writeFile(t, dir, "Projects/vibe-palace/resume.md", "resume\n")

	rep, err := BuildStatusReport(dir, true)
	if err != nil {
		t.Fatalf("BuildStatusReport: %v", err)
	}
	if rep.Version != 2 {
		t.Errorf("Version = %d, want 2", rep.Version)
	}
	if rep.Branch != "main" {
		t.Errorf("Branch = %q, want main", rep.Branch)
	}
	if rep.VaultPath != dir {
		t.Errorf("VaultPath = %q, want %q", rep.VaultPath, dir)
	}
	if len(rep.Remotes) != 1 || rep.Remotes[0].Remote != "origin" {
		t.Fatalf("expected one origin remote, got %#v", rep.Remotes)
	}
	o := rep.Remotes[0]
	if o.Ahead != 0 || o.Unpushed || o.Behind != 0 || !o.BehindKnown || o.Diverged {
		t.Errorf("in-sync remote flags wrong: %+v", o)
	}
	if o.LastFetched == nil {
		t.Error("expected a non-nil LastFetched after a successful fetch")
	}
	if !hasStatusPath(rep.Dirt.Swept, "Projects/vibe-palace/sessions/2026-06-17.md") {
		t.Errorf("session artifact should be Swept, got %v", rep.Dirt.Swept)
	}
	if !hasStatusPath(rep.Dirt.Reported, "Projects/vibe-palace/resume.md") {
		t.Errorf("resume.md should be Reported, got %v", rep.Dirt.Reported)
	}
	if hasStatusPath(rep.Dirt.Swept, "Projects/vibe-palace/resume.md") {
		t.Errorf("resume.md must never be Swept, got %v", rep.Dirt.Swept)
	}
}

// TestBuildStatusReport_NoRemotes confirms the zero-remote path: a clean local
// repo with no remotes yields an empty Remotes slice, no fetch, and a valid
// report (LastFetched omitted, behind unknown by construction).
func TestBuildStatusReport_NoRemotes(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir := initTestRepo(t)

	rep, err := BuildStatusReport(dir, false)
	if err != nil {
		t.Fatalf("BuildStatusReport: %v", err)
	}
	if rep.Version != 2 {
		t.Errorf("Version = %d, want 2", rep.Version)
	}
	if len(rep.Remotes) != 0 {
		t.Errorf("expected no remotes, got %#v", rep.Remotes)
	}
}

func hasStatusPath(paths []string, want string) bool {
	return slices.Contains(paths, want)
}

func TestListRemotes(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}

	// Zero remotes → nil, nil (not an error, not an empty non-nil slice).
	dir := initTestRepo(t)
	got, err := ListRemotes(dir)
	if err != nil {
		t.Fatalf("ListRemotes on zero-remote repo: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for zero remotes, got %#v", got)
	}

	// Two configured remotes → both returned.
	b1 := initBareRemote(t)
	b2 := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", b1)
	gitRun(t, dir, "remote", "add", "vault", b2)
	got, err = ListRemotes(dir)
	if err != nil {
		t.Fatalf("ListRemotes: %v", err)
	}
	set := map[string]bool{}
	for _, r := range got {
		set[r] = true
	}
	if !set["origin"] || !set["vault"] || len(got) != 2 {
		t.Errorf("expected [origin vault], got %#v", got)
	}
}

func TestAheadCount(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, _ := repoWithRemote(t)

	// In sync: HEAD == origin/main → 0.
	if n, err := aheadCount(dir, "origin", "main"); err != nil || n != 0 {
		t.Fatalf("aheadCount in-sync = %d, %v; want 0, nil", n, err)
	}

	// One unpushed local commit → 1.
	commitLocal(t, dir, "a.txt", "alpha\n")
	if n, err := aheadCount(dir, "origin", "main"); err != nil || n != 1 {
		t.Fatalf("aheadCount after one commit = %d, %v; want 1, nil", n, err)
	}

	// A second unpushed commit → 2.
	commitLocal(t, dir, "b.txt", "bravo\n")
	if n, err := aheadCount(dir, "origin", "main"); err != nil || n != 2 {
		t.Fatalf("aheadCount after two commits = %d, %v; want 2, nil", n, err)
	}

	// Unresolved tracking ref → error (never crashes, but signals unknown).
	if _, err := aheadCount(dir, "nope", "main"); err == nil {
		t.Errorf("expected error for unresolved ref, got nil")
	}
}

func TestGetRemoteStatus_InSync(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, _ := repoWithRemote(t)

	st, err := GetRemoteStatus(dir, "origin", "main", true)
	if err != nil {
		t.Fatalf("GetRemoteStatus: %v", err)
	}
	if st.Ahead != 0 || st.Behind != 0 || !st.BehindKnown {
		t.Errorf("in-sync: got Ahead=%d Behind=%d BehindKnown=%v, want 0/0/true", st.Ahead, st.Behind, st.BehindKnown)
	}
	if st.Diverged {
		t.Errorf("in-sync must not be Diverged")
	}
	if !st.Reachable {
		t.Errorf("in-sync remote must be Reachable")
	}
	if st.LastFetched.IsZero() {
		t.Errorf("expected a non-zero LastFetched after a successful fetch")
	}
}

func TestGetRemoteStatus_AheadOnly(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, _ := repoWithRemote(t)
	commitLocal(t, dir, "local.txt", "unpushed\n")

	st, err := GetRemoteStatus(dir, "origin", "main", true)
	if err != nil {
		t.Fatalf("GetRemoteStatus: %v", err)
	}
	if st.Ahead != 1 {
		t.Errorf("ahead-only: got Ahead=%d, want 1", st.Ahead)
	}
	if st.Behind != 0 || !st.BehindKnown {
		t.Errorf("ahead-only: got Behind=%d BehindKnown=%v, want 0/true", st.Behind, st.BehindKnown)
	}
	if st.Diverged {
		t.Errorf("ahead-only must not be Diverged")
	}
}

func TestGetRemoteStatus_BehindOnly(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, bare := repoWithRemote(t)
	advanceRemote(t, bare, "remote.txt", "from elsewhere\n")

	// fetch=true: GetRemoteStatus performs the fetch, so the advance is seen.
	st, err := GetRemoteStatus(dir, "origin", "main", true)
	if err != nil {
		t.Fatalf("GetRemoteStatus: %v", err)
	}
	if st.Ahead != 0 {
		t.Errorf("behind-only: got Ahead=%d, want 0", st.Ahead)
	}
	if st.Behind != 1 || !st.BehindKnown {
		t.Errorf("behind-only: got Behind=%d BehindKnown=%v, want 1/true", st.Behind, st.BehindKnown)
	}
	if st.Diverged {
		t.Errorf("behind-only must not be Diverged")
	}
}

func TestGetRemoteStatus_Diverged(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, bare := repoWithRemote(t)
	commitLocal(t, dir, "local.txt", "unpushed\n")           // ahead
	advanceRemote(t, bare, "remote.txt", "from elsewhere\n") // behind

	st, err := GetRemoteStatus(dir, "origin", "main", true)
	if err != nil {
		t.Fatalf("GetRemoteStatus: %v", err)
	}
	if st.Ahead != 1 || st.Behind != 1 || !st.BehindKnown {
		t.Errorf("diverged: got Ahead=%d Behind=%d BehindKnown=%v, want 1/1/true", st.Ahead, st.Behind, st.BehindKnown)
	}
	if !st.Diverged {
		t.Errorf("both-advanced state must be Diverged")
	}
}

// TestGetRemoteStatus_FetchFalseStaleNotFabricated is the H1 contract: when the
// origin has advanced but no fetch is performed, Behind must read as UNKNOWN
// (BehindKnown=false, Behind=0) — never a fabricated "in-sync" 0.
func TestGetRemoteStatus_FetchFalseStaleNotFabricated(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, bare := repoWithRemote(t)
	advanceRemote(t, bare, "remote.txt", "from elsewhere\n")

	st, err := GetRemoteStatus(dir, "origin", "main", false)
	if err != nil {
		t.Fatalf("GetRemoteStatus: %v", err)
	}
	if st.BehindKnown {
		t.Errorf("fetch=false must leave BehindKnown=false")
	}
	if st.Behind != 0 {
		t.Errorf("fetch=false must leave Behind=0 (stale, not fabricated), got %d", st.Behind)
	}
	if st.Diverged {
		t.Errorf("Diverged must be false when Behind is unknown")
	}
	// Ahead is computed against the STALE tracking ref (still seed) → 0 here.
	if st.Ahead != 0 {
		t.Errorf("fetch=false ahead against stale ref: got %d, want 0", st.Ahead)
	}
	// The remote is alive, so the cheap probe should still mark it reachable.
	if !st.Reachable {
		t.Errorf("ls-remote probe should mark a live remote Reachable on fetch=false")
	}
}

// TestGetRemoteStatus_UnreachableIsGraceful proves an unreachable remote does not
// crash the call: Reachable=false with a nil error.
func TestGetRemoteStatus_UnreachableIsGraceful(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir := initTestRepo(t)
	gitRun(t, dir, "remote", "add", "origin", filepath.Join(dir, "does-not-exist.git"))

	st, err := GetRemoteStatus(dir, "origin", "main", true)
	if err != nil {
		t.Fatalf("unreachable remote must not error the call, got: %v", err)
	}
	if st.Reachable {
		t.Errorf("dead remote must report Reachable=false")
	}
	if st.BehindKnown {
		t.Errorf("no fetch landed → BehindKnown must stay false")
	}
}

// TestGetRemoteStatus_EmptyBranchResolvesCurrent confirms an empty branch arg is
// resolved to the repo's current branch via currentBranch.
func TestGetRemoteStatus_EmptyBranchResolvesCurrent(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, _ := repoWithRemote(t)
	commitLocal(t, dir, "local.txt", "unpushed\n")

	st, err := GetRemoteStatus(dir, "origin", "", true)
	if err != nil {
		t.Fatalf("GetRemoteStatus with empty branch: %v", err)
	}
	if st.Ahead != 1 {
		t.Errorf("empty branch should resolve to main and report Ahead=1, got %d", st.Ahead)
	}
}
