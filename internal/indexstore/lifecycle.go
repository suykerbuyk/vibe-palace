// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// ErrProjectLive is returned by LifecycleTx.RemoveProject when, under the
// commit lock, the project exists after all (a pull brought it back, a rename
// was reverted, it was re-created). Nothing is removed.
var ErrProjectLive = errors.New("indexstore: the project exists; its index store is kept")

// ErrRenamePending is returned by LifecycleTx.RemoveProject when this host holds
// a rename-pending record for the project: vp vault rename's host-local step has
// still to move the store. Nothing is removed.
var ErrRenamePending = errors.New("indexstore: a rename is pending on this host; its index store is kept")

// LifecycleTx is a held index commit lock for a project that is gone from the
// vault, taken by LockLifecycle. It can only remove the project's store whole,
// change its epoch, and release: it writes no record.
type LifecycleTx struct {
	vault   *storage.Vault
	project string
	genPath string
	dir     string

	mu      sync.Mutex
	release func() error // nil once released
}

// LockLifecycle takes project's index commit lock, the same lock file Lock
// takes, without Lock's refusal of a project that is gone: every lifecycle site
// acts on exactly such a project. timeout is as for Lock (0 tries once).
//
// It takes one commit lock and must be called holding none, and holding no
// vault root lock: every caller runs after storage has returned.
func LockLifecycle(ctx context.Context, vault *storage.Vault, project string, timeout time.Duration) (*LifecycleTx, error) {
	lockPath, err := vault.IndexCommitLockPath(project)
	if err != nil {
		return nil, err
	}
	genPath, err := vault.IndexGenerationPath(project)
	if err != nil {
		return nil, err
	}
	dir, err := vault.IndexDir(project)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(vault.IndexLocksDir(), 0o755); err != nil {
		return nil, fmt.Errorf("indexstore: create locks dir: %w", err)
	}
	record(lockEvent{kind: evCommitWait, project: project})
	release, err := vaultlock.AcquireFileWithTimeout(ctx, lockPath, timeout)
	if err != nil {
		return nil, err
	}
	record(lockEvent{kind: evCommitAcquired, project: project})
	return &LifecycleTx{
		vault: vault, project: project, genPath: genPath, dir: dir,
		release: func() error {
			record(lockEvent{kind: evCommitReleased, project: project})
			return release()
		},
	}, nil
}

// removeProjectHook, when a test sets it, runs at each step of RemoveProject:
// "checked" after the re-check, "bumped" after the epoch change, "renamed" after
// the rename to a tombstone. An error stops the removal there, as a crash would.
var removeProjectHook func(step string) error

// RemoveProject removes the project's index store, palace/.local/index/<p>/.
//
// It first re-checks, under the lock, the rule the sweep scanned by
// (storage.Vault.IndexReapable), and removes nothing if the project exists
// (ErrProjectLive) or this host holds a rename-pending record (ErrRenamePending).
// It then:
//  1. changes the epoch of .generation/<p>, as every destructive step does
//     first, so every engine that cached the store reloads;
//  2. renames index/<p>/ to a dot-named tombstone, .tomb-<p>-<epoch>, in ONE
//     rename(2), and fsyncs the index root.
//
// It never deletes inside a live index/<p>/: a crash leaves the whole store or
// the whole tombstone, never a ledger over missing chunks. ReapGoneProjects
// deletes tombstones. .generation/<p> and the lock files stay.
//
// It reports whether there was a store to remove. With no store present it
// changes nothing, not even the epoch: no process can hold a cached state of a
// store that does not exist, and it creates no counter for a project that never
// had one.
func (lt *LifecycleTx) RemoveProject() (bool, error) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	if lt.release == nil {
		return false, errors.New("indexstore: LifecycleTx already released")
	}
	ok, reason, err := lt.vault.IndexReapable(lt.project)
	if err != nil {
		return false, err
	}
	if !ok {
		if strings.HasPrefix(reason, "rename pending") {
			return false, fmt.Errorf("%w: %s (%s)", ErrRenamePending, lt.project, reason)
		}
		return false, fmt.Errorf("%w: %s (%s)", ErrProjectLive, lt.project, reason)
	}
	if err := stepHook("checked"); err != nil {
		return false, err
	}
	if _, err := os.Lstat(lt.dir); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("indexstore: stat %s: %w", lt.dir, err)
	}
	g, err := bumpGenFile(lt.genPath, true)
	if err != nil {
		return false, err
	}
	putState(lt.vault.Root, lt.project, nil)
	if err := stepHook("bumped"); err != nil {
		return false, err
	}
	tomb, err := lt.vault.IndexTombstonePath(lt.project, strconv.FormatUint(g.Epoch, 16))
	if err != nil {
		return false, err
	}
	if err := renameDir(lt.dir, tomb); err != nil {
		return false, err
	}
	if err := stepHook("renamed"); err != nil {
		return true, err
	}
	return true, nil
}

