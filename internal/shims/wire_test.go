// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shimPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), Filename(name))
}

func TestWireAddsMissingFile(t *testing.T) {
	path := shimPath(t, "restart")
	res, err := Wire(path, "restart", "Reload vault")
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	if res.Kind != Added {
		t.Errorf("Kind = %v, want Added", res.Kind)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "Reload vault") {
		t.Errorf("brief not in written shim: %s", data)
	}
}

func TestWireCreatesParentDir(t *testing.T) {
	// Emulate a fresh project with no .claude/ directory: Wire must bring
	// the parent into existence (0o755).
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "commands", Filename("wrap"))
	if _, err := Wire(path, "wrap", "wrap up"); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat parent: %v", err)
	}
	if !info.IsDir() {
		t.Error("parent not a dir")
	}
}

func TestWireIdempotentUnchanged(t *testing.T) {
	path := shimPath(t, "restart")
	if _, err := Wire(path, "restart", "Reload"); err != nil {
		t.Fatalf("first Wire: %v", err)
	}
	info1, _ := os.Stat(path)
	data1, _ := os.ReadFile(path)

	res, err := Wire(path, "restart", "Reload")
	if err != nil {
		t.Fatalf("second Wire: %v", err)
	}
	if res.Kind != Unchanged {
		t.Errorf("Kind = %v, want Unchanged", res.Kind)
	}
	info2, _ := os.Stat(path)
	data2, _ := os.ReadFile(path)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Errorf("mtime changed on Unchanged: %v -> %v", info1.ModTime(), info2.ModTime())
	}
	if string(data1) != string(data2) {
		t.Error("content changed on Unchanged")
	}
}

func TestWireUpdatesOnStaleSha(t *testing.T) {
	path := shimPath(t, "restart")
	if _, err := Wire(path, "restart", "old brief"); err != nil {
		t.Fatalf("seed Wire: %v", err)
	}
	prevSha := ExpectedSha("restart", "old brief")

	res, err := Wire(path, "restart", "new brief")
	if err != nil {
		t.Fatalf("update Wire: %v", err)
	}
	if res.Kind != Updated {
		t.Errorf("Kind = %v, want Updated", res.Kind)
	}
	if res.PrevSha != prevSha {
		t.Errorf("PrevSha = %q, want %q", res.PrevSha, prevSha)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "new brief") {
		t.Errorf("new brief not present: %s", data)
	}
	if strings.Contains(string(data), "old brief") {
		t.Errorf("old brief still present: %s", data)
	}
}

func TestWireLeavesCustomFileAlone(t *testing.T) {
	path := shimPath(t, "mine")
	orig := "# personal override\ndo stuff\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	info1, _ := os.Stat(path)

	res, err := Wire(path, "mine", "some brief")
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	if res.Kind != Custom {
		t.Errorf("Kind = %v, want Custom", res.Kind)
	}
	data, _ := os.ReadFile(path)
	if string(data) != orig {
		t.Errorf("custom file was modified:\nbefore=%q\nafter =%q", orig, data)
	}
	info2, _ := os.Stat(path)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Error("custom file mtime changed")
	}
}

func TestWirePreservesFileMode(t *testing.T) {
	path := shimPath(t, "restart")
	if _, err := Wire(path, "restart", "v1"); err != nil {
		t.Fatalf("first Wire: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := Wire(path, "restart", "v2"); err != nil {
		t.Fatalf("update Wire: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode after update = %v, want 0600", info.Mode().Perm())
	}
}

func TestRemoveDeletesOnlyMarkedFiles(t *testing.T) {
	// Our shim: Remove should delete it.
	marked := shimPath(t, "restart")
	if _, err := Wire(marked, "restart", "brief"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ok, err := Remove(marked)
	if err != nil {
		t.Fatalf("Remove marked: %v", err)
	}
	if !ok {
		t.Error("Remove returned false on a marked file")
	}
	if _, err := os.Stat(marked); !os.IsNotExist(err) {
		t.Errorf("file still present after Remove: %v", err)
	}

	// Custom file: Remove must refuse.
	custom := filepath.Join(t.TempDir(), Filename("mine"))
	if err := os.WriteFile(custom, []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err = Remove(custom)
	if err != nil {
		t.Fatalf("Remove custom: %v", err)
	}
	if ok {
		t.Error("Remove reported true on a custom file")
	}
	if _, err := os.Stat(custom); err != nil {
		t.Errorf("custom file was deleted: %v", err)
	}
}

func TestRemoveMissingIsNoop(t *testing.T) {
	ok, err := Remove(filepath.Join(t.TempDir(), "nope.md"))
	if err != nil {
		t.Fatalf("Remove missing: %v", err)
	}
	if ok {
		t.Error("Remove returned true for missing file")
	}
}

func TestResultKindString(t *testing.T) {
	cases := map[ResultKind]string{
		Unchanged: "unchanged",
		Added:     "added",
		Updated:   "updated",
		Custom:    "custom",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("%d: got %q want %q", k, got, want)
		}
	}
}
