// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// The reset verbs are the only way vp removes a vault Templates/ override of a
// built-in. `vp commands upgrade` and `vp skills upgrade` list overrides and
// never change one, in any mode; resetting one is a separate, named operation
// — the positional NAME is the consent, the same in a terminal and in a pipe,
// as it is for `vp vault delete <path>` and `vp worktree remove <slug>`.
//
// A reset REMOVES the override, so the embedded floor serves the resource
// again. It never writes the embedded bytes into Templates/: that would be the
// Tier-4 mirror ADR-008's override-only model forbids, which the next
// `vp config sync` would then prune.

var templateResetFlags = []cli.FlagDef{
	{Name: "--dry-run", Help: "Print each file's diff (vault → embedded) and the exact backup name, without writing anything"},
}

func cmdCommandsReset() *cli.Command {
	return &cli.Command{
		Name:     "commands reset",
		Synopsis: "vp commands reset NAME... [--dry-run]",
		Description: "Remove the vault Templates/commands/ override of each named built-in command, so the embedded copy serves it again. " +
			"Each override is first backed up to <file>.<sha12>.bak, a name derived from its bytes that is never overwritten. " +
			"On a vault that is its own git repository the removal is committed locally (not pushed). " +
			"Every name is checked before anything is written. Project, wing and room overrides are never touched.",
		Flags: templateResetFlags,
		Examples: []cli.Example{
			{Cmd: "vp commands reset wrap", Comment: "Remove the vault override of the built-in wrap command"},
			{Cmd: "vp commands reset wrap restart --dry-run", Comment: "Show what would be removed and backed up"},
		},
		Run: func(args []string) int {
			return runTemplateResetArgs("command", args)
		},
	}
}

func cmdSkillsReset() *cli.Command {
	return &cli.Command{
		Name:     "skills reset",
		Synopsis: "vp skills reset NAME... [--dry-run]",
		Description: "Remove the vault Templates/skills/ override of each named built-in skill (every built-in file under it) or skill file (e.g. chair/references/x.md), so the embedded copy serves it again. " +
			"Each override is first backed up to <file>.<sha12>.bak, a name derived from its bytes that is never overwritten. " +
			"On a vault that is its own git repository the removal is committed locally (not pushed). " +
			"Files you added to a skill directory are left in place. Project, wing and room overrides are never touched.",
		Flags: templateResetFlags,
		Examples: []cli.Example{
			{Cmd: "vp skills reset chair", Comment: "Remove every vault override file of the built-in chair skill"},
			{Cmd: "vp skills reset chair/SKILL.md --dry-run", Comment: "Show what resetting one file would do"},
		},
		Run: func(args []string) int {
			return runTemplateResetArgs("skill", args)
		},
	}
}

func runTemplateResetArgs(resourceType string, args []string) int {
	verb := resetVerb(resourceType)
	fv, err := cli.ParseFlags(templateResetFlags, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", verb, err)
		return cli.ExitUser
	}
	return runTemplateReset(templateResetOpts{
		ResourceType: resourceType,
		Names:        fv.Args(),
		DryRun:       fv.Bool("--dry-run"),
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
	})
}

type templateResetOpts struct {
	// ResourceType is "command" or "skill".
	ResourceType string
	// Names are the positional NAMEs, as typed.
	Names  []string
	DryRun bool
	Stdout io.Writer
	Stderr io.Writer
	// VaultRootOverride, when non-empty, bypasses openProjectVault(). Test-only
	// seam.
	VaultRootOverride string
}

func resetVerb(resourceType string) string {
	if resourceType == "skill" {
		return "vp skills reset"
	}
	return "vp commands reset"
}

// resetTarget is one named built-in file with no vault copy in the worktree.
type resetTarget struct {
	change commands.Change
	rel    string
}

// resetPlan is everything a reset — or its dry run — decided before writing.
type resetPlan struct {
	present  []commands.Change // vault copy exists: removed by the reset
	pending  []resetTarget     // absent from the worktree, still in HEAD: commit only
	absent   []resetTarget     // nothing to do
	tracked  map[string]bool   // present rels HEAD holds (own repository only)
	gitState storage.VaultGit
}

