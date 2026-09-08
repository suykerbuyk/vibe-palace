// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package vaultaudit is the ratchet behind the vault audit: it compares what the
// vault ACTUALLY contains against what the design says it should, and records the
// gap so the next run reports NEW drift instead of ancient drift.
//
// # Why there is no date watermark
//
// The plan this package implements called for one: a cutoff date per dimension,
// with older artifacts reported `unknown` rather than `fail`. The live vault
// disproved it. `note_path` first appears in 2026-07-12-12a23ab8-21.md, while
// 2026-07-12-09.md — written the SAME DAY — lacks it; and note IDs sort by
// date-fingerprint-NN, so a second host writing later in the day sorts EARLIER.
// There is no sound boundary to cut on. Worse, `archive:` dates back to
// 2026-04-16: it is not a young field forgivably absent from old notes, it is an
// OLD field the linker has been failing to populate the whole time. A watermark
// would have framed a live bug as accepted history.
//
// So the accepted set is EXPLICIT — every accepted artifact is named — with the
// reason hoisted to the dimension, because the ~269 notes that predate a field
// genuinely do share one reason and 269 copies of it would be noise, not triage.
//
// # The ratchet
//
// Two failures, and the second is the point:
//
//   - NEW: a finding not in the accepted set. Fresh drift.
//   - STALE: an accepted entry that is NO LONGER a finding. Fixed, still recorded.
//
// Without STALE, the baseline rots into a lie — you fix something, the list keeps
// claiming it is broken, and the list stops meaning anything. With it, THE ACCEPTED
// SET CAN ONLY SHRINK. This is what makes fixing a bug mechanically compel the record
// to be corrected: when the archive-link backfill lands, the accepted entries stop
// being findings and the audit demands their removal.
//
// "The accepted set", not "the file". One field is deliberately exempt, and reading
// the rule as covering the whole file will make that field look like a bug:
// DimensionBaseline.PriorAccepted retains the IDENTITY of what a dimension has
// accepted before, and survives the drop that prunes everything else. Without it an
// artifact that crosses, is accepted, is repaired, and crosses again is indistinguish-
// able from one crossing for the first time — so `--accept` re-arms the ratchet at the
// new, higher figure in silence. Nothing about what was ACCEPTED survives; only that
// it once was.
//
// # Advisory, never blocking
//
// Unlike internal/sourceaudit — a source gate, where failing the build is right —
// the vault audit REPORTS and never blocks (operator decision, iteration 204: a
// blocking auditor gets disabled the first time it is wrong at a bad moment, and a
// disabled auditor audits nothing). "NEW finding" and "STALE entry" are loud lines
// in a report, not a non-zero exit from `make test`. The operator's vault contents
// are not a build input.
package vaultaudit

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
)

// Finding is one artifact failing one dimension.
//
// Artifact is a STABLE, VAULT-RELATIVE identifier — a path, never an absolute one
// and never an array index. It is the baseline's key, so an unstable ID would make
// every run report the same defect as both NEW and STALE forever.
type Finding struct {
	Dimension string
	Artifact  string
	Detail    string

	// Measure is the magnitude that makes this artifact wrong, for a dimension whose
	// predicate IS a magnitude. ZERO means "this dimension does not measure" — the
	// artifact identifier fully determines the defect, and there is no number that
	// could have drifted. Most dimensions are categorical and leave it zero.
	//
	// It carries no JSON tag because Finding is never serialized; the baseline records
	// the accepted magnitude separately, in DimensionBaseline.Measured.
	Measure int64
}

