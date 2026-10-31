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

func TestCodexRawAdapterDoesNotClaimClaudeTurnCount(t *testing.T) {
	turns, model, err := (codexRawAdapter{}).Inspect("unused")
	if err == nil || turns != 0 || model != "" {
		t.Fatalf("Inspect = (%d, %q, %v), want unknown metadata and explicit error", turns, model, err)
	}
}

func TestCodexRawAdapterRejectsMissingSource(t *testing.T) {
	if _, _, err := (codexRawAdapter{}).ResolveSource(CreateOptions{SourcePath: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing source accepted")
	}
}
