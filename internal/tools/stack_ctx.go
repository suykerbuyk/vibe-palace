// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultstack"
)

// U5a of mcp-vault-routing-per-project: handlers read their vault / engine /
// resolver from the request's VaultStack on ctx (installed by the MCP dispatch
// seam), falling back to the value they closed over at registration when no
// stack is installed. At N=1 the installed stack IS the startup stack, so the
// resolved value equals the closed-over one and behavior is unchanged; the
// point of U5a is that the read is now ctx-first, so U5b can route per request
// by installing a different stack at dispatch WITHOUT touching any handler.
//
// Each handler shadows its captured dependency with one of these at the top of
// its closure (e.g. `vault := vaultFromCtx(ctx, vault)`). The closed-over
// parameter is retained as the fallback — a later cleanup (post-U5b, once every
// path installs a stack) can drop the vestigial params.
//
// OMISSION RISK (matters before U5b's refusal->route flip): a handler that
// forgets to shadow keeps silently reading its closed-over vault, which a
// single-tool test cannot catch. TestEveryProjectKeyedToolResolvesVaultFromCtx
// (stack_ctx_test.go) is the table that turns such an omission into a red test.

// vaultFromCtx returns the request stack's vault, or the handler's closed-over
// vault when no stack is on ctx (throwaway registry / handler-level test).
func vaultFromCtx(ctx context.Context, fallback *storage.Vault) *storage.Vault {
	if st := vaultstack.StackFrom(ctx); st != nil {
		return st.Vault
	}
	return fallback
}

// resolverFromCtx returns the request stack's resolver, or the closed-over one
// when no stack is on ctx.
func resolverFromCtx(ctx context.Context, fallback *vpctx.Resolver) *vpctx.Resolver {
	if st := vaultstack.StackFrom(ctx); st != nil {
		return st.Resolver
	}
	return fallback
}

// engineFromCtx returns the request stack's engine, or the closed-over one when
// no stack is on ctx. A routed stack's Engine is non-nil by the vaultstack
// BuildFunc contract (StackFor rejects an engineless stack), so engine-only
// handlers (vp_search, vp_search_cross_project, vp_refresh_index) keep assuming
// a non-nil engine — a nil here is a programming error, NOT a signal to fall
// back to a structural ranker. Only bootstrap, which is registered even with no
// engine, has (and keeps) its own nil-engine branch.
func engineFromCtx(ctx context.Context, fallback *search.Engine) *search.Engine {
	if st := vaultstack.StackFrom(ctx); st != nil {
		return st.Engine
	}
	return fallback
}
