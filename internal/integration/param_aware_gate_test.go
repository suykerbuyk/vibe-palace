// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// surfaceRemediation is a fragment of *surface.IncompatibleError's message. It
// is the discriminator this file needs: a read-only invocation admitted by the
// gate still fails on its own terms in a bare temp vault (there is no git repo
// to pull from), so "did it pass the GATE" cannot be read off success alone —
// it is read off the absence of THIS refusal.
const surfaceRemediation = "this binary supports MCP surface v"

// aheadHarness builds the full MCP stack over a vault stamped ONE SURFACE
// VERSION AHEAD of this binary — the vault-written-by-a-newer-vp condition the
// gate exists for, and the condition under which all three of these tools were
// refused whole before the gate became param-aware.
func aheadHarness(t *testing.T) *testHarness {
	t.Helper()
	h := newHarness(t, false)
	h.registerAllTools(t)
	stampDir := filepath.Join(h.Vault.Root, "Projects", "p")
	if err := surface.WriteStamp(stampDir, surface.MCPSurfaceVersion+1, "tester"); err != nil {
		t.Fatalf("stage ahead stamp: %v", err)
	}
	return h
}

// TestIntegrationParamAwareSurfaceGate is the verification bar, end to end:
// for each of the three multiplexing tools, over the REAL tools/call path,
// against a vault this binary may not write —
//
//   - the read-only invocation PASSES the gate, and
//   - the writing invocation of the SAME tool is STILL REFUSED.
//
// 🔴 THE SECOND HALF IS WHAT MAKES THE FIRST MEAN ANYTHING. A test asserting
// only that the read passes would pass with the surface gate deleted outright.
// Each subtest below asserts both against one vault, in one harness.
func TestIntegrationParamAwareSurfaceGate(t *testing.T) {
	cases := []struct {
		tool string
		// readOnly is the invocation that must reach its handler.
		readOnly map[string]any
		// writing is the invocation that must still be refused.
		writing map[string]any
		// why documents what the writing payload would have done, so a future
		// reader can tell whether a change to the handler invalidates the row.
		why string
	}{
		{
			tool:     "vp_vault_sync",
			readOnly: map[string]any{"action": "pull"},
			writing:  map[string]any{"action": "push"},
			why:      "push commits and pushes vault history",
		},
		{
			// The paths list, not the action, is what routes vault_sync to a
			// commit — so this row proves the predicate reads BOTH. Drop the
			// len(paths) check from vaultSyncReadOnly and this is the subtest
			// that goes red.
			tool:     "vp_vault_sync",
			readOnly: map[string]any{"action": "pull"},
			writing:  map[string]any{"action": "pull", "paths": []string{"Projects/p/notes.md"}, "message": "m"},
			why:      "a paths list commits regardless of the action",
		},
		{
			tool:     "vp_vault_tidy",
			readOnly: map[string]any{"dry_run": true},
			writing:  map[string]any{"dry_run": false},
			why:      "a non-dry tidy commits capture artifacts and pushes",
		},
		{
			tool:     "vp_audit_vault",
			readOnly: map[string]any{"write": false},
			writing:  map[string]any{"write": true},
			why:      "write:true persists a report through atomicfile.Write and stamps the surface",
		},
	}

	for _, tc := range cases {
		t.Run(tc.tool+"/"+tc.why, func(t *testing.T) {
			h := aheadHarness(t)

			// Read-only direction: whatever the handler goes on to do, the
			// SURFACE GATE must not be what stopped it.
			text, _ := h.callToolRaw(t, tc.tool, tc.readOnly)
			if strings.Contains(text, surfaceRemediation) {
				t.Errorf("%s %v: refused by the surface gate, but this invocation writes nothing:\n%s",
					tc.tool, tc.readOnly, text)
			}

			// Writing direction: the gate must still refuse, in-band, with the
			// remediation the caller needs.
			text, isErr := h.callToolRaw(t, tc.tool, tc.writing)
			if !isErr {
				t.Fatalf("%s %v: must be refused against a vault ahead of this binary (%s), got success:\n%s",
					tc.tool, tc.writing, tc.why, text)
			}
			if !strings.Contains(text, surfaceRemediation) {
				t.Errorf("%s %v: refused for the wrong reason — want the surface remediation, got:\n%s",
					tc.tool, tc.writing, text)
			}
		})
	}
}

// TestIntegrationParamAwareGateDoesNotLeakToOtherTools bounds the blast radius.
// The three predicates are per-tool; every OTHER mutating tool must still be
// refused whole against an ahead vault, whatever its parameters look like. In
// particular a tool that happens to take a `write`, `action` or `dry_run`
// parameter must not inherit somebody else's predicate.
func TestIntegrationParamAwareGateDoesNotLeakToOtherTools(t *testing.T) {
	h := aheadHarness(t)

	cases := []struct {
		tool string
		args map[string]any
	}{
		{"vp_vault_write", map[string]any{"path": "Projects/p/notes.md", "content": "x"}},
		{"vp_capture_session", map[string]any{"project": "p", "summary": "x"}},
		{"vp_manage_task", map[string]any{"action": "create", "project": "p", "task": "t", "content": "## Plan\n\nx\n"}},
		// Shaped like a read-only invocation of one of the three, on a tool that
		// declares no predicate: the discriminator names must not travel.
		{"vp_memory_write", map[string]any{
			"project": "p", "rel": "n.md", "name": "n", "type": "project", "body": "b",
			"dry_run": true, "write": false, "action": "pull",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			text, isErr := h.callToolRaw(t, tc.tool, tc.args)
			if !isErr || !strings.Contains(text, surfaceRemediation) {
				t.Errorf("%s must still be refused whole by the surface gate (isErr=%v):\n%s", tc.tool, isErr, text)
			}
		})
	}
}
