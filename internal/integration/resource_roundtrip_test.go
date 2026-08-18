// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// readResourceProtocol drives a resources/read request through the real MCP
// server (HandleMessage) and returns the single text content. It exercises the
// resource provider stack wired by tools.RegisterResources.
func (h *testHarness) readResourceProtocol(t *testing.T, uri string) string {
	t.Helper()
	if !h.mcpReady {
		h.initMCP(t)
	}
	msg := json.RawMessage(fmt.Sprintf(`{
		"jsonrpc": "2.0", "id": 42, "method": "resources/read",
		"params": {"uri": %q}
	}`, uri))

	resp := h.Server.HandleMessage(context.Background(), msg)
	rpcResp, ok := resp.(mcplib.JSONRPCResponse)
	if !ok {
		t.Fatalf("resources/read %q: expected JSONRPCResponse, got %T: %+v", uri, resp, resp)
	}
	raw, _ := json.Marshal(rpcResp.Result)
	var result struct {
		Contents []struct {
			URI      string `json:"uri"`
			MIMEType string `json:"mimeType"`
			Text     string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("resources/read %q: unmarshal: %v (raw: %s)", uri, err, raw)
	}
	if len(result.Contents) != 1 {
		t.Fatalf("resources/read %q: %d contents, want 1", uri, len(result.Contents))
	}
	return result.Contents[0].Text
}

// TestIntegrationResourceByteIdentity proves the three read paths for a task body
// agree byte-for-byte through the full real-provider stack:
//
//	(a) vp_get_task with include_content=true → Content
//	(b) resources/read for the task URI → TextResourceContents.Text
//	(c) vp_read_resource paged with a tiny limit, walking offset by the returned
//	    offset+length until eof, concatenated.
//
// The body is saturated with em-dashes (— = 3 bytes, U+2014) so the paging edges
// repeatedly land mid-rune: every page must be valid UTF-8 and the concatenation
// must still be byte-identical to (a) and (b).
func TestIntegrationResourceByteIdentity(t *testing.T) {
	h := newHarness(t, false)
	h.seedProject(t, "resource-journey")
	h.registerAllTools(t)
	// Resources are registered on the Server (not the Registry), so RegisterAll
	// does not wire them — do it explicitly for the full provider stack.
	tools.RegisterResources(h.Server, h.Resolver, h.Vault)

	const (
		project  = "resource-journey"
		taskSlug = "emdash-body"
	)
	// Multibyte content with em-dashes peppered throughout.
	content := "Intro — the plan.\n" + strings.Repeat("step — detail — note —\n", 60)
	raw := h.callTool(t, "vp_manage_task", map[string]any{
		"project":  project,
		"action":   "create",
		"task":     taskSlug,
		"title":    "Em-dash Body",
		"content":  content,
		"priority": "high",
	})
	if !strings.Contains(raw, "created") {
		t.Fatalf("create task: %s", raw)
	}

	uri := mcp.TaskURI(project, taskSlug)

	// (a) vp_get_task include_content=true → full Content.
	getRaw := h.callTool(t, "vp_get_task", map[string]any{
		"project":         project,
		"task":            taskSlug,
		"include_content": true,
	})
	var got struct {
		Content     string `json:"content"`
		ContentURI  string `json:"content_uri"`
		ContentSize int    `json:"content_size"`
	}
	if err := json.Unmarshal([]byte(getRaw), &got); err != nil {
		t.Fatalf("vp_get_task unmarshal: %v (%s)", err, getRaw)
	}
	bodyA := got.Content
	if bodyA == "" {
		t.Fatal("vp_get_task returned empty content")
	}
	if got.ContentURI != uri {
		t.Errorf("vp_get_task content_uri = %q, want %q", got.ContentURI, uri)
	}
	if got.ContentSize != len(bodyA) {
		t.Errorf("vp_get_task content_size = %d, want %d", got.ContentSize, len(bodyA))
	}

	// (b) resources/read → text.
	bodyB := h.readResourceProtocol(t, uri)
	if bodyB != bodyA {
		t.Fatalf("resources/read body NOT byte-identical to vp_get_task (len %d vs %d)", len(bodyB), len(bodyA))
	}

	// (c) vp_read_resource paged with a small limit, walking offset+length.
	const limit = 8
	var reassembled strings.Builder
	offset := 0
	pages := 0
	for {
		pages++
		if pages > len(bodyA)+10 {
			t.Fatalf("vp_read_resource did not terminate (no forward progress)")
		}
		pageRaw := h.callTool(t, "vp_read_resource", map[string]any{
			"uri":    uri,
			"offset": offset,
			"limit":  limit,
		})
		var page struct {
			Offset  int    `json:"offset"`
			Length  int    `json:"length"`
			EOF     bool   `json:"eof"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(pageRaw), &page); err != nil {
			t.Fatalf("vp_read_resource unmarshal: %v (%s)", err, pageRaw)
		}
		if !utf8.ValidString(page.Content) {
			t.Fatalf("vp_read_resource page at offset %d is not valid UTF-8", offset)
		}
		if page.Offset != offset {
			t.Fatalf("vp_read_resource returned offset %d != requested %d", page.Offset, offset)
		}
		reassembled.WriteString(page.Content)
		if page.EOF {
			break
		}
		next := page.Offset + page.Length
		if next <= offset {
			t.Fatalf("vp_read_resource made no forward progress at offset %d (length %d)", offset, page.Length)
		}
		offset = next
	}
	if reassembled.String() != bodyA {
		t.Fatalf("vp_read_resource paged reassembly NOT byte-identical to vp_get_task (len %d vs %d)",
			reassembled.Len(), len(bodyA))
	}
}
