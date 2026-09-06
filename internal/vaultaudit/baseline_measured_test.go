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
		"d": {Reason: "known", Accepted: []string{"a"}},
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

	// And the on-disk shape of accepted/except is unchanged: array of strings.
	var probe struct {
		Dimensions map[string]struct {
			Accepted []string `json:"accepted"`
		} `json:"dimensions"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("a binary that predates this field could not parse the file: %v", err)
	}
	if len(probe.Dimensions["d"].Accepted) != 1 || probe.Dimensions["d"].Accepted[0] != "a" {
		t.Fatalf("accepted = %v, want the unchanged array-of-strings shape", probe.Dimensions["d"].Accepted)
	}
}

// TestLoadBaseline_IgnoresAMeasuredKeyItDoesNotKnow is the READ half of the version
// skew story, stated as a test so the claim is not just prose: an unknown key is
// ignored rather than fatal. The WRITE half is not safe and is recorded as known debt
// in the task — an older binary's --accept deletes the block outright.
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
