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
type platformFreeSpace struct{}

func (platformFreeSpace) Free(path string) (FreeSpace, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return FreeSpace{}, fmt.Errorf("ingest: statfs %s: %w", path, err)
	}
	return FreeSpace{
		Bytes:     uint64(st.Bavail) * uint64(st.Bsize),
		Inodes:    uint64(st.Ffree),
		HasInodes: true,
	}, nil
}

func newPlatformFreeSpace() FreeSpaceReader { return platformFreeSpace{} }
