// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"io"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// contextKey is an unexported type for context keys in this package.
type contextKey string

const vaultKey contextKey = "vault"

// ServerInstructions is the server-level bootstrap directive returned to MCP
// clients in the initialize response. Hosts that surface server instructions
// (Grok, etc.) show this on connect even before any AGENTS.md/CLAUDE.md block
// is read, so the first message already knows to load project context.
//
// It is exported so the capability-service tool vp_manual can echo the exact
// string a connecting client receives, without maintaining a second copy.
const ServerInstructions = "Call vp_bootstrap_context at session start for full project context — resume, workflow rules, active tasks, and recent sessions. When the user types vpc-<name> (e.g. vpc-restart, vpc-wrap), call vp_cmd with name=<name> and follow the returned instructions. When the user types vps-<name>, call vp_skill with name=<name>. Call vp_capture_session at the end of each work unit. Large bodies may be delivered as vibe-palace:// resources — read them natively via resources/read or page them with vp_read_resource."

// VaultFromContext extracts the Vault from a handler context.
// Returns nil if no vault is present.
func VaultFromContext(ctx context.Context) *storage.Vault {
	v, _ := ctx.Value(vaultKey).(*storage.Vault)
	return v
}

// ClientInfoFromContext returns the clientInfo the MCP initialize handshake
// recorded on the client session flowing in ctx, and whether a client name was
// actually CONFIRMED. It is a thin read over the session mcp-go already flows
// on every transport (stdio, streamable HTTP, SSE, in-process) — deliberately
// NOT a parallel plumbing path, and deliberately NOT contextFunc: contextFunc
// runs once in Listen before the read loop, before initialize has been
// processed, so it can never see clientInfo; GetClientInfo reads the same
// session object's atomic that handleInitialize populated, so any tool call
// after initialize sees it.
//
// ok is false for every degraded shape — no session on ctx (the HandleMessage
// test/dispatch seam registers none), a session that does not implement
// SessionWithClientInfo, and a zero/unnamed Implementation (a tool call before
// initialize, or a client that sent no name). Callers must treat false as
// ABSENT, never substitute a default: per ADR-006 a fabricated host is worse
// than a recorded unknown, because it is indistinguishable from a measured one.
func ClientInfoFromContext(ctx context.Context) (mcplib.Implementation, bool) {
	s := server.ClientSessionFromContext(ctx)
	if s == nil {
		return mcplib.Implementation{}, false
	}
	sci, ok := s.(server.SessionWithClientInfo)
	if !ok {
		return mcplib.Implementation{}, false
	}
	info := sci.GetClientInfo()
	if info.Name == "" {
		return mcplib.Implementation{}, false
	}
	return info, true
}

// Server wraps an MCP server with vault context injection.
type Server struct {
	mcp      *server.MCPServer
	vault    *storage.Vault
	registry *Registry
}

// NewServer creates an MCP server backed by the given vault.
func NewServer(vault *storage.Vault) *Server {
	s := server.NewMCPServer(
		"vibe-palace",
		"0.1.0",
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(false, false),
		server.WithRecovery(),
		server.WithInstructions(ServerInstructions),
	)
	srv := &Server{mcp: s, vault: vault}
	srv.registry = NewRegistry(s)
	return srv
}

// Registry returns the tool registry for registering and managing tools.
func (s *Server) Registry() *Registry {
	return s.registry
}

// Listen starts the MCP server on the provided reader/writer pair. It blocks
// until the reader is closed or ctx is cancelled.
func (s *Server) Listen(ctx context.Context, r io.Reader, w io.Writer) error {
	stdio := server.NewStdioServer(s.mcp)
	stdio.SetContextFunc(s.contextFunc)
	return stdio.Listen(ctx, r, w)
}

// contextFunc returns a context enriched with the vault reference.
func (s *Server) contextFunc(ctx context.Context) context.Context {
	return context.WithValue(ctx, vaultKey, s.vault)
}

// HandleMessage exposes the protocol layer for unit testing without stdio
// plumbing.
//
// It applies contextFunc before delegating, exactly as the stdio and
// streamable-HTTP transports do. Without that, this seam was the one dispatch
// path where VaultFromContext returned nil — so the surface gate silently
// no-opped in every test that went through it, and the tests could not have
// caught a gate regression.
func (s *Server) HandleMessage(ctx context.Context, msg json.RawMessage) mcplib.JSONRPCMessage {
	return s.mcp.HandleMessage(s.contextFunc(ctx), msg)
}
