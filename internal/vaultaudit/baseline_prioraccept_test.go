// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// resumeArtifact is a stand-in for the real thing. The vault path is not load-bearing
// — the baseline keys on whatever string the dimension emits — but naming it after the
// artifact this defect was actually found on keeps the tests readable against the
// report they mirror.
const resumeArtifact = "Projects/alpha/resume.md"

// 🔴 TestRegenerate_PriorAcceptanceSurvivesTheDropThatPrunesEverythingElse is the
// whole change in one test, and the two halves of it must be read together: on the
// SAME leg, the recorded measurement is pruned and the prior-acceptance record is not.
//
// The sibling's shrink-only ratchet needs Measured to go when an artifact stops being
// a finding; this change needs the identity to stay. Asserting only the survival would
// pass just as well if someone had made Measured survive too — which is the one
// outcome this design forbids, because it silently repeals the ratchet.
func TestRegenerate_PriorAcceptanceSurvivesTheDropThatPrunesEverythingElse(t *testing.T) {
	// The reason is HUMAN-WRITTEN and must differ from ReasonUntriaged, or leg 2's
	// check below compares UNTRIAGED to UNTRIAGED and cannot fail on the one thing it
	// is written to catch: a drop that stamps the marker over a real triage.
	const triaged = "accepted debt, owned by the remediation task"

	// LEG 1 — the first acceptance, into a dimension a human has already explained.
	accepted := Baseline{Dimensions: map[string]DimensionBaseline{
		DimResumeDiscipline: {Reason: triaged},
	}}.Regenerate([]Finding{m(DimResumeDiscipline, resumeArtifact, 1000)}, nil)

	d := accepted.Dimensions[DimResumeDiscipline]
	if !slices.Contains(d.PriorAccepted, resumeArtifact) {
		t.Fatalf("leg 1: prior_accepted = %v, want the artifact recorded at its first acceptance",
			d.PriorAccepted)
	}
	if d.Measured[resumeArtifact] != 1000 {
		t.Fatalf("leg 1: measured = %v, want the magnitude recorded too", d.Measured)
	}

	// LEG 2 — THE DROP. The artifact is repaired, so it is not a finding, so this
	// dimension produces nothing at all.
	dropped := accepted.Regenerate(nil, nil)

	d = dropped.Dimensions[DimResumeDiscipline]
	if !slices.Contains(d.PriorAccepted, resumeArtifact) {
		t.Fatalf("leg 2: prior_accepted = %v — the record of an earlier acceptance must "+
			"SURVIVE the drop, or a later re-crossing is indistinguishable from a first one",
			d.PriorAccepted)
	}
	if len(d.Accepted) != 0 {
		t.Fatalf("leg 2: accepted = %v, want nothing — a repaired artifact is not accepted debt",
			d.Accepted)
	}
	if len(d.Measured) != 0 {
		t.Fatalf("leg 2: measured = %v, want NOTHING — the recorded magnitude is pruned on STALE, "+
			"and that pruning is the sibling's shrink-only invariant. Surviving here would repeal it",
			d.Measured)
	}
	if d.Reason != triaged {
		t.Fatalf("leg 2: reason = %q, want the human-written triage %q carried through the drop — "+
			"stamping UNTRIAGED here is the regenerate-destroys-triage defect in a new costume",
			d.Reason, triaged)
	}

	// LEG 3 — THE RE-CROSSING, larger than it was ever accepted at. Detection is
	// unchanged: the existing not-accepted arm reports it NEW.
	recross := []Finding{m(DimResumeDiscipline, resumeArtifact, 6000)}
	added, stale := dropped.Diff(recross)
	if len(added) != 1 || added[0].Artifact != resumeArtifact {
		t.Fatalf("leg 3: added = %+v, want the re-crossed artifact reported NEW", added)
	}
	if len(stale) != 0 {
		t.Fatalf("leg 3: stale = %+v, want none — a stub that accepts nothing has nothing to go "+
			"stale, and a spurious STALE row would make every run after a drop report red", stale)
	}
	if !strings.Contains(added[0].Detail, "ACCEPTED BEFORE") {
		t.Fatalf("leg 3: detail = %q, want the report to say this artifact has been here before — "+
			"otherwise the operator meets the refusal with no warning in the report that produced it",
			added[0].Detail)
	}

	// AND THE REFUSAL. This is what a plain `--accept` now hits.
	refused := dropped.Reacceptances(recross)
	if len(refused) != 1 || refused[0].Artifact != resumeArtifact {
		t.Fatalf("reacceptances = %+v, want exactly the re-crossed artifact", refused)
	}
}

