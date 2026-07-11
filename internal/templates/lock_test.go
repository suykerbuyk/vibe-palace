// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestReadLockAbsent(t *testing.T) {
	dir := t.TempDir()
	l, err := ReadLock(dir)
	if err != nil {
		t.Fatalf("ReadLock on empty dir: %v", err)
	}
	if l.Entries == nil {
		t.Fatal("expected initialized Entries map, got nil")
	}
	if len(l.Entries) != 0 {
		t.Fatalf("expected zero entries, got %d", len(l.Entries))
	}
}

func TestWriteReadLockRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	original := Lock{Entries: map[string]LockEntry{
		"Templates/commands/wrap.md": {
			EmbeddedSHA: "aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff6666aaaa7777bbbb8888",
			WrittenAt:   now,
		},
		"Templates/workflow.md": {
			EmbeddedSHA: "0000111122223333444455556666777788889999aaaabbbbccccddddeeeeffff",
			WrittenAt:   now.Add(-time.Hour),
		},
		"Templates/commands/restart.md": {
			EmbeddedSHA: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			WrittenAt:   now.Add(-48 * time.Hour),
		},
	}}
	if err := WriteLock(dir, original); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	got, err := ReadLock(dir)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("round-trip mismatch:\n want=%+v\n  got=%+v", original, got)
	}
	// Path separators must survive.
	for k := range original.Entries {
		if _, ok := got.Entries[k]; !ok {
			t.Fatalf("key %q lost in round-trip; got keys=%v", k, keysOf(got.Entries))
		}
	}
}

func TestWriteLockEmpty(t *testing.T) {
	dir := t.TempDir()
	// Empty Entries — writing and re-reading should still work.
	if err := WriteLock(dir, Lock{Entries: map[string]LockEntry{}}); err != nil {
		t.Fatalf("WriteLock empty: %v", err)
	}
	got, err := ReadLock(dir)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if got.Entries == nil {
		t.Fatal("expected initialized Entries, got nil")
	}
	if len(got.Entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(got.Entries))
	}

	// Also: nil map is tolerated.
	if err := WriteLock(dir, Lock{}); err != nil {
		t.Fatalf("WriteLock nil-map: %v", err)
	}
	got2, err := ReadLock(dir)
	if err != nil {
		t.Fatalf("ReadLock after nil-map write: %v", err)
	}
	if got2.Entries == nil {
		t.Fatal("expected initialized Entries after nil-map write")
	}
}

func TestReadLockMalformed(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, LockRelPath)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("this is ][ not valid TOML =="), 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	_, err := ReadLock(dir)
	if err == nil {
		t.Fatal("expected error on malformed TOML, got nil")
	}
}

func TestWriteLockAtomicNoTmpLeftover(t *testing.T) {
	dir := t.TempDir()
	l := Lock{Entries: map[string]LockEntry{
		"Templates/x.md": {EmbeddedSHA: "f00d", WrittenAt: time.Now().UTC().Truncate(time.Second)},
	}}
	if err := WriteLock(dir, l); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	// No *.tmp files should remain in .vibe-palace/.
	entries, err := os.ReadDir(filepath.Join(dir, ".vibe-palace"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) == ".tmp" || hasSuffix(name, ".tmp") {
			t.Fatalf("tmp file leftover: %s", name)
		}
	}
}

func TestWriteLockCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	// Sanity: .vibe-palace does not exist yet.
	if _, err := os.Stat(filepath.Join(dir, ".vibe-palace")); !os.IsNotExist(err) {
		t.Fatalf("expected .vibe-palace absent, stat err=%v", err)
	}
	l := Lock{Entries: map[string]LockEntry{
		"Templates/a.md": {EmbeddedSHA: "cafe", WrittenAt: time.Now().UTC().Truncate(time.Second)},
	}}
	if err := WriteLock(dir, l); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, ".vibe-palace"))
	if err != nil {
		t.Fatalf("stat .vibe-palace: %v", err)
	}
	if !info.IsDir() {
		t.Fatal(".vibe-palace is not a directory")
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Fatalf(".vibe-palace perm = %o, want 0755", perm)
	}
	// And the lock itself should be 0o644.
	linfo, err := os.Stat(filepath.Join(dir, LockRelPath))
	if err != nil {
		t.Fatalf("stat lock: %v", err)
	}
	if perm := linfo.Mode().Perm(); perm != 0o644 {
		t.Fatalf("lock perm = %o, want 0644", perm)
	}
}

