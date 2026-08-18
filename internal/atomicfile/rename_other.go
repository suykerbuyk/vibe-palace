// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !windows

package atomicfile

// retryableRenameErr always reports false off Windows.
//
// POSIX rename(2) is defined to succeed even when another process holds the
// destination open: the directory entry is replaced and the old inode simply
// stays alive until the last descriptor closes. There is therefore no transient
// "someone else has it open" failure mode to wait out — every rename error here
// (ENOENT, EXDEV, EACCES on the directory, ENOSPC) is permanent, and retrying
// would only add latency to a write that is already doomed.
func retryableRenameErr(error) bool { return false }
