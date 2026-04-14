// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package agentfile

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRegenerateGolden is a gated generator. When VP_REGEN_GOLDEN=1, it
// writes testdata/block_body.golden from the live blockBody byte-for-byte.
// Keep it in the test binary so the golden can always be mechanically
// regenerated without hand-editing.
func TestRegenerateGolden(t *testing.T) {
	if os.Getenv("VP_REGEN_GOLDEN") == "" {
		t.Skip("set VP_REGEN_GOLDEN=1 to regenerate testdata/block_body.golden")
	}
	p := filepath.Join("testdata", "block_body.golden")
	if err := os.WriteFile(p, []byte(blockBody), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}
