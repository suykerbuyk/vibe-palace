// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
)

// TestWorkflowCapMirrorsExcerptCap pins the advisory workflow-caps threshold
// (check.WorkflowMaxBytes) to the shed ladder's bootstrapExcerptCap — the
// bound the cap is DERIVED from: a workflow at or under bootstrapExcerptCap
// can never be excerpted by the "workflow->excerpt" rung, so it ships whole
// on every rung (ADR-009 by construction). If either constant moves without
// the other, the advisory would fire on a safe size or stay silent on an
// excerptable one; this test makes that drift a build break instead.
func TestWorkflowCapMirrorsExcerptCap(t *testing.T) {
	if check.WorkflowMaxBytes != bootstrapExcerptCap {
		t.Fatalf("check.WorkflowMaxBytes = %d, bootstrapExcerptCap = %d — the advisory cap is derived from the excerpt bound and must move with it",
			check.WorkflowMaxBytes, bootstrapExcerptCap)
	}
}
