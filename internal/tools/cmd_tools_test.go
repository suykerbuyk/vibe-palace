// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
)

// --- vp_cmd execute mode ---

func TestCmdExecuteEmbedded(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := CmdTool(resolver)

	result, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"restart"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	s, ok := result.(string)
	if !ok {
		t.Fatalf("result type = %T, want string", result)
	}
	if !strings.Contains(s, "=== EXECUTE COMMAND: restart ===") {
		t.Error("missing execution frame header")
	}
	if !strings.Contains(s, "Source: embedded") {
		t.Error("missing source in frame")
	}
	if !strings.Contains(s, "End of command: restart") {
		t.Error("missing frame footer")
	}
	if !strings.Contains(s, "Context Restoration") {
		t.Error("missing resolved command content")
	}
}

func TestCmdExecuteVaultOverride(t *testing.T) {
	resolver, root := testResolverOnly(t)
	writeVaultFile(t, root, "Templates/commands/restart.md", "custom vault restart")

	tool := CmdTool(resolver)
	result, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"restart","project":"proj"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	s := result.(string)
	if !strings.Contains(s, "Source: vault") {
		t.Error("expected vault source")
	}
	if !strings.Contains(s, "custom vault restart") {
		t.Error("expected vault override content")
	}
}

func TestCmdExecuteProjectOverride(t *testing.T) {
	resolver, root := testResolverOnly(t)
	writeVaultFile(t, root, "Templates/commands/restart.md", "vault version")
	writeVaultFile(t, root, "Projects/proj/commands/restart.md", "project version")

	tool := CmdTool(resolver)
	result, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"restart","project":"proj"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	s := result.(string)
	if !strings.Contains(s, "Source: project") {
		t.Error("expected project source")
	}
	if !strings.Contains(s, "project version") {
		t.Error("expected project override content")
	}
}

func TestCmdExecuteNotFound(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := CmdTool(resolver)

	_, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"nonexistent"}`))
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err.Error())
	}
}

func TestCmdExecuteProjectNone(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := CmdTool(resolver)

	result, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"restart"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	s := result.(string)
	if !strings.Contains(s, "Project: (none)") {
		t.Error("expected 'Project: (none)' when no project given")
	}
}

// --- vp_cmd discovery mode ---

func TestCmdDiscoveryEmptyName(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := CmdTool(resolver)

	result, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	s, ok := result.(string)
	if !ok {
		t.Fatalf("result type = %T, want string", result)
	}
	if !strings.Contains(s, "Available commands:") {
		t.Error("missing discovery header")
	}
	if !strings.Contains(s, "restart") {
		t.Error("missing embedded command in listing")
	}
	if !strings.Contains(s, "[embedded]") {
		t.Error("missing source tag in listing")
	}
	if !strings.Contains(s, "Call vp_cmd") {
		t.Error("missing call-to-action footer")
	}
}

func TestCmdDiscoveryWhitespaceName(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := CmdTool(resolver)

	result, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"  "}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	s := result.(string)
	if !strings.Contains(s, "Available commands:") {
		t.Error("whitespace name should trigger discovery mode")
	}
}

func TestCmdDiscoveryWithProject(t *testing.T) {
	resolver, root := testResolverOnly(t)
	writeVaultFile(t, root, "Projects/proj/commands/deploy.md", "# Deploy\n\nRun the deploy pipeline.")

	tool := CmdTool(resolver)
	result, err := tool.Handler(context.Background(), json.RawMessage(`{"project":"proj"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	s := result.(string)
	if !strings.Contains(s, `Available commands for project "proj":`) {
		t.Error("missing project-specific header")
	}
	if !strings.Contains(s, "deploy") {
		t.Error("missing project command")
	}
	if !strings.Contains(s, "[project") {
		t.Error("missing project source tag")
	}
}

// --- vp_skill execute mode ---

func TestSkillExecute(t *testing.T) {
	resolver, root := testResolverOnly(t)
	writeVaultFile(t, root, "Templates/skills/analyze.md", "# Analyze\n\nPerform deep analysis.")

	tool := SkillCmdTool(resolver)
	result, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"analyze"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	s, ok := result.(string)
	if !ok {
		t.Fatalf("result type = %T, want string", result)
	}
	if !strings.Contains(s, "=== ACTIVATE SKILL: analyze ===") {
		t.Error("missing skill frame header")
	}
	if !strings.Contains(s, "Internalize these guidelines") {
		t.Error("missing skill framing instructions")
	}
	if !strings.Contains(s, "End of skill: analyze") {
		t.Error("missing skill frame footer")
	}
	if !strings.Contains(s, "Perform deep analysis.") {
		t.Error("missing resolved skill content")
	}
}