// DimensionBaseline is the accepted debt for one dimension: the shared reason, and
// the artifacts it covers.
//
// Reason is per-DIMENSION, not per-artifact, because that is where the reason
// actually lives — "these notes predate the field" is one fact about 269 notes, not
// 269 facts. Except carries the genuine one-offs: an artifact whose acceptance has a
// DIFFERENT reason than its dimension's. An artifact in Except need not also appear
// in Accepted; both are accepted.
type DimensionBaseline struct {
	Reason   string            `json:"reason"`
	Accepted []string          `json:"accepted"`
	Except   map[string]string `json:"except,omitempty"`

	// Measured is the magnitude each accepted artifact was accepted AT, for a
	// dimension that measures. It is a SIDECAR keyed by artifact rather than a wider
	// element type on Accepted, so `accepted` stays an array of strings and `except`
	// an object of strings: a binary that predates this field parses the file and
	// ignores the key, where a widened element type would make LoadBaseline hard-error.
	//
	// An artifact with no entry here is accepted at an UNRECORDED magnitude, which is
	// a finding rather than a blanket pass — see classify.
	Measured map[string]int64 `json:"measured,omitempty"`

	// PriorAccepted names every artifact this dimension has EVER accepted, and it is
	// the ONE part of a baseline entry that SURVIVES an entry going STALE.
	//
	// Everything else here is shrink-only: an artifact that stops being a finding loses
	// its Accepted entry and its Measured magnitude together, because a baseline that
	// keeps claiming a fixed thing is broken stops meaning anything. That rule has a
	// hole. An artifact that crosses, is accepted, is repaired, drops out, and later
	// crosses AGAIN comes back as a plain NEW finding with no memory that it was ever
	// accepted — so a plain `--accept` re-arms the ratchet at the new, higher figure,
	// silently, and nothing records that this is the second or the third crossing.
	// Retaining the IDENTITY across the drop is what closes it: `vp audit vault
	// --accept` refuses an artifact named here and demands an explicit `--reaccept`.
	// The refusal is the remedy — see Reacceptances.
	//
	// 🔴 IT IS AN IDENTITY SET: NOT A COUNTER, AND NOT A SURVIVING HIGH-WATER MARK.
	// Both were evaluated and rejected (2026-09-07). A counter bakes "stop at the third
	// crossing" — an operator policy that has already varied once — into the file
	// format, where a number that should stay a human instruction cannot be varied per
	// artifact. A surviving magnitude answers "has this regressed past where we once
	// accepted it", which is not the question the escalation rule asks. Do not widen
	// this field to either shape.
	//
	// 🔴 DO NOT REPURPOSE Measured FOR THIS, AND DO NOT MAKE Measured SURVIVE STALE.
	// Measured's pruning IS the shrink-only ratchet the sibling change rests on. One
	// map cannot both survive a drop and be pruned by one; that is why this is a second
	// structure rather than a wider first one.
	//
	// A record is only ever ADDED for a magnitude-bearing finding in a dimension named
	// by dimensionsRecordingPriorAcceptance — today resume-discipline alone. Be precise
	// about what that gate does and does not cover: an entry that already exists is
	// carried, saved and enforced for ANY dimension, so a set hand-written into the
	// file works. The gate stops this file GROWING one, not the rest of the code
	// honouring one. A never-pruned set added on every categorical dimension would be a
	// permanently growing suppression record that no rule reads.
	//
	// 🔴 IT IS NEVER PRUNED, BY ANY PATH, AND THAT IS THE DESIGN. The consequence is
	// worth stating rather than discovering: an artifact whose project is deleted keeps
	// its entry in the committed file forever, and if that slug is ever REUSED the new
	// project's genuine first crossing is refused as a re-crossing. That is a visible,
	// one-command false positive (`--reaccept`) against a silent false negative
	// (forgetting a real crossing), and this task exists because the silent one is
	// worse. Do not add a pruning path to fix the cosmetics.
	PriorAccepted []string `json:"prior_accepted,omitempty"`
}

