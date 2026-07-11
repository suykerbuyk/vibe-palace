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
//   - Pass: the vault is compatible (binary >= vault max), including an empty
//     vault with no stamps yet.
//   - Fail: the vault was written by a newer binary (vault surface exceeds this
//     binary's), OR the configured vault root is unreachable. Both are blocking:
//     the /vpc-restart template halts on status=fail, which is exactly what
//     should happen when the vault a session is about to mutate is not there.
//     Details carry the remediation.
//   - Info: no vault is configured at all (surface.ErrNoVault) — a normal
//     pre-`vp init` state, reported but non-halting — or any other unexpected
//     error from the scan. vibe-palace's check.Status has no Warn, so the
//     vibe-vault Warn case maps to Info here.
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

	var ue *surface.VaultUnreachableError
	if errors.As(err, &ue) {
		r.Status = Fail
		r.Summary = fmt.Sprintf("vault root %s is unreachable", ue.Path)
		r.Details = []string{
			"The configured vault root cannot be read — moved, deleted, or unmounted.",
			"Check:    vault_path in ~/.config/vibe-palace/config.toml",
			"Then:     restart the MCP server (it resolves vault_path once, at startup)",
		}
		r.Err = err
		return r
	}

	if errors.Is(err, surface.ErrNoVault) {
		r.Status = Info
		r.Summary = "no vault configured"
		r.Err = err
		return r
	}

	r.Status = Info
	r.Summary = err.Error()
	r.Err = err
	return r
}
