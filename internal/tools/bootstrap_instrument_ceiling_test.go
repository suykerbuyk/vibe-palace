// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
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
	tool := BootstrapContextTool(resolver, vault)
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
	t.Logf("live bounded prefix = %d B (specimen preview %d B, margin %d B); whole block to \"workflow\": = %d B",
		got, hostPreviewSpecimen, hostPreviewSpecimen-got, offsetOf(t, raw, `"workflow":`))
}

// worstCaseInstruments returns a BootstrapResult with EVERY optional instrument
// populated at a realistic maximum — every alert firing at once.
//
// 🔴 THE WORST CASE IS THE ONLY CASE WORTH BOUNDING, AND IT IS NOT THE COMMON
// ONE. Each of these fields is nil when its condition is healthy, by deliberate
// design ("silent when healthy"). So the block is NARROWEST on a quiet vault and
// WIDEST when every alert fires — which is precisely the session where an agent
// needs what it carries. A ceiling measured on a healthy vault would pass every
// day and fail on the only day it mattered.
//
// Values are shaped from the live 2026-08-16 payload, rounded UP, never down.
func worstCaseInstruments() BootstrapResult {
	fetched := time.Date(2026, 8, 16, 15, 17, 8, 0, time.UTC)
	return BootstrapResult{
		Project:         "vibe-palace",
		ResumeURI:       "vibe-palace://resume/vibe-palace",
		WorkflowURI:     "vibe-palace://workflow/vibe-palace",
		ResumeSha256:    "b78985c0c74d8bcc25fed2ad486d16957c9f671ac1b07bfc225c0861fb3fcb44",
		ActiveTaskCount: 33,
		VaultStaleness: &VaultStaleness{
			LastFetched: &fetched,
			AgeHours:    72.5,
			Warn:        true,
			Message:     "⚠ The vault view is 72h old — another machine may have written since. Run vp_vault_sync with action pull before trusting resume.md.",
		},
		Health: &vplog.Summary{
			Status:         vplog.StatusErrors,
			WarnCounts:     map[string]int{"mcp.makeHandler": 10, "storage.lockedWrite": 3, "capture.ingest": 2},
			CallerFriction: 8,
			LogPath:        "/home/johns/obsidian/vibe-palace-vault/palace/.local/vp.log",
			LogSize:        65554,
		},
		AuditStaleness: &vaultaudit.Staleness{
			Warn:       true,
			Message:    "⚠ The vault audit is 61 sessions stale (last 2026-06-14). Run `vp audit vault`, or /vpc-vault-audit for the full adversarial pass.",
			LastAudit:  "2026-06-14",
			Churn:      61,
			ChurnKnown: true,
		},
		FrictionTrend: &capture.FrictionTrend{
			Windows: []capture.FrictionWindow{
				{Days: 7, SessionCount: 75, AvgFriction: 31.6},
				{Days: 30, SessionCount: 252, AvgFriction: 31.5},
				{Days: 90, SessionCount: 544, AvgFriction: 26.9},
			},
			RecentAvg: 31.6,
			Direction: "worsening",
			Warn:      true,
			Message:   "⚠ Friction is worsening: 31.6 over 7d against 26.9 over 90d. Call vp_trends for the breakdown.",
		},
		// The directive, composed the way the server composes it — the real
		// renderer and the real join, not a run of filler. It is the only
		// unbounded instrument, which is exactly why it is declared last: it is
		// the field that SHOULD absorb a cut, because a cut sentence still reads.
		//
		// An earlier draft used strings.Repeat("x", 900), which was both fake and
		// OPTIMISTIC: the real composed worst case measures ~1,155 B once every
		// alert message is joined on. A fixture that under-states the field it
		// models is a fixture that reports a bound the tool does not have (288).
		PostBootstrapInstructions: composeDirective(
			renderPostBootstrapInstructions(worstCaseCommands(), nil,
				" This session is running inside Herdr pane wP:p1: `vpc-herdr` loads Herdr pane and agent control on demand."),
			worstCaseAlerts(),
		),

		Workflow: "# workflow",
		Resume:   "# resume",
		Complete: true,
	}
}

