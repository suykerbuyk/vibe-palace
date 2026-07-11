// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/server"
)

// StreamableHTTPHandler builds the streamable-HTTP MCP handler for this server
// and wraps it in bearer-token authentication. When token is empty the handler
// is served open (no auth enforced) — the operator is expected to front it with
// a tunnel or network ACL they control; the caller (cmd/vp) emits a startup
// warning in that case. No CORS headers are added: both real clients connect
// server-side, so browser preflight is out of scope for this transport.
//
// The vault is injected into the request context exactly as the stdio transport
// does via Server.contextFunc. This is load-bearing for the surface gate: until
// it was added, no contextFunc ran on this transport, so VaultFromContext
// returned nil, gateIfMutating saw root == "", and every mutating tool served
// over `vp mcp serve --allow-writes` bypassed the surface gate entirely.
func (s *Server) StreamableHTTPHandler(token string) http.Handler {
	streamable := server.NewStreamableHTTPServer(s.mcp,
		server.WithHTTPContextFunc(s.httpContextFunc),
	)
	return bearerAuthMiddleware(token, streamable)
}

// httpContextFunc enriches an inbound HTTP request context with the vault
// reference, mirroring Server.contextFunc on the stdio transport. The request is
// unused: the vault is a per-server property, not a per-request one.
func (s *Server) httpContextFunc(ctx context.Context, _ *http.Request) context.Context {
	return context.WithValue(ctx, vaultKey, s.vault)
}

// DeleteTools removes the named tools from the underlying MCP server so they no
// longer appear in tools/list or accept tools/call. It is a thin passthrough so
// the caller can stand up a dedicated HTTP server instance and strip mutating
// tools from it without reaching into package internals. Unknown names are
// ignored by the underlying server.
func (s *Server) DeleteTools(names ...string) {
	s.mcp.DeleteTools(names...)
}

// bearerAuthMiddleware enforces "Authorization: Bearer <token>" on next.
//
// When token is empty the middleware is a pass-through: no authentication is
// enforced. This is intentional — an operator may run the server locally behind
// a tunnel they control. Otherwise the presented token must exactly match the
// expected one; on any missing, malformed, or mismatched header the request is
// rejected with 401 and next is not invoked.
//
// The comparison runs in constant time regardless of the presented value's
// length: both sides are reduced to a fixed-width SHA-256 digest before
// subtle.ConstantTimeCompare, so neither a length difference nor an early byte
// mismatch leaks timing information about the expected token.
func bearerAuthMiddleware(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}

	want := sha256.Sum256([]byte(token))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			unauthorized(w)
			return
		}

		got := sha256.Sum256([]byte(presented))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			unauthorized(w)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// bearerToken extracts the credential from an Authorization header value,
// requiring the "Bearer " scheme (case-insensitive per RFC 7235). It returns
// ("", false) when the scheme is absent or the credential is empty.
func bearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(header[len(prefix):])
	if tok == "" {
		return "", false
	}
	return tok, true
}

// unauthorized writes a 401 with a short plain-text body.
func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte("unauthorized\n"))
}
