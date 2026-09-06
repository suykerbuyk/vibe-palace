// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/vaultaudit"
	"github.com/suykerbuyk/vibe-palace/internal/vplog"
)

// hostPreviewSpecimen is a MEASURED specimen, not a constant of the system, and
// it is deliberately test-local for the same reason hostInlineCutSpecimen is:
// naming a host's preview size as an exported server constant is how
// HostInlineCapBytes baked one host's 19,968 into a host-agnostic surface.
//
// 2,000 bytes = the FIXED preview Claude Code delivers in place of any MCP
// result that crosses its persistence threshold. Measured 2026-08-15 and again
// 2026-08-16, on a live /vpc-restart: the host's own banner reads
// `Preview (first 2KB)` and the observed cut landed at 2,002 B on a rune
// boundary. It does NOT scale with payload size — a 58 KB result and a 63 KB
// result are previewed identically.
//
// The tests below must not depend on this particular number being right
// tomorrow. What they assert is the CLASS: some hosts keep only a fixed prefix,
// so the BOUNDED instrument prefix — every field vp declares ahead of
// PostBootstrapInstructions — has to fit inside it, whatever that bound turns
// out to be.
//
// Note the boundary: NOT "every field ahead of Workflow". That was an earlier
// draft's invariant and it was wrong in the specific way boundedPrefixBytes
// documents — it counts the directive, which is the field a cut is SUPPOSED to
// land in, so it fails on the overflow it is meant to permit.
const hostPreviewSpecimen = 2000

// offsetOf returns the byte offset of a JSON key in a marshalled payload.
//
// It measures a MARSHALLED payload rather than summing field sizes, because a
// sum is arithmetic and this needs an observation (287): key names, escaping,
// separators and omitempty decisions are all part of the real cost.
func offsetOf(t *testing.T, raw []byte, key string) int {
	t.Helper()
	i := bytes.Index(raw, []byte(key))
	if i < 0 {
		t.Fatalf("no %s key in the payload — the boundary this measures does not exist; payload starts %.200s", key, raw)
	}
	return i
}

// boundedPrefixBytes returns the size of the BOUNDED instrument prefix: every
// field vp declares ahead of post_bootstrap_instructions.
//
// 🔴 THIS, NOT THE WHOLE BLOCK, IS THE THING THAT MUST FIT A PREVIEW, AND THE
// DISTINCTION IS THE WHOLE DESIGN. post_bootstrap_instructions is the only
// variable-length instrument, which is exactly why it is declared last: it is
// the field a cut is SUPPOSED to land in, and a cut sentence still reads. Every
// field ahead of it is bounded — a URI, a digest, a count, a fixed-shape alert
// struct — and a cut landing in one of those destroys a whole instrument.
//
// An earlier draft of this test gated on the offset of `"workflow":` instead,
// which is the size of the block INCLUDING the directive. That contradicted the
// design it was meant to enforce: it went red because the directive was long,
// i.e. it failed on the one field that is allowed to overflow. Gating here
// measures what is actually invariant.
func boundedPrefixBytes(t *testing.T, raw []byte) int {
	t.Helper()
	return offsetOf(t, raw, `"post_bootstrap_instructions":`)
}

// TestBootstrapInstrumentBlockFitsHostPreview measures the LIVE path: a real
// vault, the real registered tool, a real marshal. 209 and 286 both caught their
// own bugs only by driving the live surface, and 288 is the standing reminder
// that a property inferred from a fixture is not a property of the tool.
func TestBootstrapInstrumentBlockFitsHostPreview(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault, nil)
	raw, err := json.Marshal(bootstrapResult(t, tool, `{"project":"test-proj"}`))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got := boundedPrefixBytes(t, raw)
	if got > hostPreviewSpecimen {
		t.Errorf("bounded instrument prefix is %d B against a %d B host preview — a cut now lands in a BOUNDED "+
			"instrument and destroys it whole, instead of landing in the directive where it costs a sentence tail.\nprefix: %s",
			got, hostPreviewSpecimen, raw[:min(got, 3000)])
	}
	t.Logf("live bounded prefix = %d B (specimen preview %d B, margin %d B); whole block to %s = %d B",
		got, hostPreviewSpecimen, hostPreviewSpecimen-got, firstBulkKey, offsetOf(t, raw, firstBulkKey))
}

// 🔴 THE WORST CASE IS THE ONLY CASE WORTH BOUNDING, AND IT IS NOT THE COMMON
// ONE. Each optional field below is nil when its condition is healthy, by
// deliberate design ("silent when healthy"). So the block is NARROWEST on a
// quiet vault and WIDEST when the alerts fire — which is precisely the session
// where an agent needs what it carries. A ceiling measured on a healthy vault
// would pass every day and fail on the only day it mattered.
//
// worstCaseDirtPaths returns exactly vaultDirtSampleN paths at the width real
// vault paths run to, so the ceiling test costs what the live payload costs.
//
// The seed paths are the live specimens the task records — a sibling project's
// task file and this project's own — and the list is padded, never truncated, if
// the constant is raised past them. Rounding UP is the rule this whole fixture
// follows: a fixture that understates the field it models reports a bound the
// tool does not have.
func worstCaseDirtPaths() []string {
	seeds := []string{
		"Projects/dotfiles/tasks/fetch-bins-install-bin-version-blind-root-cause.md",
		"Projects/vibe-palace/tasks/task-writes-leave-the-vault-dirty-with-no-sweeper.md",
		"Projects/vibe-palace/tasks/surface-mismatch-consumers-drop-the-remediation.md",
	}
	out := make([]string, 0, vaultDirtSampleN)
	for i := range vaultDirtSampleN {
		if i < len(seeds) {
			out = append(out, seeds[i])
			continue
		}
		out = append(out, fmt.Sprintf("Projects/vibe-palace/tasks/a-realistically-long-task-slug-number-%02d.md", i))
	}
	return out
}

