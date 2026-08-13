// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"regexp"
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
	desc := BootstrapContextTool(resolver, vault).Description
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

// unqualifiedAbsentBudget matches the claim "an absent `budget` means nothing
// was reduced" and everything that follows it up to the end of its sentence.
// The `[^.]` bound is what makes the assertion below meaningful: a `complete`
// qualifier three sentences away does not rescue a rule that reads as
// unconditional where an agent meets it.
var unqualifiedAbsentBudget = regexp.MustCompile(`(?i)absent[^.]*budget[^.]*nothing[^.]*reduced[^.]*`)

// bulletsIn splits a markdown section into its top-level `- ` bullets, keyed by
// the byte offset each starts at. Scoping an assertion to one bullet is what
// keeps it honest: the surrounding step is full of `resume_uri` and
// `vp_read_resource` mentions, and a section-wide substring check would pass on
// a sentinel rule that says only "you were truncated".
func bulletsIn(section string) map[int]string {
	out := make(map[int]string)
	for i := 0; i+2 < len(section); {
		start := strings.Index(section[i:], "\n- ")
		if start < 0 {
			break
		}
		start += i + 1
		end := strings.Index(section[start+3:], "\n- ")
		if end < 0 {
			out[start] = section[start:]
			break
		}
		out[start] = section[start : start+3+end]
		i = start + 3
	}
	return out
}

// absent returns the members of want that s does not contain.
func absent(s string, want []string) []string {
	var out []string
	for _, w := range want {
		if !strings.Contains(s, w) {
			out = append(out, w)
		}
	}
	return out
}

// flatten strips markdown emphasis and collapses whitespace, so a phrase the
// template wrapped across a line break or bolded mid-word still reads as the
// phrase. Without it this test would pin line wrapping, not doctrine.
func flatten(s string) string {
	s = strings.NewReplacer("*", "", "`", "").Replace(s)
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// TestBootstrapDeliveryDoctrine_AbsentBudgetNeverStandsAlone is the delivery
// pin for the absent-vs-cut distinction.
//
// The defect it prevents: every one of these three surfaces used to say, and
// restart.md said it in bold, "an absent `budget` means nothing was reduced."
// Both readings of that are wrong, and both were observed:
//
//   - The contrapositive comes free — budget present ⇒ reduced ⇒ truncated —
//     and on any project with real history the ladder sheds `recent_sessions`
//     on essentially every run. So `budget` is present nearly always, and an
//     agent keying on its presence narrates a benign optional shed as a failed
//     bootstrap and re-fetches for nothing.
//   - Far worse: on a truncated channel `budget` is absent because it was CUT
//     OFF, and the rule tells the agent that means nothing was reduced. The
//     agent reads a silent host cut as a clean delivery.
//
// `budget` answers "what did vp shed"; `complete` (the last field of
// BootstrapResult, no `omitempty`, guarded structurally by
// TestBootstrapCompleteSentinelAlwaysEmitted) answers "did every byte arrive".
// Neither answers the other's question. So the claim may appear only when it
// is conditioned on `complete` in the same sentence — which is exactly the
// property asserted here, rather than a ban on the words, because the
// qualified form is true and must stay sayable.
func TestBootstrapDeliveryDoctrine_AbsentBudgetNeverStandsAlone(t *testing.T) {
	for name, body := range doctrineSites(t) {
		for _, claim := range unqualifiedAbsentBudget.FindAllString(body, -1) {
			if !strings.Contains(claim, "complete") {
				t.Errorf("%s: asserts that an absent `budget` means nothing was "+
					"reduced without conditioning it on `complete` in the same "+
					"sentence — in a truncated channel `budget` is absent because "+
					"it was CUT OFF, and this sentence tells the agent that is a "+
					"clean delivery. Offending sentence:\n\t%q", name, claim)
			}
		}
	}
}

// TestBootstrapDeliveryDoctrine_RestartTeachesTheSentinel pins the POSITIVE
// half, and it deliberately does not settle for a mention.
//
// TestEmbeddedCommands_CheckSuiteDelivery is the model: it anchors on the CALL
// because a template that NAMES a capability reads exactly like one that USES
// it, and only the second changes what an agent does. The action here is
// rehydration, so the sentinel bullet must carry the recovery handles and the
// tool that pages them — a bullet that says "you were truncated" and stops
// leaves the agent knowing it has a problem and not what to do about it.
//
// Ordering is load-bearing twice over. The doctrine lives in Step 2, ahead of
// Step 3, because a truncation discovered after the restart has continued is a
// post-mortem. And the host axis must be stated before the vp axis, because
// `budget` is uninterpretable until you know the payload arrived whole.
func TestBootstrapDeliveryDoctrine_RestartTeachesTheSentinel(t *testing.T) {
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

	// 2. The RULE that raises the sentinel must also hand over the recovery,
	// scoped to a single bullet so a rehydration instruction living elsewhere
	// in the step cannot satisfy this by accident. Prose that observes the
	// sentinel exists is not the rule; the bullet an agent acts on is.
	want := []string{
		"resume_uri",       // the handle
		"vp_read_resource", // the call that pages it — the anchor, not a mention
		"resume_sha256",    // the digest that verifies what came back
		"before",           // ...ordered ahead of every step that reads the body
	}
	sentinel, missing := -1, want
	for offset, cand := range bulletsIn(section) {
		if !strings.Contains(cand, "`complete`") {
			continue
		}
		short := absent(strings.ToLower(cand), want)
		if len(short) < len(missing) {
			sentinel, missing = offset, short
		}
		if len(short) == 0 {
			break
		}
	}
	if sentinel < 0 {
		t.Fatal("restart.md Step 2: the `complete` sentinel appears in no bullet — " +
			"prose that observes the sentinel exists is not a rule an agent acts on")
	}
	if len(missing) > 0 {
		t.Errorf("restart.md Step 2: the `complete` bullet never names %v — it tells "+
			"the agent it was truncated without telling it how to recover, or "+
			"when. Bullet at offset %d", missing, sentinel)
	}

	// 3. Host axis first. `budget` cannot be read at all until `complete` has
	// answered whether the payload arrived, so the sentinel must precede the
	// shed rules rather than trail them as a footnote.
	shedCore := strings.Index(section, "shed_core")
	if shedCore < 0 {
		t.Fatal("restart.md Step 2: never names `shed_core` — without it there is " +
			"no way to tell an intentional resume reduction from a benign " +
			"optional-rung shed")
	}
	if sentinel > shedCore {
		t.Errorf("restart.md Step 2: states the vp shed axis (offset %d) before the "+
			"host `complete` axis (offset %d) — an agent reading in order applies "+
			"`budget` rules to a payload it has not established it fully received",
			shedCore, sentinel)
	}

	// 4. The benign case is named, and named as benign. This is the false
	// positive half: without it the ladder's routine `recent_sessions` shed
	// gets narrated as a failed bootstrap on nearly every run.
	flat := flatten(section)
	for _, want := range []string{
		"recent_sessions", // the rung that sheds on essentially every run
		"benign",          // ...and the verdict it must be given
		"do not re-fetch", // ...and the action it must NOT trigger
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("restart.md Step 2: never says %q — the optional-rung shed is "+
				"the common case, and an agent without this rule re-fetches and "+
				"reports truncation on a healthy bootstrap", want)
		}
	}
}
