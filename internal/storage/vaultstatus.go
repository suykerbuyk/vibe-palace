// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

// RemoteStatus reports the sync state of the local vault branch relative to one
// configured git remote. It is a read-only snapshot: producing it never commits,
// pushes, or mutates the working tree (a fetch, when requested, only updates the
// .git tracking refs).
type RemoteStatus struct {
	Remote string // remote name (e.g. "origin")
	Ahead  int    // local commits not present on <remote>/<branch> (unpushed signal)
	Behind int    // commits on <remote>/<branch> not present locally; meaningful only when BehindKnown
	// BehindKnown is false when no fetch was performed (cached / stale tracking
	// ref), so a stale 0 is never mistaken for a real "behind 0". It is true only
	// after a successful fetch in this call.
	BehindKnown bool
	// Diverged is true only when both sides have advanced AND the behind count is
	// real: Ahead > 0 && Behind > 0 && BehindKnown.
	Diverged bool
	// Reachable reports whether the remote answered: a successful fetch (fetch=true)
	// or a successful ls-remote probe (fetch=false). False on network failure.
	Reachable bool
	// LastFetched is the mtime of the remote-tracking ref (or FETCH_HEAD) — the age
	// of the cached behind data. Zero when it cannot be determined; never fails the call.
	LastFetched time.Time
}

// currentBranch returns the vault's current branch via
// `git rev-parse --abbrev-ref HEAD`, falling back to "main" for a detached or
// empty HEAD. Mirrors the inline branch detection in CommitAndPushPaths.
func currentBranch(vaultPath string) string {
	if b, _ := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--abbrev-ref", "HEAD"); b != "" {
		return b
	}
	return "main"
}

// aheadCount is the tiny shared primitive: the network-free count of local
// commits on HEAD that are not on the remote-tracking ref <remote>/<branch>,
// via `git rev-list --count <remote>/<branch>..HEAD`. It never fetches, so it
// counts against the (possibly stale) cached tracking ref. Returns a non-nil
// error if the ref does not resolve (e.g. the remote was never fetched) or the
// count cannot be parsed.
func aheadCount(vaultPath, remote, branch string) (int, error) {
	ref := remote + "/" + branch
	out, err := gitCmd(vaultPath, 10*time.Second, "rev-list", "--count", ref+"..HEAD")
	if err != nil {
		return 0, fmt.Errorf("rev-list %s..HEAD: %w", ref, err)
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return 0, fmt.Errorf("parsing ahead count %q: %w", out, err)
	}
	return n, nil
}

// GetRemoteStatus reports sync state for one remote. When fetch is true it runs a
// bounded `git fetch <remote>` first so Behind/BehindKnown are real; when false it
// uses cached tracking refs and leaves BehindKnown=false. Read-only: never commits,
// pushes, or mutates the working tree. A fetch only updates .git tracking refs.
//
// An empty branch is resolved to the vault's current branch. An unreachable remote
// (network down, bad URL) is NOT a fatal error: the call returns a zero-ish
// RemoteStatus with Reachable=false and a nil error so the caller can still report
// other remotes. A non-nil error is returned only for unexpected failures — e.g. a
// fetch that succeeded yet whose tracking ref still will not resolve.
func GetRemoteStatus(vaultPath, remote, branch string, fetch bool) (RemoteStatus, error) {
	if branch == "" {
		branch = currentBranch(vaultPath)
	}
	st := RemoteStatus{Remote: remote}
	ref := remote + "/" + branch

	if fetch {
		// Bounded fetch: updates .git tracking refs only, never the working tree.
		// A failure here means the remote is unreachable — report it cleanly rather
		// than crashing the whole status sweep.
		if _, err := gitCmd(vaultPath, 60*time.Second, "fetch", remote); err != nil {
			st.LastFetched = lastFetched(vaultPath, remote, branch)
			return st, nil // Reachable stays false; nil error by contract.
		}
		st.Reachable = true
	} else {
		// Cheap reachability probe: returns the tip SHA only, transfers no objects
		// and computes no count. NEVER use ls-remote to derive a behind count.
		if _, err := gitCmd(vaultPath, 30*time.Second, "ls-remote", "--exit-code", remote, "HEAD"); err == nil {
			st.Reachable = true
		}
	}

	// Ahead is always computed, network-free, against the tracking ref.
	ahead, aerr := aheadCount(vaultPath, remote, branch)
	if aerr != nil {
		if fetch {
			// Fetch succeeded yet <remote>/<branch> will not resolve — unexpected.
			return st, fmt.Errorf("counting ahead for %s: %w", ref, aerr)
		}
		// fetch=false: the tracking ref may simply not exist yet (never fetched).
		// Best-effort — leave Ahead=0 and keep reporting reachability.
		ahead = 0
	}
	st.Ahead = ahead

	if fetch {
		// Behind is real only after a fetch refreshed the tracking ref.
		if out, err := gitCmd(vaultPath, 10*time.Second, "rev-list", "--count", "HEAD.."+ref); err == nil {
			if n, perr := strconv.Atoi(out); perr == nil {
				st.Behind = n
				st.BehindKnown = true
			}
		}
	}

	st.Diverged = st.Ahead > 0 && st.Behind > 0 && st.BehindKnown
	st.LastFetched = lastFetched(vaultPath, remote, branch)
	return st, nil
}