// runTemplateReset validates every name, checks every path, preflights git,
// then resets, commits and reports — or, with --dry-run, reports what the reset
// would do after the SAME checks and preflights, so a dry run refuses exactly
// where the real run would, with the same exit status. Exit status: 0 on
// success (including "nothing to reset"), 1 for a usage problem or a refused
// path, 2 when anything could not be done.
func runTemplateReset(opts templateResetOpts) int {
	verb := resetVerb(opts.ResourceType)
	if len(opts.Names) == 0 {
		fmt.Fprintf(opts.Stderr, "%s: name at least one built-in %s to reset\n", verb, opts.ResourceType)
		return cli.ExitUser
	}
	vaultRoot := opts.VaultRootOverride
	if vaultRoot == "" {
		vault, err := openProjectVault()
		if err != nil {
			fmt.Fprintf(opts.Stderr, "%s: %v\n", verb, err)
			return cli.ExitUser
		}
		vaultRoot = vault.Root
	}
	resolver := vpctx.NewResolver(vaultRoot)
	invocation := verb + " " + strings.Join(opts.Names, " ")

	// 1. Every name is validated before anything is read for writing: one
	// unknown name refuses the whole invocation. Overlapping names (chair and
	// chair/SKILL.md) are de-duplicated; each name remembers the paths it
	// selected, so "nothing to reset" is said once per name.
	var unknown, names []string
	nameRels := map[string][]string{}
	seen := map[string]bool{}
	var changes []commands.Change
	for _, raw := range opts.Names {
		name := strings.TrimSuffix(raw, "/")
		plan, err := commands.Plan(resolver, commands.PlanOptions{
			ResourceTypes: []string{opts.ResourceType},
			Only:          name,
		})
		if name == "" || errors.Is(err, commands.ErrNoEmbeddedTemplate) {
			unknown = append(unknown, raw)
			continue
		}
		if err != nil {
			fmt.Fprintf(opts.Stderr, "%s: %v\nNothing was changed.\n", verb, err)
			return cli.ExitSystem
		}
		if _, dup := nameRels[name]; !dup {
			names = append(names, name)
		}
		for _, c := range plan {
			nameRels[name] = append(nameRels[name], vaultRel(vaultRoot, c.VaultPath))
			if !seen[c.VaultPath] {
				seen[c.VaultPath] = true
				changes = append(changes, c)
			}
		}
	}
	if len(unknown) > 0 {
		for _, n := range unknown {
			fmt.Fprintf(opts.Stderr, "%s: no built-in %s named %q. A vault-wide %s with a new name is not an override of a built-in; it is yours to delete (vp vault delete <path>).\n",
				verb, resetNoun(opts.ResourceType), n, opts.ResourceType)
		}
		fmt.Fprintln(opts.Stderr, "Nothing was changed.")
		return cli.ExitUser
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Name < changes[j].Name })

	plan := resetPlan{tracked: map[string]bool{}}
	for _, c := range changes {
		if c.Kind == commands.ChangeUnneeded {
			plan.absent = append(plan.absent, resetTarget{change: c, rel: vaultRel(vaultRoot, c.VaultPath)})
			continue
		}
		plan.present = append(plan.present, c)
	}
	// The project whose higher tiers the notes below name: the same cwd default
	// vp_skill and `vp skills show` resolve (a slug only when the vault holds
	// Projects/<slug>/). A detected slug with no project directory has no
	// project tier, so the notes read the same either way.
	slug := storage.NewVault(vaultRoot).DetectedProject(mustGetwd())

	// 2. Every path is checked before any write: a symlink in any component,
	// or a non-regular file, refuses the whole invocation (Reset repeats this
	// as its own phase 1a; a dry run stops here too).
	if err := commands.CheckResetPaths(plan.present); err != nil {
		fmt.Fprintf(opts.Stderr, "%s: %v\nNothing was removed.\n", verb, err)
		if errors.Is(err, commands.ErrUnsafeResetPath) {
			return cli.ExitUser
		}
		return cli.ExitSystem
	}

	// 3. Git preflight, before any write.
	if code := preflightTemplateReset(vaultRoot, verb, &plan, opts.Stderr); code != cli.ExitOK {
		return code
	}

	if opts.DryRun {
		printResetDryRun(opts.Stdout, plan.present)
		for _, t := range plan.pending {
			fmt.Fprintf(opts.Stdout, "would commit %s: already removed from the worktree, but the removal was never committed\n", t.rel)
		}
		if plan.gitState == storage.VaultGitOK && (len(plan.pending) > 0 || anyTracked(plan.tracked)) {
			fmt.Fprintln(opts.Stdout, "would commit the removal of the tracked files locally (not pushed)")
		}
		printNothingToReset(opts.Stdout, resolver, opts.ResourceType, names, nameRels, plan, slug)
		printExtraSkillFiles(opts.Stdout, resolver, vaultRoot, opts.ResourceType, changes)
		fmt.Fprintln(opts.Stdout, "(dry run: nothing was written)")
		return cli.ExitOK
	}

	// 4. Reset: every path checked again, every backup written, then removals.
	outcomes, err := commands.Reset(plan.present)
	if err != nil {
		fmt.Fprintf(opts.Stderr, "%s: %v\nNothing was removed.\n", verb, err)
		if errors.Is(err, commands.ErrUnsafeResetPath) {
			return cli.ExitUser
		}
		return cli.ExitSystem
	}

	code := cli.ExitOK
	var committable []resetCommitEntry
	for _, o := range outcomes {
		switch {
		case o.Removed:
			what := "removed your override"
			if o.Mirror {
				what = "removed it" // vp-shipped bytes, not an override
			}
			fmt.Fprintf(opts.Stdout, "reset %s: %s; the built-in now serves it (%s)%s\n",
				o.Rel, what, resetBackupPhrase(o), higherTierNote(resolver, opts.ResourceType, o.Name, slug))
			if plan.tracked[o.Rel] {
				committable = append(committable, resetCommitEntry{Rel: o.Rel, Backup: o.Backup, Mirror: o.Mirror,
					Provenance: o.Provenance, LineEndings: o.LineEndings})
			}
		case o.AlreadyGone:
			fmt.Fprintf(opts.Stdout, "reset %s: already gone; nothing was removed\n", o.Rel)
		case o.Kept != "":
			fmt.Fprintf(opts.Stderr, "reset %s: %s\n", o.Rel, o.Kept)
			code = cli.ExitSystem
		case o.Err != nil:
			fmt.Fprintf(opts.Stderr, "reset %s: removal failed: %v", o.Rel, o.Err)
			if o.Backup != "" {
				fmt.Fprintf(opts.Stderr, " (its bytes are in %s; run the reset again to finish)", o.Backup)
			}
			fmt.Fprintln(opts.Stderr)
			code = cli.ExitSystem
		}
		if o.Backup != "" && !o.BackupReused && plan.gitState == storage.VaultGitOK {
			if ignored, err := storage.GitPathIgnored(vaultRoot, o.Backup); err == nil && !ignored {
				fmt.Fprintf(opts.Stderr, "warning: git does not ignore the backup %s, so it shows as untracked; `vp config sync` restores the vault's canonical ignore lines (*.bak).\n", o.Backup)
			}
		}
	}
	for _, t := range plan.pending {
		fmt.Fprintf(opts.Stdout, "reset %s: already removed from the worktree, but the removal was never committed; committing it now\n", t.rel)
		committable = append(committable, resetCommitEntry{Rel: t.rel, Pending: true, Backups: existingBackups(vaultRoot, t.rel)})
	}
	printNothingToReset(opts.Stdout, resolver, opts.ResourceType, names, nameRels, plan, slug)
	printExtraSkillFiles(opts.Stdout, resolver, vaultRoot, opts.ResourceType, changes)

	// 5. Commit (vault's own repository only), per the vault's git shape.
	if c := commitTemplateReset(vaultRoot, plan.gitState, committable, removedAny(outcomes), verb, invocation, opts.Stdout, opts.Stderr); c != cli.ExitOK {
		code = c
	}

	if commands.ShimSourceRemoved(outcomes) {
		fmt.Fprintln(opts.Stdout, "Shims in each project may still carry the removed override's text (command brief / skill description). Run `vp commands upgrade --overwrite` (or `vp init`) in each project.")
	}
	return code
}

