// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package storage

import "os"

// flockFile is a no-op stub on Windows.
func flockFile(f *os.File) error { return nil }

// funlockFile is a no-op stub on Windows.
func funlockFile(f *os.File) error { return nil }
