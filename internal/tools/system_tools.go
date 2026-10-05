// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/ingest"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/onboard"
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

// initSchema deliberately marks NOTHING required.
//
// `path` used to be required, which encoded the wrong model: the server's
// filesystem is not the caller's, so a path is a claim about THIS machine that
// the caller usually cannot verify. It is optional now, and when it is absent
// (or names something that is not a project) the vault side still runs and
// every working-tree step is reported as an Omission. `name` becomes required
// in exactly that case, because the slug can no longer be detected and must
// never be walked up to from an ancestor.
var initSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path":   {"type": "string", "description": "Absolute path to a project directory. It must already be an existing project directory on the server's filesystem — this tool never creates it, and it must carry .vibe-palace.toml, .git or a known manifest in that directory ITSELF. Omit it to run vault-side onboarding only."},
		"name":   {"type": "string", "description": "Project name slug. Required when path is omitted or does not name a project directory on the server; the slug is never inferred from an ancestor directory."},
		"domain": {"type": "string", "description": "Domain (e.g. work, personal, opensource)."},
		"tags":   {"type": "array", "items": {"type": "string"}, "description": "Project tags."}
	}
}`)

// initStepRow is one onboarding step's rendered outcome.
//
// Created is carried without omitempty on purpose. The question it answers —
// "did this call bring the artifact into existence, or was it already here?" —
// is one only a caller can ask, and an absent key would be indistinguishable
// from a false one. It is a POSITIVE claim only; see onboard.Outcome.Created
// for why a false does not mean "nothing was written".
type initStepRow struct {
	Step    string   `json:"step"`
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Created bool     `json:"created"`
	Details []string `json:"details,omitempty"`
}

// initOmissionRow is one step this SURFACE may not run, with the remedy.
type initOmissionRow struct {
	Step   string `json:"step"`
	Side   string `json:"side"`
	Reason string `json:"reason"`
	Remedy string `json:"remedy"`
}

// initAdvisoryRow is run-level guidance belonging to no single step.
type initAdvisoryRow struct {
	Name    string   `json:"name"`
	Summary string   `json:"summary"`
	Details []string `json:"details,omitempty"`
}

// initResult is what vp_init returns.
//
// The old result was map[string]string{"status": "initialized", ...},
// unconditionally, whatever the handler had actually managed to write. That
// unconditional success claim is the defect this tool's rewrite exists to
// delete; the missing scaffold was only its symptom.
type initResult struct {
	Status  string `json:"status"`
	Project string `json:"project"`
	Path    string `json:"path,omitempty"`
	// OK is the field a caller keys off, and it is the only top-level field
	// whose value depends on how THIS call went.
	//
	// Status and Complete answer "did this surface do everything the tool can
	// do", and over MCP that answer is a CONSTANT: ScopeForMCP always omits
	// hook-wiring and command-shims, so Complete is always false and Status is
	// always "partial" — on a flawless run and on one where the vault write
	// failed alike. Keying off either yields zero bits of information, which
	// is the same unconditional-verdict defect this tool's rewrite exists to
	// delete, and it is why OK exists rather than being derived from them.
	OK bool `json:"ok"`
	// Failed names the steps that produced a fail row, in step-table order.
	// Empty exactly when OK is true.
	Failed   []string `json:"failed"`
	Complete bool     `json:"complete"`
	// Steps and Omitted together account for EVERY step in
	// onboard.Steps(); onboard.Run refuses to return a Result where they do
	// not.
	Steps      []initStepRow     `json:"steps"`
	Omitted    []initOmissionRow `json:"omitted"`
	Advisories []initAdvisoryRow `json:"advisories,omitempty"`
}

func InitProjectTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_init",
		Mutating: true,
		Description: "Onboard a project into THIS server's vault. Vault-side onboarding " +
			"(the Projects/<slug>/commands/ + skills/ scaffold) always runs. " +
			"Working-tree steps — " +
			".vibe-palace.toml, the AGENTS.md/CLAUDE.md managed block, the project " +
			".gitignore and the commit.msg git hook — run ONLY when `path` names a " +
			"directory that is already a project on the SERVER's filesystem; this " +
			"tool never creates that directory and never walks up to an ancestor to " +
			"find one. Host-global state is NEVER written: hook wiring rewrites " +
			"~/.claude/settings.json, which over MCP is the server operator's home " +
			"rather than yours, and the .claude/commands/vpc-*.md command shims and " +
			".claude/skills/vps-*/SKILL.md skill shims decide what to emit by " +
			"reading that same home, so both are always omitted. Upgrading is a " +
			"different job and a different command: agent files and shims are " +
			"`vp commands upgrade`'s (it also removes stale shims, which onboarding " +
			"never does), and removing a vault Templates/ override of a built-in is " +
			"`vp commands reset` / `vp skills reset` (a backup is kept). " +
			"Returns {ok, failed[], status, project, complete, steps[], omitted[], " +
			"advisories[]}. KEY OFF `ok`: it is false exactly when some step this " +
			"call actually RAN failed, and `failed[]` names those steps. Do NOT key " +
			"off `status` or `complete` — over MCP hook-wiring and command-shims are " +
			"omitted on every call, so BY CONSTRUCTION `status` is always `partial` " +
			"and `complete` is always false, on a flawless run and a broken one " +
			"alike; they tell you only that a remote surface cannot finish the job " +
			"alone. `omitted[]` is that unfinished remainder: each entry carries the " +
			"verbatim command, the host it must run on, and the artifact that is " +
			"still missing. Each `steps[]` row also carries `created`, true when " +
			"that step brought its artifact into existence rather than finding it " +
			"already there.",
		Schema:  initSchema,
		Handler: initProjectHandler(vault),
	}
}

func initProjectHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		var p initParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Path != "" {
			if !filepath.IsAbs(p.Path) {
				return nil, fmt.Errorf("path must be absolute, got %q", p.Path)
			}
			if hasDotDotSegment(p.Path) {
				return nil, fmt.Errorf("path must not contain a '..' segment, got %q", p.Path)
			}
		}

		// THE GATE, and it must come before any use of p.Path for identity.
		//
		// project.DetectProject walks UPWARD until it hits the home boundary, so
		// asking it for a slug at a path that is not itself a project answers
		// with an ANCESTOR's slug. Without this, vp_init {path: ".../repo/vendor/x"}
		// fails the rooted-signal gate for the working tree and still writes
		// vault artifacts under the enclosing repo's name.
		rooted := project.HasRootedSignal(p.Path)

		name := p.Name
		if name == "" {
			if !rooted {
				return nil, fmt.Errorf(
					"name is required: %q is not a project directory on this server "+
						"(no .vibe-palace.toml, .git or known manifest in that directory itself), "+
						"so the project slug cannot be detected — pass an explicit name",
					p.Path)
			}
			if detected, derr := project.DetectProject(p.Path); derr == nil && detected != "" {
				name = detected
			} else {
				name = filepath.Base(p.Path)
			}
		}
		if err := slug.ValidateCreatable(name); err != nil {
			return nil, fmt.Errorf("invalid project name %q: %w", name, err)
		}

		req := onboard.Request{
			// The vault this server was STARTED against, never one re-resolved
			// from p.Path: that binding is the whole premise of the tool.
			OpenVault:  func() (*storage.Vault, error) { return vault, nil },
			Slug:       name,
			ProjectDir: p.Path,
			Domain:     p.Domain,
			Tags:       p.Tags,
		}
		res, err := onboard.Run(ctx, req, onboard.ScopeForMCP(req))
		if err != nil {
			// An accounting failure: some step produced neither an outcome nor
			// an omission. That is the one shape a result table cannot show, so
			// it is an error rather than a partial result.
			return nil, fmt.Errorf("onboard: %w", err)
		}

		out := initResult{
			Status:   "partial",
			Project:  res.Slug,
			Path:     p.Path,
			OK:       res.OK(),
			Failed:   append(make([]string, 0, len(res.Failed)), res.Failed...),
			Complete: res.Complete,
			Steps:    make([]initStepRow, 0, len(res.Outcomes)),
			Omitted:  make([]initOmissionRow, 0, len(res.Omitted)),
		}
		if res.Complete {
			out.Status = "initialized"
		}
		for _, oc := range res.Outcomes {
			out.Steps = append(out.Steps, initStepRow{
				Step:    oc.Step,
				Name:    oc.Name,
				Status:  checkStatusString(oc.Status),
				Summary: oc.Summary,
				Created: oc.Created,
				Details: oc.Details,
			})
		}
		for _, om := range res.Omitted {
			out.Omitted = append(out.Omitted, initOmissionRow{
				Step:   om.Step,
				Side:   om.Side.String(),
				Reason: om.Reason,
				Remedy: om.Remedy,
			})
		}
		for _, ad := range res.Advisories {
			out.Advisories = append(out.Advisories, initAdvisoryRow{
				Name:    ad.Name,
				Summary: ad.Summary,
				Details: ad.Details,
			})
		}
		return out, nil
	}
}

// hasDotDotSegment reports whether p contains a ".." PATH SEGMENT.
//
// The predicate used to be strings.Contains(p, ".."), which refuses legitimate
// paths like /home/x/foo..bar while catching nothing a segment check misses.
func hasDotDotSegment(p string) bool {
	for seg := range strings.SplitSeq(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
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
			"staged so its removal is committed. A path the vault's .gitignore " +
			"ignores is not committed either: it is listed in skipped_paths with " +
			"skip_reasons naming why.",
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

		// git_enabled refuses first, before the paths commit and before
		// ListRemotes: the same storage decision, at the same point, as the CLI.
		verb := p.Action
		if len(p.Paths) > 0 {
			verb = "commit"
		}
		if err := storage.RefuseIfGitDisabled(root, verb); err != nil {
			return nil, err
		}

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
				"status":         "ok",
				"action":         p.Action,
				"committed":      res.CommitSHA != "",
				"commit_sha":     res.CommitSHA,
				"pushed":         doPush,
				"stranded":       res.Stranded(),
				"remote_results": remoteResults,
				"skipped_paths":  res.SkippedPaths,
				"skip_reasons":   res.SkipReasons,
			}, nil
		}

		// storage.ListRemotes is the ONE remote lister. It reports an empty repo
		// as `(nil, nil)` — right for a status reader, fatal here: every branch
		// below iterates `remotes`, so zero remotes would pull nothing, push
		// nothing, and return `status: "ok"` for a sync that moved no bytes.
		// The refusal is this call site's job, not the lister's.
		remotes, err := storage.ListRemotes(root)
		if err != nil {
			return nil, fmt.Errorf("discover remotes: %w", err)
		}
		if len(remotes) == 0 {
			return nil, fmt.Errorf("no git remotes configured in %s", root)
		}

		// Bare sync now tidies capture artifacts first (classify → refuse-on-dirt →
		// commit artifacts → pull → push) via the shared orchestration. no_tidy
		// restores the raw pull+push path below.
		if p.Action == "sync" && !p.NoTidy {
			res, err := storage.SyncVault(root, remotes)
			// A pull is how this host learns a project left the vault: the index sweep
			// removes its host-local index store now that no vault lock is held.
			indexstore.ReapGoneProjects(context.Background(), storage.NewVault(root))
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
				for _, l := range res.Pull.Derived.Lines() {
					fmt.Fprintln(&output, l)
				}
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
				// A pull verdict names the failing remotes but not why; the
				// why — an aborted conflict's paths and remedy above all —
				// rides in the error, as gitPull's does.
				var failed strings.Builder
				if res.Pull != nil {
					for _, remote := range remotes {
						if rerr := res.Pull.RemoteResults[remote]; rerr != nil {
							fmt.Fprintf(&failed, "\n[pull %s] FAILED: %v", remote, rerr)
						}
					}
				}
				return nil, fmt.Errorf("sync: %w%s", err, failed.String())
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

func gitPull(root string, remotes []string) (string, error) {
	// storage.Pull is best-effort across mirror remotes: it attempts each,
	// self-healing phantom Templates/commands/*.md dirt before the merge. A
	// conflicted merge it started is aborted and named, and stops the sweep, as
	// do a killed merge and a departure (storage.pullSweepStops).
	//
	// 🔴 THIS USED TO RETURN AT THE FIRST FAILING REMOTE, which meant the two
	// front-ends disagreed about the same event: the CLI printed every remote and
	// exited 0, while this errored on remote #1 and never mentioned remote #2. There
	// was no single answer to "did the sync succeed?" Now both report EVERY remote
	// and both take the same verdict from storage.RemoteVerdict.
	res, err := storage.Pull(root, remotes)
	// A pull is how this host learns a project left the vault: the index sweep
	// removes its host-local index store now that no vault lock is held.
	indexstore.ReapGoneProjects(context.Background(), storage.NewVault(root))
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
	for _, l := range res.Derived.Lines() {
		fmt.Fprintln(&buf, l)
	}
	for _, remote := range remotes {
		fmt.Fprintf(&buf, "[pull %s] %s\n", remote, strings.TrimSpace(res.RemoteOutput[remote]))
		if rerr := res.RemoteResults[remote]; rerr != nil {
			fmt.Fprintf(&buf, "[pull %s] FAILED: %v\n", remote, rerr)
		}
	}
	if v := storage.RemoteVerdict(storage.OpPull, res.RemoteResults, ""); v != "" {
		// The result body is DISCARDED on a handler error, the same rule the
		// tidy and paths branches follow: what the caller needs to act — above
		// all a departed-slug refusal's paths and hold-branch remedy — has to
		// ride in the error itself, not only in the output it replaces.
		var failed strings.Builder
		for _, remote := range remotes {
			if rerr := res.RemoteResults[remote]; rerr != nil {
				fmt.Fprintf(&failed, "\n[pull %s] FAILED: %v", remote, rerr)
			}
		}
		return buf.String(), fmt.Errorf("%s%s", v, failed.String())
	}
	return buf.String(), nil
}

func gitPush(root string, remotes []string) (string, error) {
	// storage.PushPlain owns the refuse-on-dirty precheck (one implementation,
	// shared with the CLI) and attempts every remote, returning structured
	// per-remote results (mirroring storage.Pull).
	res, err := storage.PushPlain(root, remotes)
	var dirty *storage.DirtyTreeError
	if errors.As(err, &dirty) {
		// Caller friction: the refuse-on-dirty guard worked. Name dirty paths
		// (capped — unbounded single-line MCP errors train agents to skim) and
		// point at remedies an agent can take next turn.
		return "", apperr.Caller(fmt.Errorf("%s", formatDirtyVaultPushError(porcelainDirtyPaths(dirty.Porcelain))))
	}
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
			"entities/triples, and tracked .surface stamps; on a vault without the " +
			"migration marker also drawers and every KG record, on a migrated vault " +
			"only authored KG records and never drawers) with a " +
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

		// git_enabled refuses first, so a dry run refuses too, as the CLI's does.
		if err := storage.RefuseIfGitDisabled(root, "tidy"); err != nil {
			return nil, err
		}

		if p.DryRun {
			res, refusal := storage.TidyPreview(root)
			if res == nil {
				return nil, fmt.Errorf("tidy scan: %w", refusal)
			}
			summary := fmt.Sprintf("dry run: would sweep %d artifact%s, %d reported%s",
				len(res.Swept), plural(len(res.Swept)), len(res.Reported),
				userMemorySummarySuffix(len(res.ReportedUserContent)))
			out := map[string]any{
				"status":                "ok",
				"dry_run":               true,
				"swept":                 res.Swept,
				"reported":              res.Reported,
				"reported_user_content": res.ReportedUserContent,
				"deferred":              res.Deferred,
				"committed":             false,
			}
			// The prediction is a refusal, not a failed dry run: say it in a
			// field and the summary, and keep the result a normal one.
			if refusal != nil {
				out["would_refuse"] = refusal.Error()
				summary += "; a real tidy would refuse (see would_refuse)"
			}
			out["summary"] = summary
			return out, nil
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
		if err := storage.RefuseIfGitDisabled(vault.Root, "report vault status"); err != nil {
			return nil, err
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
	Project     string `json:"project"`
	MaxArchives int    `json:"max_archives"`
	DryRun      bool   `json:"dry_run"`
}

var refreshIndexSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"max_archives": {"type": "integer", "description": "Optional cap on the number of archives the spawned rebuild attempts; 0 (the default) means no cap."},
		"dry_run": {"type": "boolean", "description": "Report the rebuild's plan and disk/inode estimate in-process, writing nothing and taking no lock, instead of starting a rebuild."}
	},
	"required": ["project"]
}`)