// TestRegenerate_ReacceptKeepsTheHistoryItOverrode — the override is a LICENCE to
// accept, never an erasure. If re-accepting cleared the record, the second override
// would be as silent as the first re-arm was, and the third crossing would again look
// like a first.
func TestRegenerate_ReacceptKeepsTheHistoryItOverrode(t *testing.T) {
	prior := Baseline{Dimensions: map[string]DimensionBaseline{
		DimResumeDiscipline: {Reason: "known", Accepted: []string{}, PriorAccepted: []string{resumeArtifact}},
	}}

	// What `--accept --reaccept` does: Regenerate over the re-crossing.
	out := prior.Regenerate([]Finding{m(DimResumeDiscipline, resumeArtifact, 6000)}, nil)

	d := out.Dimensions[DimResumeDiscipline]
	if !slices.Contains(d.Accepted, resumeArtifact) {
		t.Fatalf("accepted = %v, want the re-crossing accepted — the override's whole job", d.Accepted)
	}
	if !slices.Contains(d.PriorAccepted, resumeArtifact) {
		t.Fatalf("prior_accepted = %v, want the history RETAINED across the override", d.PriorAccepted)
	}
	if got := len(d.PriorAccepted); got != 1 {
		t.Fatalf("prior_accepted has %d entries, want 1 — re-appending the same artifact must "+
			"deduplicate, or the set grows a row per accept forever and churns the diff", got)
	}
	// And the refusal is gone for the next run, because the artifact is accepted again.
	if refused := out.Reacceptances([]Finding{m(DimResumeDiscipline, resumeArtifact, 6000)}); len(refused) != 0 {
		t.Fatalf("reacceptances = %+v, want none — a currently-accepted artifact is not a re-crossing",
			refused)
	}
}

// TestReacceptances_OnlyRefusesAnArtifactThatIsNotAcceptedNOW pins the arm choice.
// Growth and unrecorded-measurement are findings on a CURRENTLY accepted path — the
// continuous-acceptance case `--raise` answers. Refusing those would make --raise
// unreachable without --reaccept, which is two overrides for one situation.
func TestReacceptances_OnlyRefusesAnArtifactThatIsNotAcceptedNOW(t *testing.T) {
	history := []string{resumeArtifact}
	for _, tc := range []struct {
		name string
		base DimensionBaseline
		want int
	}{
		{
			name: "grown past its record is not a re-crossing",
			base: DimensionBaseline{Accepted: []string{resumeArtifact}, PriorAccepted: history,
				Measured: map[string]int64{resumeArtifact: 1000}},
			want: 0,
		},
		{
			name: "accepted with no recorded measurement is not a re-crossing",
			base: DimensionBaseline{Accepted: []string{resumeArtifact}, PriorAccepted: history},
			want: 0,
		},
		{
			name: "excepted is not a re-crossing",
			base: DimensionBaseline{Except: map[string]string{resumeArtifact: "its own reason"},
				PriorAccepted: history, Measured: map[string]int64{resumeArtifact: 9000}},
			want: 0,
		},
		{
			name: "dropped and back IS a re-crossing",
			base: DimensionBaseline{Accepted: []string{}, PriorAccepted: history},
			want: 1,
		},
		{
			name: "never accepted before is a plain first crossing",
			base: DimensionBaseline{Accepted: []string{}},
			want: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := Baseline{Dimensions: map[string]DimensionBaseline{DimResumeDiscipline: tc.base}}
			got := b.Reacceptances([]Finding{m(DimResumeDiscipline, resumeArtifact, 6000)})
			if len(got) != tc.want {
				t.Fatalf("reacceptances = %+v, want %d", got, tc.want)
			}
		})
	}
}

