// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// futureDimension is a baseline key no registry entry will ever match.
//
// It is deliberately NOT one of the real DimXxx constants. A test that reached for a
// real name would pass today and start lying the moment that dimension were renamed
// or retired — it would be asserting "an unknown key is reported" while actually
// exercising a KNOWN one, and the assertion would go vacuously green. The name says
// out loud what it stands for: an entry written by a `vp` newer than this binary.
const futureDimension = "dimension-from-a-future-binary"

// TestUnknownDimensions reads the registry-membership rule from both sides at once:
// a fabricated key must be reported, and every real registry key must NOT be — a
// version of this function that returned everything, or nothing, would satisfy only
// one of those.
func TestUnknownDimensions(t *testing.T) {
	real := DimensionNames()
	if len(real) == 0 {
		t.Fatal("the registry is empty — every case below would pass vacuously")
	}

	// A baseline carrying every real key, for the "binary is current" case. Built
	// from DimensionNames rather than listed here, because a hand-written list beside
	// the registry is the stored-derived-value defect this package keeps deleting.
	allReal := map[string]DimensionBaseline{}
	for _, name := range real {
		allReal[name] = DimensionBaseline{Reason: "accepted", Accepted: []string{"artifact"}}
	}
	realPlusFuture := map[string]DimensionBaseline{futureDimension: {Reason: "from ahead"}}
	for name, d := range allReal {
		realPlusFuture[name] = d
	}

	cases := []struct {
		name string
		dims map[string]DimensionBaseline
		want []string
	}{
		{
			// The signal itself: a key with no registry entry is proof the binary is behind.
			name: "a key this binary does not know is reported",
			dims: map[string]DimensionBaseline{futureDimension: {Reason: "from ahead"}},
			want: []string{futureDimension},
		},
		{
			// The other half, and the one that catches an over-broad implementation:
			// a current binary must report NOTHING, or every audit run screams.
			name: "a baseline of only real registry keys is empty",
			dims: allReal,
			want: nil,
		},
		{
			// The realistic shape — a vault that gained ONE dimension since this
			// binary was built. The real keys must not be dragged along with it.
			name: "real keys are not reported alongside an unknown one",
			dims: realPlusFuture,
			want: []string{futureDimension},
		},
		{
			// Sorted, because this list reaches a COMMITTED report and Go's map order
			// is randomised. Unsorted output would churn `git log -p Audits/` on every
			// run and destroy the drift signal the report's fixed ordering protects.
			name: "output is sorted, not map order",
			dims: map[string]DimensionBaseline{
				"zzz-" + futureDimension: {},
				futureDimension:          {},
				"aaa-" + futureDimension: {},
			},
			want: []string{"aaa-" + futureDimension, futureDimension, "zzz-" + futureDimension},
		},
		{
			// An empty baseline is a legitimate state (LoadBaseline returns one for a
			// missing file). It says nothing about the binary's age.
			name: "an empty baseline reports nothing",
			dims: map[string]DimensionBaseline{},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Baseline{Dimensions: tc.dims}.UnknownDimensions()

			if len(got) != len(tc.want) {
				t.Fatalf("UnknownDimensions() = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("UnknownDimensions() = %v, want %v (differs at %d)", got, tc.want, i)
				}
			}
			// Belt and braces on the half that matters most: no real registry key may
			// ever appear, whatever else the function decided.
			for _, name := range real {
				if slices.Contains(got, name) {
					t.Errorf("registry dimension %q was reported as UNKNOWN to its own registry", name)
				}
			}
		})
	}
}

