// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// m builds a finding that CARRIES a magnitude — the shape only a measuring dimension
// produces. The bare f() helper leaves Measure zero, which is the categorical shape.
func m(dim, artifact string, measure int64) Finding {
	return Finding{Dimension: dim, Artifact: artifact, Detail: "detail", Measure: measure}
}

// TestDiff_GrowthPastRecordedIsNew — the hole this whole change exists to close. An
// accepted artifact that grows must come back as NEW, and the finding must name BOTH
// numbers so the report explains itself without a trip to the baseline file.
func TestDiff_GrowthPastRecordedIsNew(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {Reason: "known", Accepted: []string{"a"}, Measured: map[string]int64{"a": 100}},
	}}

	added, stale := base.Diff([]Finding{m("d", "a", 101)})

	if len(added) != 1 {
		t.Fatalf("added = %+v, want exactly the grown artifact", added)
	}
	if !strings.Contains(added[0].Detail, "100") || !strings.Contains(added[0].Detail, "101") {
		t.Fatalf("detail = %q, want BOTH the recorded and the current measurement", added[0].Detail)
	}
	if added[0].Artifact != "a" {
		t.Fatalf("artifact = %q — identity is (dimension, artifact) and annotating Detail "+
			"must never change it", added[0].Artifact)
	}
	if len(stale) != 0 {
		t.Fatalf("stale = %+v, want none: the artifact is still a finding", stale)
	}
}

// TestDiff_GrowthIsStrict pins the trigger at strict >, in BOTH directions. Exactly at
// the recorded value is silent; one byte over fires. A relaxed assertion here would
// pass under a tolerance band, which is the design that was rejected.
func TestDiff_GrowthIsStrict(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {Reason: "known", Accepted: []string{"a"}, Measured: map[string]int64{"a": 100}},
	}}

	for _, tc := range []struct {
		name    string
		measure int64
		want    int
	}{
		{"one under is silent", 99, 0},
		{"exactly at the recorded value is silent", 100, 0},
		{"one over fires", 101, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			added, _ := base.Diff([]Finding{m("d", "a", tc.measure)})
			if len(added) != tc.want {
				t.Fatalf("added = %d, want exactly %d", len(added), tc.want)
			}
		})
	}
}

// TestDiff_AcceptedWithNoRecordedMeasurementIsNew — the convergence arm. A path
// accepted before measurements existed has nothing bounding its growth, so it is
// reported UNTIL the baseline is upgraded. There is no once-only channel and there
// must not be one.
func TestDiff_AcceptedWithNoRecordedMeasurementIsNew(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {Reason: "known", Accepted: []string{"a"}},
	}}

	added, _ := base.Diff([]Finding{m("d", "a", 500)})
	if len(added) != 1 {
		t.Fatalf("added = %+v, want the unrecorded artifact reported", added)
	}
	if !strings.Contains(added[0].Detail, "500") {
		t.Fatalf("detail = %q, want the current measurement named", added[0].Detail)
	}

	// Reported UNTIL upgraded: a second identical run reports it again.
	again, _ := base.Diff([]Finding{m("d", "a", 500)})
	if len(again) != 1 {
		t.Fatal("second run went silent — Diff has no once-only channel and must not grow one")
	}
}

// 🔴 TestDiff_CategoricalAcceptedArtifactStaysSilent is the day-one guard, and it is
// the positive control's mirror: WITHOUT the Measure == 0 arm in classify, every
// accepted artifact in every categorical dimension becomes "measurement unrecorded"
// the moment this lands, and the entire existing baseline reports as NEW at once.
//
// Delete that arm and this test goes red. That is the whole point of it.
func TestDiff_CategoricalAcceptedArtifactStaysSilent(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"categorical": {Reason: "known", Accepted: []string{"a", "b", "c"}},
	}}

	// f() leaves Measure zero — the shape every non-measuring dimension emits.
	added, stale := base.Diff([]Finding{f("categorical", "a"), f("categorical", "b"), f("categorical", "c")})

	if len(added) != 0 {
		t.Fatalf("added = %+v, want NONE: a categorical dimension has no magnitude that "+
			"could have drifted, and reporting these turns the whole baseline red on day one", added)
	}
	if len(stale) != 0 {
		t.Fatalf("stale = %+v, want none", stale)
	}
}

