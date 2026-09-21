// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package onboard

import (
	"fmt"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/project"
)

// rowName maps a step's stable identity to the status-table row it renders
// under. The two are deliberately different vocabularies: step names are
// machine identity (Needs clauses, Omitted, the golden table) and must never
// change for a wording reason; row names are prose shown to an operator.
//
// A step may emit rows under a MORE SPECIFIC name than this — command-shims
// splits into "(Claude)", "(Grok)" and "(project)" rows — so this is the
// fallback, and the name every Omission renders under.
var rowName = map[string]string{
	"cwd-project":          "Project config",
	"project-scaffold":     "Project templates",
	"agent-wiring":         "Agent wiring",
	"command-shims":        "Slash-command shims",
	"hook-wiring":          "Hook wiring",
	"project-gitignore":    "Project .gitignore",
	"git-post-commit-hook": "Git commit.msg hook",
}

func rowNameFor(step string) string {
	if n, ok := rowName[step]; ok {
		return n
	}
	return step
}

// Rows renders a Result as the []check.Result the init status table consumes,
// in step-table order.
//
// An Omission renders as a Skip row carrying Reason as the summary and Remedy
// as a detail line. That is the whole reason Omission is a first-class value
// rather than a hand-written skip row inside a step body: the row and the
// machine-readable Omitted list are generated from the same record, so a
// surface cannot report one thing to a human and another to a caller.
func Rows(res Result) []check.Result {
	byStep := map[string][]Outcome{}
	for _, oc := range res.Outcomes {
		byStep[oc.Step] = append(byStep[oc.Step], oc)
	}
	omitByStep := map[string]Omission{}
	for _, om := range res.Omitted {
		omitByStep[om.Step] = om
	}

	var rows []check.Result
	for _, step := range Steps() {
		for _, oc := range byStep[step.Name] {
			rows = append(rows, check.Result{
				Name:    oc.Name,
				Status:  oc.Status,
				Summary: oc.Summary,
				Details: oc.Details,
			})
		}
		if om, ok := omitByStep[step.Name]; ok {
			row := check.Result{
				Name:    rowNameFor(step.Name),
				Status:  check.Skip,
				Summary: om.Reason,
			}
			if om.Remedy != "" {
				row.Details = []string{om.Remedy}
			}
			rows = append(rows, row)
		}
	}
	// Advisories render last and always as Info: nothing failed, nothing was
	// refused, and a Skip here would read as "vp init did not get to it".
	for _, ad := range res.Advisories {
		rows = append(rows, check.Result{
			Name:    ad.Name,
			Status:  check.Info,
			Summary: ad.Summary,
			Details: ad.Details,
		})
	}
	return rows
}

// hookEventNames is the slash-joined list of hook events stepHookWiring
// installs, as it appears in the hook-wiring Remedy.
//
// It named "SessionStart/SessionEnd" for as long as the remedy existed, and
// vibe-palace has never written a SessionStart hook: hook.ValidEvents is
// SessionEnd, Stop and PreCompact. Omission's doc comment makes naming the
// missing artifact MANDATORY, so a remedy pointing at an entry that will never
// appear is a contract breach, not a typo — the operator runs the remedy,
// greps the settings file for the named entry, does not find it, and cannot
// tell whether the repair worked.
//
// TestRemedy_HookEventsMatchValidEvents derives the expected set from
// hook.ValidEvents and fails in BOTH directions, so adding or removing a hook
// event without touching this line is a red test rather than a lie in a
// remedy.
const hookEventNames = "SessionEnd/Stop/PreCompact"

// stepArtifact names, per step, the concrete thing that is MISSING when the
// step does not run. A remedy that says "run vp init" without naming the
// artifact leaves the operator unable to tell whether the repair worked.
//
// dir may be empty (a caller that supplied no project directory); the
// placeholder keeps the sentence grammatical either way.
func stepArtifact(step, dir string) string {
	if dir == "" {
		dir = "<project-dir>"
	}
	switch step {
	case "cwd-project":
		return filepath.Join(dir, ".vibe-palace.toml")
	case "project-scaffold":
		return "Projects/<slug>/{commands,skills}/ in the vault"
	case "agent-wiring":
		return "the managed vibe-palace block in " + filepath.Join(dir, "AGENTS.md") + " (and any CLAUDE.md beside it)"
	case "command-shims":
		// BOTH halves. The step drives shims.Reconcile, which emits command
		// shims AND skill shims (internal/shims/target.go's ClaudeSkillsDir),
		// and remedyCaveat three lines below already talks about the skill
		// shims — so naming only the commands left the caveat referring to an
		// artifact the remedy never mentioned.
		return filepath.Join(dir, ".claude", "commands", "vpc-*.md") +
			" and " + filepath.Join(dir, ".claude", "skills", "vps-*", "SKILL.md")
	case "hook-wiring":
		return "the vp " + hookEventNames + " entries in ~/.claude/settings.json"
	case "project-gitignore":
		return filepath.Join(dir, ".gitignore")
	case "git-post-commit-hook":
		return filepath.Join(dir, ".git", "hooks", "post-commit")
	}
	return "the artifacts of " + step
}

