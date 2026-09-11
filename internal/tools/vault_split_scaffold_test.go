// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSplitLeakGateGlobal: leak gate 2 checks the vault-global trees only —
// the retired templates.lock is no longer one of its inputs. A file in a tree
// the split did not include is a leak, a non-regular entry is a leak, an
// included tree is not checked, and a tree that cannot be inventoried fails.
func TestSplitLeakGateGlobal(t *testing.T) {
	put := func(t *testing.T, dest, rel string) {
		t.Helper()
		p := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	clean := t.TempDir()
	put(t, clean, ".vibe-palace/templates.lock") // not a leak gate input any more
	if got := splitLeakGateGlobal(clean, vaultSplitParams{}); len(got) != 0 {
		t.Errorf("clean destination: %v", got)
	}

	leaky := t.TempDir()
	put(t, leaky, "Templates/commands/wrap.md")
	put(t, leaky, "Knowledge/learnings/l.md")
	got := splitLeakGateGlobal(leaky, vaultSplitParams{})
	if len(got) != 2 || !strings.Contains(strings.Join(got, "\n"), "destination Templates holds 1 file(s)") {
		t.Errorf("leaky destination: %v", got)
	}
	if got := splitLeakGateGlobal(leaky, vaultSplitParams{IncludeLearnings: true}); len(got) != 1 {
		t.Errorf("an included tree was checked: %v", got)
	}

	if runtime.GOOS != "windows" {
		linked := t.TempDir()
		if err := os.MkdirAll(filepath.Join(linked, "Audits"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(os.TempDir(), filepath.Join(linked, "Audits", "link")); err != nil {
			t.Fatal(err)
		}
		if got := splitLeakGateGlobal(linked, vaultSplitParams{}); len(got) != 1 || !strings.Contains(got[0], "non-regular") {
			t.Errorf("a non-regular entry: %v", got)
		}
	}

	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		unreadable := t.TempDir()
		dir := filepath.Join(unreadable, "Audits", "sub")
		put(t, unreadable, "Audits/sub/a.md")
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		if got := splitLeakGateGlobal(unreadable, vaultSplitParams{}); len(got) != 1 || !strings.Contains(got[0], "inventory destination") {
			t.Errorf("an unreadable tree: %v", got)
		}
	}
}

// TestSplitScaffoldDestination_FailsClosed: the scaffold is only the Vault
// reconciler (no Templates reconcile any more), and any failure in its Report
// aborts before anything is copied.
func TestSplitScaffoldDestination_FailsClosed(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "vault")
	if err := splitScaffoldDestination(context.Background(), dest); err != nil {
		t.Fatalf("fresh destination: %v", err)
	}
	for _, rel := range []string{".gitignore", ".git"} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Errorf("%s not created: %v", rel, err)
		}
	}
	for _, rel := range []string{"Templates", ".vibe-palace/templates.lock"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s created (err=%v)", rel, err)
		}
	}

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("read-only-directory fixture needs a non-root POSIX user")
	}
	parent := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	err := splitScaffoldDestination(context.Background(), filepath.Join(parent, "vault"))
	if err == nil || !strings.Contains(err.Error(), "nothing was copied") {
		t.Errorf("an unwritable destination: err = %v", err)
	}
}
