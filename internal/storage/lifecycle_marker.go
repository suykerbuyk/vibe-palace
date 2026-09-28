// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// The pending marker: `.git/vp-lifecycle-pending`, host-local and never
// tracked. A lifecycle command writes it before its first write to the vault
// and clears it only once every remote is confirmed to contain its commit.
// While it stands, the commit guard (refuseOnPendingDepartures, which every vp
// committer runs before it stages) and pullCore refuse, so no other vp writer
// can stack a commit on an unfinished lifecycle commit, rebase it, merge onto
// it, or publish it. Only the command's own publish or its redo moves it on.
//
// Design: task lifecycle-commands-design-plan, § Context (Pending marker) and
// § Command surface › Exact publish, rules 1, 2, 5 and 6.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// lifecycleMarkerName is the marker's name inside the git directory.
const lifecycleMarkerName = "vp-lifecycle-pending"

// ErrLifecyclePending is what every vp commit and pull refuses with while a
// lifecycle command's marker stands.
var ErrLifecyclePending = errors.New("a vault lifecycle command is unfinished")

// lifecycleMarker is the marker's content.
type lifecycleMarker struct {
	Command string `json:"command"` // e.g. "vault copy"
	RunID   string `json:"run_id"`
	Parent  string `json:"parent"`           // HEAD before the command wrote anything
	Commit  string `json:"commit,omitempty"` // the lifecycle commit, once it exists
	Rerun   string `json:"rerun"`            // the command line that finishes or redoes the run
	// Remotes are the run's remotes as name=url, for a command whose re-run
	// must match them (vault init).
	Remotes []string `json:"remotes,omitempty"`
	Created string   `json:"created"`
}

// LifecyclePendingError is the refusal while a marker stands.
type LifecyclePendingError struct {
	Vault  string
	Marker lifecycleMarker
	// Malformed is set when the marker exists but cannot be read. It still
	// refuses: an unreadable marker is not permission to commit.
	Malformed string
}

func (e *LifecyclePendingError) Unwrap() error { return ErrLifecyclePending }

func (e *LifecyclePendingError) Error() string {
	if e.Malformed != "" {
		return fmt.Sprintf("refusing: %s holds an unreadable lifecycle marker (%s); a vault lifecycle command did not finish. "+
			"Inspect it with `git -C %s rev-parse --git-path %s` before removing it by hand",
			e.Vault, e.Malformed, e.Vault, lifecycleMarkerName)
	}
	state := "before its commit"
	if e.Marker.Commit != "" {
		state = "after commit " + shortSHA(e.Marker.Commit) + " and before every remote confirmed it"
	}
	return fmt.Sprintf("refusing: `vp %s` stopped %s in %s, so no other commit, merge or rebase may touch this vault until it finishes. "+
		"Re-run it to finish or roll it back: %s", e.Marker.Command, state, e.Vault, e.Marker.Rerun)
}

// lifecycleMarkerPath is the marker's absolute path: inside the vault's git
// directory (`git rev-parse --git-path`, so a linked worktree gets its own).
func lifecycleMarkerPath(vaultPath string) (string, error) {
	p, err := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--git-path", lifecycleMarkerName)
	if err != nil {
		return "", fmt.Errorf("locate the lifecycle marker: %w", err)
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(vaultPath, p)
	}
	return p, nil
}

// readLifecycleMarker returns the marker and whether one exists. A marker that
// exists but does not parse is an error AND found=true: callers refuse on it.
func readLifecycleMarker(vaultPath string) (lifecycleMarker, bool, error) {
	p, err := lifecycleMarkerPath(vaultPath)
	if err != nil {
		return lifecycleMarker{}, false, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return lifecycleMarker{}, false, nil
	}
	if err != nil {
		return lifecycleMarker{}, true, fmt.Errorf("read the lifecycle marker: %w", err)
	}
	var m lifecycleMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return lifecycleMarker{}, true, fmt.Errorf("parse the lifecycle marker: %w", err)
	}
	if m.RunID == "" || (m.Parent == "" && m.Command != initMarkerCommand) {
		return m, true, fmt.Errorf("the lifecycle marker names no run id or parent")
	}
	return m, true, nil
}

