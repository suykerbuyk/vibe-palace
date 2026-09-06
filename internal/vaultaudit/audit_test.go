// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestDimensionNames_IsExactlyWhatRunEmits is the PIN under the derivation.
//
// Two surfaces now BUILD their human-facing description from DimensionNames instead of
// restating it (the MCP tool and the CLI command). That is only safe while the derived
// list is exactly the set of rows Run produces — a list that overstates would have those
// descriptions promising a dimension the audit never reports, which is the same class of
// lie the prose it replaced was telling, just harder to see.
//
// The assertion is ORDERED EQUALITY, not set equality, and it fails in both directions:
// a dimension added to the registry but not emitted, and a row emitted that the registry
// does not name. Order matters because the report's fixed row order is what makes
// `git log -p Audits/` a drift signal, and DimensionNames is documented to preserve it.
//
// It runs against an EMPTY vault deliberately. Several dimensions can find nothing or
// fail outright there — and that is the interesting case, because Run turns a dimension
// that cannot run into a StatusUnknown ROW rather than dropping it. A future refactor
// that "helpfully" skipped a failing dimension would shrink the audit's own scope
// silently, and this test is what catches it.
func TestDimensionNames_IsExactlyWhatRunEmits(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	report, err := Run(vault)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	emitted := make([]string, len(report.Dimensions))
	for i, d := range report.Dimensions {
		emitted[i] = d.Name
	}

	declared := DimensionNames()
	if len(declared) != len(emitted) {
		t.Fatalf("DimensionNames has %d entries but Run emitted %d rows\n  declared: %v\n  emitted:  %v",
			len(declared), len(emitted), declared, emitted)
	}
	for i := range declared {
		if declared[i] != emitted[i] {
			t.Errorf("position %d: DimensionNames says %q, Run emitted %q\n  declared: %v\n  emitted:  %v",
				i, declared[i], emitted[i], declared, emitted)
		}
	}
}

// TestDimensionNames_ReturnsACopy: the registry's order is load-bearing (see above), so
// a caller that sorted the returned slice to render it alphabetically must not reorder
// the report as a side effect. Callers that join it into a description are exactly the
// kind that reach for sort.Strings.
func TestDimensionNames_ReturnsACopy(t *testing.T) {
	first := DimensionNames()
	if len(first) == 0 {
		t.Fatal("DimensionNames returned nothing — the registry is empty")
	}

	original := first[0]
	first[0] = "clobbered"

	second := DimensionNames()
	if second[0] != original {
		t.Errorf("mutating the returned slice changed the registry: got %q, want %q",
			second[0], original)
	}
}
