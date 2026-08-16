// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"strings"
	"testing"
)

// liveVibePalacePreamble is the head of the vibe-palace archive, byte for byte.
// rezbldrvault's is identical apart from the project name — the second file was
// seeded from the first, which is exactly how a false comment spreads.
const liveVibePalacePreamble = `---
type: project-iterations
project: vibe-palace
---

# vibe-palace — Iteration Narratives

<!-- Each iteration gets a section below, newest first.
     An "iteration" is a logical unit of work (feature, fix, refactor)
     that may span multiple sessions. -->

## Iteration 76 — ` + "`auto-capture-ide-hooks`" + ` end-to-end (2026-04-15)

body
`

func TestRepairIterationsPreambleReplacesTheFalseComment(t *testing.T) {
	got, state := RepairIterationsPreamble(liveVibePalacePreamble)
	if state != PreambleRepaired {
		t.Fatalf("state = %q, want %q", state, PreambleRepaired)
	}
	if strings.Contains(got, "newest first") {
		t.Errorf("the false claim survived:\n%s", got)
	}
	if !strings.Contains(got, AccurateIterationsPreamble) {
		t.Errorf("the accurate sentence was not written:\n%s", got)
	}
	// Heading-only discipline applies here too, inverted: ONLY the comment
	// block's lines may differ.
	before := strings.Split(liveVibePalacePreamble, "\n")
	after := strings.Split(got, "\n")
	if len(before) != len(after) {
		t.Fatalf("line count changed %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i] == after[i] {
			continue
		}
		if !strings.Contains(before[i], "iteration") && !strings.Contains(before[i], "newest first") &&
			!strings.HasPrefix(strings.TrimSpace(before[i]), "that may span") &&
			!strings.HasPrefix(strings.TrimSpace(before[i]), "An \"iteration\"") {
			t.Errorf("line %d changed outside the comment block: %q -> %q", i+1, before[i], after[i])
		}
	}
}

// TestRepairIterationsPreambleSparesTrueStatements is the reason the match is an
// exact literal. Three places in the vault say "newest first" and mean it; a
// rule shaped like "delete the newest-first claim" would eventually eat one.
func TestRepairIterationsPreambleSparesTrueStatements(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			// rezbldr/iterations.md L5 — TRUE, and scoped to three preserved
			// pre-rename iterations that really are ordered newest first.
			name: "rezbldr's true prose sentence",
			content: "# rezbldr — Iteration History\n\n## Pre-Rename Era\n\n" +
				"Preserved verbatim (newest-first ordering) for historical continuity.\n\n" +
				writerFrame(3, "Tutorial", "body"),
		},
		{
			// A narrative describing `git log`, which really does print
			// newest-first. Body text, and evidence — it must survive verbatim.
			name: "a narrative body describing git log",
			content: "# p — Iteration Narratives\n" +
				writerFrame(1, "a", "`git log --oneline` prints newest-first. The block was reconstructed."),
		},
		{
			name:    "a hand-edited variant of the block",
			content: "# p\n\n<!-- Each iteration gets a section below, newest first. -->\n\n" + writerFrame(1, "a", "b"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, state := RepairIterationsPreamble(tc.content)
			if state != PreambleAbsent {
				t.Errorf("state = %q, want %q", state, PreambleAbsent)
			}
			if got != tc.content {
				t.Errorf("content changed:\n before %q\n  after %q", tc.content, got)
			}
		})
	}
}

func TestRepairIterationsPreambleRefusesOddShapes(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    PreambleRepairState
	}{
		{
			name:    "two copies",
			content: "# p\n\n" + falseNewestFirstPreamble + "\n\n" + falseNewestFirstPreamble + "\n" + writerFrame(1, "a", "b"),
			want:    PreambleAmbiguous,
		},
		{
			// A narrative QUOTING the defect it was filed about. Deleting it
			// would destroy the evidence for the very repair being made.
			name:    "quoted inside a narrative body",
			content: "# p\n" + writerFrame(1, "a", "the archive said:\n"+falseNewestFirstPreamble+"\nwhich is backwards"),
			want:    PreambleNotInPreamble,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, state := RepairIterationsPreamble(tc.content)
			if state != tc.want {
				t.Errorf("state = %q, want %q", state, tc.want)
			}
			if got != tc.content {
				t.Errorf("a refused repair changed content")
			}
		})
	}
}

// TestRepairIterationsPreambleIsIdempotent: the accurate sentence must not
// itself look like something to repair on the next run.
func TestRepairIterationsPreambleIsIdempotent(t *testing.T) {
	once, _ := RepairIterationsPreamble(liveVibePalacePreamble)
	twice, state := RepairIterationsPreamble(once)
	if state != PreambleAbsent {
		t.Errorf("second run state = %q, want %q", state, PreambleAbsent)
	}
	if twice != once {
		t.Errorf("second run changed bytes")
	}
}

// TestAccuratePreambleSaysWhatIsTrue pins the three facts the replacement has to
// carry. It is a comment nothing compiles, so a test is the only thing standing
// between it and the next well-meaning rewording that reintroduces a claim about
// ordering.
func TestAccuratePreambleSaysWhatIsTrue(t *testing.T) {
	if strings.Contains(AccurateIterationsPreamble, "newest") {
		t.Errorf("the replacement still talks about newest-first: %q", AccurateIterationsPreamble)
	}
	for _, must := range []string{"APPEND-ONLY", "END", "order the narratives were written", "vp_get_iteration"} {
		if !strings.Contains(AccurateIterationsPreamble, must) {
			t.Errorf("the replacement omits %q: %q", must, AccurateIterationsPreamble)
		}
	}
}
