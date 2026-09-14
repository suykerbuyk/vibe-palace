// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"path/filepath"
	"testing"
)

// TestCheckRepoFreshness_InSync mirrors TestGetRemoteStatus_InSync: a repo
// that just pushed to its only remote must report up_to_date, never
// unverified.
func TestCheckRepoFreshness_InSync(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, _ := repoWithRemote(t)

	rf, err := CheckRepoFreshness(dir, "", "", true, 5)
	if err != nil {
		t.Fatalf("CheckRepoFreshness: %v", err)
	}
	if rf.Status != RepoUpToDate {
		t.Errorf("Status = %q, want %q", rf.Status, RepoUpToDate)
	}
	if rf.Branch != "main" {
		t.Errorf("Branch = %q, want main", rf.Branch)
	}
	if len(rf.Remotes) != 1 || rf.Remotes[0].Remote != "origin" {
		t.Fatalf("expected one origin remote, got %#v", rf.Remotes)
	}
}

// TestCheckRepoFreshness_AheadOnly confirms unpushed-only local commits report
// "ahead", not "behind" or "diverged".
func TestCheckRepoFreshness_AheadOnly(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, _ := repoWithRemote(t)
	commitLocal(t, dir, "local.txt", "unpushed\n")

	rf, err := CheckRepoFreshness(dir, "", "", true, 5)
	if err != nil {
		t.Fatalf("CheckRepoFreshness: %v", err)
	}
	if rf.Status != RepoAhead {
		t.Errorf("Status = %q, want %q", rf.Status, RepoAhead)
	}
}

// TestCheckRepoFreshness_BehindOnly is the task's central case: a remote that
// advanced without the local checkout knowing must report "behind" with the
// newest upstream subject captured, never a fabricated up_to_date.
func TestCheckRepoFreshness_BehindOnly(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, bare := repoWithRemote(t)
	advanceRemote(t, bare, "remote.txt", "from elsewhere\n")

	rf, err := CheckRepoFreshness(dir, "", "", true, 5)
	if err != nil {
		t.Fatalf("CheckRepoFreshness: %v", err)
	}
	if rf.Status != RepoBehind {
		t.Errorf("Status = %q, want %q", rf.Status, RepoBehind)
	}
	if len(rf.Remotes) != 1 {
		t.Fatalf("expected one remote, got %#v", rf.Remotes)
	}
	r := rf.Remotes[0]
	if r.Behind != 1 || !r.BehindKnown {
		t.Errorf("Behind=%d BehindKnown=%v, want 1/true", r.Behind, r.BehindKnown)
	}
	if len(r.NewestUpstreamSubjects) != 1 || r.NewestUpstreamSubjects[0] != "advance remote.txt" {
		t.Errorf("NewestUpstreamSubjects = %#v, want [\"advance remote.txt\"]", r.NewestUpstreamSubjects)
	}
	if r.SubjectsTruncated {
		t.Errorf("SubjectsTruncated = true for a single commit, want false")
	}
}

// TestCheckRepoFreshness_BehindSubjectsTruncated confirms the cap and its
// truncation flag against more upstream commits than subjectLimit.
func TestCheckRepoFreshness_BehindSubjectsTruncated(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, bare := repoWithRemote(t)
	advanceRemote(t, bare, "a.txt", "one\n")
	advanceRemote(t, bare, "b.txt", "two\n")
	advanceRemote(t, bare, "c.txt", "three\n")

	rf, err := CheckRepoFreshness(dir, "", "", true, 2)
	if err != nil {
		t.Fatalf("CheckRepoFreshness: %v", err)
	}
	if rf.Status != RepoBehind {
		t.Errorf("Status = %q, want %q", rf.Status, RepoBehind)
	}
	r := rf.Remotes[0]
	if r.Behind != 3 {
		t.Errorf("Behind = %d, want 3", r.Behind)
	}
	if len(r.NewestUpstreamSubjects) != 2 {
		t.Errorf("NewestUpstreamSubjects = %#v, want 2 entries (capped)", r.NewestUpstreamSubjects)
	}
	if !r.SubjectsTruncated {
		t.Errorf("SubjectsTruncated = false with 3 commits behind a cap of 2, want true")
	}
	// Newest-first: the last-pushed commit ("advance c.txt") must lead.
	if r.NewestUpstreamSubjects[0] != "advance c.txt" {
		t.Errorf("NewestUpstreamSubjects[0] = %q, want %q (newest first)", r.NewestUpstreamSubjects[0], "advance c.txt")
	}
}