// TestDiff_ExceptAcceptedArtifactIsSubjectToGrowth — Except short-circuits the PATH
// test and returns before Accepted is consulted, so putting the magnitude test only
// behind the Accepted arm would leave every Except-accepted artifact unbounded while
// the acceptance criteria read as satisfied.
func TestDiff_ExceptAcceptedArtifactIsSubjectToGrowth(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {
			Reason:   "dimension reason",
			Except:   map[string]string{"a": "its own reason"},
			Measured: map[string]int64{"a": 100},
		},
	}}

	added, _ := base.Diff([]Finding{m("d", "a", 150)})
	if len(added) != 1 {
		t.Fatalf("added = %+v, want the grown Except-accepted artifact — Except is not a "+
			"bypass for the magnitude test", added)
	}

	// And an Except artifact within its recorded value is still silent.
	quiet, _ := base.Diff([]Finding{m("d", "a", 100)})
	if len(quiet) != 0 {
		t.Fatalf("added = %+v, want none", quiet)
	}
}

// TestRegenerate_DoesNotRaiseARetainedMeasurement is the guard that makes the rest
// work. --accept is WHOLE-REPORT, so a measurement sourced from the finding in hand
// would be re-recorded every time an operator accepts anything at all, in any
// dimension, and the ratchet would be erased by the command that maintains it.
func TestRegenerate_DoesNotRaiseARetainedMeasurement(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {Reason: "known", Accepted: []string{"a"}, Measured: map[string]int64{"a": 100}},
	}}

	out := base.Regenerate([]Finding{m("d", "a", 999)}, nil)

	if got := out.Dimensions["d"].Measured["a"]; got != 100 {
		t.Fatalf("measured = %d, want exactly 100 — an implicit accept must NEVER raise a "+
			"recorded measurement, or the guard is erased as a side effect of an unrelated accept", got)
	}
}

// TestRegenerate_LowersARetainedMeasurement — a recorded measurement may only SHRINK,
// which is the invariant --accept already advertises for the baseline as a whole.
// Merely carrying the prior value forward unchanged would let an artifact that shrank
// while still over cap keep its higher figure and then grow back up to it in silence:
// this task's own defect, in miniature.
func TestRegenerate_LowersARetainedMeasurement(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {Reason: "known", Accepted: []string{"a"}, Measured: map[string]int64{"a": 100}},
	}}

	out := base.Regenerate([]Finding{m("d", "a", 60)}, nil)

	if got := out.Dimensions["d"].Measured["a"]; got != 60 {
		t.Fatalf("measured = %d, want exactly 60 — the ratchet may only turn toward shrinkage", got)
	}
}

// TestRegenerate_RecordsAMeasurementForAnArtifactThatHasNone — the convergence half.
// A legacy entry picks up its measurement on the next accept, so the baseline moves
// onto the richer form instead of grandfathering the old one forever.
func TestRegenerate_RecordsAMeasurementForAnArtifactThatHasNone(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {Reason: "known", Accepted: []string{"a"}},
	}}

	out := base.Regenerate([]Finding{m("d", "a", 700)}, nil)

	if got := out.Dimensions["d"].Measured["a"]; got != 700 {
		t.Fatalf("measured = %d, want 700 recorded for an artifact that had none", got)
	}
}

// TestRegenerate_RaisesOnlyForTheRaiseSet — --raise is scoped to THIS run's NEW set,
// never to the whole accepted backlog. Two grown artifacts, one named in the raise
// set: exactly one moves.
func TestRegenerate_RaisesOnlyForTheRaiseSet(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {
			Reason:   "known",
			Accepted: []string{"raised", "untouched"},
			Measured: map[string]int64{"raised": 100, "untouched": 100},
		},
	}}

	findings := []Finding{m("d", "raised", 500), m("d", "untouched", 500)}
	out := base.Regenerate(findings, []Finding{m("d", "raised", 500)})

	if got := out.Dimensions["d"].Measured["raised"]; got != 500 {
		t.Fatalf("raised = %d, want 500", got)
	}
	if got := out.Dimensions["d"].Measured["untouched"]; got != 100 {
		t.Fatalf("untouched = %d, want 100 — --raise covers the NEW set and nothing else", got)
	}
}

