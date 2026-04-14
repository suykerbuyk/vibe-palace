// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
)

// TestIntegrationAgentfileUpgradeV1ToV2 is the Phase 3 integration proof:
//
//  1. Seed a project with a CLAUDE.md that has user-authored content both
//     above and below a v1 managed block.
//  2. Run the real `vp` binary's `init` pass against the project.
//  3. Assert (a) the managed block body equals the v2 golden, (b) the
//     user-authored content above and below the block is byte-identical
//     to the input, (c) a second `vp init` produces no further change to
//     the block (Unchanged on re-scan).
//
// The point of going through the built binary (rather than calling
// agentfile.Wire directly) is to exercise the same write path users hit:
// detection → legacy-drift check → Wire → atomic rename.
func TestIntegrationAgentfileUpgradeV1ToV2(t *testing.T) {
	bin := buildVPBinary(t)
	env := setupFreshEnv(t)

	// Construct a v1 block using an obviously-old sha. The exact sha is
	// irrelevant; what matters is that it differs from the current v2
	// sha so Wire classifies it as Updated rather than Unchanged.
	above := "# Project CLAUDE.md\n\n" +
		"User-authored intro paragraph.\n\n" +
		"- bullet one\n- bullet two\n\n"
	v1Block := "<!-- vibe-palace:begin v=1 sha=0000001 -->\n" +
		"## Vibe-Palace Integration\n\n" +
		"Legacy v1 body — must not survive upgrade.\n" +
		"<!-- vibe-palace:end -->"
	below := "\n\n## House rules\n\n" +
		"User-authored trailing content.\n" +
		"Final line without trailing newline."
	seed := above + v1Block + below

	claudePath := filepath.Join(env.projectDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed CLAUDE.md: %v", err)
	}

	// Drive through the real binary.
	runVP(t, bin, env, nil,
		"init", env.projectDir,
		"--name", env.projectName,
		"--vault-path", env.vaultPath,
		"--no-git")

	got, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("re-read CLAUDE.md: %v", err)
	}
	s := string(got)

	// (a) The managed block body in-place must equal the v2 golden.
	goldenPath := filepath.Join("..", "agentfile", "testdata", "block_body.golden")
	goldenBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !strings.Contains(s, string(goldenBytes)) {
		t.Errorf("upgraded CLAUDE.md missing v2 golden block body\n--- file ---\n%s\n--- golden ---\n%s", s, string(goldenBytes))
	}
	// And the new block carries v=2 with the current sha.
	if !strings.Contains(s, "v=2") {
		t.Errorf("upgraded block should carry v=2; file:\n%s", s)
	}
	if !strings.Contains(s, "sha="+agentfile.ExpectedSha()) {
		t.Errorf("upgraded block sha != ExpectedSha() %q; file:\n%s", agentfile.ExpectedSha(), s)
	}
	if strings.Contains(s, "Legacy v1 body") {
		t.Error("legacy v1 body leaked past upgrade")
	}

	// (b) Byte-identical preservation of content above and below the block.
	if !strings.HasPrefix(s, above) {
		t.Errorf("ABOVE content not byte-identical to input\n--- got prefix ---\n%q\n--- want prefix ---\n%q",
			s[:min(len(s), len(above))], above)
	}
	if !strings.HasSuffix(s, below) {
		t.Errorf("BELOW content not byte-identical to input\n--- got suffix ---\n%q\n--- want suffix ---\n%q",
			s[max(0, len(s)-len(below)):], below)
	}

	// (c) Second `vp init` must be a no-op for the block. We check by
	// comparing byte-for-byte before/after.
	before := string(got)
	runVP(t, bin, env, nil,
		"init", env.projectDir,
		"--name", env.projectName,
		"--vault-path", env.vaultPath,
		"--no-git")
	after, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("re-read after 2nd init: %v", err)
	}
	if string(after) != before {
		t.Errorf("second `vp init` mutated CLAUDE.md\n--- before ---\n%s\n--- after ---\n%s", before, string(after))
	}
}
