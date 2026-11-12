// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package context

import (
	"sort"
	"strings"
	"testing"
)

// chairReferenceRules names every reference of the embedded chair skill and
// one rule string that exists only in that reference. A reference that is
// listed, fetchable and empty (or stripped of its rule) would pass a check
// that only looked for its name; this table is what makes the move provable.
// Adding a reference without a row here fails the key-set assertion.
var chairReferenceRules = map[string]string{
	"close-a-unit":                  "git commit -F commit.msg && rm commit.msg",
	"dispatch-files-and-live-infra": "Every dispatch is two files",
	"ephemeral-mode":                "Review cadence",
	"failure-modes":                 "Failure modes",
	"herdr-mode":                    "Seat the subordinate roster",
	"review-detail":                 "Two kinds of reviewer conflict",
	"sequencing":                    "Sequencing dependent units",
	"task-hygiene-and-peers":        "Task hygiene for marathon investigations",
	"topology-and-objective":        "Detect subordinate topology",
	"wakers":                        "a one-shot idle subscription",
	"worktrees-and-composer":        "Git worktrees and stale diagnostics",
}

// TestChairSkill_ReferencesResolveAndCarryRules resolves every chair reference
// through the real section resolver (embedded tier only: a vault or project
// file replacing a reference is an operator override by design) and asserts
// the body is non-empty and carries its named rule.
func TestChairSkill_ReferencesResolveAndCarryRules(t *testing.T) {
	r := NewResolver("")
	sd, src, err := r.ResolveSkillDir("chair", "", "", "")
	if err != nil {
		t.Fatalf("ResolveSkillDir(chair): %v", err)
	}
	if src != "embedded" {
		t.Fatalf("chair source = %q, want embedded", src)
	}

	got := append([]string(nil), sd.ReferenceNames...)
	sort.Strings(got)
	var want []string
	for stem := range chairReferenceRules {
		want = append(want, stem)
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("chair references = %v, want %v (a new reference needs a row in chairReferenceRules)", got, want)
	}

	for _, stem := range want {
		data, source, err := r.ResolveSkillSection("chair", stem, "", "", "")
		if err != nil {
			t.Errorf("ResolveSkillSection(chair, %s): %v", stem, err)
			continue
		}
		if source != "embedded" {
			t.Errorf("%s: source = %q, want embedded", stem, source)
		}
		if len(data) == 0 {
			t.Errorf("%s: body is empty", stem)
			continue
		}
		if !strings.Contains(string(data), chairReferenceRules[stem]) {
			t.Errorf("%s: body does not carry its rule %q", stem, chairReferenceRules[stem])
		}
	}
}
