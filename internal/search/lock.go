// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// Per-project serialization of the engine's writers (Rebuild and
// IndexDrawers), task search-index-completeness-and-build-serialization,
// Scope 9.
//
// 🔴 LOCK ORDER: the project's in-process mutex, THEN the project's index
// commit lock (indexstore.Lock), THEN e.mu. lockProject and
// (*projectLock).Tx are the only code in this package that takes a project
// mutex or an index commit lock, so the order has one implementation:
//
//   - the commit lock is a leaf (ADR-014 decision 7): at most one is held by
//     this engine at a time, and never the index run lock;
//   - a projectLock opens at most one Tx at a time, and may close it and open
//     another while it keeps the mutex (Rebuild commits each embed batch in its
//     own short Tx, so it never blocks an ingester for the length of an embed);
//   - the project mutex is never e.mu: e.mu is taken after the commit lock
//     (reapLocked evicts under it), so e.mu before the mutex would invert the
//     order.

// searchLockTimeout bounds how long the search path (the lazy build, and
// IndexDrawers for capture) waits for a project's mutex and for its index
// commit lock. On a timeout the caller skips its own write (ADR-014 decision 7:
// searches never stall behind an ingest commit). A var so a test can make the
// wait zero.
var searchLockTimeout = 5 * time.Second

// lockEvent is one acquisition or release, reported to lockRecorder.
type lockEvent struct {
	kind    string // "mutex-wait", "mutex-acquired", "mutex-released", "commit-acquired", "commit-released"
	project string
}

// lockRecorder, when a test sets it, receives every project mutex and commit
// lock acquisition and release made through lockProject, in order.
var lockRecorder func(lockEvent)

// semWaitHook, when a test sets it, runs when an acquirer has missed the
// semaphore once and is about to wait for it, so a test can hand the
// semaphore over only once every acquirer is waiting.
var semWaitHook func()

// beforeCommitLockHook, when a test sets it, runs while a projectLock holds
// its mutex and is about to wait for the commit lock, so a test can act at the
// moment a writer is known to hold the mutex and be blocked on the commit lock.
var beforeCommitLockHook func(project string)

func recordLock(kind, project string) {
	if lockRecorder != nil {
		lockRecorder(lockEvent{kind: kind, project: project})
	}
}

// projectSem returns project's mutex: a channel semaphore of capacity one, so a
// wait for it can honour a context and a timeout.
func (e *Engine) projectSem(project string) chan struct{} {
	e.semMu.Lock()
	defer e.semMu.Unlock()
	sem, ok := e.sems[project]
	if !ok {
		sem = make(chan struct{}, 1)
		e.sems[project] = sem
	}
	return sem
}

// projectLock is a held project mutex, and at most one open Tx under it.
type projectLock struct {
	e       *Engine
	project string
	sem     chan struct{}
	tx      *indexstore.Tx
}

// lockProject takes project's in-process mutex, waiting at most timeout (0
// tries once; indexstore.NoTimeout waits until ctx is done). A timeout returns
// an error wrapping vaultlock.ErrLockWaitTimeout.
//
// Before anything else it runs the engine's index sweep (gone projects' index
// stores, indexstore.ReapGoneProjects), once per engine. The sweep takes each
// gone project's commit lock, and no process may hold two, so it must run
// while this engine holds none. Every commit lock this engine takes goes
// through lockProject, and sync.Once holds every concurrent first caller inside
// Do until the sweep returns, so the sweep always runs with none held. It runs
// under context.WithoutCancel: a Once never runs again, so a cancelled first
// caller must not cut it short for the engine's whole life.
func (e *Engine) lockProject(ctx context.Context, project string, timeout time.Duration) (*projectLock, error) {
	e.indexSweep.Do(func() { indexstore.ReapGoneProjects(context.WithoutCancel(ctx), e.vault) })
	sem := e.projectSem(project)
	recordLock("mutex-wait", project)
	if err := acquireSem(ctx, sem, timeout); err != nil {
		if errors.Is(err, vaultlock.ErrLockWaitTimeout) {
			return nil, fmt.Errorf("project %s is busy: %w", project, err)
		}
		return nil, err
	}
	recordLock("mutex-acquired", project)
	return &projectLock{e: e, project: project, sem: sem}, nil
}

// acquireSem sends on sem, waiting at most timeout. Once the send succeeds it
// returns nil, never an error, so a caller that sees an error never holds the
// semaphore and a caller that sees nil always releases it.
func acquireSem(ctx context.Context, sem chan struct{}, timeout time.Duration) error {
	select {
	case sem <- struct{}{}:
		return nil
	default:
	}
	if timeout == 0 {
		return vaultlock.ErrLockWaitTimeout
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if semWaitHook != nil {
		semWaitHook()
	}
	var expired <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		expired = t.C
	}
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-expired:
		return vaultlock.ErrLockWaitTimeout
	}
}

// Tx takes the project's index commit lock under the held mutex. The previous
// Tx must be finished first: the commit lock is a leaf, and one projectLock
// holds at most one.
func (pl *projectLock) Tx(ctx context.Context, timeout time.Duration) (*indexstore.Tx, error) {
	if pl.tx != nil && pl.tx.Held() {
		return nil, errors.New("search: a project lock already holds a commit lock")
	}
	if beforeCommitLockHook != nil {
		beforeCommitLockHook(pl.project)
	}
	tx, err := indexstore.Lock(ctx, pl.e.vault, pl.project, timeout)
	if err != nil {
		return nil, err
	}
	recordLock("commit-acquired", pl.project)
	pl.tx = tx
	return tx, nil
}

// finishTx commits the open Tx, if any, and reports the counter before and
// after, for the engine's remembered generation (see Engine.advanceGen).
func (pl *projectLock) finishTx() (before, after indexstore.Gen, err error) {
	tx := pl.tx
	if tx == nil {
		return before, after, nil
	}
	pl.tx = nil
	before = tx.Generation()
	err = tx.Commit()
	after = tx.Generation()
	recordLock("commit-released", pl.project)
	return before, after, err
}

// release finishes any open Tx (its writes are durable, so the counter records
// them) and then releases the mutex.
func (pl *projectLock) release() {
	if pl.tx != nil {
		if pl.tx.Held() {
			recordLock("commit-released", pl.project)
		}
		_ = pl.tx.Release()
		pl.tx = nil
	}
	recordLock("mutex-released", pl.project)
	<-pl.sem
}

// isLockTimeout reports whether err is a lock wait that ran out of time.
func isLockTimeout(err error) bool {
	return errors.Is(err, vaultlock.ErrLockWaitTimeout)
}
