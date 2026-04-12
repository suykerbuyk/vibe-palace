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
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// importMarker is a single JSONL record in the idempotency marker file.
type importMarker struct {
	SessionID  string `json:"session_id"`
	ImportedAt string `json:"imported_at"`
	Source     string `json:"source"`
}

// ImportVibeVault migrates session data from a VibeVault-style Projects/
// directory tree into the palace vault, using the capture indexer pipeline.
//
// opts.Resolver controls how slug collisions between sibling project
// directories are resolved. If nil, ImportVibeVault installs a default
// AutoResolver (same behavior as passing --yes).
func ImportVibeVault(
	ctx context.Context,
	vault *storage.Vault,
	engine *search.Engine,
	emb embedder.Embedder,
	cfg storage.Config,
	opts ImportOptions,
) (ImportResult, error) {
	var result ImportResult

	// Step 1: scan projects and build slug map.
	resolver := opts.Resolver
	if resolver == nil {
		onDisk, err := scanOnDiskSlugs(filepath.Join(vault.Root, "Projects"))
		if err != nil {
			return result, fmt.Errorf("scan existing slugs: %w", err)
		}
		resolver = &AutoResolver{OnDisk: onDisk}
	}

	projects, remap, err := scanProjects(vault.Root, resolver)
	if err != nil {
		return result, fmt.Errorf("scan projects: %w", err)
	}
	result.ProjectsScanned = len(projects)
	result.SlugRemap = remap

	// Step 2: create a single indexer for the entire run.
	indexer := capture.NewIndexer(vault, engine, emb, cfg)

	// Step 3: process each project.
	for dirPath, projSlug := range projects {
		progress(opts, ProgressEvent{
			Type:    ProgressProjectStart,
			Project: projSlug,
		})

		// Seed the vault-project config.toml with a commented template so
		// users see what's per-project tunable. Write-only-if-absent —
		// does not clobber any config a user (or a prior migration) put
		// in place. Skipped in dry-run.
		if !opts.DryRun {
			if _, _, err := vault.WriteVaultProjectConfig(projSlug); err != nil {
				log.Printf("migrate: write vault-project config %s: %v", projSlug, err)
				// Non-fatal: continue importing sessions.
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
				result.Errors = append(result.Errors, ImportError{
					Project:   projSlug,
					SessionID: meta.ID,
					File:      sf,
					Err:       parseErr,
				})
				progress(opts, ProgressEvent{
					Type:    ProgressError,
					Project: projSlug,
					Message: parseErr.Error(),
				})
				continue
			}

			sessionID := meta.ID
			if sessionID == "" {
				sessionID = strings.TrimSuffix(filepath.Base(sf), ".md")
			}

			// Idempotency check.
			imported, checkErr := isSessionImported(vault, projSlug, sessionID)
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
			idxErr := indexer.IndexTranscript(ctx, sessionID, projSlug, transcript)
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

			// Mark as imported.
			if markErr := markSessionImported(vault, projSlug, sessionID, "vibevault"); markErr != nil {
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
				if err := indexer.IndexTranscript(ctx, knowledgeID, projSlug, text); err != nil {
					result.Errors = append(result.Errors, ImportError{
						Project:   projSlug,
						SessionID: knowledgeID,
						File:      knowledgePath,
						Err:       err,
					})
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

// markerPath returns the path to the idempotency marker file for a project.
func markerPath(vault *storage.Vault, project string) (string, error) {
	localDir, err := vault.LocalDir(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(localDir, "imported-sessions.jsonl"), nil
}

// isSessionImported checks whether a session has already been imported
// by scanning the JSONL marker file.
func isSessionImported(vault *storage.Vault, project, sessionID string) (bool, error) {
	path, err := markerPath(vault, project)
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
		if m.SessionID == sessionID {
			return true, nil
		}
	}
	return false, nil
}

// markSessionImported appends a JSONL record to the idempotency marker file.
func markSessionImported(vault *storage.Vault, project, sessionID, source string) error {
	path, err := markerPath(vault, project)
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
