// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// vaultSyncMu serializes concurrent vault git operations.
var vaultSyncMu sync.Mutex

// ---------------------------------------------------------------------------
// vp_init
// ---------------------------------------------------------------------------

type initParams struct {
	Path   string   `json:"path"`
	Name   string   `json:"name,omitempty"`
	Domain string   `json:"domain,omitempty"`
	Tags   []string `json:"tags,omitempty"`
}

var initSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path":   {"type": "string", "description": "Absolute path to the project directory."},
		"name":   {"type": "string", "description": "Project name slug (default: auto-detect)."},
		"domain": {"type": "string", "description": "Domain (e.g. work, personal, opensource)."},
		"tags":   {"type": "array", "items": {"type": "string"}, "description": "Project tags."}
	},
	"required": ["path"]
}`)

func InitProjectTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_init",
		Mutating:    true,
		Description: "Initialize a new vibe-palace project: create .vibe-palace.toml and vault directories.",
		Schema:      initSchema,
		Handler:     initProjectHandler(vault),
	}
}

func initProjectHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p initParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		if !filepath.IsAbs(p.Path) {
			return nil, fmt.Errorf("path must be absolute, got %q", p.Path)
		}
		if strings.Contains(p.Path, "..") {
			return nil, fmt.Errorf("path must not contain '..'")
		}

		configPath := filepath.Join(p.Path, project.ConfigFileName)
		if _, err := os.Stat(configPath); err == nil {
			return nil, fmt.Errorf("%s already exists at %s", project.ConfigFileName, p.Path)
		}

		name := p.Name
		if name == "" {
			detected, err := project.DetectProject(p.Path)
			if err == nil {
				name = detected
			} else {
				name = filepath.Base(p.Path)
			}
		}
		if err := slug.Validate(name); err != nil {
			return nil, fmt.Errorf("invalid project name %q: %w", name, err)
		}

		// Write .vibe-palace.toml.
		content := fmt.Sprintf("[project]\nname = %q\n", name)
		if p.Domain != "" {
			content += fmt.Sprintf("domain = %q\n", p.Domain)
		}
		if len(p.Tags) > 0 {
			quoted := make([]string, len(p.Tags))
			for i, tag := range p.Tags {
				quoted[i] = fmt.Sprintf("%q", tag)
			}
			content += fmt.Sprintf("tags = [%s]\n", strings.Join(quoted, ", "))
		}

		if err := os.MkdirAll(p.Path, 0755); err != nil {
			return nil, fmt.Errorf("create project dir: %w", err)
		}
		if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
			return nil, fmt.Errorf("write config: %w", err)
		}

		// Create vault directories.
		tasksDir, err := vault.TasksDir(name)
		if err == nil {
			os.MkdirAll(filepath.Join(tasksDir, "done"), 0755)
			os.MkdirAll(filepath.Join(tasksDir, "cancelled"), 0755)
		}

		return map[string]string{"status": "initialized", "project": name, "path": p.Path}, nil
	}
}

// ---------------------------------------------------------------------------
// vp_vault_sync
// ---------------------------------------------------------------------------

type vaultSyncParams struct {
	Action  string   `json:"action"`
	Paths   []string `json:"paths"`
	Message string   `json:"message"`
}

var vaultSyncSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"action": {"type": "string", "description": "Action: pull, push, or sync."},
		"paths": {"type": "array", "items": {"type": "string"}, "description": "Optional explicit vault-relative paths to stage and commit before pushing. When provided, ONLY these paths are committed (never git add -A); other dirty files are left untouched. When omitted, push/sync refuse to run on a dirty vault."},
		"message": {"type": "string", "description": "Commit message. Required when paths is provided."}
	},
	"required": ["action"]
}`)

func VaultSyncTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_vault_sync",
		Mutating: true,
		Description: "Pull, push, or sync the vault git repository with configured " +
			"remotes. With no paths, push/sync refuse to run if the vault has " +
			"uncommitted changes (accidental-half-written-state guard). Pass an " +
			"explicit paths list (plus message) to stage and commit ONLY those " +
			"paths before pushing — other dirty files are left untouched; git " +
			"add -A is never used. Supplied paths that match nothing in both the " +
			"worktree and the index are skipped and reported in skipped_paths " +
			"rather than aborting the commit; a tracked-but-deleted path is still " +
			"staged so its removal is committed.",
		Schema:  vaultSyncSchema,
		Handler: vaultSyncHandler(vault),
	}
}

func vaultSyncHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p vaultSyncParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}

		switch p.Action {
		case "pull", "push", "sync":
		default:
			return nil, fmt.Errorf("invalid action %q: must be pull, push, or sync", p.Action)
		}

		vaultSyncMu.Lock()
		defer vaultSyncMu.Unlock()

		root := vault.Root

		// Explicit-path entry point: stage and commit ONLY the supplied
		// paths (never git add -A), then push when the action calls for it.
		// This is the ONLY way to mutate vault history through this tool; the
		// bare push/sync path below keeps its refuse-on-dirty guard intact.
		if len(p.Paths) > 0 {
			if p.Message == "" {
				return nil, fmt.Errorf("message is required when paths are provided")
			}
			doPush := p.Action == "push" || p.Action == "sync"
			res, err := storage.CommitAndPushPaths(root, p.Message, p.Paths, doPush)
			if err != nil {
				return nil, fmt.Errorf("commit: %w", err)
			}
			remoteResults := map[string]string{}
			for name, rerr := range res.RemoteResults {
				if rerr != nil {
					remoteResults[name] = rerr.Error()
				} else {
					remoteResults[name] = "ok"
				}
			}
			// ANY REMOTE FAILURE IS AN ERROR — never `status: "ok"` with the failure
			// buried in remote_results, a field nothing reads. This is the posture
			// vp_capture_session adopted at 200, for the same reason: a tool that
			// reports success for work it did not do trains its caller to stop
			// checking. Partial and stranded differ in the MESSAGE, never in the
			// verdict — 196 killed the middle tier and the precedent holds here.
			if v := storage.RemoteVerdict(storage.OpPush, res.RemoteResults, res.CommitSHA); v != "" {
				return nil, fmt.Errorf("%s: %s (remote_results: %v)", p.Action, v, remoteResults)
			}
			return map[string]any{
				"status":             "ok",
				"action":             p.Action,
				"committed":          res.CommitSHA != "",
				"commit_sha":         res.CommitSHA,
				"pushed":             doPush,
				"stranded":           res.Stranded(),
				"pop_conflict":       res.PopConflict,
				"pop_conflict_paths": res.PopConflictPaths,
				"remote_results":     remoteResults,
				"skipped_paths":      res.SkippedPaths,
			}, nil
		}

		remotes, err := gitRemoteList(root)
		if err != nil {
			return nil, fmt.Errorf("discover remotes: %w", err)
		}

		var output strings.Builder

		if p.Action == "pull" || p.Action == "sync" {
			out, err := gitPull(root, remotes)
			output.WriteString(out)
			if err != nil {
				return nil, fmt.Errorf("pull: %w", err)
			}
		}

		if p.Action == "push" || p.Action == "sync" {
			out, err := gitPush(root, remotes)
			output.WriteString(out)
			if err != nil {
				return nil, fmt.Errorf("push: %w", err)
			}
		}

		return map[string]string{
			"status": "ok",
			"action": p.Action,
			"output": output.String(),
		}, nil
	}
}

func gitRemoteList(root string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "remote")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git remote: %w (is %s a git repo?)", err, root)
	}
	var remotes []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			remotes = append(remotes, line)
		}
	}
	if len(remotes) == 0 {
		return nil, fmt.Errorf("no git remotes configured in %s", root)
	}
	return remotes, nil
}

func gitPull(root string, remotes []string) (string, error) {
	// storage.Pull is best-effort across mirror remotes: it attempts each,
	// self-healing phantom Templates/commands/*.md dirt before the merge, and stops
	// early only when a merge conflict leaves the tree unmergeable.
	//
	// 🔴 THIS USED TO RETURN AT THE FIRST FAILING REMOTE, which meant the two
	// front-ends disagreed about the same event: the CLI printed every remote and
	// exited 0, while this errored on remote #1 and never mentioned remote #2. There
	// was no single answer to "did the sync succeed?" Now both report EVERY remote
	// and both take the same verdict from storage.RemoteVerdict.
	res, err := storage.Pull(root, remotes)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	for _, p := range res.HealedTemplates {
		fmt.Fprintf(&buf, "[heal] discarded stale local %s (matched remote)\n", p)
	}
	for _, remote := range remotes {
		fmt.Fprintf(&buf, "[pull %s] %s\n", remote, strings.TrimSpace(res.RemoteOutput[remote]))
		if rerr := res.RemoteResults[remote]; rerr != nil {
			fmt.Fprintf(&buf, "[pull %s] FAILED: %v\n", remote, rerr)
		}
	}
	if v := storage.RemoteVerdict(storage.OpPull, res.RemoteResults, ""); v != "" {
		return buf.String(), fmt.Errorf("%s", v)
	}
	return buf.String(), nil
}

