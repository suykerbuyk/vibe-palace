// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexRawAdapterHashesExactSourceBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	want := []byte("{\"type\":\"response_item\"}\n\xff\x00")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, cleanup, err := (codexRawAdapter{}).ResolveSource(CreateOptions{SourcePath: path})
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	got, n, err := HashSource(nil, resolved)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(want)
	if got != hex.EncodeToString(sum[:]) || n != int64(len(want)) {
		t.Fatalf("hash/size = %s/%d, want %x/%d", got, n, sum, len(want))
	}
}

// TestCodexRawAdapterDoesNotClaimClaudeTurnCount is RETIRED, deliberately, and
// this note stands in its place so nobody restores it from the history.
//
// It asserted that codex-raw's Inspect returns an error and no metadata, on the
// premise that the rollout format "is not interpreted". Scope 2 of
// codex-u3-codex-raw-adapter-enrichment interprets it, so the premise is gone.
//
// 🔴 It did NOT go red when that landed, and the plan was wrong to say it would.
// Its argument was the literal path "unused", which does not exist — so the new
// Inspect still failed at os.Open and the assertion still held. It would have
// stayed GREEN while testing nothing but os.Open, which is the same vacuous-pin
// failure this unit caught in archive_test.go, from the other direction.
//
// What replaced it, in codex_rollout_test.go:
//   - the turn count is Codex's own rule, never Claude's:
//     TestCodexRawInspectCountsConversationalTurns
//   - a missing source is an error with both results zeroed:
//     TestCodexRawInspectReportsAMissingSource

func TestCodexRawAdapterRejectsMissingSource(t *testing.T) {
	if _, _, err := (codexRawAdapter{}).ResolveSource(CreateOptions{SourcePath: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing source accepted")
	}
}
