// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"os"
	"path/filepath"
	"testing"
)

func f(dim, artifact string) Finding {
	return Finding{Dimension: dim, Artifact: artifact, Detail: "detail"}
}

// TestDiff_NewFindingIsReported — the first half of the ratchet.
func TestDiff_NewFindingIsReported(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {Reason: "known", Accepted: []string{"a"}},
	}}

	added, stale := base.Diff([]Finding{f("d", "a"), f("d", "b")})

	if len(added) != 1 || added[0].Artifact != "b" {
		t.Fatalf("added = %+v, want exactly the un-accepted artifact b", added)
	}
	if len(stale) != 0 {
		t.Fatalf("stale = %+v, want none", stale)
	}
}

// TestDiff_StaleEntryIsReported — the SECOND half, and the one that makes this a
// ratchet rather than a suppression file. Without it, a fixed finding stays in the
// record forever and the baseline decays into a lie.
func TestDiff_StaleEntryIsReported(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {Reason: "known", Accepted: []string{"a", "fixed"}},
	}}

	added, stale := base.Diff([]Finding{f("d", "a")}) // "fixed" no longer a finding

	if len(added) != 0 {
		t.Fatalf("added = %+v, want none", added)
	}
	if len(stale) != 1 || stale[0].Artifact != "fixed" {
		t.Fatalf("stale = %+v, want exactly the repaired artifact — a fix that is not "+
			"recorded lets the baseline keep claiming something is broken", stale)
	}
	if stale[0].Reason != "known" {
		t.Errorf("stale entry lost its reason: %+v", stale[0])
	}
}

// TestDiff_AcceptedFindingIsSilent: accepted debt is neither new nor stale. It is
// still a finding — it is simply one we have already looked at.
func TestDiff_AcceptedFindingIsSilent(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {Reason: "predates the field", Accepted: []string{"a", "b"}},
	}}

	added, stale := base.Diff([]Finding{f("d", "a"), f("d", "b")})

	if len(added) != 0 || len(stale) != 0 {
		t.Fatalf("accepted debt must be silent: added=%+v stale=%+v", added, stale)
	}
}

// TestDiff_ExceptOverridesTheDimensionReason: a genuine one-off carries its own
// reason without forcing 269 copies of the shared one.
func TestDiff_ExceptOverridesTheDimensionReason(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {
			Reason: "shared",
			Except: map[string]string{"odd": "this one is different, and here is why"},
		},
	}}

	added, _ := base.Diff([]Finding{f("d", "odd")})
	if len(added) != 0 {
		t.Fatalf("an artifact in Except is ACCEPTED: added = %+v", added)
	}

	_, stale := base.Diff(nil) // "odd" fixed
	if len(stale) != 1 || stale[0].Reason != "this one is different, and here is why" {
		t.Fatalf("a stale Except entry must carry ITS reason, not the dimension's: %+v", stale)
	}
}

// TestDiff_UnknownDimensionFindingIsNew: a finding in a dimension the baseline has
// never heard of is NEW. A baseline cannot accept what it does not mention — silence
// is not consent.
func TestDiff_UnknownDimensionFindingIsNew(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{}}

	added, _ := base.Diff([]Finding{f("brand-new-dimension", "a")})
	if len(added) != 1 {
		t.Fatalf("a finding in an unmentioned dimension must be NEW, got %+v", added)
	}
}

// TestRegenerate_PreservesReasons is the bug internal/sourceaudit actually shipped
// and had to fix at 203: a regen that stamps UNTRIAGED over the reasons destroys the
// triage that produced them, and the list decays back into the blob it started as.
// Regeneration is ROUTINE — every fix forces one — so this is not a corner case.
func TestRegenerate_PreservesReasons(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {
			Reason:   "a human wrote this, at some cost",
			Accepted: []string{"a"},
			Except:   map[string]string{"odd": "and this"},
		},
	}}

	out := base.Regenerate([]Finding{f("d", "a"), f("d", "odd"), f("d", "new")})

	got := out.Dimensions["d"]
	if got.Reason != "a human wrote this, at some cost" {
		t.Fatalf("regen destroyed the dimension reason: %q", got.Reason)
	}
	if got.Except["odd"] != "and this" {
		t.Fatalf("regen destroyed a per-artifact reason: %+v", got.Except)
	}
	if len(got.Accepted) != 2 || got.Accepted[0] != "a" || got.Accepted[1] != "new" {
		t.Fatalf("accepted = %v, want [a new] (sorted, and 'odd' lives in Except)", got.Accepted)
	}
}

