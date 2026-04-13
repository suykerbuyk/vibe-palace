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
type headingRule struct {
	pattern *regexp.Regexp
	dest    Destination
}

var headingRules = []headingRule{
	// Keep-in-place: the managed block itself. Matched by caller before
	// classification; included here for completeness.
	{regexp.MustCompile(`^vibe-palace integration\b`), DestKeepInPlace},

	// Architecture/design family.
	{regexp.MustCompile(`^architecture\b`), DestArchitecture},
	{regexp.MustCompile(`^design\b`), DestArchitecture},
	{regexp.MustCompile(`^package layout\b`), DestArchitecture},
	{regexp.MustCompile(`^import direction\b`), DestArchitecture},
	{regexp.MustCompile(`^data model\b`), DestArchitecture},
	{regexp.MustCompile(`^move atomicity\b`), DestArchitecture},

	// Testing.
	{regexp.MustCompile(`^test(ing)?( strategy)?\b`), DestTesting},
	{regexp.MustCompile(`^coverage\b`), DestTesting},

	// Scope / non-goals.
	{regexp.MustCompile(`^non-goals?\b`), DestScope},
	{regexp.MustCompile(`^scope\b`), DestScope},
	{regexp.MustCompile(`^out of scope\b`), DestScope},

	// Commands / build / run — workflow::Commands.
	{regexp.MustCompile(`^commands?\b`), DestWorkflowCmds},
	{regexp.MustCompile(`^build\b`), DestWorkflowCmds},
	{regexp.MustCompile(`^run\b`), DestWorkflowCmds},
	{regexp.MustCompile(`^dev loop\b`), DestWorkflowCmds},
	{regexp.MustCompile(`^make targets?\b`), DestWorkflowCmds},

	// Workflow / conventions — workflow::Rules (unless body says otherwise).
	{regexp.MustCompile(`^workflow\b`), DestWorkflowRules},
	{regexp.MustCompile(`^when working in this repo\b`), DestWorkflowRules},
	{regexp.MustCompile(`^conventions?\b`), DestWorkflowRules},
	{regexp.MustCompile(`^style\b`), DestWorkflowRules},

	// Resume-family.
	{regexp.MustCompile(`^overview\b`), DestResumeScratch},
	{regexp.MustCompile(`^about\b`), DestResumeScratch},
	{regexp.MustCompile(`^status\b`), DestResumeScratch},

	// Domain facts / knowledge.
	{regexp.MustCompile(`^notation\b`), DestKnowledge},
	{regexp.MustCompile(`^glossary\b`), DestKnowledge},
	{regexp.MustCompile(`^vocabulary\b`), DestKnowledge},
	{regexp.MustCompile(`^rules of the game\b`), DestKnowledge},
	{regexp.MustCompile(`^rules\s*[—-]`), DestKnowledge}, // "Rules — quick reference"
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
	h := strings.ToLower(strings.TrimSpace(heading))
	// Treat an H1/H2 that looks like a project title (single word, or
	// matches the repo directory name) as resume material. We can't know
	// the project slug here, so be conservative: any H1/H2-style single
	// token without spaces goes to resume.
	if h == "" {
		return DestResumeScratch
	}

	for _, r := range headingRules {
		if r.pattern.MatchString(h) {
			// Tiebreaker: plain "Rules" (no "— quick ref", no "of the game")
			// goes to knowledge unless the body reads imperative.
			if r.dest == DestKnowledge && strings.HasPrefix(h, "rules") {
				if bodyLooksLikeWorkflow(body) {
					return DestWorkflowRules
				}
			}
			return r.dest
		}
	}

	// Unrecognized heading that's a single word with no punctuation is
	// likely the project title — treat as resume scratch.
	if !strings.ContainsAny(h, " \t-—:") {
		return DestResumeScratch
	}

	return DestMisc
}

func bodyLooksLikeWorkflow(body string) bool {
	for _, re := range workflowBodyHints {
		if re.MatchString(body) {
			return true
		}
	}
	return false
}