// worstCaseSurfaceMismatch builds the instrument the way assembleBootstrap
// builds it — from a real surface.IncompatibleError, through the real message
// renderer — so the fixture cannot state a size the producer does not produce.
//
// The versions are MCPSurfaceVersion and MCPSurfaceVersion+1, the shape a
// stranded host actually sees, and StampDir is an absolute vault path at the
// width the operator's real vault runs to. Rounding UP is this fixture's rule:
// CheckCompatible reports the worst stamp anywhere in the vault, which is
// frequently a sibling project with a longer path than this session's own.
func worstCaseSurfaceMismatch() *SurfaceMismatch {
	ie := &surface.IncompatibleError{
		BinarySurface: surface.MCPSurfaceVersion,
		VaultSurface:  surface.MCPSurfaceVersion + 1,
		StampDir:      "/home/johns/obsidian/vibe-palace-vault/Projects/dotfiles",
	}
	sm := &SurfaceMismatch{
		BinarySurface: ie.BinarySurface,
		VaultSurface:  ie.VaultSurface,
		StampDir:      ie.StampDir,
		Remediation:   ie.Remediation(),
	}
	sm.Message = surfaceMismatchMessage(ie)
	return sm
}

// 🔴 THE WORST CASE IS TWO BRANCHES, BECAUSE PRODUCTION EMITS TWO SHAPES.
//
// A single fixture populating every optional field measured a payload
// assembleBootstrap can no longer produce. After the advisory gate (Chair ruling
// 2026-09-06, option (d)) stop-class instruments and advisory instruments never
// ride together: a stop-class alert says the session cannot do its work, and the
// advisories are neither attached nor announced while one is firing.
//
// So a one-branch fixture would now be a LIE IN THE OTHER DIRECTION — the first
// version over-stated the block by measuring a payload nothing emits, exactly as
// the version before it under-stated the block by omitting a declared
// instrument. Both branches are measured, both must fit, and the derived guard
// keys off their UNION so an instrument that joins neither still fires.
//
// Shared scaffolding — the handle fields, ranking and the head-of-queue row —
// is in worstCaseBase, because it is on EVERY payload the gate emits and a
// second copy of it is one more thing for the two branches to disagree about.
// Values are shaped from the live 2026-08-16 payload, rounded UP, never down.
func worstCaseBase() BootstrapResult {
	return BootstrapResult{
		Project:         "vibe-palace",
		ResumeURI:       "vibe-palace://resume/vibe-palace",
		WorkflowURI:     "vibe-palace://workflow/vibe-palace",
		ResumeSha256:    "b78985c0c74d8bcc25fed2ad486d16957c9f671ac1b07bfc225c0861fb3fcb44",
		ActiveTaskCount: 33,

		// Ranking is NOT advisory and is not gated: it reports how the rows below
		// were ordered, and it is the one instrument that is never silent.
		Ranking: &RankingReport{
			Ranker:        rankerStructural,
			RankedAgainst: "commit-log-archives-orphaned-and-duplicate-commits",
			Candidates:    597,
			Returned:      5,
		},

		HeadOfQueue: []headOfQueueRow{{
			Slug: "commit-log-archives-orphaned-and-duplicate-commits",
			URI:  "vibe-palace://task/vibe-palace/commit-log-archives-orphaned-and-duplicate-commits",
		}},
		Complete: true,
	}
}

// worstCaseStopClass is the payload a session gets when it CANNOT PROCEED: both
// stop-class instruments firing, every advisory suppressed by the gate.
//
// It is the wider of the two branches and the one that matters most — a stranded
// host on a dirty vault is the session with the least room to recover on its
// own, and the two instruments here are the only ones that tell it why.
func worstCaseStopClass() BootstrapResult {
	result := worstCaseBase()

	// THE FIRST OF THE TWO STOP-CLASS ALERTS, and the one this fixture used to
	// leave nil. It was left nil deliberately and recorded as such, on the
	// reasoning that the resulting overrun was pre-existing and belonged to
	// another task — but a fixture that omits a declared instrument is not a
	// recorded exception, it is a ceiling measuring 865 B less than it claims
	// while reporting a pass. The list that was supposed to catch the omission
	// is gone; see boundedInstrumentKeys.
	result.SurfaceMismatch = worstCaseSurfaceMismatch()

	// 🔴 THE SAMPLE IS DERIVED FROM vaultDirtSampleN, NEVER HAND-LISTED.
	// Two literals sat here, so raising the constant 2 -> 5 grew the real
	// payload and left the fixture — and therefore the ceiling — measuring
	// the old arithmetic. Deriving it is what makes the ceiling above
	// enforce the sentence vaultDirtSampleN's own comment writes about
	// cost per sample.
	result.VaultDirt = &VaultDirt{
		Count:       9,
		SamplePaths: worstCaseDirtPaths(),
		Message:     vaultDirtMessage(9),
	}

	result.PostBootstrapInstructions = worstCaseDirective(worstCaseStopAlerts())
	return result
}

