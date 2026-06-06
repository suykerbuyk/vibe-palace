// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"fmt"
	"os"
)

// gateStderr is the destination for gate diagnostic output. Tests override it
// to capture stderr; production uses os.Stderr.
var gateStderr = os.Stderr

// EnforceFailStop checks compatibility and returns the error directly, unless
// VP_SURFACE_GATE=warn is set (in which case it logs a single line to stderr
// and returns nil). Used by CLI write entry points and write-tool handlers.
//
// On vault-unreachable (CheckCompatible returns nil for empty/missing paths),
// this is a no-op.
func EnforceFailStop(vaultPath string) error {
	err := CheckCompatible(vaultPath)
	if err == nil {
		return nil
	}
	if os.Getenv("VP_SURFACE_GATE") == "warn" {
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
// VP_SURFACE_QUIET=1 suppresses the stderr line for non-interactive callers.
func EnforceWarnOnly(vaultPath string) {
	err := CheckCompatible(vaultPath)
	if err == nil {
		return
	}
	if os.Getenv("VP_SURFACE_QUIET") == "1" {
		return
	}
	fmt.Fprintln(gateStderr, err.Error())
}