func stepHook(step string) error {
	if removeProjectHook != nil {
		return removeProjectHook(step)
	}
	return nil
}

// ChangeEpoch changes the epoch of .generation/<p> alone, so every engine that
// remembers the project's Gen reloads. vp vault rename's drain of the rename
// source uses it.
func (lt *LifecycleTx) ChangeEpoch() error {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	if lt.release == nil {
		return errors.New("indexstore: LifecycleTx already released")
	}
	if _, err := bumpGenFile(lt.genPath, true); err != nil {
		return err
	}
	putState(lt.vault.Root, lt.project, nil)
	return nil
}

// Release releases the commit lock. It is idempotent.
func (lt *LifecycleTx) Release() error {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	if lt.release == nil {
		return nil
	}
	err := lt.release()
	lt.release = nil
	return err
}

// runLockHeld reports whether the index run lock is actually held: the holder
// record alone is advisory and survives a crashed run, which would hide a stale
// rename-pending record forever. It try-locks the run lock and, if it got it,
// releases it at once. It is called only when a holder record names a lifecycle
// run and a rename-pending record exists, holding no commit lock (the run lock
// is never tried under one). A trigger that collides with this instant sees the
// lock held and exits, as it would against any run; the next trigger runs.
func runLockHeld(vault *storage.Vault) bool {
	record(lockEvent{kind: evRunTry})
	release, ok, err := vaultlock.TryAcquireFile(vault.IndexRunLockPath())
	if err != nil {
		return false
	}
	if !ok {
		return true
	}
	_ = release()
	return false
}

// RemovalOutcome is what a lifecycle removal did with one project's store.
type RemovalOutcome string

const (
	// RemovalRemoved: the store was moved to a tombstone.
	RemovalRemoved RemovalOutcome = "removed"
	// RemovalAbsent: there was no store to remove.
	RemovalAbsent RemovalOutcome = "absent"
	// RemovalKeptBusy: the commit lock was busy; the next sweep retries.
	RemovalKeptBusy RemovalOutcome = "kept-busy"
	// RemovalKeptLive: the project exists, so its store is kept.
	RemovalKeptLive RemovalOutcome = "kept-live"
	// RemovalKeptRenamePending: this host has a rename-pending record for it.
	RemovalKeptRenamePending RemovalOutcome = "kept-rename-pending"
)

// RemoveGoneProject removes one gone project's index store, waiting at most
// timeout for its commit lock. vp vault project delete and the split purge call
// it after the vault root lock is released. A busy lock, a live project and a
// pending rename are outcomes, not errors.
func RemoveGoneProject(ctx context.Context, vault *storage.Vault, project string, timeout time.Duration) (RemovalOutcome, error) {
	lt, err := LockLifecycle(ctx, vault, project, timeout)
	if errors.Is(err, vaultlock.ErrLockWaitTimeout) {
		return RemovalKeptBusy, nil
	}
	if err != nil {
		return "", err
	}
	defer lt.Release()
	removed, err := lt.RemoveProject()
	switch {
	case errors.Is(err, ErrProjectLive):
		return RemovalKeptLive, nil
	case errors.Is(err, ErrRenamePending):
		return RemovalKeptRenamePending, nil
	case err != nil:
		return "", err
	case removed:
		return RemovalRemoved, nil
	}
	return RemovalAbsent, nil
}

