// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"path/filepath"
	"sort"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

// ReconcileReport is the machine-usable summary of a Reconcile run: the
// capability names whose host shims were newly created (Added) or rewritten to
// match a drifted vault entry (Updated), deduplicated across every shim target
// (Claude, Grok, Cursor) and sorted, plus any non-fatal errors encountered.
//
// A command wired on both the Claude and Grok targets appears ONCE — the
// report is about capabilities the human gained, not about individual files.
// Empty() is the steady-state case: the host was already in sync.
//
// It is distinct from Report (the per-Apply count summary) because callers of
// Reconcile want the NAMES that changed, deduplicated across targets, not a
// per-file/per-target count.
type ReconcileReport struct {
	CommandsAdded   []string `json:"commands_added,omitempty"`
	CommandsUpdated []string `json:"commands_updated,omitempty"`
	SkillsAdded     []string `json:"skills_added,omitempty"`
	SkillsUpdated   []string `json:"skills_updated,omitempty"`
	// Errors holds best-effort failures (a read-only .claude/, a transient IO
	// error). They are surfaced, never returned as a hard error, because every
	// caller treats shim reconciliation as best-effort and must not break the
	// operation it rides on.
	Errors []string `json:"errors,omitempty"`
}

// Empty reports whether the reconcile changed nothing and hit no errors — the
// common steady-state, in which callers should stay silent.
func (r ReconcileReport) Empty() bool {
	return len(r.CommandsAdded) == 0 && len(r.CommandsUpdated) == 0 &&
		len(r.SkillsAdded) == 0 && len(r.SkillsUpdated) == 0 && len(r.Errors) == 0
}

// ReconcileOptions controls which project-tree host families Reconcile writes.
// Per-host skips implement the Option C warn window: when a host already has
// healthy user-global surfaces, project emission for that host is suppressed
// without suppressing other hosts (C1).
type ReconcileOptions struct {
	// SkipClaude omits project .claude/commands and .claude/skills.
	SkipClaude bool
	// SkipGrok omits project .grok/plugins/.../commands, .grok/skills, and hub.
	SkipGrok bool
	// ClaudeSkipReason / GrokSkipReason are optional human labels for init rows.
	ClaudeSkipReason string
	GrokSkipReason   string
}

// Reconcile brings projectRoot's host shim files into alignment with the
// vault's command and skill sets, ADDITIVELY: it creates missing shims and
// rewrites drifted ones, but NEVER deletes a stale shim and NEVER touches a
// hand-written (marker-less) file.
//
// Default (zero opts): Claude command + skill shims always; Grok when
// GrokPresent; Cursor when CursorPresent. Callers that have migrated a host
// to user-global surfaces pass SkipClaude / SkipGrok independently.
//
// Production callers: vp init (with per-host skips). Bootstrap no longer
// reconciles (install-time only). vp commands upgrade keeps its own plan path.
//
// slug is the owning project (from project.DetectProject) so project-scoped
// commands get shims too; an empty slug yields global-only commands.
func Reconcile(projectRoot string, resolver *vpctx.Resolver, slug string, opts ReconcileOptions) ReconcileReport {
	var r ReconcileReport
	if projectRoot == "" || resolver == nil {
		return r
	}

	cmdAdded, cmdUpdated := map[string]bool{}, map[string]bool{}
	skillAdded, skillUpdated := map[string]bool{}, map[string]bool{}

	// --- Command shims ---
	if summaries, err := commands.List(resolver, "command", slug, "", "", 60); err != nil {
		r.Errors = append(r.Errors, "list commands: "+err.Error())
	} else if len(summaries) > 0 {
		if !opts.SkipClaude {
			r.applyCommands("claude command shims", func() ([]Change, error) {
				return Plan(summaries, projectRoot)
			}, cmdAdded, cmdUpdated)
		}
		if !opts.SkipGrok && GrokPresent(projectRoot) {
			r.applyCommands("grok command shims", func() ([]Change, error) {
				return PlanGrokCommands(summaries, projectRoot)
			}, cmdAdded, cmdUpdated)
		}
	}

	// --- Skill shims ---
	if items := r.skillItems(resolver); len(items) > 0 {
		if !opts.SkipClaude {
			r.applySkills("claude skill shims", ClaudeSkill, items, projectRoot, skillAdded, skillUpdated)
		}
		if CursorPresent(projectRoot) {
			r.applySkills("cursor rule shims", CursorRule, items, projectRoot, skillAdded, skillUpdated)
		}
		if !opts.SkipGrok && GrokPresent(projectRoot) {
			r.applySkills("grok skill shims", GrokSkill, items, projectRoot, skillAdded, skillUpdated)
		}
	}

	r.CommandsAdded = sortedNames(cmdAdded)
	r.CommandsUpdated = sortedNames(cmdUpdated)
	r.SkillsAdded = sortedNames(skillAdded)
	r.SkillsUpdated = sortedNames(skillUpdated)
	return r
}