// dimensionsRecordingPriorAcceptance names the dimensions whose baseline retains
// DimensionBaseline.PriorAccepted across a drop.
//
// It is a POLICY and is deliberately NOT derived from the registry: every dimension is
// audited, and only a dimension whose predicate is a pure MAGNITUDE can drop below its
// threshold and come back. resume-discipline is the only one today, and designing for
// "every dimension that might one day measure" would be designing for no instance at
// all. Add a key here when a second such dimension actually exists.
//
// The keys are pinned against the registry by a test, so this cannot drift into naming
// a dimension that no longer runs.
var dimensionsRecordingPriorAcceptance = map[string]bool{
	DimResumeDiscipline: true,
}

// recordsPriorAcceptance reports whether accepting a finding in this dimension should
// leave a record that survives the artifact later dropping out.
func recordsPriorAcceptance(dimension string) bool {
	return dimensionsRecordingPriorAcceptance[dimension]
}

// acceptance is how a dimension's baseline covers one finding.
type acceptance int

const (
	acceptedFully         acceptance = iota // covered by path, and within its recorded magnitude
	notAccepted                             // in neither Accepted nor Except
	measurementUnrecorded                   // accepted by path, but nothing bounds its magnitude
	grownPastRecord                         // accepted by path, and now larger than what was accepted
)

// classify decides how this dimension's baseline covers a finding.
//
// There is ONE predicate here on purpose. Diff takes the complement of accepts() and
// newDimensionResult takes accepts() itself, so the two are exact complements by
// construction. A second predicate beside this one is precisely how a grown artifact
// ends up reported as NEW *and* counted as accepted in the same run, appearing twice
// in Report.Findings() and doubling the report's counts.
//
// Except is consulted FIRST and short-circuits the path test, because an artifact in
// Except need not also appear in Accepted. The magnitude test then applies to BOTH
// arms: Except carries a free-text reason with no room for a number, so keying the
// measurement off the artifact instead is what stops Except being a bypass.
func (d DimensionBaseline) classify(f Finding) acceptance {
	_, excepted := d.Except[f.Artifact]
	if !excepted && !slices.Contains(d.Accepted, f.Artifact) {
		return notAccepted
	}
	// Accepted by path. A dimension that reports no magnitude is fully covered there:
	// Measure == 0 means "this dimension does not measure", and a categorical artifact
	// has no number that could have drifted. WITHOUT this arm, every accepted artifact
	// in every categorical dimension becomes measurementUnrecorded the day this lands
	// — the whole baseline turns red at once.
	if f.Measure == 0 {
		return acceptedFully
	}
	recorded, ok := d.Measured[f.Artifact]
	if !ok {
		return measurementUnrecorded
	}
	if f.Measure > recorded {
		return grownPastRecord
	}
	return acceptedFully
}

// accepts reports whether this dimension's baseline covers the finding — by path AND,
// where the dimension measures, at no more than the magnitude that was accepted.
func (d DimensionBaseline) accepts(f Finding) bool {
	return d.classify(f) == acceptedFully
}

// reaccepting reports whether this finding is a RE-CROSSING: not accepted by the
// baseline as it stands, but named in the record of what this dimension has accepted
// before.
//
// It is deliberately built on classify rather than beside it. notAccepted is the only
// arm that can be a re-crossing — grownPastRecord and measurementUnrecorded both mean
// the path is accepted RIGHT NOW, which is the continuous-acceptance case `--raise`
// answers, and acceptedFully is not a finding to refuse at all.
func (d DimensionBaseline) reaccepting(f Finding) bool {
	return d.classify(f) == notAccepted && slices.Contains(d.PriorAccepted, f.Artifact)
}

// Reacceptances returns every finding that `vp audit vault --accept` must REFUSE
// unless the operator also passes --reaccept: an artifact that has been accepted
// before, dropped out, and is a finding again.
//
// It is the ONE predicate behind both readers — the CLI's refusal and Diff's
// annotation on the report — for the same reason classify is the one predicate behind
// Diff and newDimensionResult: a second copy of this rule is how the report ends up
// promising something the command then does not do.
func (b Baseline) Reacceptances(findings []Finding) []Finding {
	var out []Finding
	for _, f := range findings {
		if b.Dimensions[f.Dimension].reaccepting(f) {
			out = append(out, f)
		}
	}
	slices.SortFunc(out, func(x, y Finding) int { return compare2(x.Dimension, x.Artifact, y.Dimension, y.Artifact) })
	return out
}

