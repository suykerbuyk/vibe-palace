// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultlock

import (
	"errors"
	"fmt"
	"sync/atomic"
)

// ErrNotRootLock is RequireRoot's refusal: the token is nil, released, or
// guards something other than its own vault's root commit lock.
var ErrNotRootLock = errors.New("vaultlock: not a held vault root lock")

// Held is a lock this process holds, as a value: the vault root whose
// .vp-locks directory holds the sidecar, and the key it guards.
//
// 🔴 THE TOKEN CARRIES THE ROOT, SO A LOCK-HELD CALLEE TAKES NO ROOT ARGUMENT.
// Acquire is a blocking, non-reentrant LOCK_EX, so a caller already holding a
// vault's root lock cannot call anything that takes it again. The lock-held
// variants in internal/storage accept a *Held instead and derive the vault
// from it: a separate root parameter would let a token for vault X be passed
// with root Y, and the callee would then write to Y believing it was
// serialised.
type Held struct {
	root     string
	key      string
	release  func() error
	released atomic.Bool
}

// AcquireHeld is Acquire returning a *Held token rather than a bare release
// function. AcquireHeld(root, root) takes the vault root commit lock that
// every vp committer takes.
func AcquireHeld(vaultRoot, targetAbsPath string) (*Held, error) {
	key := canonicalKey(targetAbsPath)
	release, err := acquireByKey(vaultRoot, key)
	if err != nil {
		return nil, err
	}
	return &Held{root: vaultRoot, key: key, release: release}, nil
}

// Root is the vault root the token was acquired under, exactly as passed.
func (h *Held) Root() string { return h.root }

// Release unlocks. It is idempotent and safe to defer.
func (h *Held) Release() error {
	h.released.Store(true)
	return h.release()
}

// RequireRoot returns nil only when h is live and is the root commit lock of
// h.Root(): the lock AcquireHeld(root, root) takes. A per-path lock under the
// same vault, or a lock whose key names another vault's root, is refused —
// holding either excludes none of the committers the root lock excludes.
func (h *Held) RequireRoot() error {
	if h == nil {
		return fmt.Errorf("%w: no token", ErrNotRootLock)
	}
	if h.released.Load() {
		return fmt.Errorf("%w: the token for %s was released", ErrNotRootLock, h.root)
	}
	if want := canonicalKey(h.root); h.key != want {
		return fmt.Errorf("%w: the token guards %s, not the root of %s", ErrNotRootLock, h.key, h.root)
	}
	return nil
}