// TestCheckRepoFreshness_Diverged confirms both-advanced reports diverged.
func TestCheckRepoFreshness_Diverged(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, bare := repoWithRemote(t)
	commitLocal(t, dir, "local.txt", "unpushed\n")
	advanceRemote(t, bare, "remote.txt", "from elsewhere\n")

	rf, err := CheckRepoFreshness(dir, "", "", true, 5)
	if err != nil {
		t.Fatalf("CheckRepoFreshness: %v", err)
	}
	if rf.Status != RepoDiverged {
		t.Errorf("Status = %q, want %q", rf.Status, RepoDiverged)
	}
}

// TestCheckRepoFreshness_UnreachableIsUnverified is the task's core demand: a
// dead remote must report "unverified", never "up_to_date".
func TestCheckRepoFreshness_UnreachableIsUnverified(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir := initTestRepo(t)
	gitRun(t, dir, "remote", "add", "origin", filepath.Join(dir, "does-not-exist.git"))

	rf, err := CheckRepoFreshness(dir, "", "", true, 5)
	if err != nil {
		t.Fatalf("CheckRepoFreshness: %v", err)
	}
	if rf.Status != RepoUnverified {
		t.Errorf("Status = %q, want %q (must never fabricate up_to_date for a dead remote)", rf.Status, RepoUnverified)
	}
}

// TestCheckRepoFreshness_NoRemotesIsUnverified: a repo with zero configured
// remotes cannot be verified against anything.
func TestCheckRepoFreshness_NoRemotesIsUnverified(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir := initTestRepo(t)

	rf, err := CheckRepoFreshness(dir, "", "", true, 5)
	if err != nil {
		t.Fatalf("CheckRepoFreshness: %v", err)
	}
	if rf.Status != RepoUnverified {
		t.Errorf("Status = %q, want %q", rf.Status, RepoUnverified)
	}
	if len(rf.Remotes) != 0 {
		t.Errorf("expected no remotes, got %#v", rf.Remotes)
	}
}

// TestCheckRepoFreshness_FetchFalseIsUnverified: without a fetch, "behind" is
// unknowable per GetRemoteStatus's own rule, so status must not claim
// up_to_date even though the cached ahead count looks clean.
func TestCheckRepoFreshness_FetchFalseIsUnverified(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, bare := repoWithRemote(t)
	advanceRemote(t, bare, "remote.txt", "from elsewhere\n")

	rf, err := CheckRepoFreshness(dir, "", "", false, 5)
	if err != nil {
		t.Fatalf("CheckRepoFreshness: %v", err)
	}
	if rf.Status != RepoUnverified {
		t.Errorf("Status = %q, want %q (fetch=false must never claim a real behind count)", rf.Status, RepoUnverified)
	}
}

// TestCheckRepoFreshness_UpstreamIsCanonical: with an upstream set, only that
// remote's entry decides Status even when a second remote is behind.
func TestCheckRepoFreshness_UpstreamIsCanonical(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, _ := repoWithRemote(t)
	gitRun(t, dir, "branch", "--set-upstream-to=origin/main") // repoWithRemote pushes without -u
	secondBare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "fork", secondBare)
	gitRun(t, dir, "push", "fork", "main")
	advanceRemote(t, secondBare, "fork.txt", "advanced on fork\n")

	rf, err := CheckRepoFreshness(dir, "", "", true, 5)
	if err != nil {
		t.Fatalf("CheckRepoFreshness: %v", err)
	}
	if rf.UpstreamRemote != "origin" {
		t.Fatalf("test premise broken: UpstreamRemote = %q, want origin", rf.UpstreamRemote)
	}
	if rf.Status != RepoUpToDate {
		t.Errorf("Status = %q, want %q — canonical mode must key off the upstream remote (origin, in sync), "+
			"not the sibling remote (fork, behind)", rf.Status, RepoUpToDate)
	}
}

// TestCheckRepoFreshness_MultiRemoteNoUpstreamWorstOf: no upstream configured,
// two remotes, one behind and one without a matching branch at all — the
// worst-of across resolvable remotes wins, and the branchless remote is
// skipped rather than counted as unverified.
func TestCheckRepoFreshness_MultiRemoteNoUpstreamWorstOf(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, bare := repoWithRemote(t) // repoWithRemote pushes without -u, so no upstream is set
	advanceRemote(t, bare, "remote.txt", "from elsewhere\n")

	// A second remote with no "main" branch on it at all (an empty bare repo).
	emptyBare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "empty", emptyBare)

	rf, err := CheckRepoFreshness(dir, "", "", true, 5)
	if err != nil {
		t.Fatalf("CheckRepoFreshness: %v", err)
	}
	if rf.UpstreamRemote != "" {
		t.Fatalf("test premise broken: UpstreamRemote = %q, want empty (unset above)", rf.UpstreamRemote)
	}
	if rf.Status != RepoBehind {
		t.Errorf("Status = %q, want %q (worst-of across origin=behind and a skipped branchless remote)", rf.Status, RepoBehind)
	}
	if len(rf.Remotes) != 1 || rf.Remotes[0].Remote != "origin" {
		t.Errorf("expected only origin to contribute (empty has no matching branch), got %#v", rf.Remotes)
	}
}

