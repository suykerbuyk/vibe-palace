// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestJourney_NewProject_Bootstrap_Capture_Search exercises the canonical
// "new project" agent flow at the MCP protocol boundary:
//
//  1. vp_init creates the project config + vault dirs.
//  2. vp_bootstrap_context returns baseline workflow/resume for the project.
//  3. vp_capture_session writes a session with a transcript (which is
//     chunked + indexed into the search engine).
//  4. vp_search finds content from the transcript.
//
// Cross-tool consistency assertions:
//   - The project slug flowed through every tool (init returns it; bootstrap,
//     capture, and search all accept it as input without error).
//   - vp_search returns at least one result whose text or metadata links
//     back to the session ID produced by vp_capture_session.
func TestJourney_NewProject_Bootstrap_Capture_Search(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	projectName := "journey-newproj"
	projectDir := filepath.Join(t.TempDir(), projectName)

	// 1. vp_init: initialize project.
	raw := h.callTool(t, "vp_init", map[string]any{
		"path": projectDir,
		"name": projectName,
	})
	if !strings.Contains(raw, "initialized") {
		t.Fatalf("vp_init: %s", raw)
	}
	if !strings.Contains(raw, projectName) {
		t.Fatalf("vp_init result missing project name %q: %s", projectName, raw)
	}

	// 2. vp_bootstrap_context: fetch baseline context for the new project.
	raw = h.callTool(t, "vp_bootstrap_context", map[string]any{
		"project": projectName,
	})
	var boot struct {
		Project     string `json:"project"`
		WorkflowURI string `json:"workflow_uri"`
	}
	if err := json.Unmarshal([]byte(raw), &boot); err != nil {
		t.Fatalf("bootstrap parse: %v (raw=%.200s)", err, raw)
	}
	if boot.Project != projectName {
		t.Errorf("bootstrap project = %q, want %q", boot.Project, projectName)
	}
	if boot.WorkflowURI == "" {
		t.Error("bootstrap workflow_uri empty for fresh project — the body is fetched through it")
	}

	// 3. vp_capture_session: write a session with transcript.
	const uniqueMarker = "SENTINEL-PHRASE-HAPTIC-MONGOOSE"
	transcript := "User asked about testing.\nAssistant responded with " + uniqueMarker + ".\n"
	raw = h.callTool(t, "vp_capture_session", map[string]any{
		"project":    projectName,
		"summary":    "Journey bootstrap session with " + uniqueMarker,
		"tag":        "implementation",
		"transcript": transcript,
	})
	var cap struct {
		Status    string `json:"status"`
		SessionID string `json:"session_id"`
		Project   string `json:"project"`
	}
	if err := json.Unmarshal([]byte(raw), &cap); err != nil {
		t.Fatalf("capture parse: %v (raw=%s)", err, raw)
	}
	if cap.Status != "ok" {
		t.Fatalf("capture status = %q, want ok (raw=%s)", cap.Status, raw)
	}
	if cap.SessionID == "" {
		t.Fatal("capture session_id empty")
	}
	if cap.Project != projectName {
		t.Errorf("capture project = %q, want %q", cap.Project, projectName)
	}

	// 4. vp_search: look for the unique marker, confirm at least one result
	//    references the session we just wrote.
	raw = h.callTool(t, "vp_search", map[string]any{
		"project": projectName,
		"query":   uniqueMarker,
		"limit":   10,
	})
	// Results are an array of objects; with the mock embedder we just need
	// any results at all, plus cross-tool consistency: the search must
	// have seen the transcript indexed by capture.
	var sr struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &sr); err != nil {
		t.Fatalf("search parse: %v (raw=%s)", err, raw)
	}
	results := sr.Items
	if len(results) == 0 {
		t.Fatalf("search returned 0 results after capture indexed transcript (raw=%s)", raw)
	}
	// Cross-tool consistency: at least one chunk's text contains the marker
	// we planted via capture.
	foundMarker := false
	for _, r := range results {
		for _, key := range []string{"content", "text"} {
			if txt, _ := r[key].(string); strings.Contains(txt, uniqueMarker) {
				foundMarker = true
				break
			}
		}
		if foundMarker {
			break
		}
	}
	if !foundMarker {
		// Mock embedder ranks by token overlap; a direct marker hit is
		// expected. If it ever flakes, drop to a weaker "any results"
		// check — but for now require the round-trip.
		t.Errorf("search results do not contain unique marker %q from captured transcript (raw=%s)", uniqueMarker, raw)
	}
}
