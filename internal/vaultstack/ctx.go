// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultstack

import "context"

// stackCtxKey is the private context key under which a request's VaultStack
// travels. A private empty-struct type cannot collide with any other package's
// context key.
type stackCtxKey struct{}

// WithStack returns a child context carrying s as the stack to serve the current
// request on. Dispatch (internal/mcp) installs it per request; handlers
// (internal/tools) read it with StackFrom. This is the seam the per-project
// routing (U5b) swaps the routed stack in at — the handler read path is already
// ctx-first, so U5b installs a different stack here and touches no handler.
func WithStack(ctx context.Context, s *VaultStack) context.Context {
	return context.WithValue(ctx, stackCtxKey{}, s)
}

// StackFrom returns the VaultStack installed on ctx, or nil if none is. A nil
// return is the un-wired path (a throwaway registry, a handler-level test): the
// caller falls back to its closed-over vault/engine/resolver, preserving the
// pre-routing behavior.
func StackFrom(ctx context.Context) *VaultStack {
	s, _ := ctx.Value(stackCtxKey{}).(*VaultStack)
	return s
}
