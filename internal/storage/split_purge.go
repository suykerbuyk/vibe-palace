// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// A split purge used to delete the moved projects' trees and write their
// departure records, and commit nothing. Everything that commits in this vault
// then had a half-finished departure in front of it: tidy's sweep rules match
// by path, so it committed and pushed the deletions of session notes,
// transcripts, drawers and KG files while only reporting the records, the
// task files and resume.md; the SessionEnd harvest committed the memory
// deletions. Every other host pulled a departure with half its deletions and no
// record. The pieces here make the purge commit its own result, and make every
// other committer refuse while a record is pending.
//
// Task: split-purge-commits-its-own-result (U1).

// ErrPendingDeparture is what every vp commit refuses with while a departure
// record is uncommitted: a split purge that did not finish.
var ErrPendingDeparture = errors.New("a departure record is uncommitted")

// PendingDepartureError is the refusal. It names each pending record and gives
// the raw-git recoveries, because every vp commit refuses until one of them is
// run.
type PendingDepartureError struct {
	Vault   string
	Records []string // vault-relative, sorted
	// Trees are the slug trees HEAD still holds files under, sorted. The
	// recovery commands name only these: a pathspec that matches nothing makes
	// git rm, checkout and commit fail.
	Trees []string
	// Committed are the records HEAD already holds (a re-departure overwrote
	// one); undo restores them rather than removing them.
	Committed map[string]bool
}

func (e *PendingDepartureError) Unwrap() error { return ErrPendingDeparture }

func (e *PendingDepartureError) Error() string {
	slugs := make([]string, 0, len(e.Records))
	for _, r := range e.Records {
		slugs = append(slugs, strings.TrimSuffix(path.Base(r), ".json"))
	}
	recs := strings.Join(e.Records, " ")
	trees := strings.Join(e.Trees, " ")
	var b strings.Builder
	fmt.Fprintf(&b, "refusing to commit: %s %s uncommitted — a split purge of %s did not finish, and committing anything now could publish half of it. Recover with raw git, then retry:\n",
		strings.Join(e.Records, ", "), map[bool]string{true: "is", false: "are"}[len(e.Records) == 1], strings.Join(slugs, ", "))
	// Finish: one commit for every pending slug, the shape purge's own commit has.
	fin := fmt.Sprintf("git -C %s add -- %s && git -C %s commit -m \"vault split: purge %s (finished by hand)\" -- %s",
		e.Vault, recs, e.Vault, strings.Join(slugs, ", "), recs)
	if trees != "" {
		fin = fmt.Sprintf("git -C %s rm -r -q --ignore-unmatch -- %s && ", e.Vault, trees) + fin + " " + trees
	}
	fmt.Fprintf(&b, "  - finish it: %s\n", fin)
	var undo []string
	if trees != "" {
		undo = append(undo, fmt.Sprintf("git -C %s checkout HEAD -- %s", e.Vault, trees))
	}
	var restore, remove []string
	for _, r := range e.Records {
		if e.Committed[r] {
			restore = append(restore, r)
		} else {
			remove = append(remove, r)
		}
	}
	if len(restore) > 0 {
		undo = append(undo, fmt.Sprintf("git -C %s checkout HEAD -- %s", e.Vault, strings.Join(restore, " ")))
	}
	if len(remove) > 0 {
		undo = append(undo, fmt.Sprintf("git -C %s rm -q --cached --ignore-unmatch -- %s && rm -f -- %s", e.Vault, strings.Join(remove, " "), strings.Join(prefixAll(e.Vault+"/", remove), " ")))
	}
	fmt.Fprintf(&b, "  - or undo it: %s\n", strings.Join(undo, " && "))
	b.WriteString("Finishing leaves the untracked leftovers (ignored rows, the embed cache, empty directories); they are inert, because a departed slug's residue is treated as absent, and the cache is left to the reaper. ")
	b.WriteString("Until then every vp commit on this vault refuses. Files vp's typed writers write meanwhile (tasks, memory, sessions) are kept on disk but stay uncommitted until recovery; task files are not swept by tidy, so they stay dirty until their next typed write or a manual commit.")
	return b.String()
}

func prefixAll(p string, ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = p + s
	}
	return out
}

// pendingDepartures lists every departure record git reports as untracked,
// modified or staged. A git failure is an error: a committer about to publish
// must not fail open on "could not look".
func pendingDepartures(vaultPath string) ([]string, error) {
	// RAW output, never gitCmd's trimmed one: `-z` records start with their
	// two-character status, and a leading " M" would lose its space and shift
	// every path by one byte (the scanPorcelain rule, vaulttidy.go).
	cmd := exec.Command("git", "-C", vaultPath, "status", "--porcelain=v1", "-z", "-uall", "--no-renames", "--", departure.Dir)
	cmd.Env = SafeGitEnv("GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("check for an unfinished split purge: git status: %w", err)
	}
	out := string(raw)
	var recs []string
	for _, e := range parsePorcelainZ(out) {
		if strings.HasSuffix(e.Path, ".json") {
			recs = append(recs, e.Path)
		}
	}
	sort.Strings(recs)
	// One record can carry two entries — staged-deleted and still on disk shows
	// as both "D " and "??" — and every recovery line is built from this list.
	return slices.Compact(recs), nil
}