// all returns every artifact this baseline accepts, from both Accepted and Except.
func (d DimensionBaseline) all() []string {
	out := slices.Clone(d.Accepted)
	for a := range d.Except {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}

// Baseline is the accepted-findings record for the whole vault, keyed by dimension.
type Baseline struct {
	Dimensions map[string]DimensionBaseline `json:"dimensions"`
}

// StaleEntry is an accepted artifact that is no longer a finding. It is a FAILURE
// of the ratchet, not a success: the fix landed and nobody updated the record.
type StaleEntry struct {
	Dimension string
	Artifact  string
	Reason    string
}

// ReasonUntriaged marks a dimension nobody has explained yet. Deliberately ugly: a
// baseline wearing it is claiming nothing, and must read as unfinished work.
//
// internal/sourceaudit learned this the hard way — its first baseline stamped an
// equivalent marker over all 60 entries, and it took a full session of triage to
// climb back out. An untriaged entry must never be able to masquerade as an
// explained one.
const ReasonUntriaged = "UNTRIAGED: accepted at baseline creation — explain or fix"

// LoadBaseline reads a baseline. A missing file is an EMPTY baseline, not an error:
// a vault with no accepted debt is a legitimate state, and the audit should hold it
// to a clean bill of health rather than silently pass.
//
// A file that EXISTS but cannot be read or parsed IS an error. "I could not look" is
// not "there was nothing there" — the invariant this whole epic is built on.
func LoadBaseline(path string) (Baseline, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Baseline{Dimensions: map[string]DimensionBaseline{}}, nil
	}
	if err != nil {
		return Baseline{}, fmt.Errorf("read baseline %s: %w", path, err)
	}
	var b Baseline
	if err := json.Unmarshal(data, &b); err != nil {
		return Baseline{}, fmt.Errorf("parse baseline %s: %w", path, err)
	}
	if b.Dimensions == nil {
		b.Dimensions = map[string]DimensionBaseline{}
	}
	return b, nil
}

// Save writes the baseline sorted and indented, so it DIFFS CLEANLY in review.
// A report that reorders its own rows between runs is not diffable, and
// week-over-week drift is the entire point of committing this file.
//
// vaultRoot is the vault the baseline belongs to; it reaches atomicfile.Write,
// which stamps the surface version for vaultRoot/path. Pass "" only for a
// genuinely non-vault destination (a bare temp file in a test) — a "" for a
// real vault path succeeds while silently skipping the stamp.
func (b Baseline) Save(vaultRoot, path string) error {
	out := Baseline{Dimensions: make(map[string]DimensionBaseline, len(b.Dimensions))}
	for name, d := range b.Dimensions {
		accepted := slices.Clone(d.Accepted)
		slices.Sort(accepted)
		accepted = slices.Compact(accepted)
		// Sorted and compacted on the SAME terms as Accepted, and for the same reason:
		// this file is diffed week over week, and a set whose order came from map
		// iteration would churn the diff on every run while meaning the same thing.
		// slices.Clone returns nil for nil, so a dimension that records nothing keeps
		// its legacy on-disk shape and `omitempty` drops the key entirely.
		priorAccepted := slices.Clone(d.PriorAccepted)
		slices.Sort(priorAccepted)
		priorAccepted = slices.Compact(priorAccepted)
		out.Dimensions[name] = DimensionBaseline{
			Reason:        d.Reason,
			Accepted:      accepted,
			Except:        d.Except,
			Measured:      d.Measured,
			PriorAccepted: priorAccepted,
		}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	// The first baseline is written into an Audits/ directory that does not exist
	// yet on any vault that has never been audited — which is every vault, once.
	//
	// atomicfile.Write would MkdirAll this itself, so the call is redundant
	// rather than load-bearing. It stays: routing directory creation into a
	// vault-aware primitive (the old F3 family) is OFF the plan permanently, not
	// deferred, and deleting the line here would be the first step of exactly
	// that work.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create baseline dir: %w", err)
	}
	// Audits/baseline.json is committed, diffed and reviewed week over week, so
	// it gets the atomic replace and the stamp rather than a raw os.WriteFile.
	// The bytes are unchanged: MarshalIndent output plus one trailing newline.
	return atomicfile.Write(vaultRoot, path, append(data, '\n'))
}