// TestRegenerate_DropsTheMeasurementOfAFixedArtifact — pruning, which the design
// DEPENDS on rather than implements: Regenerate is driven entirely by the findings in
// hand, so an artifact that stopped being a finding loses its Accepted entry and its
// measurement together. Without this the map accumulates keys for artifacts that no
// longer exist, in the one file whose entire purpose is to be honest.
func TestRegenerate_DropsTheMeasurementOfAFixedArtifact(t *testing.T) {
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {
			Reason:   "known",
			Accepted: []string{"survivor", "fixed"},
			Measured: map[string]int64{"survivor": 100, "fixed": 100},
		},
	}}

	out := base.Regenerate([]Finding{m("d", "survivor", 90)}, nil)

	got := out.Dimensions["d"].Measured
	if len(got) != 1 {
		t.Fatalf("measured = %v, want exactly one entry — the fixed artifact's measurement "+
			"is stale debt and must not survive", got)
	}
	if _, still := got["fixed"]; still {
		t.Fatal("a repaired artifact kept its recorded measurement")
	}
}

// 🔴 TestSave_RoundTripsMeasuredThroughDisk — Save rebuilds a keyed literal instead of
// copying the struct, so a field missing from that literal is dropped on every write
// while working perfectly in memory. With `omitempty` the key vanishes entirely rather
// than appearing as null, so nothing downstream notices. Only a disk round-trip sees it.
func TestSave_RoundTripsMeasuredThroughDisk(t *testing.T) {
	p := filepath.Join(t.TempDir(), "baseline.json")
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {Reason: "known", Accepted: []string{"a"}, Measured: map[string]int64{"a": 12345}},
	}}

	// "" is the correct vault root: a bare temp file is not a vault path.
	if err := base.Save("", p); err != nil {
		t.Fatal(err)
	}
	round, err := LoadBaseline(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := round.Dimensions["d"].Measured["a"]; got != 12345 {
		t.Fatalf("measured = %d after a save/load round-trip, want 12345 — Save's literal "+
			"names its fields, and a field it omits is lost on disk with no error", got)
	}
}

