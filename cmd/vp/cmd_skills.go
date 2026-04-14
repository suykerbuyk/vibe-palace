// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

// cmdSkills is the top-level dispatcher for `vp skills ...`. It mirrors
// the layout of `vp commands`: the bare `vp skills` command prints
// usage and delegates real work to the sub-subcommands.
func cmdSkills() *cli.Command {
	return &cli.Command{
		Name:        "skills",
		Synopsis:    "vp skills <command> [flags]",
		Description: "List the directory-form skills available for this project, inspect SKILL.md / reference bodies, and upgrade vault-level copies against embedded defaults.",
		Subcommands: []string{"skills list", "skills show", "skills upgrade"},
		Run: func(args []string) int {
			fmt.Fprintln(os.Stderr, "Usage: vp skills <command> [flags]\n\nRun 'vp skills --help' for details.")
			return cli.ExitOK
		},
	}
}

var skillsListFlags = []cli.FlagDef{
	{Name: "--json", Help: "Emit machine-readable JSON instead of a formatted table"},
	{Name: "--project", Arg: "SLUG", Help: "Resolve with project-tier overrides for SLUG"},
	{Name: "--wing", Arg: "SLUG", Help: "Resolve with wing-tier overrides (requires --project)"},
	{Name: "--room", Arg: "SLUG", Help: "Resolve with room-tier overrides (requires --wing)"},
}

// cmdSkillsList mirrors `vp commands list` but for directory-form skills.
// The underlying resolver already knows how to enumerate skills across
// all five precedence tiers; we simply render the same Summary shape.
func cmdSkillsList() *cli.Command {
	return &cli.Command{
		Name:        "skills list",
		Synopsis:    "vp skills list [--json] [--project SLUG [--wing SLUG [--room SLUG]]]",
		Description: "List every directory-form skill visible to this project, with the precedence tier that provides SKILL.md. Mirrors vp_list_skills.",
		Flags:       skillsListFlags,
		Examples: []cli.Example{
			{Cmd: "vp skills list", Comment: "List skills using embedded + vault tiers"},
			{Cmd: "vp skills list --json", Comment: "Emit JSON for scripts"},
			{Cmd: "vp skills list --project myapp", Comment: "Include project-tier overrides"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(skillsListFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp skills list: %v\n", err)
				return cli.ExitUser
			}

			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp skills list: %v\n", err)
				return cli.ExitUser
			}
			resolver := vpctx.NewResolver(vault.Root)

			summaries, err := commands.List(
				resolver, "skill",
				fv.Get("--project"), fv.Get("--wing"), fv.Get("--room"),
				60,
			)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp skills list: %v\n", err)
				return cli.ExitUser
			}

			if fv.Bool("--json") {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(summaries); err != nil {
					fmt.Fprintf(os.Stderr, "vp skills list: %v\n", err)
					return cli.ExitSystem
				}
				return cli.ExitOK
			}

			printSkillsTable(os.Stdout, summaries, fv.Get("--project"))
			return cli.ExitOK
		},
	}
}

func printSkillsTable(w io.Writer, summaries []commands.Summary, project string) {
	if len(summaries) == 0 {
		fmt.Fprintln(w, "No skills available.")
		return
	}
	maxName := 0
	for _, s := range summaries {
		if len(s.Name) > maxName {
			maxName = len(s.Name)
		}
	}
	if project != "" {
		fmt.Fprintf(w, "Skills available for project %q:\n\n", project)
	} else {
		fmt.Fprintln(w, "Skills available:")
		fmt.Fprintln(w)
	}
	for _, s := range summaries {
		fmt.Fprintf(w, "  %-*s  %-8s  %s\n", maxName, s.Name, s.Source, s.Brief)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, `Use "vp skills show <name>" to view SKILL.md and its references,`)
	fmt.Fprintln(w, `or "vp_skill <name>" inside an AI session to activate it.`)
}

var skillsShowFlags = []cli.FlagDef{
	{Name: "--section", Arg: "NAME", Help: "Show a specific reference (references/<NAME>.md) instead of SKILL.md"},
	{Name: "--project", Arg: "SLUG", Help: "Resolve with project-tier overrides for SLUG"},
	{Name: "--wing", Arg: "SLUG", Help: "Resolve with wing-tier overrides (requires --project)"},
	{Name: "--room", Arg: "SLUG", Help: "Resolve with room-tier overrides (requires --wing)"},
}

