// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import "github.com/suykerbuyk/vibe-palace/internal/vaultlock"

// GCLocks removes provably-unheld .vp-locks sidecar litter from this vault, under
// the quiescence-gated coordination of ADR-003's 2026-10-27 amendment (see
// vaultlock.SweepDeadLocks). It is the shared layer BOTH the CLI (`vp vault gc`)
// and any future MCP surface call, so the enforcement is never MCP-only.
//
// It removes NO vault content and runs NO git: it only unlinks empty 0-byte lock
// sidecars whose flock no process holds, and aborts (removing nothing) when the
// vault is actively being written. dryRun reports what would be removed without
// removing anything.
func (v *Vault) GCLocks(dryRun bool) (vaultlock.SweepReport, error) {
	return vaultlock.SweepDeadLocks(v.Root, dryRun)
}
