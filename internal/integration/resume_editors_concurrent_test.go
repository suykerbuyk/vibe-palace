// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// resumeConcurrentFixture is the seeded resume.md every concurrent editor in
// TestIntegration_ResumeEditorsConcurrentDispatch contends on. It carries a
// pre-existing ### thread, the reserved ### Carried forward sub-section with one
// seeded bullet (the promote target), and a trailing section — all three must
// survive the storm intact.
const resumeConcurrentFixture = `# Resume: demo

## Open Threads

### alpha

alpha body

### Carried forward

- **promote-me** — the seeded bullet vp_carried_promote_to_task will lift into a task

## Project History

history
`

// dispatchResult is one tool call's outcome, captured off the test goroutine.
// A nil-error call (isError == false) is a PROMISE the edit landed: the whole
// point of the test is that every such promise is honored by the final file.
type dispatchResult struct {
	name    string
	slug    string
	isError bool
	text    string
}

// dispatchTool drives one tools/call through the REAL MCP stack — JSON-RPC
// message → mcp.Server.HandleMessage → registry dispatch (schema validation +
// mutating-write gate) → the registered handler — and is safe to call from a
// goroutine (it never touches *testing.T). The harness's callTool cannot be used
// here: it calls t.Fatalf, which is illegal off the test goroutine.
func dispatchTool(h *testHarness, name, slug string, args any) dispatchResult {
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return dispatchResult{name: name, slug: slug, isError: true, text: err.Error()}
	}

	msg := json.RawMessage(fmt.Sprintf(`{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": {"name": %q, "arguments": %s}
	}`, name, argsJSON))

	resp := h.Server.HandleMessage(context.Background(), msg)
	rpcResp, ok := resp.(mcplib.JSONRPCResponse)
	if !ok {
		return dispatchResult{name: name, slug: slug, isError: true, text: fmt.Sprintf("not a JSONRPCResponse: %T %+v", resp, resp)}
	}

	raw, _ := json.Marshal(rpcResp.Result)
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return dispatchResult{name: name, slug: slug, isError: true, text: fmt.Sprintf("unmarshal result: %v (raw=%s)", err, raw)}
	}
	out := dispatchResult{name: name, slug: slug, isError: result.IsError}
	if len(result.Content) > 0 {
		out.text = result.Content[0].Text
	}
	return out
}

