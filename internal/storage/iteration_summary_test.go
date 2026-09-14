// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"reflect"
	"testing"
)

func TestWriteReadIterationSummaryRoundTrip(t *testing.T) {
	v := testVault(t)

	s := IterationSummary{
		N:           5,
		MatchIndex:  0,
		Summary:     "Implemented the summarization config surface.",
		Decisions:   []string{"use a shared [summarization] block", "allow overwrite on regenerate"},
		Unblocks:    "iteration summarizer can now read config",
		Model:       "grok-3-mini",
		GeneratedAt: "2026-09-13T00:00:00Z",
	}

	if err := v.WriteIterationSummary("proj", s); err != nil {
		t.Fatalf("WriteIterationSummary: %v", err)
	}

	got, ok, err := v.ReadIterationSummary("proj", 5)
	if err != nil {
		t.Fatalf("ReadIterationSummary: %v", err)
	}
	if !ok {
		t.Fatalf("ReadIterationSummary: ok = false, want true")
	}
	if !reflect.DeepEqual(got, s) {
		t.Errorf("ReadIterationSummary = %+v, want %+v", got, s)
	}
}

// TestWriteIterationSummaryOverwrite proves WriteIterationSummary allows
// overwriting an EXISTING cache file — the deliberate difference from
// AddTriple's refuse-if-exists guard. It writes N=7 once, confirms the file
// now exists, writes DIFFERENT content for the SAME N, and asserts the
// second write's content — not the first's — is what comes back. This test
// would FAIL if someone copy-pasted AddTriple's "already exists" stat-check
// guard into WriteIterationSummary by mistake, because the second write
// would then return an error instead of silently replacing the file.
func TestWriteIterationSummaryOverwrite(t *testing.T) {
	v := testVault(t)

	first := IterationSummary{
		N:           7,
		MatchIndex:  0,
		Summary:     "first pass summary",
		Model:       "grok-3-mini",
		GeneratedAt: "2026-09-13T00:00:00Z",
	}
	if err := v.WriteIterationSummary("proj", first); err != nil {
		t.Fatalf("first WriteIterationSummary: %v", err)
	}

	// Confirm the file exists before overwriting, so the second write below
	// is unambiguously an overwrite of existing content, not a fresh create.
	before, ok, err := v.ReadIterationSummary("proj", 7)
	if err != nil || !ok {
		t.Fatalf("ReadIterationSummary after first write: ok=%v err=%v", ok, err)
	}
	if before.Summary != first.Summary {
		t.Fatalf("sanity check failed: before.Summary = %q, want %q", before.Summary, first.Summary)
	}

	second := IterationSummary{
		N:           7,
		MatchIndex:  1, // a newer entry sharing N=7 was appended; matchIndex advanced
		Summary:     "regenerated summary after entry was superseded",
		Decisions:   []string{"regenerate on matchIndex mismatch"},
		Model:       "grok-3-mini",
		GeneratedAt: "2026-09-13T01:00:00Z",
	}
	if err := v.WriteIterationSummary("proj", second); err != nil {
		t.Fatalf("second (overwrite) WriteIterationSummary: %v", err)
	}

	got, ok, err := v.ReadIterationSummary("proj", 7)
	if err != nil {
		t.Fatalf("ReadIterationSummary after overwrite: %v", err)
	}
	if !ok {
		t.Fatalf("ReadIterationSummary after overwrite: ok = false, want true")
	}
	if !reflect.DeepEqual(got, second) {
		t.Errorf("after overwrite, got %+v, want %+v (second write's content)", got, second)
	}
	if got.Summary == first.Summary {
		t.Errorf("overwrite did not take effect: still reading first write's Summary %q", first.Summary)
	}
}

func TestReadIterationSummaryMissing(t *testing.T) {
	v := testVault(t)

	got, ok, err := v.ReadIterationSummary("proj", 42)
	if err != nil {
		t.Fatalf("ReadIterationSummary for missing N: %v", err)
	}
	if ok {
		t.Errorf("ok = true, want false for a never-written N")
	}
	if !reflect.DeepEqual(got, IterationSummary{}) {
		t.Errorf("got = %+v, want zero value", got)
	}
}
