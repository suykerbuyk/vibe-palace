// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package jobqueue provides small, enrichment-agnostic host-local job-queue
// primitives: claim, requeue, complete, and an atomic write helper for
// creating or rewriting a job file in the first place. It operates purely on
// raw file paths and caller-supplied bytes and carries no knowledge of any
// job's own JSON schema — that belongs to the caller.
//
// A "job" is a *.json file in a directory, listed via os.ReadDir and matched
// by a plain ".json" suffix check — never filepath.Glob, whose bracket
// metacharacters would silently fail to match a queue directory nested under
// a path segment containing '[' or ']'. Claim atomically renames one such
// file to a "<name>.json.processing" claim file (a plain os.Rename, so it is
// safe under concurrent drainers: exactly one caller wins the rename and
// therefore the claim). Requeue either dead-letters the claim to
// "<name>.json.failed" (attempts exhausted) or rewrites it back to
// "<name>.json" so it becomes claimable again. Done removes a claim once its
// work is finished successfully. AtomicWrite (a temp file plus rename, in the
// same directory) is the one path every writer of a job file — Requeue
// itself, plus internal/summarize's and internal/capture's own enqueue
// paths, both outside this package — uses to create or rewrite one, so a
// reader can never observe a partially-written job.
//
// This began as a generalization off internal/capture/enrichqueue.go's own
// inline queue mechanics; enrichqueue.go has since been migrated onto this
// package's Claim/Requeue/Done directly, rather than merely sharing their
// shape.
package jobqueue

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProcessingSuffix marks a claimed job file, appended to its original name.
const ProcessingSuffix = ".processing"

// FailedSuffix marks a dead-lettered job file: a permanent record that a job
// exhausted its retry budget. Dead-lettered files are never deleted by this
// package.
const FailedSuffix = ".failed"

