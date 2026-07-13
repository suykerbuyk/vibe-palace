// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
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

// TestBootstrapAlertsSurviveTokenTruncation guards a bug that PREDATES the health
// work and was silently costing friction and vault-staleness their warnings too.
//
// The token-budget truncation sheds the command list and then RE-RENDERS
// PostBootstrapInstructions. That re-render used to be a blind ASSIGNMENT, which
// threw away every alert appended before it — so the payload dropped its warnings
// exactly when it was too big to fit, i.e. on a busy project, which is precisely
// when the warnings matter most. The alerts are the highest-value content in the
// payload and they were the first thing discarded.
//
// A tiny max_tokens forces the shed path.
func TestBootstrapAlertsSurviveTokenTruncation(t *testing.T) {
	vault, resolver := testSetup(t)
	now := time.Now().UTC().Format(time.RFC3339)
	writeHealthLog(t, vault, []string{
		fmt.Sprintf(`{"time":"%s","level":"ERROR","msg":"hook: session capture failed; no note was written"}`, now),
	})

	tool := BootstrapContextTool(resolver, vault)
	// max_tokens tiny enough to shed commands+skills and trigger the re-render.
	raw, err := tool.Handler(context.Background(), json.RawMessage(`{"project":"test-proj","max_tokens":1}`))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	res := raw.(BootstrapResult)

	if res.Health == nil {
		t.Fatal("health field was shed by truncation")
	}
	if !strings.Contains(res.PostBootstrapInstructions, "ERROR") {
		t.Fatalf("the health alert was DISCARDED by the token-budget re-render — "+
			"the payload drops its warnings exactly when it is busiest:\n%s",
			res.PostBootstrapInstructions)
	}
}

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
