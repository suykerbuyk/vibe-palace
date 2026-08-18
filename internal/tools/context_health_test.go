// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/vplog"
)

// bootstrapWithLog runs vp_bootstrap_context against a vault seeded with logLines.
func bootstrapWithLog(t *testing.T, logLines []string) BootstrapResult {
	t.Helper()
	vault, resolver := testSetup(t)
	if len(logLines) > 0 {
		writeHealthLog(t, vault, logLines)
	}

	tool := BootstrapContextTool(resolver, vault)
	raw, err := tool.Handler(context.Background(), json.RawMessage(`{"project":"test-proj"}`))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	res, ok := raw.(BootstrapResult)
	if !ok {
		t.Fatalf("unexpected result type %T", raw)
	}
	return res
}

// TestBootstrapPushesHealthWhenDegraded is the whole point of §1c.
//
// vp_health existed for a long time and NOTHING EVER CALLED IT — not a template, not
// a command, not a skill. It was itself a member of the class it was built to detect:
// capability built, nothing invokes it. "Who calls vp_health?" was the wrong question,
// because every pull-based answer is a rule in prose, and `vp check` is this project's
// standing proof that prose reaches nobody.
//
// So health RIDES IN the payload every session already loads. It reaches every agent
// on every host, and it survives an agent that never thinks to ask.
func TestBootstrapPushesHealthWhenDegraded(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	res := bootstrapWithLog(t, []string{
		fmt.Sprintf(`{"time":"%s","level":"ERROR","msg":"hook: session capture failed; no note was written"}`, now),
	})

	if res.Health == nil {
		t.Fatal("vp logged an ERROR and vp_bootstrap_context said NOTHING — " +
			"health is pull-only again, and no agent will ever ask")
	}
	if res.Health.Status != vplog.StatusErrors {
		t.Errorf("health status = %q, want %q", res.Health.Status, vplog.StatusErrors)
	}

	// A structured field in a large payload is easy to skim past, so the directive
	// carries a human-visible line too — mirroring friction_trend and vault_staleness.
	if !strings.Contains(res.PostBootstrapInstructions, "ERROR") {
		t.Errorf("the post-bootstrap directive does not mention the error:\n%s", res.PostBootstrapInstructions)
	}
}

// TestBootstrapHealthCarriesVerdictNotRecords is the wire proof for the other
// half of this cut: `recent_warns` must not reach the bootstrap payload, while
// vp_health must still serve it from the SAME log.
//
// 🔴 BOTH HALVES ARE REQUIRED, AND ASSERTING EITHER ALONE IS A DIFFERENT, WEAKER
// TEST. Absence in bootstrap proves nothing on its own — a vault whose log
// simply has no entries produces the same bytes, which is how a truncation test
// ends up measuring nothing (290). Presence in vp_health is what proves the
// records still EXIST and were dropped deliberately from one copy, rather than
// lost from the system.
//
// 🔴 IT ASSERTS ON MARSHALLED BYTES, NOT ON THE STRUCT. A `len(Health.RecentWarns)
// == 0` check passes on a nil slice AND on an empty one, so a regression that
// re-introduced `"recent_warns":[]` would slip through — and it would not prove
// the transport at all. What ships is what encoding/json emits, so that is what
// is measured (mcplib.NewToolResultJSON marshals the result value directly).
//
// Why the records go: they were 609 B of a 2,214 B instrument block on a live
// restart — the single largest item ahead of the bulk — spent on five records
// whose only differing content was a timestamp and a fault label. The tally
// rides in warn_counts and the directive names vp_health as the reader.
func TestBootstrapHealthCarriesVerdictNotRecords(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	logLines := make([]string, 0, 6)
	for i := range 6 {
		logLines = append(logLines, fmt.Sprintf(
			`{"time":"%s","level":"WARN","msg":"mcp.makeHandler: handler error %d","fault":"internal"}`, now, i))
	}

	vault, resolver := testSetup(t)
	writeHealthLog(t, vault, logLines)

	// ── The bootstrap side: health present, records absent, on the wire.
	raw, err := BootstrapContextTool(resolver, vault).Handler(context.Background(), json.RawMessage(`{"project":"test-proj"}`))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	br, ok := raw.(BootstrapResult)
	if !ok {
		t.Fatalf("unexpected bootstrap result type %T", raw)
	}
	if br.Health == nil {
		t.Fatal("test premise broken: the log has six WARNs and bootstrap reported no health at all, " +
			"so the absence asserted below would be vacuous")
	}
	wire, err := json.Marshal(br)
	if err != nil {
		t.Fatalf("marshal bootstrap: %v", err)
	}
	if bytes.Contains(wire, []byte(`"recent_warns"`)) {
		t.Errorf("recent_warns is back on the bootstrap wire — it is the largest single instrument in the payload "+
			"and it re-inflates the region a host preview keeps, to carry records vp_health already serves:\n%s", wire)
	}
	// The verdict itself must still arrive, or the field was gutted rather than trimmed.
	if !bytes.Contains(wire, []byte(`"warn_counts"`)) {
		t.Errorf("the bootstrap health carries no warn_counts — the tally is what replaced the records, "+
			"so dropping it too leaves an alert that names no cause:\n%s", wire)
	}

	// ── The vp_health side: the SAME log, the records still served.
	hraw, err := HealthTool(vault).Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("vp_health: %v", err)
	}
	hwire, err := json.Marshal(hraw)
	if err != nil {
		t.Fatalf("marshal health: %v", err)
	}
	if !bytes.Contains(hwire, []byte(`"recent_warns"`)) {
		t.Errorf("vp_health does not serve recent_warns either — the records were not moved behind the reader, "+
			"they were deleted from the system, and the bootstrap alert now names a reader that cannot answer:\n%s", hwire)
	}
}