// preflightTemplateReset inspects the vault's git shape and refuses, before
// anything is written, what the commit could not carry: an unreadable
// repository, no identity when a tracked file would be committed, or a staged
// change on a target. It fills plan.gitState, plan.tracked and plan.pending
// (a named built-in absent from the worktree but still in HEAD: a removal left
// uncommitted, which the reset finishes). A staged DELETION of a pending
// target is accepted — it holds no bytes, and it is the removal about to be
// committed (the state CommitRemovals leaves if its own unstage fails).
func preflightTemplateReset(vaultRoot, verb string, plan *resetPlan, errw io.Writer) int {
	gitState, gitErr := storage.InspectVaultGit(vaultRoot)
	plan.gitState = gitState
	if gitState == storage.VaultGitBroken {
		fmt.Fprintf(errw, "%s: the vault's git repository cannot be used (%v); refusing before any change.\n", verb, gitErr)
		return cli.ExitSystem
	}
	if gitState != storage.VaultGitOK {
		return cli.ExitOK
	}
	needIdentity := false
	for _, c := range plan.present {
		rel := vaultRel(vaultRoot, c.VaultPath)
		_, found, err := storage.ReadCommittedBlob(vaultRoot, rel)
		if err != nil {
			fmt.Fprintf(errw, "%s: cannot read HEAD's copy of %s (%v); refusing before any change.\n", verb, rel, err)
			return cli.ExitSystem
		}
		plan.tracked[rel] = found
		needIdentity = needIdentity || found
	}
	var stillAbsent []resetTarget
	for _, t := range plan.absent {
		_, found, err := storage.ReadCommittedBlob(vaultRoot, t.rel)
		if err != nil {
			fmt.Fprintf(errw, "%s: cannot read HEAD's copy of %s (%v); refusing before any change.\n", verb, t.rel, err)
			return cli.ExitSystem
		}
		if found {
			plan.pending = append(plan.pending, t)
			needIdentity = true
			continue
		}
		stillAbsent = append(stillAbsent, t)
	}
	plan.absent = stillAbsent
	if needIdentity {
		// A reset that would commit is refused here, before any file is
		// removed, when the host config disables git: CommitRemovals' own gate
		// runs only after the removal, which would strand a tracked deletion.
		// This preflight runs ahead of the dry-run branch, so a dry run refuses
		// exactly like the real run. The reads above only decide whether a
		// commit would happen at all.
		if err := storage.RefuseIfGitDisabled(vaultRoot, "commit the template reset"); err != nil {
			fmt.Fprintf(errw, "%s: %v\nThe removal could not be committed, so nothing was changed.\n", verb, err)
			return cli.ExitUser
		}
		if err := storage.CheckCommitIdentity(vaultRoot); err != nil {
			fmt.Fprintf(errw, "%s: %v\nThe removal could not be committed, so nothing was changed.\n", verb, err)
			return cli.ExitSystem
		}
	}
	refuse := func(rel string, err error) int {
		if err != nil {
			fmt.Fprintf(errw, "%s: cannot read the index for %s (%v); refusing before any change.\n", verb, rel, err)
		} else {
			fmt.Fprintf(errw, "%s: the index holds a staged change for %s. Committing its removal would drop the staged bytes, which no backup holds; commit or unstage it first (git -C %s restore --staged -- %s). Nothing was changed.\n",
				verb, rel, vaultRoot, rel)
		}
		return cli.ExitSystem
	}
	for _, c := range plan.present {
		rel := vaultRel(vaultRoot, c.VaultPath)
		if staged, err := storage.StagedChange(vaultRoot, rel); err != nil || staged {
			return refuse(rel, err)
		}
	}
	for _, t := range plan.pending {
		staged, err := storage.StagedChange(vaultRoot, t.rel)
		if err != nil {
			return refuse(t.rel, err)
		}
		if !staged {
			continue
		}
		deletion, err := storage.StagedDeletion(vaultRoot, t.rel)
		if err != nil || !deletion {
			return refuse(t.rel, err)
		}
	}
	return cli.ExitOK
}