// applyCommands plans (via plan) and applies one command-shim target,
// accumulating added/updated names and recording any error under what.
func (r *ReconcileReport) applyCommands(what string, plan func() ([]Change, error), added, updated map[string]bool) {
	changes, err := plan()
	if err != nil {
		r.Errors = append(r.Errors, "plan "+what+": "+err.Error())
		return
	}
	rep, err := Apply(changes, ApplyOptions{AllowStaleRemoval: false})
	if err != nil {
		r.Errors = append(r.Errors, "apply "+what+": "+err.Error())
	}
	for _, o := range rep.Outcomes {
		recordOutcome(o.Change.Name, o.Result.Kind, added, updated)
	}
}

// applySkills plans and applies one skill-shim target. For GrokSkill it also
// folds in the /vpc command hub (PlanGrokHub), which PlanSkills deliberately
// does not cover.
func (r *ReconcileReport) applySkills(what string, target TargetKind, items []SkillItem, projectRoot string, added, updated map[string]bool) {
	changes, err := PlanSkills(target, items, projectRoot)
	if err != nil {
		r.Errors = append(r.Errors, "plan "+what+": "+err.Error())
		return
	}
	if target == GrokSkill {
		if hub, err := PlanGrokHub(projectRoot); err != nil {
			r.Errors = append(r.Errors, "plan grok hub: "+err.Error())
		} else {
			changes = append(changes, hub)
		}
	}
	_, outcomes, err := ApplySkills(changes, ApplyOptions{AllowStaleRemoval: false})
	if err != nil {
		r.Errors = append(r.Errors, "apply "+what+": "+err.Error())
	}
	for _, o := range outcomes {
		recordOutcome(o.Change.Name, o.Result.Kind, added, updated)
	}
}

// skillItems builds the SkillItem set from the vault's skill resources,
// mirroring the init path: each skill's VaultPath is its canonical
// Templates/skills/<name>/SKILL.md. Unresolvable skills are skipped silently
// (lower tiers may have structural issues surfaced elsewhere).
func (r *ReconcileReport) skillItems(resolver *vpctx.Resolver) []SkillItem {
	names, err := resolver.ListResourcesScoped("skill", "", "", "")
	if err != nil {
		r.Errors = append(r.Errors, "list skills: "+err.Error())
		return nil
	}
	items := make([]SkillItem, 0, len(names))
	for _, ri := range names {
		sd, _, err := resolver.ResolveSkillDir(ri.Name, "", "", "")
		if err != nil {
			continue
		}
		items = append(items, SkillItem{
			Name:        ri.Name,
			Frontmatter: sd.Frontmatter,
			VaultPath:   filepath.Join(resolver.VaultRoot(), "Templates", "skills", ri.Name, "SKILL.md"),
		})
	}
	return items
}

// recordOutcome folds one apply outcome into the added/updated name sets.
// Names are deduplicated across targets, so a capability wired on two hosts
// counts once.
func recordOutcome(name string, kind ResultKind, added, updated map[string]bool) {
	if name == "" {
		return
	}
	switch kind {
	case Added:
		added[name] = true
	case Updated:
		updated[name] = true
	}
}

// sortedNames returns the map's keys sorted; nil for an empty set so the JSON
// omitempty tags keep a no-op reconcile's report clean.
func sortedNames(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