// worstCaseCommands is the command list the directive renders its examples from.
func worstCaseCommands() []commandSummary {
	return []commandSummary{
		{Name: "cancel-plan", Alias: "vpc-cancel-plan"},
		{Name: "capture", Alias: "vpc-capture"},
	}
}

// worstCaseAlerts is every alert firing at once, at realistic length.
//
// Derive additions the way the AuditStaleness field comment says to:
//
//	grep -n "alerts = append" internal/tools/context_tools.go
func worstCaseAlerts() []string {
	return []string{
		"⚠ Recent friction is rising: 31.6 over 7d against 26.9 over 90d. Call vp_trends for the breakdown.",
		"⚠ The vault last fetched 72h ago — another machine may have written since. Run vp_vault_sync with action pull before trusting resume.md.",
		"🔴 vp health is ERRORS in the last 24h (mcp.makeHandler x10, storage.lockedWrite x3, capture.ingest x2). Something failed and was recorded but not surfaced. Call vp_health for the full list before trusting recent captures.",
		"ℹ 8 caller-side rejection(s) in 24h (guards working, not faults) — vp_health for detail.",
		"⚠ The vault audit is STALE: 61 sessions since the last one (2026-06-14). Run `vp audit vault`, or /vpc-vault-audit for the full adversarial pass.",
		"⚠ The active task list (33 open) was shed to fit the token budget — call `vp_list_tasks` for it.",
		"⚠ bootstrap payload is over its own token budget after shedding everything sheddable — read the resume via resume_uri and treat this payload as incomplete",
	}
}

// TestBootstrapInstrumentBlockWorstCaseFitsHostPreview is the invariant that
// makes this stick. It goes RED the moment the block regrows past the preview —
// which is what will actually happen: workflow.md grew 1,201 B in two days in
// August 2026 and silently invalidated an approved design's arithmetic. A rule
// enforced by a test survives that; a rule written in a task file does not
// (PRD §1.11).
func TestBootstrapInstrumentBlockWorstCaseFitsHostPreview(t *testing.T) {
	raw, err := json.Marshal(worstCaseInstruments())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got := boundedPrefixBytes(t, raw)
	if got > hostPreviewSpecimen {
		t.Errorf("WORST-CASE bounded instrument prefix is %d B against a %d B host preview, over by %d B.\n"+
			"Every alert is firing here, which is the session where losing them costs most. "+
			"Something was added to BootstrapResult ahead of PostBootstrapInstructions, or a bounded instrument grew.\n"+
			"Shrink it or move it behind a handle — do NOT raise this ceiling, and do NOT close the gap by moving "+
			"content onto the directive: the directive is the field a cut EATS, so anything relocated there is "+
			"deleted from the surviving prefix rather than saved.\nprefix: %s",
			got, hostPreviewSpecimen, got-hostPreviewSpecimen, raw[:min(got, 3000)])
	}
	t.Logf("worst-case bounded prefix = %d B (specimen preview %d B, margin %d B); whole block to \"workflow\": = %d B; directive alone = %d B",
		got, hostPreviewSpecimen, hostPreviewSpecimen-got,
		offsetOf(t, raw, `"workflow":`), len(worstCaseInstruments().PostBootstrapInstructions))
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
	raw, err := json.Marshal(worstCaseInstruments())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	doc := string(raw)

	directive := boundedPrefixBytes(t, raw)
	bulk := offsetOf(t, raw, `"workflow":`)

	for _, instrument := range []string{
		`"resume_uri":`,
		`"workflow_uri":`,
		`"resume_sha256":`,
		`"active_task_count":`,
		`"vault_staleness":`,
		`"health":`,
		`"audit_staleness":`,
		`"friction_trend":`,
	} {
		at := strings.Index(doc, instrument)
		if at < 0 {
			t.Errorf("%s is absent from the worst-case payload — the fixture stopped exercising an instrument, so the ceiling above is measuring less than it claims", instrument)
			continue
		}
		if at > directive {
			t.Errorf("%s is at byte %d, AFTER the directive at %d — the directive is the only field that may absorb a cut, "+
				"because it is the only one a partial read still yields value from", instrument, at, directive)
		}
	}

	if directive > bulk {
		t.Errorf("the directive is at byte %d, inside the bulk that starts at %d — it is an instrument and must precede it", directive, bulk)
	}
}
