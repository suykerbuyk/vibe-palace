// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// TestExecutor_Write_CreateNew covers the fresh-write path: no
// existing target, Backup=Never means no .bak is produced.
func TestExecutor_Write_CreateNew(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "sub", "dir", "new.md")
	exec := templates.NewExecutor()
	if err := exec.Write(dst, []byte("hello"), templates.WriteOptions{Backup: templates.BackupPolicyNever}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want %q", got, "hello")
	}
	if _, err := os.Stat(dst + ".bak"); !os.IsNotExist(err) {
		t.Errorf("unexpected .bak: err=%v", err)
	}
}

// TestExecutor_Write_OverwriteNeverNoBackup proves BackupPolicyNever
// leaves no .bak even when the destination already exists (this is
// the commands upgrade contract).
func TestExecutor_Write_OverwriteNeverNoBackup(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "f.md")
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := templates.NewExecutor().Write(dst, []byte("new"), templates.WriteOptions{Backup: templates.BackupPolicyNever}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "new" {
		t.Errorf("content = %q, want new", got)
	}
	if _, err := os.Stat(dst + ".bak"); !os.IsNotExist(err) {
		t.Errorf("unexpected .bak: %v", err)
	}
}

// TestExecutor_Write_BackupAlwaysCopiesPriorBytes proves
// BackupPolicyAlways writes a .bak containing the pre-existing bytes
// (init-materialize + skills-upgrade contract).
func TestExecutor_Write_BackupAlwaysCopiesPriorBytes(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "f.md")
	if err := os.WriteFile(dst, []byte("user-edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := templates.NewExecutor().Write(dst, []byte("upgrade"), templates.WriteOptions{Backup: templates.BackupPolicyAlways}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "upgrade" {
		t.Errorf("primary = %q, want upgrade", got)
	}
	bak, err := os.ReadFile(dst + ".bak")
	if err != nil {
		t.Fatalf("read bak: %v", err)
	}
	if string(bak) != "user-edit" {
		t.Errorf(".bak = %q, want user-edit", bak)
	}
}

// TestExecutor_Write_BackupRenameSavesPrior proves
// BackupPolicyRename has the same user-visible effect (old bytes in
// .bak, new bytes in primary) as Always; they differ only in failure
// semantics which this test does not exercise.
func TestExecutor_Write_BackupRenameSavesPrior(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "f.md")
	if err := os.WriteFile(dst, []byte("prior"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := templates.NewExecutor().Write(dst, []byte("next"), templates.WriteOptions{Backup: templates.BackupPolicyRename}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "next" {
		t.Errorf("primary = %q", got)
	}
	bak, err := os.ReadFile(dst + ".bak")
	if err != nil {
		t.Fatalf("read bak: %v", err)
	}
	if string(bak) != "prior" {
		t.Errorf(".bak = %q", bak)
	}
}

// TestExecutor_Write_BackupNewFileNoBak proves a .bak is not
// fabricated when the target did not exist.
func TestExecutor_Write_BackupNewFileNoBak(t *testing.T) {
	for _, p := range []templates.BackupPolicy{templates.BackupPolicyAlways, templates.BackupPolicyRename} {
		dir := t.TempDir()
		dst := filepath.Join(dir, "new.md")
		if err := templates.NewExecutor().Write(dst, []byte("x"), templates.WriteOptions{Backup: p}); err != nil {
			t.Fatalf("Write (policy=%d): %v", p, err)
		}
		if _, err := os.Stat(dst + ".bak"); !os.IsNotExist(err) {
			t.Errorf("policy=%d created .bak for new file: %v", p, err)
		}
	}
}

// TestExecutor_Write_AtomicOnRenameTmpGone checks that a successful
// write leaves no .tmp sibling behind.
func TestExecutor_Write_AtomicLeavesNoTmp(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "f.md")
	if err := templates.NewExecutor().Write(dst, []byte("x"), templates.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "f.md" {
			t.Errorf("unexpected leftover: %s", e.Name())
		}
	}
}

// TestHashFile verifies the sha256 helper against known input.
func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := templates.HashFile(p)
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	// sha256("abc") = ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Errorf("hash = %s, want %s", got, want)
	}
	if _, err := templates.HashFile(filepath.Join(dir, "nope")); !os.IsNotExist(err) {
		t.Errorf("missing file err = %v, want IsNotExist", err)
	}
}

