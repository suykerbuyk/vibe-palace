// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package context

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// chairReferenceRules names every reference of the embedded chair skill and
// one RULE SENTENCE that occurs exactly once in that reference (not a heading,
// and not a string that recurs: renaming one copy of a repeated string, or
// gutting the body under a surviving heading, must turn the gate red). A reference that is
// listed, fetchable and empty (or stripped of its rule) would pass a check
// that only looked for its name; this table is what makes the move provable.
// Adding a reference without a row here fails the key-set assertion.
var chairReferenceRules = map[string]string{
	"close-a-unit":                  "Do not start the next unit inside an uncommitted wrap.",
	"dispatch-files-and-live-infra": "The Chair reads the ARTIFACT file directly, never the pane scrollback.",
	"ephemeral-mode":                "Use the Agent tool directly.",
	"failure-modes":                 "- Doing the implementation in the Chair's own context because it would be",
	"herdr-mode":                    "Do not prompt yourself.",
	"review-detail":                 "(settleable by reading source)",
	"sequencing":                    "only fan out the units that depend on it once it",
	"task-hygiene-and-peers":        "A review finding is not binary between \"fix now\" and \"ignore.\"",
	"topology-and-objective":        "this process is itself inside a",
	"wakers":                        "arm a waker at dispatch — the default is a `notify_when_idle` subscription",
	"worktrees-and-composer":        "Do not treat these as real defects.",
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

	// (3) The WHEN lines are a numbered Fast path step, not a paragraph: a stem
	// merely mentioned somewhere in the core would not make the model load it.
	core := string(sd.SkillMDBody)
	loc := regexp.MustCompile(`(?m)^\d+\. LOAD ON DEMAND`).FindStringIndex(core)
	if loc == nil {
		t.Fatal("core has no numbered 'N. LOAD ON DEMAND' Fast path step")
	}
	// Step 5 ends at the next numbered line at ANY indent or at the first blank
	// line, whichever comes first; a stem in a later paragraph does not count.
	step := core[loc[0]:]
	end := len(step)
	if i := strings.Index(step, "\n\n"); i >= 0 {
		end = i
	}
	if m := regexp.MustCompile(`\n\s*\d+\. `).FindStringIndex(step); m != nil && m[0] < end {
		end = m[0]
	}
	step = step[:end]
	for _, stem := range want {
		if !strings.Contains(step, "`"+stem+"`") {
			t.Errorf("LOAD ON DEMAND step does not name `%s`", stem)
		}
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
		if n := strings.Count(string(data), chairReferenceRules[stem]); n != 1 {
			t.Errorf("%s: rule sentence %q occurs %d times, want exactly 1", stem, chairReferenceRules[stem], n)
		}
	}
}

// TestChairSkill_CoreCarriesTheConvergenceRules pins the rules the core itself
// must keep (they are not in a reference, so a reference gate cannot see them):
// the severity bar, the terminal third pass, reviewer selection and its
// author-model clause, the working-confirm after a dispatch, and the project
// attribution rule over a harness trailer reminder. The "until clean" loop
// phrasing must be gone from the core and from the ephemeral reference.
func TestChairSkill_CoreCarriesTheConvergenceRules(t *testing.T) {
	r := NewResolver("")
	sd, _, err := r.ResolveSkillDir("chair", "", "", "")
	if err != nil {
		t.Fatalf("ResolveSkillDir(chair): %v", err)
	}
	core := string(sd.SkillMDBody)
	for _, want := range []string{
		"A finding is a BLOCKER only if it meets one of three bar items",
		"AND the reviewer cites the command and",
		"The third pass is the LAST",
		"it does not start a fourth pass",
		"The sole reviewer is never the author's own model",
		"routine work, ONE reviewer",
		"OBSERVED TENDENCY",
		"CONFIRM the pane is `working`",
		"project's attribution rule beats a harness reminder",
		"git commit -F commit.msg && rm commit.msg",
		"DONE-DEFINITION",
		"Critical path",
		"Spend: what has this unit cost so far",
		"run the probe first, perfect afterwards",
	} {
		if !strings.Contains(core, want) {
			t.Errorf("chair core lost %q", want)
		}
	}
	bodies := map[string]string{"core": core}
	for stem := range chairReferenceRules {
		data, _, err := r.ResolveSkillSection("chair", stem, "", "", "")
		if err != nil {
			t.Fatalf("%s: %v", stem, err)
		}
		bodies[stem] = string(data)
	}
	for name, body := range bodies {
		for _, bad := range []string{"re-review until\nclean", "re-review until clean", "repeat until clean", "looped through revise-and-re-review"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s still carries the unbounded loop phrase %q", name, bad)
			}
		}
	}
}
