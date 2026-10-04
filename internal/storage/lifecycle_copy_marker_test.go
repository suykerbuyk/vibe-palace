// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"strings"
	"testing"
)

// Copy's two-marker rule (task split-and-merge-exclude-derived-palace-paths,
// Scope 4; ADR-014 decision 11; Chair ruling C7).

// unmarkSource replaces the source's manifest with one without the marker,
// adds a tracked drawer, and publishes it.
func (f *copyFix) unmarkSource(t *testing.T) {
	t.Helper()
	writeFile(t, f.Src, "palace/p/drawers/w/r/drawers.jsonl", "{}\n")
	f.srcPush(t, ".vibe-palace/vault.toml", unmigratedManifest())
}

// unmarkDest replaces the destination's manifest with one without the marker.
func (f *copyFix) unmarkDest(t *testing.T) {
	t.Helper()
	writeFile(t, f.V, ".vibe-palace/vault.toml", unmigratedManifest())
	gitRun(t, f.V, "commit", "-q", "-am", "an unmigrated destination")
	gitRun(t, f.V, "push", "-q", "origin", "main")
}

// Copy refuses every pairing but migrated into migrated: PlanCopy lists the
// refusal, naming the fix; ApplyCopy refuses with ErrCopyRefused before the
// lock and before any write (V's HEAD, remote and trees unchanged, no
// lifecycle marker). Mutants: no copy refusal; a refusal keyed on either
// marker alone; a refusal that a dry run omits.
func TestCopy_RefusesEveryPairingButMigratedIntoMigrated(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *copyFix)
		want  []string
	}{
		{"(a) unmarked source with a tracked drawer, into migrated",
			func(t *testing.T, f *copyFix) { f.unmarkSource(t) },
			[]string{"the source at", "migrate the source first"}},
		{"(b) migrated source into an unmarked destination",
			func(t *testing.T, f *copyFix) { f.unmarkDest(t) },
			[]string{"carries no migration marker", "vp vault init"}},
		{"(c) unmarked into unmarked",
			func(t *testing.T, f *copyFix) { f.unmarkSource(t); f.unmarkDest(t) },
			[]string{"migrate the source first", "vp vault init"}},
		{"a destination marker that cannot be read",
			func(t *testing.T, f *copyFix) {
				writeFile(t, f.V, ".vibe-palace/vault.toml", "format = 2\nauthored_only = 5\n")
				gitRun(t, f.V, "commit", "-q", "-am", "a bad hand edit")
				gitRun(t, f.V, "push", "-q", "origin", "main")
			},
			[]string{"migration marker cannot be read", "authored_only"}},
		{"a source marker that cannot be read",
			func(t *testing.T, f *copyFix) {
				f.srcPush(t, ".vibe-palace/vault.toml", "format = 2\nauthored_only = 5\n")
			},
			[]string{"authored_only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCopyFix(t)
			tc.setup(t, f)
			head := gitRun(t, f.V, "rev-parse", "HEAD")
			plan, err := PlanCopy(f.req("p"))
			if !errors.Is(err, ErrCopyRefused) || plan == nil {
				t.Fatalf("PlanCopy: plan %v, err %v; want the plan and ErrCopyRefused", plan, err)
			}
			joined := strings.Join(plan.Refusals, "\n")
			for _, w := range tc.want {
				if !strings.Contains(joined, w) {
					t.Errorf("the dry run's refusals do not name %q:\n%s", w, joined)
				}
			}
			if _, err := ApplyCopy(f.req("p")); !errors.Is(err, ErrCopyRefused) {
				t.Fatalf("ApplyCopy: err = %v, want ErrCopyRefused", err)
			}
			assertCopyAbsent(t, f, head, "p")
			if markerFound(t, f.V) {
				t.Error("a refused copy wrote a lifecycle marker")
			}
		})
	}
}

// The source marker is read at the TIP: a tip that carries it, over an
// earlier commit that does not, proceeds; a tip that reverts it away is
// refused. Mutant: the marker read from the destination twice, or from a
// commit other than the tip.
func TestCopy_ReadsTheSourceMarkerAtTheTip(t *testing.T) {
	t.Run("marker added at the tip", func(t *testing.T) {
		f := newCopyFix(t)
		f.srcPush(t, ".vibe-palace/vault.toml", unmigratedManifest())
		f.srcPush(t, ".vibe-palace/vault.toml", formatManifest())
		if _, err := ApplyCopy(f.req("p")); err != nil {
			t.Fatalf("ApplyCopy: %v", err)
		}
	})
	t.Run("marker reverted away at the tip", func(t *testing.T) {
		f := newCopyFix(t)
		gitRun(t, f.Src, "revert", "--no-edit", "HEAD")
		f.srcPush(t, ".vibe-palace/vault.toml", unmigratedManifest())
		head := gitRun(t, f.V, "rev-parse", "HEAD")
		if _, err := ApplyCopy(f.req("p")); !errors.Is(err, ErrCopyRefused) {
			t.Fatalf("ApplyCopy: err = %v, want ErrCopyRefused", err)
		}
		assertCopyAbsent(t, f, head, "p")
	})
}