// Claim reclaims any stale ".processing" files in dir older than staleAge
// (presumed orphaned by a crashed claimant), then atomically claims the
// first claimable "*.json" job in dir, in deterministic (sorted-by-name)
// order.
//
// On success it returns the claimed file's new ".processing" path and its
// raw bytes. When no job is claimable — the directory is empty, missing, or
// every job is already claimed by someone else — it returns ("", nil, nil);
// this is the documented sentinel for "nothing to do" and callers should
// treat it exactly like io.EOF: not an error.
//
// Claim is safe to call concurrently: the claim step is a single os.Rename,
// which is atomic on the same filesystem, so two racing callers can never
// both claim the same job.
func Claim(dir string, staleAge time.Duration) (procPath string, data []byte, err error) {
	reclaimStale(dir, staleAge)

	entries, readDirErr := os.ReadDir(dir)
	if readDirErr != nil {
		if !os.IsNotExist(readDirErr) {
			return "", nil, fmt.Errorf("jobqueue: read dir %s: %w", dir, readDirErr)
		}
		entries = nil
	}

	var matches []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".json") {
			matches = append(matches, filepath.Join(dir, name))
		}
	}
	// os.ReadDir already returns entries sorted by filename, so matches is
	// already in deterministic order — no explicit sort needed.

	for _, jsonPath := range matches {
		candidate := jsonPath + ProcessingSuffix
		if _, statErr := os.Lstat(candidate); statErr == nil || !os.IsNotExist(statErr) {
			// candidate is already claimed by an outstanding, unfinished
			// claim of THE SAME job identity (queueFileName is deterministic
			// — see internal/summarize — so a job re-enqueued while its
			// prior claim is still in flight lands back at this exact
			// jsonPath). os.Rename would silently REPLACE an existing
			// destination on POSIX, clobbering that in-flight claim's file
			// out from under its own claimant; skip this candidate instead
			// and leave jsonPath queued — it will be claimable again once
			// the outstanding claim resolves (Done removes candidate, or a
			// failed Requeue rewrites jsonPath itself).
			//
			// Treated identically to a confirmed-existing candidate: any
			// Lstat error OTHER than "does not exist" (permission trouble, a
			// racy intermediate filesystem state) means candidate's presence
			// is genuinely unknown — proceeding with the rename on an
			// unproven absence would risk exactly the same clobber this
			// check exists to prevent. Only a confirmed os.IsNotExist(statErr)
			// is treated as "safe to proceed."
			continue
		}
		// Stamp jsonPath's mtime to now BEFORE renaming it to candidate, not
		// after. os.Rename does not update mtime on POSIX, so whichever
		// mtime the SOURCE has at rename time is the one the resulting
		// ".processing" file is born with; reclaimStale measures staleness
		// by that same mtime. Two gaps existed in an earlier version of this
		// fix that stamped candidate AFTER the rename instead:
		//
		//  1. Fail-open: if that post-rename Chtimes call failed, the claim
		//     was still handed back to the caller with its stale mtime
		//     intact — reproducing the original bug on exactly the path
		//     meant to guard against it.
		//  2. A window between the rename and the post-rename Chtimes call
		//     during which candidate was visible to a CONCURRENT drainer's
		//     own reclaimStale sweep with its old, pre-claim mtime — for a
		//     job queued long enough before being claimed, that concurrent
		//     pass could steal a claim that was still genuinely in-flight.
		//     Concurrent SessionEnd hooks across multiple Herdr panes
		//     hitting the same project's queue directory is a real scenario
		//     this project has hit before (iter 410), not a theoretical one.
		//
		// Stamping the source first closes both: os.Rename preserves the
		// inode (and therefore the mtime just set on it), so candidate is
		// born already correctly stamped — there is no window where a
		// stale-mtime ".processing" file is ever visible to anyone. And if
		// the Chtimes call itself fails, NOTHING has been claimed yet, so
		// failing closed (skip this candidate, try the next match) costs
		// nothing and can't hand back an unclocked claim.
		now := time.Now()
		if chErr := os.Chtimes(jsonPath, now, now); chErr != nil {
			// os.IsNotExist here means a concurrent Claim call already won
			// this exact candidate (renamed jsonPath away between our Lstat
			// check above and this Chtimes call) — an entirely normal
			// outcome of the concurrent-claim contention this package's own
			// docs promise to handle safely, identical in kind to the
			// silent "another claimant won the race" continue below. Treated
			// the same way: silent, not warned. Any OTHER Chtimes error
			// (permission trouble, an exotic filesystem) is genuinely
			// unexpected and worth surfacing.
			if !os.IsNotExist(chErr) {
				slog.Warn("jobqueue: stamp claim time failed; skipping candidate", "err", chErr, "item", jsonPath)
			}
			continue
		}

		if renErr := os.Rename(jsonPath, candidate); renErr != nil {
			// Another claimant won the race (or the file already vanished);
			// move on without double-claiming.
			continue
		}

		b, readErr := os.ReadFile(candidate)
		if readErr != nil {
			// The claim succeeded but the bytes are unreadable (e.g. a
			// transient permission or I/O error). Restore the claim to its
			// original claimable name rather than deleting it outright — a
			// delete here would silently destroy the job — and try the next
			// candidate.
			restoreClaim(candidate, jsonPath)
			continue
		}
		return candidate, b, nil
	}

	return "", nil, nil
}

// Requeue resolves a claimed job at procPath (a ".processing" path returned
// by Claim). Once attempts >= maxAttempts it dead-letters the job — a rename
// to "<name>.json.failed", never a delete, so the failure is a permanent
// record — and reencode is not called. Otherwise it calls reencode(attempts)
// to obtain freshly-marshaled bytes and rewrites the job back to its
// original "*.json" name so it becomes claimable again; reencode is the only
// place that understands the job's own schema, so this package never
// unmarshals or interprets the bytes it moves.
//
// If reencode or the rewrite fails, Requeue makes a best-effort attempt to
// restore procPath to its original claimable name before returning the
// error, so a transient local failure here does not silently lose the job.
//
// deadLettered reports which branch fired: true exactly when the
// attempts >= maxAttempts dead-letter branch fired (regardless of whether
// err is nil), false on the normal-requeue branch. This lets a caller log
// branch-specific diagnostics without duplicating the threshold check.
func Requeue(procPath string, attempts, maxAttempts int, reencode func(attempts int) ([]byte, error)) (deadLettered bool, err error) {
	jsonPath := strings.TrimSuffix(procPath, ProcessingSuffix)

	if attempts >= maxAttempts {
		failedPath := jsonPath + FailedSuffix
		if err := os.Rename(procPath, failedPath); err != nil {
			return true, fmt.Errorf("jobqueue: dead-letter %s: %w", procPath, err)
		}
		return true, nil
	}

	data, err := reencode(attempts)
	if err != nil {
		restoreClaim(procPath, jsonPath)
		return false, fmt.Errorf("jobqueue: reencode %s: %w", procPath, err)
	}
	if err := AtomicWrite(jsonPath, data); err != nil {
		restoreClaim(procPath, jsonPath)
		return false, fmt.Errorf("jobqueue: write %s: %w", jsonPath, err)
	}
	if err := os.Remove(procPath); err != nil {
		return false, fmt.Errorf("jobqueue: remove claim %s: %w", procPath, err)
	}
	return false, nil
}

