// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// Exact publish: how a lifecycle command publishes the one commit it verified,
// and nothing else. It never calls CommitAndPushPaths, whose reconcile rebases
// and realigns; it never rebases at all. The outcome of every push is decided
// by reading the live remote (does its tip contain the commit?), never by
// git's exit code. A re-run with a pending marker is a redo — push the same
// commit, or reset and ask for a fresh dry run — never a rebase.
//
// Design: task lifecycle-commands-design-plan, § Command surface › Exact
// publish, rules 1–6. Every function that writes or decides takes the caller's
// *vaultlock.Held root-lock token and derives the vault from it, so it cannot
// run without the lock or against another vault; this file takes no lock.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// PublishErrorKind names why an exact publish stopped.
type PublishErrorKind string

const (
	// PublishRemoteMoved: the first remote does not contain the commit and no
	// remote does. The commit was reset and the command rolled back; a fresh
	// dry run is needed.
	PublishRemoteMoved PublishErrorKind = "remote-moved"
	// PublishTransport: a later remote is still at the commit's parent. The
	// marker is kept; re-running the same command pushes the same commit.
	PublishTransport PublishErrorKind = "transport"
	// PublishMirrorDiverged: a remote holds something that is neither the
	// parent nor contains the commit, while another remote holds the commit.
	// An operator matter; the marker is kept.
	PublishMirrorDiverged PublishErrorKind = "mirror-diverged"
	// PublishStateUnknown: a remote could not be read, or HEAD is not where
	// the marker says. Nothing was reset; the marker is kept.
	PublishStateUnknown PublishErrorKind = "state-unknown"
)

// PublishError is an exact publish that stopped.
type PublishError struct {
	Kind   PublishErrorKind
	Vault  string
	Remote string
	Commit string
	Detail string
	Rerun  string
}

func (e *PublishError) Error() string {
	switch e.Kind {
	case PublishRemoteMoved:
		return fmt.Sprintf("remote %s moved: it does not contain %s, so nothing was published; the commit was reset and the run rolled back. Re-run the dry run%s",
			e.Remote, shortSHA(e.Commit), e.detail())
	case PublishTransport:
		return fmt.Sprintf("remote %s did not take %s (it is still at the commit's parent); %s is published elsewhere and is kept. Re-run the same command: %s%s",
			e.Remote, shortSHA(e.Commit), shortSHA(e.Commit), e.Rerun, e.detail())
	case PublishMirrorDiverged:
		return fmt.Sprintf("mirror %s has diverged: its tip neither is the parent of %s nor contains it. Nothing was reset; this is an operator matter (the mirror should have vp as its only writer)%s",
			e.Remote, shortSHA(e.Commit), e.detail())
	default:
		return fmt.Sprintf("publish state unknown at remote %s for %s; nothing was reset. Re-run the same command: %s%s",
			e.Remote, shortSHA(e.Commit), e.Rerun, e.detail())
	}
}

func (e *PublishError) detail() string {
	if e.Detail == "" {
		return ""
	}
	return " (" + e.Detail + ")"
}

// ErrRemoteUnreachable and ErrRemoteNotAtHead are the refusals of the
// before-commit check (rule 1).
var (
	ErrRemoteUnreachable = errors.New("remote unreachable")
	ErrRemoteNotAtHead   = errors.New("remote tip is not this vault's HEAD")
)

// lifecyclePush pushes sha to remote's branch: no force, no rebase, no
// autostash. It is a variable only so tests can simulate a push whose
// acknowledgement was lost; the outcome never trusts its error anyway.
var lifecyclePush = func(vaultPath, remote, sha, branch string) error {
	_, err := lifecycleGit(vaultPath, lifecycleNetTimeout, "push", "--quiet", remote, sha+":refs/heads/"+branch)
	return err
}

// remotesFile is the vault's tracked record of its own remotes (§ Clone ›
// Secondary remotes), written by `vp vault init --remote`.
const remotesFile = ".vibe-palace/remotes.toml"

type recordedRemotes struct {
	Remote []struct {
		Name string `toml:"name"`
		URL  string `toml:"url"`
	} `toml:"remote"`
}

