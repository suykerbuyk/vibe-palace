// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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
	assertCarriesRemediation(t, out, "the VP_SURFACE_GATE=warn bypass")
}

func TestEnforceWarnOnly_AlwaysNilWithWarning(t *testing.T) {
	t.Setenv("VP_SURFACE_QUIET", "")
	vault := aheadVault(t)
	out := captureGateStderr(t, func() { EnforceWarnOnly(vault) })
	assertCarriesRemediation(t, out, "the warn-only gate")
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

// assertCarriesRemediation is the real assertion these two tests used to make
// only in their names.
//
// 🔴 THEY BOTH CHECKED `out != ""`. These are the ONE path that passes the
// producer's Error() through verbatim to a human — the gate's own stderr — and
// a consumer there could have printed "nope" and stayed green. That is the same
// weak-assertion class as the consumers that re-rendered the error from its
// struct fields: coverage that is real, and pointed at nothing.
//
// It asserts every remediation line rather than two substrings, so a gate that
// carried half the prose fails here rather than passing on the half it kept.
func assertCarriesRemediation(t *testing.T, out, who string) {
	t.Helper()
	if out == "" {
		t.Fatalf("%s printed NOTHING to stderr — a stranded host's only route back is text", who)
	}
	for _, want := range []struct{ text, why string }{
		{"git pull && make install", "the upgrade command — the ONLY way out of the failure"},
		{"VP_SURFACE_GATE=warn", "the at-risk override, for a host that cannot upgrade now"},
		{"MCP surface", "what kind of mismatch this is"},
	} {
		if !strings.Contains(out, want.text) {
			t.Errorf("%s printed to stderr WITHOUT %q (%s).\nstderr:\n%s", who, want.text, want.why, out)
		}
	}
	// Every line, not just the two probes above: the gate prints Error() whole,
	// so anything Remediation() says must arrive.
	ie := &IncompatibleError{}
	for _, line := range ie.Remediation() {
		if !strings.Contains(out, line) {
			t.Errorf("%s dropped remediation line %q.\nstderr:\n%s", who, line, out)
		}
	}
}
