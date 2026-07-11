// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestJourney_CommandDiscovery_And_Execution walks the agent-side command
// discovery flow: bootstrap announces commands → list_commands enumerates
// them → get_command returns raw body → vp_cmd returns the execution
// frame with placeholders substituted.
//
// Cross-tool consistency assertions:
//   - Bootstrap's available_commands list and vp_list_commands agree on the
//     presence of the canonical "restart" command.
//   - vp_get_command returns the same content (minus substitution) that
//     vp_cmd wraps in its execution frame.
//   - vp_cmd's instruction body has the {{PROJECT}} placeholder replaced
//     with the project slug — no stale mustache markers.
func TestJourney_CommandDiscovery_And_Execution(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	const project = "journey-cmd"

	// 1. vp_bootstrap_context: does it advertise commands?
	raw := h.callTool(t, "vp_bootstrap_context", map[string]any{"project": project})
	var boot struct {
		AvailableCommands []struct {
			Name string `json:"name"`
		} `json:"available_commands"`
	}
	if err := json.Unmarshal([]byte(raw), &boot); err != nil {
		t.Fatalf("bootstrap parse: %v", err)
	}
	bootCmds := map[string]bool{}
	for _, c := range boot.AvailableCommands {
		bootCmds[c.Name] = true
	}
	if !bootCmds["restart"] {
		t.Fatalf("bootstrap available_commands missing 'restart'; got %v", bootCmds)
	}

	// 2. vp_list_commands: independent enumeration must agree with bootstrap.
	raw = h.callTool(t, "vp_list_commands", map[string]any{})
	var list struct {
		Resources []struct {
			Name string `json:"name"`
		} `json:"resources"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("list parse: %v", err)
	}
	listCmds := map[string]bool{}
	for _, r := range list.Resources {
		listCmds[r.Name] = true
	}
	for name := range bootCmds {
		if !listCmds[name] {
			t.Errorf("vp_list_commands missing %q that bootstrap advertised", name)
		}
	}
	if !listCmds["restart"] {
		t.Fatal("vp_list_commands missing 'restart'")
	}

	// 3. vp_get_command: fetch the raw restart body.
	raw = h.callTool(t, "vp_get_command", map[string]any{"name": "restart"})
	var get struct {
		Name    string `json:"name"`
		Content string `json:"content"`
		Source  string `json:"source"`
	}
	if err := json.Unmarshal([]byte(raw), &get); err != nil {
		t.Fatalf("get parse: %v", err)
	}
	if get.Name != "restart" {
		t.Errorf("get name = %q, want restart", get.Name)
	}
	if get.Content == "" {
		t.Fatal("get content empty")
	}
	// Raw body still has the placeholder (no project context in get_command
	// without project arg — substitution is project-scoped).
	// The embedded restart template is known to contain {{PROJECT}}.
	if !strings.Contains(get.Content, "{{PROJECT}}") {
		t.Logf("note: embedded restart.md no longer contains {{PROJECT}}; substitution assertion below is still valid")
	}

	// 4. vp_cmd: execution frame with placeholder substituted.
	body := h.callTool(t, "vp_cmd", map[string]any{
		"name":    "restart",
		"project": project,
	})
	if !strings.Contains(body, "=== EXECUTE COMMAND: restart ===") {
		t.Errorf("vp_cmd frame header missing: %.200s", body)
	}
	if !strings.Contains(body, project) {
		t.Errorf("vp_cmd body missing project slug %q", project)
	}
	// Critical cross-tool assertion: no stale mustache placeholders leaked
	// through. vp_cmd is what the agent sees; placeholders must be
	// resolved before the frame is emitted.
	if strings.Contains(body, "{{PROJECT}}") {
		t.Errorf("vp_cmd body still contains unresolved {{PROJECT}} placeholder")
	}
	if strings.Contains(body, "{{PROJECT_NAME}}") {
		t.Errorf("vp_cmd body contains unresolved {{PROJECT_NAME}} placeholder")
	}
	// And vp_cmd frame content must be consistent with vp_get_command
	// output after placeholder substitution: take the get.Content,
	// substitute {{PROJECT}}, and verify it appears inside the vp_cmd
	// frame.
	expected := strings.ReplaceAll(get.Content, "{{PROJECT}}", project)
	// Keep the check substring-sized to avoid whitespace/date variance.
	firstLine, _, _ := strings.Cut(expected, "\n")
	if firstLine != "" && !strings.Contains(body, firstLine) {
		t.Errorf("vp_cmd body missing first line from get_command content: %q", firstLine)
	}
}