func RefreshIndexTool(engine *search.Engine, vault *storage.Vault, launch detachlaunch.LaunchFunc) mcp.Tool {
	return mcp.Tool{
		Name: "vp_refresh_index",
		Description: "Start an explicit, resumable rebuild of a project's host-local search " +
			"index (`vp index rebuild <project>`) as a DETACHED background process, and " +
			"return at once — a rebuild runs for minutes to hours (it ingests the historical " +
			"backlog, re-embeds misses and rebuilds every tier), far longer than an MCP " +
			"client waits. It never runs the rebuild inside this call. Before spawning it " +
			"makes two in-process checks, taking NO lock: it REFUSES a truly empty project " +
			"(no notes, no iterations, no tracked archives, no local chunks, no tracked " +
			"drawers) rather than start a run that can only no-op; and it reads the run " +
			"lock's advisory holder record and, while that names a live process, REFUSES " +
			"naming the holder (pid, kind, project, start time) instead of starting a second " +
			"run. Otherwise it spawns `vp index rebuild` (output to palace/.local/rebuild.log) " +
			"and returns {started: true, pid, log}; the spawned process's own try-lock is the " +
			"authoritative check. Read the run's progress through vp_index_status, never by " +
			"waiting on this call. With dry_run: true it instead runs the preflight in-process " +
			"— the pending archives, the baseline set, the disk/inode estimate and the " +
			"reserve — writing nothing and taking no lock. max_archives caps the spawned run.",
		Schema:  refreshIndexSchema,
		Handler: refreshIndexHandler(engine, vault, launch),
		// Mutating because, on the start path, it launches a process that writes
		// this host's local index under palace/.local/ (the chunk store, the
		// local KG, the ledger and the embed cache). The dry-run path writes
		// nothing, but the tool is declared by its most-privileged path.
		Mutating: true,
	}
}