// worstCaseAdvisory is the payload a session gets when it CAN proceed but should
// look at something: every advisory firing, both stop-class fields nil.
//
// 🔴 ITS ALERT PROSE COMES FROM THE PRODUCERS, NOT FROM LITERALS HERE. Three of
// the four lines this fixture used to carry could not be produced by
// assembleBootstrap at all — they were plausible paraphrases written beside the
// real thing and never checked against it. That is not only a fixture error: the
// gate's rehousing probes in bootstrap_advisory_gate_test.go were written from
// these same strings, so two of them could never match anything the server
// emits and were dead the day they landed. One source, or the copies drift and
// the tests that read them go quietly vacuous.
func worstCaseAdvisory() BootstrapResult {
	result := worstCaseBase()

	staleness := worstCaseAdvisoryStaleness()
	health := worstCaseAdvisoryHealth()
	audit := worstCaseAdvisoryAudit()
	friction := worstCaseAdvisoryFriction()

	result.VaultStaleness = &staleness
	result.Health = &health
	result.AuditStaleness = &audit
	result.FrictionTrend = &friction

	result.PostBootstrapInstructions = worstCaseDirective(worstCaseAdvisoryAlerts())
	return result
}

// worstCaseAdvisoryStaleness is computeVaultStaleness's own output for a vault
// fetched 72.5 h ago — the producer, called, not a paraphrase of it.
func worstCaseAdvisoryStaleness() VaultStaleness {
	fetched := time.Date(2026, 8, 16, 15, 17, 8, 0, time.UTC)
	return computeVaultStaleness(72*time.Hour+30*time.Minute, fetched, true)
}

// worstCaseAdvisoryHealth is the health SUMMARY, which is data rather than
// prose: vplog.Summary carries no message. The alert line is rendered from it by
// healthMessage, in worstCaseAdvisoryAlerts, which is the producer.
//
// RecentWarns is nil because assembleBootstrap clears it on the bootstrap copy —
// a fixture carrying records the server strips would over-state the field.
func worstCaseAdvisoryHealth() vplog.Summary {
	return vplog.Summary{
		Status:         vplog.StatusErrors,
		WarnCounts:     map[string]int{"mcp.makeHandler": 10, "storage.lockedWrite": 3, "capture.ingest": 2},
		CallerFriction: 8,
		LogPath:        "/home/johns/obsidian/vibe-palace-vault/palace/.local/vp.log",
		LogSize:        65554,
	}
}

// worstCaseAdvisoryAudit is vaultaudit.CheckStaleness's churn verdict.
//
// 🔴 THE MESSAGE IS THE PRODUCER'S FORMAT STRING, NOT A RESTATEMENT. It cannot
// be the producer CALLED, because CheckStaleness takes a *storage.Vault and
// globs a real tree, and a fixture whose byte count depends on a temp directory
// is not a fixture. So the format string is reproduced with this struct's own
// field values, and TestAdvisoryFixtureLinesMatchTheirProducers drives the REAL
// CheckStaleness against a real stale vault and fails if the two disagree. The
// copy exists; nothing about it is unchecked.
//
// The line it replaced ("⚠ The vault audit is 61 sessions stale (last …). Run
// `vp audit vault`, or /vpc-vault-audit …") was one such unchecked paraphrase:
// the producer has never emitted that sentence.
func worstCaseAdvisoryAudit() vaultaudit.Staleness {
	const lastAudit = "2026-06-14"
	const churn = 61
	return vaultaudit.Staleness{
		Warn:       true,
		LastAudit:  lastAudit,
		Churn:      churn,
		ChurnKnown: true,
		Message: fmt.Sprintf("⚠ vault audit is STALE: %d session notes written across the vault since the %s audit (threshold %d) — run `vp audit vault`.",
			churn, lastAudit, vaultaudit.StaleChurnThreshold),
	}
}

// worstCaseAdvisoryFriction is capture.ComputeFrictionTrend's own output, driven
// by synthesized sessions — the producer, called, windows and message included.
func worstCaseAdvisoryFriction() capture.FrictionTrend {
	return capture.ComputeFrictionTrend(worstCaseFrictionSessions(), worstCaseFrictionNow, time.UTC)
}

// worstCaseFrictionNow is the clock the friction fixture is dated against. Fixed,
// because a fixture measured against time.Now() measures a different payload
// every day.
var worstCaseFrictionNow = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

// worstCaseFrictionSessions synthesizes a history that makes ComputeFrictionTrend
// WARN, with three-digit session counts in every window so the rendered rows cost
// what a busy project's rows cost.
//
// The shape is chosen to clear the producer's own two thresholds rather than to
// hit a target string: recent average above trendWarnFloor, and a recent-minus-
// baseline delta above trendDeadBand. Day offsets stop one short of each window
// so a session dated N days back at midnight still falls inside an N-day cutoff
// measured from midday.
func worstCaseFrictionSessions() []storage.SessionMeta {
	var out []storage.SessionMeta
	add := func(n, dayFrom, dayTo, friction int) {
		for i := range n {
			day := dayFrom + i%(dayTo-dayFrom+1)
			out = append(out, storage.SessionMeta{
				Date:          worstCaseFrictionNow.AddDate(0, 0, -day).Format("2006-01-02"),
				FrictionScore: friction,
			})
		}
	}
	add(75, 0, 6, 62)    // inside 7d
	add(177, 7, 29, 38)  // inside 30d, outside 7d
	add(292, 30, 89, 30) // inside 90d, outside 30d
	return out
}

