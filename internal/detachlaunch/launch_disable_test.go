// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package detachlaunch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestLaunchDisabledByTestEnvStartsNothing proves the harness-only opt-out.
// It names an EXPLICIT, non-".test" binary on purpose: the fork-bomb guard
// never fires for it, so without the opt-out Launch would really start the
// child and create the log file. That is exactly the leak this gate closes (a
// built `vp` launched by a hook outliving its test's TempDir).
func TestLaunchDisabledByTestEnvStartsNothing(t *testing.T) {
	t.Setenv(DisableEnv, "1")
	logPath := filepath.Join(t.TempDir(), "child.log")

	pid, err := Launch(testHelperBinary(t), helperArgs(), logPath)
	if !errors.Is(err, ErrDetachDisabled) {
		t.Fatalf("err = %v, want ErrDetachDisabled", err)
	}
	if pid != 0 {
		t.Fatalf("pid = %d, want 0 (no process started)", pid)
	}
	if _, statErr := os.Stat(logPath); !os.IsNotExist(statErr) {
		t.Fatalf("log file must not be created when disabled; os.Stat err = %v", statErr)
	}
}

// TestLaunchDisabledIsCheckedBeforeBinaryResolution: the gate wins even for a
// binary that cannot resolve, so it is the first thing Launch does.
func TestLaunchDisabledIsCheckedBeforeBinaryResolution(t *testing.T) {
	t.Setenv(DisableEnv, "1")
	_, err := Launch(filepath.Join(t.TempDir(), "no-such-binary"), nil, filepath.Join(t.TempDir(), "x.log"))
	if !errors.Is(err, ErrDetachDisabled) {
		t.Fatalf("err = %v, want ErrDetachDisabled", err)
	}
}

// TestLaunchNotDisabledWhenEnvUnsetOrOtherValue pins the opt-in contract: only
// the exact value "1" disables, so an unrelated value never silently turns
// production launches off.
func TestLaunchNotDisabledWhenEnvUnsetOrOtherValue(t *testing.T) {
	withHelperEnv(t)
	t.Setenv(DisableEnv, "0")
	logPath := filepath.Join(t.TempDir(), "child.log")
	pid, err := Launch(testHelperBinary(t), helperArgs(), logPath)
	if errors.Is(err, ErrDetachDisabled) {
		t.Fatalf("value %q must not disable launching", "0")
	}
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("pid = %d, want > 0", pid)
	}
}
