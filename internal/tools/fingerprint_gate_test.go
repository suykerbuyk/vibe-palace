// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// No fingerprint gates a vault write (ADR-014 decision 3, shared properties): a
// project whose chunks.fingerprint does not match the current recipe still takes
// a vp_kg_add. Regression guard: the fingerprint is host-local derived state and
// must never stop an authored write.
func TestFingerprintNeverGatesAVaultWrite(t *testing.T) {
	vault := newTestVault(t)
	testutil.InitProject(t, vault.Root, "test")
	dir, err := vault.IndexDir("test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chunks.fingerprint"), []byte("a recipe from another build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := indexstore.ReadFingerprint(vault, "test", index.ChunkRecipe{IndexerVersion: index.IndexerVersion})
	if err != nil || st != indexstore.FingerprintMismatch {
		t.Fatalf("fixture: fingerprint %v, %v; want a mismatch", st, err)
	}
	params, _ := json.Marshal(kgAddParams{Project: "test", Subject: "Alice", Predicate: "works_on", Object: "backend"})
	result, err := KGAddTool(vault).Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("vp_kg_add on a project with a mismatched fingerprint: %v", err)
	}
	if status := result.(map[string]string); status["status"] != "added" {
		t.Fatalf("status = %q, want added", status["status"])
	}
}
