// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// fakeSession implements the bare server.ClientSession interface WITHOUT
// SessionWithClientInfo — the shape of a transport session that cannot carry
// clientInfo at all.
type fakeSession struct{ id string }

func (f *fakeSession) Initialize()       {}
func (f *fakeSession) Initialized() bool { return true }
func (f *fakeSession) SessionID() string { return f.id }
func (f *fakeSession) NotificationChannel() chan<- mcplib.JSONRPCNotification {
	return nil
}

// fakeClientInfoSession adds SessionWithClientInfo, mirroring what mcp-go's
// stdio/streamable-HTTP sessions implement: handleInitialize calls
// SetClientInfo, tool handlers read GetClientInfo.
type fakeClientInfoSession struct {
	fakeSession
	info mcplib.Implementation
	caps mcplib.ClientCapabilities
}

func (f *fakeClientInfoSession) GetClientInfo() mcplib.Implementation  { return f.info }
func (f *fakeClientInfoSession) SetClientInfo(i mcplib.Implementation) { f.info = i }
func (f *fakeClientInfoSession) GetClientCapabilities() mcplib.ClientCapabilities {
	return f.caps
}
func (f *fakeClientInfoSession) SetClientCapabilities(c mcplib.ClientCapabilities) {
	f.caps = c
}

// sessionContext attaches a client session to a context the way the mcp-go
// transports do, via the server's exported WithContext.
func sessionContext(t *testing.T, sess server.ClientSession) context.Context {
	t.Helper()
	return testServer(t).mcp.WithContext(context.Background(), sess)
}

// No session on the context — the HandleMessage test/dispatch seam registers
// none. Must report absent, never a fabricated default.
func TestClientInfoFromContextNoSession(t *testing.T) {
	info, ok := ClientInfoFromContext(context.Background())
	if ok {
		t.Error("ok = true, want false for a context with no session")
	}
	if info.Name != "" {
		t.Errorf("info.Name = %q, want empty", info.Name)
	}
}

// A session that does not implement SessionWithClientInfo is absent, not an
// error and not a default.
func TestClientInfoFromContextSessionWithoutClientInfo(t *testing.T) {
	ctx := sessionContext(t, &fakeSession{id: "s1"})
	if _, ok := ClientInfoFromContext(ctx); ok {
		t.Error("ok = true, want false for a session without clientInfo support")
	}
}

// A zero Implementation — a tool call before initialize, or a client that
// sent no name — is absent. An empty name must never be confirmed.
func TestClientInfoFromContextZeroClientInfo(t *testing.T) {
	ctx := sessionContext(t, &fakeClientInfoSession{fakeSession: fakeSession{id: "s2"}})
	if _, ok := ClientInfoFromContext(ctx); ok {
		t.Error("ok = true, want false for a zero clientInfo")
	}
}

// The populated case: initialize recorded clientInfo on the session, and any
// later tool call sees it.
func TestClientInfoFromContextPopulated(t *testing.T) {
	sess := &fakeClientInfoSession{fakeSession: fakeSession{id: "s3"}}
	sess.SetClientInfo(mcplib.Implementation{Name: "Zed", Version: "0.191.0"})
	ctx := sessionContext(t, sess)

	info, ok := ClientInfoFromContext(ctx)
	if !ok {
		t.Fatal("ok = false, want true for a populated clientInfo")
	}
	if info.Name != "Zed" {
		t.Errorf("info.Name = %q, want %q", info.Name, "Zed")
	}
	if info.Version != "0.191.0" {
		t.Errorf("info.Version = %q, want %q", info.Version, "0.191.0")
	}
}


