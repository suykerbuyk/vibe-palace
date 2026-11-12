// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

// chairFrameBudget is the ceiling for the whole vp_skill result of the chair
// persona (frame header, body, footer and reference list). It is a budget this
// project chose to keep the persona under a host's inline cap, not a measured
// host constant: the one recorded cap is 19,968 B on a Grok pane, and Claude
// Code's threshold is unrecorded (see bootstrap_wire_order_test.go).
const chairFrameBudget = 17000

// TestChairSkill_FrameUnderBudget renders the exact frame vp_skill returns for
// chair and holds it under the budget, so the persona cannot silently grow
// back into an unrenderable monolith.
func TestChairSkill_FrameUnderBudget(t *testing.T) {
	r := vpctx.NewResolver("")
	sd, source, err := r.ResolveSkillDir("chair", "", "", "")
	if err != nil {
		t.Fatalf("ResolveSkillDir(chair): %v", err)
	}
	frame := buildSkillFrame(frameParams{
		Name:         "chair",
		Project:      "vibe-palace",
		Source:       source,
		Content:      string(sd.SkillMDBody),
		ResourceType: "skill",
	}, sd.ReferenceNames)
	if n := len(frame); n > chairFrameBudget {
		t.Fatalf("chair vp_skill frame = %d bytes, budget %d: move text into skills/chair/references/", n, chairFrameBudget)
	} else {
		t.Logf("chair vp_skill frame = %d bytes (budget %d)", n, chairFrameBudget)
	}
}