// refuseOnPendingDepartures is the commit guard. Every vp committer runs it
// before it stages (so a refused commit leaves nothing staged), and
// commitOnlyPaths — the one production `git commit` — runs it again as the
// backstop. Only the split purge's own commit is exempt, and not because it
// "carries the record": a commit that merely includes a pending record would,
// after a crashed purge, publish the record without its deletions.
//
// It also refuses while a lifecycle command's pending marker stands
// (lifecycle_marker.go), so every committer that already runs this guard
// before staging is blocked from stacking on, rebasing or publishing an
// unfinished lifecycle commit.
func refuseOnPendingDepartures(vaultPath string) error {
	return refuseOnPendingDeparturesFor(vaultPath, nil)
}

// refuseOnPendingDeparturesFor is the commit guard for a committer that holds
// the vault root lock as caller. The lifecycle marker does not refuse it only
// when caller is the very live token that wrote the marker: the lifecycle
// command's own commit. Every other committer passes nil through
// refuseOnPendingDepartures.
func refuseOnPendingDeparturesFor(vaultPath string, caller *vaultlock.Held) error {
	if err := refuseOnLifecyclePending(vaultPath, caller); err != nil {
		return err
	}
	recs, err := pendingDepartures(vaultPath)
	if err != nil || len(recs) == 0 {
		return err
	}
	e := &PendingDepartureError{Vault: vaultPath, Records: recs, Committed: map[string]bool{}}
	for _, r := range recs {
		if _, found, err := ReadCommittedBlob(vaultPath, r); err == nil && found {
			e.Committed[r] = true
		}
		s := strings.TrimSuffix(path.Base(r), ".json")
		for _, tree := range ProjectTrees(s) {
			if out, err := gitCmd(vaultPath, 10*time.Second, "ls-tree", "-r", "--name-only", "HEAD", "--", tree); err == nil && out != "" {
				e.Trees = append(e.Trees, tree)
			}
		}
	}
	return e
}

// SplitPurgePreflight is everything the split purge must know before it
// changes anything, and it changes nothing. It returns the HEAD the purge is
// checked against; CommitSplitPurge refuses if HEAD has moved since.
//
// collected holds every file the purge walked under the slugs' trees (a
// vault-relative path per regular file).
func SplitPurgePreflight(vaultPath string, slugs []string, collected map[string]bool) (string, error) {
	if err := RefuseIfGitDisabled(vaultPath, "commit a split purge"); err != nil {
		return "", fmt.Errorf("refusing to purge: this purge must commit its own result, and git_enabled = false forbids the commit; nothing was removed: %w", err)
	}
	if err := CheckCommitIdentity(vaultPath); err != nil {
		return "", err
	}
	if op, err := operationInProgress(vaultPath); err != nil {
		return "", err
	} else if op != "" {
		return "", fmt.Errorf("refusing to purge: a git %s is in progress in the vault; finish or abort it first", op)
	}
	// An uncommitted record is an unfinished purge. A COMMITTED one is a slug
	// that was re-created and is departing again; RecordDeparture overwrites it
	// and this purge commits the new one.
	if err := refuseOnPendingDepartures(vaultPath); err != nil {
		return "", err
	}
	// 🔴 HEAD IS READ FIRST, and every check below is made against THAT
	// commit. CommitSplitPurge refuses unless HEAD still equals it, so a
	// commit landing after this read (tidy, a pull) is caught there; read
	// last, a file committed between these checks would sit inside the
	// recorded HEAD, pass the re-check, and be removed uncopied.
	head, err := gitCmd(vaultPath, 10*time.Second, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("read HEAD: %w", err)
	}
	var trees []string
	for _, s := range slugs {
		trees = append(trees, ProjectTrees(s)...)
	}
	out, err := gitCmd(vaultPath, 30*time.Second, append([]string{"-c", "core.quotepath=off", "ls-tree", "-r", "-z", "--name-only", head, "--"}, trees...)...)
	if err != nil {
		return "", fmt.Errorf("list tracked files under the purged trees: %w", err)
	}
	splitPurgePreflightAfterList()

	// Tracked must be a subset of collected: a tracked file already missing
	// from the working tree was never walked, so it is in no manifest row and
	// was never copied. Removing its tree from the index would delete it from
	// the source tip.
	var missing []string
	for _, p := range splitZ([]byte(out)) {
		if !collected[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", fmt.Errorf("refusing to purge: %d tracked file(s) under the purged trees are missing from the working tree, so they were never copied to the destination: %s. Restore them (git checkout HEAD -- <path>) and re-run plan, apply, verify and purge",
			len(missing), strings.Join(firstN(missing, 20), ", "))
	}
	// Clean against that HEAD, working tree and index: git rm must see
	// exactly its bytes, which are the bytes the manifest bound and verify
	// proved.
	for _, args := range [][]string{{"diff", "--quiet", head, "--"}, {"diff", "--cached", "--quiet", head, "--"}} {
		if _, err := gitCmd(vaultPath, 30*time.Second, append(args, trees...)...); err != nil {
			return "", fmt.Errorf("refusing to purge: tracked files under the purged trees differ from HEAD (uncommitted or staged changes); commit or discard them, then re-run plan, apply, verify and purge")
		}
	}
	return head, nil
}