// StatusReport is the versioned, read-only vault status payload shared by the
// `vp vault status` CLI and the vp_vault_status MCP tool — one schema.
// Bump Version when adding or removing fields.
type StatusReport struct {
	Version   int    `json:"version"`
	Branch    string `json:"branch"`
	VaultPath string `json:"vault_path"`
	// ConfiguredVaultPath and Drift surface the mid-session config-change split
	// documented in tasks/mcp-server-startup-cached-vault-root.md: a long-lived
	// `vp mcp` server keeps writing to the root it resolved at STARTUP (VaultPath),
	// while a fresh resolution from the config file may now point elsewhere. The
	// running server cannot re-resolve, but it CAN report the divergence so the
	// split stops being silent. ConfiguredVaultPath is omitted (and Drift stays
	// false) whenever the freshly-resolved path equals VaultPath — the common case
	// — and always in a fresh CLI process, which resolves both from the same call
	// and therefore cannot drift.
	ConfiguredVaultPath string             `json:"configured_vault_path,omitempty"`
	Drift               bool               `json:"drift"`
	Remotes             []RemoteStatusJSON `json:"remotes"`
	Dirt                DirtJSON           `json:"dirt"`
}

// DetectVaultPathDrift compares a frozen vault root (resolved once — e.g. at MCP
// server startup and cached for the life of the process) against what a fresh
// resolution from the current working directory yields NOW. It exists because a
// running `vp mcp` server never re-resolves vault_path: if the operator edits the
// config mid-session, the server keeps writing to frozenRoot while every fresh
// `vp` process (and the SessionEnd hook) picks up the new path, splitting one
// session's writes across two vaults. The server cannot fix the split, but this
// lets a read-only status call REPORT it.
//
// configured is the freshly-resolved path; drift is true only when configured is
// non-empty AND differs from frozenRoot. A resolution failure (unreadable config,
// no cwd) returns ("", false): an unreadable config is not evidence of a swap, so
// it is reported as "no drift" rather than a false alarm.
func DetectVaultPathDrift(frozenRoot string) (configured string, drift bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	resolved, _, err := ResolveVaultPath(cwd)
	if err != nil || resolved == "" {
		return "", false
	}
	return resolved, resolved != frozenRoot
}

// RemoteStatusJSON is the JSON projection of RemoteStatus. Unpushed folds Ahead>0
// into a boolean signal; LastFetched is a pointer so an unknown fetch age is
// OMITTED (nil + omitempty) rather than serialized as a zero timestamp, and a
// known age marshals to its RFC3339 string.
type RemoteStatusJSON struct {
	Remote      string     `json:"remote"`
	Ahead       int        `json:"ahead"`
	Unpushed    bool       `json:"unpushed"` // Ahead > 0
	Behind      int        `json:"behind"`
	BehindKnown bool       `json:"behind_known"`
	Diverged    bool       `json:"diverged"`
	Reachable   bool       `json:"reachable"`
	LastFetched *time.Time `json:"last_fetched,omitempty"` // nil (omitted) when unknown
}

