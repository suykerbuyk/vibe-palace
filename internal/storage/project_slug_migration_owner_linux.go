// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build linux

package storage

// ONE-SHOT: deleted with project_slug_migration.go.

import (
	"io/fs"
	"syscall"
)

// slugFreeBytes reports the free space a non-root user can still use on the
// filesystem holding path. ok is false where it cannot be measured.
func slugFreeBytes(path string) (free uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}

// slugOwnedByUs reports whether a /proc/<pid> entry belongs to this user. The
// guard refuses OUR OWN process when its exe cannot be resolved; another
// user's (root's) is judged on comm and argv, which are always readable.
func slugOwnedByUs(st fs.FileInfo, uid int) bool {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	return int(sys.Uid) == uid
}

// slugDeviceOf is the device a file lives on, for "same filesystem" tests.
func slugDeviceOf(fi fs.FileInfo) uint64 {
	if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(sys.Dev)
	}
	return 0
}
