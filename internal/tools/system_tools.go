// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/capture"
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
	NoTidy  bool     `json:"no_tidy"`
}

var vaultSyncSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"action": {"type": "string", "description": "Action: pull, push, or sync."},
		"paths": {"type": "array", "items": {"type": "string"}, "description": "Optional explicit vault-relative paths to stage and commit before pushing. When provided, ONLY these paths are committed (never git add -A); other dirty files are left untouched. When omitted, push/sync refuse to run on a dirty vault."},
		"message": {"type": "string", "description": "Commit message. Required when paths is provided."},
		"no_tidy": {"type": "boolean", "description": "Skip the implicit capture-artifact tidy on a bare sync; raw pull+push that refuses on any dirt."}
	},
	"required": ["action"]
}`)

func VaultSyncTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_vault_sync",
		Mutating: true,
		// The bare pull writes no vault content and IS the recovery path — see
		// vaultSyncReadOnly, which also explains why the paths list is part of
		// the predicate and the action alone is not.
		ReadOnlyWhen: vaultSyncReadOnly,
		Description: "Pull, push, or sync the vault git repository with configured " +
			"remotes. A bare sync (no paths) now tidies capture artifacts first: " +
			"it classifies the working tree, commits ONLY capture artifacts " +
			"(never git add -A), pulls, then pushes — refusing up front on genuine " +
			"non-artifact dirt. Pass no_tidy:true to restore the raw behavior: a " +
			"plain pull+push that refuses to run if the vault has uncommitted " +
			"changes (accidental-half-written-state guard); bare pull/push always " +
			"use that raw path. Pass an " +
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
				// CALLER error: the guard rejected an incomplete request.
				return nil, apperr.Caller(fmt.Errorf("message is required when paths are provided"))
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

		// Bare sync now tidies capture artifacts first (classify → refuse-on-dirt →
		// commit artifacts → pull → push) via the shared orchestration. no_tidy
		// restores the raw pull+push path below.
		if p.Action == "sync" && !p.NoTidy {
			res, err := storage.SyncVault(root, remotes)
			var output strings.Builder
			if res.Committed {
				fmt.Fprintf(&output, "swept %d capture artifact%s before sync\n", len(res.Swept), plural(len(res.Swept)))
			}
			if n := len(res.Deferred); n > 0 {
				pairs := "pairs"
				if n == 1 {
					pairs = "pair"
				}
				fmt.Fprintf(&output, "Deferred %d incomplete transcript %s (manifest pending) — left for the next sweep\n", n, pairs)
			}
			if res.Pull != nil {
				for _, remote := range remotes {
					fmt.Fprintf(&output, "[pull %s] %s\n", remote, strings.TrimSpace(res.Pull.RemoteOutput[remote]))
				}
			}
			if res.Push != nil {
				for _, remote := range remotes {
					fmt.Fprintf(&output, "[push %s] %s\n", remote, strings.TrimSpace(res.Push.RemoteOutput[remote]))
				}
			}
			if err != nil {
				// The refusal/verdict message names the dirt or failing remote;
				// SyncVault already formatted it. The result body is discarded on
				// a handler error, so the error string must carry what to act on.
				// Refuse-on-dirt is caller friction — wrap so health stays green.
				// SyncVault never returns a nil *SyncResult (non-nil contract);
				// Refused is the refuse-on-dirt path — caller friction.
				if res.Refused {
					return nil, apperr.Caller(fmt.Errorf("sync: %w", err))
				}
				return nil, fmt.Errorf("sync: %w", err)
			}
			return map[string]any{
				"status":     "ok",
				"action":     "sync",
				"committed":  res.Committed,
				"commit_sha": res.CommitSHA,
				"swept":      res.Swept,
				"deferred":   res.Deferred,
				"output":     output.String(),
			}, nil
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
	// The counterpart line. Without it the [heal] output above reads as a
	// complete account of the heal pass, and a merge failure caused by one of
	// these paths looks unrelated to it. Name the path, git's own reason, and
	// the consequence — the causal link is the entire point.
	for _, f := range res.FailedHeals {
		fmt.Fprintf(&buf, "[heal] FAILED to clear %s: %s — path is still dirty and may block the merge\n", f.Path, f.Reason)
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
	if dirty := bytes.TrimSpace(out); len(dirty) > 0 {
		// Caller friction: the refuse-on-dirty guard worked. Name dirty paths
		// (capped — unbounded single-line MCP errors train agents to skim) and
		// point at remedies an agent can take next turn.
		return "", apperr.Caller(fmt.Errorf("%s", formatDirtyVaultPushError(porcelainDirtyPaths(string(dirty)))))
	}

	// Delegate the plain push loop to storage.PushPlain, which attempts every
	// remote and returns structured per-remote results (mirroring storage.Pull).
	res, err := storage.PushPlain(root, remotes)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	for _, remote := range remotes {
		fmt.Fprintf(&buf, "[push %s] %s\n", remote, strings.TrimSpace(res.RemoteOutput[remote]))
		if rerr := res.RemoteResults[remote]; rerr != nil {
			fmt.Fprintf(&buf, "[push %s] FAILED: %v\n", remote, rerr)
		}
	}
	// Every remote is attempted and reported; the verdict names all of them. Same
	// rule, same words, same outcome as the CLI — one definition, two front-ends.
	if v := storage.RemoteVerdict(storage.OpPush, res.RemoteResults, "HEAD"); v != "" {
		return buf.String(), fmt.Errorf("%s", v)
	}
	return buf.String(), nil
}

// dirtyPathErrorCap bounds how many porcelain paths appear in one MCP error
// line. Beyond this, agents skim and the legibility fix becomes noise.
const dirtyPathErrorCap = 10

// porcelainDirtyPaths extracts working-tree paths from `git status --porcelain`
// output so refuse-on-dirty errors can name the offenders. Blank/short lines are
// skipped; renames contribute the destination path. Kept local to tools — the
// wrapstate parser is deliberately package-private and this seam only needs
// enough fidelity for a human/agent-readable error line.
func porcelainDirtyPaths(porcelain string) []string {
	var paths []string
	seen := map[string]bool{}
	for line := range strings.SplitSeq(porcelain, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 4 {
			continue
		}
		// XY <path> (two status chars + space); renames are XY <old> -> <new>.
		path := strings.TrimSpace(line[3:])
		if path == "" {
			continue
		}
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = path[idx+len(" -> "):]
		}
		if len(path) >= 2 && path[0] == '"' && path[len(path)-1] == '"' {
			path = path[1 : len(path)-1]
		}
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}

// formatDirtyVaultPushError builds the refuse-on-dirty push message. paths may
// be empty (porcelain unparseable) — still actionable.
func formatDirtyVaultPushError(paths []string) string {
	const remedy = "commit or stash before pushing; " +
		"or pass paths+message to commit specific files, call vp_vault_tidy to sweep capture artifacts, " +
		"or vp_vault_status to inspect"
	if len(paths) == 0 {
		return "vault has uncommitted changes — " + remedy
	}
	shown := paths
	suffix := ""
	if len(paths) > dirtyPathErrorCap {
		shown = paths[:dirtyPathErrorCap]
		suffix = fmt.Sprintf(" …and %d more", len(paths)-dirtyPathErrorCap)
	}
	return fmt.Sprintf("vault has uncommitted changes: %s%s — %s",
		strings.Join(shown, ", "), suffix, remedy)
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
		// dry_run:true classifies and commits nothing — see vaultTidyReadOnly.
		ReadOnlyWhen: vaultTidyReadOnly,
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
				"deferred":              res.Deferred,
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
			"deferred":              res.Deferred,
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
			"classification of the working tree. It also flags vault_path drift: when " +
			"the running server's root (frozen at startup) no longer matches a fresh " +
			"resolution of the config, drift is true and configured_vault_path names " +
			"the new target — a mid-session config change that would split writes " +
			"across two vaults (reload the server to re-resolve). By DEFAULT it does " +
			"NOT fetch (fast cached path; behind_known=false). Pass refresh:true to run " +
			"a bounded per-remote git fetch for real behind counts. It NEVER commits, " +
			"pushes, or mutates the working tree; a fetch only updates .git tracking refs.",
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
		// Drift is a property of the SERVER context, not of the path BuildStatusReport
		// was handed, so it is computed here rather than inside the shared builder: a
		// long-lived server froze vault.Root at startup, and a fresh resolution now may
		// disagree. (A fresh CLI process resolves both from one call and never drifts,
		// which is why the builder leaves these fields zero.)
		if configured, drift := storage.DetectVaultPathDrift(vault.Root); drift {
			report.ConfiguredVaultPath = configured
			report.Drift = true
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

func RefreshIndexTool(engine *search.Engine, vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_refresh_index",
		Description: "Rebuild a project's semantic search index from every source it has: its " +
			"existing drawer store, its iterations.md corpus, and a BACKFILL from any " +
			"archived transcripts under Projects/<slug>/transcripts/. The archives are " +
			"decompressed and fed to the same indexer capture uses, so a backfilled drawer " +
			"is identical to one written at capture time — this is how a project with " +
			"session history but no palace store becomes searchable. The first run over an " +
			"archive decompresses it; later runs SKIP any archive whose source_sha256 is " +
			"already recorded in palace/<slug>/ingested-archives.jsonl, so a re-run neither " +
			"re-reads them nor duplicates drawers. Reports what it did (drawers, " +
			"iteration_chunks, indexed, embedded, cache_hits, reaped, plus archives_found, " +
			"archives_ingested, archives_skipped, archive_drawers and any archive_failures) " +
			"so a real rebuild is distinguishable from a no-op and a skip is never counted " +
			"as an ingest. It cannot invent content that " +
			"was never captured: it REFUSES when the project has no palace store, nothing " +
			"indexable, AND no transcript archives, rather than reporting a success it did " +
			"not achieve.",
		Schema:  refreshIndexSchema,
		Handler: refreshIndexHandler(engine, vault),
		// Mutating because it WRITES, on three independent paths: the archive
		// backfill reaches storage.Vault.AppendDrawers -> appendUnderLock, the
		// embed pass writes .vec cache files on every cache miss, and Rebuild
		// can create palace/<slug>/ outright for a project that had no store.
		// It was registered non-mutating for a long time, and the derived call
		// graph reported the disagreement as accepted-under-protest debt for
		// exactly that long.
		Mutating: true,
	}
}

// backfillStats reports what a historical-archive ingest actually did.
//
// Ingested and Skipped are separate counts and must stay separate: reporting a
// skip as an ingest would say "I read 373 archives" about a run that opened
// none of them, which is the same class of false success this tool was already
// caught making once.
type backfillStats struct {
	ArchivesFound    int
	ArchivesIngested int
	ArchivesSkipped  int
	Drawers          int
	Failures         []string
}

// backfillFromArchives turns a project's ARCHIVED TRANSCRIPTS into drawers.
//
// 🔴 WHY THIS IS IN THE HANDLER AND NOT IN Rebuild/ensureIndex. Piece 1 of this
// task established that a judgement belongs to the caller that ASKED: ensureIndex
// calls Rebuild before every cold search, so anything placed there runs because
// somebody searched. A mass decompress-and-embed of every historical archive is
// the last thing that should fire on a search. vp_refresh_index is the caller
// that asked for an index; the ingest lives here and nowhere else.
//
// It adds no chunker, no classifier and no decompressor. archive.ListEntries
// and archive.Extract already exist, and the text they yield is handed to the
// SAME capture.Indexer.IndexTranscript that runs at capture time. That is not a
// convenience — it is the correctness argument. The hook path reads the host
// transcript file RAW (os.ReadFile -> string, internal/hook/hook.go) and passes
// those bytes straight to IndexTranscript, and archive.Extract yields the same
// original bytes (they are what source_sha256 covers). So a backfilled drawer is
// byte-for-byte what capture-time indexing would have written. Introducing a
// JSONL-to-prose transform here would have made backfilled content DIFFER from
// captured content, which is a worse outcome than the noise it removes.
//
// Idempotent, and CHEAPLY so — the second half of that is the part that was
// missing. storage.DrawerID is derived from the chunk content, and AppendDrawers
// reports how many drawers it actually appended rather than erroring per
// duplicate, so a re-run adds nothing. It used to add nothing at the cost of a
// full room rescan and rewrite PER CHUNK, which is why a re-run cost the same as
// a first run and neither could finish; the batch entry point pays one read per
// (archive, room) and writes only when something is new.
//
// Per-archive failures are collected and reported rather than aborting the
// sweep — one unreadable archive must not strand the rest. A CANCELLED context
// is the one exception: it aborts, because continuing to write for a client
// that has gone away is the defect, not resilience.
func backfillFromArchives(ctx context.Context, engine *search.Engine, vault *storage.Vault, project string) (backfillStats, error) {
	var bs backfillStats

	entries, err := archive.ListEntries(vault.Root, project)
	if err != nil {
		return bs, fmt.Errorf("list transcript archives: %w", err)
	}
	bs.ArchivesFound = len(entries)
	if len(entries) == 0 {
		return bs, nil
	}

	cfg, err := vault.LoadConfig(project)
	if err != nil {
		return bs, fmt.Errorf("load config: %w", err)
	}
	// The engine is passed, so IndexTranscript both writes drawers AND indexes
	// them — the backfill is self-sufficient rather than depending on the
	// caller's Rebuild to make its work reachable.
	//
	// MEASURED, because the obvious worry here is wrong: Engine.IndexDrawers
	// writes each vector to the embed cache, so the Rebuild that follows gets
	// cache hits and embeds NOTHING extra. A counting embedder over a fixture
	// archive reports 8 texts embedded during ingest and 8 in total after the
	// rebuild. Passing nil to avoid a "double embed" would buy no work at all
	// and would trade it for an ordering dependency in which backfilled drawers
	// sit on disk unreachable by search if the Rebuild is ever reordered away.
	indexer := capture.NewIndexer(vault, engine, engine.Embedder(), cfg)

	// The ingest ledger, read ONCE for the whole sweep. This is what makes run
	// two cheap: without it every run decompressed all 373 archives and walked
	// IndexTranscript over every chunk of them, only to discover at the end
	// that every drawer was already filed. The set is a few hundred hashes and
	// is discarded when this function returns — it is a loop variable, not a
	// cache with a lifetime.
	//
	// A ledger that cannot be READ is not a reason to refuse the sweep: the
	// worst case is the behaviour that existed before it, so it degrades to a
	// full reingest rather than to a failure.
	ingested, err := vault.IngestedArchives(project)
	if err != nil {
		slog.Warn("refresh index: ingest ledger unreadable, reingesting everything",
			"project", project, "err", err)
		ingested = map[string]struct{}{}
	}

	// 🔴 THE ONLY CANCELLATION POINT ON THIS PATH. Nothing below observes ctx
	// except the embedder: IndexTranscript's chunk, classify and append work
	// takes no ctx at all. Without this check a client that gave up — an MCP
	// idle timeout, an operator's Ctrl-C — was released while this loop kept
	// decompressing and writing archives nobody was waiting for. Checked at the
	// TOP of the iteration so a cancellation costs at most the archive already
	// in flight, and reported as an error rather than a short success so the
	// counts never describe a sweep that was cut off.
	start := time.Now()
	for i, e := range entries {
		if err := ctx.Err(); err != nil {
			return bs, fmt.Errorf("backfill cancelled after %d/%d archives: %w", i, len(entries), err)
		}

		sourceSHA, sessionID := "", ""
		if e.Manifest != nil {
			sourceSHA = e.Manifest.SourceSHA256
			sessionID = e.Manifest.SessionID
		}

		// Skip BEFORE the decompress. Extracting and then discovering every
		// chunk is a duplicate is the expensive half of the old behaviour, and
		// it is the half the ledger exists to delete.
		if sourceSHA != "" {
			if _, done := ingested[sourceSHA]; done {
				bs.ArchivesSkipped++
				slog.Debug("refresh index: archive already ingested",
					"project", project, "archive", i+1, "of", len(entries),
					"session_id", sessionID, "source_sha256", sourceSHA)
				continue
			}
		}

		var buf bytes.Buffer
		if _, err := archive.Extract(e.ArchivePath, &buf); err != nil {
			bs.Failures = append(bs.Failures, fmt.Sprintf("%s: extract: %v", filepath.Base(e.ArchivePath), err))
			continue
		}
		st, err := indexer.IndexTranscript(ctx, sessionID, project, buf.String())
		if err != nil {
			bs.Failures = append(bs.Failures, fmt.Sprintf("%s: index: %v", filepath.Base(e.ArchivePath), err))
			continue
		}
		bs.ArchivesIngested++
		bs.Drawers += st.Drawers

		// Record AFTER a successful ingest, and record it even when the archive
		// yielded ZERO new drawers — "already filed by capture" is exactly as
		// ingested as "filed by this run", and the whole point is to not open
		// this file again. The ordering is deliberate: a crash between the
		// ingest and this write costs a reingest, which IndexTranscript makes
		// harmless, whereas writing first would let a crash mark an archive
		// done that was never read.
		if sourceSHA == "" {
			// Nothing to key on. Ingest it every time and say so, rather than
			// writing a row that can never match.
			slog.Warn("refresh index: archive has no source_sha256, cannot be skipped on a later run",
				"project", project, "archive", i+1, "of", len(entries), "session_id", sessionID)
		} else if err := vault.RecordIngestedArchive(project, storage.IngestedArchive{
			SourceSHA256: sourceSHA,
			SessionID:    sessionID,
		}); err != nil {
			// The ingest itself SUCCEEDED, so this is not an archive failure
			// and must not be counted as one. It costs a reingest next run, and
			// the operator should see why.
			slog.Warn("refresh index: ingest succeeded but ledger write failed",
				"project", project, "session_id", sessionID, "err", err)
			bs.Failures = append(bs.Failures,
				fmt.Sprintf("%s: ledger: %v (archive was ingested; it will be re-ingested next run)",
					filepath.Base(e.ArchivePath), err))
		} else {
			// Guard the sweep against itself: two archives can carry the same
			// bytes, and the second must skip rather than reingest.
			ingested[sourceSHA] = struct{}{}
		}

		// Per-archive, at Info, because the enclosing start/done pair cannot
		// distinguish a slow sweep from a stuck one — and "hung or just slow?"
		// being unanswerable is what cost an hour of re-runs here. One line per
		// archive is bounded by the archive count, not by the chunk count.
		slog.Info("refresh index: archive ingested",
			"project", project,
			"archive", i+1,
			"of", len(entries),
			"session_id", sessionID,
			"drawers", st.Drawers,
			"elapsed", time.Since(start),
		)
	}
	return bs, nil
}

func refreshIndexHandler(engine *search.Engine, vault *storage.Vault) mcp.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		var p refreshIndexParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}

		// A rebuild over a real corpus can run for minutes with nothing else to
		// show for it: mcp.makeHandler's own enter/exit logging is Debug, and
		// the default log level is Info, so a long refresh was silent until
		// something finally Warned (measured 2026-08-24, a ~20-minute hang
		// with no log line before the operator cancelled). These two lines are
		// visible at the default level so "it started" and "it finished" are
		// never in doubt; the exit line reports whatever stats were reached
		// even on an error return.
		start := time.Now()
		var bf backfillStats
		var stats search.RebuildStats
		slog.Info("refresh index: start", "project", p.Project)
		defer func() {
			slog.Info("refresh index: done",
				"project", p.Project,
				"elapsed", time.Since(start),
				"drawers", stats.Drawers,
				"embedded", stats.Embedded,
				"cache_hits", stats.CacheHits,
			)
		}()

		// Ask BEFORE rebuilding. A rebuild can create palace/<project>/ as a
		// side effect of indexing the iterations corpus, so asking afterwards
		// answers a different question than the one the refusal turns on.
		hadStore, err := vault.HasPalaceStore(p.Project)
		if err != nil {
			return nil, fmt.Errorf("check palace store: %w", err)
		}

		// Backfill BEFORE Rebuild. IndexTranscript writes drawers and indexes
		// them, so running it first lets the subsequent Rebuild walk the new
		// drawers too and leaves the on-disk corpus and the in-memory vector
		// index describing the same thing. Running it after would report counts
		// for a corpus the rebuild never saw.
		bf, err = backfillFromArchives(ctx, engine, vault, p.Project)
		if err != nil {
			return nil, fmt.Errorf("backfill from archives: %w", err)
		}

		stats, err = engine.Rebuild(ctx, p.Project)
		if err != nil {
			return nil, fmt.Errorf("rebuild index: %w", err)
		}

		// 🔴 The operator asked for an index and there is none. Refuse — do not
		// report a zero-count success. `{"status":"rebuilt"}` was previously
		// returned here having walked nothing, which is worse than a missing
		// feature: it CLOSES the investigation. An operator clearing a
		// project-tree-coherence finding sees success, the next audit reports
		// the finding again, and the natural reading is that the audit is
		// flaky rather than that this tool lied (iter 265).
		//
		// The condition is "no store AND nothing indexed", not "no store".
		// Since a583440 the iterations corpus is a second source that needs no
		// palace store, so a project with iterations.md but no drawers gets a
		// real index from a rebuild that legitimately CREATES the store —
		// refusing that would be this same defect inverted, reporting failure
		// for work that was done.
		// ArchivesSkipped counts here as evidence, not just ArchivesIngested: an
		// archive the ledger already covers is content this project HAS, so a
		// second run over a project whose every archive is ingested must not
		// suddenly report "nothing to refresh" for the corpus the first run
		// filed. Without that term the ledger would turn a success into a
		// refusal on the very next call.
		if !hadStore && stats.Indexed == 0 && bf.ArchivesIngested == 0 && bf.ArchivesSkipped == 0 {
			return nil, fmt.Errorf(
				"refresh index %q: nothing to refresh — no palace store at palace/%s/drawers, "+
					"and no indexable content anywhere (0 drawers, 0 iteration chunks, "+
					"%d transcript archives).\n"+
					"This tool re-embeds an existing corpus and backfills from ARCHIVED "+
					"TRANSCRIPTS under Projects/%s/transcripts/; it cannot invent content "+
					"that was never captured.\n"+
					"Drawers are otherwise written at capture time (internal/capture indexes "+
					"the transcript, never the session note), so a project captured without a "+
					"transcript AND without an archive has none.\n"+
					"Next step: capture new work in this project normally, or delete the "+
					"orphaned history. Do not re-run this expecting a different answer.",
				p.Project, p.Project, bf.ArchivesFound, p.Project)
		}

		return map[string]any{
			"status":           "rebuilt",
			"project":          p.Project,
			"had_palace_store": hadStore,
			"drawers":          stats.Drawers,
			"iteration_chunks": stats.IterationChunks,
			"indexed":          stats.Indexed,
			"embedded":         stats.Embedded,
			"cache_hits":       stats.CacheHits,
			"reaped":           stats.Reaped,
			// Backfill counts are reported separately from the rebuild's own
			// counts: "I re-embedded 40 drawers" and "I created 12 of them from
			// archives just now" are different claims, and collapsing them would
			// hide whether the backfill did anything.
			"archives_found":    bf.ArchivesFound,
			"archives_ingested": bf.ArchivesIngested,
			// Archives the ingest ledger already covers, which were never
			// opened on this call. Reported beside ingested rather than folded
			// into it: "found 373, ingested 0, skipped 373" is the shape of a
			// healthy second run, and it is unreadable if the two collapse.
			"archives_skipped": bf.ArchivesSkipped,
			"archive_drawers":  bf.Drawers,
			"archive_failures": bf.Failures,
		}, nil
	}
}
