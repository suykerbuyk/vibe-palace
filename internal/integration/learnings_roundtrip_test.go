// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// TestIntegrationLearningsRoundtrip drives the two read-only learning MCP tools
// (vp_list_learnings, vp_get_learning) and the vault-wide learning resource
// (vibe-palace://learning/{slug}) through the real MCP stack — registry → tool
// dispatch → resource resolution — proving the full-stack wiring agrees with the
// on-disk vault. It mirrors TestIntegrationResourceByteIdentity's content-URI /
// byte-identity pattern, adapted to the vault-wide learnings surface.
func TestIntegrationLearningsRoundtrip(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)
	// Resources are registered on the Server (not the Registry), so RegisterAll
	// does not wire them — do it explicitly for the full provider stack.
	tools.RegisterResources(h.Server, h.Resolver, h.Vault)

	// 1. Seed learnings vault-WIDE under {vault}/Knowledge/learnings using the
	// real vault root from the harness. Two distinct types make filtering
	// testable; the em-dash body keeps byte lengths multibyte-honest.
	dir := h.Vault.LearningsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir learnings dir: %v", err)
	}

	type seed struct {
		slug, name, desc, typ, body string
	}
	seeds := []seed{
		{"alpha-feedback", "Alpha Lesson", "First feedback learning", "feedback", "Alpha body — keep it tight.\n"},
		{"beta-reference", "Beta Reference", "A reference document", "reference", "Beta reference body — cite it.\nSecond line.\n"},
		{"gamma-feedback", "Gamma Lesson", "Second feedback learning", "feedback", "Gamma body line one — note.\nLine two.\n"},
	}
	for _, s := range seeds {
		file := fmt.Sprintf("---\nname: %s\ndescription: %s\ntype: %s\n---\n%s", s.name, s.desc, s.typ, s.body)
		p := filepath.Join(dir, s.slug+".md")
		if err := os.WriteFile(p, []byte(file), 0o644); err != nil {
			t.Fatalf("write learning %s: %v", s.slug, err)
		}
	}

	// 2. vp_list_learnings with no filter → all seeded learnings, sorted by slug.
	listRaw := h.callTool(t, "vp_list_learnings", map[string]any{})
	var listAll struct {
		Learnings []storage.LearningMetadata `json:"learnings"`
	}
	if err := json.Unmarshal([]byte(listRaw), &listAll); err != nil {
		t.Fatalf("vp_list_learnings unmarshal: %v (%s)", err, listRaw)
	}
	wantSlugs := []string{"alpha-feedback", "beta-reference", "gamma-feedback"}
	if len(listAll.Learnings) != len(wantSlugs) {
		t.Fatalf("vp_list_learnings: got %d learnings, want %d (%s)", len(listAll.Learnings), len(wantSlugs), listRaw)
	}
	for i, want := range wantSlugs {
		got := listAll.Learnings[i]
		if got.Slug != want {
			t.Fatalf("vp_list_learnings[%d].slug = %q, want %q (not sorted?)", i, got.Slug, want)
		}
		// Cross-check the full metadata against the seed of the same slug.
		var src seed
		for _, s := range seeds {
			if s.slug == want {
				src = s
			}
		}
		if got.Name != src.name || got.Description != src.desc || got.Type != src.typ {
			t.Errorf("vp_list_learnings[%d] metadata = %+v, want name=%q desc=%q type=%q",
				i, got, src.name, src.desc, src.typ)
		}
	}

	// 3. vp_list_learnings with a filter_type → only matching learnings.
	feedbackRaw := h.callTool(t, "vp_list_learnings", map[string]any{"filter_type": "feedback"})
	var listFeedback struct {
		Learnings []storage.LearningMetadata `json:"learnings"`
	}
	if err := json.Unmarshal([]byte(feedbackRaw), &listFeedback); err != nil {
		t.Fatalf("vp_list_learnings(feedback) unmarshal: %v (%s)", err, feedbackRaw)
	}
	wantFeedback := []string{"alpha-feedback", "gamma-feedback"}
	if len(listFeedback.Learnings) != len(wantFeedback) {
		t.Fatalf("vp_list_learnings(feedback): got %d, want %d (%s)", len(listFeedback.Learnings), len(wantFeedback), feedbackRaw)
	}
	for i, want := range wantFeedback {
		if got := listFeedback.Learnings[i]; got.Slug != want || got.Type != "feedback" {
			t.Errorf("vp_list_learnings(feedback)[%d] = {slug=%q type=%q}, want slug=%q type=feedback", i, got.Slug, got.Type, want)
		}
	}

	// 4. vp_get_learning for a known slug → content_uri, content_size, inline body.
	const targetSlug = "beta-reference"
	wantBody := strings.TrimPrefix("Beta reference body — cite it.\nSecond line.\n", "\n")
	getRaw := h.callTool(t, "vp_get_learning", map[string]any{"slug": targetSlug})
	var got struct {
		Slug        string `json:"slug"`
		Name        string `json:"name"`
		Type        string `json:"type"`
		Content     string `json:"content"`
		ContentURI  string `json:"content_uri"`
		ContentSize int    `json:"content_size"`
	}
	if err := json.Unmarshal([]byte(getRaw), &got); err != nil {
		t.Fatalf("vp_get_learning unmarshal: %v (%s)", err, getRaw)
	}
	wantURI := mcp.LearningURI(targetSlug)
	if got.ContentURI != wantURI {
		t.Errorf("vp_get_learning content_uri = %q, want %q", got.ContentURI, wantURI)
	}
	if got.ContentSize != len(wantBody) {
		t.Errorf("vp_get_learning content_size = %d, want %d", got.ContentSize, len(wantBody))
	}
	if got.Content != wantBody {
		t.Errorf("vp_get_learning content = %q, want parsed body %q", got.Content, wantBody)
	}

	// 5. Content-URI contract: resources/read on the content_uri returns bytes
	// byte-identical to the body vp_get_learning reported as content (and whose
	// length is content_size). This is the whole point of the idiom — a host that
	// dropped a large inline body fetches content_uri to get exactly that body
	// back. The resource therefore returns the parsed body, NOT the raw file with
	// frontmatter (the frontmatter is already exposed as structured fields).
	resBytes := h.readResourceProtocol(t, got.ContentURI)
	if resBytes != got.Content {
		t.Fatalf("resources/read %q NOT byte-identical to vp_get_learning content (len %d vs %d)",
			got.ContentURI, len(resBytes), len(got.Content))
	}
	if len(resBytes) != got.ContentSize {
		t.Errorf("resources/read length %d != content_size %d", len(resBytes), got.ContentSize)
	}
	// Sanity: the parsed body must NOT carry the raw frontmatter fence.
	if strings.HasPrefix(resBytes, "---\n") {
		t.Errorf("resource body should be the parsed body, not the raw file with frontmatter: %q", resBytes)
	}

	// 6. vp_get_learning for an unknown slug → must error (callTool fails on
	// IsError, so drive the protocol directly and assert IsError=true).
	if !h.toolCallIsError(t, "vp_get_learning", map[string]any{"slug": "no-such-learning"}) {
		t.Fatal("vp_get_learning with unknown slug should have returned an error")
	}
}

// toolCallIsError drives a tools/call through the real server and reports whether
// the result signalled IsError=true, without failing the test (callTool fatals on
// errors, so this is the negative-path counterpart).
func (h *testHarness) toolCallIsError(t *testing.T, name string, args any) bool {
	t.Helper()
	if !h.mcpReady {
		h.initMCP(t)
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	msg := json.RawMessage(fmt.Sprintf(`{
		"jsonrpc": "2.0", "id": 77, "method": "tools/call",
		"params": {"name": %q, "arguments": %s}
	}`, name, argsJSON))
	resp := h.Server.HandleMessage(context.Background(), msg)
	rpcResp, ok := resp.(mcplib.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}
	raw, _ := json.Marshal(rpcResp.Result)
	var result struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v (raw: %s)", err, raw)
	}
	return result.IsError
}
