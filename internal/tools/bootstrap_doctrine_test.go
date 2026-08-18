// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/shims"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// doctrineSites returns every surface that teaches an agent how to read the
// bootstrap payload's delivery state. There are three, and they are pinned
// together on purpose: the command template reaches only hosts that ran
// /vpc-restart, the tool description reaches every agent on every host, and
// the Grok /vpc hub is the shim in front of the exact host where the 19.5 KB
// flat inline cap was measured. Fixing one and leaving the others is the
// ADR-006 failure mode — a rule with one reader is not a rule.
func doctrineSites(t *testing.T) map[string]string {
	t.Helper()

	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded returned error: %v", err)
	}
	restart := ""
	for _, r := range resources {
		if r.RelPath == "commands/restart.md" {
			restart = string(r.Bytes)
		}
	}
	if restart == "" {
		t.Fatal("embedded resource \"commands/restart.md\" missing")
	}

	vault, resolver := testSetup(t)
	desc := BootstrapContextTool(resolver, vault, nil).Description
	if desc == "" {
		t.Fatal("vp_bootstrap_context carries no description")
	}

	hub := shims.RenderSkill(shims.GrokSkill, shims.GrokHubItem())
	if hub == "" {
		t.Fatal("Grok /vpc hub rendered empty")
	}

	return map[string]string{
		"commands/restart.md":                restart,
		"vp_bootstrap_context.Description":   desc,
		"shims: Grok /vpc hub (GrokHubItem)": hub,
	}
}

// TestBootstrapDeliveryDoctrine_RestartTeachesSentinelAndFetch pins the two
// things Step 2 must do, and it deliberately does not settle for a mention.
//
// TestEmbeddedCommands_CheckSuiteDelivery is the model: it anchors on the CALL
// because a template that NAMES a capability reads exactly like one that USES
// it, and only the second changes what an agent does.
//
// Ordering is load-bearing. The doctrine lives in Step 2, ahead of Step 3,
// because a truncation discovered after the restart has continued is a
// post-mortem — and because a session that reaches Step 3 without this
// project's rules and state in hand has already started working without them.
func TestBootstrapDeliveryDoctrine_RestartTeachesSentinelAndFetch(t *testing.T) {
	body := doctrineSites(t)["commands/restart.md"]

	step2 := strings.Index(body, "## Step 2")
	step3 := strings.Index(body, "## Step 3")
	if step2 < 0 || step3 < 0 || step2 > step3 {
		t.Fatalf("restart.md: Step 2/Step 3 anchors not found in order (step2=%d step3=%d)", step2, step3)
	}
	section := body[step2:step3]

	// 1. The sentinel is taught where bootstrap happens, not somewhere later.
	if !strings.Contains(section, "`complete`") {
		t.Fatal("restart.md Step 2: never names the `complete` sentinel — the " +
			"only field that distinguishes a host cut from a clean delivery " +
			"reaches no agent from this template")
	}

	// 2. The FETCH is mandated, unconditionally, in the step where bootstrap
	// happens.
	//
	// 🔴 THIS REPLACED THE OLD ASSERTION, WHICH PINNED THE RECOVERY ONTO THE
	// SENTINEL BULLET. That was right while the bodies arrived inline: the only
	// reason to reach for resume_uri was that a host had cut the body you were
	// sent. Phase 3 stopped sending them, so a rehydrate-on-truncation rule would
	// leave an agent whose payload arrived WHOLE with no resume and no workflow
	// at all — the failure mode is now the silent one, and conditioning the fetch
	// on a truncation signal is exactly how it would ship.
	//
	// It anchors on the CALL, not a mention, for the reason
	// TestEmbeddedCommands_CheckSuiteDelivery does: a template that NAMES a
	// capability reads exactly like one that USES it, and only the second changes
	// what an agent does.
	for _, want := range []string{
		"vp_read_resource", // the call
		"workflow_uri",     // the project's own rules
		"resume_uri",       // its state
		"resume_sha256",    // the digest a later compare-and-set write keys on
	} {
		if !strings.Contains(section, want) {
			t.Errorf("restart.md Step 2: never names %s — the payload no longer carries the "+
				"documents, so a restart that does not fetch them is a session with neither "+
				"this project's rules nor its state", want)
		}
	}

	// The fetch must read as EVERY restart. A conditional fetch is the defect
	// this replaced: it fires only when something already looks wrong, and the
	// whole-payload case is the one that silently loses both documents.
	if !strings.Contains(strings.ToLower(section), "every restart") {
		t.Error("restart.md Step 2: does not say the fetch happens on EVERY restart — " +
			"an agent reading it as a recovery step will skip it whenever the payload " +
			"arrives clean, which is almost always")
	}

	// And it must be ordered ahead of the steps that act on what it loads.
	fetchAt := strings.Index(section, "vp_read_resource")
	doctrineAt := strings.Index(section, "vp_get_doctrine")
	if fetchAt < 0 || doctrineAt < 0 || fetchAt > doctrineAt {
		t.Errorf("restart.md Step 2: the document fetch is not ordered ahead of the doctrine "+
			"fetch (fetch=%d doctrine=%d) — the project's own rules should be in hand before "+
			"the generic manual is", fetchAt, doctrineAt)
	}
}
