// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package atomicfile

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func TestWrite_RoundTripDefaultPerm(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "file.txt")
	if err := Write("", p, []byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q", got)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("perm = %o, want 0644", info.Mode().Perm())
	}
}

func TestWrite_InheritPerm(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(p, []byte("orig"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write("", p, []byte("new"), WithInheritPerm()); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("inherited perm = %o, want 0600", info.Mode().Perm())
	}
}

func TestWrite_InheritPermFallbackOnMissingTarget(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "new.txt")
	if err := Write("", p, []byte("x"), WithInheritPerm()); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("fallback perm = %o, want 0644", info.Mode().Perm())
	}
}

func TestWrite_WithFsyncRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "file.txt")
	if err := Write("", p, []byte("durable"), WithFsync()); err != nil {
		t.Fatalf("Write with fsync: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "durable" {
		t.Fatalf("content = %q", got)
	}
}

func TestWrite_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(p, []byte("old-and-longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write("", p, []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "new" {
		t.Fatalf("overwrite content = %q, want \"new\"", got)
	}
}

func TestWrite_ErrorWhenParentIsFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Target's parent ("blocker") is a regular file → MkdirAll must fail.
	if err := Write("", filepath.Join(blocker, "child.txt"), []byte("y")); err == nil {
		t.Fatal("expected error when parent path is a file, got nil")
	}
}

func TestWrite_ErrorWhenDirNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "ro")
	if err := os.Mkdir(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sub, 0o755) })
	// Dir exists (MkdirAll is a no-op) but is not writable → CreateTemp fails.
	if err := Write("", filepath.Join(sub, "file.txt"), []byte("y")); err == nil {
		t.Fatal("expected error writing into a read-only dir, got nil")
	}
}

func TestWrite_StampsVaultWrite(t *testing.T) {
	vault := t.TempDir()
	p := filepath.Join(vault, "Projects", "proj", "resume.md")
	if err := Write(vault, p, []byte("body")); err != nil {
		t.Fatal(err)
	}
	s, err := surface.ReadStamp(filepath.Join(vault, "Projects", "proj"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Surface != surface.MCPSurfaceVersion {
		t.Fatalf("stamp surface = %d, want %d", s.Surface, surface.MCPSurfaceVersion)
	}
}

func TestWrite_NoStampWhenVaultRootEmpty(t *testing.T) {
	vault := t.TempDir()
	p := filepath.Join(vault, "Projects", "proj", "resume.md")
	if err := Write("", p, []byte("body")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vault, "Projects", "proj", ".surface")); !os.IsNotExist(err) {
		t.Fatalf("empty vaultRoot should not stamp (err=%v)", err)
	}
}

func TestWrite_NoStampOutsideVault(t *testing.T) {
	vault := t.TempDir()
	other := t.TempDir()
	p := filepath.Join(other, "file.txt")
	if err := Write(vault, p, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other, ".surface")); !os.IsNotExist(err) {
		t.Fatalf("outside-vault write should not stamp (err=%v)", err)
	}
}

// TestWriteObserverSeesBothEntryPoints pins the invariant SetWriteObserver's own
// comment states: every content write this package completes is observed.
//
// It covers WriteStream specifically because that is where the first cut of the
// seam was silent. A task write routed through WriteStream would have vanished
// from the per-file write count that cmd/vp's
// TestBoardFieldsWritesEachFileExactlyOnce depends on, and nothing would have
// failed to say so.
func TestWriteObserverSeesBothEntryPoints(t *testing.T) {
	dir := t.TempDir()
	// Guarded because the observer is package-global and notifyWrite invokes it
	// OUTSIDE SetWriteObserver's own mutex — so two concurrent writes reach this
	// closure concurrently. Nothing here runs in parallel today; the guard is what
	// keeps SetWriteObserver's doc claim ("tests that install it may run alongside
	// others in the same binary") true of the tests as well as of the seam.
	var mu sync.Mutex
	var seen []string
	restore := SetWriteObserver(func(p string) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, filepath.Base(p))
	})
	defer restore()

	if err := Write("", filepath.Join(dir, "via-write"), []byte("a")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := WriteStream("", filepath.Join(dir, "via-stream"), func(w io.Writer) error {
		_, err := w.Write([]byte("b"))
		return err
	}); err != nil {
		t.Fatalf("WriteStream: %v", err)
	}

	want := []string{"via-write", "via-stream"}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != len(want) {
		t.Fatalf("observer saw %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("observer[%d] = %q, want %q", i, seen[i], want[i])
		}
	}
}

// TestWriteObserverIsRestoredAndOptional pins that the seam is inert by default
// and that restore actually restores — a leaked observer would make an unrelated
// test in the same binary count writes it never made.
func TestWriteObserverIsRestoredAndOptional(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var count int
	restore := SetWriteObserver(func(string) {
		mu.Lock()
		defer mu.Unlock()
		count++
	})
	if err := Write("", filepath.Join(dir, "one"), []byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	restore()
	if err := Write("", filepath.Join(dir, "two"), []byte("y")); err != nil {
		t.Fatalf("Write after restore: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Errorf("count = %d, want 1 — the observer kept firing after restore", count)
	}
}
