// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package vaultstack is the routing foundation of the mcp-vault-routing-per-
// project epic (ADR-012 Option C): a lazy registry mapping a vault ROOT to the
// per-vault {vault, engine, resolver} stack, so one long-lived `vp mcp` can
// serve N vaults. Each stack's embedder is a refcounted Handle lent by one
// process-wide embedder.SharedModelCache, so N engines share ONE resident model
// (the per-vault resident model is ~310 MB of private anonymous heap — see the
// epic Measurement — and the OS shares nothing across processes, so the sharing
// MUST be in-process).
//
// This package is registry + lifecycle ONLY. It does NOT route dispatch: the MCP
// handler layer (U5) resolves a request's project to a vault root (via the U1
// snapshot) and calls StackFor; nothing here imports internal/mcp.
package vaultstack

import (
	"context"
	"fmt"
	"sync"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// VaultStack is everything needed to serve one vault. Every field is per-vault
// EXCEPT the embedder model, which emb (a refcounted handle into the shared
// cache) lends from the one shared instance.
type VaultStack struct {
	Root     string
	Vault    *storage.Vault
	Cfg      storage.Config
	Engine   *search.Engine
	Resolver *vpctx.Resolver

	// emb is the SAME handle value the Engine holds (passed as the Engine's
	// embedder at construction). It is retained here for identity/ownership, but
	// it is NOT closed separately: VaultStack.Close closes the Engine, whose
	// Engine.Close -> embedder.Close() is the single decrement of this handle.
	// Closing emb here too would double-decrement the shared refcount.
	emb embedder.Handle
}

// NewVaultStack assembles a stack. The caller (cmd/vp's injected BuildFunc) has
// already Acquired handle from the shared cache and built eng with that SAME
// handle as its embedder — so the stack and the engine share one handle and
// Close decrements exactly once.
func NewVaultStack(root string, v *storage.Vault, cfg storage.Config, eng *search.Engine, resolver *vpctx.Resolver, handle embedder.Handle) *VaultStack {
	return &VaultStack{Root: root, Vault: v, Cfg: cfg, Engine: eng, Resolver: resolver, emb: handle}
}

// Close shuts the stack down. It closes ONLY the Engine: Engine.Close waits for
// the engine's warm/convert goroutines, closes its indexes, and then calls its
// embedder's Close — which is this stack's single shared-handle decrement. The
// shared model is Destroyed only when the LAST stack holding its key closes.
func (s *VaultStack) Close() error {
	if s == nil || s.Engine == nil {
		return nil
	}
	return s.Engine.Close()
}

// BuildFunc constructs a stack for a vault root. It is injected (by cmd/vp) so
// this package depends on neither cmd/vp nor the detached-launch/tools wiring the
// real build needs. It MUST return (nil, err) on any failure and never a stack
// with a nil Engine. ctx is honoured for cancellation.
type BuildFunc func(ctx context.Context, root string) (*VaultStack, error)

// stackBuild is one in-flight build. Joiners capture the pointer under the
// registry lock and read stack/err from it after done closes — never by
// re-looking-up the maps (which race the owner's delete-then-publish window).
type stackBuild struct {
	done  chan struct{}
	stack *VaultStack
	err   error
}

// Registry is the process-wide lazy vault-root -> stack map plus the one shared
// model cache. Safe for concurrent use.
type Registry struct {
	mu       sync.Mutex
	stacks   map[string]*VaultStack // root -> BUILT stack (success only)
	building map[string]*stackBuild // root -> in-flight build
	build    BuildFunc
	// models is the one shared cache the injected build closes over; the
	// registry owns it for the process lifetime. Handles drain it; it has no
	// Close of its own (the last handle's Close Destroys the model).
	models *embedder.SharedModelCache
}

// NewRegistry returns an empty registry. build is the injected stack builder and
// models is the shared cache that builder Acquires from.
func NewRegistry(build BuildFunc, models *embedder.SharedModelCache) *Registry {
	return &Registry{
		stacks:   make(map[string]*VaultStack),
		building: make(map[string]*stackBuild),
		build:    build,
		models:   models,
	}
}

// StackFor returns the stack for root, building it once and lazily. Concurrent
// callers for the same root share one build (the captured-pointer failure-retry
// joiner, mirroring search.Engine.ensureIndex/endBuild): a failed build is
// retryable (not memoized), a cancelling joiner leaves the in-flight entry for
// the owner, and the owner publishes/cleans up in a defer so a build panic still
// releases joiners.
func (r *Registry) StackFor(ctx context.Context, root string) (*VaultStack, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.Lock()
	if s, ok := r.stacks[root]; ok {
		r.mu.Unlock()
		return s, nil
	}
	if b, ok := r.building[root]; ok {
		// Joiner: capture THIS pointer, wait on it, read its result. Never
		// re-look-up the maps after the wait — the owner's delete-then-publish
		// window would otherwise read as "absent" and start a second build.
		r.mu.Unlock()
		select {
		case <-b.done:
			return b.stack, b.err
		case <-ctx.Done():
			// Leave the building entry in place for the owner; do NOT delete it
			// and do NOT start a second build.
			return nil, ctx.Err()
		}
	}
	// Owner: publish the in-flight marker, build outside the lock.
	b := &stackBuild{done: make(chan struct{})}
	r.building[root] = b
	r.mu.Unlock()

	// Defer the cleanup+publish so a panic in build still clears building and
	// releases joiners (mirrors ensureIndex's deferred endBuild).
	defer func() {
		r.mu.Lock()
		delete(r.building, root)
		if b.err == nil && b.stack != nil {
			r.stacks[root] = b.stack
		}
		r.mu.Unlock()
		close(b.done)
	}()

	b.stack, b.err = r.build(ctx, root)
	// Never publish a half-built stack as success.
	if b.err == nil && (b.stack == nil || b.stack.Engine == nil) {
		b.stack, b.err = nil, fmt.Errorf("vaultstack: build returned a nil or engineless stack for %q", root)
	}
	return b.stack, b.err
}

// Close closes every built stack (each decrements its shared handle; the shared
// model Destroys at the last handle) and clears the maps. In-flight builds are
// not waited on — Close runs at process shutdown after serving has stopped.
func (r *Registry) Close() error {
	r.mu.Lock()
	stacks := r.stacks
	r.stacks = make(map[string]*VaultStack)
	r.building = make(map[string]*stackBuild)
	r.mu.Unlock()

	var firstErr error
	for _, s := range stacks {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
