// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
)

// RebuildProof is what only a completed `vp index rebuild` can hand over: the
// one thing that may empty the baseline set or clear failure counts. Its
// fields are unexported, so the zero value is the only proof any other code
// can make, and every method that takes one refuses it.
//
// It is a plain value: it stays usable after the run lock that produced it is
// released, and nothing checks that the rebuild still holds the lock when the
// proof is spent. The rebuild driver spends it while it still holds the run
// lock.
type RebuildProof struct {
	vaultRoot string
	project   string
	failed    []string // source_sha256 of the archives that failed in that run
	ok        bool
}

// CompletedRebuild returns the proof of a completed rebuild of project, held
// under this run lock. failed lists the archives (source_sha256) that failed
// in that run: their failure records are kept, so coverage still names them.
// Only a held run lock of kind KindRebuild gives one;
// explicit-resumable-index-rebuild-with-disk-watchdog calls it when every
// live archive of the project was attempted.
func (rl *RunLock) CompletedRebuild(project string, failed []string) (RebuildProof, error) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.release == nil {
		return RebuildProof{}, errors.New("indexstore: the run lock is released; no rebuild is in progress")
	}
	if rl.holder.Kind != KindRebuild {
		return RebuildProof{}, fmt.Errorf("indexstore: a %s run cannot prove a rebuild", rl.holder.Kind)
	}
	if _, err := rl.vault.IndexDir(project); err != nil {
		return RebuildProof{}, err
	}
	f := slices.Clone(failed)
	slices.Sort(f)
	return RebuildProof{vaultRoot: rl.vault.Root, project: project, failed: slices.Compact(f), ok: true}, nil
}

func (p RebuildProof) check(tx *Tx) error {
	if !p.ok || p.vaultRoot != tx.vault.Root || p.project != tx.project {
		return errors.New("indexstore: not the proof of a completed rebuild of this project")
	}
	return nil
}

// listArchivesFn lists a project's tracked archives; a seam for tests.
var listArchivesFn = archive.ListEntries

// EnsureLedger creates the project's ledger when it has none. The new ledger's
// first line is the baseline set: the source_sha256 of every archive of the
// project present now, except those in exclude, the archives named by the
// trigger creating the ledger (ADR-014 decision 2, "The ledger's baseline is a
// set, not a time"). An archive whose manifest has no source_sha256 cannot be
// keyed, and is left out with a warning. A project that already has a ledger
// is left alone. It reports whether it created one.
func (tx *Tx) EnsureLedger(exclude []string) (bool, error) {
	s, err := tx.state()
	if err != nil {
		return false, err
	}
	if s.ledger.Exists() {
		return false, nil
	}
	rec, err := tx.freshBaseline(exclude)
	if err != nil {
		return false, err
	}
	if err := tx.fail(tx.appendLedger(s, rec)); err != nil {
		return false, err
	}
	return true, nil
}

func (tx *Tx) freshBaseline(exclude []string) (ledgerRecord, error) {
	entries, err := listArchivesFn(tx.vault.Root, tx.project)
	if err != nil {
		return ledgerRecord{}, fmt.Errorf("indexstore: list archives of %s: %w", tx.project, err)
	}
	set := []string{}
	for _, e := range entries {
		if e.Manifest == nil || e.Manifest.SourceSHA256 == "" {
			slog.Warn("index store: archive has no source_sha256; left out of the baseline set",
				"project", tx.project, "archive", e.ArchivePath)
			continue
		}
		if slices.Contains(exclude, e.Manifest.SourceSHA256) {
			continue
		}
		set = append(set, e.Manifest.SourceSHA256)
	}
	slices.Sort(set)
	return ledgerRecord{Kind: recBaseline, Archives: slices.Compact(set), CreatedAt: time.Now().UTC().Format(time.RFC3339)}, nil
}

// AddToBaseline adds archives to the baseline set: copy and merge
// (split-and-merge-exclude-derived-palace-paths) and import
// (importers-write-the-frozen-tracked-corpus) add the archives they bring in.
// On a host with no ledger for the project it does nothing and creates
// nothing: a ledger created later records those archives anyway.
func (tx *Tx) AddToBaseline(shas []string) error {
	s, err := tx.state()
	if err != nil {
		return err
	}
	if !s.ledger.Exists() || len(shas) == 0 {
		return nil
	}
	add := slices.Clone(shas)
	slices.Sort(add)
	return tx.fail(tx.appendLedger(s, ledgerRecord{Kind: recBaselineAdd, Archives: slices.Compact(add)}))
}

// ClearBaseline empties the baseline set. Only a completed rebuild may.
func (tx *Tx) ClearBaseline(proof RebuildProof) error {
	if err := proof.check(tx); err != nil {
		return err
	}
	s, err := tx.state()
	if err != nil {
		return err
	}
	if !s.ledger.Exists() {
		return ErrNoLedger
	}
	return tx.fail(tx.appendLedger(s, ledgerRecord{Kind: recBaselineClear}))
}

// RecordFailure records one more failure of an archive. A failure is never an
// ingest: the session stays pending, and none of the archive's records loads.
func (tx *Tx) RecordFailure(sessionID, sha string, cause error) error {
	if sha == "" {
		return errors.New("indexstore: RecordFailure needs a source_sha256")
	}
	s, err := tx.state()
	if err != nil {
		return err
	}
	if !s.ledger.Exists() {
		return ErrNoLedger
	}
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	return tx.fail(tx.appendLedger(s, ledgerRecord{
		Kind: recFailure, SessionID: sessionID, SHA: sha, Count: s.ledger.FailureCount(sha) + 1, LastError: msg,
	}))
}

// ClearFailures clears the failure count of every archive except those that
// failed in the proving rebuild's own run, whose records are kept so coverage
// still names them as failed. Only a completed rebuild may.
func (tx *Tx) ClearFailures(proof RebuildProof) error {
	if err := proof.check(tx); err != nil {
		return err
	}
	s, err := tx.state()
	if err != nil {
		return err
	}
	if !s.ledger.Exists() {
		return ErrNoLedger
	}
	return tx.fail(tx.appendLedger(s, ledgerRecord{Kind: recFailureClear, Keep: proof.failed}))
}