func refreshIndexHandler(engine *search.Engine, vault *storage.Vault, launch detachlaunch.LaunchFunc) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p refreshIndexParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}

		deps := ingest.Deps{Vault: vault, Engine: engine, Embedder: engine.Embedder()}

		// dry_run: the preflight in-process. It writes nothing and takes no
		// lock, so a trigger that arrives during it is never turned away
		// (ADR-014 decision 7; this child's Scope 2).
		if p.DryRun {
			rep, err := ingest.DryRun(deps, ingest.RebuildOptions{Project: p.Project, MaxArchives: p.MaxArchives})
			if err != nil {
				return nil, fmt.Errorf("refresh index %q: dry run: %w", p.Project, err)
			}
			return refreshDryRunResult(rep), nil
		}

		// "Nothing to refresh": the in-process preflight on child 2's
		// TrulyEmpty, before any spawn. A truly empty project (no notes, no
		// iterations, no tracked archives, no local chunks and no tracked
		// drawers) is refused rather than starting a run that can only no-op.
		empty, err := engine.TrulyEmpty(p.Project)
		if err != nil {
			return nil, fmt.Errorf("check project corpus: %w", err)
		}
		if empty {
			return nil, fmt.Errorf("refresh index %q: nothing to refresh — the project has no "+
				"session notes, no iterations, no tracked transcript archives, no host-local "+
				"chunks, and no tracked drawers, so `vp index rebuild` would index nothing. "+
				"Capture work in this project first; do not delete its history.", p.Project)
		}

		// The run lock's advisory holder record, read WITHOUT taking the lock:
		// a status probe never try-locks, because a colliding trigger would see
		// the lock held, exit, and be lost. While it names a live process,
		// refuse naming the holder; the spawned run's own try-lock stays the
		// authoritative check (ADR-014 lines 613-621).
		if h, herr := indexstore.ReadHolder(vault); herr == nil && pidAlive(h.PID) {
			return nil, fmt.Errorf("refresh index %q: an index run already holds the lock: "+
				"pid %d, kind %s, project %q, started %s. Read its progress with "+
				"vp_index_status; this call started nothing.",
				p.Project, h.PID, h.Kind, h.Project, h.StartTime.UTC().Format(time.RFC3339))
		}

		// Spawn `vp index rebuild` detached and return at once. --vault-root is
		// passed because a detached child's working directory is not the vault.
		// The spawned process's own try-lock is the authoritative check: if a
		// run holds the lock, it exits naming the holder in its log. Because
		// this handler took no lock, a trigger that arrives meanwhile is never
		// turned away by it.
		args := []string{"index", "rebuild", p.Project, "--vault-root", vault.Root}
		if p.MaxArchives > 0 {
			args = append(args, "--max-archives", strconv.Itoa(p.MaxArchives))
		}
		logPath := filepath.Join(vault.VaultLocalDir(), "rebuild.log")
		pid, err := launch("", args, logPath)
		if err != nil {
			// No lock was taken, so no trigger was lost; the next trigger or
			// call serves the project. One warning, then the error.
			slog.Warn("refresh index: could not launch the rebuild", "project", p.Project, "error", err)
			return nil, fmt.Errorf("refresh index %q: launch rebuild: %w", p.Project, err)
		}
		return map[string]any{"started": true, "pid": pid, "log": logPath}, nil
	}
}

