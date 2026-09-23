// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// departedRoot is a vault (no git) where "old" was renamed to "new".
func departedRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Projects", "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := (departure.Record{Slug: "old", Kind: departure.Renamed, To: "new", Date: "2026-09-23"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(root, filepath.FromSlash(departure.RelPath("old")))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// projectTool is a mutating tool with a `project` parameter, the shape every
// project-taking production tool has. `mode:"read"` makes the invocation
// read-only, exactly as multiplexTool does.
func projectTool(ran *bool) Tool {
	return Tool{
		Name:     "vp_fake_project_writer",
		Mutating: true,
		Schema:   json.RawMessage(`{"type":"object","properties":{"project":{"type":"string"},"to_project":{"type":"string"},"mode":{"type":"string"}}}`),
		ReadOnlyWhen: func(params json.RawMessage) bool {
			var p struct {
				Mode string `json:"mode"`
			}
			return json.Unmarshal(params, &p) == nil && p.Mode == "read"
		},
		Handler: func(context.Context, json.RawMessage) (any, error) { *ran = true; return "ok", nil },
	}
}

// T9. The refusal holds on BOTH dispatch paths: in-process Dispatch, and the
// JSON-RPC tools/call path (makeHandler) every real host uses.
func TestGateRefusesADepartedProjectOnMakeHandler(t *testing.T) {
	root := departedRoot(t)

	t.Run("dispatch", func(t *testing.T) {
		reg := testRegistry(t)
		ran := false
		if err := reg.Register(projectTool(&ran)); err != nil {
			t.Fatal(err)
		}
		ctx := context.WithValue(context.Background(), vaultKey, storage.NewVault(root))
		_, err := reg.Dispatch(ctx, "vp_fake_project_writer", json.RawMessage(`{"project":"old"}`))
		if err == nil || !strings.Contains(err.Error(), `renamed to "new"`) {
			t.Fatalf("Dispatch must refuse a departed project with the redirect, got %v", err)
		}
		if ran {
			t.Fatal("the handler ran despite the refusal")
		}
	})

	t.Run("tools_call", func(t *testing.T) {
		srv := NewServer(storage.NewVault(root))
		ran := false
		if err := srv.Registry().Register(projectTool(&ran)); err != nil {
			t.Fatal(err)
		}
		initServer(t, srv)
		msg := json.RawMessage(`{"jsonrpc":"2.0","id":7,"method":"tools/call",
			"params":{"name":"vp_fake_project_writer","arguments":{"to_project":"old"}}}`)
		resp, ok := srv.HandleMessage(context.Background(), msg).(mcplib.JSONRPCResponse)
		if !ok {
			t.Fatal("expected a JSONRPCResponse")
		}
		raw, _ := json.Marshal(resp.Result)
		if !strings.Contains(string(raw), `"isError":true`) || !strings.Contains(string(raw), `renamed to \"new\"`) {
			t.Fatalf("tools/call must refuse a departed to_project with the redirect, got %s", raw)
		}
		if ran {
			t.Fatal("the handler ran despite the refusal")
		}
	})
}

// G3. A READ of a departed project is admitted: the refusal is a reason to
// refuse a WRITE, and readOnlyInvocation runs before it.
func TestGateAllowsAReadOnlyInvocationOfADepartedProject(t *testing.T) {
	reg := testRegistry(t)
	ran := false
	if err := reg.Register(projectTool(&ran)); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), vaultKey, storage.NewVault(departedRoot(t)))
	if _, err := reg.Dispatch(ctx, "vp_fake_project_writer", json.RawMessage(`{"project":"old","mode":"read"}`)); err != nil {
		t.Fatalf("a read-only invocation must pass: %v", err)
	}
	if !ran {
		t.Fatal("the read should have reached the handler")
	}
}

// G4. A slug with no record and no history — a brand-new project's first
// write — is left alone.
func TestGateLeavesANeverSeenSlugAlone(t *testing.T) {
	reg := testRegistry(t)
	ran := false
	if err := reg.Register(projectTool(&ran)); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), vaultKey, storage.NewVault(departedRoot(t)))
	if _, err := reg.Dispatch(ctx, "vp_fake_project_writer", json.RawMessage(`{"project":"brand-new"}`)); err != nil {
		t.Fatalf("a never-seen slug must pass: %v", err)
	}
	if !ran {
		t.Fatal("the write should have reached the handler")
	}
}
