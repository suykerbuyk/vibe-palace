// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package check

import (
	"os"
)

// fileUID returns a FileInfo's owning uid, or a sentinel that can never equal a
// real euid when the platform does not expose one.
func fileUID(fi os.FileInfo) uint32 {
	// Windows exposes no uid: inspect the process rather than skip
	// every one of them, which would silently disable liveness entirely.
	return uint32(os.Geteuid())
}
