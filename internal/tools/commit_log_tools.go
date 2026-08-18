// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// vp_archive_commit_log — archive-at-wrap of the landed commit messages.
//
// This is the feature-branch half of the two-path commit-message archive. The
// direct-to-main path keeps vp_ingest_commit_msg (Step 7): it mirrors the
// message the human is ABOUT to commit into the vault commit.msg while the tree
// is still dirty. But a feature-branch commit has ALREADY landed by wrap time
// (the tree is clean), so there is nothing dirty for that guard to describe and
// the message never reaches the vault.
//
// vp_archive_commit_log closes that gap by DERIVING the archive from git rather
// than from an authored-ahead file: it walks git log over the PROJECT repo from
// the last-archived anchor to HEAD, appends each real commit's full message to
// Projects/<slug>/commit-log.md, and advances the anchor. It is tool-agnostic
// (it captures whatever landed, by whoever/whatever) and idempotent (a second
// run with no new commits appends nothing), so it needs no call-order
// discipline — which is the whole point. It writes vault artifacts through the
// storage layer, never a hand-rolled os.WriteFile.

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

var archiveCommitLogSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug. If omitted, detected from project_path."},
		"project_path": {"type": "string", "description": "Absolute path to the local project repo root whose git history is walked. Required."}
	},
	"required": ["project_path"]
}`)

// ArchiveCommitLogTool walks git log <last-archived>..HEAD over the project
// repo and appends each landed commit's full message to the vault
// commit-log.md, advancing the anchor.
func ArchiveCommitLogTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_archive_commit_log",
		Mutating: true,
		Description: "Archive the messages of the commits that have LANDED in the " +
			"project repo since the last archive into the vault permanent history " +
			"Projects/<slug>/commit-log.md, then advance the last-archived anchor. " +
			"Walks git log <last-archived>..HEAD over project_path (the local repo " +
			"root) and appends each real commit's full message — DERIVED from git, " +
			"never authored ahead. On the first run (no anchor yet) it seeds from " +
			"the repo's oldest root commit. It is idempotent: a re-run with no new " +
			"commits appends nothing. This is the feature-branch counterpart to " +
			"vp_ingest_commit_msg (which stays for the direct-to-main path); run it " +
			"every wrap regardless of flow. Returns {project, commits_archived, " +
			"anchor_from, anchor_to, commit_log_path, anchor_path}.",
		Schema: archiveCommitLogSchema,
		Handler: func(ctx context.Context, params json.RawMessage) (any, error) {
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

			slug, err := resolveWrapProject(args.Project, args.ProjectPath)
			if err != nil {
				return nil, fmt.Errorf("detect project from %q: %w", args.ProjectPath, err)
			}
			projectRoot := wrapstate.ResolveProjectRoot(args.ProjectPath)

			// Refuse to scaffold a phantom vault project from an unmanaged
			// directory: vault.ArchiveCommitBodies lazily creates Projects/<slug>/
			// on first write. Key on the resolved repo ROOT.
			if err := project.RequireKnownProject(slug, vault.Root, projectRoot); err != nil {
				return nil, err
			}

			runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			// HEAD is where the archive will be current through. No HEAD (not a
			// repo, unborn branch) means there is nothing to archive — no-op.
			head, err := wrapstate.HeadSHA(runCtx, projectRoot)
			if err != nil {
				return nil, fmt.Errorf("resolve HEAD: %w", err)
			}
			if head == "" {
				return map[string]any{
					"project":          slug,
					"commits_archived": 0,
					"anchor_from":      "",
					"anchor_to":        "",
					"note":             "project_path is not a git repo with commits — nothing to archive",
				}, nil
			}

			anchor, err := vault.ReadCommitLogAnchor(slug)
			if err != nil {
				return nil, err
			}

			// Refuse an anchor that does not describe this repo's history
			// rather than emitting whatever the walk happens to produce. An
			// anchor off the HEAD line (rebase, abandoned branch) makes
			// <anchor>..HEAD a symmetric difference, so the walk re-yields
			// commits already archived on the surviving line AND the anchor
			// itself names a commit that never landed — which is how the
			// permanent history came to record an orphan (iter 281).
			if err := wrapstate.ValidateAnchorAgainstHEAD(runCtx, projectRoot, anchor); err != nil {
				anchorPath, _ := vault.CommitLogAnchorFile(slug)
				switch {
				case errors.Is(err, wrapstate.ErrAnchorNotAncestor):
					return nil, fmt.Errorf(
						"%w.\n"+
							"  anchor: %s\n"+
							"  HEAD:   %s\n"+
							"  file:   %s\n"+
							"The anchor names a commit this repo has, but HEAD does not descend from it — "+
							"a rebase or an abandoned branch left it off the line. Walking <anchor>..HEAD "+
							"from here would re-archive commits already in commit-log.md and record a "+
							"commit that never landed.\n"+
							"Remediation: set the anchor to the last commit ALREADY archived that is still "+
							"an ancestor of HEAD, then re-run. Verify with:\n"+
							"  git merge-base --is-ancestor <sha> HEAD && echo ok\n"+
							"Do not hand-edit commit-log.md to reconcile it; existing entries are the "+
							"honest record of what the old writer wrote.",
						err, anchor, head, anchorPath)
				case errors.Is(err, wrapstate.ErrAnchorUnresolvable):
					return nil, fmt.Errorf(
						"%w.\n"+
							"  anchor: %s\n"+
							"  file:   %s\n"+
							"This clone cannot resolve the anchor commit — it was garbage-collected, or "+
							"this is a partial/network-isolated clone that never fetched it.\n"+
							"Remediation: fetch the missing history, or set the anchor to a commit this "+
							"clone holds that is an ancestor of HEAD.",
						err, anchor, anchorPath)
				default:
					return nil, err
				}
			}
			// First-run seed: mirror wrapstate.Collect's empty-anchor handling —
			// the oldest root commit is the exclusive lower bound, so the very
			// first commit is not archived, but every commit after it is (a
			// one-time backfill on first ever run).
			scanFrom := anchor
			if scanFrom == "" {
				root, rerr := wrapstate.OldestRootCommit(runCtx, projectRoot)
				if rerr != nil {
					return nil, fmt.Errorf("oldest root commit: %w", rerr)
				}
				scanFrom = root
			}

			var commits []wrapstate.CommitInfo
			if scanFrom != "" {
				commits, err = wrapstate.CommitBodiesSinceAnchor(runCtx, projectRoot, scanFrom)
				if err != nil {
					return nil, fmt.Errorf("commit bodies since anchor: %w", err)
				}
			}

			appended, skipped, err := vault.ArchiveCommitBodies(slug, commits, head)
			if err != nil {
				return nil, err
			}

			logPath, _ := vault.CommitLogFile(slug)
			anchorPath, _ := vault.CommitLogAnchorFile(slug)
			return map[string]any{
				"project":          slug,
				"commits_archived": appended,
				// Commits the walk yielded that commit-log.md already held —
				// normally another host archived them. Reported rather than
				// silently dropped: a non-zero value is the signal that two
				// hosts' anchors have diverged over one shared log.
				"duplicates_skipped": skipped,
				"anchor_from":        anchor,
				"anchor_to":          head,
				"commit_log_path":    logPath,
				"anchor_path":        anchorPath,
			}, nil
		},
	}
}
