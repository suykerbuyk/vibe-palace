// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcphost

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestCodexSpanEditorPreservesLiveLinesAndCommentOrder(t *testing.T) {
	original := codexLiveFixture()
	before := strings.Split(strings.TrimSuffix(original, "\n"), "\n")
	enabled := true
	patched, err := patchCodexServerTable([]byte(original), codexServerConfig{Command: "vp", Args: []string{"mcp"}, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	after := strings.Split(strings.TrimSuffix(string(patched), "\n"), "\n")
	assertOriginalLinesInOrder(t, before, after, map[string]bool{
		"command = \"/old/path/vp\" # command comment": true,
		"enabled = false": true,
	})
	comment := "# operator note: leave this exactly here"
	if indexLine(after, comment) != indexLine(before, comment) {
		t.Fatalf("operator comment moved: before=%d after=%d", indexLine(before, comment), indexLine(after, comment))
	}
}

func TestCodexSpanEditorKeepsAllApprovalTables(t *testing.T) {
	original := codexLiveFixture()
	var before map[string]any
	if _, err := toml.Decode(original, &before); err != nil {
		t.Fatal(err)
	}
	serverBefore := before["mcp_servers"].(map[string]any)[codexServerName].(map[string]any)
	approvalBefore := approvalValues(serverBefore)
	enabled := true
	patched, err := patchCodexServerTable([]byte(original), codexServerConfig{Command: "vp", Args: []string{"mcp"}, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	if _, err := toml.Decode(string(patched), &after); err != nil {
		t.Fatal(err)
	}
	serverAfter := after["mcp_servers"].(map[string]any)[codexServerName].(map[string]any)
	approvalAfter := approvalValues(serverAfter)
	if len(approvalBefore) != 14 {
		t.Fatalf("fixture has %d approval values, want 14", len(approvalBefore))
	}
	if !equalStringMap(approvalBefore, approvalAfter) {
		t.Fatalf("approval values changed:\nbefore=%v\nafter=%v", approvalBefore, approvalAfter)
	}
}

func TestCodexSpanEditorPlacesEnabledInBaseServerTable(t *testing.T) {
	input := "[mcp_servers.vibe-palace.tools.vp_bootstrap_context]\n\napproval = \"approve\"\n"
	enabled := true
	patched, err := patchCodexServerTable([]byte(input), codexServerConfig{Command: "vp", Args: []string{"mcp"}, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if _, err := toml.Decode(string(patched), &doc); err != nil {
		t.Fatal(err)
	}
	server := doc["mcp_servers"].(map[string]any)[codexServerName].(map[string]any)
	if server["enabled"] != true {
		t.Fatalf("base server enabled = %v", server["enabled"])
	}
	tool := server["tools"].(map[string]any)["vp_bootstrap_context"].(map[string]any)
	if _, exists := tool["enabled"]; exists {
		t.Fatalf("enabled was incorrectly inserted in tools table: %s", patched)
	}
}

func TestCodexSpanEditorRepeatInstallIsByteStable(t *testing.T) {
	enabled := true
	first, err := patchCodexServerTable([]byte(codexLiveFixture()), codexServerConfig{Command: "vp", Args: []string{"mcp"}, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	second, err := patchCodexServerTable(first, codexServerConfig{Command: "vp", Args: []string{"mcp"}, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("repeat install changed bytes\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestCodexSpanEditorRejectsMalformedTOMLWithoutMutation(t *testing.T) {
	input := []byte("[mcp_servers.vibe-palace]\ncommand = [\n")
	enabled := true
	if _, err := patchCodexServerTable(input, codexServerConfig{Command: "vp", Args: []string{"mcp"}, Enabled: &enabled}); err == nil {
		t.Fatal("malformed TOML was accepted")
	}
	if string(input) != "[mcp_servers.vibe-palace]\ncommand = [\n" {
		t.Fatal("input was mutated")
	}
}

func codexLiveFixture() string {
	var b strings.Builder
	b.WriteString("# global operator comment\n")
	b.WriteString("[mcp_servers.vibe-palace]\n")
	b.WriteString("# operator note: leave this exactly here\n")
	b.WriteString("command = \"/old/path/vp\" # command comment\n")
	b.WriteString("args = [\"mcp\"]\n")
	b.WriteString("enabled = false\n")
	b.WriteString("default_tools_approval_mode = \"approve\"\n")
	b.WriteString("custom_key = \"keep me\"\n")
	for i := 0; i < 13; i++ {
		b.WriteString("\n[mcp_servers.vibe-palace.tools.tool")
		b.WriteString(strconvItoa(i))
		b.WriteString("]\napproval = \"approve\"\n")
	}
	return b.String()
}

func assertOriginalLinesInOrder(t *testing.T, before, after []string, replaced map[string]bool) {
	t.Helper()
	position := 0
	for _, line := range before {
		if replaced[line] {
			continue
		}
		for position < len(after) && after[position] != line {
			position++
		}
		if position == len(after) {
			t.Fatalf("original non-target line missing or reordered: %q\nafter=%q", line, after)
		}
		position++
	}
}

func indexLine(lines []string, want string) int {
	for i, line := range lines {
		if line == want {
			return i
		}
	}
	return -1
}

func approvalValues(server map[string]any) map[string]string {
	values := map[string]string{}
	if mode, ok := server["default_tools_approval_mode"]; ok {
		values["default_tools_approval_mode"] = mode.(string)
	}
	tools, _ := server["tools"].(map[string]any)
	for name, raw := range tools {
		tool, _ := raw.(map[string]any)
		if approval, ok := tool["approval"].(string); ok {
			values[name] = approval
		}
	}
	return values
}

func equalStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func strconvItoa(i int) string {
	return fmt.Sprintf("%d", i)
}