// ReapReport is one index sweep pass.
type ReapReport struct {
	// Removed names the projects whose store this pass moved to a tombstone.
	Removed []string
	// Kept names each candidate this pass left, with why.
	Kept map[string]RemovalOutcome
	// Tombstones counts the tombstones this pass deleted.
	Tombstones int
	// StaleRenamePending names, old slug to target, every rename-pending record
	// whose rename is not running. Each one is logged.
	StaleRenamePending map[string]string
	// Errors are what the pass could not do. It never fails its caller.
	Errors []error
}

// ReapGoneProjects is the index sweep. It holds no lock when called, and takes
// each candidate's commit lock once, for one project at a time:
//   - every project storage.Vault.IndexReapCandidates names is tried once
//     (LockLifecycle with timeout 0, then RemoveProject); a busy lock, a live
//     project or a pending rename leaves the store for the next pass;
//   - every tombstone (.tomb-*) is deleted;
//   - every rename-pending record whose vp vault rename is not running (no run
//     lock holder record of kind lifecycle names it) is logged, including one
//     whose store is already gone.
//
// It never fails its caller: what it could not do is in the report's Errors.
// It is never run inside the embed-cache sweep, whose first run can come from a
// Put made under a commit lock.
func ReapGoneProjects(ctx context.Context, vault *storage.Vault) ReapReport {
	rep := ReapReport{Kept: map[string]RemovalOutcome{}, StaleRenamePending: map[string]string{}}
	candidates, err := vault.IndexReapCandidates()
	if err != nil {
		rep.Errors = append(rep.Errors, err)
	}
	for _, p := range candidates {
		out, err := RemoveGoneProject(ctx, vault, p, 0)
		switch {
		case err != nil:
			rep.Errors = append(rep.Errors, fmt.Errorf("remove index store of %s: %w", p, err))
		case out == RemovalRemoved:
			rep.Removed = append(rep.Removed, p)
		case out != RemovalAbsent:
			rep.Kept[p] = out
		}
	}
	tombs, err := vault.IndexTombstones()
	if err != nil {
		rep.Errors = append(rep.Errors, err)
	}
	for _, t := range tombs {
		if err := removeTree(t); err != nil {
			rep.Errors = append(rep.Errors, err)
			continue
		}
		rep.Tombstones++
	}
	pending, err := vault.ListRenamePending()
	if err != nil {
		rep.Errors = append(rep.Errors, err)
	}
	running := ""
	if h, err := ReadHolder(vault); err == nil && h.Kind == KindLifecycle && len(pending) > 0 && runLockHeld(vault) {
		running = h.Project
	}
	olds := make([]string, 0, len(pending))
	for old := range pending {
		olds = append(olds, old)
	}
	sort.Strings(olds)
	for _, old := range olds {
		if old == running {
			continue
		}
		to := pending[old]
		rep.StaleRenamePending[old] = to
		slog.Warn("index store kept by a rename-pending record whose rename is not running: re-run or undo vp vault rename, or run vp index rebuild for the target",
			"record", old, "renamed_to", to)
	}
	for _, e := range rep.Errors {
		slog.Warn("index sweep", "err", e)
	}
	return rep
}

// LifecycleRemovalTimeout is how long a delete or a split purge waits for a gone
// project's commit lock before reporting its store as kept for the next sweep.
const LifecycleRemovalTimeout = 30 * time.Second

// IndexRemoval is one project's store removal, as the delete commands report it
// (index_removal).
type IndexRemoval struct {
	Slug    string         `json:"slug"`
	Path    string         `json:"path"`
	Outcome RemovalOutcome `json:"outcome,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// RemoveGoneProjects runs RemoveGoneProject for each project in turn, one commit
// lock at a time, and reports each outcome. It never fails: an error is
// reported in its entry, and the next sweep retries the store.
func RemoveGoneProjects(ctx context.Context, vault *storage.Vault, projects []string, timeout time.Duration) []IndexRemoval {
	out := make([]IndexRemoval, 0, len(projects))
	for _, p := range projects {
		r := IndexRemoval{Slug: p, Path: "palace/.local/index/" + p + "/"}
		outcome, err := RemoveGoneProject(ctx, vault, p, timeout)
		if err != nil {
			r.Error = err.Error()
			slog.Warn("index store of a removed project kept; the next index sweep retries it", "project", p, "err", err)
		}
		r.Outcome = outcome
		out = append(out, r)
	}
	return out
}
