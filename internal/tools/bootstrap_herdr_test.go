// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
)

// directiveBase is the opening sentence of the capability directive, repeated
// here as a LITERAL on purpose: these tests pin what the directive renders,
// and re-deriving the expectation from the constant under test would let a
// change to that constant pass unnoticed.
const directiveBase = "After presenting this bootstrap summary, tell the user in one or two lines which commands and skills are now available and how to invoke them (`vpc-<name>`, `vps-<name>`)."

func herdrTestCommands() []commandSummary {
	return []commandSummary{
		{Name: "wrap", Alias: "vpc-wrap"},
		{Name: "restart", Alias: "vpc-restart"},
	}
}

// TestPostBootstrapDirectiveUnchangedOutsideHerdr pins the SILENT case byte for
// byte, in both of the directive's branches.
//
// Most sessions do not run in a Herdr pane, and for those the directive must be
// exactly what it was before the announcement existed — not "close enough".
// This payload's directive is excluded from the token-shed ladder, so anything
// that leaks into it is weight every session pays for whether or not it applies;
// an equality assertion is the only one that catches a stray space.
func TestPostBootstrapDirectiveUnchangedOutsideHerdr(t *testing.T) {
	got := renderPostBootstrapInstructions(herdrTestCommands(), nil, "")
	want := directiveBase + " Examples from this project: `vpc-wrap`, `vpc-restart`."
	if got != want {
		t.Errorf("directive with examples changed outside Herdr:\n got %q\nwant %q", got, want)
	}

	// The degraded branch, taken when the command list was shed away, must be
	// pinned too: it is a separate return statement and could drift alone.
	gotFallback := renderPostBootstrapInstructions(nil, nil, "")
	wantFallback := directiveBase + " If no examples survived truncation, call `" +
		agentfile.CommandToolName + "` or `" + agentfile.SkillToolName + "` with no arguments to list them."
	if gotFallback != wantFallback {
		t.Errorf("degraded directive changed outside Herdr:\n got %q\nwant %q", gotFallback, wantFallback)
	}
}

// TestPostBootstrapDirectiveAnnouncesHerdr pins that the finished line is
// appended without displacing anything.
//
// renderPostBootstrapInstructions no longer knows what Herdr is — it appends
// whatever text it is handed — so this test drives it with literal text rather
// than a pane id, which is exactly the contract it now has.
func TestPostBootstrapDirectiveAnnouncesHerdr(t *testing.T) {
	const line = " This session is running inside Herdr pane pane-7: `vpc-herdr` loads Herdr pane and agent control on demand."

	got := renderPostBootstrapInstructions(herdrTestCommands(), nil, line)

	// The announcement is an ADDITION, never a replacement: the command/skill
	// directive that every session depends on has to survive it intact, and the
	// line lands at the END of the BASE directive — which composeDirective then
	// places after the alerts, so the announcement as a whole is what a host
	// preview cutting into this field destroys first.
	want := directiveBase + " Examples from this project: `vpc-wrap`, `vpc-restart`." + line
	if got != want {
		t.Errorf("announcing directive is not the silent one plus the line:\n got %q\nwant %q", got, want)
	}
}

// TestHerdrAnnouncementGatesOnStdio pins the gate and the two announcing shapes
// together, because they are now one function.
//
// The serve case is the one worth having a test for: `vp mcp serve` inherits
// the environment of whoever started it, so HERDR_ENV there describes a pane
// no connecting agent can see. Announcing on that transport would point an
// agent at somebody else's terminal.
func TestHerdrAnnouncementGatesOnStdio(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "pane-7")

	got := herdrAnnouncement(true)
	if !strings.Contains(got, "vpc-herdr") || !strings.Contains(got, "pane-7") {
		t.Errorf("stdio in a Herdr pane must name vpc-herdr and the pane: got %q", got)
	}
	if !strings.HasPrefix(got, " ") {
		t.Errorf("the line must carry its own leading separator, or it runs into the directive: got %q", got)
	}
	if herdrAnnouncement(false) != "" {
		t.Errorf("HTTP serve must never announce a Herdr pane: got %q", herdrAnnouncement(false))
	}

	// A missing pane id DOWNGRADES the sentence, it never suppresses it — and it
	// never invents an id, which would be indistinguishable from a measured one.
	t.Setenv("HERDR_PANE_ID", "")
	noID := herdrAnnouncement(true)
	if !strings.Contains(noID, "vpc-herdr") {
		t.Errorf("stayed silent when the pane id was unavailable: got %q", noID)
	}
	if strings.Contains(noID, "pane-7") {
		t.Errorf("announced a pane id Herdr never exported: got %q", noID)
	}

	t.Setenv("HERDR_ENV", "")
	if herdrAnnouncement(true) != "" {
		t.Errorf("outside Herdr the line must be empty: got %q", herdrAnnouncement(true))
	}
}

