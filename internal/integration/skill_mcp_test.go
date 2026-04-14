// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// TestIntegrationSkillMCP exercises Phase 2 end-to-end over the MCP
// JSON-RPC surface: initialize → tools/list → tools/call vp_skill →
// tools/call vp_get_skill_section. The harness drives the same
// HandleMessage entrypoint mcp.Server.ServeStdio uses, so protocol
// framing is covered even though we bypass the stdio pipe for speed.
func TestIntegrationSkillMCP(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)
	h.initMCP(t)

	// --- tools/list: assert both skill tools are present. ---
	listResp := h.Server.HandleMessage(context.Background(), json.RawMessage(`{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list"
	}`))
	rpcList, ok := listResp.(mcplib.JSONRPCResponse)
	if !ok {
		t.Fatalf("tools/list: want JSONRPCResponse, got %T: %+v", listResp, listResp)
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
	if !names["vp_skill"] {
		t.Error("tools/list missing vp_skill")
	}
	if !names["vp_get_skill_section"] {
		t.Error("tools/list missing vp_get_skill_section")
	}

	// --- tools/call vp_skill: frame contains body + references list. ---
	body := h.callTool(t, "vp_skill", map[string]any{"name": "startup-analyst"})
	if !strings.Contains(body, "=== ACTIVATE SKILL: startup-analyst ===") {
		t.Error("vp_skill frame header missing")
	}
	if !strings.Contains(body, "References (fetch on demand via vp_get_skill_section):") {
		t.Error("vp_skill frame missing references block")
	}
	for _, ref := range []string{
		"capex-opex", "competitive-landscape", "funding-sources",
		"reality-validation", "strategic-partnerships",
	} {
		if !strings.Contains(body, "  - "+ref+"\n") {
			t.Errorf("vp_skill frame missing reference bullet %q", ref)
		}
	}
	if strings.Contains(body, "---\nname: startup-analyst") {
		t.Error("SKILL.md frontmatter should be stripped from the inlined body")
	}

	// --- tools/call vp_get_skill_section: reference body returned. ---
	// The handler returns structured JSON (content+source), which the
	// MCP layer serialises into a JSON text content block. Parse and
	// assert the unwrapped content is non-empty.
	sectionText := h.callTool(t, "vp_get_skill_section", map[string]any{
		"name": "startup-analyst", "section": "capex-opex",
	})
	var sec struct {
		Content string `json:"content"`
		Source  string `json:"source"`
	}
	if err := json.Unmarshal([]byte(sectionText), &sec); err != nil {
		t.Fatalf("unmarshal section result: %v (raw: %s)", err, sectionText)
	}
	if sec.Source != "embedded" {
		t.Errorf("source = %q, want embedded", sec.Source)
	}
	if len(sec.Content) == 0 {
		t.Error("empty reference content")
	}
}
