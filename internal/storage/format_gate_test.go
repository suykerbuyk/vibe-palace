// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// bornCurrentVault returns an ephemeral vault stamped at the current data format
// (RequiredDataFormat), mirroring how a freshly-created real vault is stamped at
// creation. Test vaults are NEW empty dirs, so stamping them born-current is the
// same "stamp on creation" semantics — it is what lets KG reads pass the armed
// format gate. Used by the shared storage test helpers.
func bornCurrentVault(t *testing.T, root string) *Vault {
	t.Helper()
	if err := surface.WriteFormat(root, surface.RequiredDataFormat); err != nil {
		t.Fatalf("stamp born-current vault: %v", err)
	}
	return NewVault(root)
}

// TestFormatGate_NormalReadsUnaffected proves normal KG reads succeed against a
// born-current vault (the state a freshly-created real vault and every test
// vault are in): with the gate armed at RequiredDataFormat, a vault stamped at
// creation clears it and reads are unaffected.
func TestFormatGate_NormalReadsUnaffected(t *testing.T) {
	v := testVault(t)

	tr := Triple{Subject: "Kai", Predicate: "knows", Object: "Go", ValidFrom: "2026-01-01"}
	if err := v.AddTriple("proj", tr); err != nil {
		t.Fatalf("AddTriple: %v", err)
	}

	got, err := v.ListTriples("proj")
	if err != nil {
		t.Fatalf("ListTriples should be unaffected at RequiredDataFormat=0, got %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListTriples returned %d triples, want 1", len(got))
	}

	q, err := v.QueryEntity("proj", "Kai", "", "out")
	if err != nil {
		t.Fatalf("QueryEntity should be unaffected at RequiredDataFormat=0, got %v", err)
	}
	if len(q) != 1 {
		t.Fatalf("QueryEntity returned %d triples, want 1", len(q))
	}

	stats, err := v.KGStats("proj")
	if err != nil {
		t.Fatalf("KGStats should be unaffected at RequiredDataFormat=0, got %v", err)
	}
	if stats.TripleCount != 1 {
		t.Fatalf("KGStats.TripleCount = %d, want 1", stats.TripleCount)
	}
}

// TestFormatGate_MigratorSeamOff confirms the migrator-exempt seam defaults off
// and can be flipped on. On a born-current vault the non-exempt gate passes; the
// exempt seam bypasses the gate entirely (the path the migration relies on to
// read format-0 data). Also pins that a fresh NewVault is not exempt by default.
func TestFormatGate_MigratorSeamOff(t *testing.T) {
	v := testVault(t)
	if v.migratorExempt {
		t.Fatal("fresh Vault should not be migrator-exempt by default")
	}
	if err := v.checkFormatGate(); err != nil {
		t.Fatalf("default (non-exempt) gate on a born-current vault should pass, got %v", err)
	}

	v.SetMigratorExempt(true)
	if !v.migratorExempt {
		t.Fatal("SetMigratorExempt(true) did not set the flag")
	}
	if err := v.checkFormatGate(); err != nil {
		t.Fatalf("migrator-exempt gate should pass, got %v", err)
	}
}