// TestClassify exercises every row of the classification table.
func TestClassify(t *testing.T) {
	cases := []struct {
		name             string
		vault, emb, lock string
		want             templates.Classification
	}{
		{"missing vault", "", "aa", "", templates.ClassMissing},
		{"missing vault with lock", "", "aa", "aa", templates.ClassMissing},
		{"adoptable: no lock, vault matches embedded", "aa", "aa", "", templates.ClassAdoptable},
		{"diverged: no lock, vault differs from embedded", "bb", "aa", "", templates.ClassDiverged},
		{"unchanged: all three equal", "aa", "aa", "aa", templates.ClassUnchanged},
		{"embedded bumped: vault==lock, embedded differs", "aa", "bb", "aa", templates.ClassEmbeddedBumped},
		{"user edited: embedded==lock, vault differs", "bb", "aa", "aa", templates.ClassUserEdited},
		{"diverged: all three differ", "bb", "cc", "aa", templates.ClassDiverged},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := templates.Classify(c.vault, c.emb, c.lock)
			if got != c.want {
				t.Errorf("Classify(%q,%q,%q) = %d, want %d", c.vault, c.emb, c.lock, got, c.want)
			}
		})
	}
}

// TestExecutor_Write_DefaultPerm verifies the Perm=0 default becomes 0644.
func TestExecutor_Write_DefaultPerm(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "f")
	if err := templates.NewExecutor().Write(dst, []byte("x"), templates.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	// Only the low 9 bits are interesting (platform may mask others).
	if st.Mode().Perm() != 0o644 {
		t.Errorf("perm = %o, want 0644", st.Mode().Perm())
	}
}

// TestExecutor_Write_StampsSurfaceWhenVaultRootSet is the regression guard for
// the write-path-correctness fix: a template write that names its vault root
// now stamps .surface structurally. Before the private atomicWrite copy was
// removed, `vp commands`/`vp skills upgrade` (which route through Executor.Write)
// stamped nowhere, so a template bump left the surface version stale.
func TestExecutor_Write_StampsSurfaceWhenVaultRootSet(t *testing.T) {
	vault := t.TempDir()
	dst := filepath.Join(vault, "Projects", "proj", "commands", "restart.md")
	if err := templates.NewExecutor().Write(dst, []byte("body"), templates.WriteOptions{
		Backup:    templates.BackupPolicyNever,
		VaultRoot: vault,
	}); err != nil {
		t.Fatal(err)
	}
	s, err := surface.ReadStamp(filepath.Join(vault, "Projects", "proj"))
	if err != nil {
		t.Fatalf("read stamp: %v", err)
	}
	if s.Surface != surface.MCPSurfaceVersion {
		t.Errorf("stamp surface = %d, want %d", s.Surface, surface.MCPSurfaceVersion)
	}
}

// TestExecutor_Write_NoStampWhenVaultRootEmpty verifies the non-vault write
// path is unchanged: an empty VaultRoot skips stamping entirely.
func TestExecutor_Write_NoStampWhenVaultRootEmpty(t *testing.T) {
	vault := t.TempDir()
	dst := filepath.Join(vault, "Projects", "proj", "commands", "restart.md")
	if err := templates.NewExecutor().Write(dst, []byte("body"), templates.WriteOptions{
		Backup: templates.BackupPolicyNever,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vault, "Projects", "proj", ".surface")); !os.IsNotExist(err) {
		t.Errorf("empty VaultRoot must not stamp (stat err=%v)", err)
	}
}
