// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestWriteHostLocalWithBackup pins what `vp config upgrade` has always done to
// a host-local file, now that the checkout rebind shares it: the .bak holds the
// pre-image, every file is 0644, no .tmp survives, and each failure keeps the
// error prefix config upgrade reported.
func TestWriteHostLocalWithBackup(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".vibe-palace.toml")
	if err := os.WriteFile(p, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bak, err := WriteHostLocalWithBackup(p, []byte("old\n"), []byte("new\n"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if bak != p+".bak" {
		t.Errorf("backup path %q, want %q", bak, p+".bak")
	}
	for path, want := range map[string]string{p: "new\n", p + ".bak": "old\n"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Errorf("%s = %q (err %v), want %q", path, got, err, want)
		}
		if st, err := os.Stat(path); err == nil && runtime.GOOS != "windows" && st.Mode().Perm() != 0o644 {
			t.Errorf("%s mode %v, want 0644", path, st.Mode().Perm())
		}
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("a .tmp must not survive a successful write (stat err %v)", err)
	}

	// Failure texts: a backup path that is a directory, and a target that is a
	// directory (the rename fails).
	blocked := filepath.Join(dir, "blocked.toml")
	if err := os.MkdirAll(blocked+".bak", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteHostLocalWithBackup(blocked, nil, []byte("x")); err == nil || !strings.HasPrefix(err.Error(), "create backup: ") {
		t.Errorf("backup failure = %v, want the \"create backup: \" prefix", err)
	}
	dirTarget := filepath.Join(dir, "target-is-a-dir")
	if err := os.MkdirAll(filepath.Join(dirTarget, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteHostLocalWithBackup(dirTarget, nil, []byte("x")); err == nil || !strings.HasPrefix(err.Error(), "rename: ") {
		t.Errorf("rename failure = %v, want the \"rename: \" prefix", err)
	}
	if _, err := os.Stat(dirTarget + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("a failed rename must remove its .tmp (stat err %v)", err)
	}
}
