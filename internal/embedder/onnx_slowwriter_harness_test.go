// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// This file is the PRIMARY regression evidence for the shared ONNX model
// cache race NewONNX guards against (see modelCacheLockPath and NewONNX's
// doc comment in onnx.go): a deterministic, network-free harness that
// reproduces the EXACT vulnerable sequence hugot.DownloadModel triggers via
// viant/afs@v1.30.0's file/upload.go:Upload -- os.Remove the existing file,
// os.OpenFile(O_CREATE|O_WRONLY) at the SAME path, then stream new content
// into it IN PLACE, no temp file, no rename -- with an artificial per-chunk
// delay standing in for the real network transfer time that made the
// original race a low, timing-dependent hit rate (1-in-4 on the reporting
// machine; 0-in-5 for the real cross-process test in this sandbox).
//
// TestONNXCacheHarness_WithoutLock_ReliablyTearsReads proves the harness is a
// true-positive detector: it reliably observes torn reads with no
// coordination. TestONNXCacheHarness_WithLock_NeverTearsReads proves that
// vaultlock.Acquire, held by both sides exactly the way NewONNX holds it
// around hugot.DownloadModel, closes that window completely. Both must pass
// for this to stand as evidence the production fix works -- proving the
// negative (sub-test B) means nothing without first proving the detector is
// real (sub-test A). The cross-process test in onnx_crossprocess_test.go is
// supplementary, network-timing-dependent evidence only; this harness is the
// primary evidence because it is fast and deterministic.
const (
	harnessIterations   = 20
	harnessChunkSize    = 4096
	harnessChunkDelay   = 5 * time.Millisecond
	harnessPollInterval = 50 * time.Microsecond
)

// harnessPayload builds a distinguishable multi-chunk payload labeled with
// label, large enough (5 chunks at harnessChunkSize) that slowWrite's
// per-chunk sleep gives a concurrent reader a real window to observe a
// partially-written file.
func harnessPayload(label string) []byte {
	line := []byte(label + "-")
	buf := make([]byte, 0, harnessChunkSize*5+len(line))
	for len(buf) < harnessChunkSize*5 {
		buf = append(buf, line...)
	}
	return buf
}

// slowWrite reproduces the EXACT vulnerable sequence hugot.DownloadModel
// triggers via viant/afs@v1.30.0's file/upload.go:Upload: remove any existing
// file, create it fresh at the SAME path, then stream new content into it IN
// PLACE with no temp file and no rename. perChunkDelay stands in for the real
// network transfer time that made the original bug's hit rate
// timing-dependent; it is what makes this harness deterministic instead of
// network-timing-dependent.
func slowWrite(path string, payload []byte, chunkSize int, perChunkDelay time.Duration) error {
	if stat, _ := os.Stat(path); stat != nil {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove existing: %w", err)
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open for write: %w", err)
	}
	defer f.Close()

	for start := 0; start < len(payload); start += chunkSize {
		end := start + chunkSize
		if end > len(payload) {
			end = len(payload)
		}
		if _, err := f.Write(payload[start:end]); err != nil {
			return fmt.Errorf("write chunk: %w", err)
		}
		if end < len(payload) {
			time.Sleep(perChunkDelay)
		}
	}
	return nil
}

// observeTorn reads path and reports whether its content is a torn/partial
// read: neither the complete "before" file (the reader beat the writer's
// os.Remove) nor the complete "want" file (the write already finished) --
// i.e. any partial, truncated, or otherwise inconsistent content in between.
// A file that does not exist (the gap between os.Remove and os.OpenFile) is
// reported as "not yet created", not torn, via sizeSeen == -1: a competing
// reader would just see ENOENT there, not a corrupt file.
func observeTorn(path string, before, want []byte) (torn bool, sizeSeen int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, -1
	}
	if bytes.Equal(data, before) || bytes.Equal(data, want) {
		return false, len(data)
	}
	return true, len(data)
}