func anyTracked(tracked map[string]bool) bool {
	for _, t := range tracked {
		if t {
			return true
		}
	}
	return false
}

// printNothingToReset says "nothing to reset" once per NAME whose every
// selected file has no vault copy and no pending removal. A skill named whole
// with some files reset says nothing about its other built-in files: that
// they have no vault copy is the normal case, not news.
func printNothingToReset(w io.Writer, resolver *vpctx.Resolver, resourceType string, names []string, nameRels map[string][]string, plan resetPlan, slug string) {
	absent := map[string]bool{}
	for _, t := range plan.absent {
		absent[t.rel] = true
	}
	for _, name := range names {
		all := len(nameRels[name]) > 0
		for _, rel := range nameRels[name] {
			all = all && absent[rel]
		}
		if all {
			fmt.Fprintf(w, "nothing to reset: %s\n",
				nothingToResetReason(resolver, commands.Change{ResourceType: resourceType, Name: name}, slug))
		}
	}
}

// existingBackups lists the content-named backups (<rel>.<12 hex>.bak) beside
// rel, as vault-relative paths — what an earlier reset of it left.
func existingBackups(vaultRoot, rel string) []string {
	dir := filepath.Join(vaultRoot, filepath.FromSlash(path.Dir(rel)))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	prefix := path.Base(rel) + "."
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, prefix) || !strings.HasSuffix(n, ".bak") {
			continue
		}
		mid := strings.TrimSuffix(strings.TrimPrefix(n, prefix), ".bak")
		if len(mid) != 12 || strings.Trim(mid, "0123456789abcdef") != "" {
			continue
		}
		out = append(out, path.Join(path.Dir(rel), n))
	}
	sort.Strings(out)
	return out
}