// refreshDryRunResult renders a DryRunReport as the tool's result: per project
// the pending archives and the baseline set separately with their bytes, plus
// the estimate, the reserve, the free space and any start refusal.
func refreshDryRunResult(rep ingest.DryRunReport) map[string]any {
	projects := make([]map[string]any, 0, len(rep.Projects))
	for _, pe := range rep.Projects {
		projects = append(projects, map[string]any{
			"project":                     pe.Project,
			"pending_archives":            pe.PendingArchives,
			"pending_compressed_bytes":    pe.PendingCompressedBytes,
			"pending_uncompressed_bytes":  pe.PendingUncompressedBytes,
			"baseline_archives":           pe.BaselineArchives,
			"baseline_compressed_bytes":   pe.BaselineCompressedBytes,
			"baseline_uncompressed_bytes": pe.BaselineUncompressedBytes,
			"estimate_bytes":              pe.Estimate.Bytes,
			"estimate_inodes":             pe.Estimate.Inodes,
		})
	}
	out := map[string]any{
		"dry_run":        true,
		"projects":       projects,
		"reserve_bytes":  rep.Reserve.Bytes,
		"reserve_inodes": rep.Reserve.Inodes,
		"free_bytes":     rep.Free.Bytes,
	}
	if rep.Free.HasInodes {
		out["free_inodes"] = rep.Free.Inodes
	}
	if rep.Refusal != "" {
		out["refusal"] = rep.Refusal
	}
	return out
}

// pidAlive reports whether pid names a live process. The holder record is
// advisory and the spawned rebuild's own try-lock is authoritative, so a
// conservative answer only ever costs an extra harmless spawn. A process alive
// but owned by another user (EPERM) counts as alive.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
