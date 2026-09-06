// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// compatibleVaultCtx is aheadVaultCtx's healthy twin: a vault stamped at THIS
// binary's version, so the surface gate admits a mutating tool and the write
// actually happens. The stale-image advisory only has meaning on a write that
// succeeded.
func compatibleVaultCtx(t *testing.T) context.Context {
	t.Helper()
	root := t.TempDir()
	if err := surface.WriteStamp(filepath.Join(root, "Projects", "p"), surface.MCPSurfaceVersion, "tester"); err != nil {
		t.Fatalf("stage compatible stamp: %v", err)
	}
	return context.WithValue(context.Background(), vaultKey, storage.NewVault(root))
}

const fakeReplacedImage = "/home/u/.local/bin/vp (deleted)"

// wireReq builds a schema-valid tools/call request. Arguments must be an empty
// OBJECT, not null, or validateParams refuses before the gate is ever reached
// and the test would pass for the wrong reason.
func wireReq() mcplib.CallToolRequest {
	var req mcplib.CallToolRequest
	req.Params.Arguments = map[string]any{}
	return req
}

// staleImageRegistry returns a registry whose self-image probe reports a
// replaced binary, plus a mutating tool that records whether it ran.
func staleImageRegistry(t *testing.T, ran *bool) *Registry {
	t.Helper()
	reg := testRegistry(t)
	reg.selfImageReplaced = func() (bool, string) { return true, fakeReplacedImage }
	if err := reg.Register(Tool{
		Name:     "mut",
		Mutating: true,
		Handler:  func(context.Context, json.RawMessage) (any, error) { *ran = true; return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}
	return reg
}

// 🔴 TestStaleBinaryAdvisorySucceedsAndDoesNotRefuse is the operator ruling
// itself, as a test: "prior to mutating the vault, the operator be advised that
// his tooling may not do the right thing but is given the option to proceed
// anyway."
//
// The write MUST happen. A version of this that refused would satisfy nobody:
// it would lose work to protect a schema, which is the trade internal/surface's
// warn-only path already refuses to make for `vp hook`.
func TestStaleBinaryAdvisorySucceedsAndDoesNotRefuse(t *testing.T) {
	ran := false
	reg := staleImageRegistry(t, &ran)

	out, err := reg.Dispatch(compatibleVaultCtx(t), "mut", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("a replaced image REFUSED the write: %v — the advisory must never gate", err)
	}
	if !ran {
		t.Fatal("the handler did not run: the write was suppressed rather than advised")
	}
	if out != "ok" {
		t.Fatalf("result = %v, want the handler's own value passed through untouched", out)
	}
}

// 🔴 TestStaleBinaryAdvisoryFiresOnBothDispatchPaths is the pin the Chair asked
// for: gateIfMutating has two call sites, and whatever consults it must move at
// both or at neither.
//
// It works by exhausting the once-per-process latch from ONE path and proving
// the OTHER path was the one that consumed it. If the advisory were computed at
// a call site instead of inside the gate, one of these two sub-tests would find
// the latch still open — because the un-wired path would never have consumed it.
func TestStaleBinaryAdvisoryFiresOnBothDispatchPaths(t *testing.T) {
	t.Run("Dispatch consumes the latch", func(t *testing.T) {
		ran := false
		reg := staleImageRegistry(t, &ran)
		ctx := compatibleVaultCtx(t)

		if _, err := reg.Dispatch(ctx, "mut", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		// If Dispatch went through the gate, the latch is now closed.
		if reg.markStaleBinaryOnce() {
			t.Fatal("Dispatch did not consult the stale-image advisory — the latch is " +
				"still open, so this dispatch path is not wired")
		}
	})

	t.Run("the wire path consumes the latch", func(t *testing.T) {
		ran := false
		reg := staleImageRegistry(t, &ran)
		ctx := compatibleVaultCtx(t)

		rt := reg.tools["mut"]
		res, err := reg.makeHandler(rt)(ctx, wireReq())
		if err != nil {
			t.Fatal(err)
		}
		if res == nil || res.IsError {
			t.Fatalf("the wire path refused the write: %+v", res)
		}
		if reg.markStaleBinaryOnce() {
			t.Fatal("makeHandler did not consult the stale-image advisory — the latch " +
				"is still open, so this dispatch path is not wired")
		}
	})
}

// TestStaleBinaryAdvisoryLeadsTheContentArray — a host that truncates a large
// result keeps the head, so a trailing notice is cut exactly on the big writes
// where running a replaced binary costs most. Same reasoning as the drift
// banner, and asserted by POSITION rather than presence for the same reason.
func TestStaleBinaryAdvisoryLeadsTheContentArray(t *testing.T) {
	ran := false
	reg := staleImageRegistry(t, &ran)

	rt := reg.tools["mut"]
	res, err := reg.makeHandler(rt)(compatibleVaultCtx(t), wireReq())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) < 2 {
		t.Fatalf("content = %+v, want the advisory prepended to the handler's own output", res.Content)
	}
	first, ok := res.Content[0].(mcplib.TextContent)
	if !ok {
		t.Fatalf("leading content is %T, want the advisory banner", res.Content[0])
	}
	if !strings.Contains(first.Text, "SUCCEEDED") || !strings.Contains(first.Text, fakeReplacedImage) {
		t.Fatalf("leading content is not the stale-image advisory:\n%s", first.Text)
	}
}

// 🔴 TestStaleBinaryAdvisoryFiresOncePerProcess — a warning on every mutating
// call is the ignorable warning this whole advisory exists not to become. The
// second write must be silent.
func TestStaleBinaryAdvisoryFiresOncePerProcess(t *testing.T) {
	ran := false
	reg := staleImageRegistry(t, &ran)
	ctx := compatibleVaultCtx(t)
	rt := reg.tools["mut"]

	first, err := reg.makeHandler(rt)(ctx, wireReq())
	if err != nil {
		t.Fatal(err)
	}
	second, err := reg.makeHandler(rt)(ctx, wireReq())
	if err != nil {
		t.Fatal(err)
	}

	if len(second.Content) != len(first.Content)-1 {
		t.Fatalf("second call carried %d content items, want exactly one FEWER than the "+
			"first (%d) — the advisory repeated", len(second.Content), len(first.Content))
	}
	for _, c := range second.Content {
		if tc, ok := c.(mcplib.TextContent); ok && strings.Contains(tc.Text, "SUCCEEDED") {
			t.Fatal("the advisory fired twice; it must latch once per server process")
		}
	}
}

// TestStaleBinaryAdvisorySilentOnAHealthyImage is the positive control's twin:
// with a live image nothing is prepended, so a healthy session is never taught
// to skim the banner.
func TestStaleBinaryAdvisorySilentOnAHealthyImage(t *testing.T) {
	reg := testRegistry(t)
	reg.selfImageReplaced = func() (bool, string) { return false, "" }
	if err := reg.Register(Tool{
		Name:     "mut",
		Mutating: true,
		Handler:  func(context.Context, json.RawMessage) (any, error) { return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}

	res, err := reg.makeHandler(reg.tools["mut"])(compatibleVaultCtx(t), wireReq())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Content {
		if tc, ok := c.(mcplib.TextContent); ok && strings.Contains(tc.Text, "SUCCEEDED") {
			t.Fatal("a healthy image emitted the stale-binary advisory")
		}
	}
	if !reg.markStaleBinaryOnce() {
		t.Fatal("a healthy image consumed the latch — a later genuine staleness " +
			"would then go unreported for the rest of the process")
	}
}

// 🔴 TestStaleBinaryAdvisoryNotIssuedForAReadOnlyInvocation — the ruling is
// scoped to "prior to MUTATING the vault", and the interesting case is NOT a
// read-only tool (the Mutating early-return already covers that). It is a
// MUTATING tool whose particular invocation provably writes nothing — the
// param-aware refinement `vp_vault_sync action:"pull"` relies on.
//
// Spending the once-per-process latch on such a read would silence the advisory
// for the write that actually matters, which is strictly worse than never
// having built it.
func TestStaleBinaryAdvisoryNotIssuedForAReadOnlyInvocation(t *testing.T) {
	ctx := compatibleVaultCtx(t)
	ran := false
	rt := &registeredTool{tool: multiplexTool("mux", &ran)}
	reg := &Registry{selfImageReplaced: func() (bool, string) { return true, fakeReplacedImage }}

	// A read-only INVOCATION of a MUTATING tool: nothing is written, so nothing
	// is advised.
	advisory, err := reg.gateIfMutating(ctx, rt, json.RawMessage(`{"mode":"read"}`), nil)
	if err != nil {
		t.Fatalf("read-only invocation refused: %v", err)
	}
	if advisory != "" {
		t.Fatal("a read-only invocation of a mutating tool emitted the stale-image " +
			"advisory and consumed the once-per-process latch")
	}

	// The writing invocation of the SAME tool does get it — proving the guard
	// discriminates rather than simply never firing.
	advisory, err = reg.gateIfMutating(ctx, rt, json.RawMessage(`{"mode":"write"}`), nil)
	if err != nil {
		t.Fatalf("writing invocation refused: %v", err)
	}
	if advisory == "" {
		t.Fatal("the writing invocation got no advisory — the guard is not discriminating, " +
			"it is just off")
	}
}
