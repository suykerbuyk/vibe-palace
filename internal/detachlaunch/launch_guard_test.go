// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// These tests prove the fork-bomb guard: Launch refuses to detach a Go test
// binary, starting no process and creating no log file. They run under the
// detachlaunch.test binary, so the guard fires and they CANNOT spawn — that is
// exactly the property being proven (removing the guard would make Launch try
// to exec the test binary instead).
package detachlaunch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestLaunchRefusesSelfRelaunchUnderTestBinary exercises the binary=="" path.
// Under `go test` the resolved self-executable is detachlaunch.test, so the
// guard refuses before startDetached.
func TestLaunchRefusesSelfRelaunchUnderTestBinary(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "child.log")

	pid, err := Launch("", []string{"drain", "archives"}, logPath)
	if !errors.Is(err, ErrRefusedTestBinary) {
		t.Fatalf("err = %v, want ErrRefusedTestBinary", err)
	}
	if pid != 0 {
		t.Fatalf("pid = %d, want 0 (no process started)", pid)
	}
	if _, statErr := os.Stat(logPath); !os.IsNotExist(statErr) {
		t.Fatalf("log file must not be created on refusal; os.Stat err = %v", statErr)
	}
}

// TestLaunchRefusesExplicitTestBinary exercises the explicit-binary path: a
// path whose base ends in ".test" is refused by suffix, before any process or
// log file — the nonexistent path is never actually execed.
func TestLaunchRefusesExplicitTestBinary(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "child.log")

	pid, err := Launch("/nonexistent/foo.test", nil, logPath)
	if !errors.Is(err, ErrRefusedTestBinary) {
		t.Fatalf("err = %v, want ErrRefusedTestBinary", err)
	}
	if pid != 0 {
		t.Fatalf("pid = %d, want 0 (no process started)", pid)
	}
	if _, statErr := os.Stat(logPath); !os.IsNotExist(statErr) {
		t.Fatalf("log file must not be created on refusal; os.Stat err = %v", statErr)
	}
}
