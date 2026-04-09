// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"strings"
)

// Format represents a detected transcript format.
type Format int

const (
	// FormatPlainText is unstructured text (default).
	FormatPlainText Format = iota
	// FormatJSONRPC is JSON with "role" fields (chat completion format).
	FormatJSONRPC
	// FormatMarkdown is markdown with turn markers (## Human / ## Assistant).
	FormatMarkdown
)

// DetectFormat inspects the first ~500 bytes of text to determine its format.
func DetectFormat(text string) Format {
	sample := text
	if len(sample) > 500 {
		sample = sample[:500]
	}
	sample = strings.TrimSpace(sample)

	// JSON-RPC / chat format: starts with [ or { and contains "role".
	if len(sample) > 0 && (sample[0] == '[' || sample[0] == '{') {
		if strings.Contains(sample, `"role"`) {
			return FormatJSONRPC
		}
	}

	// Markdown transcript: contains turn markers.
	lower := strings.ToLower(sample)
	markdownMarkers := []string{
		"## human", "## assistant", "## user",
		"### human", "### assistant", "### user",
		"**human:**", "**assistant:**", "**user:**",
	}
	for _, m := range markdownMarkers {
		if strings.Contains(lower, m) {
			return FormatMarkdown
		}
	}

	return FormatPlainText
}
