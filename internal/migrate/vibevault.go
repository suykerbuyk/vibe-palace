// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// importMarker is a single JSONL record in the idempotency marker file.
//
// Reason is empty for successful imports. A non-empty Reason flags
// the entry as diagnostic — `isSessionImported` ignores entries whose
// Reason is non-empty so a future re-migration after manual repair
// can still ingest the session.
type importMarker struct {
	SessionID  string `json:"session_id"`
	ImportedAt string `json:"imported_at"`
	Source     string `json:"source"`
	Reason     string `json:"reason,omitempty"`
}

// markerReasonParseFailed flags a session whose frontmatter could not
// be parsed. The marker is written under the tolerant path so the
// operator has a durable audit trail; it does NOT count as imported.
const markerReasonParseFailed = "parse_failed"

// ImportVibeVault migrates session data from a VibeVault-style Projects/
// directory tree into the palace vault, using the capture indexer pipeline.
//
// opts.Resolver controls how slug collisions between sibling project
// directories are resolved. If nil, ImportVibeVault installs a default
// AutoResolver (same behavior as passing --yes).
func ImportVibeVault(
	ctx context.Context,
	source *storage.Vault,
	destination *storage.Vault,
	engine *search.Engine,
	emb embedder.Embedder,
	cfg storage.Config,
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
	result.ProjectsScanned = len(projects)
	result.SlugRemap = remap

	// Step 2: create a single indexer for the entire run. All indexer
	// writes land in the DESTINATION vault.
	indexer := capture.NewIndexer(destination, engine, emb, cfg)

	// Step 3: process each project.
	for dirPath, projSlug := range projects {
		progress(opts, ProgressEvent{
			Type:    ProgressProjectStart,
			Project: projSlug,
		})

		// Seed the vault-project config.toml (and its tasks/ subdirs) via
		// the VaultProject reconciler — the sole orchestrator for this
		// artifact. Check → Plan → Apply preserves write-only-if-absent
		// semantics (a present file becomes Unchanged or Update, never
		// clobbered) while adding drift-detection parity with `vp init`.
		// Skipped in dry-run. Non-fatal on error — session import
		// continues.
		if !opts.DryRun {
			if err := reconcileVaultProject(ctx, destination, projSlug); err != nil {
				log.Printf("migrate: reconcile vault-project config %s: %v", projSlug, err)
			}
		}

		// Gather session files.
		sessionsDir := filepath.Join(dirPath, "sessions")
		sessionFiles, _ := filepath.Glob(filepath.Join(sessionsDir, "*.md"))

		total := len(sessionFiles)

		for i, sf := range sessionFiles {
			if err := ctx.Err(); err != nil {
				return result, err
			}

			data, readErr := os.ReadFile(sf)
			if readErr != nil {
				result.Errors = append(result.Errors, ImportError{
					Project: projSlug,
					File:    sf,
					Err:     readErr,
				})
				progress(opts, ProgressEvent{
					Type:    ProgressError,
					Project: projSlug,
					Message: readErr.Error(),
				})
				continue
			}

			meta, body, parseErr := storage.ParseFrontmatter(data)
			if parseErr != nil {
				// Frontmatter parse failed — meta.ID is unreliable, so
				// derive the session ID from the filename.
				failedID := strings.TrimSuffix(filepath.Base(sf), ".md")

				result.Errors = append(result.Errors, ImportError{
					Project:   projSlug,
					SessionID: failedID,
					File:      sf,
					Err:       parseErr,
				})
				progress(opts, ProgressEvent{
					Type:      ProgressError,
					Project:   projSlug,
					SessionID: failedID,
					File:      sf,
					Message:   parseErr.Error(),
				})

				if opts.Strict {
					return result, fmt.Errorf("parse frontmatter %s: %w", sf, parseErr)
				}

				// Tolerant path: emit a skip event so per-project
				// counters stay accurate, then record a parse_failed
				// marker (real runs only — dry-run never writes
				// markers) so the operator has a durable audit trail.
				result.SessionsSkipped++
				progress(opts, ProgressEvent{
					Type:      ProgressSessionSkip,
					Project:   projSlug,
					SessionID: failedID,
					File:      sf,
					Current:   i + 1,
					Total:     total,
				})
				if !opts.DryRun {
					if markErr := markSessionParseFailed(destination, projSlug, failedID); markErr != nil {
						log.Printf("migrate: record parse_failed marker %s/%s: %v", projSlug, failedID, markErr)
					}
				}
				continue
			}

			sessionID := meta.ID
			if sessionID == "" {
				sessionID = strings.TrimSuffix(filepath.Base(sf), ".md")
			}

			// Idempotency check.
			imported, checkErr := isSessionImported(destination, projSlug, sessionID)
			if checkErr != nil {
				result.Errors = append(result.Errors, ImportError{
					Project:   projSlug,
					SessionID: sessionID,
					File:      sf,
					Err:       checkErr,
				})
				continue
			}
			if imported {
				result.SessionsSkipped++
				progress(opts, ProgressEvent{
					Type:      ProgressSessionSkip,
					Project:   projSlug,
					SessionID: sessionID,
					Current:   i + 1,
					Total:     total,
				})
				continue
			}

			if opts.DryRun {
				result.SessionsImported++
				progress(opts, ProgressEvent{
					Type:      ProgressSessionDone,
					Project:   projSlug,
					SessionID: sessionID,
					Current:   i + 1,
					Total:     total,
				})
				continue
			}

			// Determine transcript text.
			transcript := strings.TrimSpace(body)
			if transcript == "" {
				transcript = strings.TrimSpace(meta.Summary)
			}

			// Index the transcript (even if empty — let the indexer decide).
			idxStats, idxErr := indexer.IndexTranscript(ctx, sessionID, projSlug, transcript)
			if idxErr != nil {
				result.Errors = append(result.Errors, ImportError{
					Project:   projSlug,
					SessionID: sessionID,
					File:      sf,
					Err:       idxErr,
				})
				progress(opts, ProgressEvent{
					Type:    ProgressError,
					Project: projSlug,
					Message: idxErr.Error(),
				})
				continue
			}
			result.DrawersCreated += idxStats.Drawers
			result.EntitiesCreated += idxStats.Entities
			result.TriplesCreated += idxStats.Triples

			// Mark as imported.
			if markErr := markSessionImported(destination, projSlug, sessionID, "vibevault"); markErr != nil {
				result.Errors = append(result.Errors, ImportError{
					Project:   projSlug,
					SessionID: sessionID,
					File:      sf,
					Err:       markErr,
				})
				continue
			}

			result.SessionsImported++
			progress(opts, ProgressEvent{
				Type:      ProgressSessionDone,
				Project:   projSlug,
				SessionID: sessionID,
				Current:   i + 1,
				Total:     total,
			})
		}

		// Step 4: import knowledge.md if present.
		knowledgePath := filepath.Join(dirPath, "knowledge.md")
		if data, err := os.ReadFile(knowledgePath); err == nil {
			text := strings.TrimSpace(string(data))
			if text != "" && !opts.DryRun {
				knowledgeID := "knowledge-" + projSlug
				kStats, err := indexer.IndexTranscript(ctx, knowledgeID, projSlug, text)
				if err != nil {
					result.Errors = append(result.Errors, ImportError{
						Project:   projSlug,
						SessionID: knowledgeID,
						File:      knowledgePath,
						Err:       err,
					})
				} else {
					result.DrawersCreated += kStats.Drawers
					result.EntitiesCreated += kStats.Entities
					result.TriplesCreated += kStats.Triples
				}
			}
		}

		progress(opts, ProgressEvent{
			Type:    ProgressProjectDone,
			Project: projSlug,
		})
	}

	return result, nil
}