// writeLifecycleMarker creates the marker under the caller's root-lock token,
// and records this process as the run's owner. It refuses if a marker already
// exists: that run must be finished or rolled back first.
func writeLifecycleMarker(held *vaultlock.Held, m lifecycleMarker) error {
	if err := held.RequireRoot(); err != nil {
		return err
	}
	vaultPath := held.Root()
	// Only `vault init` has no parent: its commit is the vault's root commit.
	if m.RunID == "" || m.Command == "" || (m.Parent == "" && m.Command != initMarkerCommand) {
		return fmt.Errorf("lifecycle marker needs a command, a run id and a parent")
	}
	if m.Created == "" {
		m.Created = time.Now().UTC().Format(time.RFC3339)
	}
	p, err := lifecycleMarkerPath(vaultPath)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		old, _, rerr := readLifecycleMarker(vaultPath)
		return &LifecyclePendingError{Vault: vaultPath, Marker: old, Malformed: errString(rerr)}
	}
	if err != nil {
		return fmt.Errorf("write the lifecycle marker: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		_ = os.Remove(p)
		return fmt.Errorf("write the lifecycle marker: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(p)
		return fmt.Errorf("write the lifecycle marker: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write the lifecycle marker: %w", err)
	}
	ownedRuns.Store(runKey(vaultPath), ownedRun{held: held, runID: m.RunID})
	return nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ownedRun is a lifecycle run this process owns: the live root-lock token it
// wrote the marker under, and the run id.
type ownedRun struct {
	held  *vaultlock.Held
	runID string
}

// ownedRuns maps a vault root to the run this process owns there. It is what
// lets the run's OWN commit pass the commit guard while its marker stands:
// commitPathsLocked (U2) and the commitOnlyPaths backstop run the plain guard,
// and the guard exempts the marker only while the very token that wrote it is
// still live. Any other committer — another process, or this one after the
// token is released — is refused: in-process it could not reach a commit
// anyway without the root lock the token holds, and across processes the map
// is empty.
var ownedRuns sync.Map // canonical root -> ownedRun

func runKey(vaultPath string) string {
	if abs, err := filepath.Abs(vaultPath); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(vaultPath)
}

// ownsLifecycleRun reports whether caller is the very live root-lock token
// that wrote runID's marker in vaultPath. Only the caller's own token counts:
// another token being live somewhere in this process proves nothing about
// the committer asking.
func ownsLifecycleRun(vaultPath, runID string, caller *vaultlock.Held) bool {
	if caller == nil || caller.RequireRoot() != nil || runKey(caller.Root()) != runKey(vaultPath) {
		return false
	}
	v, ok := ownedRuns.Load(runKey(vaultPath))
	if !ok {
		return false
	}
	o := v.(ownedRun)
	return o.runID == runID && o.held == caller
}

// setLifecycleMarkerCommit records the lifecycle commit in the run's marker.
func setLifecycleMarkerCommit(held *vaultlock.Held, runID, commit string) error {
	if err := held.RequireRoot(); err != nil {
		return err
	}
	vaultPath := held.Root()
	m, found, err := readLifecycleMarker(vaultPath)
	if err != nil {
		return err
	}
	if !found || m.RunID != runID {
		return fmt.Errorf("the lifecycle marker is not this run's (run %s)", runID)
	}
	m.Commit = commit
	p, err := lifecycleMarkerPath(vaultPath)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("update the lifecycle marker: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("update the lifecycle marker: %w", err)
	}
	// fsync before the rename, so a crash leaves the old marker or the new
	// one, never an empty file.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("update the lifecycle marker: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("update the lifecycle marker: %w", err)
	}
	return os.Rename(tmp, p)
}

// clearLifecycleMarker removes the run's marker. A marker from another run is
// refused, never removed.
func clearLifecycleMarker(held *vaultlock.Held, runID string) error {
	if err := held.RequireRoot(); err != nil {
		return err
	}
	vaultPath := held.Root()
	defer func() {
		if v, ok := ownedRuns.Load(runKey(vaultPath)); ok && v.(ownedRun).runID == runID {
			ownedRuns.Delete(runKey(vaultPath))
		}
	}()
	m, found, err := readLifecycleMarker(vaultPath)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if m.RunID != runID {
		return fmt.Errorf("the lifecycle marker belongs to run %s, not %s", m.RunID, runID)
	}
	p, err := lifecycleMarkerPath(vaultPath)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// refuseOnLifecyclePending is the commit guard's marker check. It refuses while
// a marker stands, unless caller is the very live root-lock token that wrote
// it: the lifecycle command's own commit. A committer without a token (nil)
// is always refused. A marker that cannot be read refuses; a git failure
// locating it is an error, never a pass.
func refuseOnLifecyclePending(vaultPath string, caller *vaultlock.Held) error {
	return lifecyclePendingCheck(vaultPath, caller)
}

// refuseOnLifecyclePendingStrict is the pull's marker check: no exemption at
// all, because a pull holds no token. In a long-lived server another call's
// pull must never merge onto the running command's commit.
func refuseOnLifecyclePendingStrict(vaultPath string) error {
	return lifecyclePendingCheck(vaultPath, nil)
}

func lifecyclePendingCheck(vaultPath string, caller *vaultlock.Held) error {
	m, found, err := readLifecycleMarker(vaultPath)
	if !found {
		if err != nil && !isNotRepoError(err) {
			return err
		}
		return nil
	}
	if err != nil {
		return &LifecyclePendingError{Vault: vaultPath, Marker: m, Malformed: err.Error()}
	}
	if ownsLifecycleRun(vaultPath, m.RunID, caller) {
		return nil
	}
	return &LifecyclePendingError{Vault: vaultPath, Marker: m}
}

// isNotRepoError is true when git cannot find a repository at all. Some
// committers run on a vault whose git state they detect later; the marker
// check must not turn "not a repository" into a new refusal those callers
// never had.
func isNotRepoError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not a git repository")
}