// cmdSkillsShow prints either the stripped SKILL.md body plus a
// references list, or — when --section=<name> is supplied — the raw
// body of that one reference file. The latter matches the MCP
// vp_get_skill_section tool byte-for-byte.
func cmdSkillsShow() *cli.Command {
	return &cli.Command{
		Name:        "skills show",
		Synopsis:    "vp skills show <name> [--section NAME] [--project SLUG [--wing SLUG [--room SLUG]]]",
		Description: "Print the SKILL.md body for the named skill along with its reference list. With --section, print just that reference's body.",
		Flags:       skillsShowFlags,
		Examples: []cli.Example{
			{Cmd: "vp skills show startup-analyst", Comment: "Show SKILL.md + references list"},
			{Cmd: "vp skills show startup-analyst --section capex-opex", Comment: "Show a single reference body"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(skillsShowFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp skills show: %v\n", err)
				return cli.ExitUser
			}
			pos := fv.Args()
			if len(pos) != 1 {
				fmt.Fprintln(os.Stderr, "vp skills show: exactly one skill name is required")
				return cli.ExitUser
			}
			name := pos[0]

			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp skills show: %v\n", err)
				return cli.ExitUser
			}
			resolver := vpctx.NewResolver(vault.Root)

			return runSkillsShow(os.Stdout, os.Stderr, resolver, skillsShowOpts{
				Name:    name,
				Section: fv.Get("--section"),
				Project: fv.Get("--project"),
				Wing:    fv.Get("--wing"),
				Room:    fv.Get("--room"),
			})
		},
	}
}

type skillsShowOpts struct {
	Name    string
	Section string
	Project string
	Wing    string
	Room    string
}

// runSkillsShow is the testable core of `vp skills show`. Keeping the
// filesystem / resolver dependencies explicit makes it easy to exercise
// both the SKILL.md path and the --section path from unit tests.
func runSkillsShow(stdout, stderr io.Writer, resolver *vpctx.Resolver, opts skillsShowOpts) int {
	if opts.Section != "" {
		data, source, err := resolver.ResolveSkillSection(
			opts.Name, opts.Section, opts.Project, opts.Wing, opts.Room,
		)
		if err != nil {
			fmt.Fprintf(stderr, "vp skills show: %v\n", err)
			return cli.ExitUser
		}
		fmt.Fprintf(stdout, "# skill: %s | section: %s | source: %s\n\n",
			opts.Name, opts.Section, source)
		stdout.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			fmt.Fprintln(stdout)
		}
		return cli.ExitOK
	}

	sd, source, err := resolver.ResolveSkillDir(
		opts.Name, opts.Project, opts.Wing, opts.Room,
	)
	if err != nil {
		fmt.Fprintf(stderr, "vp skills show: %v\n", err)
		return cli.ExitUser
	}
	fmt.Fprintf(stdout, "# skill: %s | source: %s\n\n", opts.Name, source)
	stdout.Write(sd.SkillMDBody)
	if len(sd.SkillMDBody) > 0 && sd.SkillMDBody[len(sd.SkillMDBody)-1] != '\n' {
		fmt.Fprintln(stdout)
	}
	if len(sd.ReferenceNames) > 0 {
		refs := append([]string(nil), sd.ReferenceNames...)
		sort.Strings(refs)
		fmt.Fprintln(stdout, "\nReferences (fetch with --section=<name>):")
		for _, r := range refs {
			fmt.Fprintf(stdout, "  - %s\n", r)
		}
	}
	return cli.ExitOK
}

var skillsUpgradeFlags = []cli.FlagDef{
	{Name: "--dry-run", Help: "Print the upgrade plan without writing"},
	{Name: "--overwrite", Help: "Accept every change without prompting (required in non-TTY)"},
	{Name: "--only", Arg: "NAME", Help: "Upgrade only the named skill (all files under it)"},
	{Name: "--granular", Help: "Prompt per file instead of per skill directory"},
}

