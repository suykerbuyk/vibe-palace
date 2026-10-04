// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// NoTimeout makes Lock wait for the index commit lock until ctx is done. The
// ingester and the rebuild use it; searches and other note-time writers pass a
// finite timeout and skip their own write when it expires.
const NoTimeout time.Duration = -1

// ErrProjectGone is returned by Lock when, under the commit lock, the project
// is no longer in the vault. No Tx is returned, so nothing recreates the
// project's index directory.
var ErrProjectGone = errors.New("indexstore: project is no longer in the vault")

// Tx is a held index commit lock for one project of one vault (ADR-014
// decision 7, "Two locks"). Every write to the host-local index is a method on
// *Tx, so a write without the lock does not compile.
//
// The commit lock is short: it is held only to write data already computed,
// never while embedding, and never across a run. It is a leaf: no process holds
// two at once, and none takes the run lock while holding one.
type Tx struct {
	vault   *storage.Vault
	project string
	genPath string
	files   projectFiles

	mu          sync.Mutex
	release     func() error // nil once finished
	lockGen     Gen          // the counter as Lock found it
	gen         Gen          // the counter: as found by Lock, then as left by Commit
	wrote       bool
	changeEpoch bool
	preBumped   bool   // the counter was bumped before a destructive step
	st          *state // loaded on the first write or read that needs it
	broken      bool   // a write failed part-way: the cached state is not trusted

	recipe             *index.ChunkRecipe // set by UseRecipe
	fingerprintChecked bool               // chunks.fingerprint is known to exist
}

// Lock takes the index commit lock for project and returns the Tx that every
// write goes through.
//
// It waits until the lock is taken, timeout elapses (the error wraps
// vaultlock.ErrLockWaitTimeout; 0 tries once, NoTimeout never expires) or ctx
// is done (ctx's error), whichever comes first. A cancel is noticed during
// the wait. On an error nothing was acquired.
//
// Under the lock it checks that the project still exists
// (storage.Vault.ProjectExists, the rule search.ProjectExists applies) and
// returns ErrProjectGone if not. It then makes sure the store counter exists,
// replacing a malformed one (ensureGenFile).
//
// Known limit: Lock on a project that is gone, or was never there, still
// creates that project's lock file in palace/.local/locks/.
// Lock files are never deleted while a process may hold one open, so a gone
// project's lock file is left in place: a known, unowned residual of one empty
// file per slug per host.
func Lock(ctx context.Context, vault *storage.Vault, project string, timeout time.Duration) (*Tx, error) {
	lockPath, err := vault.IndexCommitLockPath(project)
	if err != nil {
		return nil, err
	}
	genPath, err := vault.IndexGenerationPath(project)
	if err != nil {
		return nil, err
	}
	pf, err := filesFor(vault, project)
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
	if commitWaitHook != nil {
		commitWaitHook()
	}
	release, err := vaultlock.AcquireFileWithTimeout(ctx, lockPath, timeout)
	if err != nil {
		return nil, err
	}
	record(lockEvent{kind: evCommitAcquired, project: project})
	unlock := func() error {
		record(lockEvent{kind: evCommitReleased, project: project})
		return release()
	}

	exists, err := vault.ProjectExists(project)
	if err != nil {
		_ = unlock()
		return nil, err
	}
	if !exists {
		_ = unlock()
		return nil, fmt.Errorf("%w: %s", ErrProjectGone, project)
	}

	g, err := ensureGenFile(genPath)
	if err != nil {
		_ = unlock()
		return nil, err
	}
	return &Tx{
		vault:   vault,
		project: project,
		genPath: genPath,
		files:   pf,
		release: unlock,
		lockGen: g,
		gen:     g,
	}, nil
}

// commitWaitHook, when a test sets it, runs just before Lock waits for the
// commit lock, so a test can act while a waiter is known to be past every
// check Lock makes before acquiring.
var commitWaitHook func()

// Generation is the store's change counter. Until Commit it is the counter as
// Lock found it under the lock; after Commit it is the counter as this Tx left
// it.
//
// A holder of in-memory store state (the chunk id set, the ledger, the local
// KG key index) remembers the Generation its state matches, and re-reads that
// state when a later Tx's Generation differs, which means another writer
// changed the store. After its own Commit it remembers the new Generation, so
// it does not re-read its own write. That is valid only for a holder whose
// in-memory state reflects every write this Tx made; a holder that saw only
// some of them must keep its old value and reload. Each holder keeps its own remembered
// value: one holder's Lock never consumes another holder's signal.
func (tx *Tx) Generation() Gen {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	return tx.gen
}

// noteWrite records that a write method changed the store. moreThanAppend is
// set by every write that is more than an append, which changes the epoch.
// Write methods call it only after their write succeeded, so a failed write
// leaves the counter alone.
func (tx *Tx) noteWrite(moreThanAppend bool) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	tx.wrote = true
	if moreThanAppend {
		tx.changeEpoch = true
	}
}

// beginDestructive bumps the store counter, with a new epoch, BEFORE the
// first step of a write that is more than an append (a rewrite, a removal, a
// torn-tail cut), once per Tx. If this process dies part-way through that
// write, the counter has already moved, so every other process that cached
// the store's state (a long-lived MCP server's id set and ledger) reloads it
// instead of appending against a store that is no longer what it remembers.
//
// The Tx still bumps the counter again when it finishes, with a new epoch: a
// lock-free reader may have read the store between this bump and the write,
// and must see the write's own change. So a commit that did more than append
// moves gen by two.
func (tx *Tx) beginDestructive() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.preBumped {
		return nil
	}
	g, err := bumpGenFile(tx.genPath, true)
	if err != nil {
		return err
	}
	tx.preBumped, tx.gen = true, g
	tx.wrote, tx.changeEpoch = true, true
	return nil
}

// Commit finishes the Tx: if it wrote anything, it bumps the store counter
// (Gen by one, and the epoch when a write was more than an append), then it
// releases the commit lock. A Tx that wrote nothing leaves the counter alone.
// Calling it twice is an error; Release after Commit is a no-op.
//
// Known limit: the counter is bumped after the records are written, so a
// process that dies between the two leaves records no counter announces. A
// running engine then misses that append until the next counter change; the
// next Commit on the project covers it, and a fresh process reads the store
// whole. Whether coverage must also notice it is
// search-index-completeness-and-build-serialization's to decide.
func (tx *Tx) Commit() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.release == nil {
		return errors.New("indexstore: Tx already finished")
	}
	return tx.finishLocked()
}

// Release finishes a Tx that was not committed. Writes already made are
// durable, so it records them in the counter exactly as Commit does, and then
// releases the lock. It is idempotent, and safe to defer after Commit.
func (tx *Tx) Release() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.release == nil {
		return nil
	}
	return tx.finishLocked()
}

func (tx *Tx) finishLocked() error {
	var gerr error
	if tx.wrote {
		var g Gen
		if g, gerr = bumpGenFile(tx.genPath, tx.changeEpoch); gerr == nil {
			tx.gen = g
		}
	}
	// The cached state saw every write this Tx made, unless one failed part
	// way or the counter could not be bumped.
	switch {
	case tx.broken || gerr != nil:
		putState(tx.vault.Root, tx.project, nil)
	case tx.st != nil:
		tx.st.gen = tx.gen
		putState(tx.vault.Root, tx.project, tx.st)
	}
	rerr := tx.release()
	tx.release = nil
	if gerr != nil {
		return gerr
	}
	return rerr
}
