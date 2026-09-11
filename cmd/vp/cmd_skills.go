// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
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
		Description: "List the directory-form skills available for this project, inspect SKILL.md / reference bodies, report vault Templates/skills overrides of built-in skills, and reset one when you name it.",
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
	{Name: "--dry-run", Help: "Print the report as a plan table"},
	{Name: "--overwrite", Help: "Accepted for compatibility; there is nothing to accept — vp skills upgrade never resets an override"},
	{Name: "--only", Arg: "NAME", Help: "Report only the named skill (all files under it)"},
	{Name: "--granular", Help: "List per file instead of per skill directory"},
}

// cmdSkillsUpgrade reports how the vault's Templates/skills/ copies compare to
// the built-in skills. It writes nothing: a vault override of a built-in skill
// is the operator's and is listed as [keep], and `vp skills reset NAME` is the
// only command that removes one. The skill SHIMS a project carries are `vp
// commands upgrade`'s.
func cmdSkillsUpgrade() *cli.Command {
	return &cli.Command{
		Name:        "skills upgrade",
		Synopsis:    "vp skills upgrade [--dry-run] [--overwrite] [--only NAME] [--granular]",
		Description: "Report every vault Templates/skills/ override of a built-in skill, one [keep] line per skill (per file with --granular). It never writes or removes a Templates/ file, in any mode: `vp skills reset NAME` removes an override on request, keeping a backup. Project skill shims are refreshed by `vp commands upgrade`.",
		Flags:       skillsUpgradeFlags,
		Examples: []cli.Example{
			{Cmd: "vp skills upgrade", Comment: "List the vault's skill overrides, one line per skill"},
			{Cmd: "vp skills upgrade --dry-run", Comment: "Show the comparison as a plan table"},
			{Cmd: "vp skills upgrade --only startup-analyst", Comment: "Report a single skill's files"},
			{Cmd: "vp skills upgrade --granular", Comment: "List per file instead of per skill"},
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
	Stdout    io.Writer
	Stderr    io.Writer
	// VaultRootOverride, when non-empty, bypasses openProjectVault().
	VaultRootOverride string
}

// skillGroupID returns the skill name for a nested skill-file change — the
// first path segment before the first "/" in c.Name. It groups SKILL.md and
// every reference of one skill into one line of the report.
func skillGroupID(c commands.Change) string {
	n := c.Name
	if before, _, ok := strings.Cut(n, "/"); ok {
		return before
	}
	return n
}

// runSkillsUpgrade is report-only. It reads the vault and prints; it never
// reads stdin and never writes, so every mode exits 0 unless the plan cannot
// be built.
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

	overrides, unchanged, unneeded := 0, 0, 0
	for _, c := range plan {
		switch c.Kind {
		case commands.ChangeOverride:
			overrides++
		case commands.ChangeUnchanged:
			unchanged++
		case commands.ChangeUnneeded:
			// See cmd_commands.go: unneeded is not unchanged.
			unneeded++
		}
	}

	if opts.Overwrite {
		fmt.Fprintln(opts.Stdout, "--overwrite: nothing to accept — vp skills upgrade never resets an override; use vp skills reset NAME")
	}

	if opts.DryRun {
		printSkillsUpgradePlan(opts.Stdout, plan, opts.Granular)
		fmt.Fprintf(opts.Stdout,
			"\nSummary (dry run): %d override(s) kept, %d unchanged, %d unneeded.\n",
			overrides, unchanged, unneeded)
		return cli.ExitOK
	}

	printSkillKeepLines(opts.Stdout, plan, opts.Granular)
	if overrides == 0 {
		fmt.Fprintln(opts.Stdout, "No vault Templates/skills override of a built-in skill. Nothing to do.")
		return cli.ExitOK
	}
	fmt.Fprintf(opts.Stdout, "\nDone. %d override file(s) of built-in skills kept; nothing was written.\n", overrides)
	return cli.ExitOK
}

// printSkillKeepLines prints the [keep] report: one line per skill with an
// override, naming the files that differ, or one line per file when granular.
func printSkillKeepLines(w io.Writer, plan []commands.Change, granular bool) {
	if granular {
		for _, c := range plan {
			if c.Kind != commands.ChangeOverride {
				continue
			}
			fmt.Fprintf(w, "[keep] Templates/skills/%s — override of a built-in (vault %s, embedded %s); vp skills upgrade never resets one — to remove it: vp skills reset %s\n",
				c.Name, c.VaultHash, c.EmbeddedHash, c.Name)
		}
		return
	}
	var order []string
	files := map[string][]string{}
	for _, c := range plan {
		if c.Kind != commands.ChangeOverride {
			continue
		}
		id := skillGroupID(c)
		if _, ok := files[id]; !ok {
			order = append(order, id)
		}
		files[id] = append(files[id], strings.TrimPrefix(c.Name, id+"/"))
	}
	for _, id := range order {
		fmt.Fprintf(w, "[keep] Templates/skills/%s/ — override of a built-in (%d file(s) differ: %s); vp skills upgrade never resets one — to remove it: vp skills reset %s\n",
			id, len(files[id]), strings.Join(files[id], ", "), id)
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
			printPlanLine(w, "  ", c.Name, skillGroupID(c), c)
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
			printPlanLine(w, "    ", strings.TrimPrefix(c.Name, g.id+"/"), g.id, c)
		}
	}
}

// printPlanLine prints one skill-file row of the plan, labelled label and
// indented by indent. An override row names the reset that removes it.
func printPlanLine(w io.Writer, indent, label, skill string, c commands.Change) {
	switch c.Kind {
	case commands.ChangeOverride:
		fmt.Fprintf(w, "%soverride  %s  (vault %s, embedded %s; kept — vp skills reset %s removes it)\n",
			indent, label, c.VaultHash, c.EmbeddedHash, skill)
	case commands.ChangeUnchanged:
		fmt.Fprintf(w, "%sunchanged %s\n", indent, label)
	case commands.ChangeUnneeded:
		fmt.Fprintf(w, "%sunneeded  %s  (no local override; embedded floor serves it)\n", indent, label)
	}
}
