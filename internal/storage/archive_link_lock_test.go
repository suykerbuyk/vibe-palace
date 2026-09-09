// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// TestTryLinkArchiveToSessions_SucceedsUncontended is the sanity case: with no
// concurrent holder, the non-blocking form behaves exactly like the blocking
// one — finds the caller-keyed note and links it on the first attempt.
func TestTryLinkArchiveToSessions_SucceedsUncontended(t *testing.T) {
	v := testVault(t)
	const sid = "try-link-0001"
	ref := seedBackfillNote(t, v, "proj", SessionMeta{
		SessionKey: sid, SessionKeySource: KeySourceCaller,
		ArchiveSessionID: sid, Title: "wrap", Tag: "implementation",
	})

	res, err := v.TryLinkArchiveToSessions("proj", sid, "Projects/proj/transcripts/x.manifest.json")
	if err != nil {
		t.Fatalf("TryLinkArchiveToSessions: %v", err)
	}
	if !res.Found {
		t.Fatal("Found = false, want true")
	}
	if res.Canonical.NotePath != ref.NotePath {
		t.Fatalf("Canonical = %s, want %s", res.Canonical.NotePath, ref.NotePath)
	}
}

// TestTryLinkArchiveToSessions_RefusesUnderContention is the failure mode this
// function exists to convert: with the sessions directory already locked by
// another writer (simulating a second SessionEnd hook's own
// TryLinkArchiveToSessions, or the blocking backfill applier, mid-flight on
// the SAME project), the non-blocking form must NOT hang. It must retry a
// bounded number of times and then return promptly, wrapping
// ErrSessionsDirLocked — never silently succeed, never block past the
// sub-second budget the hook can afford.
func TestTryLinkArchiveToSessions_RefusesUnderContention(t *testing.T) {
	v := testVault(t)
	const sid = "try-link-0002"
	seedBackfillNote(t, v, "proj", SessionMeta{
		SessionKey: sid, SessionKeySource: KeySourceCaller,
		ArchiveSessionID: sid, Title: "wrap", Tag: "implementation",
	})

	dir, err := v.SessionDir("proj")
	if err != nil {
		t.Fatalf("SessionDir: %v", err)
	}
	release, err := vaultlock.Acquire(v.Root, dir)
	if err != nil {
		t.Fatalf("simulate contention: %v", err)
	}
	defer func() { _ = release() }()

	start := time.Now()
	_, err = v.TryLinkArchiveToSessions("proj", sid, "Projects/proj/transcripts/x.manifest.json")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("TryLinkArchiveToSessions succeeded while the directory was held by another writer")
	}
	if !errors.Is(err, ErrSessionsDirLocked) {
		t.Fatalf("err = %v, want it to wrap ErrSessionsDirLocked", err)
	}
	// Upper bound only — proves this NEVER degrades into the blocking wait
	// LinkArchiveToSessions would take (which would hang here forever, since
	// this test never releases the lock). The exact retry/backoff constants
	// are re-derived from the function's own doc comment, never hardcoded
	// here, so tightening them later does not require touching this bound.
	if elapsed > 2*time.Second {
		t.Fatalf("took %s to give up — expected a bounded, sub-second retry budget, not a near-blocking wait", elapsed)
	}
}

// TestTryLinkArchiveToSessions_RecoverableAfterContentionClears proves the
// miss above is not a loss: once the contending holder releases, the SAME
// session is still an ordinary, recoverable stranded entry — the exact-match
// predicate ADR-007 requires is untouched by a refused attempt.
func TestTryLinkArchiveToSessions_RecoverableAfterContentionClears(t *testing.T) {
	v := testVault(t)
	const sid = "try-link-0003"
	ref := seedBackfillNote(t, v, "proj", SessionMeta{
		SessionKey: sid, SessionKeySource: KeySourceCaller,
		ArchiveSessionID: sid, Title: "wrap", Tag: "implementation",
	})

	dir, err := v.SessionDir("proj")
	if err != nil {
		t.Fatalf("SessionDir: %v", err)
	}
	release, err := vaultlock.Acquire(v.Root, dir)
	if err != nil {
		t.Fatalf("simulate contention: %v", err)
	}
	if _, err := v.TryLinkArchiveToSessions("proj", sid, "Projects/proj/transcripts/x.manifest.json"); err == nil {
		release()
		t.Fatal("expected the first attempt to be refused under contention")
	}
	release()

	res, err := v.TryLinkArchiveToSessions("proj", sid, "Projects/proj/transcripts/x.manifest.json")
	if err != nil {
		t.Fatalf("TryLinkArchiveToSessions after contention cleared: %v", err)
	}
	if res.Canonical.NotePath != ref.NotePath {
		t.Fatalf("Canonical = %s, want %s", res.Canonical.NotePath, ref.NotePath)
	}
}
