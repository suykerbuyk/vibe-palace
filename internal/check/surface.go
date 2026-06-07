// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"errors"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// CheckSurface verifies the binary's MCPSurfaceVersion is at or above the
// maximum .surface stamp recorded in the vault at vaultRoot. It mirrors the
// runtime gate (surface.CheckCompatible) so `vp check` reports the same
// verdict an MCP write would face.
//
//   - Pass: the vault is compatible (binary >= vault max), an empty vault
//     (no stamps), or vaultRoot is "" / unreachable — surface.CheckCompatible
//     treats those as best-effort and returns nil.
//   - Fail: the vault was written by a newer binary (vault surface exceeds
//     this binary's). Details carry the upgrade / override remediation.
//   - Info: any unexpected error from the compatibility scan. vibe-palace's
//     check.Status has no Warn, so the vibe-vault Warn case maps to Info here.
func CheckSurface(vaultRoot string) Result {
	r := Result{Name: "Surface"}

	err := surface.CheckCompatible(vaultRoot)
	if err == nil {
		r.Status = Pass
		r.Summary = fmt.Sprintf("binary v%d >= vault max", surface.MCPSurfaceVersion)
		return r
	}

	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		r.Status = Fail
		r.Summary = fmt.Sprintf("binary v%d < vault v%d at %s",
			ie.BinarySurface, ie.VaultSurface, ie.StampDir)
		r.Details = []string{
			"Upgrade:  cd ~/code/vibe-palace && git pull && make install",
			"Override (at risk):  VP_SURFACE_GATE=warn <command>",
		}
		r.Err = err
		return r
	}

	r.Status = Info
	r.Summary = err.Error()
	r.Err = err
	return r
}
