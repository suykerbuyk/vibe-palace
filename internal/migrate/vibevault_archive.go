// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The vibevault importer writes transcript archives, never tracked derived
// files (task importers-write-the-frozen-tracked-corpus, Scope 3 and plan
// revisions R-2, R-8, R-9). Each imported session becomes an inline-adapter
// archive dated by the session's own date; the host-local pending-archive
// ingester indexes archives once it ships.

// Marker reasons. An empty reason is a successful import; the others are
// diagnostic, except markerReasonEmpty, which also counts as done (a session
// with no text has no archive to confirm it).
const (
	markerReasonEmpty   = "empty"
	markerReasonNoDate  = "no_date"
	markerReasonBatchID = "batch_id_refused"
)

// knowledgeEpoch dates a knowledge.md whose frontmatter has no date: a fixed
// instant, so every host computes the same archive (doc/MIGRATION.md).
var knowledgeEpoch = time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC)

// batchIDLike matches a mempalace import batch id. A vibevault session id of
// that form is refused: the ledger answers StartDay for sessions and batches
// alike, so a session must never carry a batch's id.
var batchIDLike = regexp.MustCompile(`^mempalace:[0-9a-f]{64}:`)

// importMarker is one line of the import marker file.
type importMarker struct {
	SessionID  string `json:"session_id"`
	ImportedAt string `json:"imported_at"`
	Source     string `json:"source"`
	Reason     string `json:"reason,omitempty"`
}

// markerFile is the project's import marker in the DESTINATION vault:
// palace/.local/imports/<p>/imported-sessions.jsonl.
func markerFile(destination *storage.Vault, project string) (string, error) {
	dir, err := destination.ImportsDir(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "imported-sessions.jsonl"), nil
}

// legacyMarkerFile is where markers lived before: palace/<p>/.local/, which is
// not ignored and showed as vault dirt.
func legacyMarkerFile(destination *storage.Vault, project string) (string, error) {
	dir, err := destination.LocalDir(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "imported-sessions.jsonl"), nil
}

// markers is the project's marker file, read once per run: every reason
// recorded for each session id.
type markers map[string][]string

// loadMarkers reads the project's markers. A legacy marker file is read once:
// its lines are appended to the new file, it is deleted, and its emptied
// palace/<p>/.local/ directory is removed.
func loadMarkers(destination *storage.Vault, project string) (markers, error) {
	path, err := markerFile(destination, project)
	if err != nil {
		return nil, err
	}
	legacy, err := legacyMarkerFile(destination, project)
	if err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(legacy); err == nil {
		if err := storage.EnsureDir(filepath.Dir(path)); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		_, werr := f.Write(data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return nil, fmt.Errorf("move legacy import marker: %w", werr)
		}
		if err := os.Remove(legacy); err != nil {
			return nil, fmt.Errorf("remove legacy import marker: %w", err)
		}
		_ = os.Remove(filepath.Dir(legacy)) // only when empty
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read legacy import marker: %w", err)
	}

	m := markers{}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var mk importMarker
		if err := json.Unmarshal([]byte(line), &mk); err != nil {
			continue // a malformed line is skipped
		}
		m[mk.SessionID] = append(m[mk.SessionID], mk.Reason)
	}
	return m, nil
}

// done reports whether a session counts as imported. The marker is only a
// HINT: a successful-import line counts only while the vault holds a manifest
// for that session (archived), so a marker left behind by a deleted,
// recreated, renamed or departed project never suppresses an import. A
// session recorded as empty has no archive by design and counts as done.
func (m markers) done(sessionID string, archived map[string]map[string]bool) bool {
	for _, reason := range m[sessionID] {
		switch reason {
		case "":
			if len(archived[sessionID]) > 0 {
				return true
			}
		case markerReasonEmpty:
			return true
		}
	}
	return false
}

// appendMarker appends one marker line for project.
func appendMarker(destination *storage.Vault, project, sessionID, source, reason string) error {
	path, err := markerFile(destination, project)
	if err != nil {
		return err
	}
	if err := storage.EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.Marshal(importMarker{
		SessionID:  sessionID,
		ImportedAt: time.Now().UTC().Format(time.RFC3339),
		Source:     source,
		Reason:     reason,
	})
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

// archivedSessions maps each session id archived for project in the vault to
// the source hashes of its archives. An unreadable manifest is listed as
// absent (archive.ListEntries skips it), which is harmless: the session is
// archived again, and archive.Create's own skip makes that a no-op when the
// archive is there.
func archivedSessions(vaultRoot, project string) (map[string]map[string]bool, error) {
	entries, err := archive.ListEntries(vaultRoot, project)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]bool{}
	for _, e := range entries {
		if e.Manifest == nil {
			continue
		}
		if out[e.Manifest.SessionID] == nil {
			out[e.Manifest.SessionID] = map[string]bool{}
		}
		out[e.Manifest.SessionID][e.Manifest.SourceSHA256] = true
	}
	return out, nil
}

