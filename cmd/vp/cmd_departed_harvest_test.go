// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// The session-exit harvest from a stale checkout of a moved project refuses
// and re-creates nothing — with no Projects/harvp, and with a stray file there.
func TestHarvestRefusesADepartedProject(t *testing.T) {
	for _, stray := range []bool{false, true} {
		live, _ := harvestCLIFixture(t, "git_enabled = false")
		if err := os.RemoveAll(filepath.Join(live, "Projects", "harvp")); err != nil {
			t.Fatal(err)
		}
		b, err := (departure.Record{Slug: "harvp", Kind: departure.MovedToVault, To: "git@example.invalid:q/v.git", Date: "2026-09-27"}).Encode()
		if err != nil {
			t.Fatal(err)
		}
		putVaultFile(t, live, departure.RelPath("harvp"), string(b))
		if stray {
			putVaultFile(t, live, "Projects/harvp/stray.md", "stray\n")
		}
		_, stderr, code := runVaultCmdCapturingBoth(t, cmdMemoryHarvest())
		if code == 0 {
			t.Fatalf("stray %v: the harvest was not refused: %s", stray, stderr)
		}
		if _, err := os.Lstat(filepath.Join(live, "Projects", "harvp", "memory")); err == nil {
			t.Fatalf("stray %v: the harvest wrote Projects/harvp/memory", stray)
		}
	}
}
