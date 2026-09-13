// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package jobqueue provides small, enrichment-agnostic host-local job-queue
// primitives: claim, requeue and complete. It operates purely on raw file
// paths and caller-supplied bytes and carries no knowledge of any job's own
// JSON schema — that belongs to the caller.
//
// A "job" is a *.json file in a directory. Claim atomically renames one such
// file to a "<name>.json.processing" claim file (a plain os.Rename, so it is
// safe under concurrent drainers: exactly one caller wins the rename and
// therefore the claim). Requeue either dead-letters the claim to
// "<name>.json.failed" (attempts exhausted) or rewrites it back to
// "<name>.json" so it becomes claimable again. Done removes a claim once its
// work is finished successfully.
//
// This mirrors the shape of internal/capture/enrichqueue.go's queue
// mechanics, generalized off that package's specific job schema.
package jobqueue

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ProcessingSuffix marks a claimed job file, appended to its original name.
const ProcessingSuffix = ".processing"

// failedSuffix marks a dead-lettered job file: a permanent record that a job
// exhausted its retry budget. Dead-lettered files are never deleted by this
// package.
const failedSuffix = ".failed"

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

	matches, globErr := filepath.Glob(filepath.Join(dir, "*.json"))
	if globErr != nil {
		return "", nil, fmt.Errorf("jobqueue: glob %s: %w", dir, globErr)
	}
	sort.Strings(matches)

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
		if renErr := os.Rename(jsonPath, candidate); renErr != nil {
			// Another claimant won the race (or the file already vanished);
			// move on without double-claiming.
			continue
		}

		b, readErr := os.ReadFile(candidate)
		if readErr != nil {
			// The claim succeeded but the bytes are unreadable (e.g. removed
			// out from under us). Drop this claim and try the next
			// candidate rather than fail the whole call.
			_ = os.Remove(candidate)
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
func Requeue(procPath string, attempts, maxAttempts int, reencode func(attempts int) ([]byte, error)) error {
	jsonPath := strings.TrimSuffix(procPath, ProcessingSuffix)

	if attempts >= maxAttempts {
		failedPath := jsonPath + failedSuffix
		if err := os.Rename(procPath, failedPath); err != nil {
			return fmt.Errorf("jobqueue: dead-letter %s: %w", procPath, err)
		}
		return nil
	}

	data, err := reencode(attempts)
	if err != nil {
		restoreClaim(procPath, jsonPath)
		return fmt.Errorf("jobqueue: reencode %s: %w", procPath, err)
	}
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		restoreClaim(procPath, jsonPath)
		return fmt.Errorf("jobqueue: write %s: %w", jsonPath, err)
	}
	if err := os.Remove(procPath); err != nil {
		return fmt.Errorf("jobqueue: remove claim %s: %w", procPath, err)
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
	stale, err := filepath.Glob(filepath.Join(dir, "*"+ProcessingSuffix))
	if err != nil {
		return
	}
	for _, p := range stale {
		info, statErr := os.Stat(p)
		if statErr != nil || time.Since(info.ModTime()) < staleAge {
			continue
		}
		target := strings.TrimSuffix(p, ProcessingSuffix)
		if _, existsErr := os.Stat(target); existsErr == nil {
			_ = os.Remove(p)
			continue
		}
		_ = os.Rename(p, target)
	}
}
