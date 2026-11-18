// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"errors"
	"strings"
	"testing"
)

// T1: the decision table of KGHonesty, over the exact signature every surface
// uses. Each row is a coverage state a real project can be in.
//
// Q1: pure function, but every surface test (internal/tools) drives it through
// the production handlers; this is the table, not the wiring. Q2: the rows
// differ in State/kgEmpty from each other. Q3: the authored row and the notes
// row hold the SAME ExcludedTiers, so a predicate keyed on ExcludedTiers
// instead of the state fails the notes row.
//
// Mutation (M1): return "" for authored; (M3): key on ExcludedTiers.
func TestKGHonestyDecisionTable(t *testing.T) {
	excluded := []string{"transcripts"}
	cov := func(st CoverageState) *Coverage {
		return &Coverage{Project: "p", State: st, Reason: "why-" + string(st), N: 1, M: 3, ExcludedTiers: excluded}
	}
	for _, tc := range []struct {
		name    string
		cov     *Coverage
		err     error
		empty   bool
		want    KGStatus
		wantTxt string
	}{
		{"authored, empty", cov(CoverageAuthored), nil, true, KGTierExcluded, "archive-extracted"},
		{"authored, populated", cov(CoverageAuthored), nil, false, KGTierExcluded, "3 archives not indexed"},
		// notes carries the SAME ExcludedTiers but the project has no archives.
		{"notes, empty is NOT excluded", cov(CoverageNotes), nil, true, KGEmpty, "no facts recorded"},
		{"partial, empty", cov(CoveragePartial), nil, true, KGEmpty, "1 of 3 sessions ingested"},
		{"current, empty", cov(CoverageCurrent), nil, true, KGEmpty, "no facts recorded"},
		{"unbuilt, empty", cov(CoverageUnbuilt), nil, true, KGUnbuilt, "why-unbuilt"},
		{"absent, empty", cov(CoverageAbsent), nil, true, KGAbsent, "why-absent"},
		{"stale, empty", cov(CoverageStale), nil, true, KGStale, "why-stale"},
		{"legacy, empty", cov(CoverageLegacy), nil, true, KGLegacy, "why-legacy"},
		{"coverage error, empty", nil, errors.New("boom"), true, KGCoverageUnavailable, "could not determine why: boom"},
		{"coverage error, populated", nil, errors.New("boom"), false, "", ""},
		{"stale, populated", cov(CoverageStale), nil, false, "", ""},
		{"current, populated", cov(CoverageCurrent), nil, false, "", ""},
	} {
		got, reason := KGHonesty(tc.cov, tc.err, tc.empty)
		if got != tc.want {
			t.Errorf("%s: status = %q, want %q", tc.name, got, tc.want)
		}
		if !strings.Contains(reason, tc.wantTxt) {
			t.Errorf("%s: reason %q lacks %q", tc.name, reason, tc.wantTxt)
		}
		if got == "not_built" {
			t.Errorf("%s: not_built is not a status", tc.name)
		}
		if tc.want == "" && reason != "" {
			t.Errorf("%s: a silent row carries a reason %q", tc.name, reason)
		}
	}
}