// ownerHost is the "where do I go to fix this" half of a Remedy. Which machine
// it is depends on the SIDE, not on the caller: a working-tree artifact belongs
// to whoever owns that directory, while ~/.claude/settings.json belongs to
// whoever is running the AI host.
func ownerHost(side Side, dir string) string {
	switch side {
	case SideHostGlobal:
		return "on the machine whose AI host you are using — the one that owns ~/.claude/settings.json"
	case SideVault:
		return "on the machine that has write access to the vault"
	default:
		if dir == "" {
			return "on the machine that owns the project directory"
		}
		return "on the machine that owns " + dir
	}
}

// omitForSide builds the Omission for a step whose SIDE this surface may not
// write.
func omitForSide(step Step, req Request) Omission {
	var reason string
	switch step.Side {
	case SideHostGlobal:
		reason = "this surface may not write host-global state: " + rowNameFor(step.Name) +
			" rewrites the RUNNING host's ~/.claude/settings.json, which over MCP is the server operator's, not yours"
	case SideWorkingTree:
		// Three cases, not two. HasRootedSignal fails for a directory that is
		// absent just as it fails for one that is present but unmarked, and
		// telling an operator whose path does not exist that it "carries no
		// .vibe-palace.toml, .git or known manifest" sends them to look for
		// markers in a directory they cannot open. `vp init` never creates
		// that directory, so "create it, then re-run" is the actual repair.
		switch {
		case req.ProjectDir == "":
			reason = "no project directory was supplied, so nothing in a working tree can be written"
		case !project.IsDir(req.ProjectDir):
			reason = fmt.Sprintf(
				"%s does not exist on this server, and onboarding never creates a project directory",
				req.ProjectDir)
		default:
			reason = fmt.Sprintf(
				"%s is not provably a project root here (no .vibe-palace.toml, .git or known manifest in that directory)",
				req.ProjectDir)
		}
	default:
		reason = "this surface may not write the " + step.Side.String() + " side"
	}
	return Omission{
		Step:   step.Name,
		Side:   step.Side,
		Reason: reason,
		Remedy: remedyCommand(step, req) + " " + ownerHost(step.Side, req.ProjectDir) +
			" — it writes " + stepArtifact(step.Name, req.ProjectDir) + "." + remedyCaveat(step.Name),
	}
}

// omitForHostRead builds the Omission for a step that WRITES a side this
// surface may write, but DECIDES what to write by reading the running host's
// home. command-shims is the whole reason ReadsHostGlobal exists: it emits
// project-local shims, but skips a host whose user-global surface is already
// healthy — and over MCP "healthy" would be measured on the server operator's
// machine while the files land in the caller's project.
func omitForHostRead(step Step, req Request) Omission {
	return Omission{
		Step: step.Name,
		Side: step.Side,
		Reason: rowNameFor(step.Name) +
			" decides what to write by inspecting the RUNNING host's ~/.claude and ~/.grok, which over MCP is the server operator's home rather than yours",
		Remedy: remedyCommand(step, req) + " " + ownerHost(step.Side, req.ProjectDir) +
			" — it writes " + stepArtifact(step.Name, req.ProjectDir) +
			" against YOUR host's command surface." + remedyCaveat(step.Name),
	}
}

// remedyCaveat is the "and what you get is not exactly what you missed" half of
// a Remedy.
//
// Only command-shims has one, and it is not cosmetic. An operator handed a
// remedy will reach for `vp commands upgrade` whether or not the remedy names
// it, and the two commands do NOT write the same set: shims.Reconcile
// suppresses the Claude command shims (internal/shims/reconcile.go, the
// !opts.SkipClaude guard) and the Claude skill shims (the same guard over the
// skill items) whenever the host's user-global Claude surface is already
// healthy, while `vp commands upgrade` plans them unconditionally
// (cmd/vp/cmd_commands.go's planShims and planSkillShims). So the substitute
// over-delivers, and a remedy that let the operator discover that by finding
// unexpected files in their repo would be the same silent-divergence failure
// this package exists to prevent.
func remedyCaveat(step string) string {
	if step != "command-shims" {
		return ""
	}
	return " NOTE: `vp commands upgrade` also emits these shims, but it writes a SUPERSET — " +
		"`vp init` suppresses the Claude command and skill shims when your user-global Claude " +
		"surface is already healthy, and `vp commands upgrade` always plans them."
}

// remedyCommand is the verbatim command that performs the omitted step.
func remedyCommand(step Step, req Request) string {
	dir := req.ProjectDir
	if dir == "" {
		dir = "<project-dir>"
	}
	if step.Name == "hook-wiring" {
		return "run `vp hook install`"
	}
	return "run `vp init " + dir + "`"
}