func gitPush(root string, remotes []string) (string, error) {
	// Check for clean state.
	cmd := exec.Command("git", "-C", root, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	if len(bytes.TrimSpace(out)) > 0 {
		return "", fmt.Errorf("vault has uncommitted changes, commit before pushing")
	}

	var buf strings.Builder
	results := make(map[string]error, len(remotes))
	for _, remote := range remotes {
		cmd := exec.Command("git", "-C", root, "push", remote, "main")
		var combined bytes.Buffer
		cmd.Stdout = &combined
		cmd.Stderr = &combined
		err := cmd.Run()
		results[remote] = err
		fmt.Fprintf(&buf, "[push %s] %s\n", remote, strings.TrimSpace(combined.String()))
		if err != nil {
			fmt.Fprintf(&buf, "[push %s] FAILED: %v\n", remote, err)
		}
	}
	// Every remote is attempted and reported; the verdict names all of them. Same
	// rule, same words, same outcome as the CLI — one definition, two front-ends.
	if v := storage.RemoteVerdict(storage.OpPush, results, "HEAD"); v != "" {
		return buf.String(), fmt.Errorf("%s", v)
	}
	return buf.String(), nil
}

// ---------------------------------------------------------------------------
// vp_vault_tidy
// ---------------------------------------------------------------------------

type vaultTidyParams struct {
	DryRun bool  `json:"dry_run"`
	Push   *bool `json:"push"`
}

var vaultTidySchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"dry_run": {"type": "boolean", "description": "Classify dirt without committing (default false). Returns the swept/reported split only."},
		"push": {"type": "boolean", "description": "Push the tidy commit to all configured remotes (default true). Downgrades to a local-only commit when no remotes are configured."}
	}
}`)

func VaultTidyTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_vault_tidy",
		Mutating: true,
		Description: "Scan the whole vault and commit ONLY classified capture " +
			"artifacts (session summaries, transcript archives, knowledge-graph " +
			"entities/triples, drawers, and tracked .surface stamps) with a " +
			"hostname-stamped message. git add -A is NEVER used: every other dirty " +
			"file is reported for human eyes and left untouched. With dry_run, " +
			"classifies without committing. With push (default true), pushes the " +
			"commit to all configured remotes, downgrading to a local-only commit " +
			"when none exist.",
		Schema:  vaultTidySchema,
		Handler: vaultTidyHandler(vault),
	}
}

func vaultTidyHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p vaultTidyParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}

		vaultSyncMu.Lock()
		defer vaultSyncMu.Unlock()

		root := vault.Root

		if p.DryRun {
			res, err := storage.TidyScan(root)
			if err != nil {
				return nil, fmt.Errorf("tidy scan: %w", err)
			}
			summary := fmt.Sprintf("dry run: would sweep %d artifact%s, %d reported%s",
				len(res.Swept), plural(len(res.Swept)), len(res.Reported),
				userMemorySummarySuffix(len(res.ReportedUserContent)))
			return map[string]any{
				"status":                "ok",
				"dry_run":               true,
				"swept":                 res.Swept,
				"reported":              res.Reported,
				"reported_user_content": res.ReportedUserContent,
				"committed":             false,
				"summary":               summary,
			}, nil
		}

		push := true
		if p.Push != nil {
			push = *p.Push
		}
		res, err := storage.TidyVault(root, push)
		if err != nil {
			return nil, fmt.Errorf("tidy: %w", err)
		}

		remoteResults := map[string]string{}
		pushedCount := 0
		for name, rerr := range res.RemoteResults {
			if rerr != nil {
				remoteResults[name] = rerr.Error()
			} else {
				remoteResults[name] = "ok"
				pushedCount++
			}
		}

		var summary string
		if res.Committed {
			summary = fmt.Sprintf("Swept %d artifact%s (commit %s); %d reported%s",
				len(res.Swept), plural(len(res.Swept)), res.CommitSHA, len(res.Reported),
				userMemorySummarySuffix(len(res.ReportedUserContent)))
			switch {
			case res.PushDowngraded:
				summary += "; no remotes — committed locally only"
			case res.Stranded:
				summary += "; STRANDED — commit NOT pushed to any remote (local-only; reconcile required)"
			case len(remoteResults) > 0:
				summary += fmt.Sprintf("; pushed to %d/%d remote%s", pushedCount, len(remoteResults), plural(len(remoteResults)))
			}
			if res.PopConflict {
				summary += fmt.Sprintf("; pushed, but autostash re-apply conflicted — resolve markers in %s; edits saved in stash",
					strings.Join(res.PopConflictPaths, ", "))
			}
		} else {
			summary = fmt.Sprintf("no-op: nothing to sweep, %d reported%s",
				len(res.Reported), userMemorySummarySuffix(len(res.ReportedUserContent)))
		}

		// A tidy that could not reach a remote is a tidy that FAILED, even though the
		// commit landed locally. The result body is DISCARDED on a handler error (196),
		// so everything the caller needs to act — the commit that does exist, and what
		// is missing from where — has to ride in the error string itself.
		if v := storage.RemoteVerdict(storage.OpPush, res.RemoteResults, res.CommitSHA); v != "" {
			return nil, fmt.Errorf("tidy: %s — the commit EXISTS locally (%s, %d swept) but is not safe; remote_results: %v",
				v, res.CommitSHA, len(res.Swept), remoteResults)
		}

		return map[string]any{
			"status":                "ok",
			"dry_run":               false,
			"swept":                 res.Swept,
			"reported":              res.Reported,
			"reported_user_content": res.ReportedUserContent,
			"committed":             res.Committed,
			"commit_sha":            res.CommitSHA,
			"push_downgraded":       res.PushDowngraded,
			"stranded":              res.Stranded,
			"pop_conflict":          res.PopConflict,
			"pop_conflict_paths":    res.PopConflictPaths,
			"remote_results":        remoteResults,
			"summary":               summary,
		}, nil
	}
}

// plural returns "s" for any count other than 1, for terse pluralization in
// human-readable tool summaries.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// userMemorySummarySuffix renders the user-memory clause appended to a tidy
// summary. The reported count is the full catch-all; this clause flags how many
// of those are expected user-memory files pending commit (committed by
// wrap/SessionEnd, not by tidy) rather than unexpected dirt. Empty when n == 0.
func userMemorySummarySuffix(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" (incl. %d user-memory file%s pending commit)", n, plural(n))
}

// ---------------------------------------------------------------------------
// vp_vault_status
// ---------------------------------------------------------------------------

type vaultStatusParams struct {
	Refresh  bool     `json:"refresh"`
	Sections []string `json:"sections,omitempty"`
}

var vaultStatusSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"refresh": {"type": "boolean", "description": "Perform a bounded per-remote git fetch so behind counts are real (default false). When false, behind counts are reported from cached tracking refs and behind_known is false."},
		"sections": {"type": "array", "items": {"type": "string", "enum": ["sync", "dirt"]}, "description": "Optional subset of report sections to return: \"sync\" (per-remote git sync state, the remotes field) and/or \"dirt\" (working-tree dirt classification). Default/empty returns all. Unselected sections are ZEROED (present-but-empty, not computed) — do not read a suppressed section's fields as real data. The tidy scan always runs regardless; this only trims the payload."}
	}
}`)

func VaultStatusTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_vault_status",
		Mutating: false,
		Description: "Read-only vault sync + working-tree dirt report. Per remote it " +
			"reports ahead/unpushed, behind (only meaningful when behind_known), " +
			"diverged, reachable, and last_fetched; plus the tidy sweep/report dirt " +
			"classification of the working tree. By DEFAULT it does NOT fetch (fast " +
			"cached path; behind_known=false). Pass refresh:true to run a bounded " +
			"per-remote git fetch for real behind counts. It NEVER commits, pushes, " +
			"or mutates the working tree; a fetch only updates .git tracking refs.",
		Schema:  vaultStatusSchema,
		Handler: vaultStatusHandler(vault),
	}
}

func vaultStatusHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p vaultStatusParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		report, err := storage.BuildStatusReport(vault.Root, p.Refresh)
		if err != nil {
			return nil, fmt.Errorf("vault status: %w", err)
		}
		// Optional post-filter: when a narrowing sections selector is supplied,
		// ZERO the unselected section's field (present-but-empty, not an absent
		// key, and never omitempty on the shared output struct). Default/empty
		// returns the full report byte-for-byte unchanged. The tidy scan and
		// remote probes always run — this trims the payload, not the IO.
		if len(p.Sections) > 0 {
			var wantSync, wantDirt bool
			for _, s := range p.Sections {
				switch s {
				case "sync":
					wantSync = true
				case "dirt":
					wantDirt = true
				default:
					return nil, fmt.Errorf("invalid section %q: must be one of [sync dirt]", s)
				}
			}
			if !wantSync {
				report.Remotes = nil
			}
			if !wantDirt {
				report.Dirt = storage.DirtJSON{}
			}
		}
		return report, nil
	}
}

// ---------------------------------------------------------------------------
// vp_refresh_index
// ---------------------------------------------------------------------------

type refreshIndexParams struct {
	Project string `json:"project"`
}

var refreshIndexSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."}
	},
	"required": ["project"]
}`)

func RefreshIndexTool(engine *search.Engine) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_refresh_index",
		Description: "Rebuild the semantic search index for a project from scratch.",
		Schema:      refreshIndexSchema,
		Handler:     refreshIndexHandler(engine),
	}
}

func refreshIndexHandler(engine *search.Engine) mcp.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		var p refreshIndexParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if err := engine.Rebuild(ctx, p.Project); err != nil {
			return nil, fmt.Errorf("rebuild index: %w", err)
		}
		return map[string]string{"status": "rebuilt", "project": p.Project}, nil
	}
}
