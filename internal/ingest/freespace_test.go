// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"errors"
	"testing"
)

// statfsFree is a FreeSpaceReader that reports what the platform reader would
// derive from a statfs(2) result carrying these f_files (total) and f_ffree
// (free) values. It calls hasInodeAccounting -- the SAME function the platform
// reader calls -- rather than re-deriving the capability, so a revert of that
// function to `true` turns these tests red instead of leaving them green over
// a private copy of the rule.
type statfsFree struct {
	bytes uint64
	files uint64 // statfs(2) f_files: the TOTAL file nodes
	ffree uint64 // statfs(2) f_ffree: the FREE file nodes
}

func (r statfsFree) Free(string) (FreeSpace, error) {
	return FreeSpace{Bytes: r.bytes, Inodes: r.ffree, HasInodes: hasInodeAccounting(r.files)}, nil
}

// TestHasInodeAccountingTellsAbsentFromZero is the unit of the fix: the
// capability comes from the TOTAL, so "no inode accounting" (btrfs/ZFS, total
// 0) is told apart from "no inodes left" (a real total, nothing free).
func TestHasInodeAccountingTellsAbsentFromZero(t *testing.T) {
	if hasInodeAccounting(0) {
		t.Error("a filesystem reporting a zero inode total was read as doing inode accounting; " +
			"that is btrfs/ZFS dynamic allocation, not an exhausted filesystem")
	}
	for _, total := range []uint64{1, 1 << 20, 21_012_480, ^uint64(0)} {
		if !hasInodeAccounting(total) {
			t.Errorf("hasInodeAccounting(%d) = false, want true: a non-zero total is real accounting", total)
		}
	}
}

// inodeCase is one (f_files, f_ffree) shape and what the watchdog must do with
// it. Bytes are held far above the reserve in every case so only the inode
// decision is under test; TestByteReserveStillFiresWithoutInodeAccounting
// covers the other half.
type inodeCase struct {
	name       string
	files      uint64
	ffree      uint64
	wantRefuse bool
	why        string
}

var inodeCases = []inodeCase{
	{
		name: "no inode accounting (btrfs, ZFS)", files: 0, ffree: 0, wantRefuse: false,
		why: "a zero TOTAL is a filesystem declining to count inodes; only the byte reserve applies",
	},
	{
		name: "genuinely exhausted", files: 1 << 20, ffree: 0, wantRefuse: true,
		why: "a real total with nothing free is a full filesystem and must still refuse",
	},
	{
		name: "free below the reserve", files: 1 << 20, ffree: 400_000, wantRefuse: true,
		why: "400000 free is under the 500000-inode reserve",
	},
	{
		name: "free above the reserve", files: 1 << 20, ffree: 1_000_000, wantRefuse: false,
		why: "1000000 free leaves the reserve intact after the estimate",
	},
}

// TestWatchdogInodeCheckFollowsInodeAccounting drives all four shapes through
// BOTH guards -- startCheck (the start/dry-run refusal) and check (the
// during-run checkpoint) -- because both refuse on zero free inodes and the
// defect blocked `vp index rebuild` and `vp drain archives` alike.
func TestWatchdogInodeCheckFollowsInodeAccounting(t *testing.T) {
	const plentyBytes = 100 << 30
	reserve := Reserve{Bytes: 4 << 30, Inodes: 500_000}
	est := Estimate{Bytes: 1 << 20, Inodes: 100_000}

	for _, tc := range inodeCases {
		t.Run(tc.name, func(t *testing.T) {
			r := statfsFree{bytes: plentyBytes, files: tc.files, ffree: tc.ffree}
			w := newWatchdog(r, t.TempDir(), reserve)

			for _, guard := range []struct {
				name string
				run  func() error
			}{
				{"startCheck", func() error { return w.startCheck(est) }},
				{"check", w.check},
			} {
				err := guard.run()
				switch {
				case tc.wantRefuse && err == nil:
					t.Errorf("%s: no refusal for files=%d ffree=%d; want ErrReserve (%s)",
						guard.name, tc.files, tc.ffree, tc.why)
				case tc.wantRefuse && !errors.Is(err, ErrReserve):
					t.Errorf("%s: err = %v, want ErrReserve", guard.name, err)
				case !tc.wantRefuse && err != nil:
					t.Errorf("%s: refused files=%d ffree=%d with %v; want pass (%s)",
						guard.name, tc.files, tc.ffree, err, tc.why)
				}
			}
		})
	}
}

// TestByteReserveStillFiresWithoutInodeAccounting: skipping the inode check on
// a filesystem that reports no inode total must not be readable as "the
// watchdog is off on btrfs". The byte reserve is the guard that still has a
// measurement to make, and it must still fire -- in both guards.
func TestByteReserveStillFiresWithoutInodeAccounting(t *testing.T) {
	reserve := Reserve{Bytes: 4 << 30, Inodes: 500_000}
	// files=0 is the btrfs shape, so the inode check is skipped; bytes are
	// under the reserve, so the byte check must refuse on its own.
	r := statfsFree{bytes: 1 << 20, files: 0, ffree: 0}
	w := newWatchdog(r, t.TempDir(), reserve)

	if err := w.startCheck(Estimate{Bytes: 1 << 20, Inodes: 100_000}); !errors.Is(err, ErrReserve) {
		t.Fatalf("startCheck err = %v, want ErrReserve: the byte reserve must still guard a filesystem with no inode accounting", err)
	}
	if err := w.check(); !errors.Is(err, ErrReserve) {
		t.Fatalf("check err = %v, want ErrReserve: the byte reserve must still guard a filesystem with no inode accounting", err)
	}

	// And with bytes restored, the same no-inode-accounting filesystem passes
	// both guards -- the case that was permanently impossible before the fix.
	ok := statfsFree{bytes: 100 << 30, files: 0, ffree: 0}
	w = newWatchdog(ok, t.TempDir(), reserve)
	if err := w.startCheck(Estimate{Bytes: 1 << 20, Inodes: 6_535_000}); err != nil {
		t.Fatalf("startCheck on a filesystem with no inode accounting and ample bytes: %v", err)
	}
	if err := w.check(); err != nil {
		t.Fatalf("check on a filesystem with no inode accounting and ample bytes: %v", err)
	}
}