// TestBootstrapIsSilentWhenHealthy pins the other half of the operator's decision.
//
// An always-on "healthy ✅" is the soft signal agents learn to skim past — the same
// reasoning that killed the `partial` capture status tier. The field appearing AT ALL
// must mean something needs looking at.
func TestBootstrapIsSilentWhenHealthy(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	res := bootstrapWithLog(t, []string{
		fmt.Sprintf(`{"time":"%s","level":"INFO","msg":"all is well"}`, now),
	})

	if res.Health != nil {
		t.Errorf("bootstrap reported health (%q) on a HEALTHY system — "+
			"an always-on green light is the signal agents learn to ignore", res.Health.Status)
	}
}

// TestBootstrapAlertsSurviveTokenTruncation was DELETED here in
// first-principles Phase 3, not moved and not renamed.
//
// It drove the handler with `{"max_tokens":1}` to force the shed path and then
// asserted the health alert survived the directive re-render that path
// performed. Phase 2 deleted the shed ladder and the `max_tokens` parameter with
// it — and an unknown JSON key is IGNORED, so the call kept succeeding and the
// test kept passing while exercising exactly the same path as
// TestBootstrapPushesHealthWhenDegraded above. A test that names a mechanism the
// binary no longer has reads as coverage of that mechanism and is worth less
// than no test, because it also supplies false confidence (the 290 family).
//
// The property it cared about is still enforced: alerts are collected in their
// own slice and composed into the directive by composeDirective at the END of
// assembly, which is what makes losing them unrepresentable. That is covered by
// TestBootstrapPushesHealthWhenDegraded (alert reaches the directive) and by
// TestDirectiveCutKeepsAlertsAndLosesAnnouncement (alerts lead, so a cut eats the
// announcement instead).

// TestBootstrapPushesHealthWhenBlind is the case that matters most on a fresh host
// and on every non-MCP process: there is NO LOG. That is not health, it is blindness,
// and the whole task exists to stop reporting blindness as success.
func TestBootstrapPushesHealthWhenBlind(t *testing.T) {
	res := bootstrapWithLog(t, nil) // no log file at all

	if res.Health == nil {
		t.Fatal("vp cannot read its own log and bootstrap said NOTHING — " +
			"a blind instrument is being reported as a healthy one")
	}
	if res.Health.Status != vplog.StatusUnknown {
		t.Errorf("health status = %q, want %q", res.Health.Status, vplog.StatusUnknown)
	}
	if !strings.Contains(res.PostBootstrapInstructions, "UNKNOWN") {
		t.Errorf("the directive does not tell the agent health is unknown:\n%s", res.PostBootstrapInstructions)
	}
}
