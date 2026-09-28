// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package departedpath

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func vaultWithRecord(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, RecordDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(RecordRel("p"))), []byte(`{"kind":"deleted"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// A record that exists but cannot be inspected fails CLOSED: departed.
func TestRecordExistsFailsClosedWhenUninspectable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through a mode-000 directory")
	}
	root := vaultWithRecord(t)
	dir := filepath.Join(root, RecordDir)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if !RecordExists(root, "p") {
		t.Fatal("an uninspectable record must count as present")
	}
	if !RecordExists(root, "never") {
		t.Fatal("an uninspectable records directory must fail closed for every slug")
	}
}

// RefuseAbs judges a path that is lexically outside the given root but
// resolves inside it (the root named through a symlink).
func TestRefuseAbsResolvesAPathOutsideTheNamedRoot(t *testing.T) {
	real := vaultWithRecord(t)
	link := filepath.Join(t.TempDir(), "vault-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := RefuseAbs(link, filepath.Join(real, "Projects", "p", "x.md")); !errors.Is(err, ErrDeparted) {
		t.Fatalf("err = %v, want ErrDeparted", err)
	}
	if err := RefuseRecordAbs(link, filepath.Join(real, filepath.FromSlash(RecordRel("q")))); !errors.Is(err, ErrRecordPath) {
		t.Fatalf("record path: err = %v, want ErrRecordPath", err)
	}
}