func resetNoun(resourceType string) string {
	if resourceType == "skill" {
		return "skill or skill file"
	}
	return "command"
}

func mustGetwd() string {
	wd, _ := os.Getwd()
	return wd
}

func vaultRel(vaultRoot, abs string) string {
	rel, _ := filepath.Rel(vaultRoot, abs)
	return filepath.ToSlash(rel)
}

func removedAny(outcomes []commands.ResetOutcome) bool {
	for _, o := range outcomes {
		if o.Removed {
			return true
		}
	}
	return false
}

func resetBackupPhrase(o commands.ResetOutcome) string {
	switch {
	case o.Mirror:
		return shippedPhrase(o.Rel, o.Provenance, o.LineEndings)
	case o.BackupReused:
		return "backup: " + o.Backup + ", which already held these bytes"
	default:
		return "backup: " + o.Backup
	}
}

// shippedPhrase says what vp-shipped bytes a reset removed without a backup:
// the built-in itself, or an earlier shipped version of it, which is
// recoverable from vibe-palace's history.
func shippedPhrase(rel string, prov templates.Provenance, lineEndings bool) string {
	switch {
	case prov == templates.ProvenanceEarlier:
		return "a copy of an earlier shipped version of " + strings.TrimPrefix(rel, "Templates/") +
			"; no backup needed — recoverable from vibe-palace history"
	case lineEndings:
		return "identical, line endings aside, to the built-in; no backup needed"
	default:
		return "identical to the built-in; no backup needed"
	}
}

// higherTierNote qualifies "the built-in now serves it" when, for the project
// this command runs in, a project-tier override still serves the resource. A
// reset never touches that tier. Wing and room tiers need a wing and room to
// resolve and are not consulted here.
func higherTierNote(resolver *vpctx.Resolver, resourceType, name, slug string) string {
	if tier, where := servingTier(resolver, resourceType, name, slug); tier != "" && tier != "embedded" && tier != "vault" {
		return fmt.Sprintf(" — for project %s the %s tier (%s) still serves it; a reset never touches that tier", slug, tier, where)
	}
	return ""
}

// nothingToResetReason says why a named built-in has nothing to reset, and
// which tier serves it for the detected project.
func nothingToResetReason(resolver *vpctx.Resolver, c commands.Change, slug string) string {
	tier, where := servingTier(resolver, c.ResourceType, c.Name, slug)
	if tier != "" && tier != "embedded" && tier != "vault" {
		return fmt.Sprintf("%s has no vault override; for project %s the %s tier (%s) serves it, and a reset never touches that tier", c.Name, slug, tier, where)
	}
	return fmt.Sprintf("%s has no vault override; the built-in already serves it", c.Name)
}

// servingTier resolves which tier serves the resource for project slug, and
// where the file for a project tier lives. It returns "" when it cannot tell
// (no project detected, or the lookup failed).
func servingTier(resolver *vpctx.Resolver, resourceType, name, slug string) (tier, where string) {
	if slug == "" {
		return "", ""
	}
	if resourceType == "skill" {
		skill, _, _ := strings.Cut(name, "/")
		_, source, err := resolver.ResolveSkillDir(skill, slug, "", "")
		if err != nil {
			return "", ""
		}
		return source, path.Join("Projects", slug, "skills", skill)
	}
	_, source, err := resolver.ResolveScoped("command:"+name, slug, "", "")
	if err != nil {
		return "", ""
	}
	return source, path.Join("Projects", slug, "commands", name+".md")
}