// cmdSkillsUpgrade mirrors `vp commands upgrade` but operates on the
// directory-form skill corpus. By default prompts are grouped per skill
// directory — SKILL.md plus every references/*.md file share one
// accept/skip prompt. --granular restores the per-file prompt for
// surgical reviews.
func cmdSkillsUpgrade() *cli.Command {
	return &cli.Command{
		Name:        "skills upgrade",
		Synopsis:    "vp skills upgrade [--dry-run] [--overwrite] [--only NAME] [--granular]",
		Description: "Compare embedded skill templates against the vault copy (tier 4) and, for each skill directory, show a unified diff of affected files and prompt to accept, skip, or accept-all. Project/wing/room overrides are never touched.",
		Flags:       skillsUpgradeFlags,
		Examples: []cli.Example{
			{Cmd: "vp skills upgrade", Comment: "Interactive upgrade, one prompt per skill"},
			{Cmd: "vp skills upgrade --dry-run", Comment: "Show the plan without writing"},
			{Cmd: "vp skills upgrade --overwrite", Comment: "Accept every change without prompting"},
			{Cmd: "vp skills upgrade --only startup-analyst", Comment: "Upgrade a single skill's files"},
			{Cmd: "vp skills upgrade --granular", Comment: "Prompt per file instead of per skill"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(skillsUpgradeFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp skills upgrade: %v\n", err)
				return cli.ExitUser
			}
			return runSkillsUpgrade(skillsUpgradeOpts{
				DryRun:    fv.Bool("--dry-run"),
				Overwrite: fv.Bool("--overwrite"),
				Only:      fv.Get("--only"),
				Granular:  fv.Bool("--granular"),
				Stdin:     os.Stdin,
				Stdout:    os.Stdout,
				Stderr:    os.Stderr,
			})
		},
	}
}

type skillsUpgradeOpts struct {
	DryRun    bool
	Overwrite bool
	Only      string
	Granular  bool
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	// InteractiveOverride, when non-nil, forces interactive on/off for tests.
	InteractiveOverride *bool
	// VaultRootOverride, when non-empty, bypasses openProjectVault().
	VaultRootOverride string
	// ProjectRootOverride, when non-empty, replaces os.Getwd().
	ProjectRootOverride string
}

// skillGroupID returns the skill name for a nested skill-file change —
// the first path segment before the first "/" in c.Name. Used as the
// default GroupBy for the skills-upgrade prompt so SKILL.md and every
// reference share one accept/skip prompt.
func skillGroupID(c commands.Change) string {
	n := c.Name
	if i := strings.IndexByte(n, '/'); i >= 0 {
		return n[:i]
	}
	return n
}

func runSkillsUpgrade(opts skillsUpgradeOpts) int {
	vaultRoot := opts.VaultRootOverride
	if vaultRoot == "" {
		vault, err := openProjectVault()
		if err != nil {
			fmt.Fprintf(opts.Stderr, "vp skills upgrade: %v\n", err)
			return cli.ExitUser
		}
		vaultRoot = vault.Root
	}
	resolver := vpctx.NewResolver(vaultRoot)

	plan, err := commands.Plan(resolver, commands.PlanOptions{
		ResourceTypes: []string{"skill"},
		Only:          opts.Only,
	})
	if err != nil {
		fmt.Fprintf(opts.Stderr, "vp skills upgrade: %v\n", err)
		return cli.ExitUser
	}

	added, updated, unchanged := 0, 0, 0
	for _, c := range plan {
		switch c.Kind {
		case commands.ChangeNew:
			added++
		case commands.ChangeUpdated:
			updated++
		case commands.ChangeUnchanged:
			unchanged++
		}
	}

	if opts.DryRun {
		printSkillsUpgradePlan(opts.Stdout, plan, opts.Granular)
		fmt.Fprintf(opts.Stdout,
			"\nSummary (dry run): %d new, %d updated, %d unchanged.\n",
			added, updated, unchanged)
		if added+updated > 0 {
			return cli.ExitUser
		}
		return cli.ExitOK
	}

	interactive := isTerminal(os.Stdin) && !opts.Overwrite
	if opts.InteractiveOverride != nil {
		interactive = *opts.InteractiveOverride && !opts.Overwrite
	}
	if !opts.Overwrite && !interactive {
		if added+updated == 0 {
			fmt.Fprintln(opts.Stdout, "All embedded skills match. Nothing to do.")
			return cli.ExitOK
		}
		fmt.Fprintln(opts.Stderr,
			"vp skills upgrade: stdin is not a terminal and --overwrite was not set.")
		fmt.Fprintln(opts.Stderr,
			"Re-run with --overwrite to accept every change, or --dry-run to preview.")
		return cli.ExitUser
	}

	reader := bufio.NewReader(opts.Stdin)

	// GroupBy: by default collapse to the skill directory; --granular
	// returns identity so every file becomes its own prompt.
	groupBy := skillGroupID
	if opts.Granular {
		groupBy = func(c commands.Change) string { return c.Name }
	}

	promptRes := runUpgradePrompt(plan, UpgradePromptOpts{
		GroupBy:      groupBy,
		RenderHeader: renderSkillHeader(opts.Granular),
		RenderBody:   renderSkillBody(opts.Granular),
		AcceptAll:    opts.Overwrite,
		Reader:       reader,
		Stdout:       opts.Stdout,
		Stderr:       opts.Stderr,
	})

	// Write-back: the commands.Apply path handles .bak emission for
	// user-modified vault copies via writeAtomicWithBackup — this is
	// the same Apply used by commands.
	if err := commands.ApplyWithBackup(promptRes.Accepted); err != nil {
		fmt.Fprintf(opts.Stderr, "apply skills: %v\n", err)
		return cli.ExitSystem
	}

	if promptRes.Quit {
		fmt.Fprintln(opts.Stdout, "Aborting — no further changes applied.")
	}
	fmt.Fprintf(opts.Stdout,
		"\nDone. %d accepted, %d skipped.\n",
		promptRes.AcceptedCount, promptRes.SkippedCount)
	return cli.ExitOK
}

func renderSkillHeader(granular bool) func(w io.Writer, groupID string, group []commands.Change) {
	return func(w io.Writer, groupID string, group []commands.Change) {
		if granular || len(group) == 1 {
			c := group[0]
			fmt.Fprintf(w, "\n=== %s (%s) ===\n", c.Name, c.Kind)
			return
		}
		// Per-skill group: header names the skill and enumerates the
		// affected files with their individual kinds.
		fmt.Fprintf(w, "\n=== skill %s (%d files) ===\n", groupID, len(group))
		for _, c := range group {
			rel := strings.TrimPrefix(c.Name, groupID+"/")
			fmt.Fprintf(w, "  - %s (%s)\n", rel, c.Kind)
		}
	}
}

func renderSkillBody(granular bool) func(w io.Writer, groupID string, group []commands.Change) {
	return func(w io.Writer, groupID string, group []commands.Change) {
		for _, c := range group {
			if !granular && len(group) > 1 {
				fmt.Fprintf(w, "\n--- %s ---\n", c.Name)
			}
			if c.Kind == commands.ChangeUpdated {
				diff := commands.RenderUnified(
					"vault/"+c.Name,
					"embedded/"+c.Name,
					c.VaultContent, c.EmbeddedContent,
				)
				fmt.Fprint(w, diff)
			} else {
				fmt.Fprintf(w, "(new file; %d bytes will be added)\n",
					len(c.EmbeddedContent))
			}
		}
	}
}

// printSkillsUpgradePlan emits a dry-run-style plan. When granular is
// false, entries are grouped by skill directory so the user sees one
// block per skill.
func printSkillsUpgradePlan(w io.Writer, plan []commands.Change, granular bool) {
	if len(plan) == 0 {
		fmt.Fprintln(w, "No embedded skill files to compare.")
		return
	}
	fmt.Fprintln(w, "Skills upgrade plan:")
	if granular {
		for _, c := range plan {
			printPlanLine(w, c)
		}
		return
	}
	// Group by skill (first segment). Within each group, list member
	// files in their plan-order.
	type grp struct {
		id     string
		member []commands.Change
	}
	var groups []grp
	idx := map[string]int{}
	for _, c := range plan {
		id := skillGroupID(c)
		if i, ok := idx[id]; ok {
			groups[i].member = append(groups[i].member, c)
			continue
		}
		idx[id] = len(groups)
		groups = append(groups, grp{id: id, member: []commands.Change{c}})
	}
	for _, g := range groups {
		fmt.Fprintf(w, "  skill %s:\n", g.id)
		for _, c := range g.member {
			rel := strings.TrimPrefix(c.Name, g.id+"/")
			switch c.Kind {
			case commands.ChangeNew:
				fmt.Fprintf(w, "    new       %s  (hash %s)\n", rel, c.EmbeddedHash)
			case commands.ChangeUpdated:
				fmt.Fprintf(w, "    updated   %s  (vault %s → embedded %s)\n",
					rel, c.VaultHash, c.EmbeddedHash)
			case commands.ChangeUnchanged:
				fmt.Fprintf(w, "    unchanged %s\n", rel)
			}
		}
	}
}

func printPlanLine(w io.Writer, c commands.Change) {
	switch c.Kind {
	case commands.ChangeNew:
		fmt.Fprintf(w, "  new       %s  (hash %s)\n", c.Name, c.EmbeddedHash)
	case commands.ChangeUpdated:
		fmt.Fprintf(w, "  updated   %s  (vault %s → embedded %s)\n",
			c.Name, c.VaultHash, c.EmbeddedHash)
	case commands.ChangeUnchanged:
		fmt.Fprintf(w, "  unchanged %s\n", c.Name)
	}
}
