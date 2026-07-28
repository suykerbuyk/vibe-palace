// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// TestIntegrationCheck drives vp_check through the FULL MCP stack: the real
// tool set is registered via tools.RegisterAll and the tool is dispatched BY
// NAME over the JSON-RPC tools/call path (Server.HandleMessage), exactly as an
// MCP client would, against a real on-disk vault.
//
// This is the test that proves DELIVERY rather than logic. The diagnostic suite
// existed for iterations while reaching no agent on any host, because nothing
// exposed it over MCP; a green unit test on an unreachable tool is precisely
// this task's own failure mode.
func TestIntegrationCheck(t *testing.T) {
	// dispatch unmarshals the tool's JSON text content back into the tool's own
	// result struct, proving the envelope survives the wire.
	dispatch := func(t *testing.T, h *testHarness, args map[string]any) tools.CheckSuiteResult {
		t.Helper()
		raw := h.callTool(t, "vp_check", args)
		var out tools.CheckSuiteResult
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("decode CheckSuiteResult: %v\n%s", err, raw)
		}
		return out
	}

	// vp_check is advertised on tools/list and vp_check_resume_refs — which it
	// subsumes — is gone from it.
	t.Run("reachable_over_tools_list", func(t *testing.T) {
		h := newHarness(t, false)
		h.registerAllTools(t)
		h.initMCP(t)

		resp := h.Server.HandleMessage(context.Background(), json.RawMessage(`{
			"jsonrpc": "2.0", "id": 2, "method": "tools/list"
		}`))
		rpcList, ok := resp.(mcplib.JSONRPCResponse)
		if !ok {
			t.Fatalf("tools/list: want JSONRPCResponse, got %T: %+v", resp, resp)
		}
		listRaw, _ := json.Marshal(rpcList.Result)
		var listOut struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(listRaw, &listOut); err != nil {
			t.Fatalf("unmarshal tools/list: %v", err)
		}
		names := map[string]bool{}
		for _, tl := range listOut.Tools {
			names[tl.Name] = true
		}
		if !names["vp_check"] {
			t.Error("tools/list missing vp_check — the checks reach no agent")
		}
		if names["vp_check_resume_refs"] {
			t.Error("tools/list still advertises vp_check_resume_refs; vp_check subsumes it")
		}
		if !names["vp_surface_check"] {
			t.Error("tools/list missing vp_surface_check — it stays as the surface preflight")
		}
	})

	// Omitting the optional argument runs the whole cheap suite, in the
	// declared order, twice over with identical ordering.
	t.Run("default_runs_all_deterministically", func(t *testing.T) {
		h := newHarness(t, false)
		h.registerAllTools(t)

		first := dispatch(t, h, map[string]any{})
		if len(first.Checks) != len(check.ProducerOrder) {
			t.Fatalf("default run produced %d rows, want %d", len(first.Checks), len(check.ProducerOrder))
		}
		second := dispatch(t, h, map[string]any{})

		var a, b []string
		for i := range first.Checks {
			a = append(a, first.Checks[i].Name)
		}
		for i := range second.Checks {
			b = append(b, second.Checks[i].Name)
		}
		if strings.Join(a, ",") != strings.Join(b, ",") {
			t.Fatalf("checks[] reordered between identical runs:\n %v\n %v", a, b)
		}

		// Never the embedder: the selector path must not reach check.Run.
		raw, _ := json.Marshal(first)
		if strings.Contains(string(raw), "Embedder") {
			t.Errorf("vp_check emitted an Embedder row over the wire:\n%s", raw)
		}
	})

	// The verdicts the wire carries are the verdicts the CLI computes for the
	// same selector. Compared at the check.Result level ONLY: `vp check --json`
	// builds the entire MCP tool registry for its binary block unless the filter
	// is exactly {"surface"}, so envelope parity is explicitly not the contract.
	t.Run("verdicts_match_the_cli_selector", func(t *testing.T) {
		h := newHarness(t, false)
		h.registerAllTools(t)

		dir := filepath.Join(h.Vault.Root, "Projects", "refproj")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		body := "## State\n\nplan: ~/.claude/plans/active.md\n"
		if err := os.WriteFile(filepath.Join(dir, "resume.md"), []byte(body), 0o644); err != nil {
			t.Fatalf("write resume: %v", err)
		}

		const selector = "resume-refs"
		want, err := check.RunSelected(h.Vault.Root, selector)
		if err != nil {
			t.Fatalf("check.RunSelected: %v", err)
		}

		out := dispatch(t, h, map[string]any{"checks": []string{selector}})

		if len(out.Checks) != len(want) {
			t.Fatalf("got %d rows over the wire, CLI selector produced %d", len(out.Checks), len(want))
		}
		for i, w := range want {
			got := out.Checks[i]
			if got.Name != w.Name {
				t.Errorf("row %d name = %q, want %q", i, got.Name, w.Name)
			}
			if got.Summary != w.Summary {
				t.Errorf("row %d summary = %q, want %q", i, got.Summary, w.Summary)
			}
			if strings.Join(got.Details, "\n") != strings.Join(w.Details, "\n") {
				t.Errorf("row %d details = %v, want %v", i, got.Details, w.Details)
			}
		}
		// The verdict itself, and the detail array the removed wrapper used to
		// carry, both survive the round-trip.
		if out.Checks[0].Status != "info" {
			t.Errorf("status = %q, want info for a resume carrying a host-local plan ref", out.Checks[0].Status)
		}
		if !strings.Contains(strings.Join(out.Checks[0].Details, "\n"), "~/.claude/plans/active.md") {
			t.Errorf("details lost the breach line: %v", out.Checks[0].Details)
		}
	})

	// An unknown selector is refused over the wire rather than silently
	// reporting a clean bill of health.
	t.Run("unknown_selector_is_refused", func(t *testing.T) {
		h := newHarness(t, false)
		h.registerAllTools(t)

		text, isErr := h.callToolRaw(t, "vp_check", map[string]any{"checks": []string{"no-such-check"}})
		if !isErr {
			t.Fatalf("unknown selector must be refused, got: %s", text)
		}
	})
}
