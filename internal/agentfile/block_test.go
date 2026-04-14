// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package agentfile

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestBlockContentHashStable(t *testing.T) {
	// Content hash must be deterministic and dependent on blockBody; two
	// calls return the same value, and it's a 7-hex-char prefix.
	h1 := blockContentHash()
	h2 := blockContentHash()
	if h1 != h2 {
		t.Fatalf("hash non-deterministic: %q vs %q", h1, h2)
	}
	if !regexp.MustCompile(`^[0-9a-f]{7}$`).MatchString(h1) {
		t.Errorf("hash %q does not match 7-hex-char format", h1)
	}
}

func TestBlockOpenDelimFormat(t *testing.T) {
	d := blockOpenDelim()
	want := regexp.MustCompile(`^<!-- vibe-palace:begin v=\d+ sha=[0-9a-f]{7} -->$`)
	if !want.MatchString(d) {
		t.Errorf("open delim %q does not match expected format", d)
	}
}

func TestManagedBlockStructure(t *testing.T) {
	b := managedBlock()
	if !strings.HasPrefix(b, "<!-- vibe-palace:begin ") {
		t.Errorf("block missing opening delim: %q", b)
	}
	if !strings.HasSuffix(b, blockCloseDelim) {
		t.Errorf("block missing closing delim: %q", b)
	}
	if !strings.Contains(b, "vp_bootstrap_context") {
		t.Error("block body missing bootstrap_context reference")
	}
	if !strings.Contains(b, "vpc-<name>") {
		t.Error("block body missing vpc-<name> pattern")
	}
	if !strings.Contains(b, "vps-<name>") {
		t.Error("block body missing vps-<name> pattern")
	}
	// Canonical tool names must appear exactly as advertised by the
	// exported constants — this is the drift guard paired with the
	// matching assertion in internal/tools/context_tools_test.go.
	if !strings.Contains(b, "`"+CommandToolName+"`") {
		t.Errorf("block body missing canonical command tool name %q", CommandToolName)
	}
	if !strings.Contains(b, "`"+SkillToolName+"`") {
		t.Errorf("block body missing canonical skill tool name %q", SkillToolName)
	}
	// v2 skill-lifetime contract tokens. These are the load-bearing
	// words that teach the model the full `vps-*` posture protocol.
	// If the block body is reworded, each of these must still appear
	// verbatim somewhere in the managed block.
	for _, token := range []string{
		"STANDING",
		"vps-clear",
		"vps-replace:",
		"model-parsed",
	} {
		if !strings.Contains(b, token) {
			t.Errorf("block body missing required v2 token %q", token)
		}
	}
}

// TestBlockVersionIsV2 ensures the schema version is 2 — in-the-wild v1
// blocks must be detected as stale by ScanBlock/Wire and upgraded.
func TestBlockVersionIsV2(t *testing.T) {
	if blockVersion != 2 {
		t.Errorf("blockVersion = %d, want 2", blockVersion)
	}
	if !strings.Contains(blockOpenDelim(), "v=2") {
		t.Errorf("open delim missing v=2: %q", blockOpenDelim())
	}
}

// TestBlockBodyGolden pins the exact wording of the managed block. Copy
// changes must update testdata/block_body.golden deliberately; accidental
// drift fails here before it ships to every user's CLAUDE.md.
func TestBlockBodyGolden(t *testing.T) {
	golden := filepath.Join("testdata", "block_body.golden")
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(want) != blockBody {
		t.Errorf("blockBody drifted from golden fixture.\n--- got ---\n%s\n--- want ---\n%s\n(update testdata/block_body.golden if the change is intentional)", blockBody, string(want))
	}
}
