// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"path/filepath"
	"strings"
	"testing"
)

// 🔴 TestStrandedHostCanReadItsWayOut is the bump's safety net, and it exists
// because bumping MCPSurfaceVersion strands every host that has not run
// `make install` — vault-wide and at once, since CheckCompatible takes the MAX
// across every stamp.
//
// A stranded host's ONLY route back is text. If the mismatch message says what
// is wrong without saying what to do, the operator is left to guess, and the
// gate has converted a version skew into a support call.
//
// This asserts the PRODUCER, which was previously pinned for the override line
// and not for the upgrade command — so the upgrade half could have been deleted
// with the whole suite staying green.
func TestStrandedHostCanReadItsWayOut(t *testing.T) {
	root := t.TempDir()
	// A vault one version AHEAD of this binary: exactly what every un-upgraded
	// host sees the moment a newer one writes.
	if err := WriteStamp(filepath.Join(root, "Projects", "p"), MCPSurfaceVersion+1, "tester"); err != nil {
		t.Fatal(err)
	}

	err := CheckCompatible(root)
	if err == nil {
		t.Fatal("a vault ahead of this binary did not produce an error")
	}
	msg := err.Error()

	for _, want := range []struct{ text, why string }{
		{"git pull && make install", "the upgrade command — the ONLY way out of the failure"},
		{"VP_SURFACE_GATE=warn", "the at-risk override, for a host that cannot upgrade now"},
		{"MCP surface", "what kind of mismatch this is"},
	} {
		if !strings.Contains(msg, want.text) {
			t.Errorf("the stranded-host message omits %q (%s).\nFull message:\n%s",
				want.text, want.why, msg)
		}
	}
}

// TestStrandedHostMessageNamesBothVersions — "you are behind" is not actionable
// without "behind WHAT". An operator triaging six writer identities needs to
// know which vault raised the floor and to what.
func TestStrandedHostMessageNamesBothVersions(t *testing.T) {
	root := t.TempDir()
	stampDir := filepath.Join(root, "Projects", "p")
	if err := WriteStamp(stampDir, MCPSurfaceVersion+1, "tester"); err != nil {
		t.Fatal(err)
	}

	err := CheckCompatible(root)
	if err == nil {
		t.Fatal("expected an incompatibility")
	}
	msg := err.Error()

	if !strings.Contains(msg, stampDir) {
		t.Errorf("message does not name the offending stamp dir %q:\n%s", stampDir, msg)
	}
	// Both numbers, so the operator can tell a one-version lag from a five.
	if !strings.Contains(msg, "v"+itoa(MCPSurfaceVersion)) ||
		!strings.Contains(msg, "v"+itoa(MCPSurfaceVersion+1)) {
		t.Errorf("message does not name BOTH this binary's version and the vault's:\n%s", msg)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestSurfaceVersionIsMonotonic guards the one mistake this constant can make
// that nothing else would catch: going DOWN. A decrement silently un-strands
// every host by lowering this binary's claim rather than by upgrading anything,
// and every vault already stamped higher would then be written by a binary that
// believes it is current.
func TestSurfaceVersionIsMonotonic(t *testing.T) {
	// The floor is the last version this project shipped and stamped into live
	// vaults. Raise it with the constant; never lower either.
	const shippedFloor = 3
	if MCPSurfaceVersion < shippedFloor {
		t.Fatalf("MCPSurfaceVersion = %d, below the shipped floor %d. Live vaults carry "+
			"stamps at the floor; a binary claiming less than it already wrote would "+
			"strand itself against its own history.", MCPSurfaceVersion, shippedFloor)
	}
}