// TestRegenerate_RecordsPriorAcceptanceOnlyForTheNamedDimensions — the scoping is the
// point. A never-pruned set on every categorical dimension would be a permanently
// growing suppression record with no reader, and the ruling was to design for the one
// pure-magnitude dimension that exists.
func TestRegenerate_RecordsPriorAcceptanceOnlyForTheNamedDimensions(t *testing.T) {
	// The counter-example CARRIES A MAGNITUDE, deliberately. A Measure-zero finding is
	// suppressed by the other half of the gate, so using one here would leave this test
	// passing with the dimension check deleted — which is exactly what it happened to
	// do until a mutation sweep said so.
	out := Baseline{Dimensions: map[string]DimensionBaseline{}}.Regenerate([]Finding{
		m(DimResumeDiscipline, resumeArtifact, 1000),
		m(DimKGPortability, "palace/kg/triples/bad:name.md", 1000),
	}, nil)

	if got := out.Dimensions[DimKGPortability].PriorAccepted; len(got) != 0 {
		t.Fatalf("a dimension outside the policy set recorded prior acceptance: %v — the record "+
			"is scoped to the one pure-magnitude dimension, and a set nothing reads is a "+
			"suppression list that only grows", got)
	}
	if got := out.Dimensions[DimResumeDiscipline].PriorAccepted; len(got) != 1 {
		t.Fatalf("prior_accepted = %v, want the magnitude dimension's artifact recorded", got)
	}
}

// TestDimensionsRecordingPriorAcceptance_NamesOnlyRegisteredDimensions — the policy set
// is hand-written beside the registry rather than derived from it, deliberately, so
// this is the guard that keeps it from naming a dimension that no longer runs. A key
// nothing produces findings for is a rule that silently never fires.
func TestDimensionsRecordingPriorAcceptance_NamesOnlyRegisteredDimensions(t *testing.T) {
	registered := DimensionNames()
	if len(dimensionsRecordingPriorAcceptance) == 0 {
		t.Fatal("no dimension records prior acceptance — the refusal can never fire")
	}
	for name := range dimensionsRecordingPriorAcceptance {
		if !slices.Contains(registered, name) {
			t.Errorf("dimensionsRecordingPriorAcceptance names %q, which is not a registered "+
				"dimension: %v", name, registered)
		}
	}
}

// 🔴 TestSave_RoundTripsPriorAcceptedThroughDisk — Save rebuilds a KEYED LITERAL rather
// than copying the struct, so a field missing from that literal is silently dropped on
// every write while working perfectly in memory, and `omitempty` means the key just
// vanishes rather than appearing as null. Only a disk round-trip catches it. This is
// the same pin TestSave_RoundTripsMeasuredThroughDisk exists for, on the field this
// change adds — and it matters more here, because the whole value of this field is
// that it survives to a LATER RUN, which means it survives through the file.
func TestSave_RoundTripsPriorAcceptedThroughDisk(t *testing.T) {
	p := filepath.Join(t.TempDir(), "baseline.json")
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		DimResumeDiscipline: {Reason: "known", Accepted: []string{}, PriorAccepted: []string{resumeArtifact}},
	}}

	// "" is the correct vault root: a bare temp file is not a vault path.
	if err := base.Save("", p); err != nil {
		t.Fatal(err)
	}
	round, err := LoadBaseline(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := round.Dimensions[DimResumeDiscipline].PriorAccepted; !slices.Contains(got, resumeArtifact) {
		t.Fatalf("prior_accepted = %v after a save/load round-trip, want the artifact — Save's "+
			"literal names its fields, and a field it omits is lost on disk with no error", got)
	}
	// And the refusal still fires on the LOADED baseline, which is the only one a real
	// run ever sees. A field that round-trips into a struct nothing consults is dead.
	if refused := round.Reacceptances([]Finding{m(DimResumeDiscipline, resumeArtifact, 6000)}); len(refused) != 1 {
		t.Fatalf("reacceptances after reload = %+v, want the re-crossing refused", refused)
	}
}

