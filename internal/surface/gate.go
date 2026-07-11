// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"errors"
	"fmt"
	"os"
)

// gateStderr is the destination for gate diagnostic output. Tests override it
// to capture stderr; production uses os.Stderr.
var gateStderr = os.Stderr

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