// AtomicWrite atomically writes data to path: a temp file in the same
// directory (required for the rename to be same-filesystem), chmod 0o644,
// then rename over path. Callers outside this package (internal/summarize,
// internal/capture) use this directly for their own job-file writes so the
// same atomicity applies to brand-new job files, not just requeue rewrites.
func AtomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("jobqueue: create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("jobqueue: write temp file %s: %w", tmpPath, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		cleanup()
		return fmt.Errorf("jobqueue: chmod temp file %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("jobqueue: close temp file %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("jobqueue: rename %s to %s: %w", tmpPath, path, err)
	}
	return nil
}

// restoreClaim makes a best-effort attempt to restore procPath to its
// original claimable name jsonPath, EXCEPT when jsonPath already exists: that
// means a fresh job of the same identity was re-enqueued while this claim was
// still outstanding, and restoring would silently clobber that fresh copy
// with this claim's stale bytes (including a stale Attempts count). In that
// case procPath is deliberately left as an orphaned ".processing" file
// instead — reclaimStale's own clobber-avoidance below (which, unlike this
// function, DELETES a stale claim whose target is occupied rather than
// merely leaving it — a genuinely different post-condition, since a claim
// reclaimStale is looking at is old enough to treat as abandoned, whereas one
// restoreClaim is looking at was live moments ago) already handles cleaning
// this one up safely once it, too, goes stale.
func restoreClaim(procPath, jsonPath string) {
	// As in Claim's own candidate check: any Lstat error other than "does
	// not exist" leaves jsonPath's presence genuinely unknown, and an
	// unproven absence is not grounds to risk the clobber this check exists
	// to prevent — only a confirmed os.IsNotExist(statErr) proceeds.
	if _, statErr := os.Lstat(jsonPath); statErr == nil || !os.IsNotExist(statErr) {
		return
	}
	_ = os.Rename(procPath, jsonPath)
}

// Done removes a successfully-processed claim, permanently retiring the job.
func Done(procPath string) error {
	if err := os.Remove(procPath); err != nil {
		return fmt.Errorf("jobqueue: remove claim %s: %w", procPath, err)
	}
	return nil
}

// reclaimStale renames orphaned "<name>.json.processing" files — claimed by
// a caller that crashed before finishing — back to "<name>.json" so they
// become claimable again, once they are older than staleAge. A fresh (live)
// claim within staleAge is left untouched. If a fresh job already occupies
// the target name (unusual, but possible after manual intervention), the
// stale claim is dropped rather than clobbering it.
func reclaimStale(dir string, staleAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	var stale []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ProcessingSuffix) {
			stale = append(stale, filepath.Join(dir, name))
		}
	}

	for _, p := range stale {
		info, statErr := os.Stat(p)
		if statErr != nil || time.Since(info.ModTime()) < staleAge {
			continue
		}
		target := strings.TrimSuffix(p, ProcessingSuffix)
		if linkErr := os.Link(p, target); linkErr != nil {
			if os.IsExist(linkErr) {
				_ = os.Remove(p) // fresh job already occupies target; drop the stale claim
			} else {
				slog.Warn("jobqueue: reclaim stale claim failed", "err", linkErr, "item", p)
			}
			continue // any other Link error: unproven state, leave p for next pass
		}
		_ = os.Remove(p)
	}
}
