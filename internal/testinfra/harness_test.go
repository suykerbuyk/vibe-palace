// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testinfra

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
)

// TestHarnessRoundTrip is the new regression anchor called for by the plan:
// the harness's contract has never been tested from outside
// internal/integration before this relocation. It is not an isolation test
// (the harness sets zero env vars; that lands in a later phase) — it is proof
// the relocated package works standalone: construct, register tools, init the
// MCP handshake, seed a project, and drive one real tool call end to end.
func TestHarnessRoundTrip(t *testing.T) {
	h := NewHarnessWithEmbedder(t, embedder.NewMock(384))
	h.RegisterAllTools(t)

	if h.MCPReady() {
		t.Fatal("MCPReady() = true before InitMCP")
	}
	h.InitMCP(t)
	if !h.MCPReady() {
		t.Fatal("MCPReady() = false after InitMCP")
	}

	h.SeedProject(t, "roundtrip-project")

	body := TaskBody("Prove the relocated harness drives a real tool call end to end.")
	text := h.CallTool(t, "vp_manage_task", map[string]any{
		"action":  "create",
		"project": "roundtrip-project",
		"task":    "roundtrip-task",
		"content": body,
	})
	if !strings.Contains(text, "roundtrip-task") {
		t.Errorf("create result missing task slug:\n%s", text)
	}
}

// TestHarnessCallToolRawLazyInitsMCP pins CallToolRaw's own lazy-init check
// (relocated verbatim): a harness that never called InitMCP explicitly must
// still be MCP-ready by the time CallToolRaw returns.
func TestHarnessCallToolRawLazyInitsMCP(t *testing.T) {
	h := NewHarnessWithEmbedder(t, embedder.NewMock(384))
	h.RegisterAllTools(t)

	if h.MCPReady() {
		t.Fatal("MCPReady() = true before any call")
	}
	// The project is deliberately unknown: even a refused call must still run
	// the initialize handshake lazily before CallToolRaw returns.
	_, _ = h.CallToolRaw(t, "vp_manage_task", map[string]any{
		"action":  "create",
		"project": "no-such-project",
		"task":    "whatever",
		"content": TaskBody("Any refusal still runs the handshake lazily."),
	})
	if !h.MCPReady() {
		t.Fatal("MCPReady() = false after CallToolRaw — lazy-init did not run")
	}
}

// TestSentinelPATHDerivesNonEmptyNameSetAndWins mirrors cmd/vp's own
// "vacuous lock" guard (cmd_check_test.go's TestFullCheckExecsNoAgentCLI):
// SentinelPATH's derived name set must be non-empty, and every derived name
// must resolve via exec.LookPath to the sentinel directory, not a real binary
// that happens to be installed on the machine running the suite. Also pins
// the review-flagged fix: "cursor" (not just "cursor-agent") must be in the
// fixed agent-CLI list.
func TestSentinelPATHDerivesNonEmptyNameSetAndWins(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sentinels are sh scripts; windows is out of scope")
	}

	logPath := SentinelPATH(t)
	if logPath == "" {
		t.Fatal("SentinelPATH returned an empty log path")
	}

	found := false
	for _, name := range fixedAgentCLIs {
		if name == "cursor" {
			found = true
		}
	}
	if !found {
		t.Fatal(`fixedAgentCLIs is missing "cursor"`)
	}

	for _, name := range fixedAgentCLIs {
		resolved, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("LookPath(%q): %v", name, err)
		}
		dir := filepath.Dir(resolved)
		if _, err := os.Stat(filepath.Join(dir, "sentinel.log")); err == nil {
			t.Fatalf("LookPath(%q) resolved into the log's own directory, not a sentinel bin dir", name)
		}
		if _, err := exec.Command(resolved).CombinedOutput(); err == nil {
			t.Fatalf("sentinel %q exited 0, want nonzero (real binaries would differ)", name)
		}
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read sentinel log: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("sentinel log is empty after running every derived name")
	}
	for _, name := range fixedAgentCLIs {
		if !strings.Contains(string(raw), name+"|") {
			t.Errorf("sentinel log missing an entry for %q:\n%s", name, raw)
		}
	}
}
