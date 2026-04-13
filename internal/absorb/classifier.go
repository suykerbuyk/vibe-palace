// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package absorb migrates legacy agent-context files (CLAUDE.md, AGENTS.md,
// .cursorrules, .rules, copilot instructions) into the palace vault and
// leaves each source file containing only its managed vibe-palace block.
package absorb

import (
	"regexp"
	"strings"
)

// Destination identifies a vault file that absorbed content routes to.
// The string form is the destination's path relative to the project's vault
// directory (Projects/{slug}/) — flat layout, no agentctx/ segment.
type Destination struct {
	// Path is relative to Projects/{slug}/ (e.g. "resume.md", "doc/architecture.md").
	Path string
	// Section is the ## subheading under which content merges within a
	// multi-topic file. Empty for whole-file destinations.
	Section string
	// Scratch, when true, means "do not merge directly — queue into
	// absorbed/resume-suggestions.md for human review." Set for resume.md.
	Scratch bool
}

func (d Destination) IsZero() bool { return d.Path == "" }

// Well-known destinations used across the package. Paths are relative to
// Projects/{slug}/.
var (
	DestResumeScratch   = Destination{Path: "resume.md", Scratch: true}
	DestArchitecture    = Destination{Path: "doc/architecture.md"}
	DestTesting         = Destination{Path: "doc/testing.md"}
	DestScope           = Destination{Path: "doc/scope.md"}
	DestMisc            = Destination{Path: "doc/misc.md"}
	DestWorkflowCmds    = Destination{Path: "workflow.md", Section: "Commands"}
	DestWorkflowRules   = Destination{Path: "workflow.md", Section: "Rules"}
	DestKnowledge       = Destination{Path: "knowledge.md"}
	DestKeepInPlace     = Destination{Path: ""} // sentinel — do not migrate
)

// ruleSet matches a heading pattern (lowercased, first-token) to a destination.
// Ordered: earlier entries win. Keep each pattern tight — body tiebreakers
// happen in Classify below, not here.
//
// The name is a short, human-readable label surfaced in dry-run output so
// users can see *why* a section was classified the way it was (e.g.
// "matched: architecture"). Keep names stable — they appear verbatim in
// the UI.
type headingRule struct {
	name    string
	pattern *regexp.Regexp
	dest    Destination
}

var headingRules = []headingRule{
	// Keep-in-place: the managed block itself. Matched by caller before
	// classification; included here for completeness.
	{"keep-in-place", regexp.MustCompile(`^vibe-palace integration\b`), DestKeepInPlace},

	// Architecture/design family.
	{"architecture", regexp.MustCompile(`^architecture\b`), DestArchitecture},
	{"architecture", regexp.MustCompile(`^design\b`), DestArchitecture},
	{"architecture", regexp.MustCompile(`^package layout\b`), DestArchitecture},
	{"architecture", regexp.MustCompile(`^import direction\b`), DestArchitecture},
	{"architecture", regexp.MustCompile(`^data model\b`), DestArchitecture},
	{"architecture", regexp.MustCompile(`^move atomicity\b`), DestArchitecture},

	// Testing.
	{"testing", regexp.MustCompile(`^test(ing)?( strategy)?\b`), DestTesting},
	{"testing", regexp.MustCompile(`^coverage\b`), DestTesting},

	// Scope / non-goals.
	{"scope", regexp.MustCompile(`^non-goals?\b`), DestScope},
	{"scope", regexp.MustCompile(`^scope\b`), DestScope},
	{"scope", regexp.MustCompile(`^out of scope\b`), DestScope},

	// Commands / build / run — workflow::Commands.
	{"workflow-cmds", regexp.MustCompile(`^commands?\b`), DestWorkflowCmds},
	{"workflow-cmds", regexp.MustCompile(`^build\b`), DestWorkflowCmds},
	{"workflow-cmds", regexp.MustCompile(`^run\b`), DestWorkflowCmds},
	{"workflow-cmds", regexp.MustCompile(`^dev loop\b`), DestWorkflowCmds},
	{"workflow-cmds", regexp.MustCompile(`^make targets?\b`), DestWorkflowCmds},

	// Workflow / conventions — workflow::Rules (unless body says otherwise).
	{"workflow-rules", regexp.MustCompile(`^workflow\b`), DestWorkflowRules},
	{"workflow-rules", regexp.MustCompile(`^when working in this repo\b`), DestWorkflowRules},
	{"workflow-rules", regexp.MustCompile(`^conventions?\b`), DestWorkflowRules},
	{"workflow-rules", regexp.MustCompile(`^style\b`), DestWorkflowRules},

	// Resume-family.
	{"resume", regexp.MustCompile(`^overview\b`), DestResumeScratch},
	{"resume", regexp.MustCompile(`^about\b`), DestResumeScratch},
	{"resume", regexp.MustCompile(`^status\b`), DestResumeScratch},

	// Domain facts / knowledge.
	{"knowledge", regexp.MustCompile(`^notation\b`), DestKnowledge},
	{"knowledge", regexp.MustCompile(`^glossary\b`), DestKnowledge},
	{"knowledge", regexp.MustCompile(`^vocabulary\b`), DestKnowledge},
	{"knowledge", regexp.MustCompile(`^rules of the game\b`), DestKnowledge},
	{"knowledge", regexp.MustCompile(`^rules\s*[—-]`), DestKnowledge}, // "Rules — quick reference"
}

// workflowBodyHints are verbs/phrases that tip an ambiguous "Rules" heading
// toward workflow.md (imperative prescriptions to the agent) rather than
// knowledge.md (domain rules of a game or protocol).
var workflowBodyHints = []*regexp.Regexp{
	regexp.MustCompile(`(?mi)^\s*[-*]?\s*(don't|do not|never|always|must|should|avoid|prefer)\b`),
	regexp.MustCompile(`(?mi)\brun\s+['"` + "`" + `]?(vp|go|make|npm|pnpm)\b`),
}

// Classify maps a section heading (plus its body for tiebreakers) to a
// destination. heading should be the heading text without the leading `#`
// marks. Returns DestMisc when nothing matches — caller may prompt the user
// for a manual route.
//
// Pure function; no I/O.
func Classify(heading, body string) Destination {
	d, _ := classifyWithRule(heading, body)
	return d
}

// classifyWithRule is like Classify but also returns a short, human-readable
// reason explaining the decision. Used by the planner to populate Item.Reason
// so users see *why* a section routed the way it did.
func classifyWithRule(heading, body string) (Destination, string) {
	h := strings.ToLower(strings.TrimSpace(heading))
	if h == "" {
		return DestResumeScratch, "empty heading → resume scratch"
	}

	for _, r := range headingRules {
		if r.pattern.MatchString(h) {
			if r.dest == DestKnowledge && strings.HasPrefix(h, "rules") {
				if bodyLooksLikeWorkflow(body) {
					return DestWorkflowRules, "rules heading + imperative body → workflow"
				}
			}
			return r.dest, "matched: " + r.name
		}
	}

	// Unrecognized heading that's a single word with no punctuation is
	// likely the project title — treat as resume scratch.
	if !strings.ContainsAny(h, " \t-—:") {
		return DestResumeScratch, "single-word heading → resume scratch"
	}

	return DestMisc, "unrecognized heading — defaults to doc/misc.md"
}

func bodyLooksLikeWorkflow(body string) bool {
	for _, re := range workflowBodyHints {
		if re.MatchString(body) {
			return true
		}
	}
	return false
}
