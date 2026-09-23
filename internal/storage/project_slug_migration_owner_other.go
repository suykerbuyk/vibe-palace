// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !linux

package storage

// ONE-SHOT: deleted with project_slug_migration.go.

import "io/fs"

// slugFreeBytes: not measured off Linux. The caller treats "cannot measure"
// as "do not refuse", and says so, rather than guessing.
func slugFreeBytes(string) (uint64, bool) { return 0, false }

// slugOwnedByUs: there is no /proc to scan off Linux, so the guard refuses
// outright there (or takes --attest-no-agents). Nothing calls this with a real
// process entry; it exists so the package builds for the Windows host, which
// runs the cache phase and its own rollback.
func slugOwnedByUs(fs.FileInfo, int) bool { return false }

// slugDeviceOf: not measured off Linux; callers then treat two paths as
// possibly different filesystems, which is the conservative answer.
func slugDeviceOf(fs.FileInfo) uint64 { return 0 }
