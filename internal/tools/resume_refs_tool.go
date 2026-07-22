// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Resume-refs tool (vp_check_resume_refs). A read-only probe that reports which
// committed <vault>/Projects/*/resume.md files carry a host-local plan
// reference — a home-relative `~/.claude/plans/…` or an absolute
// `/…/.claude/plans/…` path. Such a path is dead weight in a shared, committed
// artifact: it does not resolve on any other host and leaks a local home
// layout. It lets command templates run the check host-agnostically
// (Claude/Grok/Zed) instead of shelling out to `vp check --check resume-refs`.
//
// All verdict logic lives in internal/check (CheckResumeRefs); this layer only
// resolves the vault root and marshals the check.Result into a clean,
// self-describing JSON struct. It never writes.

package tools

import (
	"context"
	"encoding/json"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// ResumeRefsResult is the JSON output of vp_check_resume_refs. It is a minimal,
// self-describing projection of check.Result: status is the lower-cased verdict
// (pass/info/skip), and details enumerates each offending "project: line N:
// <path>" breach (populated on info).
type ResumeRefsResult struct {
	Status  string   `json:"status"`            // "pass" | "info" | "skip"
	Summary string   `json:"summary"`           // one-line verdict
	Details []string `json:"details,omitempty"` // one line per offending host-local ref
}

var resumeRefsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug. Accepted for parity with other project-scoped read tools; the resume-refs scan always covers every Projects/*/resume.md in the vault, so this value does not narrow the verdict."}
	}
}`)

// resumeRefsStatusString maps a check.Status to the lower-cased verdict string
// used in the tool's JSON output. CheckResumeRefs only ever returns
// Pass/Info/Skip — never Fail.
func resumeRefsStatusString(s check.Status) string {
	switch s {
	case check.Info:
		return "info"
	case check.Skip:
		return "skip"
	default:
		return "pass"
	}
}

// ResumeRefsTool exposes the resume host-local-plan-reference scan as a
// non-mutating MCP tool.
func ResumeRefsTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_check_resume_refs",
		Description: "Read-only lint: report which committed Projects/*/resume.md files " +
			"carry a host-local plan reference — a home-relative `~/.claude/plans/…` " +
			"or an absolute `/…/.claude/plans/…` path — which is unresolvable on any " +
			"other host and leaks a local home layout into the shared vault. " +
			"Fence-aware (paths inside a Markdown code fence are ignored). Returns " +
			"{status (pass|info|skip), summary, details[] (one `project: line N: <path>` " +
			"per breach)}. This is advisory: status is never \"fail\".",
		Schema: resumeRefsSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Project string `json:"project"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}

			// The scan is whole-vault: CheckResumeRefs walks every
			// Projects/*/resume.md. The project arg does not narrow it.
			r := check.CheckResumeRefs(vault)

			return ResumeRefsResult{
				Status:  resumeRefsStatusString(r.Status),
				Summary: r.Summary,
				Details: r.Details,
			}, nil
		},
	}
}
