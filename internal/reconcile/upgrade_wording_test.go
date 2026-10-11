// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"strings"
	"testing"
)

// TestUpgradeSummary pins the two-category drift summary: required-only,
// optional-only, and both-tiers branches must each read distinctly, and the
// both-tiers line must name both counts — never collapse to a single
// undifferentiated "N new setting(s)" list. This is the operator-facing dry-run
// wording (plan §4/§6).
func TestUpgradeSummary(t *testing.T) {
	cases := []struct {
		name               string
		required, optional int
		want               string
	}{
		{"required only", 3, 0, "3 required key(s) — will add as commented defaults"},
		{"optional only", 0, 5, "5 optional setting(s) — will document as inert comments"},
		{"both tiers", 2, 4, "2 required key(s) as commented defaults; 4 optional setting(s) documented"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := upgradeSummary(c.required, c.optional)
			if got != c.want {
				t.Errorf("upgradeSummary(%d,%d) = %q, want %q", c.required, c.optional, got, c.want)
			}
		})
	}

	// The two-tier line must keep the categories textually distinct.
	both := upgradeSummary(2, 4)
	if !strings.Contains(both, "required") || !strings.Contains(both, "optional") {
		t.Errorf("both-tiers summary must name required AND optional, got %q", both)
	}
	if strings.Contains(both, "new setting(s)") {
		t.Errorf("summary must not collapse to the old \"new setting(s)\" wording, got %q", both)
	}
}

// TestUpgradeDetails pins that proposed keys are tier-tagged so the dry-run never
// reads as one undifferentiated list: required keys first with a "required  "
// prefix, optional keys after with an "optional  " prefix.
func TestUpgradeDetails(t *testing.T) {
	required := []string{"git_enabled", "log_level"}
	optional := []string{"embedder.backend", "palace.llm.endpoint"}
	got := upgradeDetails(required, optional)

	want := []string{
		"required  git_enabled",
		"required  log_level",
		"optional  embedder.backend",
		"optional  palace.llm.endpoint",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("upgradeDetails mismatch\n got: %v\nwant: %v", got, want)
	}

	// Every line carries exactly one tier prefix; the two categories are distinct.
	for i, line := range got {
		isReq := strings.HasPrefix(line, "required  ")
		isOpt := strings.HasPrefix(line, "optional  ")
		if isReq == isOpt {
			t.Errorf("detail %d %q must carry exactly one of the required/optional prefixes", i, line)
		}
	}
}
