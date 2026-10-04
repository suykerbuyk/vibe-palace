// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// Merge's two-marker rule and residue filter (task
// split-and-merge-exclude-derived-palace-paths, Scopes 2 and 4).

func unmarkVault(t *testing.T, root string) {
	t.Helper()
	if err := surface.WriteVaultManifest(root, surface.VaultManifest{Format: surface.RequiredDataFormat}); err != nil {
		t.Fatal(err)
	}
}

// Merge refuses every pairing but migrated into migrated: plan, apply and
// verify each refuse, before any inventory, and the destination is unchanged.
// (b) is an unmarked source whose only derived content is an unstamped
// extracted triple: the rule keys on the markers alone, never on drawers.
// Mutants: no refusal; a drawer-only rule, which passes (b); a refusal keyed
// on the source marker only, which passes (c) and (d); a refusal in plan
// only, which apply bypasses.
func TestVaultMerge_RefusesEveryPairingButMigratedIntoMigrated(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source func(t *testing.T) string
		dest   func(t *testing.T) string
		want   string
	}{
		{"(a) unmarked source with a tracked drawer, into migrated",
			func(t *testing.T) string { s := mergeSourceVault(t, "alpha"); unmarkVault(t, s); return s },
			func(t *testing.T) string { return mergeDestVault(t, "gamma") },
			"the source vault"},
		{"(b) unmarked source with only an unstamped extracted triple, into migrated",
			func(t *testing.T) string {
				s := splitFixtureVault(t, "alpha")
				unmarkVault(t, s)
				for _, rel := range []string{"palace/alpha/drawers/w/r/drawers.jsonl", "palace/alpha/kg/entities.jsonl"} {
					writeSplitFile(t, s, rel, "")
				}
				writeSplitFile(t, s, "palace/alpha/kg/triples/s/a.json", `{"subject":"a","predicate":"b","object":"c","extracted_at":"2026-10-04T00:00:00Z"}`)
				return s
			},
			func(t *testing.T) string { return mergeDestVault(t, "gamma") },
			"the source vault"},
		{"(c) migrated source into an unmarked destination",
			func(t *testing.T) string { return mergeSourceVault(t, "alpha") },
			func(t *testing.T) string { d := mergeDestVault(t, "gamma"); unmarkVault(t, d); return d },
			"the destination vault"},
		{"(d) unmarked into unmarked",
			func(t *testing.T) string { s := mergeSourceVault(t, "alpha"); unmarkVault(t, s); return s },
			func(t *testing.T) string { d := mergeDestVault(t, "gamma"); unmarkVault(t, d); return d },
			"carries no migration marker"},
		{"a source marker that cannot be read",
			func(t *testing.T) string {
				s := mergeSourceVault(t, "alpha")
				writeSplitFile(t, s, ".vibe-palace/vault.toml", "format = 2\nauthored_only = 5\n")
				return s
			},
			func(t *testing.T) string { return mergeDestVault(t, "gamma") },
			"authored_only"},
		{"a destination marker that cannot be read",
			func(t *testing.T) string { return mergeSourceVault(t, "alpha") },
			func(t *testing.T) string {
				d := mergeDestVault(t, "gamma")
				writeSplitFile(t, d, ".vibe-palace/vault.toml", "format = 2\nauthored_only = 5\n")
				return d
			},
			"authored_only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, dest := tc.source(t), tc.dest(t)
			before := snapshotTree(t, dest)
			p := mergeParams(src, "alpha")
			for _, action := range []string{"plan", "apply", "verify"} {
				p.Action = action
				p.ManifestSHA256 = strings.Repeat("a", 64)
				if action == "plan" {
					p.ManifestSHA256 = ""
				}
				if _, err := callMerge(t, dest, p); err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("%s: err = %v, want a refusal naming %q", action, err, tc.want)
				}
			}
			if after := snapshotTree(t, dest); !equalStringMaps(before, after) {
				t.Errorf("a refused merge changed the destination")
			}
		})
	}
}

// Merge leaves the source's derived residue out of the manifest, as split
// does, and keeps the ignored *.bak manifest. Mutant: fixing split only.
func TestVaultMergePlan_LeavesOutDerivedResidue(t *testing.T) {
	src := splitResidueVault(t)
	dest := mergeDestVault(t, "gamma")
	m, err := buildMergeManifest(storage.NewVault(dest), mergeParams(src, "alpha"), true)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range m.Entries {
		paths = append(paths, e.Path)
	}
	for _, rel := range []string{residueDrawer, residueLedger} {
		if slices.Contains(paths, rel) {
			t.Errorf("residue %s is in the merge manifest", rel)
		}
	}
	if !slices.Contains(paths, residueBak) {
		t.Errorf("the ignored *.bak manifest left the merge manifest")
	}
}