// printExtraSkillFiles lists the files a vault skill directory holds that are
// not built-in files of that skill — every file the binary embeds for it
// counts as built-in, named or not — because a reset never removes them.
func printExtraSkillFiles(w io.Writer, resolver *vpctx.Resolver, vaultRoot, resourceType string, changes []commands.Change) {
	if resourceType != "skill" {
		return
	}
	skills := map[string]bool{}
	for _, c := range changes {
		skill, _, _ := strings.Cut(c.Name, "/")
		skills[skill] = true
	}
	builtin := map[string]bool{}
	embedded, _ := resolver.ListEmbedded("skill")
	for _, n := range embedded {
		if skill, _, _ := strings.Cut(n, "/"); skills[skill] {
			builtin["Templates/skills/"+n] = true
		}
	}
	var names []string
	for s := range skills {
		names = append(names, s)
	}
	sort.Strings(names)
	for _, s := range names {
		dir := filepath.Join(vaultRoot, "Templates", "skills", s)
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
				return nil
			}
			rel := vaultRel(vaultRoot, p)
			if !builtin[rel] {
				fmt.Fprintf(w, "left %s: not a built-in file of %s; a reset never removes it\n", rel, s)
			}
			return nil
		})
	}
}

// printResetDryRun prints, for each file a reset would remove, its diff (vault
// → embedded) and the exact backup name it would get — or, for vp-shipped
// bytes (a mirror, an earlier shipped version), that it needs no backup,
// exactly as the real run decides. It writes nothing.
func printResetDryRun(w io.Writer, changes []commands.Change) {
	for _, c := range changes {
		rel := vaultRel(c.VaultRoot, c.VaultPath)
		switch c.Kind {
		case commands.ChangeUnchanged:
			fmt.Fprintf(w, "would reset %s: remove it (%s)\n", rel,
				shippedPhrase(rel, templates.ProvenanceCurrent, c.VaultContent != c.EmbeddedContent))
			continue
		case commands.ChangeStale:
			fmt.Fprintf(w, "would reset %s: remove it (%s)\n", rel, shippedPhrase(rel, templates.ProvenanceEarlier, false))
			continue
		}
		fmt.Fprintf(w, "would reset %s: remove it; backup: %s\n", rel, templates.BackupName(rel, []byte(c.VaultContent)))
		fmt.Fprint(w, commands.RenderUnified("vault/"+rel, "embedded/"+rel, c.VaultContent, c.EmbeddedContent))
	}
}

// resetCommitEntry is one path a reset commit carries.
type resetCommitEntry struct {
	Rel    string
	Backup string
	// Mirror: vp-shipped bytes, removed with no backup; Provenance and
	// LineEndings say which (see commands.ResetOutcome).
	Mirror      bool
	Provenance  templates.Provenance
	LineEndings bool
	// Pending: removed from the worktree by an earlier run whose commit did
	// not land; this run only commits it. Backups names the content-named
	// backups found beside it, which that earlier run may have written.
	Pending bool
	Backups []string
}