// reconcileVaultProject delegates vault-project config.toml creation to
// the VaultProject reconciler (Check → Plan → Apply). This is the sole
// orchestration path for `<vault>/Projects/<slug>/config.toml`; migrate
// no longer calls storage.WriteVaultProjectConfig directly.
//
// The reconciler's Plan does not currently emit ActionPrompt for this
// tier, so migrate runs non-interactively with no ActionPrompt callback.
// Returns the first error encountered in Apply's Report, if any.
func reconcileVaultProject(ctx context.Context, vault *storage.Vault, projSlug string) error {
	r := reconcile.NewVaultProject(vault, projSlug)
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
			if verr := slug.Validate(newSlug); verr != nil {
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

// markerPath returns the path to the idempotency marker file for a
// project. Markers live in the DESTINATION vault — they record what has
// been written there, never anything about the source tree.
func markerPath(destination *storage.Vault, project string) (string, error) {
	localDir, err := destination.LocalDir(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(localDir, "imported-sessions.jsonl"), nil
}

// isSessionImported checks whether a session has already been imported
// by scanning the JSONL marker file in the DESTINATION vault.
func isSessionImported(destination *storage.Vault, project, sessionID string) (bool, error) {
	path, err := markerPath(destination, project)
	if err != nil {
		return false, err
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m importMarker
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue // skip malformed lines
		}
		if m.SessionID == sessionID && m.Reason == "" {
			return true, nil
		}
	}
	return false, nil
}

// markSessionImported appends a successful-import JSONL record to the
// DESTINATION vault.
func markSessionImported(destination *storage.Vault, project, sessionID, source string) error {
	return markSessionWithReason(destination, project, sessionID, source, "")
}

// markSessionParseFailed appends a parse_failed JSONL record so the
// operator has a durable audit trail. Entries with this reason do NOT
// count as imported (see isSessionImported), so re-running migrate
// after manual file repair will pick the session up.
func markSessionParseFailed(destination *storage.Vault, project, sessionID string) error {
	return markSessionWithReason(destination, project, sessionID, "vibevault", markerReasonParseFailed)
}

// markSessionWithReason appends a JSONL record to the idempotency marker
// file in the DESTINATION vault. An empty reason marks a successful
// import; a non-empty reason is diagnostic-only.
func markSessionWithReason(destination *storage.Vault, project, sessionID, source, reason string) error {
	path, err := markerPath(destination, project)
	if err != nil {
		return err
	}

	if err := storage.EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}

	m := importMarker{
		SessionID:  sessionID,
		ImportedAt: time.Now().UTC().Format(time.RFC3339),
		Source:     source,
		Reason:     reason,
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "%s\n", data)
	return err
}

// progress calls the progress callback if non-nil.
func progress(opts ImportOptions, evt ProgressEvent) {
	if opts.Progress != nil {
		opts.Progress(evt)
	}
}
