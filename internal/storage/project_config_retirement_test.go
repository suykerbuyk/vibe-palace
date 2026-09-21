// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// ONE-SHOT: deleted with project_config_retirement.go.

import (
	"slices"
	"testing"
)

// retiredScoringWant is the palace.scoring every fixture below carries, as a
// decoded map. The expected block is renderScoringSections of it — the host
// writer's own renderer — so a fixture passes only if the retirement renders
// through that renderer from a correct decode.
func retiredScoringWant(t *testing.T) string {
	t.Helper()
	want, err := renderScoringSections(map[string]any{
		"min_score": 0.4,
		"rooms": map[string]any{
			"decisions": map[string]any{"high": 0.9, "medium": 0.6},
			"notes":     map[string]any{"low": 0.1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return want
}

// TestRenderRetiredScoringIsTheHostWritersRenderer is D1: the carried block is
// byte-identical to what renderScoringSections produces, so pasting it into a
// host-local file is exactly what the host writer would have written.
func TestRenderRetiredScoringIsTheHostWritersRenderer(t *testing.T) {
	data := []byte(`[meta]
kind = "project"

[palace.scoring]
min_score = 0.4

[palace.scoring.rooms.decisions]
high = 0.9
medium = 0.6

[palace.scoring.rooms.notes]
low = 0.1
`)
	got, _, err := RenderRetiredScoring(data)
	if err != nil {
		t.Fatalf("RenderRetiredScoring: %v", err)
	}
	if want := retiredScoringWant(t); got != want {
		t.Errorf("carried block is not renderScoringSections' output\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderRetiredScoringDecodesEveryShape is D2. Every shape is
// ordinary TOML that a line scanner or a [meta] requirement gets wrong:
//
//   - nested-indented: headers indented under [palace], as the encoder emits;
//   - flat-spliced: fully-qualified sections spliced below unrelated content;
//   - no-meta: a hand-written file with no [meta] block.
//
// D5's quantum-ng shape has its own test below.
func TestRenderRetiredScoringDecodesEveryShape(t *testing.T) {
	want := retiredScoringWant(t)
	for _, tc := range []struct{ name, data string }{
		{"nested-indented", `[meta]
  kind = "project"

[palace]
  [palace.scoring]
    min_score = 0.4
    [palace.scoring.rooms]
      [palace.scoring.rooms.decisions]
        high = 0.9
        medium = 0.6
      [palace.scoring.rooms.notes]
        low = 0.1
`},
		{"flat-spliced", `[meta]
kind = "project"

[search]
k = 5

[palace.scoring]
min_score = 0.4

[palace.scoring.rooms.decisions]
high = 0.9
medium = 0.6

[palace.scoring.rooms.notes]
low = 0.1

[summarization]
enabled = true
`},
		{"no-meta", `[palace.scoring]
min_score = 0.4
rooms.decisions.high = 0.9
rooms.decisions.medium = 0.6
rooms.notes.low = 0.1
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := RenderRetiredScoring([]byte(tc.data))
			if err != nil {
				t.Fatalf("RenderRetiredScoring: %v", err)
			}
			if got != want {
				t.Errorf("shape %s did not render the full scoring block\n got:\n%s\nwant:\n%s", tc.name, got, want)
			}
		})
	}
}

// TestRenderRetiredScoringQuantumNgShape is D5, the live file that broke the
// line-scanning assumptions: an INDENTED [meta], a bare [palace] carrying a
// key of its own, and flush-left rooms with NO [palace.scoring] header anywhere
// — the rooms' parent tables exist only implicitly. A reader that locates
// [palace.scoring] by scanning lines finds nothing here and drops the rooms.
func TestRenderRetiredScoringQuantumNgShape(t *testing.T) {
	got, _, err := RenderRetiredScoring([]byte(`  [meta]
  kind = "project"
  schema = 1

[palace]
enabled = true

[palace.scoring.rooms.decisions]
high = 0.9
medium = 0.6

[palace.scoring.rooms.notes]
low = 0.1
`))
	if err != nil {
		t.Fatalf("RenderRetiredScoring: %v", err)
	}
	want, err := renderScoringSections(map[string]any{"rooms": map[string]any{
		"decisions": map[string]any{"high": 0.9, "medium": 0.6},
		"notes":     map[string]any{"low": 0.1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("parentless rooms were lost\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderRetiredScoringNamesDroppedSections: every section with no
// per-project tier is named, and [meta] and palace.scoring are not.
func TestRenderRetiredScoringNamesDroppedSections(t *testing.T) {
	_, dropped, err := RenderRetiredScoring([]byte(`[meta]
kind = "project"
[search]
k = 5
[palace.rooms.decisions]
keywords = ["x"]
[palace.scoring]
min_score = 0.4
[enrichment]
enabled = true
`))
	if err != nil {
		t.Fatalf("RenderRetiredScoring: %v", err)
	}
	if want := []string{"enrichment", "palace.rooms", "search"}; !slices.Equal(dropped, want) {
		t.Errorf("dropped = %v, want %v", dropped, want)
	}
}

func TestRenderRetiredScoringRefusesMalformedTOML(t *testing.T) {
	if _, _, err := RenderRetiredScoring([]byte("[palace.scoring\nmin_score = 0.4\n")); err == nil {
		t.Error("malformed TOML decoded without error")
	}
}

func TestIsStampPathMatchesCheckCompatiblesLocations(t *testing.T) {
	for rel, want := range map[string]bool{
		"Projects/p/.surface":    true,
		"palace/p/.surface":      true,
		"Templates/.surface":     true,
		"Audits/.surface":        true,
		".surface":               false,
		"Projects/.surface":      false,
		"Projects/p/q/.surface":  false,
		"Projects/p/config.toml": false,
	} {
		if got := isStampPath(rel); got != want {
			t.Errorf("isStampPath(%q) = %v, want %v", rel, got, want)
		}
	}
}