func firstN(ss []string, n int) []string {
	if len(ss) <= n {
		return ss
	}
	return append(append([]string(nil), ss[:n]...), fmt.Sprintf("… and %d more", len(ss)-n))
}

// splitPurgePreflightAfterList is a TEST SEAM, a no-op in production: it runs
// between the preflight's tracked-file listing and its clean check — the
// window in which a commit landing must not slip into the HEAD the purge is
// checked against.
var splitPurgePreflightAfterList = func() {}

// SplitPurgeCommit is step 2 of a split purge.
type SplitPurgeCommit struct {
	Slugs      []string // the purged slugs
	Records    []string // the departure records purge wrote, vault-relative
	Message    string   // the commit subject and body; the hostname line is appended
	ExpectHead string   // the HEAD SplitPurgePreflight returned
	// Trailers, when set, is the message's final paragraph, after the hostname
	// line (stampedCommitMessage): git reads trailers only from the last
	// paragraph. The split purge sets none, so its message is unchanged.
	Trailers string
}

// SplitPurgeCommitResult reports the commit.
type SplitPurgeCommitResult struct {
	CommitSHA      string
	TrackedRemoved int
}

// SplitPurgeHeadMovedError is CommitSplitPurge's refusal when HEAD moved
// between the preflight and the lock. Nothing was removed.
type SplitPurgeHeadMovedError struct{ From, To string }

func (e *SplitPurgeHeadMovedError) Error() string {
	return fmt.Sprintf("refusing to purge: the vault's HEAD moved from %s to %s after this purge was checked (a pull or merge landed); nothing was removed. Re-run purge", short(e.From), short(e.To))
}

// CommitSplitPurge removes every TRACKED file under the purged slugs' trees
// and commits that removal together with the departure records, in one local
// commit, under the vault commit lock. It never pushes.
//
// 🔴 THE LOCK IS HELD FROM THE HEAD CHECK TO THE ASSERTION, and nothing under
// it takes a per-path vaultlock key: this runs git only (git rm, add, commit,
// and the rollback's reset and checkout), so the ADR-003 order is unchanged.
// Every vp committer, and the reconcile paths that rebase or merge
// (pushCommitted, pullCore), take the same key, so none of them can commit,
// stash or merge over a purge in flight.
//
// `git rm` without -f is the compare-and-set: it refuses the whole removal if
// any tracked file differs from the index or from HEAD. Untracked and ignored
// files are left for the caller's cleanup, after this commit, so a rollback
// never has anything to restore but git's own.
//
// On any failure before the commit lands it restores the index and the tracked
// files from HEAD (lossless) and returns the error; the caller then restores
// or removes the records. After the commit lands, a failed assertion is an
// error naming the commit.
func CommitSplitPurge(vaultPath string, c SplitPurgeCommit) (*SplitPurgeCommitResult, error) {
	held, err := vaultlock.AcquireHeld(vaultPath, vaultPath)
	if err != nil {
		return nil, fmt.Errorf("acquire vault commit lock: %w", err)
	}
	defer held.Release()
	return CommitSplitPurgeLocked(held, c)
}

