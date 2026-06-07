// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package vaultlock

import "os"

// flockExclusive is a no-op stub on Windows.
func flockExclusive(f *os.File) error { return nil }

// funlock is a no-op stub on Windows.
func funlock(f *os.File) error { return nil }
