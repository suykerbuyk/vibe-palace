// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRefreshIndexRefusesProjectWithNothingToRefresh is the defect test.
//
// It seeds NOTHING — no drawers, no iterations.md. That is the whole point and
// it is why this defect shipped: every existing test of this path seeds a
// drawer store first, and a fixture with a store cannot see a bug whose only
// symptom is what happens when there isn't one.
//
// It goes through callToolRaw rather than the handler, because a refusal is
// what is being asserted and callToolRaw is the helper that can observe one.
//
// Mutation: restore `return map[string]string{"status":"rebuilt", ...}, nil` on
// this path and this goes red while every seeded test stays green.
func TestRefreshIndexRefusesProjectWithNothingToRefresh(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	text, isErr := h.callToolRaw(t, "vp_refresh_index", map[string]any{
		"project": "never-indexed",
	})

	if !isErr {
		t.Fatalf("refresh of a project with no store and no content reported SUCCESS: %s", text)
	}
	// The refusal has to carry the reason and the real next step, or it just
	// moves the dead end somewhere else.
	for _, want := range []string{"nothing to refresh", "no palace store", "capture time"} {
		if !strings.Contains(text, want) {
			t.Errorf("refusal missing %q:\n%s", want, text)
		}
	}
}

// TestRefreshIndexReportsCountsForARealRebuild is the other half: a project
// that DOES have a store must still succeed, and the result must let a caller
// tell a real rebuild from a no-op without listing the vault. A refusal that
// also refused healthy projects would be a worse instrument, not a better one.
func TestRefreshIndexReportsCountsForARealRebuild(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)
	h.addDrawer(t, "counted", "facts", "general", "the drawer body", "facts", "2026-08-18")

	text, isErr := h.callToolRaw(t, "vp_refresh_index", map[string]any{
		"project": "counted",
	})
	if isErr {
		t.Fatalf("refresh of a project WITH a store was refused: %s", text)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, text)
	}
	if got["status"] != "rebuilt" {
		t.Errorf("status = %v, want rebuilt", got["status"])
	}
	if got["had_palace_store"] != true {
		t.Errorf("had_palace_store = %v, want true", got["had_palace_store"])
	}
	// The counts are the point: without them "rebuilt" is unfalsifiable.
	drawers, ok := got["drawers"].(float64)
	if !ok || drawers < 1 {
		t.Errorf("drawers = %v, want >= 1 — a real rebuild must be distinguishable from a no-op", got["drawers"])
	}
	if _, ok := got["indexed"]; !ok {
		t.Errorf("result carries no indexed count: %s", text)
	}
}
