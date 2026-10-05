// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

// TestRefreshIndexRefusesProjectWithNothingToRefresh: a truly empty project is
// refused in-process, before any spawn, and nothing is launched.
//
// Mutation: make the handler always spawn, and this goes red (a refusal becomes
// a launch).
func TestRefreshIndexRefusesProjectWithNothingToRefresh(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)
	h.seedProject(t, "never-indexed") // the project exists but has no content

	text, isErr := h.callToolRaw(t, "vp_refresh_index", map[string]any{
		"project": "never-indexed",
	})
	if !isErr {
		t.Fatalf("refresh of a project with nothing to index reported SUCCESS: %s", text)
	}
	for _, want := range []string{"nothing to refresh", "index nothing"} {
		if !strings.Contains(text, want) {
			t.Errorf("refusal missing %q:\n%s", want, text)
		}
	}
	if n := len(h.RecordedLaunches()); n != 0 {
		t.Fatalf("a refused project launched %d rebuilds, want 0", n)
	}
}

// TestRefreshIndexReportsCountsForARealRebuild: the dry_run report carries the
// per-project counts (the started call carries none), and the started call
// launches the detached rebuild exactly once.
func TestRefreshIndexReportsCountsForARealRebuild(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)
	h.seedProject(t, "counted")
	h.Seed(t, testinfra.WithDrawer("counted", "facts", "general", "the drawer body", "facts", "2026-08-18T10:00:00Z"))

	// dry_run: an in-process report, no launch.
	text, isErr := h.callToolRaw(t, "vp_refresh_index", map[string]any{
		"project": "counted",
		"dry_run": true,
	})
	if isErr {
		t.Fatalf("dry run was refused: %s", text)
	}
	var rep map[string]any
	if err := json.Unmarshal([]byte(text), &rep); err != nil {
		t.Fatalf("dry-run result is not JSON: %v\n%s", err, text)
	}
	if rep["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", rep["dry_run"])
	}
	if _, ok := rep["projects"]; !ok {
		t.Errorf("dry-run report carries no per-project counts: %s", text)
	}
	if n := len(h.RecordedLaunches()); n != 0 {
		t.Fatalf("a dry run launched %d rebuilds, want 0", n)
	}

	// The started call: {started:true}, exactly one launch, and no counts.
	text, isErr = h.callToolRaw(t, "vp_refresh_index", map[string]any{"project": "counted"})
	if isErr {
		t.Fatalf("start of a project with content was refused: %s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, text)
	}
	if got["started"] != true {
		t.Errorf("started = %v, want true", got["started"])
	}
	if _, ok := got["drawers"]; ok {
		t.Errorf("the started call must carry no rebuild counts: %s", text)
	}
	if n := len(h.RecordedLaunches()); n != 1 {
		t.Fatalf("the started call launched %d rebuilds, want 1", n)
	}
}
