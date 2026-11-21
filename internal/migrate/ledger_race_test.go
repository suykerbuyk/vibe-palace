// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/departedpath"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// holdTwoCallers installs the afterLegacyRead seam so that the first two callers
// both wait there until both have arrived: each has then observed the legacy
// marker file, and neither has begun to move it.
func holdTwoCallers(t *testing.T) {
	t.Helper()
	var mu sync.Mutex
	arrived := 0
	both := make(chan struct{})
	prev := afterLegacyRead
	afterLegacyRead = func() {
		mu.Lock()
		arrived++
		n := arrived
		if n == 2 {
			close(both)
		}
		mu.Unlock()
		if n > 2 {
			return
		}
		select {
		case <-both:
		case <-time.After(10 * time.Second):
			t.Error("the second caller never reached the legacy-read seam")
		}
	}
	t.Cleanup(func() { afterLegacyRead = prev })
}

// Two vp migrate runs that both see the legacy marker file must leave one
// ledger line per legacy line, and neither may fail: the loser's stale view of
// the legacy file must not turn into an error.
func TestLoadMarkersConcurrentLegacyMoveIsExactlyOnce(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	legacy, err := legacyMarkerFile(vault, "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"session_id":"s1","imported_at":"2026-01-01T00:00:00Z","source":"vibevault"}`,
		`{"session_id":"s2","imported_at":"2026-01-01T00:00:00Z","source":"vibevault"}`,
		`{"session_id":"s3","imported_at":"2026-01-01T00:00:00Z","source":"vibevault","reason":"empty"}`,
	}
	if err := os.WriteFile(legacy, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	holdTwoCallers(t)

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = loadMarkers(vault, "p")
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}
	path, _ := markerFile(vault, "p")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(got) != len(lines) {
		t.Errorf("ledger holds %d lines, want %d:\n%s", len(got), len(lines), data)
	}
	for _, l := range lines {
		if n := strings.Count(string(data), l+"\n"); n != 1 {
			t.Errorf("legacy line %q appears %d times, want 1", l, n)
		}
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("the legacy marker file is still there (stat err %v)", err)
	}
}

// Concurrent appenders to one ledger lose and interleave nothing. This passes on
// the old raw O_APPEND write too (a single short write is atomic): it pins the
// locked path, it is not what catches a revert (the sourceaudit funnel rule is).
func TestAppendMarkerConcurrentAppendsAreAllKept(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	const writers, each = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range each {
				if err := appendMarker(vault, "p", fmt.Sprintf("w%d-n%d", w, n), "src", ""); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	m, err := loadMarkers(vault, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != writers*each {
		t.Errorf("ledger holds %d sessions, want %d", len(m), writers*each)
	}
	path, _ := markerFile(vault, "p")
	data, _ := os.ReadFile(path)
	if n := strings.Count(string(data), "\n"); n != writers*each {
		t.Errorf("ledger holds %d lines, want %d", n, writers*each)
	}
}

// A torn tail (a crash mid-append, no newline) is skipped on read, and the
// complete lines before it still count.
func TestLoadMarkersSkipsATornTail(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if err := appendMarker(vault, "p", "s1", "src", ""); err != nil {
		t.Fatal(err)
	}
	path, _ := markerFile(vault, "p")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"session_id":"s2","imp`)
	f.Close()
	m, err := loadMarkers(vault, "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["s1"]; !ok || len(m) != 1 {
		t.Errorf("markers = %v, want only s1", m)
	}
}

// The ledger path carries no project slug, so a departed project's ledger line
// is still written: a marker is a hint, confirmed against the manifests.
func TestAppendMarkerForADepartedProjectStillWrites(t *testing.T) {
	root := t.TempDir()
	rec := filepath.Join(root, filepath.FromSlash(departedpath.RecordRel("gone")))
	if err := os.MkdirAll(filepath.Dir(rec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !departedpath.RecordExists(root, "gone") {
		t.Fatal("the departure record is not seen")
	}
	vault := storage.NewVault(root)
	if err := appendMarker(vault, "gone", "s1", "src", ""); err != nil {
		t.Fatalf("append for a departed project: %v", err)
	}
	m, err := loadMarkers(vault, "gone")
	if err != nil || len(m["s1"]) != 1 {
		t.Fatalf("markers = %v, %v", m, err)
	}
}
