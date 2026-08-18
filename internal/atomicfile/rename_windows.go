// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package atomicfile

import (
	"errors"

	"golang.org/x/sys/windows"
)

// retryableRenameErr reports whether a failed rename is worth retrying on
// Windows.
//
// os.Rename compiles to MoveFileEx(..., MOVEFILE_REPLACE_EXISTING), which fails
// when any other process holds an open handle to the destination that was not
// opened with FILE_SHARE_DELETE. On a CI runner or a developer desktop that is
// routinely a virus scanner or the search indexer opening the file for a few
// milliseconds — the handle goes away on its own, so the same rename succeeds a
// moment later. Windows reports that as ERROR_ACCESS_DENIED (5) or
// ERROR_SHARING_VIOLATION (32).
//
// Every other failure (missing source, bad path, cross-volume move, a real
// permission problem on the directory) is permanent and must surface at once.
//
// errors.Is walks the *os.LinkError os.Rename returns down to the syscall.Errno,
// so this is a value comparison, never a string match on the message text.
func retryableRenameErr(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
