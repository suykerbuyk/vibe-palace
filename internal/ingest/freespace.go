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

// Watchdog constants: the per-MiB cost of a rebuild and the free-space reserve
// floors. They feed the start/during-run estimate (archive MiB × Cost…) the
// watchdog checks against an absolute floor of free bytes and free inodes on the
// filesystem holding palace/.local/ — a RESERVE, never a consumption budget.
//
// Derived from the 2026-10-07 rusty-can completed-run measurement on binary
// 0eeca35 (task explicit-resumable-index-rebuild-with-disk-watchdog, section
// "Watchdog cost measurement 2026-10-07"): a completed rebuild wrote 3.68 MiB and
// 1,219 inodes per uncompressed archive MiB; the values below carry ~2× headroom.
//
// 🔴 Revisit (and re-measure) when: the embedder model or vector dim changes; the
// chunker or the decision/knowledge-extraction tier changes (that tier ~2×'d chunk
// density in 0eeca35); the embed-cache dedup behaviour changes (currently ~0.48
// inode/chunk); a materially denser or larger project is embedded (vibe-palace
// itself is UNMEASURED and likely denser than rusty-can); or the gate is deployed
// to constrained hardware where it actually fires.
//
// How to re-measure: on a disposable remote-stripped vault copy (targeted by an
// untracked .vibe-palace.toml vault_path — NOT --vault-root, which does not
// redirect), run one completed `vp index rebuild <project>`, then recompute
// CostBytesPerMiB = (index/<p> + embed-cache/<p> bytes, EXCLUDING the one-time
// models/ ONNX cache) / uncompressed-archive-MiB; CostInodesPerMiB likewise.
const (
	// DefaultReserveBytes is the free-byte floor a run must leave.
	DefaultReserveBytes uint64 = 4 << 30 // 4 GiB
	// DefaultReserveInodes is the free-inode floor a run must leave.
	DefaultReserveInodes uint64 = 500_000
	// CostBytesPerMiB and CostInodesPerMiB estimate a run's consumption from
	// the archive MiB it will read.
	CostBytesPerMiB  uint64 = 8 << 20 // ~8 MiB of index per MiB of archive
	CostInodesPerMiB uint64 = 2500
)

// orDefault fills a zero reserve with the default floors.
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
// using the measured per-MiB cost.
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
// default floors).
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
// reader nil means the platform reader; a zero reserve means the default
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