// lifecycleRemoteOrder is the push order: the remotes.toml order if the vault
// records one, then any configured remote it does not list; otherwise origin
// first, then the rest in `git remote` order. Every configured remote is in
// the result: exact publish reaches all of them.
func lifecycleRemoteOrder(vaultPath string) ([]string, error) {
	configured, err := ListRemotes(vaultPath)
	if err != nil {
		return nil, fmt.Errorf("list remotes: %w", err)
	}
	var order []string
	data, err := os.ReadFile(filepath.Join(vaultPath, filepath.FromSlash(remotesFile)))
	switch {
	case err == nil:
		var rec recordedRemotes
		if _, derr := toml.Decode(string(data), &rec); derr != nil {
			return nil, fmt.Errorf("parse %s: %w", remotesFile, derr)
		}
		for _, r := range rec.Remote {
			if slices.Contains(configured, r.Name) && !slices.Contains(order, r.Name) {
				order = append(order, r.Name)
			}
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("read %s: %w", remotesFile, err)
	default:
		if slices.Contains(configured, "origin") {
			order = append(order, "origin")
		}
	}
	for _, r := range configured {
		if !slices.Contains(order, r) {
			order = append(order, r)
		}
	}
	return order, nil
}

// liveTip reads remote's branch tip from the live remote (ls-remote), and
// fetches it if the object is not local, so containment can be tested. An
// absent branch is tip "" with no error; an unreadable remote is an error.
func liveTip(vaultPath, remote, branch string) (string, error) {
	out, err := lifecycleGit(vaultPath, lifecycleNetTimeout, "ls-remote", remote, "refs/heads/"+branch)
	if err != nil {
		return "", withCredentialHint(fmt.Errorf("%w: %s: %v", ErrRemoteUnreachable, remote, err))
	}
	tip := ""
	for line := range strings.SplitSeq(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == "refs/heads/"+branch {
			tip = f[0]
		}
	}
	if tip == "" {
		return "", nil
	}
	if _, err := gitCmd(vaultPath, 10*time.Second, "cat-file", "-e", tip+"^{commit}"); err != nil {
		if _, ferr := lifecycleGit(vaultPath, lifecycleNetTimeout, "fetch", "--no-tags", "--quiet", remote,
			"+refs/heads/"+branch+":refs/remotes/"+remote+"/"+branch); ferr != nil {
			return "", fmt.Errorf("%w: %s: fetch %s: %v", ErrRemoteUnreachable, remote, shortSHA(tip), ferr)
		}
	}
	return tip, nil
}

// tipContains reports whether tip is sha or has sha as an ancestor.
func tipContains(vaultPath, sha, tip string) (bool, error) {
	if tip == "" {
		return false, nil
	}
	if tip == sha {
		return true, nil
	}
	_, err := gitCmd(vaultPath, 30*time.Second, "merge-base", "--is-ancestor", sha, tip)
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// requireHeadAtEveryRemote is rule 1's check before a lifecycle command
// commits: every remote of the vault is reachable and its live tip equals
// HEAD. It returns HEAD.
func requireHeadAtEveryRemote(held *vaultlock.Held, branch string, remotes []string) (string, error) {
	if err := held.RequireRoot(); err != nil {
		return "", err
	}
	vaultPath := held.Root()
	head, err := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("read HEAD: %w", err)
	}
	if len(remotes) == 0 {
		return "", fmt.Errorf("%s has no remote: a lifecycle command publishes its commit, so it needs one", vaultPath)
	}
	for _, r := range remotes {
		tip, err := liveTip(vaultPath, r, branch)
		if err != nil {
			return "", err
		}
		if tip != head {
			return "", fmt.Errorf("%w: %s/%s is %s, HEAD is %s — pull or push first, so that nothing unseen or unpushed exists",
				ErrRemoteNotAtHead, r, branch, shortSHA(tip), shortSHA(head))
		}
	}
	return head, nil
}

// resetToParent is the only history rewrite exact publish performs: it drops
// the command's own, unpublished commit. It refuses unless HEAD is exactly the
// marker's commit (or its parent when there is none), so it can never discard
// anyone else's commit, and it resets with --keep, so it never discards anyone
// else's uncommitted edit either.
func resetToParent(held *vaultlock.Held, m lifecycleMarker) error {
	vaultPath := held.Root()
	head, err := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("read HEAD: %w", err)
	}
	want := m.Commit
	if want == "" {
		want = m.Parent
	}
	if head != want {
		return &PublishError{Kind: PublishStateUnknown, Vault: vaultPath, Commit: m.Commit, Rerun: m.Rerun,
			Detail: fmt.Sprintf("HEAD is %s, not the run's %s; refusing to reset", shortSHA(head), shortSHA(want))}
	}
	// --keep, never --hard: V holds other projects' uncommitted edits (tidy
	// leaves dirt by design), and --hard would revert them. --keep moves HEAD,
	// updates only the paths that differ between the commit and its parent,
	// and aborts if one of those is locally modified.
	if _, err := gitCmd(vaultPath, 60*time.Second, "reset", "--keep", "-q", m.Parent); err != nil {
		return fmt.Errorf("reset to %s: %w", shortSHA(m.Parent), err)
	}
	return nil
}

