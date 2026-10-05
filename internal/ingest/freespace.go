// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The disk and inode watchdog (explicit-resumable-index-rebuild-with-disk-watchdog,
// Scope 7). It is a free-space RESERVE — an absolute floor of free bytes and
// free inodes that a run must leave on the filesystem holding palace/.local/ —
// never a consumption budget. The Part-1 measurement lost 2.7 M inodes and
// 8 GiB of disk, and a per-run archive budget measured on one project does not
// size another, so the guard is on the thing that actually runs out.

// FreeSpace is the free space on a filesystem: free bytes always, and free
// inodes where the platform reports them (not on Windows).
type FreeSpace struct {
	Bytes     uint64
	Inodes    uint64
	HasInodes bool
}

// FreeSpaceReader reports the free space on the filesystem holding a path.
// freespace_unix.go and freespace_windows.go provide the platform reader
// (newPlatformFreeSpace); a test injects its own.
type FreeSpaceReader interface {
	Free(path string) (FreeSpace, error)
}

// Reserve is the absolute free space a run must leave on the filesystem that
// holds palace/.local/: a floor, not a budget.
type Reserve struct {
	Bytes  uint64
	Inodes uint64
}

// 🔶 PLACEHOLDER watchdog constants. The real reserve floors and the cost per
// MiB of archive are MEASURED on the acceptance run (Scope 9, Acceptance) and
// recorded in the task by the Chair; these stand in until then. They are
// deliberately rough — do NOT read them as tuned numbers.
const (
	// DefaultReserveBytes is the free-byte floor (PLACEHOLDER: 2 GiB).
	DefaultReserveBytes uint64 = 2 << 30
	// DefaultReserveInodes is the free-inode floor (PLACEHOLDER).
	DefaultReserveInodes uint64 = 500_000
	// CostBytesPerMiB and CostInodesPerMiB estimate a run's consumption from
	// the archive MiB it will read (PLACEHOLDER).
	CostBytesPerMiB  uint64 = 12 << 20 // ~12 MiB of index per MiB of archive
	CostInodesPerMiB uint64 = 4096
)

// orDefault fills a zero reserve with the placeholder floors.
func (r Reserve) orDefault() Reserve {
	if r.Bytes == 0 {
		r.Bytes = DefaultReserveBytes
	}
	if r.Inodes == 0 {
		r.Inodes = DefaultReserveInodes
	}
	return r
}

// ErrReserve is the watchdog's refusal: free space is at or below the reserve,
// or an estimate would cross it. It is distinct so the CLI and a dry run can
// name it, and so a watchdog stop is told apart from a real failure.
var ErrReserve = errors.New("ingest: free-space reserve would be crossed")

// Estimate is a run's predicted consumption, in bytes and inodes.
type Estimate struct {
	Bytes  uint64
	Inodes uint64
}

// EstimateFor computes the estimate from the archive MiB a run will read,
// using the (placeholder) per-MiB cost.
func EstimateFor(archiveMiB uint64) Estimate {
	return Estimate{Bytes: archiveMiB * CostBytesPerMiB, Inodes: archiveMiB * CostInodesPerMiB}
}

// watchdog guards a run against filling the filesystem that holds
// palace/.local/. Its during-run check is the IngestArchive Checkpoint; its
// start/dry-run check refuses before the run begins.
type watchdog struct {
	reader  FreeSpaceReader
	path    string
	reserve Reserve
}

// newWatchdog builds a watchdog over reader (nil means the platform reader),
// guarding the filesystem that holds path, with reserve (zero means the
// placeholder floors).
func newWatchdog(reader FreeSpaceReader, path string, reserve Reserve) *watchdog {
	if reader == nil {
		reader = newPlatformFreeSpace()
	}
	return &watchdog{reader: reader, path: path, reserve: reserve.orDefault()}
}

// check is the during-run guard: free space must stay strictly above the
// reserve, in bytes and (where reported) inodes. It is the Checkpoint passed
// to every IngestArchive and to the routine ingester, so neither a rebuild nor
// a background drain can fill the disk.
// freeAt reads the free space on the filesystem holding w.path, resolving to
// the nearest existing ancestor first: palace/.local/ may not exist yet before
// the first write (and never does under --dry-run), and an ancestor is on the
// same filesystem, so its free space is the same.
func (w *watchdog) freeAt() (FreeSpace, error) {
	return w.reader.Free(existingAncestor(w.path))
}

// existingAncestor returns path, or its nearest ancestor that exists.
func existingAncestor(path string) string {
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}

func (w *watchdog) check() error {
	fs, err := w.freeAt()
	if err != nil {
		return err
	}
	if fs.Bytes < w.reserve.Bytes {
		return fmt.Errorf("%w: %d free bytes is below the %d-byte reserve", ErrReserve, fs.Bytes, w.reserve.Bytes)
	}
	if fs.HasInodes && fs.Inodes < w.reserve.Inodes {
		return fmt.Errorf("%w: %d free inodes is below the %d-inode reserve", ErrReserve, fs.Inodes, w.reserve.Inodes)
	}
	return nil
}

// checkpoint is check as an IngestOptions.Checkpoint (the chunk count is not
// read: the guard is on free space, not on the archive's size).
func (w *watchdog) checkpoint(int) error { return w.check() }

// ReserveCheckpoint returns an IngestOptions.Checkpoint that stops a run before
// it would cross the free-space reserve. The routine ingester (`vp drain
// archives`) passes it so a background run cannot fill a disk either (Scope 7).
// reader nil means the platform reader; a zero reserve means the placeholder
// floors.
func ReserveCheckpoint(reader FreeSpaceReader, path string, reserve Reserve) func(int) error {
	return newWatchdog(reader, path, reserve).checkpoint
}

// startCheck refuses a run (and a --dry-run) when the estimate exceeds free
// space minus the reserve, in either unit. The message names both numbers and
// the reserve.
func (w *watchdog) startCheck(est Estimate) error {
	fs, err := w.freeAt()
	if err != nil {
		return err
	}
	if fs.Bytes < w.reserve.Bytes || est.Bytes > fs.Bytes-w.reserve.Bytes {
		return fmt.Errorf("%w: estimate of %d bytes exceeds free %d minus the %d-byte reserve",
			ErrReserve, est.Bytes, fs.Bytes, w.reserve.Bytes)
	}
	if fs.HasInodes && (fs.Inodes < w.reserve.Inodes || est.Inodes > fs.Inodes-w.reserve.Inodes) {
		return fmt.Errorf("%w: estimate of %d inodes exceeds free %d minus the %d-inode reserve",
			ErrReserve, est.Inodes, fs.Inodes, w.reserve.Inodes)
	}
	return nil
}
