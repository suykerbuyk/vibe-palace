// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package ingest

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestPlatformFreeSpaceDerivesHasInodesFromTheRealStatfs is the FIRST test to
// exercise platformFreeSpace.Free at all -- before this, the production reader
// had no coverage of any kind and every watchdog test injected an
// already-derived FreeSpace, which cannot show that the reader sets HasInodes
// correctly.
//
// 🔴 READ THIS BEFORE TRUSTING IT AS THE PROOF. On a filesystem that reports a
// non-zero inode total (ext4, xfs, APFS) the fixed reader and the old
// `HasInodes: true` reader return the SAME answer, so this test is green
// either way there. It is a real proof on exactly the filesystems that carry
// the defect -- btrfs, ZFS, anything reporting a zero total -- where the old
// reader says true and the expectation below says false. Run
// `go test ./internal/ingest` on such a host to see it discriminate; the
// four-case watchdog table in freespace_test.go is what proves the rule
// everywhere else.
func TestPlatformFreeSpaceDerivesHasInodesFromTheRealStatfs(t *testing.T) {
	dir := t.TempDir()

	fs, err := (platformFreeSpace{}).Free(dir)
	if err != nil {
		t.Fatalf("Free(%s): %v", dir, err)
	}

	// Independent of the reader: ask statfs(2) ourselves and state the
	// expectation as the literal rule, not by calling hasInodeAccounting --
	// calling it would move both sides of the comparison together and the
	// test could never fail.
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		t.Fatalf("statfs(%s): %v", dir, err)
	}
	if want := st.Files > 0; fs.HasInodes != want {
		t.Errorf("HasInodes = %v for a filesystem reporting f_files=%d, f_ffree=%d; want %v",
			fs.HasInodes, st.Files, st.Ffree, want)
	}

	// Invariants that hold on every unix host, so these do discriminate here:
	// a temp dir's filesystem has room, and a reader that reports no inode
	// accounting must be doing so because the total really is zero.
	if fs.Bytes == 0 {
		t.Error("Free reported 0 free bytes on the filesystem holding t.TempDir()")
	}
	if !fs.HasInodes && st.Files != 0 {
		t.Errorf("HasInodes is false but the filesystem reports f_files=%d: the capability was not derived from the total", st.Files)
	}
	t.Logf("this host: f_files=%d f_ffree=%d -> HasInodes=%v, Inodes=%d, Bytes=%d",
		st.Files, st.Ffree, fs.HasInodes, fs.Inodes, fs.Bytes)
}