// rollBackRun resets the run's commit (if any), runs the command's rollback
// and clears the marker, in that order.
func rollBackRun(held *vaultlock.Held, m lifecycleMarker, rollback func() error) error {
	if err := held.RequireRoot(); err != nil {
		return err
	}
	if err := resetToParent(held, m); err != nil {
		return err
	}
	if rollback != nil {
		if err := rollback(); err != nil {
			return fmt.Errorf("roll back %s: %w (the marker is kept)", m.Command, err)
		}
	}
	return clearLifecycleMarker(held, m.RunID)
}

// exactPublish publishes m.Commit to every remote in order (rules 3–5).
// publishedElsewhere says some remote already holds the commit (a redo), so a
// first remote that refuses it must not reset what another remote has.
func exactPublish(held *vaultlock.Held, branch string, remotes []string, m lifecycleMarker, rollback func() error, publishedElsewhere bool) error {
	if err := held.RequireRoot(); err != nil {
		return err
	}
	vaultPath := held.Root()
	if m.Commit == "" {
		return fmt.Errorf("exact publish needs the marker's commit")
	}
	for i, r := range remotes {
		pushErr := lifecyclePush(vaultPath, r, m.Commit, branch)
		tip, err := liveTip(vaultPath, r, branch)
		if err != nil {
			return &PublishError{Kind: PublishStateUnknown, Vault: vaultPath, Remote: r, Commit: m.Commit, Rerun: m.Rerun, Detail: err.Error()}
		}
		ok, err := tipContains(vaultPath, m.Commit, tip)
		if err != nil {
			return &PublishError{Kind: PublishStateUnknown, Vault: vaultPath, Remote: r, Commit: m.Commit, Rerun: m.Rerun, Detail: err.Error()}
		}
		if ok {
			continue
		}
		detail := ""
		if pushErr != nil {
			detail = pushErr.Error()
		}
		if i == 0 && !publishedElsewhere {
			if err := rollBackRun(held, m, rollback); err != nil {
				var rpe *PublishError
				if errors.As(err, &rpe) && rpe.Remote == "" {
					rpe.Remote = r
				}
				return err
			}
			return &PublishError{Kind: PublishRemoteMoved, Vault: vaultPath, Remote: r, Commit: m.Commit, Rerun: m.Rerun, Detail: detail}
		}
		if tip == m.Parent {
			return &PublishError{Kind: PublishTransport, Vault: vaultPath, Remote: r, Commit: m.Commit, Rerun: m.Rerun, Detail: detail}
		}
		return &PublishError{Kind: PublishMirrorDiverged, Vault: vaultPath, Remote: r, Commit: m.Commit, Rerun: m.Rerun, Detail: detail}
	}
	// Rule 5: confirm every remote from the live remote, then clear.
	for _, r := range remotes {
		tip, err := liveTip(vaultPath, r, branch)
		if err != nil {
			return &PublishError{Kind: PublishStateUnknown, Vault: vaultPath, Remote: r, Commit: m.Commit, Rerun: m.Rerun, Detail: err.Error()}
		}
		if ok, err := tipContains(vaultPath, m.Commit, tip); err != nil || !ok {
			return &PublishError{Kind: PublishStateUnknown, Vault: vaultPath, Remote: r, Commit: m.Commit, Rerun: m.Rerun,
				Detail: "the confirming read does not show the commit"}
		}
	}
	return clearLifecycleMarker(held, m.RunID)
}

// RedoOutcome is what a re-run found and did.
type RedoOutcome string

const (
	// RedoNone: no marker; the command runs normally.
	RedoNone RedoOutcome = "none"
	// RedoRolledBack: the marker had no commit; the run's writes were rolled
	// back and the marker cleared. The command then runs normally, afresh.
	RedoRolledBack RedoOutcome = "rolled-back"
	// RedoPublished: the same commit was published and the marker cleared.
	// The command continues with its post-publish steps.
	RedoPublished RedoOutcome = "published"
)

// lifecycleRunTrailer is the trailer every lifecycle commit carries with its
// run id (the marker's RunID). A redo adopts an unrecorded commit only when it
// carries this run's trailer.
const lifecycleRunTrailer = "Vp-Run"

// lifecycleRunTrailerLine is the trailer line a lifecycle command puts in its
// commit message.
func lifecycleRunTrailerLine(runID string) string { return lifecycleRunTrailer + ": " + runID }

