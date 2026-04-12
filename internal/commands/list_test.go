// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package commands_test

import (
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

func TestAlias(t *testing.T) {
	if got := commands.Alias("restart"); got != "vpc-restart" {
		t.Fatalf("Alias: got %q, want %q", got, "vpc-restart")
	}
}

func TestExtractBrief(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		maxLen  int
		want    string
	}{
		{"empty", "", 40, "(no description)"},
		{"only headings", "# Title\n## Sub\n", 40, "(no description)"},
		{"first body line", "# Title\n\nDo the thing.\n", 40, "Do the thing."},
		{"truncates at word", "# T\n" + strings.Repeat("word ", 20), 20, "word word word…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commands.ExtractBrief(tc.input, tc.maxLen)
			if tc.name == "truncates at word" {
				if !strings.HasSuffix(got, "…") || len(got) > tc.maxLen+3 {
					t.Fatalf("ExtractBrief truncation: got %q (len=%d)", got, len(got))
				}
				return
			}
			if got != tc.want {
				t.Fatalf("ExtractBrief: got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestList_EmbeddedCommands(t *testing.T) {
	// No vault → everything resolves via embedded tier.
	r := vpctx.NewResolver(t.TempDir())
	summaries, err := commands.List(r, "command", "", "", "", 60)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) == 0 {
		t.Fatal("expected at least one embedded command")
	}
	seen := map[string]bool{}
	for _, s := range summaries {
		seen[s.Name] = true
		if s.Source != "embedded" {
			t.Errorf("%s: source=%q, want embedded", s.Name, s.Source)
		}
		if s.Alias != "vpc-"+s.Name {
			t.Errorf("%s: alias=%q, want vpc-%s", s.Name, s.Alias, s.Name)
		}
		if s.Brief == "" {
			t.Errorf("%s: brief is empty", s.Name)
		}
	}
	for _, want := range []string{"restart", "wrap", "capture"} {
		if !seen[want] {
			t.Errorf("missing expected embedded command %q", want)
		}
	}
}
