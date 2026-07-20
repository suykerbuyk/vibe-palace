// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import "github.com/suykerbuyk/vibe-palace/internal/surface"

// SetMigratorExempt marks this Vault as the KG data-migration driver, bypassing
// the vault data-format read gate (checkFormatGate) so it can READ format-0
// (unmigrated) triples in order to rewrite them. Only the migration command
// calls this; every other caller leaves the flag false and is gated normally.
//
// This is the wired-but-off MIGRATOR SEAM: the consumer task
// kg-triple-filename-sanitization flips it on for its migration Vault. It is
// inert today because RequiredDataFormat == 0 makes the gate a no-op regardless.
func (v *Vault) SetMigratorExempt(exempt bool) { v.migratorExempt = exempt }

// checkFormatGate enforces the vault data-format READ gate for KG storage reads
// (QueryEntity / KGStats / ListTriples). It is a PURE READ — no write side
// effect — and, because surface.RequiredDataFormat == 0 today, a no-op for every
// current vault. The migration driver (SetMigratorExempt) bypasses it, and
// VP_FORMAT_GATE=warn downgrades a mismatch to a logged warning.
//
// The hazard this guards is a READ hazard: a current binary reading a vault
// still on the old encoding silently undercounts triples. That is why the gate
// lives here at the storage-read layer and fires when the vault is BEHIND the
// binary — not at the write-only surface seam.
func (v *Vault) checkFormatGate() error {
	return surface.EnforceFormatFailStop(v.Root, v.migratorExempt)
}
