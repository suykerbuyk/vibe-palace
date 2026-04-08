// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import "testing"

func TestDetectFormatPlainText(t *testing.T) {
	tests := []string{
		"Just some plain text here.",
		"Multiple lines\nof plain\ntext content.",
		"",
	}
	for _, text := range tests {
		if got := DetectFormat(text); got != FormatPlainText {
			t.Errorf("DetectFormat(%q) = %d, want FormatPlainText", text, got)
		}
	}
}

func TestDetectFormatJSONRPC(t *testing.T) {
	tests := []string{
		`[{"role":"user","content":"hello"}]`,
		`{"role":"assistant","content":"hi there"}`,
	}
	for _, text := range tests {
		if got := DetectFormat(text); got != FormatJSONRPC {
			t.Errorf("DetectFormat(%q) = %d, want FormatJSONRPC", text[:40], got)
		}
	}
}

func TestDetectFormatMarkdown(t *testing.T) {
	tests := []string{
		"## Human\n\nHello there\n\n## Assistant\n\nHi!",
		"### User\n\nQuestion?\n\n### Assistant\n\nAnswer.",
		"**Human:**\nSomething\n\n**Assistant:**\nReply",
	}
	for _, text := range tests {
		if got := DetectFormat(text); got != FormatMarkdown {
			t.Errorf("DetectFormat(%q...) = %d, want FormatMarkdown", text[:20], got)
		}
	}
}

func TestDetectFormatJSONWithoutRole(t *testing.T) {
	// JSON that doesn't contain "role" should not be detected as JSON-RPC.
	text := `{"key": "value", "count": 42}`
	if got := DetectFormat(text); got != FormatPlainText {
		t.Errorf("DetectFormat(JSON without role) = %d, want FormatPlainText", got)
	}
}

func TestDetectWing(t *testing.T) {
	if got := DetectWing("my-project"); got != "my-project" {
		t.Errorf("DetectWing = %q, want %q", got, "my-project")
	}
}

func TestDetectRoom(t *testing.T) {
	tests := []struct {
		content string
		want    string
	}{
		{"We need to write a test for this function.", "testing"},
		{"The _test.go file is failing.", "testing"},
		{"Deploy the Docker container to production.", "devops"},
		{"The API endpoint returns 404.", "api"},
		{"Run the SQL migration on the database.", "data"},
		{"Update the TOML config file.", "config"},
		{"There's a panic and a crash in the logs.", "debugging"},
		{"Refactor the old code to use new patterns.", "refactoring"},
		{"The architecture uses a clean interface pattern.", "architecture"},
		{"Profiling shows high latency in the hot loop.", "performance"},
		{"Check the auth token permissions.", "security"},
		{"The sky is blue and water is wet.", "general"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := DetectRoom(tt.content); got != tt.want {
				t.Errorf("DetectRoom(%q...) = %q, want %q", tt.content[:30], got, tt.want)
			}
		})
	}
}

func TestDetectRoomCaseInsensitive(t *testing.T) {
	if got := DetectRoom("THE API ENDPOINT IS BROKEN"); got != "api" {
		t.Errorf("expected case-insensitive match: got %q, want %q", got, "api")
	}
}

func TestDetectHall(t *testing.T) {
	tests := []struct {
		content string
		want    string
	}{
		{"We decided to use brute-force search.", "decisions"},
		{"I chose the ONNX backend for embeddings.", "decisions"},
		{"Discovered that the library has a bug.", "discoveries"},
		{"Turns out the cache was stale.", "discoveries"},
		{"I prefer tabs over spaces.", "preferences"},
		{"You should use context.Background() here.", "advice"},
		{"Best practice is to validate inputs.", "advice"},
		{"The release happened last Tuesday.", "events"},
		{"We shipped the new feature.", "events"},
		{"The sky is blue and water is wet.", "facts"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := DetectHall(tt.content); got != tt.want {
				t.Errorf("DetectHall(%q...) = %q, want %q", tt.content[:30], got, tt.want)
			}
		})
	}
}

func TestDetectHallDefault(t *testing.T) {
	if got := DetectHall("just some ordinary text with no keywords"); got != "facts" {
		t.Errorf("expected default hall 'facts', got %q", got)
	}
}

func TestIsMarkdownTurnMarker(t *testing.T) {
	markers := []string{
		"## Human",
		"## Assistant",
		"### User",
		"**Human:**",
		"**Assistant:**",
		"  ## human  ",
	}
	for _, m := range markers {
		if !isMarkdownTurnMarker(m) {
			t.Errorf("expected %q to be a turn marker", m)
		}
	}

	nonMarkers := []string{
		"# Human",
		"Human:",
		"Just text",
		"## Introduction",
	}
	for _, m := range nonMarkers {
		if isMarkdownTurnMarker(m) {
			t.Errorf("expected %q to NOT be a turn marker", m)
		}
	}
}