// DirtJSON mirrors the tidy sweep/report split for working-tree dirt.
type DirtJSON struct {
	Swept               []string `json:"swept"`
	Reported            []string `json:"reported"`
	ReportedUserContent []string `json:"reported_user_content"`
}

// BuildStatusReport assembles the full read-only status report: branch detection,
// ListRemotes + GetRemoteStatus per remote (fetch controls real-vs-cached behind
// counts), and TidyScan dirt classification. It never commits, pushes, or mutates
// the working tree; a fetch only updates .git tracking refs. A non-nil error from
// any remote probe or the tidy scan is propagated.
func BuildStatusReport(vaultPath string, fetch bool) (StatusReport, error) {
	branch := currentBranch(vaultPath)

	remotes, err := ListRemotes(vaultPath)
	if err != nil {
		return StatusReport{}, err
	}

	rems := make([]RemoteStatusJSON, 0, len(remotes))
	for _, remote := range remotes {
		st, serr := GetRemoteStatus(vaultPath, remote, branch, fetch)
		if serr != nil {
			return StatusReport{}, fmt.Errorf("%s: %w", remote, serr)
		}
		var last *time.Time
		if !st.LastFetched.IsZero() {
			lf := st.LastFetched
			last = &lf
		}
		rems = append(rems, RemoteStatusJSON{
			Remote:      st.Remote,
			Ahead:       st.Ahead,
			Unpushed:    st.Ahead > 0,
			Behind:      st.Behind,
			BehindKnown: st.BehindKnown,
			Diverged:    st.Diverged,
			Reachable:   st.Reachable,
			LastFetched: last,
		})
	}

	tidy, err := TidyScan(vaultPath)
	if err != nil {
		return StatusReport{}, err
	}

	return StatusReport{
		Version:   2,
		Branch:    branch,
		VaultPath: vaultPath,
		Remotes:   rems,
		Dirt: DirtJSON{
			Swept:               tidy.Swept,
			Reported:            tidy.Reported,
			ReportedUserContent: tidy.ReportedUserContent,
		},
	}, nil
}

// VaultFetchAge reports how long ago the vault's default remote was last fetched,
// derived ENTIRELY from local git plumbing and os.Stat — it is provably
// network-free and therefore safe on the session-start critical path and on
// hosts that may be offline. It invokes only local commands: `git remote`
// (ListRemotes), `git rev-parse` (currentBranch + lastFetched's --git-path), and
// os.Stat of the tracking ref / FETCH_HEAD mtime. It NEVER runs `git fetch`,
// `git ls-remote`, or any command that touches the network.
//
// The default remote is "origin" when present, else the first configured remote.
// When there is no remote, or the tracking ref / FETCH_HEAD cannot be stat'd
// (e.g. never fetched on this host), known is false and age/lastFetched are zero.
// Otherwise known is true, lastFetched is the ref mtime, and age is
// time.Now().Sub(lastFetched).
func VaultFetchAge(vaultPath string) (age time.Duration, fetchedAt time.Time, known bool) {
	remotes, err := ListRemotes(vaultPath) // local `git remote`; (nil,nil) when none
	if err != nil || len(remotes) == 0 {
		return 0, time.Time{}, false
	}
	remote := remotes[0]
	if slices.Contains(remotes, "origin") {
		remote = "origin"
	}
	branch := currentBranch(vaultPath) // local `git rev-parse`, no network
	fetchedAt = lastFetched(vaultPath, remote, branch)
	if fetchedAt.IsZero() {
		return 0, time.Time{}, false
	}
	return time.Since(fetchedAt), fetchedAt, true
}

// lastFetched returns the mtime of the loose remote-tracking ref
// refs/remotes/<remote>/<branch>, falling back to FETCH_HEAD (updated by every
// fetch) when the ref is packed or absent. Best-effort: returns the zero time
// when neither path can be stat'd. Never fails the status call.
func lastFetched(vaultPath, remote, branch string) time.Time {
	for _, gitPath := range []string{"refs/remotes/" + remote + "/" + branch, "FETCH_HEAD"} {
		p, err := gitCmd(vaultPath, 5*time.Second, "rev-parse", "--git-path", gitPath)
		if err != nil || p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(vaultPath, p)
		}
		if fi, statErr := os.Stat(p); statErr == nil {
			return fi.ModTime()
		}
	}
	return time.Time{}
}
