// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build linux

package storage

// ONE-SHOT: deleted with project_slug_migration.go.

import (
	"io/fs"
	"syscall"
)

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
