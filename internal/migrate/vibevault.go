// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// markerReasonParseFailed flags a session whose frontmatter could not
// be parsed. The marker is written under the tolerant path so the
// operator has a durable audit trail; it does NOT count as imported.
const markerReasonParseFailed = "parse_failed"

// ImportVibeVault migrates session data from a VibeVault-style Projects/
// directory tree into the palace vault as transcript archives: each session
// with text becomes an inline-adapter archive under
// Projects/<slug>/transcripts/, dated by the session's own date, and nothing
// is chunked, embedded or written to a tracked derived file (task
// importers-write-the-frozen-tracked-corpus, Scope 3). It loads no model. The
// archives reach the search index only through the host-local pending-archive
// ingester, which a later release adds.
//
// opts.Resolver controls how slug collisions between sibling project
// directories are resolved. If nil, ImportVibeVault installs a default
// AutoResolver (same behavior as passing --yes).
func ImportVibeVault(
	ctx context.Context,
	source *storage.Vault,
	destination *storage.Vault,
	opts ImportOptions,
) (ImportResult, error) {
	var result ImportResult

	// Step 1: scan projects and build slug map. Reads come from the
	// SOURCE vault tree only.
	resolver := opts.Resolver
	if resolver == nil {
		onDisk, err := scanOnDiskSlugs(filepath.Join(source.Root, "Projects"))
		if err != nil {
			return result, fmt.Errorf("scan existing slugs: %w", err)
		}
		resolver = &AutoResolver{OnDisk: onDisk}
	}

	projects, remap, err := scanProjects(source.Root, resolver)
	if err != nil {
		return result, fmt.Errorf("scan projects: %w", err)
	}

	// Restrict to the requested projects (matched by either the slugified
	// source dir name or the final post-remap slug) so a targeted run does
	// not fan out across an entire shared source vault.
	if len(opts.OnlyProjects) > 0 {
		only := make(map[string]bool, len(opts.OnlyProjects))
		for _, s := range opts.OnlyProjects {
			only[s] = true
		}
		for dir, finalSlug := range projects {
			orig := slug.Slugify(filepath.Base(dir))
			if !only[orig] && !only[finalSlug] {
				delete(projects, dir)
			}
		}
	}

	result.ProjectsScanned = len(projects)
	result.SlugRemap = remap

	// Step 3: process each project.
	for dirPath, projSlug := range projects {
		progress(opts, ProgressEvent{
			Type:    ProgressProjectStart,
			Project: projSlug,
		})

		// Initialise the destination project the way `vp init` does: the
		// Projects/<slug>/{commands,skills}/ README scaffold, which is the
		// marker storage.ClassifyProjectDir reads as an initialised project.
		// Write-only-if-absent: a present README is Unchanged, never
		// clobbered. Skipped in dry-run. Non-fatal on error — session import
		// continues.
		if !opts.DryRun {
			if err := scaffoldProject(ctx, destination, projSlug); err != nil {
				log.Printf("migrate: scaffold project %s: %v", projSlug, err)
			}
		}

		// Carry the agentctx tree (resume/iterations/workflow/knowledge/
		// tasks/memory + the verbatim migrated/ archive) when requested.
		// Pure file IO — runs in dry-run too (preview), needs no embedder.
		if opts.WithAgentctx {
			actx, aerr := copyAgentctx(destination, dirPath, projSlug, opts)
			result.Agentctx.Copied += actx.Copied
			result.Agentctx.Skipped += actx.Skipped
			result.Agentctx.Bytes += actx.Bytes
			result.Agentctx.CopiedPaths = append(result.Agentctx.CopiedPaths, actx.CopiedPaths...)
			result.Agentctx.SkippedPaths = append(result.Agentctx.SkippedPaths, actx.SkippedPaths...)
			result.Agentctx.CrownJewelSkipped = append(result.Agentctx.CrownJewelSkipped, actx.CrownJewelSkipped...)
			if aerr != nil {
				result.Errors = append(result.Errors, ImportError{Project: projSlug, Err: aerr})
				progress(opts, ProgressEvent{Type: ProgressError, Project: projSlug, Message: aerr.Error()})
			}
		}

		if !opts.SkipSessions {
			if err := importProjectSessions(ctx, destination, dirPath, projSlug, opts, &result); err != nil {
				return result, err
			}
		}

		progress(opts, ProgressEvent{
			Type:    ProgressProjectDone,
			Project: projSlug,
		})
	}

	return result, nil
}

