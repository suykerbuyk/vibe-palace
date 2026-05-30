// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// SessionParams contains all inputs for a session capture. Both the MCP
// handler and the hook command populate this struct.
type SessionParams struct {
	Project          string
	Summary          string
	Title            string
	Tag              string
	Model            string
	Decisions        []string
	FilesChanged     []string
	OpenThreads      []string
	Transcript       string // raw transcript for friction analysis
	ArchiveSessionID string
	ArchiveAdapter   string
	CWD              string // for claim sentinel (Phase 3 will use this)
	NeedsIndexing    bool   // when true, skip indexing (deferred to later)
}

// SessionResult is the outcome of a successful session capture.
type SessionResult struct {
	Status        string
	Project       string
	NotePath      string
	Iteration     int
	SessionID     string
	FrictionScore int
}

// WriteSession performs the full capture pipeline: title defaulting,
// metadata population, archive resolution, friction analysis, body
// building, vault write, read-back, bidirectional archive linking,
// and optional transcript indexing. Both the MCP handler and the hook
// command call this.
func WriteSession(ctx context.Context, vault *storage.Vault, indexer *Indexer, p SessionParams) (*SessionResult, error) {
	if p.Project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if p.Summary == "" {
		return nil, fmt.Errorf("summary is required")
	}

	now := time.Now().UTC()
	date := now.Format("2006-01-02")

	title := p.Title
	if title == "" {
		title = p.Summary
		if len(title) > 80 {
			title = title[:80]
		}
	}

	meta := storage.SessionMeta{
		Date:         date,
		Title:        title,
		Summary:      p.Summary,
		Tag:          p.Tag,
		Model:        p.Model,
		Decisions:    p.Decisions,
		FilesChanged: p.FilesChanged,
		OpenThreads:  p.OpenThreads,
	}

	// Resolve the archive (if requested) before writing the session
	// so the note can carry the archive: frontmatter field. The
	// note -> manifest back-link is closed after the write below.
	var archiveEntry *archive.Entry
	if p.ArchiveSessionID != "" {
		adapter := p.ArchiveAdapter
		if adapter == "" {
			adapter = archive.ClaudeCodeAdapterName
		}
		if e, err := archive.ResolveEntry(vault.Root, p.Project, p.ArchiveSessionID); err == nil && e.Manifest.Adapter == adapter {
			archiveEntry = e
			meta.Archive = archive.VaultRelPath(vault.Root, e.ManifestPath)
		}
		// A missing archive is non-fatal -- capture still succeeds
		// without the link. Future hook runs that archive after
		// capture can call LinkSessionNote to close the loop.
	}

	// Score transcript friction before writing session.
	if p.Transcript != "" {
		if score, err := AnalyzeFriction(p.Transcript); err == nil {
			meta.FrictionScore = score
		}
	}

	body := buildSessionBody(p)

	sessionID, err := vault.WriteSession(p.Project, meta, body)
	if err != nil {
		return nil, fmt.Errorf("write session: %w", err)
	}

	// Parse iteration from sessionID (format: "YYYY-MM-DD-NN").
	iteration := ParseIteration(sessionID)

	// Read session back to get the note path.
	readMeta, _, readErr := vault.ReadSession(p.Project, date, iteration)
	notePath := ""
	if readErr == nil {
		notePath = readMeta.NotePath
	}

	// Close the bidirectional link: write vault_rel_session_note
	// into the archive manifest. Non-fatal on failure -- the
	// note's archive: field still provides one-way traversal.
	if archiveEntry != nil {
		sessionAbs, err := vault.SessionFile(p.Project, date, iteration)
		if err == nil {
			rel := archive.VaultRelPath(vault.Root, sessionAbs)
			_ = archive.LinkSessionNote(archiveEntry.ManifestPath, rel)
		}
	}

	// Index transcript if provided, unless NeedsIndexing signals deferral.
	// Per-call IndexStats are not surfaced through SessionResult today;
	// discard with `_` and revisit if dogfood-log telemetry needs them.
	if p.Transcript != "" && indexer != nil && !p.NeedsIndexing {
		if _, err := indexer.IndexTranscript(ctx, sessionID, p.Project, p.Transcript); err != nil {
			// Non-fatal: session was captured, indexing failed.
			return &SessionResult{
				Status:        "partial",
				Project:       p.Project,
				NotePath:      notePath,
				Iteration:     iteration,
				SessionID:     sessionID,
				FrictionScore: meta.FrictionScore,
			}, nil
		}
	}

	return &SessionResult{
		Status:        "ok",
		Project:       p.Project,
		NotePath:      notePath,
		Iteration:     iteration,
		SessionID:     sessionID,
		FrictionScore: meta.FrictionScore,
	}, nil
}

// buildSessionBody assembles the markdown body for a session file.
func buildSessionBody(p SessionParams) string {
	var b []string
	b = append(b, "\n## Summary\n")
	b = append(b, p.Summary)

	if len(p.Decisions) > 0 {
		b = append(b, "\n\n## Decisions\n")
		for _, d := range p.Decisions {
			b = append(b, "- "+d)
		}
	}

	if len(p.FilesChanged) > 0 {
		b = append(b, "\n\n## Files Changed\n")
		for _, f := range p.FilesChanged {
			b = append(b, "- `"+f+"`")
		}
	}

	if len(p.OpenThreads) > 0 {
		b = append(b, "\n\n## Open Threads\n")
		for _, t := range p.OpenThreads {
			b = append(b, "- "+t)
		}
	}

	return joinLines(b)
}

func joinLines(lines []string) string {
	result := ""
	for _, l := range lines {
		result += l + "\n"
	}
	return result
}

// ParseIteration extracts the iteration number from a session ID (format: "YYYY-MM-DD-NN").
func ParseIteration(sessionID string) int {
	parts := strings.Split(sessionID, "-")
	if len(parts) < 4 {
		return 0
	}
	n, _ := strconv.Atoi(parts[len(parts)-1])
	return n
}
