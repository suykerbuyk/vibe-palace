// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package atomicfile

import (
	"fmt"
	"os"
	"time"
)

// Seam vars. renameWithRetry never calls os.Rename, time.Sleep or the
// per-GOOS classifier directly: it goes through these package-level vars so a
// unit test on ANY platform can substitute a fake rename, a fake classifier
// and a zero-cost backoff schedule. They are unexported — production code has
// no reason to swap them, and tests must restore them in t.Cleanup.
var (
	renameFn             = os.Rename
	isRetryableRenameErr = retryableRenameErr
	sleepFn              = time.Sleep

	// renameRetryBackoff is the wait BEFORE each retry, so the number of
	// attempts is len(renameRetryBackoff)+1 == 7 and the worst-case total
	// sleep is 785ms.
	//
	// Why these numbers: the failure being defended against is a third-party
	// process (Windows Defender, the search indexer, a backup agent) holding a
	// transient handle on the destination file without FILE_SHARE_DELETE,
	// which makes MoveFileEx(MOVEFILE_REPLACE_EXISTING) fail with
	// ERROR_ACCESS_DENIED or ERROR_SHARING_VIOLATION. Those handles are held
	// for milliseconds to a few hundred milliseconds, so the first two retries
	// clear the common case in under 40ms, and the tail gives the scanner
	// roughly three quarters of a second to let go. The bound is deliberately
	// under one second: a vault write is on a user-interactive path and an
	// unrecoverable rename must surface as an error, not as a multi-second
	// hang. Go's own cmd/go/internal/robustio uses the same shape with a
	// comparable ~2s ceiling.
	renameRetryBackoff = []time.Duration{
		10 * time.Millisecond,
		25 * time.Millisecond,
		50 * time.Millisecond,
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
	}
)

// renameWithRetry renames oldpath over newpath, retrying on errors the
// platform classifier marks as transient (see rename_windows.go /
// rename_other.go).
//
// A non-retryable error returns immediately from the first attempt, unwrapped,
// so callers keep the exact error value os.Rename produced. When every attempt
// is retryable and every attempt fails, the LAST error is returned wrapped with
// the attempt count; the wrap uses %w so errors.Is / errors.As still reach the
// underlying *os.LinkError and syscall.Errno.
func renameWithRetry(oldpath, newpath string) error {
	for attempt := 0; ; attempt++ {
		err := renameFn(oldpath, newpath)
		if err == nil {
			return nil
		}
		if !isRetryableRenameErr(err) {
			return err
		}
		if attempt >= len(renameRetryBackoff) {
			return fmt.Errorf("after %d attempts: %w", attempt+1, err)
		}
		sleepFn(renameRetryBackoff[attempt])
	}
}
