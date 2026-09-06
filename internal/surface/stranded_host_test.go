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

// TestRemediationIsErrorsOnlyProseSource pins the RELATIONSHIP between the two,
// which is the thing that can rot once the prose has a second exit.
//
// Extracting Remediation() collapsed two drifted copies into one. That buys
// nothing if Error() later grows its own inline tail again: the rendered message
// and the list consumers would diverge exactly as version.go and check/surface.go
// did, and every existing assertion — which checks only that SOME copy contains
// the substrings — would stay green through it.
//
// So this asserts CONTAINMENT, not substrings: every remediation line must appear
// in the rendered message, and the rendered tail must be nothing but those lines
// under a uniform margin. TestStrandedHostCanReadItsWayOut pins WHAT the text
// says; this pins that there is only one place it is said.
func TestRemediationIsErrorsOnlyProseSource(t *testing.T) {
	e := &IncompatibleError{BinarySurface: 2, VaultSurface: 3, StampDir: "/v/Projects/p"}

	rem := e.Remediation()
	if len(rem) == 0 {
		t.Fatal("Remediation() is empty — every consumer sourcing from it now emits nothing")
	}
	for _, line := range rem {
		if strings.TrimSpace(line) == "" {
			t.Errorf("blank remediation line: %q", line)
		}
		if strings.HasPrefix(line, "    ") {
			t.Errorf("remediation line %q carries Error()'s four-space margin — the lines are "+
				"shared with LIST consumers (check.Result.Details, the bootstrap alert) and must "+
				"carry only their RELATIVE indentation", line)
		}
	}

	msg := e.Error()
	for _, line := range rem {
		if !strings.Contains(msg, line) {
			t.Errorf("Error() does not contain remediation line %q — the rendered message has its "+
				"own copy of the prose again.\nFull message:\n%s", line, msg)
		}
	}

	// The tail after the header lines must be EXACTLY the remediation, margined.
	var wantTail strings.Builder
	for _, line := range rem {
		wantTail.WriteString("\n    ")
		wantTail.WriteString(line)
	}
	if !strings.HasSuffix(msg, wantTail.String()) {
		t.Errorf("Error()'s tail is not exactly Remediation() under a four-space margin — "+
			"something is being rendered from a second source.\ngot:\n%s\nwant tail:\n%s",
			msg, wantTail.String())
	}
}

// 🔴 TestErrorRendersExactBytes is the pin the rest of this file only LOOKED like
// it had.
//
// Every other assertion here is a substring check, and version.go's own comment
// claimed TestStrandedHostCanReadItsWayOut pinned the rendered bytes. It does not —
// it asserts three substrings. Under substrings alone, changing the last-writer
// line's four-space margin to eight is green everywhere, and so is any reflow of
// the block that keeps the words. That matters because the rendered form IS the
// contract: it is what a stranded operator reads on stderr, and Remediation() was
// extracted from it on the promise that the bytes did not move.
//
// So this asserts the whole string, for both LastWriter states. A deliberate
// rewording updates the literals here in the same change; an accidental one stops.
func TestErrorRendersExactBytes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *IncompatibleError
		want string
	}{
		{
			name: "empty LastWriter renders unknown",
			err:  &IncompatibleError{BinarySurface: 2, VaultSurface: 3, StampDir: "/v/Projects/dotfiles"},
			want: "vp: this binary supports MCP surface v2; vault target '/v/Projects/dotfiles' is at v3\n" +
				"    last writer: unknown (best-effort, not enforced)\n" +
				"    action:    cd ~/code/vibe-palace && git pull && make install\n" +
				"    if you cannot upgrade right now (deploy host, network outage):\n" +
				"       VP_SURFACE_GATE=warn <original-command>   (proceed at risk)",
		},
		{
			name: "populated LastWriter renders verbatim",
			err:  &IncompatibleError{BinarySurface: 1, VaultSurface: 9, StampDir: "d", LastWriter: "abc1234"},
			want: "vp: this binary supports MCP surface v1; vault target 'd' is at v9\n" +
				"    last writer: abc1234 (best-effort, not enforced)\n" +
				"    action:    cd ~/code/vibe-palace && git pull && make install\n" +
				"    if you cannot upgrade right now (deploy host, network outage):\n" +
				"       VP_SURFACE_GATE=warn <original-command>   (proceed at risk)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("the rendered message changed.\ngot:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

// 🔴 TestRemediationContentIsPinned is the CONTENT pin, and without it the rest of
// the suite pins only a RELATIONSHIP.
//
// Every "every line is present" assertion in this repo — here, in wrapstate, in
// tools, in gate_test — derives its expectation by calling Remediation() itself. So
// they all agree with whatever Remediation() currently says, including after a line
// is deleted from it: drop "if you cannot upgrade right now (deploy host, network
// outage):" and the entire suite stays green while the override command below it
// loses the sentence that explains when to use it.
//
// This is the one assertion that states the text independently. It is deliberately
// exact rather than substring-based, for the same reason as the byte pin above.
func TestRemediationContentIsPinned(t *testing.T) {
	want := []string{
		"action:    cd ~/code/vibe-palace && git pull && make install",
		"if you cannot upgrade right now (deploy host, network outage):",
		"   VP_SURFACE_GATE=warn <original-command>   (proceed at risk)",
	}
	got := (&IncompatibleError{}).Remediation()

	if len(got) != len(want) {
		t.Fatalf("the remediation has %d line(s), want %d — a line was added or DELETED, and every "+
			"other assertion in this repo derives its expectation from this function, so nothing "+
			"else would have noticed.\ngot:  %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("remediation line %d changed.\ngot:  %q\nwant: %q\n\nThe two load-bearing halves "+
				"are the upgrade command and the at-risk override; the line between them is what says "+
				"WHEN the override applies.", i, got[i], want[i])
		}
	}
}
