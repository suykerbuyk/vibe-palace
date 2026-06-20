// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package absorb

import (
	"os"
	"path/filepath"
	"testing"
)

const checkersFixture = `# checkers01

Intro paragraph that belongs to resume.

## Status

Pre-code. Only prd exists.

## Architecture (from PRD §4, §7)

Package layout once bootstrapped:
- cmd/
- internal/game
- internal/tui

## Rules — quick reference

- 8x8 board
- 12 pieces/side
- Forced capture

## Move atomicity

A Move represents an entire turn.

## Commands (once bootstrapped)

- go build
- go test

## Testing strategy (PRD §8)

Unit tests for every rules-engine scenario.

## Notation

Standard draughts numeric 1-32.

## Non-goals for v1 (do not build)

Networked play, save/load UI.

## When working in this repo

- Never silently deviate from PRD.
- Do not add legality check in internal/tui.

<!-- vibe-palace:begin v=1 sha=c823313 -->
## Vibe-Palace Integration

body
<!-- vibe-palace:end -->
`

func TestBuildPlan_RoutingTable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(checkersFixture), 0644); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Destination{
		"checkers01":                      DestResumeScratch,
		"Status":                          DestResumeScratch,
		"Architecture (from PRD §4, §7)":  DestArchitecture,
		"Rules — quick reference":         DestKnowledge,
		"Move atomicity":                  DestArchitecture,
		"Commands (once bootstrapped)":    DestWorkflowCmds,
		"Testing strategy (PRD §8)":       DestTesting,
		"Notation":                        DestKnowledge,
		"Non-goals for v1 (do not build)": DestScope,
		"When working in this repo":       DestWorkflowRules,
	}
	got := map[string]Destination{}
	for _, it := range plan.Items {
		got[it.Section.Heading] = it.Dest
	}
	for heading, wantDest := range want {
		gotDest, ok := got[heading]
		if !ok {
			t.Errorf("missing section %q in plan", heading)
			continue
		}
		if gotDest != wantDest {
			t.Errorf("section %q: got %+v, want %+v", heading, gotDest, wantDest)
		}
	}
	// Vibe-Palace Integration must not appear as a plan item.
	if _, leaked := got["Vibe-Palace Integration"]; leaked {
		t.Errorf("Vibe-Palace Integration leaked into plan")
	}
	// Sources must include CLAUDE.md as supported.
	if len(plan.Sources) != 1 || !plan.Sources[0].Supported {
		t.Errorf("expected one supported source, got %+v", plan.Sources)
	}
	if !plan.Sources[0].HadBlock {
		t.Errorf("HadBlock should be true for fixture with managed block")
	}
}

func TestBuildPlan_NoFiles(t *testing.T) {
	dir := t.TempDir()
	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 0 || len(plan.Sources) != 0 {
		t.Errorf("expected empty plan, got %+v", plan)
	}
}

func TestBuildPlan_WholeFileAdapter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".cursorrules"),
		[]byte("You are a helpful assistant.\nAlways cite sources.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(plan.Items))
	}
	if plan.Items[0].Dest != DestKnowledge {
		t.Errorf("cursorrules → %+v, want knowledge", plan.Items[0].Dest)
	}
	if plan.Items[0].Section.Heading != "" {
		t.Errorf("whole-file section should have empty heading, got %q",
			plan.Items[0].Section.Heading)
	}
}
