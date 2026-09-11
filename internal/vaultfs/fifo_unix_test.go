// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package vaultfs

import "syscall"

func mkfifo(path string) error { return syscall.Mkfifo(path, 0o644) }