// TestCheckRepoFreshness_BehindOutranksUnverified pins the worst-of PRIORITY,
// not just its arithmetic: with no upstream configured and two remotes that
// BOTH genuinely contribute an entry to Remotes — one dead (a real unverified
// result, not a skip) and one confirmed behind — the overall Status must be
// "behind", never "unverified". A confirmed problem must never be masked by an
// unrelated remote this checkout cannot reach.
func TestCheckRepoFreshness_BehindOutranksUnverified(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir := initTestRepo(t)

	// A dead remote: reachable=false, but it DOES have a resolvable ref shape
	// (git accepts the remote add), so it contributes a real unverified entry
	// rather than being skipped for lacking a matching branch.
	gitRun(t, dir, "remote", "add", "dead", filepath.Join(dir, "does-not-exist.git"))

	// A live remote that is genuinely behind.
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "mirror", bare)
	gitRun(t, dir, "push", "mirror", "main")
	advanceRemote(t, bare, "remote.txt", "from elsewhere\n")

	rf, err := CheckRepoFreshness(dir, "", "", true, 5)
	if err != nil {
		t.Fatalf("CheckRepoFreshness: %v", err)
	}
	if rf.UpstreamRemote != "" {
		t.Fatalf("test premise broken: UpstreamRemote = %q, want empty (no upstream configured)", rf.UpstreamRemote)
	}

	// The premise this test needs: BOTH remotes actually landed in Remotes —
	// a real unreachable entry alongside a real behind entry, never a skip.
	var sawUnverified, sawBehind bool
	for _, r := range rf.Remotes {
		switch r.Remote {
		case "dead":
			if r.Reachable {
				t.Fatalf("test premise broken: the dead remote reported Reachable=true: %+v", r)
			}
			sawUnverified = true
		case "mirror":
			if !(r.BehindKnown && r.Behind > 0) {
				t.Fatalf("test premise broken: the mirror remote is not genuinely behind: %+v", r)
			}
			sawBehind = true
		}
	}
	if !sawUnverified || !sawBehind {
		t.Fatalf("test premise broken: expected one real unverified entry and one real behind entry in "+
			"Remotes, got %#v (sawUnverified=%v sawBehind=%v)", rf.Remotes, sawUnverified, sawBehind)
	}

	if rf.Status != RepoBehind {
		t.Errorf("Status = %q, want %q — a confirmed BEHIND remote must outrank a merely unverified sibling, "+
			"never the reverse", rf.Status, RepoBehind)
	}
}

// TestCheckRepoFreshness_ExplicitRemoteWins: an explicit remote argument
// overrides both upstream detection and remote enumeration.
func TestCheckRepoFreshness_ExplicitRemoteWins(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, _ := repoWithRemote(t)
	secondBare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "fork", secondBare)
	gitRun(t, dir, "push", "fork", "main")
	advanceRemote(t, secondBare, "fork.txt", "advanced on fork\n")

	rf, err := CheckRepoFreshness(dir, "fork", "", true, 5)
	if err != nil {
		t.Fatalf("CheckRepoFreshness: %v", err)
	}
	if rf.UpstreamRemote != "" {
		t.Errorf("UpstreamRemote should be empty when remote is explicit, got %q", rf.UpstreamRemote)
	}
	if rf.Status != RepoBehind {
		t.Errorf("Status = %q, want %q (explicit remote must pin to fork, ignoring origin's in-sync state)", rf.Status, RepoBehind)
	}
}

// TestCheckRepoFreshness_BadPathErrors confirms a non-directory / non-repo
// path is a hard Go error, not a freshness verdict.
func TestCheckRepoFreshness_BadPathErrors(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	if _, err := CheckRepoFreshness(filepath.Join(t.TempDir(), "does-not-exist"), "", "", true, 5); err == nil {
		t.Error("expected an error for a nonexistent path")
	}

	notARepo := t.TempDir()
	if _, err := CheckRepoFreshness(notARepo, "", "", true, 5); err == nil {
		t.Error("expected an error for a directory that is not a git repository")
	}
}