func TestSkillExecuteNotFound(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := SkillCmdTool(resolver)

	_, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"nonexistent"}`))
	if err == nil {
		t.Fatal("expected error for nonexistent skill")
	}
}

// --- vp_skill discovery mode ---

func TestSkillDiscoveryEmpty(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := SkillCmdTool(resolver)

	result, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	s := result.(string)
	if s != "No skills available." {
		t.Errorf("got %q, want 'No skills available.'", s)
	}
}

func TestSkillDiscoveryWithSkills(t *testing.T) {
	resolver, root := testResolverOnly(t)
	writeVaultFile(t, root, "Templates/skills/analyze.md", "# Analyze\n\nDeep analysis skill.")
	writeVaultFile(t, root, "Templates/skills/summarize.md", "Summarize content concisely.")

	tool := SkillCmdTool(resolver)
	result, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	s := result.(string)
	if !strings.Contains(s, "Available skills:") {
		t.Error("missing discovery header")
	}
	if !strings.Contains(s, "analyze") {
		t.Error("missing analyze skill")
	}
	if !strings.Contains(s, "summarize") {
		t.Error("missing summarize skill")
	}
	if !strings.Contains(s, "Call vp_skill") {
		t.Error("missing call-to-action footer")
	}
}

// --- extractBrief ---

func TestExtractBrief(t *testing.T) {
	tests := []struct {
		name    string
		content string
		maxLen  int
		want    string
	}{
		{
			name:    "normal line",
			content: "# Title\n\nThis is the description.",
			maxLen:  60,
			want:    "This is the description.",
		},
		{
			name:    "empty content",
			content: "",
			maxLen:  60,
			want:    "(no description)",
		},
		{
			name:    "long line truncated",
			content: "This is a very long line that exceeds the maximum length allowed",
			maxLen:  20,
			want:    "This is a very long ",
		},
		{
			name:    "heading only",
			content: "# Title\n## Subtitle",
			maxLen:  60,
			want:    "(no description)",
		},
		{
			name:    "blanks and headings then content",
			content: "\n\n# Title\n\n\nActual content here.",
			maxLen:  60,
			want:    "Actual content here.",
		},
		{
			name:    "content exactly at maxLen",
			content: "12345",
			maxLen:  5,
			want:    "12345",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractBrief(tt.content, tt.maxLen)
			if got != tt.want {
				t.Errorf("extractBrief() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- Execution frame structure ---

func TestExecutionFrameMarkdownHeaders(t *testing.T) {
	content := "# Step 1\n\nDo something.\n\n## Step 2\n\nDo more."
	frame := buildExecutionFrame(frameParams{
		Name:         "test-cmd",
		Project:      "proj",
		Source:       "vault",
		Content:      content,
		ResourceType: "command",
	})

	if !strings.Contains(frame, "=== EXECUTE COMMAND: test-cmd ===") {
		t.Error("frame header missing")
	}
	if !strings.Contains(frame, "---\n\n# Step 1") {
		t.Error("content markdown headers should be intact inside frame")
	}
	if !strings.Contains(frame, "End of command: test-cmd") {
		t.Error("frame footer missing")
	}
}

func TestExecutionFrameWithWingRoom(t *testing.T) {
	frame := buildExecutionFrame(frameParams{
		Name:         "lint",
		Project:      "proj",
		Wing:         "backend",
		Room:         "api",
		Source:       "room",
		Content:      "Run linter.",
		ResourceType: "command",
	})

	if !strings.Contains(frame, "Wing: backend") {
		t.Error("wing missing from frame header")
	}
	if !strings.Contains(frame, "Room: api") {
		t.Error("room missing from frame header")
	}
	if !strings.Contains(frame, "Source: room") {
		t.Error("room source missing from frame header")
	}
}

func TestExecutionFrameOmitsEmptyWingRoom(t *testing.T) {
	frame := buildExecutionFrame(frameParams{
		Name:         "restart",
		Project:      "proj",
		Source:       "embedded",
		Content:      "Restart.",
		ResourceType: "command",
	})

	if strings.Contains(frame, "Wing:") {
		t.Error("empty wing should not appear in frame")
	}
	if strings.Contains(frame, "Room:") {
		t.Error("empty room should not appear in frame")
	}
}

// --- Tool schema validation ---

func TestCmdToolSchemas(t *testing.T) {
	resolver, _ := testResolverOnly(t)

	tools := []struct {
		name string
		tool mcp.Tool
	}{
		{"vp_cmd", CmdTool(resolver)},
		{"vp_skill", SkillCmdTool(resolver)},
	}

	for _, tt := range tools {
		t.Run(tt.name, func(t *testing.T) {
			if tt.tool.Name != tt.name {
				t.Errorf("Name = %q, want %q", tt.tool.Name, tt.name)
			}
			if tt.tool.Description == "" {
				t.Error("Description should not be empty")
			}
			var schema map[string]any
			if err := json.Unmarshal(tt.tool.Schema, &schema); err != nil {
				t.Fatalf("invalid schema JSON: %v", err)
			}
			if schema["type"] != "object" {
				t.Errorf("schema type = %v, want object", schema["type"])
			}
			// No required fields.
			if _, hasRequired := schema["required"]; hasRequired {
				t.Error("schema should have no required fields")
			}
		})
	}
}
