// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package commands

import "testing"

// TestMatchesOnly covers the skill-directory prefix contract and the
// exact-match path used for commands. Lives in-package so it can see
// the unexported helper.
func TestMatchesOnly(t *testing.T) {
	cases := []struct {
		rt, name, only string
		want           bool
	}{
		{"command", "restart", "restart", true},
		{"command", "restart", "wrap", false},
		{"skill", "startup-analyst", "startup-analyst", true},
		{"skill", "startup-analyst/SKILL.md", "startup-analyst", true},
		{"skill", "startup-analyst/references/capex-opex.md", "startup-analyst", true},
		{"skill", "other-skill/SKILL.md", "startup-analyst", false},
	}
	for _, tc := range cases {
		if got := matchesOnly(tc.rt, tc.name, tc.only); got != tc.want {
			t.Errorf("matchesOnly(%q,%q,%q)=%v, want %v",
				tc.rt, tc.name, tc.only, got, tc.want)
		}
	}
}