// scaffoldProject lays down Projects/<slug>/{commands,skills}/ with README
// stubs through the same TemplateTree scaffold `vp init`'s project-scaffold
// step and `vp config sync` use, so the three cannot disagree about what an
// initialised project is. It replaced the retired vault-project reconciler,
// which wrote Projects/<slug>/config.toml and tasks/{done,cancelled}: the
// config is no longer written by anything, and the task archive directories
// are created on first use by the task mover.
//
// No reconciler prompts; migrate runs non-interactively.
// Returns the first error encountered in Apply's Report, if any.
func scaffoldProject(ctx context.Context, vault *storage.Vault, projSlug string) error {
	r := reconcile.NewTemplateTree(vault.Root, "Projects/"+projSlug, reconcile.TemplateTreeSeed{
		Mode: reconcile.TemplateModeScaffold,
	})
	plan, err := r.Plan(ctx)
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	rep, err := r.Apply(ctx, plan)
	if err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	if len(rep.Errors) > 0 {
		return rep.Errors[0]
	}
	return nil
}

// scanProjects scans {vaultRoot}/Projects/ and returns a map of
// original directory path to slugified project name. Slug collisions
// are delegated to the SlugResolver. The second return value is a map
// of originalSlug → finalSlug for every directory whose slug was
// renamed; unchanged entries are omitted.
//
// Processing order is os.ReadDir order (sorted by name). The first
// directory to claim a slug keeps it; later colliders are renamed.
func scanProjects(vaultRoot string, resolver SlugResolver) (map[string]string, map[string]string, error) {
	projectsDir := filepath.Join(vaultRoot, "Projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil, nil, fmt.Errorf("read Projects dir: %w", err)
	}

	onDisk, err := scanOnDiskSlugs(projectsDir)
	if err != nil {
		return nil, nil, fmt.Errorf("scan on-disk slugs: %w", err)
	}

	result := make(map[string]string, len(entries))
	slugToDir := make(map[string]string, len(entries))
	taken := make(map[string]bool, len(entries))
	remap := make(map[string]string)

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		s := slug.Slugify(name)
		if s == "" {
			log.Printf("migrate: skipping project dir %q (empty slug)", name)
			continue
		}

		dirPath := filepath.Join(projectsDir, name)

		if prev, ok := slugToDir[s]; ok {
			if resolver == nil {
				return nil, nil, fmt.Errorf(
					"slug collision: directories %q and %q both map to slug %q (no resolver configured)",
					prev, dirPath, s,
				)
			}
			newSlug, rerr := resolver.Resolve(dirPath, prev, s, taken)
			if rerr != nil {
				return nil, nil, rerr
			}
			if verr := slug.ValidateCreatable(newSlug); verr != nil {
				return nil, nil, fmt.Errorf("resolver returned invalid slug %q: %w", newSlug, verr)
			}
			if taken[newSlug] {
				return nil, nil, fmt.Errorf("resolver returned slug %q that is already in use this scan", newSlug)
			}
			if onDisk[newSlug] {
				return nil, nil, fmt.Errorf("resolver returned slug %q that already exists on disk", newSlug)
			}
			remap[s] = newSlug
			s = newSlug
		}
		slugToDir[s] = dirPath
		taken[s] = true
		result[dirPath] = s
	}

	return result, remap, nil
}

// progress calls the progress callback if non-nil.
func progress(opts ImportOptions, evt ProgressEvent) {
	if opts.Progress != nil {
		opts.Progress(evt)
	}
}
