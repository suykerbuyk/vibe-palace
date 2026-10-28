// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// gateStderr is the destination for gate diagnostic output. Tests override it
// to capture stderr; production uses os.Stderr. It is an io.Writer (not an
// *os.File) so SetGateWriter can point it at a *bytes.Buffer across the package
// boundary.
var gateStderr io.Writer = os.Stderr

// SetGateWriter swaps the sink for gate diagnostics (gateStderr) and returns a
// func that restores the PREVIOUS writer. It is a TEST SEAM: production never
// calls it, so the default sink (os.Stderr) is unchanged and gate behaviour is
// unaffected.
//
// It swaps ONLY gateStderr — the sink shared by EnforceWarnOnly, EnforceFailStop
// and the format gate (format.go). It deliberately does NOT cover the other
// os.Stderr writes in this package (e.g. version.go's unrecognized-path
// warning); do not "fix" it to.
//
// Not safe for concurrent use: it mutates a package global. A test that uses it
// must NOT t.Parallel with another that writes the gate, and must restore via
// the returned closure (t.Cleanup). A nil writer is rejected here — left to
// reach Fprintln it would panic later, at a confusing place.
func SetGateWriter(w io.Writer) func() {
	if w == nil {
		panic("surface.SetGateWriter: nil writer")
	}
	old := gateStderr
	gateStderr = w
	return func() { gateStderr = old }
}

// EnforceFailStop checks compatibility and returns the error directly. Used by
// CLI write entry points and write-tool handlers.
//
// VP_SURFACE_GATE=warn bypasses an *IncompatibleError only — a version mismatch
// is a coherent "proceed at risk" (the binary is old, but the vault is there and
// the write may well be fine). It deliberately does NOT bypass ErrNoVault or
// *VaultUnreachableError: you cannot accept the risk of writing to a vault that
// does not exist, and letting an operator override their way past it only moves
// the failure somewhere later and less legible.
func EnforceFailStop(vaultPath string) error {
	err := CheckCompatible(vaultPath)
	if err == nil {
		return nil
	}
	var ie *IncompatibleError
	if errors.As(err, &ie) && os.Getenv("VP_SURFACE_GATE") == "warn" {
		fmt.Fprintln(gateStderr, err.Error())
		return nil
	}
	return err
}

// EnforceWarnOnly checks compatibility and emits a single stderr warning on
// mismatch (regardless of VP_SURFACE_GATE). Always returns nil. Used by
// `vp hook` and read-only CLI commands so an out-of-date binary can never block
// a capture or a read.
//
// ErrNoVault is silent here: a read on a host with no vault configured is a
// normal pre-`vp init` state, not something to nag about. A configured-but-
// unreachable vault still warns — that one is always a misconfiguration.
//
// VP_SURFACE_QUIET=1 suppresses the stderr line for non-interactive callers.
func EnforceWarnOnly(vaultPath string) {
	err := CheckCompatible(vaultPath)
	if err == nil || errors.Is(err, ErrNoVault) {
		return
	}
	if os.Getenv("VP_SURFACE_QUIET") == "1" {
		return
	}
	fmt.Fprintln(gateStderr, err.Error())
}