func TestWriteLockPathSeparatorsPreserved(t *testing.T) {
	dir := t.TempDir()
	key := "Templates/commands/deeply/nested/thing.md"
	l := Lock{Entries: map[string]LockEntry{
		key: {EmbeddedSHA: "abcd", WrittenAt: time.Now().UTC().Truncate(time.Second)},
	}}
	if err := WriteLock(dir, l); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	got, err := ReadLock(dir)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if _, ok := got.Entries[key]; !ok {
		t.Fatalf("expected key %q preserved; got keys=%v", key, keysOf(got.Entries))
	}
}

func TestWriteLockWrittenAtPreserved(t *testing.T) {
	dir := t.TempDir()
	when := time.Date(2025, 6, 7, 8, 9, 10, 0, time.UTC)
	l := Lock{Entries: map[string]LockEntry{
		"Templates/t.md": {EmbeddedSHA: "11", WrittenAt: when},
	}}
	if err := WriteLock(dir, l); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	got, err := ReadLock(dir)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if !got.Entries["Templates/t.md"].WrittenAt.Equal(when) {
		t.Fatalf("WrittenAt mismatch: got %v, want %v", got.Entries["Templates/t.md"].WrittenAt, when)
	}
}

func TestWriteLockConcurrent(t *testing.T) {
	dir := t.TempDir()
	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	now := time.Now().UTC().Truncate(time.Second)
	for i := range n {
		go func() {
			defer wg.Done()
			// Each writer has distinct content — last-writer-wins is
			// acceptable; what must hold is that the final file is a
			// valid, fully-written Lock matching exactly one of them.
			l := Lock{Entries: map[string]LockEntry{
				"Templates/c.md": {
					EmbeddedSHA: sha("writer", i),
					WrittenAt:   now.Add(time.Duration(i) * time.Second),
				},
			}}
			if err := WriteLock(dir, l); err != nil {
				t.Errorf("writer %d: WriteLock: %v", i, err)
			}
		}()
	}
	wg.Wait()
	got, err := ReadLock(dir)
	if err != nil {
		t.Fatalf("ReadLock after concurrent writers: %v", err)
	}
	entry, ok := got.Entries["Templates/c.md"]
	if !ok {
		t.Fatalf("expected Templates/c.md in final lock; got keys=%v", keysOf(got.Entries))
	}
	// SHA must match one of the writers' payloads.
	found := false
	for i := range n {
		if entry.EmbeddedSHA == sha("writer", i) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("final SHA %q matches none of the writers", entry.EmbeddedSHA)
	}
	// And no *.tmp leftovers.
	entries, err := os.ReadDir(filepath.Join(dir, ".vibe-palace"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if hasSuffix(e.Name(), ".tmp") {
			t.Fatalf("tmp leftover after concurrent writes: %s", e.Name())
		}
	}
}

func TestWriteLockMkdirBlocked(t *testing.T) {
	dir := t.TempDir()
	// Place a regular file where .vibe-palace/ should be; MkdirAll
	// will fail because the path component is not a directory.
	blocker := filepath.Join(dir, ".vibe-palace")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("setup blocker: %v", err)
	}
	err := WriteLock(dir, Lock{Entries: map[string]LockEntry{
		"Templates/x.md": {EmbeddedSHA: "f", WrittenAt: time.Now()},
	}})
	if err == nil {
		t.Fatal("expected WriteLock to fail when .vibe-palace is a file")
	}
}

func TestWriteLockTmpCreateFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	dir := t.TempDir()
	// Pre-create .vibe-palace/ as read-only so os.CreateTemp inside
	// it fails — exercises the tmp-create error branch.
	vp := filepath.Join(dir, ".vibe-palace")
	if err := os.MkdirAll(vp, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(vp, 0o500); err != nil {
		t.Fatalf("chmod ro: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(vp, 0o755) })
	err := WriteLock(dir, Lock{Entries: map[string]LockEntry{
		"Templates/x.md": {EmbeddedSHA: "f", WrittenAt: time.Now()},
	}})
	if err == nil {
		t.Fatal("expected WriteLock to fail when tmp cannot be created")
	}
}

func TestReadLockUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	dir := t.TempDir()
	lockPath := filepath.Join(dir, LockRelPath)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte(""), 0o000); err != nil {
		t.Fatalf("write unreadable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockPath, 0o644) })
	_, err := ReadLock(dir)
	if err == nil {
		t.Fatal("expected error reading unreadable lock, got nil")
	}
}

// --- helpers ---

func keysOf(m map[string]LockEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func hasSuffix(s, suf string) bool {
	if len(s) < len(suf) {
		return false
	}
	return s[len(s)-len(suf):] == suf
}

func sha(prefix string, i int) string {
	// Deterministic 64-char hex per writer.
	base := prefix
	// Fill to 64 chars using hex-safe digits.
	const hexpad = "0123456789abcdef"
	b := make([]byte, 64)
	for j := range 64 {
		b[j] = hexpad[(j+i+len(base))%16]
	}
	return string(b)
}
