// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import "testing"

// TestFormatGate_NormalReadsUnaffected proves the data-format read gate is inert
// today (RequiredDataFormat == 0, absent manifest == 0): normal KG reads against
// an ephemeral, unstamped vault succeed unchanged.
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
// (a fresh NewVault is gated by default) and can be flipped on. The gate itself
// is inert at RequiredDataFormat=0, so both postures currently read cleanly; the
// test locks in the seam's wiring for the downstream migration task.
func TestFormatGate_MigratorSeamOff(t *testing.T) {
	v := testVault(t)
	if v.migratorExempt {
		t.Fatal("fresh Vault should not be migrator-exempt by default")
	}
	if err := v.checkFormatGate(); err != nil {
		t.Fatalf("default (non-exempt) gate at required=0 should pass, got %v", err)
	}

	v.SetMigratorExempt(true)
	if !v.migratorExempt {
		t.Fatal("SetMigratorExempt(true) did not set the flag")
	}
	if err := v.checkFormatGate(); err != nil {
		t.Fatalf("migrator-exempt gate should pass, got %v", err)
	}
}
