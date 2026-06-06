// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// captureGateStderr swaps gateStderr for a buffer for the duration of fn.
func captureGateStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := gateStderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	gateStderr = w
	defer func() { gateStderr = old }()

	fn()
	w.Close()

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String()
}

// aheadVault creates a vault with a stamp one version ahead of this binary.
func aheadVault(t *testing.T) string {
	t.Helper()
	vault := t.TempDir()
	if err := WriteStamp(filepath.Join(vault, "Projects", "foo"), MCPSurfaceVersion+1, "newer"); err != nil {
		t.Fatal(err)
	}
	return vault
}

func TestEnforceFailStop_Compatible(t *testing.T) {
	if err := EnforceFailStop(t.TempDir()); err != nil {
		t.Fatalf("compatible vault should pass, got %v", err)
	}
}

func TestEnforceFailStop_IncompatibleReturnsError(t *testing.T) {
	t.Setenv("VP_SURFACE_GATE", "")
	vault := aheadVault(t)
	out := captureGateStderr(t, func() {
		if err := EnforceFailStop(vault); err == nil {
			t.Fatal("ahead vault should fail-stop, got nil")
		}
	})
	if out != "" {
		t.Fatalf("fail-stop should not print to stderr, got %q", out)
	}
}

func TestEnforceFailStop_WarnEnvBypasses(t *testing.T) {
	t.Setenv("VP_SURFACE_GATE", "warn")
	vault := aheadVault(t)
	var retErr error
	out := captureGateStderr(t, func() { retErr = EnforceFailStop(vault) })
	if retErr != nil {
		t.Fatalf("VP_SURFACE_GATE=warn should return nil, got %v", retErr)
	}
	if out == "" {
		t.Fatal("warn bypass should print remediation to stderr")
	}
}

func TestEnforceWarnOnly_AlwaysNilWithWarning(t *testing.T) {
	t.Setenv("VP_SURFACE_QUIET", "")
	vault := aheadVault(t)
	out := captureGateStderr(t, func() { EnforceWarnOnly(vault) })
	if out == "" {
		t.Fatal("warn-only should print remediation on mismatch")
	}
}

func TestEnforceWarnOnly_QuietSuppresses(t *testing.T) {
	t.Setenv("VP_SURFACE_QUIET", "1")
	vault := aheadVault(t)
	out := captureGateStderr(t, func() { EnforceWarnOnly(vault) })
	if out != "" {
		t.Fatalf("VP_SURFACE_QUIET=1 should suppress stderr, got %q", out)
	}
}

func TestEnforceWarnOnly_CompatibleSilent(t *testing.T) {
	out := captureGateStderr(t, func() { EnforceWarnOnly(t.TempDir()) })
	if out != "" {
		t.Fatalf("compatible vault should be silent, got %q", out)
	}
}
