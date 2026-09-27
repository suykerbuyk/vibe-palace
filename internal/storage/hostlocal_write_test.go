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

// TestWriteHostLocalWithBackup pins what `vp config upgrade`, `vp config bind`
// and the checkout rebind do to a host-local file: the .bak holds the
// pre-image, the file and its .bak keep the file's own mode (a 0600 config
// stays 0600 — it used to be forced to 0644), no .tmp survives, and each
// failure keeps the error prefix config upgrade reported.
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
		if st, err := os.Stat(path); err == nil && runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600 (the original file's)", path, st.Mode().Perm())
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

// A .bak left at another mode by an earlier write ends at the config's mode,
// and the new bytes never enter the old .bak file (they go into a fresh temp
// at the target mode, renamed over it) — so a 0600 config's bytes are never,
// even briefly, in a world-readable file. MUTATION CONTRACT: drop the chmod
// and the 0640 case goes RED; write the .bak in place and the inode check does.
func TestWriteHostLocalBackupTakesTheConfigsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes and inodes")
	}
	for _, c := range []struct {
		name            string
		cfgMode, bakWas os.FileMode
	}{
		{"config_0600_bak_0644", 0o600, 0o644},
		{"config_0640_bak_0666", 0o640, 0o666},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "config.toml")
			if err := os.WriteFile(p, []byte("old\n"), c.cfgMode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, c.cfgMode); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p+".bak", []byte("stale\n"), c.bakWas); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p+".bak", c.bakWas); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(p + ".bak")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := WriteHostLocalWithBackup(p, []byte("old\n"), []byte("new\n")); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(p + ".bak")
			if err != nil {
				t.Fatal(err)
			}
			if after.Mode().Perm() != c.cfgMode {
				t.Errorf(".bak mode %v, want the config's %v", after.Mode().Perm(), c.cfgMode)
			}
			if os.SameFile(before, after) {
				t.Error("the new bytes were written into the old .bak file, which held its old mode while they landed")
			}
			if leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp*")); len(leftovers) != 0 {
				t.Errorf("temp files survived: %v", leftovers)
			}
		})
	}
}