// TestONNXCacheHarness_WithoutLock_ReliablyTearsReads is the true-positive
// control required before sub-test B's "zero torn reads" can be trusted as
// evidence the lock works, rather than evidence the harness never looks.
// Without any coordination, a concurrent tight-poll reader reliably observes
// a torn/partial file while slowWrite is mid-copy at the same path.
func TestONNXCacheHarness_WithoutLock_ReliablyTearsReads(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "model.onnx")
	before := harnessPayload("before")

	tornCount := 0
	for i := 0; i < harnessIterations; i++ {
		// Reset to the SAME known-good baseline every iteration, so "torn"
		// means neither the old complete file nor the new complete file --
		// not merely "differs from the previous iteration's payload".
		if err := os.WriteFile(target, before, 0o644); err != nil {
			t.Fatalf("iteration %d: reset baseline: %v", i, err)
		}
		want := harnessPayload(fmt.Sprintf("iter%04d", i))

		var wg sync.WaitGroup
		stop := make(chan struct{})
		var sawTorn atomic.Bool

		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if torn, _ := observeTorn(target, before, want); torn {
					sawTorn.Store(true)
				}
				time.Sleep(harnessPollInterval)
			}
		}()

		if err := slowWrite(target, want, harnessChunkSize, harnessChunkDelay); err != nil {
			t.Fatalf("iteration %d: slowWrite: %v", i, err)
		}
		close(stop)
		wg.Wait()

		if sawTorn.Load() {
			tornCount++
		}
	}

	// "Substantial fraction", per the plan's validated 20/20 result -- a
	// little slack for scheduler jitter on unusually loaded hardware without
	// making the assertion toothless.
	minTorn := harnessIterations - 2
	if tornCount < minTorn {
		t.Fatalf("harness did not reliably reproduce torn reads: %d/%d iterations torn (want >= %d/%d) -- "+
			"the harness may not be a valid true-positive detector, so sub-test B's zero-torn-reads result cannot be trusted",
			tornCount, harnessIterations, minTorn, harnessIterations)
	}
	t.Logf("torn reads observed in %d/%d iterations without a lock", tornCount, harnessIterations)
}

// TestONNXCacheHarness_WithLock_NeverTearsReads is the fix's primary evidence:
// with both sides of the race -- the slow writer and the concurrent reader --
// holding vaultlock.Acquire exactly the way NewONNX holds it around
// hugot.DownloadModel, the same tight-poll reader that reliably tears reads
// in the sibling test above never observes a torn file. A single torn read
// fails immediately: it would mean the lock design does not actually close
// the window.
func TestONNXCacheHarness_WithLock_NeverTearsReads(t *testing.T) {
	lockRoot := t.TempDir()
	target := filepath.Join(lockRoot, "model.onnx")
	before := harnessPayload("before")

	for i := 0; i < harnessIterations; i++ {
		if err := os.WriteFile(target, before, 0o644); err != nil {
			t.Fatalf("iteration %d: reset baseline: %v", i, err)
		}
		want := harnessPayload(fmt.Sprintf("iter%04d", i))

		var wg sync.WaitGroup
		stop := make(chan struct{})

		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				release, err := vaultlock.Acquire(lockRoot, target)
				if err != nil {
					t.Errorf("iteration %d: reader Acquire: %v", i, err)
					return
				}
				torn, size := observeTorn(target, before, want)
				release()
				if torn {
					t.Errorf("iteration %d: torn read observed WITH the lock held (size=%d) -- the lock does not close the window",
						i, size)
				}
				time.Sleep(harnessPollInterval)
			}
		}()

		release, err := vaultlock.Acquire(lockRoot, target)
		if err != nil {
			t.Fatalf("iteration %d: writer Acquire: %v", i, err)
		}
		writeErr := slowWrite(target, want, harnessChunkSize, harnessChunkDelay)
		release()
		if writeErr != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("iteration %d: slowWrite: %v", i, writeErr)
		}

		close(stop)
		wg.Wait()
	}
}
