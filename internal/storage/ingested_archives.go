// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// IngestedArchive is one row of the archive-ingest ledger: a transcript archive
// whose contents have already been turned into drawers.
//
// 🔴 THE KEY IS THE CONTENT HASH, NEVER THE SESSION ID. A session can be
// re-archived — more turns, a repaired transcript — and the new archive carries
// the same session_id with DIFFERENT bytes. Keyed on session_id, that archive
// would be skipped forever and its new content would never reach the index.
// Keyed on source_sha256 it is simply an archive nobody has ingested yet, which
// is the truth. SessionID rides along for diagnosis only: it is what a human
// reads when asking "which session is this row?", and nothing keys on it.
type IngestedArchive struct {
	SourceSHA256 string `json:"source_sha256"`
	SessionID    string `json:"session_id,omitempty"`
}

// IngestedArchivesFile returns the path to a project's archive-ingest ledger:
// {vault}/palace/{project}/ingested-archives.jsonl
//
// It lives in the PALACE store, beside drawers, and not under
// Projects/<slug>/transcripts/ with the archives it names. The distinction is
// what the file MEANS: an archive is durable source material, while this ledger
// is derived state describing what the drawer store already contains. Delete
// palace/<project>/ to force a full reingest and the ledger goes with it, which
// is the correct coupling; parking it next to the archives would leave a ledger
// asserting drawers that no longer exist.
func (v *Vault) IngestedArchivesFile(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "palace", project, "ingested-archives.jsonl"), nil
}

// IngestedArchives returns the set of source_sha256 values already ingested for
// this project. A missing ledger is an empty set, not an error: every project
// had no ledger before this existed, and the correct reading of "no ledger" is
// "nothing has been ingested", which reingests rather than skipping.
//
// A malformed line is skipped and warned rather than failing the read, for the
// same reason readDrawerFile tolerates one: this file is appended to through F4,
// so a crash mid-append can leave a torn final line, and letting that line
// invalidate the whole ledger would silently turn every subsequent run back
// into a full reingest. A row with an empty hash is skipped too — it can key
// nothing, so it can skip nothing.
func (v *Vault) IngestedArchives(project string) (map[string]struct{}, error) {
	path, err := v.IngestedArchivesFile(project)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]struct{}{}, nil
		}
		return nil, fmt.Errorf("open ingested-archives ledger: %w", err)
	}
	defer f.Close()

	seen := map[string]struct{}{}
	lineNo := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxDrawerLine)
	for scanner.Scan() {
		lineNo++
		var rec IngestedArchive
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			slog.Warn("skipping malformed ingested-archives line",
				"project", project, "line", lineNo, "err", err)
			continue
		}
		if rec.SourceSHA256 == "" {
			continue
		}
		seen[rec.SourceSHA256] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan ingested-archives ledger: %w", err)
	}
	return seen, nil
}

// RecordIngestedArchive appends one row to the ledger, and is a no-op when the
// hash is already recorded.
//
// It REFUSES an empty SourceSHA256. A row that keys nothing can never be
// matched, so writing one would grow the file on every run while skipping
// nothing — the shape of a cache that looks like it is working and is not. The
// caller's correct response to an archive with no hash is to ingest it every
// time and say so, not to record a placeholder.
func (v *Vault) RecordIngestedArchive(project string, rec IngestedArchive) error {
	if rec.SourceSHA256 == "" {
		return fmt.Errorf("record ingested archive: refusing to write an empty source_sha256")
	}
	path, err := v.IngestedArchivesFile(project)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure palace dir: %w", err)
	}

	// The lock spans the read→dedup→append, exactly as the drawer and commit-log
	// writers do: appendUnderLock does NOT acquire, and reading before acquiring
	// would let a concurrent refresh land the same hash between the read and the
	// append.
	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock ingested-archives ledger: %w", err)
	}
	defer release()

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read ingested-archives ledger: %w", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(existing))
	scanner.Buffer(make([]byte, 0, 64*1024), maxDrawerLine)
	for scanner.Scan() {
		var cur IngestedArchive
		if err := json.Unmarshal(scanner.Bytes(), &cur); err != nil {
			continue
		}
		if cur.SourceSHA256 == rec.SourceSHA256 {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan ingested-archives ledger: %w", err)
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal ingested archive: %w", err)
	}
	out := append(line, '\n')
	// Heal a torn final line rather than appending onto it, for the reason
	// AppendDrawers does: without the separator a torn row and the new row
	// become one unparseable line, and this ledger would lose a row it just
	// wrote on top of the one that was already damaged.
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		out = append([]byte{'\n'}, out...)
	}
	if err := v.appendUnderLock(path, out); err != nil {
		return fmt.Errorf("write ingested-archives ledger: %w", err)
	}
	return nil
}
