// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package chunk

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
	text := `{"key": "value", "count": 42}`
	if got := DetectFormat(text); got != FormatPlainText {
		t.Errorf("DetectFormat(JSON without role) = %d, want FormatPlainText", got)
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
