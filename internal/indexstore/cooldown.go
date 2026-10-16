// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// DefaultRunCooldown is the inter-run cooldown (ADR-016 decision 6): a trigger
// that fires within this window of the last completed, embedding index run for
// a (host, vault) defers rather than starting another back-to-back pass, so a
// multiplexed host does not stay continuously hot. It lives here, not in
// internal/ingest, because both internal/ingest and internal/search consult it
// and search must not import ingest. The value is minutes-scale (Scope-6
// measured per-run passes at minutes-to->10 min at ~5.6 cores).
const DefaultRunCooldown = 10 * time.Minute

// WriteRunCompleted stamps the cooldown marker with t (ADR-016 decision 6). It
// replaces the marker (never appends) through the package's one write
// primitive, so TestWritesGoThroughThisFile stays green and the host-local
// path carries no .surface stamp. It is written only when a run actually
// embedded, by the caller that completed it.
func WriteRunCompleted(vault *storage.Vault, t time.Time) error {
	if err := os.MkdirAll(vault.IndexLocksDir(), 0o755); err != nil {
		return err
	}
	return writeFile(vault.IndexRunCompletedPath(), []byte(t.UTC().Format(time.RFC3339Nano)+"\n"))
}

// ReadRunCompleted reads the cooldown marker without taking any lock. It is
// FAIL-OPEN: a missing, unreadable, empty, or unparseable marker returns
// (zero, false, nil), so a torn or absent marker never suppresses a run. The
// bool is whether a usable timestamp was read; callers gate on it and ignore
// the error (a bad marker must never defer a build).
func ReadRunCompleted(vault *storage.Vault) (time.Time, bool, error) {
	data, err := os.ReadFile(vault.IndexRunCompletedPath())
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, nil
	}
	t, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if perr != nil {
		return time.Time{}, false, nil
	}
	return t, true, nil
}

// RunCooldownActive reports whether now is within cooldown of the last
// completed embedding run for the vault. It is the shared predicate behind the
// four U4 cooldown sites: fail-open (a bad/missing marker is not active) and
// disabled when cooldown <= 0 (so a zero override turns the throttle off).
func RunCooldownActive(vault *storage.Vault, now time.Time, cooldown time.Duration) bool {
	if cooldown <= 0 {
		return false
	}
	last, ok, _ := ReadRunCompleted(vault)
	if !ok {
		return false
	}
	return now.Sub(last) < cooldown
}