// commitTemplateReset commits a reset's removals according to the vault's git
// shape. Only a vault that is its own repository is ever committed, and only
// locally; the operator's `vp vault sync` publishes the commit.
func commitTemplateReset(vaultRoot string, gitState storage.VaultGit, entries []resetCommitEntry, removed bool, verb, invocation string, w, errw io.Writer) int {
	switch gitState {
	case storage.VaultGitOK:
		if len(entries) == 0 {
			return cli.ExitOK
		}
		rels := make([]string, len(entries))
		for i, e := range entries {
			rels[i] = e.Rel
		}
		host, _ := os.Hostname()
		if host == "" {
			host = "unknown"
		}
		res, err := storage.CommitRemovals(vaultRoot, templateResetCommitMessage(invocation, entries, host), rels)
		var left *storage.RemovalsLeftInHEADError
		switch {
		case errors.As(err, &left):
			// The commit landed; only some paths are still in HEAD.
			fmt.Fprintf(w, "committed the removal locally as %s (not pushed; `vp vault sync` publishes it)\n", left.SHA)
			fmt.Fprintf(errw, "%s: but HEAD still holds %s — something changed it while the commit ran.\n", verb, strings.Join(left.Paths, ", "))
			fmt.Fprintf(errw, "  To commit the rest: git -C %s commit -m \"chore(templates): finish an operator reset\" -- %s\n", vaultRoot, strings.Join(left.Paths, " "))
			fmt.Fprintf(errw, "  To restore the rest instead: git -C %s checkout HEAD -- %s\n", vaultRoot, strings.Join(left.Paths, " "))
			return cli.ExitSystem
		case err != nil:
			fmt.Fprintf(errw, "%s: removed, not committed: %v\n", verb, err)
			fmt.Fprintf(errw, "  To commit the removal: git -C %s commit -m \"chore(templates): finish an operator reset\" -- %s\n", vaultRoot, strings.Join(rels, " "))
			fmt.Fprintf(errw, "  To undo it instead:    git -C %s checkout HEAD -- %s\n", vaultRoot, strings.Join(rels, " "))
			fmt.Fprintln(errw, "  Running the same reset again also finishes the commit.")
			return cli.ExitSystem
		}
		if res != nil && res.CommitSHA != "" {
			fmt.Fprintf(w, "committed the removal locally as %s (not pushed; `vp vault sync` publishes it)\n", res.CommitSHA)
		}
		return cli.ExitOK
	case storage.VaultGitNested:
		if !removed {
			return cli.ExitOK
		}
		top, _ := storage.GitTopLevel(vaultRoot)
		fmt.Fprintf(w, "not committed: the vault is inside another repository (%s), and vp never commits a repository that is not the vault's own. The removal is left in that repository's working tree for its owner to commit.\n", top)
		return cli.ExitOK
	case storage.VaultGitUnavailable:
		if removed {
			fmt.Fprintln(errw, "warning: not committed: git is not on PATH, though the vault has a .git entry. Commit the removal with git once it is available.")
		}
		return cli.ExitOK
	default: // VaultNotGit: the removal and the backup are the whole reset.
		return cli.ExitOK
	}
}

// templateResetCommitMessage is the reset commit's message: what was removed,
// at whose request, and where each file's bytes still are. The subject counts
// files, not overrides: a named byte-identical mirror is removed too.
func templateResetCommitMessage(invocation string, entries []resetCommitEntry, host string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "chore(templates): operator reset of %d vault Templates/ file(s)\n\n", len(entries))
	fmt.Fprintf(&b, "`%s` removed these vault Templates/ files at the operator's request,\n", invocation)
	b.WriteString("so the embedded built-in serves each again. vp removed only the files\n")
	var backedUp, shipped bool
	for _, e := range entries {
		backedUp = backedUp || e.Backup != "" || len(e.Backups) > 0
		shipped = shipped || (e.Mirror && !e.Pending)
	}
	if backedUp {
		b.WriteString("named. The committed copy of each is in this commit's parent; the\n")
		fmt.Fprintf(&b, "working-tree bytes at reset time are in the backup named beside it, on\nhost %s.\n", host)
	} else {
		fmt.Fprintf(&b, "named, on host %s. The committed copy of each is in this commit's parent.\n", host)
	}
	if shipped {
		b.WriteString("A file whose line says \"no backup needed\" held vp-shipped bytes — the\n")
		b.WriteString("built-in's, or an earlier shipped version's, recoverable from the binary\n")
		b.WriteString("or from vibe-palace's history — so none was written.\n")
	}
	b.WriteString("\n")
	for _, e := range entries {
		switch {
		case e.Pending && len(e.Backups) > 0:
			fmt.Fprintf(&b, "- %s (removed by an earlier reset and left uncommitted; backup: %s)\n", e.Rel, strings.Join(e.Backups, ", "))
		case e.Pending:
			fmt.Fprintf(&b, "- %s (removed earlier and left uncommitted; no backup of it is on this host)\n", e.Rel)
		case e.Mirror:
			fmt.Fprintf(&b, "- %s (%s)\n", e.Rel, shippedPhrase(e.Rel, e.Provenance, e.LineEndings))
		default:
			fmt.Fprintf(&b, "- %s (backup: %s)\n", e.Rel, e.Backup)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
