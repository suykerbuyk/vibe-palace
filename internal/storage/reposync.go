// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// RepoFreshness is CheckRepoFreshness's wire shape: how repoPath's current
// branch compares to its configured git remote(s). Unlike RemoteStatus /
// StatusReport (vault-only callers, which always assume a vault root),
// CheckRepoFreshness never assumes repoPath is the vault, and never assumes a
// remote is named "origin" — a caller must resolve or accept the remote(s) it
// finds.
type RepoFreshness struct {
	Branch         string            `json:"branch"`
	UpstreamRemote string            `json:"upstream_remote,omitempty"`
	Remotes        []RemoteFreshness `json:"remotes"`
	// Status is the single verdict distilled from Remotes — see the
	// RepoUpToDate/RepoAhead/RepoBehind/RepoDiverged/RepoUnverified constants.
	// An unreachable remote, or one with no matching branch, is NEVER
	// collapsed into RepoUpToDate: it proves nothing about behind, so the
	// honest answer is RepoUnverified.
	Status string `json:"status"`
}

// RemoteFreshness is the per-remote sync state CheckRepoFreshness reports,
// plus the newest upstream-only commit subjects when there are any.
type RemoteFreshness struct {
	Remote      string     `json:"remote"`
	Ahead       int        `json:"ahead"`
	AheadKnown  bool       `json:"ahead_known"`
	Behind      int        `json:"behind"`
	BehindKnown bool       `json:"behind_known"`
	Diverged    bool       `json:"diverged"`
	Reachable   bool       `json:"reachable"`
	LastFetched *time.Time `json:"last_fetched,omitempty"`
	// NewestUpstreamSubjects are the newest-first commit subjects on Remote's
	// branch that HEAD lacks, capped at the caller's subjectLimit. Populated
	// only when Behind > 0 && BehindKnown: a subject list derived from a
	// cached tracking ref would carry the same "may be a phantom" risk Behind
	// itself carries on the non-fetch path (see GetRemoteStatus), so it is
	// skipped there too rather than guessed at.
	NewestUpstreamSubjects []string `json:"newest_upstream_subjects,omitempty"`
	// SubjectsTruncated is true when more commits unique to the remote exist
	// than NewestUpstreamSubjects shows.
	SubjectsTruncated bool `json:"subjects_truncated,omitempty"`
}

// RepoFreshness.Status values.
const (
	RepoUpToDate   = "up_to_date"
	RepoAhead      = "ahead"
	RepoBehind     = "behind"
	RepoDiverged   = "diverged"
	RepoUnverified = "unverified"
)