// commitRunTrailer reads commit's Vp-Run trailer ("" if none).
func commitRunTrailer(vaultPath, commit string) (string, error) {
	out, err := gitCmd(vaultPath, 10*time.Second, "-c", "log.showSignature=false", "log", "-1", "--no-color",
		"--format=%(trailers:key="+lifecycleRunTrailer+",valueonly,unfold)", commit)
	if err != nil {
		return "", err
	}
	lines := strings.Fields(out)
	if len(lines) != 1 {
		return "", nil
	}
	return lines[0], nil
}

// ErrRedoRemoteMoved is the redo's refusal when no remote holds the commit and
// some remote has moved on: the commit was reset, the run rolled back and the
// marker cleared; the admin re-runs the dry run and pastes the new line.
var ErrRedoRemoteMoved = errors.New("a remote moved since the unfinished run and none holds its commit: the run was rolled back; re-run the dry run")

// redoLifecycle is rule 6: what a re-run does with a pending marker. It never
// rebases. command is the re-running command; a marker from another command
// refuses.
func redoLifecycle(held *vaultlock.Held, command, branch string, remotes []string, rollback func() error) (RedoOutcome, lifecycleMarker, error) {
	if err := held.RequireRoot(); err != nil {
		return RedoNone, lifecycleMarker{}, err
	}
	vaultPath := held.Root()
	m, found, err := readLifecycleMarker(vaultPath)
	if !found {
		return RedoNone, m, err
	}
	if err != nil {
		return RedoNone, m, &LifecyclePendingError{Vault: vaultPath, Marker: m, Malformed: err.Error()}
	}
	if m.Command != command {
		return RedoNone, m, &LifecyclePendingError{Vault: vaultPath, Marker: m}
	}
	if m.Commit == "" {
		// The run died before it recorded a commit. If it had committed (the
		// window between commit and marker update), HEAD's parent is the
		// marker's parent: no other committer could have committed while the
		// marker stood.
		head, err := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return RedoNone, m, fmt.Errorf("read HEAD: %w", err)
		}
		if head != m.Parent {
			parent, perr := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--verify", "HEAD~1^{commit}")
			if perr != nil || parent != m.Parent {
				return RedoNone, m, &PublishError{Kind: PublishStateUnknown, Vault: vaultPath, Rerun: m.Rerun,
					Detail: fmt.Sprintf("HEAD %s is neither the run's parent %s nor one commit past it", shortSHA(head), shortSHA(m.Parent))}
			}
			// One commit past the parent is the run's own commit ONLY if it
			// carries the run's Vp-Run trailer. The marker blocks vp's
			// committers, not raw git, an editor plugin or a human, so a
			// commit without the trailer is somebody else's: never adopted,
			// never published, and never reset away either.
			if run, terr := commitRunTrailer(vaultPath, head); terr != nil || run != m.RunID {
				return RedoNone, m, &PublishError{Kind: PublishStateUnknown, Vault: vaultPath, Commit: head, Rerun: m.Rerun,
					Detail: fmt.Sprintf("HEAD %s is one commit past the run's parent but does not carry %s: %s; it is not this run's commit, so it is neither published nor reset — inspect it by hand",
						shortSHA(head), lifecycleRunTrailer, m.RunID)}
			}
			if err := setLifecycleMarkerCommit(held, m.RunID, head); err != nil {
				return RedoNone, m, err
			}
			m.Commit = head
		} else {
			if err := rollBackRun(held, m, rollback); err != nil {
				return RedoNone, m, err
			}
			return RedoRolledBack, m, nil
		}
	}
	anyHas, allOK := false, true
	for _, r := range remotes {
		tip, err := liveTip(vaultPath, r, branch)
		if err != nil {
			return RedoNone, m, &PublishError{Kind: PublishStateUnknown, Vault: vaultPath, Remote: r, Commit: m.Commit, Rerun: m.Rerun, Detail: err.Error()}
		}
		has, err := tipContains(vaultPath, m.Commit, tip)
		if err != nil {
			return RedoNone, m, &PublishError{Kind: PublishStateUnknown, Vault: vaultPath, Remote: r, Commit: m.Commit, Rerun: m.Rerun, Detail: err.Error()}
		}
		anyHas = anyHas || has
		if !has && tip != m.Parent {
			allOK = false
		}
	}
	switch {
	case allOK:
		if err := exactPublish(held, branch, remotes, m, rollback, anyHas); err != nil {
			return RedoNone, m, err
		}
		return RedoPublished, m, nil
	case !anyHas:
		if err := rollBackRun(held, m, rollback); err != nil {
			return RedoNone, m, err
		}
		return RedoNone, m, ErrRedoRemoteMoved
	default:
		return RedoNone, m, &PublishError{Kind: PublishMirrorDiverged, Vault: vaultPath, Commit: m.Commit, Rerun: m.Rerun,
			Detail: "some remote holds the commit while another holds neither it nor its parent"}
	}
}
