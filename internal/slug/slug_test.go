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

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Valid slugs
		{name: "simple word", input: "hello", wantErr: false},
		{name: "hyphenated", input: "hello-world", wantErr: false},
		{name: "alphanumeric segments", input: "a1-b2", wantErr: false},
		{name: "single char", input: "a", wantErr: false},
		{name: "numeric", input: "123", wantErr: false},

		// Boundary lengths
		{name: "60 chars", input: strings.Repeat("a", 60), wantErr: false},
		{name: "64 chars", input: strings.Repeat("a", 64), wantErr: false},
		{name: "65 chars", input: strings.Repeat("a", 65), wantErr: true},

		// Empty
		{name: "empty string", input: "", wantErr: true},

		// Path traversal
		{name: "double dot", input: "..", wantErr: true},
		{name: "embedded double dot", input: "foo..bar", wantErr: true},

		// Path separators
		{name: "forward slash", input: "/", wantErr: true},
		{name: "backslash", input: "\\", wantErr: true},
		{name: "embedded slash", input: "foo/bar", wantErr: true},

		// Uppercase
		{name: "leading uppercase", input: "Hello", wantErr: true},
		{name: "all uppercase", input: "HELLO", wantErr: true},

		// Special characters
		{name: "underscore", input: "hello_world", wantErr: true},
		{name: "space", input: "hello world", wantErr: true},
		{name: "dot", input: "hello.world", wantErr: true},

		// Hyphen placement
		{name: "leading hyphen", input: "-hello", wantErr: true},
		{name: "trailing hyphen", input: "hello-", wantErr: true},
		{name: "consecutive hyphens", input: "hello--world", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// TestValidateCreatable pins the fix for
// slug-validate-accepts-windows-reserved-device-names: a Windows reserved
// device name must be refused for a NEW project/room/wing name, even though
// it passes the plain slugPattern regex Validate uses.
func TestValidateCreatable(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "ordinary valid slug", input: "hello-world", wantErr: false},

		// Windows reserved device names — the actual bug. Validate alone
		// accepts every one of these.
		{name: "con", input: "con", wantErr: true},
		{name: "aux", input: "aux", wantErr: true},
		{name: "nul", input: "nul", wantErr: true},
		{name: "prn", input: "prn", wantErr: true},
		{name: "com1", input: "com1", wantErr: true},
		{name: "lpt1", input: "lpt1", wantErr: true},

		// A reserved name is not merely a PREFIX match — "console" and
		// "auxiliary" are real, legal slugs and must not be caught by a naive
		// substring check.
		{name: "reserved-name prefix is fine", input: "console", wantErr: false},
		{name: "reserved-name prefix is fine 2", input: "auxiliary", wantErr: false},

		// Validate's own rejections must still surface, unchanged, through
		// ValidateCreatable — it must not silently swallow them or replace
		// them with a portability-specific message.
		{name: "already invalid per Validate", input: "BAD SLUG", wantErr: true},
		{name: "empty string", input: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreatable(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCreatable(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}

	// Validate's own error message must still be the one returned for a
	// string Validate itself rejects — ValidateCreatable must not mask it
	// behind vaultfs's error or its own.
	err := ValidateCreatable("BAD SLUG")
	if err == nil {
		t.Fatal("expected an error")
	}
	wantValidateErr := Validate("BAD SLUG")
	if err.Error() != wantValidateErr.Error() {
		t.Errorf("ValidateCreatable(%q) = %q, want Validate's own message %q", "BAD SLUG", err, wantValidateErr)
	}
}
