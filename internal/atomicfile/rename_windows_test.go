// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// TestRetryableRenameErr_Windows pins the classifier against the real OS error
// values, including through the *os.LinkError that os.Rename actually returns —
// that wrapper is why the classifier must use errors.Is and not a type
// assertion or a string match.
func TestRetryableRenameErr_Windows(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"access denied", syscall.Errno(5), true},
		{"sharing violation", syscall.Errno(32), true},
		{"access denied const", windows.ERROR_ACCESS_DENIED, true},
		{"sharing violation const", windows.ERROR_SHARING_VIOLATION, true},
		{"access denied via LinkError", &os.LinkError{
			Op: "rename", Old: "a", New: "b", Err: syscall.Errno(5),
		}, true},
		{"sharing violation via LinkError", &os.LinkError{
			Op: "rename", Old: "a", New: "b", Err: syscall.Errno(32),
		}, true},
		{"not exist", fs.ErrNotExist, false},
		{"file not found errno", syscall.Errno(2), false},
		{"not exist via LinkError", &os.LinkError{
			Op: "rename", Old: "a", New: "b", Err: syscall.Errno(2),
		}, false},
		{"random", errors.New("something went wrong"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryableRenameErr(tc.err); got != tc.want {
				t.Fatalf("retryableRenameErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