// worstCaseBranches is every shape production emits, and it is what the derived
// guard takes its UNION over. A branch added to the payload and not to this
// slice is a branch nothing measures.
func worstCaseBranches() []BootstrapResult {
	return []BootstrapResult{worstCaseStopClass(), worstCaseAdvisory()}
}

// worstCaseDirective composes the directive the way the server composes it — the
// real renderer and the real join, not a run of filler. It is the only unbounded
// instrument, which is exactly why it is declared last: it is the field that
// SHOULD absorb a cut, because a cut sentence still reads.
//
// An earlier draft used strings.Repeat("x", 900), which was both fake and
// OPTIMISTIC: the real composed worst case measures ~1,155 B once every alert
// message is joined on. A fixture that under-states the field it models is a
// fixture that reports a bound the tool does not have (288).
func worstCaseDirective(alerts []string) string {
	return composeDirective(
		renderPostBootstrapInstructions(worstCaseCommands(), nil,
			" This session is running inside Herdr pane wP:p1: `vpc-herdr` loads Herdr pane and agent control on demand."),
		alerts,
	)
}

// worstCaseCommands is the command list the directive renders its examples from.
func worstCaseCommands() []commandSummary {
	return []commandSummary{
		{Name: "cancel-plan", Alias: "vpc-cancel-plan"},
		{Name: "capture", Alias: "vpc-capture"},
	}
}

// The alert lines, SPLIT ALONG THE SAME SEAM AS THE STRUCTS. They were one
// function returning every alert at once; the advisory gate makes that a set the
// server never composes, and a directive fixture that joins all eight would
// measure a string production cannot build.
//
// Derive additions the way the AuditStaleness field comment says to:
//
//	grep -n "alerts = append" internal/tools/context_tools.go
//
// and put each new one on the side of the gate its append site sits on.

// worstCaseStopAlerts is the stop-class pair, in the order assembleBootstrap
// appends them. Order is delivery order — the alerts lead the directive, so the
// LAST append is what a host cut reaches first — and the surface mismatch leads
// because it is the only alert that says no mutating tool will run at all.
func worstCaseStopAlerts() []string {
	return []string{
		worstCaseSurfaceMismatch().Message,
		vaultDirtMessage(9),
	}
}

// worstCaseAdvisoryAlerts is every alert BELOW the gate firing at once, IN THE
// ORDER assembleBootstrap appends them, and every line is the producer's.
//
// 🔴 TWO LINES WERE DELETED RATHER THAN CORRECTED. The task-list shed and the
// over-budget line have no append site anywhere outside a test: the token shed
// ladder that raised them is gone (ADR-009), and only its comments survive in
// context_tools.go. They were kept on a "the fixture rounds up" argument, which
// is wrong in the one way that matters — rounding up models a payload that could
// GROW into existence, and these model a payload that was DELETED. Carrying dead
// prose in a fixture is precisely how the rehousing probes came to be written
// against strings no producer emits.
//
// Derive additions the way the AuditStaleness field comment says to:
//
//	grep -n "alerts = append" internal/tools/context_tools.go
//
// and put each new one on the side of the gate its append site sits on.
func worstCaseAdvisoryAlerts() []string {
	health := worstCaseAdvisoryHealth()
	return []string{
		worstCaseAdvisoryFriction().Message,
		worstCaseAdvisoryStaleness().Message,
		healthMessage(health),
		callerFrictionMessage(health),
		worstCaseAdvisoryAudit().Message,
	}
}

// 🔴 TestAdvisoryFixtureLinesMatchTheirProducers IS THE CHECK THAT WAS MISSING,
// and its absence is why three of this fixture's four advisory lines had drifted
// into prose no producer emits.
//
// The drift was invisible because nothing read the fixture against the server.
// The lines were plausible — "⚠ Friction is worsening: 31.6 over 7d against 26.9
// over 90d", "another machine may have written since", "The vault audit is 61
// sessions stale" — and every one of them was invented beside the producer
// rather than taken from it. That mattered beyond the byte count: the gate's
// rehousing probes were written from these strings, so two probes searched the
// directive for text the server cannot produce and could never fire.
//
// Most of the fixture now CALLS its producers, which makes drift unrepresentable
// for those lines. This test covers the one that cannot be called purely —
// vaultaudit.CheckStaleness needs a real tree — by building that tree and
// comparing bytes, and it re-asserts the callable ones actually WARN, because a
// producer that silently stopped warning would leave the advisory branch
// measuring a payload with no alerts on it.
func TestAdvisoryFixtureLinesMatchTheirProducers(t *testing.T) {
	// The one copy: the audit line, against the real producer on a real vault.
	fixture := worstCaseAdvisoryAudit()
	produced := vaultaudit.CheckStaleness(staleAuditVault(t, fixture.LastAudit, fixture.Churn), worstCaseFrictionNow)

	if !produced.Warn {
		t.Fatalf("test premise broken: the synthesized vault did not make CheckStaleness warn (%+v)", produced)
	}
	if produced.Message != fixture.Message {
		t.Errorf("the audit fixture line is not what vaultaudit.CheckStaleness emits.\ngot (fixture):  %q\nwant (producer): %q\n\n"+
			"This line is a COPY of the producer's format string — the producer takes a *storage.Vault and "+
			"cannot be called from a pure fixture — so this comparison is the only thing keeping the two "+
			"in agreement. Update the fixture, never this assertion.",
			fixture.Message, produced.Message)
	}

	// The callable producers: assert they still WARN, so the branch keeps
	// measuring a payload with its alerts on.
	if vs := worstCaseAdvisoryStaleness(); !vs.Warn || vs.Message == "" {
		t.Errorf("computeVaultStaleness no longer warns for the fixture's fetch age (%+v) — the advisory "+
			"branch would measure a payload with no staleness alert on it", vs)
	}
	if ft := worstCaseAdvisoryFriction(); !ft.Warn || ft.Message == "" {
		t.Errorf("capture.ComputeFrictionTrend no longer warns for the synthesized history "+
			"(direction=%q recent=%.1f windows=%v) — the fixture's sessions no longer clear the producer's "+
			"own thresholds, so the friction alert has silently left the branch",
			ft.Direction, ft.RecentAvg, ft.Windows)
	}
	if h := worstCaseAdvisoryHealth(); healthMessage(h) == "" || callerFrictionMessage(h) == "" {
		t.Errorf("healthMessage or callerFrictionMessage went silent for the fixture summary (%+v)", h)
	}

	// And the composed set is what assembleBootstrap would append: five lines,
	// none empty. A producer returning "" would shrink the directive silently.
	alerts := worstCaseAdvisoryAlerts()
	if len(alerts) != 5 {
		t.Errorf("the advisory alert set is %d lines, want 5 — one per append site below the gate "+
			"(friction, vault staleness, health, caller friction, audit staleness)", len(alerts))
	}
	for i, a := range alerts {
		if strings.TrimSpace(a) == "" {
			t.Errorf("advisory alert %d is empty; its producer went silent", i)
		}
	}
}

