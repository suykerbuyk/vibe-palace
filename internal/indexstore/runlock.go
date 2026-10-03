// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// RunKind names what a run-lock holder is doing.
type RunKind string

const (
	// KindIngest is a pending-archive ingest run.
	KindIngest RunKind = "ingest"
	// KindRebuild is a `vp index rebuild` run.
	KindRebuild RunKind = "rebuild"
	// KindLifecycle is a lifecycle step that must keep ingest and rebuild out.
	KindLifecycle RunKind = "lifecycle"
)

// Progress is the archives a run has done out of the total it plans.
type Progress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// Holder is the run lock's holder record, palace/.local/locks/index-run.holder.
//
// It is ADVISORY. Lock files are empty, so the holder writes this record after
// it acquires the lock, to name itself and report progress. After a kill or a
// pid reuse it can be stale, and it never decides who holds the lock: only a
// try-lock is authoritative, and only a run takes one.
type Holder struct {
	PID       int       `json:"pid"`
	Kind      RunKind   `json:"kind"`
	Project   string    `json:"project,omitempty"`
	StartTime time.Time `json:"start_time"`
	Progress  *Progress `json:"progress,omitempty"`
}

// ErrHolderUnknown is returned by ReadHolder when the holder record is missing
// or unparseable: the lock may be held, but by whom is unknown.
var ErrHolderUnknown = errors.New("held by another process (holder unknown)")

// RunLock is a held index run lock: one per host per vault, non-blocking, held
// for a whole ingest or rebuild run (ADR-014 decision 7, "Two locks").
//
// One process holds it at most once. A holder passes its *RunLock on; it never
// calls TryRunLock again, because a second open of the same lock file
// conflicts with the first on Linux.
//
// The run lock is taken before any index commit lock, never while one is held.
type RunLock struct {
	vault *storage.Vault

	mu      sync.Mutex
	holder  Holder
	release func() error // nil once released
}

// TryRunLock tries the vault's index run lock without blocking. On success it
// writes the holder record and returns ok=true. When another process holds the
// lock it returns ok=false at once; the caller exits (a second ingest trigger)
// or names the holder with ReadHolder (`vp index rebuild`).
func TryRunLock(vault *storage.Vault, kind RunKind, project string) (*RunLock, bool, error) {
	switch kind {
	case KindIngest, KindRebuild, KindLifecycle:
	default:
		return nil, false, fmt.Errorf("indexstore: unknown run kind %q", kind)
	}
	if project != "" {
		if _, err := vault.IndexDir(project); err != nil {
			return nil, false, err
		}
	}
	if err := os.MkdirAll(vault.IndexLocksDir(), 0o755); err != nil {
		return nil, false, fmt.Errorf("indexstore: create locks dir: %w", err)
	}
	release, ok, err := tryRunLockFile(vault)
	if err != nil || !ok {
		return nil, false, err
	}
	rl := &RunLock{
		vault:   vault,
		release: release,
		holder:  Holder{PID: os.Getpid(), Kind: kind, Project: project, StartTime: time.Now().UTC()},
	}
	if err := rl.writeHolderLocked(); err != nil {
		_ = release()
		return nil, false, err
	}
	return rl, true, nil
}

// tryRunLockFile is the one place the run lock is try-locked.
func tryRunLockFile(vault *storage.Vault) (func() error, bool, error) {
	record(lockEvent{kind: evRunTry})
	release, ok, err := vaultlock.TryAcquireFile(vault.IndexRunLockPath())
	if err != nil || !ok {
		return nil, ok, err
	}
	record(lockEvent{kind: evRunAcquired})
	return func() error {
		record(lockEvent{kind: evRunReleased})
		return release()
	}, true, nil
}

