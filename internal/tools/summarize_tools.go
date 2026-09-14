// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Background summarization queue tools (vp_enqueue_iteration_summary,
// vp_trigger_summarization_drain). These are the MCP-side counterpart of
// internal/summarize's host-local queue and cmd/vp/cmd_drain.go's one-shot
// drain command: the wrap flow enqueues a cheap synchronous write here, then
// (separately) asks the server to launch a detached `vp drain summaries`
// process against whatever is queued.
//
// Both tools follow the same project/project_path shape as
// wrapstate_tools.go's vp_stamp_iter — project is optional and detected from
// project_path via resolveWrapProject, project_path is required and absolute,
// because the MCP server (vp mcp) is long-lived and its own cwd is never the
// caller's project.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
	"github.com/suykerbuyk/vibe-palace/internal/jobqueue"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	// Aliased: this package already declares a function named summarize
	// (audit_tools.go's per-dimension verdict renderer), which collides with
	// the package identifier otherwise.
	summarizequeue "github.com/suykerbuyk/vibe-palace/internal/summarize"
)

// ---------------------------------------------------------------------------
// vp_enqueue_iteration_summary
// ---------------------------------------------------------------------------

var enqueueIterationSummarySchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug. If omitted, detected from project_path."},
		"project_path": {"type": "string", "description": "Absolute path to the local project repo root. Required."},
		"iter": {"type": "integer", "minimum": 1, "description": "The iteration number to summarize (>= 1) — pass the iter_n vp_append_iteration just returned."}
	},
	"required": ["project_path", "iter"]
}`)

// EnqueueIterationSummaryTool persists a KindIteration job to the host-local
// summarization queue (internal/summarize) for a later `vp drain summaries`
// pass to pick up. It writes no vault state and needs no *storage.Vault: the
// queue lives entirely under project_path/.vibe-palace/, exactly like
// wrapstate's own project-root anchors.
func EnqueueIterationSummaryTool() mcp.Tool {
	return mcp.Tool{
		Name:     "vp_enqueue_iteration_summary",
		Mutating: true,
		Description: "Queue a background summarization job for one iteration " +
			"(internal/summarize's host-local queue under " +
			"<project_path>/.vibe-palace/summarization-queue/). Call this right " +
			"after vp_stamp_iter, in the same wrap, passing the SAME iter_n " +
			"vp_append_iteration returned. This only enqueues — it does not " +
			"summarize or drain anything itself; pair it with " +
			"vp_trigger_summarization_drain to actually process the queue. " +
			"project_path is required and must be absolute. Returns {status, " +
			"queue_path}.",
		Schema: enqueueIterationSummarySchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Project     string `json:"project"`
				ProjectPath string `json:"project_path"`
				Iter        int    `json:"iter"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.ProjectPath == "" {
				return nil, fmt.Errorf("project_path is required")
			}
			if args.Iter < 1 {
				return nil, fmt.Errorf("iter must be >= 1, got %d", args.Iter)
			}

			slug, err := resolveWrapProject(args.Project, args.ProjectPath)
			if err != nil {
				return nil, fmt.Errorf("detect project from %q: %w", args.ProjectPath, err)
			}

			if err := summarizequeue.EnqueueIterationSummary(args.ProjectPath, slug, args.Iter); err != nil {
				return nil, err
			}

			// queue_path is informational only: internal/summarize does not
			// export a path-building helper for its per-item filename (which
			// is deterministic — derived from the item's own identity, e.g.
			// "iteration-<N>.json" — precisely so a repeat enqueue of the same
			// job overwrites rather than duplicates; see queueFileName's own
			// doc comment), so this reports the QUEUE DIRECTORY the item just
			// landed in — summarize.QueueDir itself, the single source of
			// truth for this path (also used by cmd/vp/cmd_drain.go and this
			// tool's own drain-trigger handler below), rather than a second,
			// independently-maintained copy of the literal.
			return map[string]any{
				"status":     "enqueued",
				"queue_path": summarizequeue.QueueDir(args.ProjectPath),
			}, nil
		},
	}
}

// ---------------------------------------------------------------------------
// vp_trigger_summarization_drain
// ---------------------------------------------------------------------------

var triggerSummarizationDrainSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug. Accepted for shape-parity with the other wrap tools; this tool's own logic needs only project_path."},
		"project_path": {"type": "string", "description": "Absolute path to the local project repo root. Required."}
	},
	"required": ["project_path"]
}`)

// TriggerSummarizationDrainTool launches `vp drain summaries` as a detached
// background process (internal/detachlaunch) when the project's
// summarization queue is non-empty, so the caller (wrap) never blocks on
// draining it.
//
// vault is accepted for signature symmetry with its wrapstate-tool siblings
// (CollectWrapStateTool, StampIterTool, PreflightWrapTool all take
// *storage.Vault) but is not read by this handler: the check below is a
// cheap os.ReadDir scan of project_path's own queue directory, and the launched `vp
// drain summaries` subprocess resolves its own vault independently (see
// cmd/vp/cmd_drain.go). Kept rather than dropped in case a future revision
// needs a project.RequireKnownProject-style gate here.
func TriggerSummarizationDrainTool(vault *storage.Vault, launch detachlaunch.LaunchFunc) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_trigger_summarization_drain",
		Mutating: true,
		Description: "Check project_path's summarization queue " +
			"(<project_path>/.vibe-palace/summarization-queue/*.json, plus any " +
			"orphaned *.json.processing claim left by a drain that crashed " +
			"mid-job — reclaiming those requires an actual drain to run, so an " +
			"orphaned claim alone still counts as non-empty) and, if non-empty, " +
			"launch `vp drain summaries --project-path <project_path>` " +
			"as a DETACHED background process (internal/detachlaunch) so this " +
			"call returns immediately — it never waits for the drain to finish. " +
			"An empty (or missing) queue launches nothing. The launched process " +
			"holds its own single-flight lock (internal/vaultlock, an OS-level " +
			"advisory flock under .vibe-palace/.vp-locks/, released " +
			"automatically by the OS even on a crash); this tool does not " +
			"duplicate that check, so a status of \"already_running\" is reported " +
			"by the LAUNCHED PROCESS's own log, never by this call, which only " +
			"ever returns \"empty\" or \"launched\". project_path is required and " +
			"must be absolute. Returns {status: \"empty\"|\"launched\", pid?}.",
		Schema: triggerSummarizationDrainSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Project     string `json:"project"`
				ProjectPath string `json:"project_path"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.ProjectPath == "" {
				return nil, fmt.Errorf("project_path is required")
			}
			if !filepath.IsAbs(args.ProjectPath) {
				// Unlike its sibling EnqueueIterationSummaryTool (a plain
				// file write with no further consequence), this handler
				// launches a detached subprocess whose own --project-path
				// re-validation (cmd/vp/cmd_drain.go) would reject a
				// relative path — but only AFTER this call has already
				// returned {"status":"launched","pid":N} as if it
				// succeeded. The caller would see a false success while the
				// child immediately exits on its own validation error, and
				// the queued jobs would sit stuck with no visible failure
				// anywhere but the child's log. Fail fast here instead.
				return nil, fmt.Errorf("project_path must be absolute, got %q", args.ProjectPath)
			}

			// Check BOTH plain jobs and orphaned claims. A "*.json" glob alone
			// misses a queue whose only entry is a "*.json.processing" file
			// left by a drain that crashed mid-job: nothing here reclaims
			// that file directly (only jobqueue.Claim's own reclaimStale
			// sweep does, and only once a drain actually runs), so reporting
			// "empty" for it would leave the crashed job stuck until some
			// unrelated new job happened to be enqueued in the same project.
			// Launching a drain either way is cheap and idempotent — an
			// empty drain (nothing left to reclaim) just exits immediately.
			queueDir := summarizequeue.QueueDir(args.ProjectPath)
			// os.ReadDir (unlike filepath.Glob, which silently returns
			// (nil, nil) for a missing dir and also treats brackets in
			// queueDir's own path — e.g. a project directory literally named
			// "proj[1]" — as glob metacharacters rather than literal text)
			// errors with an fs.PathError wrapping ENOENT when queueDir does
			// not exist yet, which is the single most common case (a project
			// that has never queued anything). That must be treated as
			// "empty", not propagated as a tool error — this handler, unlike
			// DrainEnrichmentQueue/DrainSummarizationQueue, does not os.Stat
			// queueDir before reading it.
			entries, readDirErr := os.ReadDir(queueDir)
			if readDirErr != nil {
				if !os.IsNotExist(readDirErr) {
					return nil, fmt.Errorf("read summarization queue: %w", readDirErr)
				}
				entries = nil
			}

			var pending, claimed []string
			for _, entry := range entries {
				name := entry.Name()
				switch {
				case strings.HasSuffix(name, ".json"+jobqueue.ProcessingSuffix):
					claimed = append(claimed, filepath.Join(queueDir, name))
				case strings.HasSuffix(name, ".json"):
					pending = append(pending, filepath.Join(queueDir, name))
				}
			}

			if len(pending) == 0 && len(claimed) == 0 {
				return map[string]any{"status": "empty"}, nil
			}

			logPath := filepath.Join(args.ProjectPath, ".vibe-palace", "summarization-drain.log")
			pid, err := launch("", []string{"drain", "summaries", "--project-path", args.ProjectPath}, logPath)
			if err != nil {
				return nil, fmt.Errorf("launch drain summaries: %w", err)
			}

			return map[string]any{
				"status": "launched",
				"pid":    pid,
			}, nil
		},
	}
}
