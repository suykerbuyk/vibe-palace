// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package ingest

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// platformFreeSpace reads free bytes and free inodes via statfs(2). It covers
// every release target but Windows (linux, darwin): the goreleaser matrix is
// linux/darwin/windows, and darwin shares this file through the `unix` tag.
//
// darwin reports the same fields with the same meaning and the same width:
// statfs(2) documents f_files as "total file nodes in file system" and f_ffree
// as "free file nodes in fs", and golang.org/x/sys/unix declares both as
// uint64 on darwin/amd64 and darwin/arm64, as it does on every linux arch. So
// the capability derivation below is correct on both targets from one file.
type platformFreeSpace struct{}

func (platformFreeSpace) Free(path string) (FreeSpace, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return FreeSpace{}, fmt.Errorf("ingest: statfs %s: %w", path, err)
	}
	return FreeSpace{
		Bytes:  uint64(st.Bavail) * uint64(st.Bsize),
		Inodes: uint64(st.Ffree),
		// Derived from the reported TOTAL, never asserted: see
		// hasInodeAccounting for why a zero total is "no inode accounting"
		// rather than "no inodes left".
		HasInodes: hasInodeAccounting(st.Files),
	}, nil
}

func newPlatformFreeSpace() FreeSpaceReader { return platformFreeSpace{} }