// TestSave_NoPriorAcceptedKeyForADimensionThatRecordsNone — an older binary must still
// parse what this one writes, and every categorical dimension in the live baseline must
// keep its current on-disk shape. omitempty does the work; this proves it.
func TestSave_NoPriorAcceptedKeyForADimensionThatRecordsNone(t *testing.T) {
	p := filepath.Join(t.TempDir(), "baseline.json")
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		DimKGPortability: {Reason: "known", Accepted: []string{"a"}},
	}}
	if err := base.Save("", p); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "prior_accepted") {
		t.Fatalf("a dimension that records no prior acceptance emitted the key:\n%s", raw)
	}
}

// TestSave_ANoChangeRunIsByteIdentical — Audits/baseline.json is committed and diffed
// week over week, and its own docstring says clean diffs are the entire point. The stub
// entry a drop leaves behind is new state in that file, so it gets the same guarantee:
// regenerate the same findings against the saved result and the bytes must not move.
func TestSave_ANoChangeRunIsByteIdentical(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.json")
	second := filepath.Join(dir, "second.json")

	// A baseline carrying BOTH shapes: a live accepted artifact, and a dimension that
	// survives only as a prior-acceptance stub.
	seeded := Baseline{Dimensions: map[string]DimensionBaseline{}}.Regenerate([]Finding{
		m(DimResumeDiscipline, resumeArtifact, 1000),
		f(DimKGPortability, "palace/kg/triples/bad:name.md"),
	}, nil)
	dropped := seeded.Regenerate([]Finding{f(DimKGPortability, "palace/kg/triples/bad:name.md")}, nil)
	if err := dropped.Save("", first); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadBaseline(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := reloaded.Regenerate([]Finding{f(DimKGPortability, "palace/kg/triples/bad:name.md")}, nil).
		Save("", second); err != nil {
		t.Fatal(err)
	}

	a, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("a no-change run moved the bytes:\n--- first ---\n%s\n--- second ---\n%s", a, b)
	}
	if !strings.Contains(string(a), "prior_accepted") {
		t.Fatalf("the fixture lost its stub, so this test proved nothing:\n%s", a)
	}
}

// 🔴 TestRegenerate_ADroppedArtifactKeepsItsRecordWhileItsDimensionLivesOn covers the
// case the whole-dimension stub does NOT: one artifact is repaired while another in the
// same dimension is still a finding, so the dimension survives through the main loop and
// the stub pass never runs for it.
//
// This is the LIVE shape, not a corner. resume-discipline measures every project's
// resume.md, so a dimension with one artifact over cap and one just repaired is the
// ordinary week. Rebuilding the record from the findings in hand would erase the history
// of every artifact this run happens not to be looking at — the same whole-report hazard
// the measurement ratchet's `--raise` guard exists for.
func TestRegenerate_ADroppedArtifactKeepsItsRecordWhileItsDimensionLivesOn(t *testing.T) {
	const other = "Projects/beta/resume.md"
	prior := Baseline{Dimensions: map[string]DimensionBaseline{
		DimResumeDiscipline: {
			Reason:        "known",
			Accepted:      []string{other},
			PriorAccepted: []string{other, resumeArtifact},
			Measured:      map[string]int64{other: 1000, resumeArtifact: 900},
		},
	}}

	// Only beta is still over cap. alpha was repaired and is not a finding.
	out := prior.Regenerate([]Finding{m(DimResumeDiscipline, other, 1000)}, nil)

	d := out.Dimensions[DimResumeDiscipline]
	if !slices.Contains(d.PriorAccepted, resumeArtifact) {
		t.Fatalf("prior_accepted = %v, want the REPAIRED artifact's record kept: the dimension "+
			"survived on another artifact, so nothing here is evidence that alpha never crossed",
			d.PriorAccepted)
	}
	if !slices.Contains(d.PriorAccepted, other) {
		t.Fatalf("prior_accepted = %v, want the surviving artifact recorded too", d.PriorAccepted)
	}
	// And the sibling's invariant on the same leg: the repaired artifact's MAGNITUDE is
	// pruned. Identity survives, measurement does not.
	if _, still := d.Measured[resumeArtifact]; still {
		t.Fatalf("measured = %v, want the repaired artifact's magnitude pruned — that pruning is "+
			"the shrink-only ratchet, and only the identity is exempt from it", d.Measured)
	}
	// The consequence: alpha crossing again is still a refusal.
	if refused := out.Reacceptances([]Finding{m(DimResumeDiscipline, resumeArtifact, 5000)}); len(refused) != 1 {
		t.Fatalf("reacceptances = %+v, want alpha's re-crossing refused", refused)
	}
}

