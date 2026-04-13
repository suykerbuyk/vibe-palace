// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package absorb

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		heading string
		body    string
		want    Destination
	}{
		{"Architecture", "", DestArchitecture},
		{"Architecture (from PRD §4, §7)", "", DestArchitecture},
		{"Design", "", DestArchitecture},
		{"Package layout", "", DestArchitecture},
		{"Move atomicity", "", DestArchitecture},
		{"Testing", "", DestTesting},
		{"Testing strategy (PRD §8)", "", DestTesting},
		{"Coverage", "", DestTesting},
		{"Non-goals for v1", "", DestScope},
		{"Out of scope", "", DestScope},
		{"Commands (once bootstrapped)", "", DestWorkflowCmds},
		{"Build", "", DestWorkflowCmds},
		{"Make targets", "", DestWorkflowCmds},
		{"When working in this repo", "", DestWorkflowRules},
		{"Workflow", "", DestWorkflowRules},
		{"Conventions", "", DestWorkflowRules},
		{"Status", "", DestResumeScratch},
		{"Overview", "", DestResumeScratch},
		{"About", "", DestResumeScratch},
		{"Notation", "", DestKnowledge},
		{"Glossary", "", DestKnowledge},
		{"Vocabulary", "", DestKnowledge},
		{"Rules — quick reference (PRD §3 is authoritative)", "", DestKnowledge},
		{"Rules of the game", "", DestKnowledge},
		// Body tiebreaker — "Rules" with workflow-imperative body → workflow.
		{"Rules — team conventions", "- Never commit secrets\n- Always run vp check", DestWorkflowRules},
		// Single-word heading → resume scratch (project title).
		{"Checkers01", "", DestResumeScratch},
		// Unknown → misc.
		{"Some Random Heading", "", DestMisc},
		// Empty heading (preamble) → resume scratch.
		{"", "some intro text", DestResumeScratch},
		// Vibe-Palace Integration → keep in place.
		{"Vibe-Palace Integration", "", DestKeepInPlace},
	}
	for _, tc := range tests {
		t.Run(tc.heading, func(t *testing.T) {
			got := Classify(tc.heading, tc.body)
			if got != tc.want {
				t.Errorf("Classify(%q) = %+v; want %+v", tc.heading, got, tc.want)
			}
		})
	}
}
