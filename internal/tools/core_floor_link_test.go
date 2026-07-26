// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"strconv"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
)

// TestCoreFloorMatchesBootstrapBudget pins the advisory core-floor threshold
// (check.CoreMaxBytes) to the budget it is DERIVED from, so the two cannot drift
// apart silently:
//
//	DefaultBootstrapMaxTokens × 4 bytes/token − CoreContextReserveBytes
//
// This lives here, not in internal/check, because check must not import tools —
// this very file gives tools an edge to check, and the reverse edge would cycle.
//
// It replaced TestWorkflowCapMirrorsExcerptCap at iteration 260. That test pinned
// a workflow-ONLY cap to bootstrapExcerptCap, which stopped meaning anything once
// the budget rose above the core floor: the ladder rarely reaches the workflow
// rung now, so the old advisory fired on contracts that were no longer causing
// harm. What must fit is the core TOGETHER — a lean resume affords a fat contract
// and vice versa — so a workflow-only cap was never derivable on its own.
func TestCoreFloorMatchesBootstrapBudget(t *testing.T) {
	const bytesPerToken = 4
	want := DefaultBootstrapMaxTokens*bytesPerToken - check.CoreContextReserveBytes
	if check.CoreMaxBytes != want {
		t.Fatalf("check.CoreMaxBytes = %d, but DefaultBootstrapMaxTokens(%d)×%d − CoreContextReserveBytes(%d) = %d — "+
			"the advisory cap is derived from the budget and must move with it",
			check.CoreMaxBytes, DefaultBootstrapMaxTokens, bytesPerToken, check.CoreContextReserveBytes, want)
	}
}

// TestBootstrapSchemaAdvertisesTheRealDefault keeps the tool schema's documented
// default honest. The description is prose in a JSON literal, so nothing but a
// test stops it saying 8000 forever after the constant moves — which is exactly
// how the budget came to be four unsynchronised literals in the first place.
func TestBootstrapSchemaAdvertisesTheRealDefault(t *testing.T) {
	want := strconv.Itoa(DefaultBootstrapMaxTokens)
	if !strings.Contains(string(bootstrapSchema), "Default: "+want) {
		t.Errorf("vp_bootstrap_context schema does not advertise the real default %q — "+
			"an agent reading the schema would size its request against a stale number", want)
	}
}
