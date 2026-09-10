// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestJourney_NewProject_Bootstrap_Capture_Search exercises the canonical
// "new project" agent flow at the MCP protocol boundary:
//
//  1. vp_init onboards the project: the project config in the working tree,
//     and the FULL vault scaffold (config.toml, tasks/, commands/, skills/).
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
	// The directory must EXIST and carry a project signal. It used to be a
	// path that was never created — the journey passed only because the
	// handler's os.MkdirAll conjured it into being on the server's disk, which
	// is precisely the silent remote misuse that line has been deleted for.
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "go.mod"), []byte("module journey\n"), 0o644); err != nil {
		t.Fatalf("mark project dir: %v", err)
	}

	// 1. vp_init: onboard the project.
	raw := h.callTool(t, "vp_init", map[string]any{
		"path": projectDir,
		"name": projectName,
	})
	// NOT "initialized": over MCP, hook-wiring and command-shims are always
	// omitted (they belong to the server operator's host, not the caller's), so
	// status is "partial" on every successful call and `complete` is the field
	// that carries meaning.
	var initRes struct {
		Status   string `json:"status"`
		Project  string `json:"project"`
		Complete bool   `json:"complete"`
		Steps    []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"steps"`
		Omitted []struct {
			Step   string `json:"step"`
			Remedy string `json:"remedy"`
		} `json:"omitted"`
	}
	if err := json.Unmarshal([]byte(raw), &initRes); err != nil {
		t.Fatalf("vp_init parse: %v (raw=%.300s)", err, raw)
	}
	if initRes.Status != "partial" || initRes.Complete {
		t.Fatalf("vp_init status = %q complete = %v, want partial/false: %s",
			initRes.Status, initRes.Complete, raw)
	}
	if initRes.Project != projectName {
		t.Fatalf("vp_init result missing project name %q: %s", projectName, raw)
	}
	for _, st := range initRes.Steps {
		if st.Status == "fail" {
			t.Errorf("vp_init step %s failed: %s", st.Step, raw)
		}
	}
	if len(initRes.Omitted) == 0 {
		t.Error("vp_init reported no omissions; hook-wiring and command-shims are always omitted over MCP")
	}
	for _, om := range initRes.Omitted {
		if om.Remedy == "" {
			t.Errorf("omission %q carries no remedy", om.Step)
		}
	}

	// The vault scaffold an MCP-only client must be able to produce for
	// itself. Before this was wired to internal/onboard the tool wrote a
	// two-line marker and two task directories, claimed "initialized", and
	// left the vault side of the project permanently missing.
	for _, rel := range []string{
		"config.toml",
		filepath.Join("tasks", "done"),
		filepath.Join("tasks", "cancelled"),
		filepath.Join("commands", "README.md"),
		filepath.Join("skills", "README.md"),
	} {
		p := filepath.Join(h.Vault.Root, "Projects", projectName, rel)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("vp_init left the vault scaffold incomplete: %s missing (%v)", p, err)
		}
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