// CheckRepoFreshness reports how repoPath's branch compares to its git
// remote(s). It is read-only in the same sense GetRemoteStatus is: it never
// commits, pushes, or mutates the working tree, and a fetch only updates
// .git tracking refs.
//
// remote pins the check to exactly that remote. When remote is "", the
// branch's configured upstream (`branch.<branch>.remote`) is used if set;
// otherwise every configured remote that has a same-named branch is checked
// and Status is the worst-of across them. branch defaults to repoPath's
// current branch when "".
//
// fetch controls real-vs-cached behind counts exactly as GetRemoteStatus's
// own fetch parameter does: a real `git fetch` is the ONLY way Behind can be
// real (see GetRemoteStatus's doc for why ls-remote and a cached tracking ref
// can never stand in for it). subjectLimit caps NewestUpstreamSubjects per
// remote; 0 skips the extra `git log` call entirely.
//
// It returns a non-nil error only when repoPath does not exist, is not a
// directory, or is not inside a git repository — never for a network or
// remote-reachability failure, which is reported as Status RepoUnverified
// instead. This mirrors GetRemoteStatus's own contract: an unreachable remote
// is data, not a Go error.
func CheckRepoFreshness(repoPath, remote, branch string, fetch bool, subjectLimit int) (RepoFreshness, error) {
	fi, err := os.Stat(repoPath)
	if err != nil || !fi.IsDir() {
		return RepoFreshness{}, fmt.Errorf("%s: does not exist or is not a directory", repoPath)
	}
	if _, err := gitCmd(repoPath, 5*time.Second, "rev-parse", "--git-dir"); err != nil {
		return RepoFreshness{}, fmt.Errorf("%s: not a git repository: %w", repoPath, err)
	}

	if branch == "" {
		branch = currentBranch(repoPath)
	}
	upstream, _ := gitCmd(repoPath, 5*time.Second, "config", "--get", "branch."+branch+".remote")

	canonical := remote != "" || upstream != ""
	var candidates []string
	switch {
	case remote != "":
		candidates = []string{remote}
	case upstream != "":
		candidates = []string{upstream}
	default:
		all, err := ListRemotes(repoPath)
		if err != nil {
			return RepoFreshness{}, fmt.Errorf("listing remotes: %w", err)
		}
		candidates = all
	}

	rf := RepoFreshness{Branch: branch}
	if remote == "" && upstream != "" {
		rf.UpstreamRemote = upstream
	}

	for _, r := range candidates {
		st, err := GetRemoteStatus(repoPath, r, branch, fetch)
		if err != nil {
			// GetRemoteStatus errors only when a fetch succeeded (the remote
			// is reachable) yet <remote>/<branch> still will not resolve —
			// i.e. this remote has no such branch. That candidate
			// contributes no signal; skip it rather than failing the whole
			// check over one remote's shape, so "check every remote when
			// there is no upstream" tolerates remotes that do not mirror
			// this branch.
			continue
		}
		rfr := RemoteFreshness{
			Remote:      st.Remote,
			Ahead:       st.Ahead,
			AheadKnown:  st.AheadKnown,
			Behind:      st.Behind,
			BehindKnown: st.BehindKnown,
			Diverged:    st.Diverged,
			Reachable:   st.Reachable,
		}
		if !st.LastFetched.IsZero() {
			lf := st.LastFetched
			rfr.LastFetched = &lf
		}
		if st.Behind > 0 && st.BehindKnown && subjectLimit > 0 {
			subs, truncated := newestUpstreamSubjects(repoPath, r, branch, subjectLimit)
			rfr.NewestUpstreamSubjects = subs
			rfr.SubjectsTruncated = truncated
		}
		rf.Remotes = append(rf.Remotes, rfr)
	}

	rf.Status = repoFreshnessStatus(rf.Remotes, canonical)
	return rf, nil
}

// repoFreshnessStatus distills the per-remote data into one verdict.
//
// In canonical mode (an explicit remote or a real upstream was supplied),
// candidates has at most one entry and its status IS the verdict. Otherwise
// it is the worst-of across every remote that had a matching branch, ranked
// diverged > behind > unverified > ahead > up_to_date — behind/diverged
// outrank an unverified sibling remote because they are CONFIRMED, not
// guessed; an ahead-only remote never masks a behind one.
func repoFreshnessStatus(remotes []RemoteFreshness, canonical bool) string {
	if len(remotes) == 0 {
		return RepoUnverified
	}
	if canonical {
		return singleRemoteStatus(remotes[0])
	}
	rank := map[string]int{RepoUpToDate: 0, RepoAhead: 1, RepoUnverified: 2, RepoBehind: 3, RepoDiverged: 4}
	worst := RepoUpToDate
	for _, r := range remotes {
		s := singleRemoteStatus(r)
		if rank[s] > rank[worst] {
			worst = s
		}
	}
	return worst
}

// singleRemoteStatus is the verdict for one RemoteFreshness entry.
func singleRemoteStatus(r RemoteFreshness) string {
	if !r.Reachable || !r.BehindKnown {
		// Reachable but BehindKnown=false happens only when fetch=false was
		// requested — an ahead-only signal, never a real "you're stale"
		// answer. Treated the same as unreachable: behind is unproven.
		return RepoUnverified
	}
	if r.Diverged {
		return RepoDiverged
	}
	if r.Behind > 0 {
		return RepoBehind
	}
	if r.Ahead > 0 && r.AheadKnown {
		return RepoAhead
	}
	return RepoUpToDate
}

// newestUpstreamSubjects returns the newest-first commit subjects on
// <remote>/<branch> that HEAD lacks, capped at limit, via a bounded
// `git log --format=%s HEAD..<remote>/<branch>`. Best-effort: any failure
// (including a shallow clone or an unresolvable ref) returns (nil, false)
// rather than erroring the caller.
func newestUpstreamSubjects(repoPath, remote, branch string, limit int) ([]string, bool) {
	ref := remote + "/" + branch
	out, err := gitCmd(repoPath, 15*time.Second, "log", "--format=%s", "-n", strconv.Itoa(limit+1), "HEAD.."+ref)
	if err != nil || out == "" {
		return nil, false
	}
	lines := strings.Split(out, "\n")
	truncated := len(lines) > limit
	if truncated {
		lines = lines[:limit]
	}
	return lines, truncated
}