// 🔴 TestRegenerate_AnUnknownDimensionKeepsItsPriorAcceptanceUnaliased covers the ONE
// path nobody exercises in normal use: a dimension whose key this binary's registry
// does not know, carried verbatim because it never ran and therefore proved nothing.
//
// Two failures live here and neither is visible from a current binary. The carry is a
// STRUCT COPY followed by a hand-written list of clones, so a field added later is
// carried but not cloned — the result then ALIASES the caller's baseline, and the
// block's own doc comment promises it does not. And nothing else in the tree proves an
// older binary's --accept preserves this field at all, which is the exact loss the
// block exists to prevent, on its newest member.
func TestRegenerate_AnUnknownDimensionKeepsItsPriorAcceptanceUnaliased(t *testing.T) {
	const unknown = "a-dimension-this-binary-does-not-know"
	if slices.Contains(DimensionNames(), unknown) {
		t.Fatalf("%q is a registered dimension, so this test no longer covers the unknown path", unknown)
	}
	in := Baseline{Dimensions: map[string]DimensionBaseline{
		unknown: {Reason: "written by a newer binary", PriorAccepted: []string{"Projects/x/resume.md"}},
	}}

	out := in.Regenerate(nil, nil)

	got := out.Dimensions[unknown].PriorAccepted
	if !slices.Contains(got, "Projects/x/resume.md") {
		t.Fatalf("prior_accepted = %v, want the record carried — a dimension this binary never "+
			"ran proved nothing, so its entry may not be dropped", got)
	}
	got[0] = "MUTATED"
	if in.Dimensions[unknown].PriorAccepted[0] == "MUTATED" {
		t.Fatal("the carried entry ALIASES the input baseline: writing through the result " +
			"mutated the caller's copy. Regenerate returns a new baseline and every field of " +
			"it must be freshly allocated, or a later Save or edit reaches back into the input")
	}
}

// TestRegenerate_AnExceptedArtifactIsRecorded pins a semantic that is easy to read the
// other way. classify consults Except FIRST and treats it as accepted — an artifact
// there need not appear in Accepted at all — so an artifact accepted through a one-off
// reason has been accepted, and its return after a repair is a re-crossing like any
// other. The sibling table's "excepted is not a re-crossing" case covers the artifact
// while it is STILL excepted; this covers what happens after that entry is pruned.
func TestRegenerate_AnExceptedArtifactIsRecorded(t *testing.T) {
	prior := Baseline{Dimensions: map[string]DimensionBaseline{
		DimResumeDiscipline: {Reason: "known", Except: map[string]string{resumeArtifact: "its own reason"}},
	}}

	accepted := prior.Regenerate([]Finding{m(DimResumeDiscipline, resumeArtifact, 1000)}, nil)
	if !slices.Contains(accepted.Dimensions[DimResumeDiscipline].PriorAccepted, resumeArtifact) {
		t.Fatalf("prior_accepted = %v, want the EXCEPTED artifact recorded: Except is an acceptance "+
			"with its own reason, not a bypass", accepted.Dimensions[DimResumeDiscipline].PriorAccepted)
	}

	// And after the repair drops the Except entry, the return is refused.
	dropped := accepted.Regenerate(nil, nil)
	if _, still := dropped.Dimensions[DimResumeDiscipline].Except[resumeArtifact]; still {
		t.Fatal("the except entry survived the drop — the shrink rule covers Except too")
	}
	if refused := dropped.Reacceptances([]Finding{m(DimResumeDiscipline, resumeArtifact, 6000)}); len(refused) != 1 {
		t.Fatalf("reacceptances = %+v, want the re-crossing of a once-excepted artifact refused", refused)
	}
}

