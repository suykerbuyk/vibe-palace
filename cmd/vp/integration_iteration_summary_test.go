// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/summarize"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// TestEnqueueThenDrain_EndToEnd is Phase 6's true end-to-end integration
// proof. Every other test in this package either calls runDrainSummaries/
// runSummarizeIterations directly, or manually seeds a queue-directory JSON
// file by hand (see cmd_drain_test.go's own
// TestRunDrainSummaries_BracketedProjectPathProcessesQueuedJob, which calls
// summarize.EnqueueIterationSummary directly). NONE of them go through the
// REAL, actual production job-CREATION path an operator/agent would trigger:
//
//  1. vp_enqueue_iteration_summary (the MCP tool,
//     internal/tools/summarize_tools.go's EnqueueIterationSummaryTool),
//     invoked here by calling its Handler directly with JSON-marshaled args
//     — exactly what the MCP server's own dispatch does, minus the transport
//     — NOT summarize.EnqueueIterationSummary called directly and NOT a
//     hand-written queue file.
//  2. The job then sits in the real host-local queue directory exactly as
//     production creates it.
//  3. vp drain summaries (runDrainSummaries, the real CLI entrypoint body,
//     now wired with a real config-driven summarize.DispatchSummarizer)
//     claims and processes it.
//
// This is the proof that vp_enqueue_iteration_summary + vp drain summaries,
// wired through DispatchSummarizer, actually works end-to-end as a real
// operator/agent would trigger it — not just that the individual pieces work
// in isolation.
func TestEnqueueThenDrain_EndToEnd(t *testing.T) {
	f := newSummarizeTestFixture(t)
	f.writeIterations(t, "## Iteration 1 — First\n\nDid the first thing.\n")

	// Step 1: the REAL enqueue path. EnqueueIterationSummaryTool's Handler is
	// an mcp.Tool.Handler (func(context.Context, json.RawMessage) (any,
	// error)) — called directly here with marshaled args, without spinning up
	// a real MCP server, exactly as internal/tools/summarize_tools_test.go's
	// own TestEnqueueIterationSummaryTool_Success does, but against this
	// file's fixture project (a real iterations.md entry + a resolvable,
	// enabled [summarization] config) rather than an empty temp dir.
	tool := tools.EnqueueIterationSummaryTool()
	params, err := json.Marshal(map[string]any{
		"project":      f.slug,
		"project_path": f.projectPath,
		"iter":         1,
	})
	if err != nil {
		t.Fatalf("marshal enqueue args: %v", err)
	}
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("EnqueueIterationSummaryTool handler: %v", err)
	}
	out, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("handler result type = %T, want map[string]any", res)
	}
	if out["status"] != "enqueued" {
		t.Fatalf("status = %v, want %q", out["status"], "enqueued")
	}

	// Step 2: confirm a real queue file landed on disk at the expected
	// location — summarize.QueueDir plus the zero-padded "iteration-<N>.json"
	// naming convention EnqueueIterationSummary itself uses (independently
	// pinned by cmd_drain_test.go's bracketed-path test and
	// summarize_tools_test.go's own glob assertion; the padding width itself
	// is pinned in internal/summarize/summarize_test.go).
	queueFile := filepath.Join(summarize.QueueDir(f.projectPath), "iteration-00001.json")
	if _, err := os.Stat(queueFile); err != nil {
		t.Fatalf("expected a real queue file at %s, got: %v", queueFile, err)
	}

	// Step 3: the REAL `vp drain summaries` CLI entrypoint body.
	// runDrainSummaries (post-Phase-5) takes no injectable Summarizer
	// parameter at all — it resolves the project's own [summarization]
	// config and builds a real summarize.DispatchSummarizer itself (see
	// cmd_drain.go), so there is nothing left for this test to inject either.
	var buf bytes.Buffer
	code := runDrainSummaries(f.projectPath, 10, &buf)
	if code != cli.ExitOK {
		t.Fatalf("runDrainSummaries: code = %d, want ExitOK; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "drained=1") {
		t.Errorf("expected drained=1 in output, got: %s", buf.String())
	}

	// The queue file must be gone: actually claimed and processed, not left
	// behind.
	if _, err := os.Stat(queueFile); !os.IsNotExist(err) {
		t.Errorf("queue file still present after drain (job was not processed): stat err = %v", err)
	}

	// Exactly one real HTTP call reached the canned httptest server — proof
	// the real IterationSummarizer ran, not a no-op dispatcher.
	if got := f.callCount(); got != 1 {
		t.Errorf("HTTP call count = %d, want 1", got)
	}

	// The vault-committed cache file now exists with the canned response's
	// content, readable through the same storage.(*Vault).ReadIterationSummary
	// path internal/search/iterations.go's collectIterationCorpus uses to
	// surface it in search results.
	got, ok, err := f.vault.ReadIterationSummary(f.slug, 1)
	if err != nil {
		t.Fatalf("ReadIterationSummary: %v", err)
	}
	if !ok {
		t.Fatal("expected an iteration summary to have been cached, found none")
	}
	if got.Summary != "Summarized." {
		t.Errorf("Summary = %q, want %q", got.Summary, "Summarized.")
	}
}
