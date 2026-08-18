// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !windows

package atomicfile

import (
	"errors"
	"io/fs"
	"syscall"
	"testing"
)

// TestRetryableRenameErr_AlwaysFalseOffWindows guards the non-Windows stub: no
// error, transient-looking or otherwise, may make the loop spin off Windows.
// syscall.Errno(5) is EIO here and syscall.Errno(32) is EPIPE — the same
// numbers that ARE retryable on Windows — so this also pins that the stub is a
// real per-GOOS split and not an accidental shared classifier.
func TestRetryableRenameErr_AlwaysFalseOffWindows(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"random", errors.New("something went wrong")},
		{"nil", nil},
		{"not exist", fs.ErrNotExist},
		{"errno 5", syscall.Errno(5)},
		{"errno 32", syscall.Errno(32)},
		{"eacces", syscall.EACCES},
		{"wrapped", errors.Join(errors.New("outer"), syscall.EACCES)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if retryableRenameErr(tc.err) {
				t.Fatalf("retryableRenameErr(%v) = true, want false off Windows", tc.err)
			}
		})
	}
}
