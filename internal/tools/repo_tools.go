// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// repoFreshnessDefaultSubjectLimit caps newest_upstream_subjects per remote
// when the caller does not specify subject_limit.
const repoFreshnessDefaultSubjectLimit = 5

type repoFreshnessParams struct {
	ProjectPath  string `json:"project_path"`
	Remote       string `json:"remote,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Fetch        *bool  `json:"fetch,omitempty"`
	SubjectLimit *int   `json:"subject_limit,omitempty"`
}

var repoFreshnessSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project_path": {
			"type": "string",
			"description": "Absolute path to the project's git checkout to check. REQUIRED — this tool never infers a cwd, because a multiplexed MCP server (vp mcp serve) has no single caller cwd and even a per-client stdio server may not be running from the checkout the operator means."
		},
		"remote": {
			"type": "string",
			"description": "Check only this remote. Default: the branch's configured upstream (branch.<branch>.remote) if set, else every configured remote that has a matching branch, worst-of."
		},
		"branch": {
			"type": "string",
			"description": "Branch to check. Default: the repo's current branch."
		},
		"fetch": {
			"type": "boolean",
			"description": "Perform a bounded per-remote 'git fetch' so behind counts are real (default true). Unlike vp_vault_status's refresh (default false), this tool's whole purpose is answering \"is local behind upstream\", and that question is unanswerable from a cached tracking ref or ls-remote alone — pass fetch:false only for a fast ahead-only reachability probe, and expect status \"unverified\" rather than a real behind verdict. It never commits, pushes, or mutates the working tree; a fetch only updates .git tracking refs."
		},
		"subject_limit": {
			"type": "integer",
			"description": "Max newest-upstream-only commit subjects to return per remote when behind (default 5). 0 skips the extra 'git log' call."
		}
	},
	"required": ["project_path"]
}`)

// RepoFreshnessTool returns the MCP tool definition for vp_repo_freshness.
func RepoFreshnessTool() mcp.Tool {
	return mcp.Tool{
		Name:     "vp_repo_freshness",
		Mutating: false,
		Description: "Read-only report of how a project git checkout compares to its configured remote(s) — " +
			"the project-repo analogue of vp_vault_status, for a checkout that is not the vault. Reports per-remote " +
			"ahead/behind/diverged/reachable plus the newest commit subjects unique to upstream when behind, and a " +
			"single status: \"up_to_date\", \"ahead\", \"behind\", \"diverged\", or \"unverified\". An unreachable " +
			"remote, a repo with no configured remote, or a fetch:false call is reported as \"unverified\" — NEVER " +
			"as \"up_to_date\": a remote-tracking ref is a cache of the remote, not the remote itself, and behind can " +
			"only be known from a real fetch. It never commits, pushes, or mutates the working tree.",
		Schema:  repoFreshnessSchema,
		Handler: repoFreshnessHandler,
	}
}

func repoFreshnessHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p repoFreshnessParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	path := strings.TrimSpace(p.ProjectPath)
	if path == "" {
		return nil, fmt.Errorf("project_path is required")
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("project_path %q: does not exist or is not a directory", path)
	}

	fetch := true
	if p.Fetch != nil {
		fetch = *p.Fetch
	}
	subjectLimit := repoFreshnessDefaultSubjectLimit
	if p.SubjectLimit != nil {
		subjectLimit = *p.SubjectLimit
	}

	rf, err := storage.CheckRepoFreshness(path, p.Remote, p.Branch, fetch, subjectLimit)
	if err != nil {
		return nil, fmt.Errorf("repo freshness: %w", err)
	}
	return rf, nil
}