// 🔴 TestRegenerate_PreservesADimensionThisBinaryDoesNotKnow is THE regression this
// change exists for, and it is the destructive half of the defect.
//
// `vp audit vault --accept` calls Regenerate and then Save. Regenerate builds its
// output from findings alone, and a dimension this binary does not know produces no
// findings — so before this fix, an operator on a stale `vp` typing --accept for an
// unrelated reason PERMANENTLY DELETED the reason a human wrote, the accepted list,
// the per-artifact exceptions and the recorded measurements of every dimension their
// binary predated. The report that run also mangles can be recovered by re-running.
// The baseline cannot: the prior contents are gone the moment Save returns.
//
// The assertion is EXACT — every field compared value by value, not merely "the entry
// is still there". A carry-forward that preserved the key and dropped Measured would
// satisfy a presence check while destroying the magnitudes the ratchet is built on.
func TestRegenerate_PreservesADimensionThisBinaryDoesNotKnow(t *testing.T) {
	prior := DimensionBaseline{
		Reason:   "a human wrote this in a newer binary, at some cost",
		Accepted: []string{"Notes/a.md", "Notes/b.md"},
		Except:   map[string]string{"Notes/odd.md": "this one is different"},
		Measured: map[string]int64{"Notes/a.md": 42, "Notes/odd.md": 7},
	}
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		futureDimension: prior,
		// A known dimension alongside it, so the test exercises the real shape: a
		// stale binary that CAN audit some of the vault and accepts what it found.
		DimArchiveRoundTrip: {Reason: "known debt", Accepted: []string{"Archive/x.json"}},
	}}

	// The findings a stale binary produces: only the dimensions it actually ran.
	out := base.Regenerate([]Finding{f(DimArchiveRoundTrip, "Archive/x.json")}, nil)

	got, present := out.Dimensions[futureDimension]
	if !present {
		t.Fatalf("regen DELETED %q — a dimension that never ran proved nothing, and dropping "+
			"its entry destroys human triage that no re-run can rebuild", futureDimension)
	}
	if got.Reason != prior.Reason {
		t.Errorf("Reason = %q, want %q", got.Reason, prior.Reason)
	}
	if !slices.Equal(got.Accepted, prior.Accepted) {
		t.Errorf("Accepted = %v, want %v", got.Accepted, prior.Accepted)
	}
	if len(got.Except) != len(prior.Except) {
		t.Errorf("Except = %v, want %v", got.Except, prior.Except)
	}
	for artifact, reason := range prior.Except {
		if got.Except[artifact] != reason {
			t.Errorf("Except[%q] = %q, want %q", artifact, got.Except[artifact], reason)
		}
	}
	if len(got.Measured) != len(prior.Measured) {
		t.Errorf("Measured = %v, want %v", got.Measured, prior.Measured)
	}
	for artifact, measure := range prior.Measured {
		if got.Measured[artifact] != measure {
			t.Errorf("Measured[%q] = %d, want %d", artifact, got.Measured[artifact], measure)
		}
	}

	// And the dimension that DID run is still regenerated normally — the carry-forward
	// must not swallow the ordinary path on its way past.
	if ran := out.Dimensions[DimArchiveRoundTrip]; len(ran.Accepted) != 1 || ran.Accepted[0] != "Archive/x.json" {
		t.Errorf("the dimension that ran regenerated wrong: %+v", ran)
	}
}

// TestRegenerate_PreservationDoesNotAliasTheInput: the carried entry must be a deep
// copy. Every other entry Regenerate returns is freshly allocated, and one entry
// secretly sharing the caller's maps would let an edit of the RESULT mutate the
// INPUT — a baseline quietly rewriting the record it was derived from.
func TestRegenerate_PreservationDoesNotAliasTheInput(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		futureDimension: {
			Reason:   "r",
			Accepted: []string{"a"},
			Except:   map[string]string{"odd": "why"},
			Measured: map[string]int64{"a": 10},
		},
	}}

	out := base.Regenerate(nil, nil)
	out.Dimensions[futureDimension].Except["odd"] = "CLOBBERED"
	out.Dimensions[futureDimension].Measured["a"] = 999
	out.Dimensions[futureDimension].Accepted[0] = "CLOBBERED"

	src := base.Dimensions[futureDimension]
	if src.Except["odd"] != "why" || src.Measured["a"] != 10 || src.Accepted[0] != "a" {
		t.Fatalf("mutating the regenerated baseline reached back into the input: %+v", src)
	}
}

// 🔴 TestRegenerate_StillDropsAKnownDimensionThatIsFixed is the guard on the guard.
//
// The shrink rule — THE BASELINE MAY ONLY SHRINK — is what makes fixing a bug
// mechanically compel the record to be corrected. The preservation above is a hole
// cut in it, and a hole cut slightly too wide would blunt the ratchet everywhere
// without failing a single other test: entries would simply stop dropping, quietly,
// and the baseline would rot back into the suppression file it was designed not to be.
//
// The distinction is the whole of it. A KNOWN dimension with no findings was ASKED and
// answered "clean" — it drops. An UNKNOWN dimension with no findings was never asked.
func TestRegenerate_StillDropsAKnownDimensionThatIsFixed(t *testing.T) {
	known := DimensionNames()[0]
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		known: {
			Reason:   "was broken",
			Accepted: []string{"Notes/fixed.md"},
			Except:   map[string]string{"Notes/fixed-odd.md": "was special"},
			Measured: map[string]int64{"Notes/fixed.md": 5},
		},
	}}

	// The fix landed: this dimension ran and found nothing.
	out := base.Regenerate(nil, nil)

	if entry, present := out.Dimensions[known]; present {
		t.Fatalf("a KNOWN dimension that ran clean survived the regen as %+v — the baseline may "+
			"only SHRINK, and preserving it here would turn the ratchet into a suppression file",
			entry)
	}
}

// TestRegenerate_FindingsWinOverTheCarryForward: Regenerate is a pure function and a
// caller may hand it anything. If findings DO arrive for a key the registry does not
// list, the binary demonstrably has evidence about it after all — and evidence must
// beat a verbatim copy of the old entry, or a real fix in that dimension could never
// be recorded.
func TestRegenerate_FindingsWinOverTheCarryForward(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		futureDimension: {Reason: "prior", Accepted: []string{"stale-artifact", "survivor"}},
	}}

	out := base.Regenerate([]Finding{f(futureDimension, "survivor")}, nil)

	got := out.Dimensions[futureDimension]
	if len(got.Accepted) != 1 || got.Accepted[0] != "survivor" {
		t.Fatalf("Accepted = %v, want only the surviving finding — a carry-forward must not "+
			"override an entry rebuilt from real evidence", got.Accepted)
	}
	if got.Reason != "prior" {
		t.Errorf("Reason = %q, want the human-written one preserved by the ordinary path", got.Reason)
	}
}