// sessionNoon is the archive date of a source session: its date: at 12:00 UTC.
// captured_at is then that UTC day on every host, and the archive filename's
// day, which archive.Create takes in the process-local zone, is the same date
// on every host within ±11 h of UTC (doc/MIGRATION.md).
func sessionNoon(date string) (time.Time, error) {
	date = strings.TrimSpace(date)
	if len(date) < 10 {
		return time.Time{}, fmt.Errorf("no session date (%q)", date)
	}
	d, err := time.Parse("2006-01-02", date[:10])
	if err != nil {
		return time.Time{}, fmt.Errorf("session date %q: %w", date, err)
	}
	return d.Add(12 * time.Hour), nil
}

// sourceSHA256 is the hash archive.Create records for SourceContent text.
func sourceSHA256(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// pendingArchive is one source the importer will archive.
type pendingArchive struct {
	sessionID string
	file      string
	text      string
	date      time.Time
	marker    bool // record an import marker once archived (sessions; not knowledge.md)
}

// addToBaseline adds the archives an import brings in to this host's
// baseline set, BEFORE any of them is created (ADR-014 decision 2,
// "Additions"), so no automatic ingest trigger spends its budget on the
// import. It takes the project's index commit lock for one step; on a host
// with no ledger for the project it records nothing (a ledger created later
// records the archives anyway).
func addToBaseline(ctx context.Context, destination *storage.Vault, project string, shas []string) error {
	if len(shas) == 0 {
		return nil
	}
	tx, err := indexstore.Lock(ctx, destination, project, indexstore.NoTimeout)
	if err != nil {
		return err
	}
	defer tx.Release()
	if err := tx.AddToBaseline(shas); err != nil {
		return err
	}
	return tx.Commit()
}

// archiveCreateFn is archive.Create, a seam so a test can record the order of
// the baseline addition and the archive writes, or fail one.
var archiveCreateFn = archive.Create

// writeArchive archives one source with the inline adapter, dated by its
// session date, never the clock.
func writeArchive(destination *storage.Vault, project string, p pendingArchive) (*archive.CreateResult, error) {
	return archiveCreateFn(archive.CreateOptions{
		Adapter:       archive.InlineAdapterName,
		SessionID:     p.sessionID,
		SourceContent: []byte(p.text),
		VaultRoot:     destination.Root,
		ProjectSlug:   project,
		Now:           p.date,
	})
}

// importProjectSessions archives one project's sessions and knowledge.md:
//
//  1. read every session note, and decide what each needs (the marker is a
//     hint, confirmed against the archived manifests);
//  2. add the source hashes of the archives this run brings in, the ones with
//     no (session_id, source_sha256) manifest in the vault yet, to this host's
//     baseline set, in one commit step, before any archive is written;
//  3. write each archive, then its marker.
func importProjectSessions(ctx context.Context, destination *storage.Vault, dirPath, projSlug string, opts ImportOptions, result *ImportResult) error {
	sessionFiles, _ := filepath.Glob(filepath.Join(dirPath, "sessions", "*.md"))
	total := len(sessionFiles)

	archived := map[string]map[string]bool{}
	mk := markers{}
	if !opts.DryRun {
		var err error
		if archived, err = archivedSessions(destination.Root, projSlug); err != nil {
			return fmt.Errorf("list archives of %s: %w", projSlug, err)
		}
		if mk, err = loadMarkers(destination, projSlug); err != nil {
			return fmt.Errorf("read import markers of %s: %w", projSlug, err)
		}
	}
	fail := func(id, file string, err error) {
		result.Errors = append(result.Errors, ImportError{Project: projSlug, SessionID: id, File: file, Err: err})
		progress(opts, ProgressEvent{Type: ProgressError, Project: projSlug, SessionID: id, File: file, Message: err.Error()})
	}
	mark := func(id, reason string) {
		if opts.DryRun {
			return
		}
		if err := appendMarker(destination, projSlug, id, "vibevault", reason); err != nil {
			fail(id, "", err)
		}
	}
	skip := func(id, file string, i int) {
		result.SessionsSkipped++
		progress(opts, ProgressEvent{Type: ProgressSessionSkip, Project: projSlug, SessionID: id, File: file, Current: i + 1, Total: total})
	}

	var pending []pendingArchive
	for i, sf := range sessionFiles {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := os.ReadFile(sf)
		if err != nil {
			fail("", sf, err)
			continue
		}
		meta, body, parseErr := storage.ParseFrontmatter(data)
		if parseErr != nil {
			// meta.ID is unreliable, so the session id is the filename.
			failedID := strings.TrimSuffix(filepath.Base(sf), ".md")
			fail(failedID, sf, parseErr)
			if opts.Strict {
				return fmt.Errorf("parse frontmatter %s: %w", sf, parseErr)
			}
			skip(failedID, sf, i)
			mark(failedID, markerReasonParseFailed)
			continue
		}
		sessionID := meta.ID
		if sessionID == "" {
			sessionID = strings.TrimSuffix(filepath.Base(sf), ".md")
		}
		if batchIDLike.MatchString(sessionID) {
			fail(sessionID, sf, fmt.Errorf("refused: session id %q has the form of a mempalace import batch id", sessionID))
			skip(sessionID, sf, i)
			mark(sessionID, markerReasonBatchID)
			continue
		}
		if mk.done(sessionID, archived) {
			skip(sessionID, sf, i)
			continue
		}
		text := strings.TrimSpace(body)
		if text == "" {
			text = strings.TrimSpace(meta.Summary)
		}
		if text == "" {
			// The inline adapter refuses empty content: no archive, and the
			// session is recorded as done.
			result.SessionsEmpty++
			result.SessionsImported++
			mark(sessionID, markerReasonEmpty)
			progress(opts, ProgressEvent{Type: ProgressSessionDone, Project: projSlug, SessionID: sessionID, Current: i + 1, Total: total})
			continue
		}
		date, err := sessionNoon(meta.Date)
		if err != nil {
			// Never dated by the clock: reported and skipped.
			fail(sessionID, sf, err)
			skip(sessionID, sf, i)
			mark(sessionID, markerReasonNoDate)
			continue
		}
		if archived[sessionID][sourceSHA256(text)] {
			// Already archived (a pull, or an earlier run whose marker is
			// gone): nothing to bring in.
			skip(sessionID, sf, i)
			mark(sessionID, "")
			continue
		}
		if opts.DryRun {
			result.SessionsImported++
			progress(opts, ProgressEvent{Type: ProgressSessionDone, Project: projSlug, SessionID: sessionID, Current: i + 1, Total: total})
			continue
		}
		pending = append(pending, pendingArchive{sessionID: sessionID, file: sf, text: text, date: date, marker: true})
	}

	// knowledge.md, archived under knowledge-<slug>, dated by its own
	// frontmatter date or the fixed epoch.
	knowledgePath := filepath.Join(dirPath, "knowledge.md")
	if data, err := os.ReadFile(knowledgePath); err == nil && !opts.DryRun {
		if text := strings.TrimSpace(string(data)); text != "" {
			date := knowledgeEpoch
			if meta, _, perr := storage.ParseFrontmatter(data); perr == nil {
				if d, derr := sessionNoon(meta.Date); derr == nil {
					date = d
				}
			}
			id := "knowledge-" + projSlug
			if !archived[id][sourceSHA256(text)] {
				pending = append(pending, pendingArchive{sessionID: id, file: knowledgePath, text: text, date: date})
			}
		}
	}
	if len(pending) == 0 {
		return nil
	}

	// The baseline set first: only the archives this run brings in.
	shas := make([]string, len(pending))
	for i, p := range pending {
		shas[i] = sourceSHA256(p.text)
	}
	if err := addToBaseline(ctx, destination, projSlug, shas); err != nil {
		// Not fatal: the archives are then ordinary pending archives on this
		// host, ingested within each run's budget.
		fail("", "", fmt.Errorf("add the imported archives to this host's baseline set: %w", err))
	}

	for _, p := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := writeArchive(destination, projSlug, p)
		if err != nil {
			fail(p.sessionID, p.file, err)
			continue
		}
		if !res.Skipped {
			result.ArchivesWritten++
		}
		if p.marker {
			mark(p.sessionID, "")
			result.SessionsImported++
			progress(opts, ProgressEvent{Type: ProgressSessionDone, Project: projSlug, SessionID: p.sessionID})
		}
	}
	return nil
}
