// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/migrate"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// hashTree walks root and returns a map of relative-path → sha256 hex
// for every regular file beneath it. It is used to prove byte-for-byte
// stability of the SOURCE vault across a migration run.
func hashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("hashTree(%s): %v", root, err)
	}
	return out
}

// TestIntegrationMigrateCrossVaultSourceReadOnly is the guard test for the
// source/destination split in migrate.ImportVibeVault. It seeds a
// VibeVault-shaped SOURCE tree, migrates into a distinct empty
// DESTINATION vault, and proves three things:
//
//  1. Data lands in the destination (drawers, markers, config.toml).
//  2. The source tree is byte-for-byte stable — no writes leak to it.
//  3. Idempotency works across vaults — a second run dedupes via the
//     destination markers (catching a "marker read still points at
//     source" regression).
func TestIntegrationMigrateCrossVaultSourceReadOnly(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	const projName = "cross-vault-demo"

	// Seed the SOURCE vault with a vv-shape Projects/<name>/sessions tree.
	sessDir := filepath.Join(srcDir, "Projects", projName, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	session1 := `---
session_id: "2026-05-01-01"
project: cross-vault-demo
date: "2026-05-01"
title: "Database migrations"
summary: "Versioned PostgreSQL migrations"
tag: implementation
---
## Human

We need a database migration system for PostgreSQL with up and down
migrations and rollback support.

## Assistant

Use sequential numbered SQL files plus a migrations tracking table. Each
migration runs in a transaction for atomicity.
`
	session2 := `---
session_id: "2026-05-02-01"
project: cross-vault-demo
date: "2026-05-02"
title: "Auth middleware"
summary: "JWT bearer middleware"
tag: implementation
---
## Human

Let's implement JWT authentication middleware for the HTTP API.

## Assistant

The middleware validates Bearer tokens with RS256 and attaches claims to
the request context. We touched internal/auth/middleware.go for this.
`
	for name, content := range map[string]string{
		"2026-05-01-01.md": session1,
		"2026-05-02-01.md": session2,
	} {
		if err := os.WriteFile(filepath.Join(sessDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := storage.Config{
		SearchDefaultLimit: 10,
		BoostWing:          0.12,
		BoostHall:          0.24,
		BoostRoom:          0.34,
		ChunkMaxChars:      800,
		ChunkOverlap:       100,
	}

	emb := embedder.NewMock(384)
	t.Cleanup(func() { emb.Close() })

	source := storage.NewVault(srcDir)
	dest := storage.NewVault(dstDir)

	// Engine MUST be bound to the destination — its embed-cache writes
	// land under the engine's vault.
	engine := search.NewEngine(emb, dest, cfg)
	t.Cleanup(func() { engine.Close() })

	// Snapshot the SOURCE tree BEFORE migration.
	before := hashTree(t, srcDir)
	if len(before) == 0 {
		t.Fatal("source snapshot is empty — fixture seeding failed")
	}

	ctx := context.Background()
	result, err := migrate.ImportVibeVault(ctx, source, dest, engine, emb, cfg, migrate.ImportOptions{})
	if err != nil {
		t.Fatalf("ImportVibeVault: %v", err)
	}

	// (a) Destination got data.
	if result.SessionsImported != 2 {
		t.Errorf("SessionsImported = %d, want 2", result.SessionsImported)
	}
	if result.DrawersCreated == 0 {
		t.Errorf("DrawersCreated = 0, want > 0")
	}

	// Discover the destination slug dir under palace/.
	palaceDirs, err := filepath.Glob(filepath.Join(dstDir, "palace", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(palaceDirs) == 0 {
		t.Fatal("no destination palace/<slug>/ directory created")
	}
	slugDir := palaceDirs[0]
	slugName := filepath.Base(slugDir)

	markerFile := filepath.Join(slugDir, ".local", "imported-sessions.jsonl")
	if _, err := os.Stat(markerFile); err != nil {
		t.Errorf("expected destination marker file %s: %v", markerFile, err)
	}

	cfgFile := filepath.Join(dstDir, "Projects", slugName, "config.toml")
	if _, err := os.Stat(cfgFile); err != nil {
		t.Errorf("expected destination config.toml %s: %v", cfgFile, err)
	}

	// (b) SOURCE is byte-for-byte stable.
	after := hashTree(t, srcDir)
	if len(after) != len(before) {
		t.Errorf("source file count changed: before=%d after=%d", len(before), len(after))
	}
	for rel, sum := range before {
		got, ok := after[rel]
		if !ok {
			t.Errorf("source file disappeared after migration: %s", rel)
			continue
		}
		if got != sum {
			t.Errorf("source file mutated after migration: %s", rel)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok {
			t.Errorf("new file appeared in source after migration: %s", rel)
		}
	}

	// Explicit belt-and-suspenders: no palace/ tree and no marker file
	// should ever appear under the source.
	if _, err := os.Stat(filepath.Join(srcDir, "palace")); !os.IsNotExist(err) {
		t.Errorf("source/palace/ was created (write leaked to source): err=%v", err)
	}
	_ = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.Name() == "imported-sessions.jsonl" {
			t.Errorf("marker file leaked to source: %s", path)
		}
		return nil
	})

	// (c) Idempotency across vaults: a second run dedupes via the
	// destination markers.
	result2, err := migrate.ImportVibeVault(ctx, source, dest, engine, emb, cfg, migrate.ImportOptions{})
	if err != nil {
		t.Fatalf("second ImportVibeVault: %v", err)
	}
	if result2.SessionsImported != 0 {
		t.Errorf("second run SessionsImported = %d, want 0", result2.SessionsImported)
	}
	if result2.SessionsSkipped != result.SessionsImported {
		t.Errorf("second run SessionsSkipped = %d, want %d (first run import count)",
			result2.SessionsSkipped, result.SessionsImported)
	}
}
