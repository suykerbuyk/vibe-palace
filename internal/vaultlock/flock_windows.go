// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package vaultlock

import "os"

// flockExclusive is a no-op stub on Windows: it locks NOTHING and reports
// success, so every "serialized" vault writer is in fact unprotected here. The
// parameter is unused deliberately — see the windows-lockfileex task for the
// LockFileEx implementation that will make this real.
func flockExclusive(_ *os.File) error { return nil }

// funlock is a no-op stub on Windows. See flockExclusive.
func funlock(_ *os.File) error { return nil }