// ReadHolder reads the holder record without taking the run lock. A status
// probe must never try-lock: a colliding trigger would see the lock held,
// exit, and be lost. A missing or unparseable record gives ErrHolderUnknown.
func ReadHolder(vault *storage.Vault) (Holder, error) {
	data, err := os.ReadFile(vault.IndexRunHolderPath())
	if errors.Is(err, fs.ErrNotExist) {
		return Holder{}, ErrHolderUnknown
	}
	if err != nil {
		return Holder{}, fmt.Errorf("indexstore: read holder record: %w", err)
	}
	var h Holder
	if err := json.Unmarshal(data, &h); err != nil || h.PID <= 0 || h.Kind == "" {
		return Holder{}, ErrHolderUnknown
	}
	return h, nil
}

// SetProject records the project the run is working on.
func (rl *RunLock) SetProject(project string) error {
	if _, err := rl.vault.IndexDir(project); err != nil {
		return err
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.holder.Project = project
	return rl.writeHolderLocked()
}

// SetProgress records the run's progress, read by the coverage instrument.
func (rl *RunLock) SetProgress(done, total int) error {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.holder.Progress = &Progress{Done: done, Total: total}
	return rl.writeHolderLocked()
}

func (rl *RunLock) writeHolderLocked() error {
	if rl.release == nil {
		return errors.New("indexstore: run lock already released")
	}
	data, err := json.Marshal(rl.holder)
	if err != nil {
		return err
	}
	return writeFile(rl.vault.IndexRunHolderPath(), append(data, '\n'))
}

// Release removes the holder record and then releases the lock, in that
// order, so no probe sees a record naming a holder that has let go. It is
// idempotent.
func (rl *RunLock) Release() error {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.releaseLocked()
}

func (rl *RunLock) releaseLocked() error {
	if rl.release == nil {
		return nil
	}
	herr := removeFile(rl.vault.IndexRunHolderPath())
	if errors.Is(herr, fs.ErrNotExist) {
		herr = nil
	}
	lerr := rl.release()
	rl.release = nil
	if herr != nil {
		return fmt.Errorf("indexstore: remove holder record: %w", herr)
	}
	return lerr
}

// ReleaseAndRecheck releases the run lock without losing a trigger that
// arrived after the holder's last rescan (ADR-014 decision 7, "No lost
// trigger").
//
// A trigger writes its archive durably before it tries the lock. The holder
// rescans before it releases, and passes in seen every pending source_sha256
// that rescan saw. Then:
//
//  1. the holder record is removed and the lock released;
//  2. pending() is called, and every sha in seen is dropped;
//  3. if nothing is left, it returns reacquired=false. An archive left over by
//     the budget, or a failing one, was in seen, so it waits for the next
//     trigger instead of making this holder run again without end;
//  4. otherwise it try-locks again. On success it rewrites the holder record
//     and returns reacquired=true, and the caller runs again. On contention it
//     returns reacquired=false: the new holder took the lock after the archive
//     was durable, so its own scan sees it.
//
// Known limits, for pending-archive-ingester-and-per-archive-commit-step to
// handle:
//   - the new holder in step 4 may be a KindLifecycle or KindRebuild run, not
//     an ingest. A lifecycle step does not ingest, so the archive then waits
//     for the next trigger (the next hook, pull, capture or `vp mcp` start);
//   - if pending() fails, the lock has already been released and the error is
//     returned; the archive likewise waits for the next trigger.
func (rl *RunLock) ReleaseAndRecheck(seen map[string]struct{}, pending func() ([]string, error)) (reacquired bool, err error) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.release == nil {
		return false, errors.New("indexstore: run lock already released")
	}
	if err := rl.releaseLocked(); err != nil {
		return false, err
	}
	shas, err := pending()
	if err != nil {
		return false, fmt.Errorf("indexstore: recheck pending archives: %w", err)
	}
	unseen := false
	for _, s := range shas {
		if _, ok := seen[s]; !ok {
			unseen = true
			break
		}
	}
	if !unseen {
		return false, nil
	}
	release, ok, err := tryRunLockFile(rl.vault)
	if err != nil || !ok {
		return false, err
	}
	rl.release = release
	rl.holder.PID = os.Getpid()
	rl.holder.StartTime = time.Now().UTC()
	rl.holder.Progress = nil
	if err := rl.writeHolderLocked(); err != nil {
		_ = rl.releaseLocked()
		return false, err
	}
	return true, nil
}