// Diff compares findings against the baseline and returns what the audit must
// report: fresh drift, and fixed-but-still-recorded debt.
//
// STALE is the half that makes this a ratchet instead of a suppression file. See
// the package doc.
func (b Baseline) Diff(findings []Finding) (added []Finding, stale []StaleEntry) {
	// present[dimension][artifact] — what the audit found THIS run.
	present := make(map[string]map[string]bool, len(b.Dimensions))
	for _, f := range findings {
		if present[f.Dimension] == nil {
			present[f.Dimension] = map[string]bool{}
		}
		present[f.Dimension][f.Artifact] = true

		// Annotating Detail is safe and is the established idiom here: finding
		// identity is (Dimension, Artifact), so a richer Detail cannot churn the
		// accepted baseline (see archive.go's note on the same move).
		d := b.Dimensions[f.Dimension]
		switch d.classify(f) {
		case notAccepted:
			fresh := f
			if d.reaccepting(f) {
				fresh.Detail = fmt.Sprintf("%s — ACCEPTED BEFORE, DROPPED, AND BACK: this artifact "+
					"has crossed and been accepted at least once already, was repaired, and is a "+
					"finding again. `vp audit vault --accept` REFUSES it; re-accepting is deliberate, "+
					"`vp audit vault --accept --reaccept`, and the record of the earlier acceptance "+
					"survives that.", f.Detail)
			}
			added = append(added, fresh)
		case grownPastRecord:
			grown := f
			grown.Detail = fmt.Sprintf("%s — GREW PAST ITS ACCEPTED MEASUREMENT: %d recorded, %d now. "+
				"An accepted measurement may only shrink. Bring it back under the recorded value, or "+
				"re-record it deliberately with `vp audit vault --accept --raise`.",
				f.Detail, d.Measured[f.Artifact], f.Measure)
			added = append(added, grown)
		case measurementUnrecorded:
			bare := f
			bare.Detail = fmt.Sprintf("%s — ACCEPTED WITH NO RECORDED MEASUREMENT; it measures %d now. "+
				"The path was accepted before magnitudes were recorded, so nothing bounds its growth. "+
				"Re-accept to record it; this is reported until the baseline is upgraded, not once.",
				f.Detail, f.Measure)
			added = append(added, bare)
		case acceptedFully:
		}
	}

	for name, d := range b.Dimensions {
		for _, artifact := range d.all() {
			if present[name][artifact] {
				continue
			}
			reason := d.Reason
			if r, ok := d.Except[artifact]; ok {
				reason = r
			}
			stale = append(stale, StaleEntry{Dimension: name, Artifact: artifact, Reason: reason})
		}
	}

	slices.SortFunc(added, func(x, y Finding) int { return compare2(x.Dimension, x.Artifact, y.Dimension, y.Artifact) })
	slices.SortFunc(stale, func(x, y StaleEntry) int { return compare2(x.Dimension, x.Artifact, y.Dimension, y.Artifact) })
	return added, stale
}

