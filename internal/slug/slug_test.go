// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package slug

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"space and leading digits", "00_Regulatory Monitor", "00-regulatory-monitor"},
		{"plain lowercase", "ObsAkkadscale", "obsakkadscale"},
		{"already hyphenated", "Modo-Melius", "modo-melius"},
		{"lowercase hyphenated", "vibe-palace", "vibe-palace"},
		{"dot separator", "fetch.bins", "fetch-bins"},
		{"empty string", "", ""},
		{"single char", "a", "a"},
		{"all separators", "---", ""},
		{"multiple spaces", "hello   world", "hello-world"},
		{"mixed separators", "a/b.c:d_e", "a-b-c-d-e"},
		{"truncation at hyphen boundary", strings.Repeat("a-", 40), strings.Repeat("a-", 30)[:59]},
		{"truncation without hyphen", strings.Repeat("a", 70), strings.Repeat("a", 60)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Slugify(tt.input)
			if got != tt.want {
				t.Errorf("Slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSlugifyTruncation(t *testing.T) {
	// Build a string that will produce a slug longer than 60 chars.
	// "abcdefghij-" repeated 7 times = 77 chars slug.
	long := strings.Repeat("abcdefghij ", 7)
	got := Slugify(long)
	if len(got) > 60 {
		t.Errorf("Slugify(%q) length = %d, want <= 60", long, len(got))
	}
	// Should truncate at a hyphen boundary.
	if strings.HasSuffix(got, "-") {
		t.Errorf("Slugify(%q) = %q ends with hyphen", long, got)
	}
}