// TestSave_LegacyShapeSurvivesForACategoricalDimension — an older binary must still
// parse what this one writes, so a dimension with no measurements must not sprout a
// `measured` key at all.
func TestSave_LegacyShapeSurvivesForACategoricalDimension(t *testing.T) {
	p := filepath.Join(t.TempDir(), "baseline.json")
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		"d": {
			Reason:   "known",
			Accepted: []string{"a"},
			// An Except entry is present so the probe below tests a POPULATED map
			// rather than an absent key: an `except` that silently stopped being an
			// object of strings would round-trip through an empty one unnoticed.
			Except: map[string]string{"b": "its own reason"},
		},
	}}
	if err := base.Save("", p); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "measured") {
		t.Fatalf("a categorical dimension emitted a `measured` key:\n%s", raw)
	}

	// And the on-disk shape of BOTH legacy fields is unchanged: `accepted` an array
	// of strings, `except` an object of strings. The probe names them both, because
	// the sidecar was chosen over a widened element type precisely so a binary that
	// predates `measured` keeps parsing this file — and a claim about what an older
	// binary can parse is only worth as much as the fields the probe actually
	// declares. `except` was in the comment and not in the struct.
	var probe struct {
		Dimensions map[string]struct {
			Accepted []string          `json:"accepted"`
			Except   map[string]string `json:"except"`
		} `json:"dimensions"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("a binary that predates this field could not parse the file: %v", err)
	}
	if len(probe.Dimensions["d"].Accepted) != 1 || probe.Dimensions["d"].Accepted[0] != "a" {
		t.Fatalf("accepted = %v, want the unchanged array-of-strings shape", probe.Dimensions["d"].Accepted)
	}
	if got := probe.Dimensions["d"].Except; len(got) != 1 || got["b"] != "its own reason" {
		t.Fatalf("except = %v, want the unchanged object-of-strings shape — widening this "+
			"field is what the Measured sidecar exists to avoid", got)
	}
}

// TestLoadBaseline_IgnoresAnUnknownKey is the READ half of the version skew story,
// stated as a test so the claim is not just prose: an unknown key is ignored rather
// than fatal.
//
// THE WRITE HALF IS NOT SAFE, AND ITS RULING IS NOT HERE. An older binary's
// `--accept` runs Regenerate + Save and deletes the whole `measured` block with no
// error, because Baseline carries no version field that could detect it. That is
// ruled by the surface counter, not by this package and not by a debt note: the
// 2 -> 3 bump in internal/surface/version.go names `Audits/baseline.json GAINED
// measured (b621a18)` as one of its five reasons, so surfaceGate's EnforceFailStop
// refuses a v2 binary before it can reach the file at all.
//
// That is STRONGER than accepting the loss as debt, and it is also LOAD-BEARING
// ELSEWHERE, so nothing in this package may be written as if it owned the remedy:
// no test here ties this block to that gate, and the honest description is
// satisfied-by-a-different-remedy rather than clean.
func TestLoadBaseline_IgnoresAnUnknownKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "baseline.json")
	body := `{"dimensions":{"d":{"reason":"r","accepted":["a"],"measured":{"a":5},"future":{"x":1}}}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadBaseline(p)
	if err != nil {
		t.Fatalf("an unknown key was treated as fatal: %v", err)
	}
	if got.Dimensions["d"].Measured["a"] != 5 {
		t.Fatal("measured did not survive the load")
	}
}

// 🔴 TestRun_GrownAcceptedArtifactIsReportedExactlyOnce is the C1 pin, and the reason
// the predicate was widened rather than duplicated.
//
// accepts() is called from TWO places — Diff, and newDimensionResult's independent
// recomputation of the accepted set that backs Report.Findings(). Teach only Diff
// about growth and the artifact is reported NEW *and* counted as accepted in the same
// run, landing twice in Findings() and doubling the report's counts.
//
// This drives the real Run against a fixture vault and a fixture baseline on disk,
// because that double-count is invisible to any test that calls Diff alone.
func TestRun_GrownAcceptedArtifactIsReportedExactlyOnce(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	// A coherent fixture: a linked manifest and its drawer, so the OTHER dimensions
	// have nothing to say and this test fails only for its own reason.
	note := seedNote(t, vault, "alpha", "2026-07-14-good.md")
	seedManifest(t, vault, "alpha", "aaaa", note)

	rel := "Projects/alpha/resume.md"
	writeFile(t, vault.Root, rel, strings.Repeat("x", check.ResumeMaxBytes+500))

	// Accepted at a SMALLER size than it now measures: the growth case.
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		DimResumeDiscipline: {
			Reason:   "accepted debt",
			Accepted: []string{rel},
			Measured: map[string]int64{rel: int64(check.ResumeMaxBytes + 1)},
		},
	}}
	if err := base.Save(vault.Root, filepath.Join(vault.Root, filepath.FromSlash(BaselineRelPath))); err != nil {
		t.Fatal(err)
	}

	report, err := Run(vault)
	if err != nil {
		t.Fatal(err)
	}

	var dim DimensionResult
	for _, d := range report.Dimensions {
		if d.Name == DimResumeDiscipline {
			dim = d
		}
	}

	if len(dim.New) != 1 || dim.New[0].Artifact != rel {
		t.Fatalf("New = %+v, want exactly the grown resume", dim.New)
	}
	// Exact equality in BOTH directions: it must be reported, and it must NOT also be
	// counted as accepted. `>= 1` would pass under the double-count this pins.
	if dim.Accepted != 0 {
		t.Fatalf("Accepted = %d, want exactly 0 — a grown artifact is NOT accepted, and "+
			"counting it in both places is the double-count this test exists for", dim.Accepted)
	}
	if dim.Status != StatusFail {
		t.Fatalf("status = %q, want fail", dim.Status)
	}

	seen := 0
	for _, f := range report.Findings() {
		if f.Artifact == rel {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("the grown artifact appears %d times in Report.Findings(), want exactly 1 — "+
			"Findings() concatenates New and the accepted set, so a stale second predicate "+
			"shows up here as a duplicate", seen)
	}
}

// TestRun_AcceptedResumeWithinItsRecordedMeasurementIsSilent is the positive control's
// counterpart: the same fixture, accepted at its true size, must pass. Without it the
// test above could be satisfied by a predicate that reports EVERYTHING.
func TestRun_AcceptedResumeWithinItsRecordedMeasurementIsSilent(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	note := seedNote(t, vault, "alpha", "2026-07-14-good.md")
	seedManifest(t, vault, "alpha", "aaaa", note)

	rel := "Projects/alpha/resume.md"
	size := check.ResumeMaxBytes + 500
	writeFile(t, vault.Root, rel, strings.Repeat("x", size))

	base := Baseline{Dimensions: map[string]DimensionBaseline{
		DimResumeDiscipline: {
			Reason:   "accepted debt",
			Accepted: []string{rel},
			Measured: map[string]int64{rel: int64(size)},
		},
	}}
	if err := base.Save(vault.Root, filepath.Join(vault.Root, filepath.FromSlash(BaselineRelPath))); err != nil {
		t.Fatal(err)
	}

	report, err := Run(vault)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range report.Dimensions {
		if d.Name != DimResumeDiscipline {
			continue
		}
		if len(d.New) != 0 {
			t.Fatalf("New = %+v, want none: it measures exactly what was accepted", d.New)
		}
		if d.Accepted != 1 {
			t.Fatalf("Accepted = %d, want exactly 1 — passing is not the same as clean", d.Accepted)
		}
		if d.Status != StatusPass {
			t.Fatalf("status = %q, want pass", d.Status)
		}
	}
}

// resumeDimension runs the real audit and returns the resume-discipline row.
//
// It FATALS on an absent row rather than returning a zero value. The idiom of
// ranging the report and `continue`-ing past every other name passes silently when
// the row it was looking for is not there at all — a test that asserts nothing while
// reporting success, which is the failure this package is named after.
func resumeDimension(t *testing.T, vault *storage.Vault) (Report, DimensionResult) {
	t.Helper()
	report, err := Run(vault)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range report.Dimensions {
		if d.Name == DimResumeDiscipline {
			return report, d
		}
	}
	t.Fatalf("no %s row in the report — Run emits a row per registry entry, so an "+
		"absent one means the dimension left the registry", DimResumeDiscipline)
	return Report{}, DimensionResult{}
}

// 🔴 TestRun_MeasuredAcceptedArtifactGoesStaleWhenItDropsUnderCap is the STALE half of
// the ratchet for a MEASURING dimension, and it had no coverage.
//
// TestRun_FixingTheBugForcesTheBaselineToShrink pins the same property, but it drives
// archive-round-trip: a CATEGORICAL dimension, where an artifact stops being a finding
// because a manifest gained a back-link. That is the wrong dimension for this claim.
// A measured artifact stops being a finding by CROSSING A THRESHOLD, and the code that
// decides it — classify's magnitude arms, and Regenerate's per-artifact Measured map —
// does not exist on the categorical path at all. Nothing proved the two agree.
//
// Driven through the real Run against an on-disk fixture baseline, because the stale
// sweep lives in Diff while the per-dimension filter that decides which stale entries
// belong to this row lives in newDimensionResult; a Diff-only test sees only half of it.
func TestRun_MeasuredAcceptedArtifactGoesStaleWhenItDropsUnderCap(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	note := seedNote(t, vault, "alpha", "2026-07-14-good.md")
	seedManifest(t, vault, "alpha", "aaaa", note)

	rel := "Projects/alpha/resume.md"
	size := check.ResumeMaxBytes + 500
	writeFile(t, vault.Root, rel, strings.Repeat("x", size))

	base := Baseline{Dimensions: map[string]DimensionBaseline{
		DimResumeDiscipline: {
			Reason:   "accepted debt",
			Accepted: []string{rel},
			Measured: map[string]int64{rel: int64(size)},
		},
	}}
	if err := base.Save(vault.Root, filepath.Join(vault.Root, filepath.FromSlash(BaselineRelPath))); err != nil {
		t.Fatal(err)
	}

	// Precondition, asserted rather than assumed: at exactly its recorded measurement
	// the artifact is accepted and silent. Without this the test below could be
	// satisfied by a fixture that was never accepted in the first place.
	//
	// It earns more than that, as mutation testing showed: relaxing classify's trigger
	// from `>` to `>=` reds this block, because the artifact then fails at exactly the
	// figure it was accepted at. The precondition is a second witness to the strictness
	// TestDiff_GrowthIsStrict pins, reached through the real Run rather than Diff alone.
	if _, d := resumeDimension(t, vault); d.Status != StatusPass || d.Accepted != 1 || len(d.Stale) != 0 {
		t.Fatalf("precondition: status=%q accepted=%d stale=%+v, want a silently accepted resume",
			d.Status, d.Accepted, d.Stale)
	}

	// THE PRUNE LANDS. The resume drops under the cap and stops being a finding.
	writeFile(t, vault.Root, rel, strings.Repeat("x", check.ResumeMaxBytes-1))

	_, d := resumeDimension(t, vault)

	if len(d.Stale) != 1 || d.Stale[0].Artifact != rel {
		t.Fatalf("stale = %+v, want the pruned resume — a measured artifact that came back "+
			"under its cap is FIXED, and the audit must demand the baseline shrink or the "+
			"record rots into a lie", d.Stale)
	}
	if d.Stale[0].Reason != "accepted debt" {
		t.Fatalf("reason = %q, want the dimension's own reason carried onto the stale entry",
			d.Stale[0].Reason)
	}
	if len(d.New) != 0 {
		t.Fatalf("New = %+v, want none: a repaired artifact is stale debt, never fresh drift", d.New)
	}
	if d.Accepted != 0 {
		t.Fatalf("Accepted = %d, want exactly 0 — it is no longer a finding, so it cannot be "+
			"counted as an accepted one", d.Accepted)
	}
	if d.Status != StatusFail {
		t.Fatalf("status = %q, want fail: a stale entry is a ratchet FAILURE, not a pass", d.Status)
	}
}

// 🔴 TestRun_DropAndRecrossResetsTheRecordedMeasurement composes the three legs that
// were each pinned alone, and the composition is the point: the whole cycle is where
// the measurement is lost, and no single-leg test can see that.
//
// The legs already covered separately are the drop going STALE (above), a regeneration
// with no findings dropping the dimension, and an unaccepted artifact coming back NEW.
// Run them end to end and the property that falls out is the one the task's Definition
// of Done originally got WRONG:
//
//	`--accept` never raises a RETAINED measurement. Drop below the cap once and there
//	is nothing retained to raise — the number resets to whatever the re-crossing
//	measures, however much larger that is.
//
// This is the drop-and-recross hole, and it is deliberately NOT fixed here: `Measured`
// is pruned on STALE because step 6 depends on that, while closing this hole needs a
// value that SURVIVES the prune. The two want opposite behaviour from one map, which
// is why the sibling task exists. This test pins the current, ruled behaviour so the
// hole is a recorded property with a name rather than a surprise.
//
// 🔴 WHAT THIS TEST DOES AND DOES NOT GUARD — verified by mutation, not asserted.
// Legs 1 and 2 discriminate: deleting Diff's stale sweep reds leg 1, and carrying
// every prior dimension forward through Regenerate reds leg 2. Leg 3's assertions do
// NOT, and cannot: they pin the ABSENCE of a mechanism, and there is no machinery to
// break. They are executable documentation of a ruled-in hole — which is the point of
// writing them down, and is not the same thing as a guard. Do not cite this test as
// coverage of a surviving-measurement rule; there is no such rule to cover.
func TestRun_DropAndRecrossResetsTheRecordedMeasurement(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	note := seedNote(t, vault, "alpha", "2026-07-14-good.md")
	seedManifest(t, vault, "alpha", "aaaa", note)

	rel := "Projects/alpha/resume.md"
	basePath := filepath.Join(vault.Root, filepath.FromSlash(BaselineRelPath))

	accepted := check.ResumeMaxBytes + 500
	writeFile(t, vault.Root, rel, strings.Repeat("x", accepted))
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		DimResumeDiscipline: {
			Reason:   "accepted debt",
			Accepted: []string{rel},
			Measured: map[string]int64{rel: int64(accepted)},
		},
	}}
	if err := base.Save(vault.Root, basePath); err != nil {
		t.Fatal(err)
	}

	// LEG 1 — THE DROP. The resume is pruned under its cap and goes stale.
	writeFile(t, vault.Root, rel, strings.Repeat("x", check.ResumeMaxBytes-1))
	dropped, d := resumeDimension(t, vault)
	if len(d.Stale) != 1 || d.Stale[0].Artifact != rel {
		t.Fatalf("leg 1: stale = %+v, want the pruned resume", d.Stale)
	}

	// LEG 2 — THE ACCEPT THAT CLEARS IT. Regenerate is driven entirely by the findings
	// in hand, and this dimension produced NONE, so its whole entry — reason, accepted
	// paths and recorded measurements alike — is dropped by the outer loop.
	//
	// Be precise about which rule this leg tests, because the two are easy to conflate:
	// this is the DIMENSION-LEVEL shrink rule. The per-artifact Measured prune inside
	// Regenerate's `!ok` branch never runs here at all — that branch needs a surviving
	// finding to iterate on — and it is guarded by
	// TestRegenerate_DropsTheMeasurementOfAFixedArtifact, which mutation testing shows
	// is its ONLY guard.
	regen := base.Regenerate(dropped.Findings(), nil)
	if _, still := regen.Dimensions[DimResumeDiscipline]; still {
		t.Fatalf("leg 2: the dimension survived a regeneration with no findings: %+v",
			regen.Dimensions[DimResumeDiscipline])
	}
	if err := regen.Save(vault.Root, basePath); err != nil {
		t.Fatal(err)
	}

	// LEG 3 — THE RE-CROSSING, at a size LARGER than was ever accepted.
	recross := accepted + 5000
	writeFile(t, vault.Root, rel, strings.Repeat("x", recross))
	recrossed, d := resumeDimension(t, vault)

	if len(d.New) != 1 || d.New[0].Artifact != rel {
		t.Fatalf("leg 3: New = %+v, want the re-crossed resume reported", d.New)
	}
	if len(d.Stale) != 0 {
		t.Fatalf("leg 3: stale = %+v, want none — the entry was cleared in leg 2", d.Stale)
	}
	// It comes back as FRESH DRIFT, not as growth. The growth arm cannot fire: it needs
	// a recorded measurement, and the drop took it. Asserting the absence of the growth
	// wording is what distinguishes the two paths, which are otherwise both "1 NEW".
	if strings.Contains(d.New[0].Detail, "GREW PAST") {
		t.Fatalf("detail = %q, want the plain over-cap finding: the recorded measurement did "+
			"not survive the drop, so there is no prior figure to have grown past", d.New[0].Detail)
	}
	if strings.Contains(d.New[0].Detail, "ACCEPTED WITH NO RECORDED MEASUREMENT") {
		t.Fatalf("detail = %q, want the plain over-cap finding: the path is not accepted at "+
			"all any more, so the unrecorded-measurement arm must not fire either", d.New[0].Detail)
	}

	// AND THE CONSEQUENCE. A plain --accept now nails the floor at the NEW, HIGHER
	// number. Nothing remembers the smaller figure this artifact was once accepted at.
	rearmed := regen.Regenerate(recrossed.Findings(), nil)
	if got := rearmed.Dimensions[DimResumeDiscipline].Measured[rel]; got != int64(recross) {
		t.Fatalf("re-accepted at %d, want %d — a drop below the cap RESETS the recorded "+
			"measurement, so the ratchet's floor is whatever the re-crossing measures", got, recross)
	}
	if got := rearmed.Dimensions[DimResumeDiscipline].Measured[rel]; got <= int64(accepted) {
		t.Fatalf("re-accepted at %d, which is not above the %d it was originally accepted at — "+
			"this test is meant to demonstrate the floor RISING, and it did not", got, accepted)
	}
}
