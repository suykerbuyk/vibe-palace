// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// newVaultRoot returns a Vault whose Root is a real temp dir (vaultfs requires
// the vault root to exist so EvalSymlinks can canonicalise it).
func newVaultRoot(t *testing.T) *storage.Vault {
	t.Helper()
	return storage.NewVault(t.TempDir())
}

func sha256Hex(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func TestVaultWriteThenRead(t *testing.T) {
	vault := newVaultRoot(t)

	writeTool := VaultWriteTool(vault)
	if writeTool.Name != "vp_vault_write" {
		t.Fatalf("name = %q", writeTool.Name)
	}
	p, _ := json.Marshal(map[string]any{"path": "Notes/a.md", "content": "hello"})
	res, err := writeTool.Handler(context.Background(), p)
	if err != nil {
		t.Fatalf("write handler: %v", err)
	}
	wr := res.(vaultfs.WriteResult)
	if wr.Bytes != 5 || wr.Sha256 != sha256Hex("hello") {
		t.Errorf("write result = %+v", wr)
	}

	readTool := VaultReadTool(vault)
	rp, _ := json.Marshal(map[string]any{"path": "Notes/a.md"})
	rres, err := readTool.Handler(context.Background(), rp)
	if err != nil {
		t.Fatalf("read handler: %v", err)
	}
	c := rres.(vaultfs.Content)
	if c.Content != "hello" {
		t.Errorf("content = %q", c.Content)
	}
}

func TestVaultWriteMissingPath(t *testing.T) {
	vault := newVaultRoot(t)
	tool := VaultWriteTool(vault)
	p, _ := json.Marshal(map[string]any{"content": "x"})
	if _, err := tool.Handler(context.Background(), p); err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestVaultReadMissingFile(t *testing.T) {
	vault := newVaultRoot(t)
	tool := VaultReadTool(vault)
	p, _ := json.Marshal(map[string]any{"path": "nope.md"})
	if _, err := tool.Handler(context.Background(), p); err == nil {
		t.Fatal("expected ErrFileNotFound")
	}
}

func TestVaultList(t *testing.T) {
	vault := newVaultRoot(t)
	mustWrite(t, vault, "d/a.md", "aaa")
	mustWrite(t, vault, "d/b.md", "bb")

	tool := VaultListTool(vault)
	p, _ := json.Marshal(map[string]any{"path": "d"})
	res, err := tool.Handler(context.Background(), p)
	if err != nil {
		t.Fatalf("list handler: %v", err)
	}
	lr := res.(vaultListResult)
	if len(lr.Entries) != 2 {
		t.Errorf("entries = %d, want 2", len(lr.Entries))
	}
}

func TestVaultExists(t *testing.T) {
	vault := newVaultRoot(t)
	mustWrite(t, vault, "f.md", "x")

	tool := VaultExistsTool(vault)
	p, _ := json.Marshal(map[string]any{"path": "f.md"})
	res, err := tool.Handler(context.Background(), p)
	if err != nil {
		t.Fatalf("exists handler: %v", err)
	}
	e := res.(vaultfs.Existence)
	if !e.Exists || e.Type != "file" {
		t.Errorf("existence = %+v", e)
	}

	mp, _ := json.Marshal(map[string]any{"path": "missing"})
	mres, err := tool.Handler(context.Background(), mp)
	if err != nil {
		t.Fatalf("exists handler (missing): %v", err)
	}
	if mres.(vaultfs.Existence).Exists {
		t.Error("missing path should report exists=false")
	}
}

func TestVaultSha256(t *testing.T) {
	vault := newVaultRoot(t)
	mustWrite(t, vault, "f.md", "hello")

	tool := VaultSha256Tool(vault)
	p, _ := json.Marshal(map[string]any{"path": "f.md"})
	res, err := tool.Handler(context.Background(), p)
	if err != nil {
		t.Fatalf("sha256 handler: %v", err)
	}
	sr := res.(vaultfs.Sha256Result)
	if sr.Sha256 != sha256Hex("hello") || sr.Bytes != 5 {
		t.Errorf("sha256 result = %+v", sr)
	}
}

func TestVaultEdit(t *testing.T) {
	vault := newVaultRoot(t)
	mustWrite(t, vault, "f.md", "alpha bravo charlie")

	tool := VaultEditTool(vault)
	p, _ := json.Marshal(map[string]any{"path": "f.md", "old_string": "bravo", "new_string": "DELTA"})
	res, err := tool.Handler(context.Background(), p)
	if err != nil {
		t.Fatalf("edit handler: %v", err)
	}
	er := res.(vaultfs.EditResult)
	if er.Replacements != 1 {
		t.Errorf("replacements = %d", er.Replacements)
	}
	got, _ := os.ReadFile(filepath.Join(vault.Root, "f.md"))
	if string(got) != "alpha DELTA charlie" {
		t.Errorf("content = %q", got)
	}
}

func TestVaultEditMissingOldString(t *testing.T) {
	vault := newVaultRoot(t)
	mustWrite(t, vault, "f.md", "x")
	tool := VaultEditTool(vault)
	p, _ := json.Marshal(map[string]any{"path": "f.md"})
	if _, err := tool.Handler(context.Background(), p); err == nil {
		t.Fatal("expected error for missing old_string")
	}
}

func TestVaultDelete(t *testing.T) {
	vault := newVaultRoot(t)
	mustWrite(t, vault, "f.md", "x")

	tool := VaultDeleteTool(vault)
	p, _ := json.Marshal(map[string]any{"path": "f.md"})
	res, err := tool.Handler(context.Background(), p)
	if err != nil {
		t.Fatalf("delete handler: %v", err)
	}
	if !res.(vaultfs.DeleteResult).Removed {
		t.Error("removed should be true")
	}
	if _, err := os.Stat(filepath.Join(vault.Root, "f.md")); !os.IsNotExist(err) {
		t.Errorf("file should be gone: %v", err)
	}
}

func TestVaultMove(t *testing.T) {
	vault := newVaultRoot(t)
	mustWrite(t, vault, "src.md", "data")

	tool := VaultMoveTool(vault)
	if tool.Name != "vp_vault_move" {
		t.Fatalf("name = %q", tool.Name)
	}
	p, _ := json.Marshal(map[string]any{"from_path": "src.md", "to_path": "dst/dst.md"})
	res, err := tool.Handler(context.Background(), p)
	if err != nil {
		t.Fatalf("move handler: %v", err)
	}
	if !res.(vaultfs.MoveResult).Moved {
		t.Error("moved should be true")
	}
	got, _ := os.ReadFile(filepath.Join(vault.Root, "dst/dst.md"))
	if string(got) != "data" {
		t.Errorf("content = %q", got)
	}
}

func TestVaultMoveMissingArgs(t *testing.T) {
	vault := newVaultRoot(t)
	tool := VaultMoveTool(vault)
	p, _ := json.Marshal(map[string]any{"from_path": "a.md"})
	if _, err := tool.Handler(context.Background(), p); err == nil {
		t.Fatal("expected error for missing to_path")
	}
}

// mustWrite writes a file via the vaultfs backend (exercising the same path
// the tools use) and fails the test on error.
func mustWrite(t *testing.T, vault *storage.Vault, rel, content string) {
	t.Helper()
	if _, err := vaultfs.Write(vault.Root, rel, content, ""); err != nil {
		t.Fatalf("seed write %s: %v", rel, err)
	}
}