// TestIntegration_ResumeEditorsConcurrentDispatch is the full-stack acceptance
// test for the resume.md lost-update fix (see ADR-003, "The resume.md
// lost-update hole").
//
// The six surgical editors (vp_thread_insert/_replace/_remove,
// vp_carried_add/_remove/_promote_to_task) used to do a LOCK-FREE
// read→modify→write of Projects/<p>/resume.md — bare os.ReadFile → mdutil →
// atomicfile.Write — while storage.WriteResume took a vaultlock on the same
// path. An advisory flock only serializes writers that opt in, so the lock
// bought nothing: concurrent editors read the same base and each renamed its own
// whole-file body over the target, silently discarding the others. Every lost
// edit still returned a nil error and a bytes_written payload.
//
// Unlike the package-level unit tests, which call the tool value's Handler
// directly, this drives every call through the PRODUCTION WIRING: tools.RegisterAll
// on a real mcp.Server, a real JSON-RPC tools/call message, the registry's
// schema validation and mutating-write gate, and a real on-disk vault. That is
// the path an agent's concurrent wrap actually takes (mcp-go dispatches tool
// calls on a worker pool), so it is the path the fix must hold on.
//
// N=32 concurrent calls model a realistic wrap: thread inserts, carried-forward
// adds, and one promote-to-task (which touches TWO locked files — the task file
// and resume.md — sequentially, never nested). The assertion is on the FINAL
// ON-DISK FILE: every call that returned a nil error must appear exactly once,
// and nothing pre-existing may be clobbered.
//
// Two traps worth naming:
//   - The slug indices are ZERO-PADDED (thread-000..). Unpadded, "thread-1" is a
//     substring of "thread-10".."thread-19" and both the edit and the counting
//     below break silently.
//   - `-race` CANNOT see this bug. A file-level lost update is not a memory data
//     race; the final file content is the only detector. This test therefore must
//     NOT be skipped under -short (it spawns no subprocesses) — `make test` runs
//     -short, and a skipped regression test guards nothing.
func TestIntegration_ResumeEditorsConcurrentDispatch(t *testing.T) {
	const (
		project      = "demo"
		nThreads     = 29
		nCarriedAdds = 2
		nPromotes    = 1
		n            = nThreads + nCarriedAdds + nPromotes // 32 concurrent tool calls
	)

	h := newHarness(t, false)
	h.registerAllTools(t)
	h.initMCP(t) // handshake once, on the test goroutine

	if err := h.Vault.WriteResume(project, resumeConcurrentFixture); err != nil {
		t.Fatalf("seed resume.md: %v", err)
	}
	resumePath, err := h.Vault.ResumeFile(project)
	if err != nil {
		t.Fatalf("ResumeFile: %v", err)
	}

	// Fixed-width, zero-padded slugs: no slug is a substring of another.
	threadSlug := func(i int) string { return fmt.Sprintf("thread-%03d", i) }
	carriedSlug := func(i int) string { return fmt.Sprintf("carried-%03d", i) }

	results := make([]dispatchResult, n)
	var wg sync.WaitGroup

	for i := range nThreads {
		wg.Go(func() {
			results[i] = dispatchTool(h, "vp_thread_insert", threadSlug(i), map[string]any{
				"project":  project,
				"position": map[string]any{"mode": "bottom"},
				"slug":     threadSlug(i),
				"body":     "body of " + threadSlug(i),
			})
		})
	}
	for i := range nCarriedAdds {
		idx := nThreads + i
		wg.Go(func() {
			results[idx] = dispatchTool(h, "vp_carried_add", carriedSlug(i), map[string]any{
				"project": project,
				"slug":    carriedSlug(i),
				"title":   "carried item " + carriedSlug(i),
			})
		})
	}
	promoteIdx := nThreads + nCarriedAdds
	wg.Go(func() {
		results[promoteIdx] = dispatchTool(h, "vp_carried_promote_to_task", "promote-me", map[string]any{
			"project":       project,
			"slug":          "promote-me",
			"new_task_slug": "promoted-task",
		})
	})
	wg.Wait()

	data, err := os.ReadFile(resumePath)
	if err != nil {
		t.Fatalf("read final resume.md: %v", err)
	}
	final := string(data)

	// THE INVARIANT: a nil-error tool call must be visible in the final file.
	lost := 0
	for _, r := range results {
		if r.isError {
			// A refusal is allowed (nothing was promised); a lost update is not.
			t.Errorf("%s(%s) returned an error: %s", r.name, r.slug, r.text)
			continue
		}
		var marker string
		switch r.name {
		case "vp_thread_insert":
			marker = "### " + r.slug
		case "vp_carried_add":
			marker = "**" + r.slug + "**"
		case "vp_carried_promote_to_task":
			// The promoted bullet must be GONE from the carried list — its
			// removal is the edit, and losing it means the bullet is back while
			// the task exists.
			if strings.Contains(final, "**promote-me**") {
				lost++
				t.Errorf("lost update: vp_carried_promote_to_task reported success but the promote-me bullet is still in the final content")
			}
			continue
		}
		switch got := strings.Count(final, marker); got {
		case 1:
			// survived
		case 0:
			lost++
			t.Errorf("lost update: %s(%s) reported success but %q is missing from the final content", r.name, r.slug, marker)
		default:
			t.Errorf("duplicate: %s(%s) — %q appears %d times in the final content", r.name, r.slug, marker, got)
		}
	}
	if lost > 0 {
		t.Errorf("%d of %d concurrent edits through the MCP registry dispatch were lost", lost, n)
	}

	// The promote's OTHER locked file: the task must exist, created through
	// storage.CreateTask (task lock taken and released) before the resume lock
	// was ever acquired.
	taskPath, err := h.Vault.TaskFile(project, "promoted-task")
	if err != nil {
		t.Fatalf("TaskFile: %v", err)
	}
	if _, err := os.Stat(taskPath); err != nil {
		t.Errorf("promoted task file missing at %s: %v", taskPath, err)
	}

	// Nothing pre-existing may be clobbered: the seeded thread, the reserved
	// sub-section, and the trailing section all survive.
	for _, want := range []string{"### alpha", "### Carried forward", "## Project History", "history"} {
		if !strings.Contains(final, want) {
			t.Errorf("pre-existing content %q was clobbered:\n%s", want, final)
		}
	}
	if got := strings.Count(final, "### Carried forward"); got != 1 {
		t.Errorf("### Carried forward appears %d times, want 1", got)
	}
}