// 🔴 TestRegenerate_ACategoricalFindingInAMeasuringDimensionIsNotRecorded is a forward
// guard, not a description of today: every resume-discipline finding currently carries
// a magnitude, so nothing in the live vault exercises it.
//
// It exists because auditResumeDiscipline's own doc records a placeholder-token check
// that is deliberately unimplemented and is real future work. When it lands it will
// emit CATEGORICAL findings — Measure == 0 — on the SAME artifact key as the size cap.
// Only a magnitude can fall under a threshold and come back, so a token finding must
// never arm a refusal built for magnitudes; the day that check ships, this test is what
// says so, in the package rather than in a comment nobody re-reads.
func TestRegenerate_ACategoricalFindingInAMeasuringDimensionIsNotRecorded(t *testing.T) {
	out := Baseline{Dimensions: map[string]DimensionBaseline{}}.
		Regenerate([]Finding{f(DimResumeDiscipline, resumeArtifact)}, nil)

	d := out.Dimensions[DimResumeDiscipline]
	if !slices.Contains(d.Accepted, resumeArtifact) {
		t.Fatalf("accepted = %v — a categorical finding is still accepted debt", d.Accepted)
	}
	if len(d.PriorAccepted) != 0 {
		t.Fatalf("prior_accepted = %v, want nothing: a finding with no magnitude cannot drop "+
			"below a threshold and come back, so there is no crossing to remember", d.PriorAccepted)
	}
}

// TestSave_NormalisesAHandWrittenPriorAcceptedSet — Audits/baseline.json is a committed
// file a human may edit, and the unknown-dimension carry hands Save whatever the file
// held, unnormalised. Save is therefore the last place that can guarantee the clean
// diff its own docstring promises: an unsorted or duplicated set written straight
// through would churn the file on the next run for no change in meaning.
func TestSave_NormalisesAHandWrittenPriorAcceptedSet(t *testing.T) {
	p := filepath.Join(t.TempDir(), "baseline.json")
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		DimResumeDiscipline: {
			Reason:        "known",
			Accepted:      []string{},
			PriorAccepted: []string{"Projects/z/resume.md", "Projects/a/resume.md", "Projects/a/resume.md"},
		},
	}}
	if err := base.Save("", p); err != nil {
		t.Fatal(err)
	}
	round, err := LoadBaseline(p)
	if err != nil {
		t.Fatal(err)
	}
	got := round.Dimensions[DimResumeDiscipline].PriorAccepted
	want := []string{"Projects/a/resume.md", "Projects/z/resume.md"}
	if !slices.Equal(got, want) {
		t.Fatalf("prior_accepted = %v, want %v — sorted and deduplicated, or the file churns on "+
			"a run that changed nothing", got, want)
	}
}

// TestSave_ADroppedDimensionWritesAnEmptyAcceptedArray — `accepted` carries no
// omitempty, so a stub built with a nil slice serialises as `"accepted": null` in a
// file humans read in review. `[]` says "this dimension accepts nothing"; `null` says
// nothing and invites a reader to wonder whether the writer failed. The byte-stability
// test cannot see this, because both runs produce the same null.
func TestSave_ADroppedDimensionWritesAnEmptyAcceptedArray(t *testing.T) {
	p := filepath.Join(t.TempDir(), "baseline.json")
	accepted := Baseline{Dimensions: map[string]DimensionBaseline{}}.
		Regenerate([]Finding{m(DimResumeDiscipline, resumeArtifact, 1000)}, nil)
	if err := accepted.Regenerate(nil, nil).Save("", p); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "null") {
		t.Fatalf("the stub left behind by a drop serialised a null:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"accepted": []`) {
		t.Fatalf("want an explicit empty accepted array in the stub:\n%s", raw)
	}
}