// 🔴 TestRender_UnknownDimensionsAreLoudButDoNotChangeTheVerdict pins the exact hazard
// this whole change is about, and pins that closing it did not create a second gate.
//
// The hazard is QUIET PARTIALITY. An unknown dimension gets no row in the table — Run
// only builds rows for the registry. Its accepted artifacts do produce stale entries
// inside Baseline.Diff, but newDimensionResult keeps only the entries matching the
// KNOWN dimension it is building, so they are computed and then discarded. Failed()
// never sees them. The result is a report that renders ✅ PASS while whole dimensions
// of the vault went unexamined — which is why the block is the ONLY signal here, not
// an explanation of some louder one.
//
// And it must STAY only a signal. The audit is advisory by operator decision (204),
// and the dispatch-seam surface gate is where a stale binary is actually refused. If
// this block ever started flipping the headline, that would be a second gate in a
// second place, and the first one to be wrong at a bad moment gets both switched off.
func TestRender_UnknownDimensionsAreLoudButDoNotChangeTheVerdict(t *testing.T) {
	clean := Report{Dimensions: []DimensionResult{{Name: "d", Status: StatusPass, Evidence: "echo hi"}}}
	partial := clean
	partial.UnknownDimensions = []string{futureDimension}

	cleanOut := clean.Render("2026-07-14", "/some/vault")
	partialOut := partial.Render("2026-07-14", "/some/vault")

	// Absent field, absent block. A report from a current binary must not carry a
	// paragraph about being stale — a warning that fires always is read as never.
	if strings.Contains(cleanOut, futureDimension) || strings.Contains(cleanOut, "BEHIND THE VAULT") {
		t.Errorf("a report with no unknown dimensions emitted the stale-binary block:\n%s", partialOut)
	}

	// Present field, loud block — naming the dimension, the mechanism, and the fix.
	if !strings.Contains(partialOut, futureDimension) {
		t.Error("the block does not name the dimension that did not run")
	}
	if !strings.Contains(partialOut, "BEHIND THE VAULT") {
		t.Error("the block does not say the binary is behind the vault")
	}
	// The remediation is asserted by DERIVING it from its one source rather than by
	// restating the prose here. A hand-written expectation would be a third copy of
	// text that has already drifted once when it had two, and it would keep passing
	// against a block that had quietly stopped matching the gate's wording.
	for _, line := range (&surface.IncompatibleError{}).Remediation() {
		if !strings.Contains(partialOut, line) {
			t.Errorf("the block dropped a remediation line %q — a stranded reader's only route "+
				"back is this text", line)
		}
	}
	if !strings.Contains(partialOut, BaselineRelPath) {
		t.Error("the block does not name the file the evidence came from")
	}

	// THE PIN. Both reports carry the same PASS headline: the block explains a partial
	// audit, it does not fail one.
	const headline = "## ✅ PASS — no new drift"
	if !strings.Contains(cleanOut, headline) {
		t.Fatalf("precondition: the control report is not a PASS\n%s", firstLines(cleanOut, 12))
	}
	if !strings.Contains(partialOut, headline) {
		t.Errorf("an unknown dimension changed the report's VERDICT — this is a reader-side "+
			"signal, not a gate, and gating belongs at the dispatch seam\n%s",
			firstLines(partialOut, 12))
	}
	if partial.Failed() {
		t.Error("Failed() flipped on an unknown dimension — the verdict must be computed from " +
			"the rows, exactly as before")
	}
}

// TestRun_PopulatesUnknownDimensionsFromTheBaselineItDiffed: the field has to be filled
// from the SAME baseline Run diffed against, not from a second read. A re-read could
// see a different file, and a report disagreeing with the record it was built from is
// a report about a vault that never existed.
//
// This also proves the wiring end to end: without it, UnknownDimensions could be
// correct and the report still silently partial, because nobody called it.
func TestRun_PopulatesUnknownDimensionsFromTheBaselineItDiffed(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	base := Baseline{Dimensions: map[string]DimensionBaseline{
		futureDimension: {Reason: "written by a newer vp", Accepted: []string{"Notes/x.md"}},
	}}
	basePath := filepath.Join(vault.Root, filepath.FromSlash(BaselineRelPath))
	if err := base.Save(vault.Root, basePath); err != nil {
		t.Fatalf("save baseline: %v", err)
	}

	report, err := Run(vault)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(report.UnknownDimensions) != 1 || report.UnknownDimensions[0] != futureDimension {
		t.Fatalf("report.UnknownDimensions = %v, want [%s] — Run must carry the signal or the "+
			"report is silently partial", report.UnknownDimensions, futureDimension)
	}
	// And the dimension genuinely has no row, which is the thing the block warns about.
	for _, d := range report.Dimensions {
		if d.Name == futureDimension {
			t.Fatalf("an unknown dimension somehow produced a row: %+v", d)
		}
	}
}