// TestRegenerate_DropsFixedFindings — the baseline may only SHRINK. A fixed artifact
// must not survive a regen, including a fixed artifact that had its own Except reason.
func TestRegenerate_DropsFixedFindings(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {
			Reason:   "r",
			Accepted: []string{"a", "fixed"},
			Except:   map[string]string{"fixed-odd": "was special"},
		},
	}}

	out := base.Regenerate([]Finding{f("d", "a")})

	got := out.Dimensions["d"]
	if len(got.Accepted) != 1 || got.Accepted[0] != "a" {
		t.Fatalf("accepted = %v, want only the surviving finding", got.Accepted)
	}
	if _, present := got.Except["fixed-odd"]; present {
		t.Fatal("a fixed artifact kept its Except entry — the baseline GREW a lie instead of shrinking")
	}
}

// TestRegenerate_NewDimensionIsUntriaged: an unexplained dimension must never be able
// to masquerade as an explained one.
func TestRegenerate_NewDimensionIsUntriaged(t *testing.T) {
	out := Baseline{}.Regenerate([]Finding{f("fresh", "a")})

	if got := out.Dimensions["fresh"].Reason; got != ReasonUntriaged {
		t.Fatalf("reason = %q, want the UNTRIAGED marker", got)
	}
}

// TestLoadBaseline_MissingIsEmptyNotError: a vault with no accepted debt is a
// legitimate state and must be held to a clean bill of health.
func TestLoadBaseline_MissingIsEmptyNotError(t *testing.T) {
	b, err := LoadBaseline(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("a missing baseline is not an error: %v", err)
	}
	if len(b.Dimensions) != 0 {
		t.Fatalf("dimensions = %+v, want empty", b.Dimensions)
	}
}

// TestLoadBaseline_CorruptIsAnError: a file that EXISTS but cannot be parsed is an
// error, never an empty baseline. Treating it as empty would accept nothing and
// therefore report every accepted artifact as NEW — a wall of red — or, worse,
// silently pass a vault whose debt record we could not read. "I could not look" is
// not "there was nothing there."
func TestLoadBaseline_CorruptIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadBaseline(path); err == nil {
		t.Fatal("a corrupt baseline must be an error, not a silently-empty one")
	}
}

// TestSave_IsDeterministicAndSorted: the baseline is committed to the vault, so an
// unstable serialization would produce a spurious diff on every run and destroy the
// week-over-week drift signal that is the entire reason to commit it.
func TestSave_IsDeterministicAndSorted(t *testing.T) {
	dir := t.TempDir()
	p1, p2 := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")

	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"z": {Reason: "r", Accepted: []string{"c", "a", "b", "a"}},
		"a": {Reason: "r", Accepted: []string{"y", "x"}},
	}}

	if err := base.Save(p1); err != nil {
		t.Fatal(err)
	}
	if err := base.Save(p2); err != nil {
		t.Fatal(err)
	}

	b1, _ := os.ReadFile(p1)
	b2, _ := os.ReadFile(p2)
	if string(b1) != string(b2) {
		t.Fatal("two saves of the same baseline differ — the file is not byte-reproducible")
	}

	round, err := LoadBaseline(p1)
	if err != nil {
		t.Fatal(err)
	}
	got := round.Dimensions["z"].Accepted
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("accepted = %v, want sorted and de-duplicated [a b c]", got)
	}
}
