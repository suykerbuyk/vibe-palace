// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import "fmt"

// KGStatus says why a knowledge-graph result is empty, so an empty answer is
// never indistinguishable from "no such fact". It is the value of the
// additive `kg_status` field on every KG surface.
type KGStatus string

const (
	// KGTierExcluded: this host excludes the transcript tier, which is where
	// archive-extracted KG records come from.
	KGTierExcluded KGStatus = "tier_excluded"
	// KGEmpty: the graph is genuinely empty on a host whose coverage gives no
	// other explanation.
	KGEmpty KGStatus = "empty"
	// KGUnbuilt, KGAbsent, KGStale and KGLegacy carry the coverage state of the
	// same name; `not_built` is not a status.
	KGUnbuilt KGStatus = "unbuilt"
	KGAbsent  KGStatus = "absent"
	KGStale   KGStatus = "stale"
	KGLegacy  KGStatus = "legacy"
	// KGCoverageUnavailable: the graph is empty and coverage could not be read,
	// so the reason cannot be determined. It is never silence.
	KGCoverageUnavailable KGStatus = "coverage_unavailable"
)

// KGHonesty is the ONE decision of why a KG result is empty. Every surface
// calls it with the same argument convention:
//
//   - cov is the project's CoverageState derivation (nil iff covErr != nil);
//   - covErr is that derivation's error, passed through unchanged;
//   - kgEmpty is "the KG result this surface is about to return is empty".
//     `vp index status` reads no KG and passes false.
//
// The single predicate for "the transcript tier is excluded" is
// cov.State == CoverageAuthored: built, archives exist, the config excludes
// the tier AND the last build observed nothing archive-owned left to serve.
// ExcludedTiers alone is not it (it is also set in state `notes`, where the
// project has no archives), and the raw config is not it (a built index may
// still be serving transcripts). A KG READ failure is not a status: callers
// report it as kg_unreadable or a tool error.
func KGHonesty(cov *Coverage, covErr error, kgEmpty bool) (KGStatus, string) {
	if covErr != nil || cov == nil {
		if !kgEmpty {
			return "", ""
		}
		if covErr != nil {
			return KGCoverageUnavailable, fmt.Sprintf("could not determine why: %v", covErr)
		}
		return KGCoverageUnavailable, "could not determine why: coverage unavailable"
	}
	if cov.State == CoverageAuthored {
		return KGTierExcluded, kgTierExcludedReason(cov.M)
	}
	if !kgEmpty {
		return "", ""
	}
	switch cov.State {
	case CoverageUnbuilt:
		return KGUnbuilt, cov.Reason
	case CoverageAbsent:
		return KGAbsent, cov.Reason
	case CoverageStale:
		return KGStale, cov.Reason
	case CoverageLegacy:
		return KGLegacy, cov.Reason
	case CoveragePartial:
		return KGEmpty, fmt.Sprintf("no facts recorded for this project (%d of %d sessions ingested)", cov.N, cov.M)
	default: // notes, current
		return KGEmpty, "no facts recorded for this project"
	}
}

// kgTierExcludedReason is the one wording of the excluded-tier explanation.
// Only ARCHIVE-EXTRACTED records are transcript-fed: mempalace imports and
// authored vp_kg_add facts remain and are still shown.
func kgTierExcludedReason(archives int) string {
	return fmt.Sprintf("archive-extracted knowledge-graph facts come from the transcript tier, which is excluded on this host "+
		"(%d archives not indexed); those facts are absent, not zero. Imported (mempalace) and authored (vp_kg_add) "+
		"facts, if any, are still shown. Opt in with --transcripts (days-long rebuild; see vp index rebuild --help).", archives)
}