// staleAuditVault builds a vault whose newest audit report is dated lastAudit and
// stamped with a session-note count exactly churn below the vault's current one,
// so vaultaudit.CheckStaleness reports that churn.
func staleAuditVault(t *testing.T, lastAudit string, churn int) *storage.Vault {
	t.Helper()
	root := t.TempDir()
	vault := bornCurrentTestVault(t, root)

	// Enough notes that current - stamped == churn, with the stamp above zero so
	// the report is genuinely ANCHORED (a stamp of zero and no stamp at all are
	// different states, and conflating them once shipped a nag reporting a whole
	// corpus as churn).
	const stamped = 40
	sessions := filepath.Join(root, "Projects", "test-proj", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range stamped + churn {
		note := filepath.Join(sessions, fmt.Sprintf("2026-08-16-fixture-%03d.md", i))
		if err := os.WriteFile(note, []byte("---\ndate: 2026-08-16\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	audits := filepath.Join(root, "Audits")
	if err := os.MkdirAll(audits, 0o755); err != nil {
		t.Fatal(err)
	}
	report := fmt.Sprintf("---\ndate: %s\nsession_notes: %d\n---\n\n# Vault audit\n", lastAudit, stamped)
	if err := os.WriteFile(filepath.Join(audits, lastAudit+"-vault-audit.md"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	return vault
}

// TestBootstrapInstrumentBlockWorstCaseFitsHostPreview is the invariant that
// makes this stick. It goes RED the moment the block regrows past the preview —
// which is what will actually happen: workflow.md grew 1,201 B in two days in
// August 2026 and silently invalidated an approved design's arithmetic. A rule
// enforced by a test survives that; a rule written in a task file does not
// (PRD §1.11).
//
// 🔴 EVERY BRANCH MUST FIT, AND THE SUBTESTS ARE NOT A STYLE CHOICE. A gate that
// splits the payload into shapes gives a ceiling more than one worst case, and a
// ceiling that measures the narrower one is the same defect this whole file
// exists to catch, wearing a different hat. Ranging over worstCaseBranches()
// means a branch added there is measured the day it is added.
func TestBootstrapInstrumentBlockWorstCaseFitsHostPreview(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload BootstrapResult
	}{
		{"stop-class", worstCaseStopClass()},
		{"advisory", worstCaseAdvisory()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			got := boundedPrefixBytes(t, raw)
			if got > hostPreviewSpecimen {
				t.Errorf("WORST-CASE bounded instrument prefix (%s branch) is %d B against a %d B host preview, over by %d B.\n"+
					"Every alert on this side of the advisory gate is firing here, which is the session where losing "+
					"them costs most. Something was added to BootstrapResult ahead of PostBootstrapInstructions, or a "+
					"bounded instrument grew.\n"+
					"Shrink it or move it behind a handle — do NOT raise this ceiling, and do NOT close the gap by moving "+
					"content onto the directive: the directive is the field a cut EATS, so anything relocated there is "+
					"deleted from the surviving prefix rather than saved.\nprefix: %s",
					tc.name, got, hostPreviewSpecimen, got-hostPreviewSpecimen, raw[:min(got, 3000)])
			}
			t.Logf("%s worst-case bounded prefix = %d B (specimen preview %d B, margin %d B); whole block to %s = %d B; directive alone = %d B",
				tc.name, got, hostPreviewSpecimen, hostPreviewSpecimen-got,
				firstBulkKey, offsetOf(t, raw, firstBulkKey), len(tc.payload.PostBootstrapInstructions))
		})
	}
}

// 🔴 TestWorstCaseBranchesAreDisjoint pins the PREMISE the two branches rest on.
//
// Splitting one fixture into two is only honest if the split matches the gate.
// If a stop-class field ever appeared on the advisory branch — or an advisory on
// the stop-class branch — the two fixtures would together measure a payload
// production never emits while each one individually looked fine, which is
// exactly the over-statement the split was made to remove.
//
// It asserts the fixture, not the server. TestStopClassSuppressesAdvisories in
// bootstrap_advisory_gate_test.go asserts the same seam on the LIVE path,
// because a property inferred from a fixture is not a property of the tool (288).
func TestWorstCaseBranchesAreDisjoint(t *testing.T) {
	stop := worstCaseStopClass()
	adv := worstCaseAdvisory()

	if stop.SurfaceMismatch == nil || stop.VaultDirt == nil {
		t.Errorf("the stop-class branch does not populate both stop-class instruments "+
			"(surface_mismatch=%v vault_dirt=%v) — it is not measuring the widest payload the gate emits",
			stop.SurfaceMismatch != nil, stop.VaultDirt != nil)
	}
	if stop.VaultStaleness != nil || stop.Health != nil || stop.AuditStaleness != nil || stop.FrictionTrend != nil {
		t.Errorf("the stop-class branch carries an advisory instrument — the gate suppresses all four, so this " +
			"fixture measures a payload production never emits and over-states the ceiling")
	}

	if adv.VaultStaleness == nil || adv.Health == nil || adv.AuditStaleness == nil || adv.FrictionTrend == nil {
		t.Errorf("the advisory branch does not populate all four advisories "+
			"(vault_staleness=%v health=%v audit_staleness=%v friction_trend=%v)",
			adv.VaultStaleness != nil, adv.Health != nil, adv.AuditStaleness != nil, adv.FrictionTrend != nil)
	}
	if adv.SurfaceMismatch != nil || adv.VaultDirt != nil {
		t.Errorf("the advisory branch carries a stop-class instrument — a stop firing is precisely what " +
			"suppresses the advisories, so the two cannot be on one payload")
	}

	// Ranking and the handles are on BOTH: they are not advisory, and a reader
	// that cannot reach the bulk has nothing to proceed with either way.
	for _, tc := range []struct {
		name    string
		payload BootstrapResult
	}{{"stop-class", stop}, {"advisory", adv}} {
		if tc.payload.Ranking == nil || tc.payload.ResumeURI == "" || tc.payload.ResumeSha256 == "" {
			t.Errorf("the %s branch dropped a handle field or the ranking report; those are not advisory "+
				"and ride on every payload", tc.name)
		}
	}
}

// TestDirectiveCutKeepsAlertsAndLosesAnnouncement reproduces the actual defect
// rather than describing it: it cuts a composed directive at a preview-sized
// offset and asserts what is left.
//
// This is the assertion the old order would have failed. Measured on a live
// /vpc-restart 2026-08-16, the host's 2 KB preview ended inside this field at
// `…(guards working, not fa`: the whole capability announcement survived and the
// caller-friction alert was destroyed mid-word. Alerts-first inverts that.
//
// 🔴 THE NEGATIVE ASSERTION IS THE ONE THAT MATTERS. A test that only checks the
// alerts are present passes on a directive that was never cut at all, which is
// how a truncation test measures nothing (290). So this asserts the cut REALLY
// removed the announcement — if the fixture ever shrinks under the cut, the test
// says so instead of quietly going green.
func TestDirectiveCutKeepsAlertsAndLosesAnnouncement(t *testing.T) {
	const announcement = "After presenting this bootstrap summary, tell the user in one or two lines which commands and skills are now available and how to invoke them (`vpc-<name>`, `vps-<name>`). Examples from this project: `vpc-cancel-plan`, `vpc-capture`."
	alerts := []string{
		"⚠ vp logged ERRORS in the last 24h (mcp.makeHandler x10). Call vp_health for detail.",
		"ℹ 8 caller-side rejection(s) in 24h (guards working, not faults) — vp_health for detail.",
		"⚠ The vault audit is 61 sessions stale (last 2026-06-14). Run `vp audit vault`.",
	}

	composed := composeDirective(announcement, alerts)

	// Cut where a host would: partway in, past the alerts but inside the
	// announcement. The premise is asserted, not assumed.
	cut := len(composed) - len(announcement)/2
	if cut >= len(composed) {
		t.Fatalf("test premise broken: the cut at %d does not truncate a %d-byte directive", cut, len(composed))
	}
	prefix := composed[:cut]

	for _, alert := range alerts {
		if !strings.Contains(prefix, alert) {
			t.Errorf("a cut directive lost an alert — this is the defect, not a cosmetic ordering issue.\nlost: %q\nprefix: %q", alert, prefix)
		}
	}
	if strings.Contains(prefix, announcement) {
		t.Fatalf("the announcement survived the cut whole, so nothing was actually truncated and the assertions above prove nothing (composed %d B, cut at %d)",
			len(composed), cut)
	}
}

// directiveKey is the JSON key of the one unbounded instrument, and it is the
// BOUNDARY the two functions below are written against: everything DECLARED
// ahead of it is bounded and must survive a host cut; everything after it is
// either the field a cut eats or the bulk behind a handle.
const directiveKey = "post_bootstrap_instructions"

// boundedInstrumentKeys derives the instrument list from BootstrapResult's OWN
// JSON TAGS, in declaration order, stopping at the directive.
//
// 🔴 ENUMERATING IS WHAT FAILED, SO THIS DOES NOT RE-ENUMERATE. The list this
// replaces was nine string literals, hand-maintained, and its whole job was to
// catch "the fixture stopped exercising an instrument". It could not: a tenth
// instrument (`surface_mismatch`, 012c2ae) did not join it by being declared,
// so the guard against a hand-maintained fixture was itself a hand-maintained
// list, and the ceiling above measured 865 B less than it claimed while
// reporting a pass.
//
// A second copy of a list can only ever drift out of agreement with the thing it
// describes. vaultaudit.DimensionNames closed this same class by construction —
// the human-facing description of the audit is generated from the dimension
// registry, so no second copy exists to disagree — and BootstrapResult's field
// declarations are exactly that registry for this payload. An instrument is in
// scope the DAY IT IS DECLARED, not the day someone remembers to add a literal.
//
// GENERATING IS CORRECT HERE, and that is not the default (see DimensionNames'
// note on why the source-audit ungated-writer anchor set is a PIN instead). The
// derived predicate and the property being checked cover exactly the same set:
// the property is "every field declared ahead of the directive is present and
// precedes it", and reflect.Type gives that set with nothing left over. There is
// no gap for a generated list to silently drop.
//
// `project` is included, where the literal list omitted it. It is a declared
// field ahead of the directive, so it is in scope by the same rule as the rest;
// omitting it was one more judgement call the list was carrying invisibly.
func boundedInstrumentKeys(t *testing.T, rt reflect.Type) []string {
	t.Helper()
	if rt.Kind() != reflect.Struct {
		t.Fatalf("boundedInstrumentKeys wants a struct type, got %s", rt.Kind())
	}

	var keys []string
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			// A field with no json tag marshals under its Go name, which is not
			// a wire key any reader keys on. There are none today; if one
			// arrives, it is a wire-shape bug and belongs in a fix, not a skip
			// this guard papers over.
			t.Fatalf("%s.%s has no usable json tag (%q) — it would land on the wire under its Go name",
				rt.Name(), f.Name, f.Tag.Get("json"))
		}
		if name == directiveKey {
			return keys
		}
		keys = append(keys, name)
	}

	t.Fatalf("%s declares no %q field — the boundary this guard is written against does not exist, "+
		"so every assertion below it would be vacuous", rt.Name(), directiveKey)
	return nil
}

// instrumentBranch is one marshalled payload the guard measures: a name for the
// failure message, the document, and the offset of the directive within it.
type instrumentBranch struct {
	name      string
	doc       string
	directive int
}

// marshalBranch marshals one branch and locates its directive.
func marshalBranch(t *testing.T, name string, payload any) instrumentBranch {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s branch: %v", name, err)
	}
	doc := string(raw)
	at := strings.Index(doc, `"`+directiveKey+`":`)
	if at < 0 {
		t.Fatalf("the %s branch has no %q key — the boundary every assertion below is measured against "+
			"does not exist in it, so they would all be vacuous", name, directiveKey)
	}
	return instrumentBranch{name: name, doc: doc, directive: at}
}

// instrumentUnionFindings reports every derived instrument that appears in NO
// branch, and every instrument that landed AFTER the directive in some branch.
//
// 🔴 THE UNION IS THE POINT, AND SO IS THE FACT THAT IT IS A UNION OF BRANCHES
// RATHER THAN A SINGLE PAYLOAD. The advisory gate means no one payload carries
// every declared instrument any more: a stop-class payload has no advisories and
// an advisory payload has no stop-class fields. Requiring presence in ONE
// document would now go red on a correct server, and the obvious repair —
// dropping the presence check — would delete the only assertion that catches a
// declared instrument no fixture exercises. That is the 012c2ae defect exactly.
//
// So presence is checked against the union: an instrument must be exercised by
// SOME branch. Ordering is checked per branch, because a cut lands in one
// payload at a time. An instrument that joins neither branch fires with the same
// message class it fired with before the split.
func instrumentUnionFindings(keys []string, branches []instrumentBranch) []string {
	var findings []string
	for _, key := range keys {
		seen := false
		for _, b := range branches {
			at := strings.Index(b.doc, `"`+key+`":`)
			if at < 0 {
				continue
			}
			seen = true
			if at > b.directive {
				findings = append(findings, fmt.Sprintf(
					"%q is at byte %d of the %s branch, AFTER the directive at %d — the directive is the only "+
						"field that may absorb a cut, because it is the only one a partial read still yields "+
						"value from", key, at, b.name, b.directive))
			}
		}
		if !seen {
			names := make([]string, len(branches))
			for i, b := range branches {
				names[i] = b.name
			}
			findings = append(findings, fmt.Sprintf(
				"%q is absent from EVERY worst-case branch (%s) — the fixture stopped exercising an instrument, "+
					"so the ceiling above is measuring less than it claims. It is in scope because BootstrapResult "+
					"DECLARES it ahead of %q; populate it in whichever branch the advisory gate puts it on.",
				key, strings.Join(names, ", "), directiveKey))
		}
	}
	return findings
}

// 🔴 TestDerivedInstrumentGuardFiresOnAnUnpopulatedField IS THE PROOF, and it is
// the assertion the replaced literal list could not make about itself.
//
// The defect was not "someone forgot to add `surface_mismatch`". It was that
// forgetting was POSSIBLE: the guard's scope came from a second, hand-written
// copy of the field list, so a declared-but-unpopulated instrument was invisible
// to it. Deriving the scope from the declarations is only a fix if the derived
// guard actually FIRES on that shape — otherwise this is the same trust, moved.
//
// It is retargeted, not relaxed, for the two-branch split. The shape it proves
// is now the harder one: an instrument that joins NEITHER branch. A guard that
// took the union and then accepted "absent everywhere" would pass this file's
// whole suite while measuring nothing, and the union is exactly the change that
// makes that failure mode reachable.
//
// Nothing in this file enumerates `new_instrument`; the finding comes from the
// struct tag alone.
func TestDerivedInstrumentGuardFiresOnAnUnpopulatedField(t *testing.T) {
	// A payload shaped like BootstrapResult's instrument region, with one
	// instrument DECLARED and left nil — exactly a new alert added to the struct
	// and forgotten in the fixture.
	type payloadWithANewInstrument struct {
		Project                   string           `json:"project"`
		ResumeURI                 string           `json:"resume_uri"`
		StopClass                 *VaultDirt       `json:"stop_class,omitempty"`
		Advisory                  *VaultDirt       `json:"advisory,omitempty"`
		NewInstrument             *VaultDirt       `json:"new_instrument,omitempty"`
		PostBootstrapInstructions string           `json:"post_bootstrap_instructions,omitempty"`
		HeadOfQueue               []headOfQueueRow `json:"head_of_queue"`
	}

	base := payloadWithANewInstrument{
		Project:                   "vibe-palace",
		ResumeURI:                 "vibe-palace://resume/vibe-palace",
		PostBootstrapInstructions: "⚠ something is wrong.",
	}
	fired := &VaultDirt{Count: 1, Message: "⚠ dirty"}

	// Two branches that between them exercise every instrument EXCEPT the newly
	// declared one — the two-branch analogue of the fixture 012c2ae shipped.
	stop, advisory := base, base
	stop.StopClass = fired
	advisory.Advisory = fired

	keys := boundedInstrumentKeys(t, reflect.TypeOf(base))
	if !slices.Contains(keys, "new_instrument") {
		t.Fatalf("the derived list does not contain the newly declared instrument, so the assertion below "+
			"would prove nothing. derived: %v", keys)
	}
	if slices.Contains(keys, directiveKey) || slices.Contains(keys, "head_of_queue") {
		t.Errorf("the derived list ran past the directive into the bulk: %v", keys)
	}

	branches := []instrumentBranch{
		marshalBranch(t, "stop-class", stop),
		marshalBranch(t, "advisory", advisory),
	}

	// The premise: each branch on its own is MISSING an instrument the other
	// carries. Asserted, not assumed — if the fixtures ever populated both, the
	// union below would be the same as either branch and would prove nothing
	// about unions at all.
	if strings.Contains(branches[0].doc, `"advisory":`) || strings.Contains(branches[1].doc, `"stop_class":`) {
		t.Fatalf("test premise broken: the branches are not disjoint, so this measures no union.\nstop: %s\nadvisory: %s",
			branches[0].doc, branches[1].doc)
	}

	findings := instrumentUnionFindings(keys, branches)
	if len(findings) != 1 || !strings.Contains(findings[0], "new_instrument") {
		t.Fatalf("the guard did not fire on an instrument DECLARED ahead of the directive and populated in "+
			"NEITHER branch — this is the exact defect 012c2ae shipped, and deriving the list has not fixed "+
			"it.\nfindings: %v", findings)
	}

	// And the other direction: populating it in ONE branch clears the finding.
	// The union must accept an instrument that only one shape carries — that is
	// the whole reason presence is checked across branches rather than within
	// one — while still failing when nothing carries it.
	advisory.NewInstrument = fired
	branches[1] = marshalBranch(t, "advisory", advisory)
	if findings := instrumentUnionFindings(keys, branches); len(findings) != 0 {
		t.Errorf("populating the instrument in ONE branch did not clear the finding, so the guard demands "+
			"presence in every branch — which the advisory gate makes impossible, and which would push a "+
			"maintainer to delete the check rather than fix the fixture: %v", findings)
	}
}

// TestBootstrapDirectiveIsLastInstrument pins the property the ceiling depends
// on and cannot itself detect: of everything ahead of the bulk, the directive
// must come LAST.
//
// It is the only variable-length instrument in the block. Every other field is
// bounded — a URI, a digest, a count, a fixed-shape alert struct — so a cut that
// lands in one of those destroys a whole instrument, while a cut landing in the
// directive costs a sentence tail that still reads. This is not stylistic
// ordering: it decides what a truncated payload can still be used for.
//
// `command_invocation` used to sit after it, and was deleted rather than moved,
// because it restated what mcp.ServerInstructions already delivers at initialize.
func TestBootstrapDirectiveIsLastInstrument(t *testing.T) {
	keys := boundedInstrumentKeys(t, reflect.TypeOf(BootstrapResult{}))

	branches := []instrumentBranch{
		marshalBranch(t, "stop-class", worstCaseStopClass()),
		marshalBranch(t, "advisory", worstCaseAdvisory()),
	}
	for _, finding := range instrumentUnionFindings(keys, branches) {
		t.Error(finding)
	}

	// The directive itself must still precede the bulk, in every branch. This is
	// per-branch, not a union: it is a property of a payload, and a union would
	// let one malformed shape hide behind a well-formed one.
	for _, b := range branches {
		bulk := strings.Index(b.doc, firstBulkKey)
		if bulk < 0 {
			t.Fatalf("the %s branch has no %s key, so the instrument/index boundary does not exist in it",
				b.name, firstBulkKey)
		}
		if b.directive > bulk {
			t.Errorf("the directive is at byte %d of the %s branch, inside the bulk that starts at %d — "+
				"it is an instrument and must precede it", b.directive, b.name, bulk)
		}
	}
}