// CommitSplitPurgeLocked is CommitSplitPurge for a caller that already holds
// the vault root commit lock: the vault is held.Root(), and the lock is not
// taken again (vaultlock.Acquire is not reentrant). A token that is not a live
// root lock refuses before any git runs.
func CommitSplitPurgeLocked(held *vaultlock.Held, c SplitPurgeCommit) (*SplitPurgeCommitResult, error) {
	if err := held.RequireRoot(); err != nil {
		return nil, err
	}
	vaultPath := held.Root()

	head, err := gitCmd(vaultPath, 10*time.Second, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("read HEAD: %w", err)
	}
	if head != c.ExpectHead {
		return nil, &SplitPurgeHeadMovedError{From: c.ExpectHead, To: head}
	}

	// Only trees with index entries: git rm, add and a commit pathspec all
	// fatal on a pathspec that matches nothing (a Projects-only slug has no
	// palace/ tree).
	var trees []string
	tracked := 0
	for _, s := range c.Slugs {
		for _, tree := range ProjectTrees(s) {
			out, err := gitCmd(vaultPath, 30*time.Second, "ls-files", "-z", "--", tree)
			if err != nil {
				return nil, fmt.Errorf("list tracked files under %s: %w", tree, err)
			}
			if n := len(splitZ([]byte(out))); n > 0 {
				trees = append(trees, tree)
				tracked += n
			}
		}
	}
	pathspecs := append(append([]string(nil), trees...), c.Records...)

	// removed is set once git rm has removed the tracked files. Only then is
	// there anything to restore: a git rm that refused removed nothing, and
	// restoring from HEAD would overwrite the very change that made it refuse.
	removed := false
	rollback := func(cause error) error {
		var errs []string
		if _, err := gitCmd(vaultPath, 30*time.Second, append([]string{"reset", "-q", "--"}, pathspecs...)...); err != nil {
			errs = append(errs, fmt.Sprintf("unstage failed (%v)", err))
		}
		if removed {
			if _, err := gitCmd(vaultPath, 60*time.Second, append([]string{"checkout", "HEAD", "--"}, trees...)...); err != nil {
				errs = append(errs, fmt.Sprintf("restore failed (%v) — run: git -C %s checkout HEAD -- %s", err, vaultPath, strings.Join(trees, " ")))
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("%w; and the rollback did not complete: %s", cause, strings.Join(errs, "; "))
		}
		if removed {
			return fmt.Errorf("%w (rolled back: the tracked files are restored from HEAD)", cause)
		}
		return fmt.Errorf("%w (nothing was removed)", cause)
	}

	if len(trees) > 0 {
		if _, err := gitCmd(vaultPath, 60*time.Second, append([]string{"rm", "-r", "-q", "--"}, trees...)...); err != nil {
			return nil, rollback(fmt.Errorf("git rm: %w", err))
		}
		removed = true
	}
	if len(c.Records) > 0 {
		if err := stageInBatches(vaultPath, c.Records); err != nil {
			return nil, rollback(fmt.Errorf("stage departure records: %w", err))
		}
	}
	// Audits/.surface is restamped by the record write when its content
	// differs, or created when the vault had none: either way it is this
	// purge's own change, and left uncommitted it would be dirt. Commit it
	// then, and only then.
	const surfaceRel = "Audits/.surface"
	if _, err := os.Lstat(vaultPath + "/" + surfaceRel); err == nil {
		dirty, err := HasUncommittedChanges(vaultPath, surfaceRel)
		if err != nil {
			return nil, rollback(fmt.Errorf("check %s: %w", surfaceRel, err))
		}
		if dirty {
			if err := stageInBatches(vaultPath, []string{surfaceRel}); err != nil {
				return nil, rollback(fmt.Errorf("stage %s: %w", surfaceRel, err))
			}
			pathspecs = append(pathspecs, surfaceRel)
		}
	}

	if err := commitPathspec(vaultPath, stampedCommitMessage(c.Message, c.Trailers), pathspecs); err != nil {
		return nil, rollback(err)
	}

	sha, _ := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--short", "HEAD")
	res := &SplitPurgeCommitResult{CommitSHA: sha, TrackedRemoved: tracked}

	// The commit landed: from here a failure is reported, never rolled back.
	var bad []string
	if len(trees) > 0 {
		if out, err := gitCmd(vaultPath, 30*time.Second, append([]string{"ls-files", "--"}, trees...)...); err != nil || out != "" {
			bad = append(bad, "tracked files remain under "+strings.Join(trees, ", "))
		}
	}
	for _, r := range c.Records {
		if _, found, err := ReadCommittedBlob(vaultPath, r); err != nil || !found {
			bad = append(bad, r+" is not in HEAD")
		}
	}
	// TRACKED state only (-uno): untracked and ignored rows under the trees are
	// still there by design until the caller's cleanup, which runs next.
	if out, err := gitCmd(vaultPath, 30*time.Second, append([]string{"status", "--porcelain=v1", "-uno", "--"}, pathspecs...)...); err != nil || out != "" {
		bad = append(bad, "the purged paths' tracked state is not clean after the commit")
	}
	if len(bad) > 0 {
		return res, fmt.Errorf("split purge commit %s landed but: %s", sha, strings.Join(bad, "; "))
	}
	return res, nil
}

// RestoreFromHEAD restores rel's index entry and working-tree file from HEAD.
// A split purge's rollback uses it for a departure record that was already
// committed before the purge overwrote it.
func RestoreFromHEAD(vaultPath, rel string) error {
	_, err := gitCmd(vaultPath, 10*time.Second, "checkout", "HEAD", "--", rel)
	return err
}