// alertMarkers are substrings unique to each alert composeDirective can join
// onto the directive. They exist so the channel test below can find the FIRST
// alert without hard-coding which one a given fixture happens to raise — the
// set a temp vault triggers (fetch age unknown, health unknown) is incidental,
// and pinning one of them would make this test fail for reasons that have
// nothing to do with Herdr.
//
// Derive additions the way the AuditStaleness field comment says to:
//
//	grep -n "alerts = append" internal/tools/context_tools.go
var alertMarkers = []string{
	"Recent friction is rising",        // friction trend
	"vault fetch age unknown",          // vault staleness, never-fetched shape
	"vault last fetched",               // vault staleness, aged shape
	"vp health is",                     // health
	"caller-side rejection",            // caller friction
	"vault audit is STALE",             // audit staleness, aged shape
	"has NEVER been audited",           // audit staleness, never-run shape
	"was shed to fit the token budget", // task list shed by the ladder
	"over its own token budget",        // over-budget verdict
}

// lastAlertIndex returns the offset of the LATEST alert text in a composed
// directive, or -1 when no alert fired.
//
// It REPLACED a firstAlertIndex when composeDirective put the alerts ahead of
// the capability directive: "the announcement follows every alert" needs the
// last one, exactly as "the announcement precedes every alert" needed the first.
// The old helper was deleted rather than kept beside this one — an unused
// mirror-image helper is the thing a later reader reaches for by accident, and
// it would re-assert the pre-inversion order.
func lastAlertIndex(directive string) int {
	last := -1
	for _, m := range alertMarkers {
		if at := strings.Index(directive, m); at > last {
			last = at
		}
	}
	return last
}

// TestHerdrAnnouncementRidesTheDirectiveNotTheAlerts is the CHANNEL test, and
// the assertion is deliberately about ORDER rather than presence.
//
// composeDirective joins the alerts and the capability directive into the SAME
// field, so a test that merely greps post_bootstrap_instructions for "herdr"
// passes identically whether the line was rendered into the directive or
// appended to the alerts slice — it cannot tell the two designs apart, which
// makes it worthless as a guard. Because the join has a fixed order, the channel
// is observable from outside.
//
// 🔴 THE COMPARISON INVERTED WHEN composeDirective INVERTED. Alerts are now
// joined FIRST (they sit in the region a host preview keeps; the announcement is
// the correct casualty of a cut — see composeDirective). So a directive-borne
// line now FOLLOWS every alert, where it used to precede them. The property
// under test is unchanged — "the Herdr line rides the capability directive, not
// the alerts slice" — only its observable signature moved, and this test moved
// with it rather than being deleted for going red.
//
// The comparison is against the LAST alert, not a chosen one, because "after
// some alert" is satisfied by an announcement inserted halfway down the alerts
// slice — which is exactly the wrong implementation this test exists to catch.
//
// One case is genuinely unobservable and this test does not pretend otherwise:
// an announcement appended as the FINAL element of the alerts slice produces
// byte-identical output, since composeDirective joins with the same single space
// the announcement carries. That shape is a design defect (an announcement
// occupying the alert channel, per the AuditStaleness field comment) rather than
// a delivery defect, and no assertion on the delivered string can distinguish it.
//
// The fixture relies on the vault-staleness alert: a temp vault has no fetch
// history, so its age is unknown and computeVaultStaleness warns. The test
// fails loudly below if no alert is raised at all, so a fixture that stops
// producing one cannot quietly make this assertion vacuous.
func TestHerdrAnnouncementRidesTheDirectiveNotTheAlerts(t *testing.T) {
	vault, resolver := testSetup(t)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "pane-7")

	br := assembleBootstrap(resolver, vault, "test-proj", "", "", nil, true)
	directive := br.PostBootstrapInstructions

	herdrAt := strings.Index(directive, "vpc-herdr")
	if herdrAt < 0 {
		t.Fatalf("the Herdr announcement is missing entirely:\n%s", directive)
	}
	alertAt := lastAlertIndex(directive)
	if alertAt < 0 {
		t.Fatalf("fixture raised no alert at all, so the channel is unobservable:\n%s", directive)
	}
	if herdrAt < alertAt {
		t.Errorf("the Herdr line (index %d) precedes the last alert (index %d), so it is riding the alerts slice rather than the capability directive:\n%s",
			herdrAt, alertAt, directive)
	}
}