// Regenerate rebuilds the baseline from the current findings while PRESERVING every
// dimension's reason.
//
// The reason is the entire value of a baseline entry — it is where a human recorded
// WHY this debt is accepted. Regeneration is routine (every fix forces one), so a
// regen that stamps UNTRIAGED over the reasons destroys the triage that produced
// them and the list decays back into the undifferentiated blob it started as.
// internal/sourceaudit shipped exactly that bug and had to fix it at 203.
//
// Survivors keep their dimension's reason. Fixed findings drop out — the baseline
// may only shrink. A genuinely NEW dimension arrives marked UNTRIAGED, so an
// unexplained dimension can never masquerade as an explained one.
//
// TWO THINGS ARE EXEMPT FROM THE SHRINK RULE, and both are carried by the blocks at
// the end of this function rather than by the main loop:
//
//   - A dimension whose key this binary's registry does not know
//     (Baseline.UnknownDimensions). It produced no findings because it NEVER RAN, not
//     because it was fixed, and the two are indistinguishable from the findings alone.
//     Its entry is carried through verbatim.
//   - DimensionBaseline.PriorAccepted, the record of what a dimension has accepted
//     before. It exists precisely to outlive the drop, so that an artifact returning
//     after a repair is not mistaken for a first crossing. Everything else about a
//     dropped entry — its accepted paths, its exceptions, its measurements — still
//     goes, so neither exemption softens the ratchet.
//
// The first is carried entirely by the block at the end of this function. The second
// takes TWO carries, because a drop has two shapes and only one of them is visible
// down there: an artifact repaired while ANOTHER artifact keeps its dimension alive is
// carried in the main loop's `!ok` branch, and a dimension whose every artifact was
// repaired never reaches the main loop at all and is rebuilt as a stub at the end.
// Both are needed; the second alone was the shape the first round of tests missed.
//
// The same "may only shrink" rule governs recorded measurements, and it is the whole
// point of the guard: --accept is WHOLE-REPORT, so if a measurement were sourced from
// the finding in hand, an operator accepting an unrelated finding in a different
// dimension would silently re-record every measured artifact at its current size and
// erase the ratchet — invisibly, as a side effect of a command typed for something
// else. So a surviving artifact keeps min(prior, current), and raising is explicit.
//
// raise names the findings whose measurement may go UP — the run's NEW set, threaded
// in from `--accept --raise`. It is a second parameter rather than a second method
// because a raising and a non-raising Regenerate that can drift apart is two
// definitions of one rule. Pass nil to raise nothing.
func (b Baseline) Regenerate(findings, raise []Finding) Baseline {
	raising := make(map[[2]string]bool, len(raise))
	for _, f := range raise {
		raising[[2]string{f.Dimension, f.Artifact}] = true
	}
	out := Baseline{Dimensions: map[string]DimensionBaseline{}}
	for _, f := range findings {
		d, ok := out.Dimensions[f.Dimension]
		if !ok {
			prior, existed := b.Dimensions[f.Dimension]
			d = DimensionBaseline{Reason: ReasonUntriaged}
			if existed && prior.Reason != "" {
				d.Reason = prior.Reason
			}
			// Carry ONLY the per-artifact overrides that are still findings; an
			// override for a fixed artifact is stale debt and must not survive.
			if existed && len(prior.Except) > 0 {
				d.Except = map[string]string{}
				for _, g := range findings {
					if g.Dimension != f.Dimension {
						continue
					}
					if r, has := prior.Except[g.Artifact]; has {
						d.Except[g.Artifact] = r
					}
				}
				if len(d.Except) == 0 {
					d.Except = nil
				}
			}
			// Same rule, same reason, for recorded magnitudes: a measurement for an
			// artifact that is no longer a finding is stale debt. Dropping it here is
			// what PRUNES the map — there is no second sweep, and there must not be
			// one. An artifact that goes STALE loses its Accepted entry and its
			// measurement together.
			if existed && len(prior.Measured) > 0 {
				d.Measured = map[string]int64{}
				for _, g := range findings {
					if g.Dimension != f.Dimension {
						continue
					}
					if m, has := prior.Measured[g.Artifact]; has {
						d.Measured[g.Artifact] = m
					}
				}
				if len(d.Measured) == 0 {
					d.Measured = nil
				}
			}
			// AND THE EXACT OPPOSITE RULE FOR PriorAccepted, one block below the
			// pruning it must not be mistaken for: it is carried WHOLE, with no
			// still-a-finding filter, because an artifact that stopped being a
			// finding is precisely the case this record exists to remember. The
			// carry is unconditional — a dimension dropping out of
			// dimensionsRecordingPriorAcceptance must stop ADDING to the record,
			// never silently delete what is already in it.
			if existed {
				d.PriorAccepted = slices.Clone(prior.PriorAccepted)
			}
		}
		if _, overridden := d.Except[f.Artifact]; !overridden {
			d.Accepted = append(d.Accepted, f.Artifact)
		}
		// PER-FINDING, deliberately. The carry-forward blocks above run once per
		// dimension, under the `!ok` guard; recording a magnitude has to see every
		// finding, or only the dimension's first artifact ever gets a measurement.
		if f.Measure > 0 {
			if d.Measured == nil {
				d.Measured = map[string]int64{}
			}
			recorded, has := d.Measured[f.Artifact]
			if !has || f.Measure < recorded || raising[[2]string{f.Dimension, f.Artifact}] {
				d.Measured[f.Artifact] = f.Measure
			}
		}
		// PER-FINDING, and gated on BOTH the dimension and the finding's shape.
		//
		// The dimension half is the policy. The `Measure > 0` half is what the policy
		// is actually about: only a MAGNITUDE can fall below a threshold and come
		// back, so only a magnitude-bearing finding can ever be re-crossed. It is not
		// redundant with the dimension gate, and the difference is already scheduled
		// to matter — auditResumeDiscipline's doc records a placeholder-token check
		// that is deliberately unimplemented for now, and when it lands it will emit
		// CATEGORICAL findings (Measure == 0) on this same artifact key. Without this
		// half, those would silently start arming a refusal built for magnitudes.
		//
		// An artifact already in the set is re-appended and deduplicated by the sort
		// below, so a re-acceptance under --reaccept keeps the history rather than
		// erasing it.
		//
		// An artifact covered by Except rather than Accepted IS recorded, deliberately
		// — classify treats both arms as accepted, and an artifact accepted through a
		// one-off reason has been accepted. See TestRegenerate_AnExceptedArtifactIsRecorded.
		if recordsPriorAcceptance(f.Dimension) && f.Measure > 0 {
			d.PriorAccepted = append(d.PriorAccepted, f.Artifact)
		}
		out.Dimensions[f.Dimension] = d
	}
	for name, d := range out.Dimensions {
		slices.Sort(d.Accepted)
		d.Accepted = slices.Compact(d.Accepted)
		slices.Sort(d.PriorAccepted)
		d.PriorAccepted = slices.Compact(d.PriorAccepted)
		out.Dimensions[name] = d
	}

	// A DIMENSION THIS BINARY DOES NOT KNOW NEVER RAN, SO IT PROVED NOTHING, SO ITS
	// ENTRY MAY NOT BE DROPPED.
	//
	// The distinction against the shrink rule is the whole of it, and it is not a
	// softening of that rule:
	//
	//   - A dimension this binary KNOWS that produced no findings is GENUINELY FIXED.
	//     It still drops, exactly as before. That is the ratchet, and it survives
	//     this change untouched.
	//   - A dimension this binary DOES NOT KNOW produced no findings because IT WAS
	//     NEVER ASKED. "No findings" and "no evidence" render identically here, and
	//     treating the second as the first is how a regeneration deletes triage.
	//
	// Without this, an operator on a `vp` older than the vault who types
	// `vp audit vault --accept` PERMANENTLY DESTROYS the human-written reasons,
	// accepted lists, exceptions and measurements of every dimension their binary
	// predates — silently, as a side effect of a command typed for something else.
	// The report that run also mangles is recoverable by re-running from a current
	// binary. This file is not: the prior contents are gone the moment Save returns.
	//
	// The `produced` guard keeps the shrink rule authoritative wherever it can speak.
	// If the findings DID carry this dimension, the binary demonstrably knows about
	// it after all (Regenerate is a pure function and a caller may pass anything), and
	// the freshly built entry — which reflects real evidence — must win over a
	// verbatim carry-forward of the old one.
	//
	// The entry is deep-copied rather than aliased. Regenerate returns a NEW baseline
	// and every other entry in it is freshly allocated; one entry secretly sharing the
	// caller's maps would let a later Save or edit of the result mutate the input.
	for _, name := range b.UnknownDimensions() {
		if _, produced := out.Dimensions[name]; produced {
			continue
		}
		prior := b.Dimensions[name]
		// STRUCT COPY, THEN CLONE THE REFERENCE TYPES — never a field-by-field
		// construction. A hand-enumerated copy silently drops any field added to
		// DimensionBaseline later, and it would drop it on the ONE path nobody
		// exercises: this branch runs only on a binary older than the vault, so the
		// loss would ship undetected by every test written on a current one. That is
		// the hand-maintained-list defect ADR-007 is about, in the one place where
		// nobody would see it rot. Assignment carries every field, present and future.
		//
		// The clones exist only to break aliasing with the input. Both helpers return
		// nil for a nil argument, so the nil-versus-empty distinction the omitempty
		// tags encode survives the copy unchanged.
		carried := prior
		carried.Accepted = slices.Clone(prior.Accepted)
		carried.Except = maps.Clone(prior.Except)
		carried.Measured = maps.Clone(prior.Measured)
		carried.PriorAccepted = slices.Clone(prior.PriorAccepted)
		out.Dimensions[name] = carried
	}

	// A DIMENSION THAT PRODUCED NO FINDINGS AT ALL STILL KEEPS ITS PRIOR-ACCEPTANCE
	// RECORD, AND NOTHING ELSE.
	//
	// This is the drop half of the whole change, and it is easy to miss: the loop that
	// builds `out` ranges over FINDINGS, so a dimension whose every accepted artifact
	// was repaired simply never appears — its reason, its accepted paths and its
	// measurements all vanish together, which is the shrink rule working. Without this
	// pass, PriorAccepted would vanish with them on the one run it exists to survive,
	// and a re-crossing months later would look exactly like a first crossing.
	//
	// What survives is deliberately a STUB, not the old entry: it accepts nothing.
	// Accepted is an empty slice rather than nil so the file reads `"accepted": []` —
	// "this dimension accepts nothing now" — instead of a null a reviewer has to
	// interpret. Except and Measured are dropped, so the shrink rule is untouched: a
	// repaired artifact keeps neither its acceptance nor its magnitude. Reason is kept
	// because it is human-written triage, and destroying it on a drop is the
	// regenerate-stamps-UNTRIAGED defect in a different costume.
	//
	// It runs AFTER the unknown-dimension carry above, and skips anything already
	// present, so it can neither pre-empt that verbatim copy nor mislead its
	// `produced` guard into reading a stub as real evidence.
	for name, prior := range b.Dimensions {
		if len(prior.PriorAccepted) == 0 {
			continue
		}
		if _, produced := out.Dimensions[name]; produced {
			continue
		}
		out.Dimensions[name] = DimensionBaseline{
			Reason:        prior.Reason,
			Accepted:      []string{},
			PriorAccepted: slices.Clone(prior.PriorAccepted),
		}
	}
	return out
}

// compare2 orders by (a1,b1) then (a2,b2) — a stable two-key sort so reports and
// baselines are byte-reproducible across runs.
func compare2(a1, b1, a2, b2 string) int {
	switch {
	case a1 < a2:
		return -1
	case a1 > a2:
		return 1
	case b1 < b2:
		return -1
	case b1 > b2:
		return 1
	}
	return 0
}
