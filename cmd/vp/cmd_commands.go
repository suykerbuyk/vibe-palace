// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/shims"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// isTerminal reports whether f is a TTY. Falls back to false on any error.
// The VP_ASSUME_TTY=1 escape hatch forces true — used by integration tests
// that drive the upgrade prompts through a pipe. Production code should
// never rely on this override; it has no user-facing documentation.
func isTerminal(f *os.File) bool {
	if os.Getenv("VP_ASSUME_TTY") == "1" {
		return true
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func cmdCommands() *cli.Command {
	return &cli.Command{
		Name:        "commands",
		Synopsis:    "vp commands <command> [flags]",
		Description: "List the commands available for this project and upgrade vault-level copies against embedded defaults.",
		Subcommands: []string{"commands list", "commands upgrade"},
	}
}

var commandsListFlags = []cli.FlagDef{
	{Name: "--json", Help: "Emit machine-readable JSON instead of a formatted table"},
	{Name: "--project", Arg: "SLUG", Help: "Resolve with project-tier overrides for SLUG"},
	{Name: "--wing", Arg: "SLUG", Help: "Resolve with wing-tier overrides (requires --project)"},
	{Name: "--room", Arg: "SLUG", Help: "Resolve with room-tier overrides (requires --wing)"},
}

func cmdCommandsList() *cli.Command {
	return &cli.Command{
		Name:        "commands list",
		Synopsis:    "vp commands list [--json] [--project SLUG [--wing SLUG [--room SLUG]]]",
		Description: "List every command visible to this project, with the precedence tier that provides it. Mirrors vp_list_commands.",
		Flags:       commandsListFlags,
		Examples: []cli.Example{
			{Cmd: "vp commands list", Comment: "List commands using embedded + vault tiers"},
			{Cmd: "vp commands list --json", Comment: "Emit JSON for scripts"},
			{Cmd: "vp commands list --project myapp", Comment: "Include project-tier overrides"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(commandsListFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp commands list: %v\n", err)
				return cli.ExitUser
			}

			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp commands list: %v\n", err)
				return cli.ExitUser
			}
			resolver := vpctx.NewResolver(vault.Root)

			summaries, err := commands.List(
				resolver, "command",
				fv.Get("--project"), fv.Get("--wing"), fv.Get("--room"),
				60,
			)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp commands list: %v\n", err)
				return cli.ExitUser
			}

			if fv.Bool("--json") {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(summaries); err != nil {
					fmt.Fprintf(os.Stderr, "vp commands list: %v\n", err)
					return cli.ExitSystem
				}
				return cli.ExitOK
			}

			printCommandsTable(os.Stdout, summaries, fv.Get("--project"))
			return cli.ExitOK
		},
	}
}

func printCommandsTable(w io.Writer, summaries []commands.Summary, project string) {
	if len(summaries) == 0 {
		fmt.Fprintln(w, "No commands available.")
		return
	}
	maxName, maxAlias := 0, 0
	for _, s := range summaries {
		if len(s.Name) > maxName {
			maxName = len(s.Name)
		}
		if len(s.Alias) > maxAlias {
			maxAlias = len(s.Alias)
		}
	}
	if project != "" {
		fmt.Fprintf(w, "Commands available for project %q:\n\n", project)
	} else {
		fmt.Fprintln(w, "Commands available:")
		fmt.Fprintln(w)
	}
	for _, s := range summaries {
		fmt.Fprintf(w, "  %-*s  %-*s  %-8s  %s\n",
			maxName, s.Name, maxAlias, s.Alias, s.Source, s.Brief)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, `Use "vp_get_command <name>" (or type vpc-<name> in an AI session)`)
	fmt.Fprintln(w, "to retrieve the full command instructions.")
}

var commandsUpgradeFlags = []cli.FlagDef{
	{Name: "--dry-run", Help: "Print the upgrade plan without writing"},
	{Name: "--overwrite", Help: "Accept every change without prompting (required in non-TTY)"},
	{Name: "--only", Arg: "NAME", Help: "Upgrade only the named template"},
}

func cmdCommandsUpgrade() *cli.Command {
	return &cli.Command{
		Name:        "commands upgrade",
		Synopsis:    "vp commands upgrade [--dry-run] [--overwrite] [--only NAME]",
		Description: "Compare embedded command templates against the vault copy (tier 4) and, for each difference, show a unified diff and prompt to accept, skip, or accept-all. Project/wing/room overrides are never touched.",
		Flags:       commandsUpgradeFlags,
		Examples: []cli.Example{
			{Cmd: "vp commands upgrade", Comment: "Interactive upgrade with diffs"},
			{Cmd: "vp commands upgrade --dry-run", Comment: "Show the plan without writing"},
			{Cmd: "vp commands upgrade --overwrite", Comment: "Accept every change without prompting"},
			{Cmd: "vp commands upgrade --only restart", Comment: "Upgrade a single template"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(commandsUpgradeFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp commands upgrade: %v\n", err)
				return cli.ExitUser
			}
			return runCommandsUpgrade(commandsUpgradeOpts{
				DryRun:    fv.Bool("--dry-run"),
				Overwrite: fv.Bool("--overwrite"),
				Only:      fv.Get("--only"),
				Stdin:     os.Stdin,
				Stdout:    os.Stdout,
				Stderr:    os.Stderr,
			})
		},
	}
}

type commandsUpgradeOpts struct {
	DryRun    bool
	Overwrite bool
	Only      string
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	// InteractiveOverride, when non-nil, forces interactive on/off for tests.
	InteractiveOverride *bool
	// VaultRootOverride, when non-empty, bypasses openProjectVault().
	// Test-only seam.
	VaultRootOverride string
	// ProjectRootOverride, when non-empty, replaces os.Getwd() for agent-block
	// scanning. Test-only seam.
	ProjectRootOverride string
}

func runCommandsUpgrade(opts commandsUpgradeOpts) int {
	vaultRoot := opts.VaultRootOverride
	if vaultRoot == "" {
		vault, err := openProjectVault()
		if err != nil {
			fmt.Fprintf(opts.Stderr, "vp commands upgrade: %v\n", err)
			return cli.ExitUser
		}
		vaultRoot = vault.Root
	}
	resolver := vpctx.NewResolver(vaultRoot)

	plan, err := commands.Plan(resolver, commands.PlanOptions{Only: opts.Only})
	if err != nil {
		fmt.Fprintf(opts.Stderr, "vp commands upgrade: %v\n", err)
		return cli.ExitUser
	}

	// Dirty-vault warning (non-fatal for --dry-run and --overwrite; blocking
	// in interactive mode unless the user proceeds past the prompt).
	if dirty, paths := vaultCommandsDirty(vaultRoot); dirty {
		fmt.Fprintf(opts.Stderr,
			"warning: vault has uncommitted changes under Templates/commands:\n")
		for _, p := range paths {
			fmt.Fprintf(opts.Stderr, "  %s\n", p)
		}
		fmt.Fprintf(opts.Stderr,
			"Run 'git -C %s status' and commit or stash before upgrading.\n",
			vaultRoot)
	}

	added, updated, unchanged, unneeded, custom := 0, 0, 0, 0, 0
	for _, c := range plan {
		switch c.Kind {
		case commands.ChangeNew:
			added++
		case commands.ChangeUpdated:
			updated++
		case commands.ChangeUnchanged:
			unchanged++
		case commands.ChangeUnneeded:
			// Counted apart from unchanged on purpose: "unchanged" means a
			// vault copy was compared and matched, "unneeded" means there is
			// no vault copy and none is wanted. Folding them would report a
			// comparison that never happened.
			unneeded++
		}
	}
	// Count vault-only (custom) templates: listed by resolver as source=vault
	// but not in the embedded name set.
	embeddedNames, _ := resolver.ListEmbedded("command")
	embeddedSet := map[string]bool{}
	for _, n := range embeddedNames {
		embeddedSet[n] = true
	}
	allVault, _ := resolver.ListResourcesScoped("command", "", "", "")
	for _, ri := range allVault {
		if ri.Source == "vault" && !embeddedSet[ri.Name] {
			custom++
		}
	}

	// Scan agent files (CLAUDE.md, AGENTS.md, …) for stale managed blocks.
	// Skip when --only is set so targeted template updates don't sweep in
	// unrelated agent-file rewrites.
	projectRoot := opts.ProjectRootOverride
	if projectRoot == "" {
		projectRoot, _ = os.Getwd()
	}
	var blockChanges []commands.BlockChange
	if opts.Only == "" && projectRoot != "" {
		bc, err := commands.ScanAgentBlocks(projectRoot)
		if err != nil {
			fmt.Fprintf(opts.Stderr, "vp commands upgrade: scan agent blocks: %v\n", err)
		} else {
			blockChanges = bc
		}
	}
	pendingBlocks := 0
	for _, b := range blockChanges {
		if b.Kind != commands.BlockCurrent {
			pendingBlocks++
		}
	}

	// Slash-command shims: compute the plan against the project root so we
	// can present shim drift alongside template + agent-block drift. Scoped
	// by --only to the matching shim (Stale entries for other names are
	// filtered out so a targeted run never sweeps unrelated state).
	shimPlan, shimErr := planShims(resolver, projectRoot, opts.Only)
	if shimErr != nil {
		fmt.Fprintf(opts.Stderr, "vp commands upgrade: shim plan: %v\n", shimErr)
	}

	// Grok native command shims (first-class /vpc-* via .grok/plugins/...).
	// Emitted only when Grok is detected, using identical bodies.
	var grokShimPlan []shims.Change
	if shims.GrokPresent(projectRoot) {
		var gerr error
		grokShimPlan, gerr = planGrokCommandShims(resolver, projectRoot, opts.Only)
		if gerr != nil {
			fmt.Fprintf(opts.Stderr, "vp commands upgrade: grok shim plan: %v\n", gerr)
		}
	}
	shimAdd, shimUpd, shimStale, shimCustom := 0, 0, 0, 0
	for _, c := range shimPlan {
		switch c.Kind {
		case shims.New:
			shimAdd++
		case shims.Modified:
			shimUpd++
		case shims.Stale:
			shimStale++
		case shims.CustomChange:
			shimCustom++
		}
	}
	grokShimAdd, grokShimUpd, grokShimStale, grokShimCustom := 0, 0, 0, 0
	for _, c := range grokShimPlan {
		switch c.Kind {
		case shims.New:
			grokShimAdd++
		case shims.Modified:
			grokShimUpd++
		case shims.Stale:
			grokShimStale++
		case shims.CustomChange:
			grokShimCustom++
		}
	}
	pendingShims := shimAdd + shimUpd + shimStale + grokShimAdd + grokShimUpd + grokShimStale

	// Per-project skill shims: ClaudeSkill always; CursorRule when a Cursor
	// layout is present. Planned against the project root so we can present
	// skill-shim drift alongside command-shim drift, then prompt + apply it
	// through the same UX. Scoped by --only the same way command shims are.
	skillPlan, skillErr := planSkillShims(resolver, projectRoot, opts.Only)
	if skillErr != nil {
		fmt.Fprintf(opts.Stderr, "vp commands upgrade: skill shim plan: %v\n", skillErr)
	}
	skillAdd, skillUpd, skillStale, skillCustom := 0, 0, 0, 0
	for _, c := range skillPlan {
		switch c.Kind {
		case shims.New:
			skillAdd++
		case shims.Modified:
			skillUpd++
		case shims.Stale:
			skillStale++
		case shims.CustomChange:
			skillCustom++
		}
	}
	pendingSkillShims := skillAdd + skillUpd + skillStale

	if opts.DryRun {
		printUpgradePlan(opts.Stdout, plan)
		printBlockPlan(opts.Stdout, blockChanges)
		printShimPlan(opts.Stdout, shimPlan)
		if len(grokShimPlan) > 0 {
			printGrokCommandShimPlan(opts.Stdout, grokShimPlan)
		}
		printSkillShimPlan(opts.Stdout, skillPlan)
		fmt.Fprintf(opts.Stdout,
			"\nSummary (dry run): %d new, %d updated, %d unchanged, %d unneeded, %d custom, %d agent-file block(s) need updating, shims: %d new, %d updated, %d stale, %d custom, grok shims: %d new, %d updated, %d stale, %d custom, skill shims: %d new, %d updated, %d stale, %d custom.\n",
			added, updated, unchanged, unneeded, custom, pendingBlocks,
			shimAdd, shimUpd, shimStale, shimCustom,
			grokShimAdd, grokShimUpd, grokShimStale, grokShimCustom,
			skillAdd, skillUpd, skillStale, skillCustom)
		if added+updated+pendingBlocks+pendingShims+pendingSkillShims > 0 {
			return cli.ExitUser // non-zero — manual review required
		}
		return cli.ExitOK
	}

	interactive := isTerminal(os.Stdin) && !opts.Overwrite
	if opts.InteractiveOverride != nil {
		interactive = *opts.InteractiveOverride && !opts.Overwrite
	}
	if !opts.Overwrite && !interactive {
		// Non-interactive and not --overwrite: refuse to silently write.
		// But we still proceed if there is nothing to do.
		if added+updated+pendingBlocks+pendingShims+pendingSkillShims == 0 {
			fmt.Fprintln(opts.Stdout, "All embedded templates, agent blocks, and shims match. Nothing to do.")
			return cli.ExitOK
		}
		fmt.Fprintln(opts.Stderr,
			"vp commands upgrade: stdin is not a terminal and --overwrite was not set.")
		fmt.Fprintln(opts.Stderr,
			"Re-run with --overwrite to accept every change, or --dry-run to preview.")
		return cli.ExitUser
	}

	reader := bufio.NewReader(opts.Stdin)

	promptRes := runUpgradePrompt(plan, UpgradePromptOpts{
		GroupBy: func(c commands.Change) string { return c.Name },
		RenderHeader: func(w io.Writer, _ string, group []commands.Change) {
			c := group[0]
			fmt.Fprintf(w, "\n=== %s (%s) ===\n", c.Name, c.Kind)
		},
		RenderBody: func(w io.Writer, _ string, group []commands.Change) {
			c := group[0]
			if c.Kind == commands.ChangeUpdated {
				diff := commands.RenderUnified(
					"vault/"+c.Name+".md",
					"embedded/"+c.Name+".md",
					c.VaultContent, c.EmbeddedContent,
				)
				fmt.Fprint(w, diff)
			} else {
				fmt.Fprintf(w, "(new file; %d bytes will be added)\n",
					len(c.EmbeddedContent))
			}
		},
		AcceptAll: opts.Overwrite,
		Reader:    reader,
		Stdout:    opts.Stdout,
		Stderr:    opts.Stderr,
	})
	accepted := promptRes.Accepted
	acceptAll := promptRes.AcceptAll
	acceptedCount := promptRes.AcceptedCount
	skippedCount := promptRes.SkippedCount
	if promptRes.Quit {
		fmt.Fprintln(opts.Stdout, "Aborting — no further changes applied.")
		return applyAndReport(opts.Stdout, opts.Stderr, projectRoot, accepted, nil, nil, nil, nil,
			acceptedCount, skippedCount, custom, shimCustom, grokShimCustom, skillCustom)
	}

	// Agent-file managed blocks: collect acceptances using the same prompt.
	acceptedBlocks := make([]commands.BlockChange, 0, len(blockChanges))
	for _, b := range blockChanges {
		if b.Kind == commands.BlockCurrent {
			continue
		}
		if acceptAll {
			acceptedBlocks = append(acceptedBlocks, b)
			acceptedCount++
			fmt.Fprintf(opts.Stdout, "[accept] agent-block %s (%s)\n",
				b.Target.DisplayName, b.Kind)
			continue
		}
		fmt.Fprintf(opts.Stdout, "\n=== agent-block %s (%s) ===\n",
			b.Target.DisplayName, b.Kind)
		if b.Kind == commands.BlockStale {
			fmt.Fprintf(opts.Stdout, "Present sha=%s → expected sha=%s\n",
				b.PresentSha, b.ExpectedSha)
		} else {
			fmt.Fprintf(opts.Stdout, "No managed block present; one will be appended (sha=%s).\n",
				b.ExpectedSha)
		}
		choice, err := cli.PromptChoice(opts.Stdout, reader)
		if err != nil {
			fmt.Fprintf(opts.Stderr, "vp commands upgrade: %v\n", err)
			return cli.ExitSystem
		}
		switch choice {
		case "a":
			acceptedBlocks = append(acceptedBlocks, b)
			acceptedCount++
		case "A":
			acceptedBlocks = append(acceptedBlocks, b)
			acceptedCount++
			acceptAll = true
		case "s":
			skippedCount++
		case "q":
			fmt.Fprintln(opts.Stdout, "Aborting — no further changes applied.")
			return applyAndReport(opts.Stdout, opts.Stderr, projectRoot, accepted, acceptedBlocks, nil, nil, nil,
				acceptedCount, skippedCount, custom, shimCustom, grokShimCustom, skillCustom)
		}
	}

	// Slash-command shims: per-entry prompts for New / Modified / Stale.
	// Unchanged and CustomChange entries never surface here. Stale prompts
	// make deletion consequence explicit; only accepted entries reach Apply.
	acceptedShims := make([]shims.Change, 0, len(shimPlan))
	acceptedGrokShims := make([]shims.Change, 0, len(grokShimPlan))
	for _, s := range shimPlan {
		if s.Kind == shims.UnchangedChange || s.Kind == shims.CustomChange {
			continue
		}
		if acceptAll {
			acceptedShims = append(acceptedShims, s)
			acceptedCount++
			fmt.Fprintf(opts.Stdout, "[accept] shim %s (%s)\n", s.Name, s.Kind)
			continue
		}
		fmt.Fprintf(opts.Stdout, "\n=== shim %s (%s) ===\n", s.Name, s.Kind)
		switch s.Kind {
		case shims.New:
			fmt.Fprintf(opts.Stdout, "%s will be created.\n", s.Path)
		case shims.Modified:
			fmt.Fprintf(opts.Stdout, "%s body is stale (prev sha=%s); will be rewritten.\n",
				s.Path, s.PrevSha)
		case shims.Stale:
			fmt.Fprintf(opts.Stdout,
				"%s no longer maps to a known command (sha=%s); accept to delete.\n",
				s.Path, s.PrevSha)
		}
		choice, err := cli.PromptChoice(opts.Stdout, reader)
		if err != nil {
			fmt.Fprintf(opts.Stderr, "vp commands upgrade: %v\n", err)
			return cli.ExitSystem
		}
		switch choice {
		case "a":
			acceptedShims = append(acceptedShims, s)
			acceptedCount++
		case "A":
			acceptedShims = append(acceptedShims, s)
			acceptedCount++
			acceptAll = true
		case "s":
			skippedCount++
		case "q":
			fmt.Fprintln(opts.Stdout, "Aborting — no further changes applied.")
			return applyAndReport(opts.Stdout, opts.Stderr, projectRoot, accepted, acceptedBlocks, acceptedShims, nil, nil,
				acceptedCount, skippedCount, custom, shimCustom, grokShimCustom, skillCustom)
		}
	}

	// Grok command shims (under .grok/plugins/...): same prompt UX.
	for _, s := range grokShimPlan {
		if s.Kind == shims.UnchangedChange || s.Kind == shims.CustomChange {
			continue
		}
		if acceptAll {
			acceptedGrokShims = append(acceptedGrokShims, s)
			acceptedCount++
			fmt.Fprintf(opts.Stdout, "[accept] grok-shim %s (%s)\n", s.Name, s.Kind)
			continue
		}
		fmt.Fprintf(opts.Stdout, "\n=== grok-shim %s (%s) ===\n", s.Name, s.Kind)
		switch s.Kind {
		case shims.New:
			fmt.Fprintf(opts.Stdout, "%s will be created.\n", s.Path)
		case shims.Modified:
			fmt.Fprintf(opts.Stdout, "%s body is stale (prev sha=%s); will be rewritten.\n",
				s.Path, s.PrevSha)
		case shims.Stale:
			fmt.Fprintf(opts.Stdout,
				"%s no longer maps to a known command (sha=%s); accept to delete.\n",
				s.Path, s.PrevSha)
		}
		choice, err := cli.PromptChoice(opts.Stdout, reader)
		if err != nil {
			fmt.Fprintf(opts.Stderr, "vp commands upgrade: %v\n", err)
			return cli.ExitSystem
		}
		switch choice {
		case "a":
			acceptedGrokShims = append(acceptedGrokShims, s)
			acceptedCount++
		case "A":
			acceptedGrokShims = append(acceptedGrokShims, s)
			acceptedCount++
			acceptAll = true
		case "s":
			skippedCount++
		case "q":
			fmt.Fprintln(opts.Stdout, "Aborting — no further changes applied.")
			return applyAndReport(opts.Stdout, opts.Stderr, projectRoot, accepted, acceptedBlocks, acceptedShims, acceptedGrokShims, nil,
				acceptedCount, skippedCount, custom, shimCustom, grokShimCustom, skillCustom)
		}
	}

	// Per-project skill shims: per-entry prompts for New / Modified / Stale,
	// mirroring the command-shim loop above. Unchanged and CustomChange
	// entries never surface here; only accepted entries reach ApplySkills.
	acceptedSkillShims := make([]shims.SkillChange, 0, len(skillPlan))
	for _, s := range skillPlan {
		if s.Kind == shims.UnchangedChange || s.Kind == shims.CustomChange {
			continue
		}
		if acceptAll {
			acceptedSkillShims = append(acceptedSkillShims, s)
			acceptedCount++
			fmt.Fprintf(opts.Stdout, "[accept] skill-shim %s %s (%s)\n", s.Target, s.Name, s.Kind)
			continue
		}
		fmt.Fprintf(opts.Stdout, "\n=== skill-shim %s %s (%s) ===\n", s.Target, s.Name, s.Kind)
		switch s.Kind {
		case shims.New:
			fmt.Fprintf(opts.Stdout, "%s will be created.\n", s.Path)
		case shims.Modified:
			fmt.Fprintf(opts.Stdout, "%s body is stale (prev sha=%s); will be rewritten.\n",
				s.Path, s.PrevSha)
		case shims.Stale:
			fmt.Fprintf(opts.Stdout,
				"%s no longer maps to a known skill (sha=%s); accept to delete.\n",
				s.Path, s.PrevSha)
		}
		choice, err := cli.PromptChoice(opts.Stdout, reader)
		if err != nil {
			fmt.Fprintf(opts.Stderr, "vp commands upgrade: %v\n", err)
			return cli.ExitSystem
		}
		switch choice {
		case "a":
			acceptedSkillShims = append(acceptedSkillShims, s)
			acceptedCount++
		case "A":
			acceptedSkillShims = append(acceptedSkillShims, s)
			acceptedCount++
			acceptAll = true
		case "s":
			skippedCount++
		case "q":
			fmt.Fprintln(opts.Stdout, "Aborting — no further changes applied.")
			return applyAndReport(opts.Stdout, opts.Stderr, projectRoot, accepted, acceptedBlocks, acceptedShims, acceptedGrokShims, acceptedSkillShims,
				acceptedCount, skippedCount, custom, shimCustom, grokShimCustom, skillCustom)
		}
	}

	return applyAndReport(opts.Stdout, opts.Stderr, projectRoot, accepted, acceptedBlocks, acceptedShims, acceptedGrokShims, acceptedSkillShims,
		acceptedCount, skippedCount, custom, shimCustom, grokShimCustom, skillCustom)
}

func applyAndReport(w, errw io.Writer, projectRoot string, accepted []commands.Change, acceptedBlocks []commands.BlockChange, acceptedShims, acceptedGrokShims []shims.Change, acceptedSkillShims []shims.SkillChange, acceptedCount, skippedCount, custom, shimCustom, grokShimCustom, skillCustom int) int {
	if err := commands.Apply(accepted); err != nil {
		fmt.Fprintf(errw, "apply templates: %v\n", err)
		return cli.ExitSystem
	}
	if err := commands.ApplyAgentBlocks(acceptedBlocks); err != nil {
		fmt.Fprintf(errw, "apply agent blocks: %v\n", err)
		return cli.ExitSystem
	}
	// Only user-approved Stale entries are in acceptedShims, so it is safe
	// to allow removal — Apply will only touch what the caller passed.
	rep, err := shims.Apply(acceptedShims, shims.ApplyOptions{AllowStaleRemoval: true})
	if err != nil {
		fmt.Fprintf(errw, "apply shims: %v\n", err)
		return cli.ExitSystem
	}
	grokRep, err := shims.Apply(acceptedGrokShims, shims.ApplyOptions{AllowStaleRemoval: true})
	if err != nil {
		fmt.Fprintf(errw, "apply grok shims: %v\n", err)
		return cli.ExitSystem
	}
	// Skill shims ride the same contract: only user-approved Stale entries
	// reach here, so AllowStaleRemoval cleans up a removed skill's shim.
	skillRep, _, err := shims.ApplySkills(acceptedSkillShims, shims.ApplyOptions{AllowStaleRemoval: true})
	if err != nil {
		fmt.Fprintf(errw, "apply skill shims: %v\n", err)
		return cli.ExitSystem
	}
	// Reconcile the project repo-root .gitignore so existing projects
	// self-heal (ignore host-local vp artifacts) without a full re-init.
	// Append-only and idempotent; non-fatal — log and carry on.
	if projectRoot != "" {
		if err := storage.ReconcileProjectGitignore(projectRoot); err != nil {
			slog.Error("project gitignore reconcile error", "err", err)
		}
	}
	fmt.Fprintf(w,
		"\nDone. %d accepted, %d skipped, %d custom (untouched). Shims: %d added, %d updated, %d removed, %d custom. Grok shims: %d added, %d updated, %d removed, %d custom. Skill shims: %d added, %d updated, %d removed, %d custom.\n",
		acceptedCount, skippedCount, custom,
		rep.Added, rep.Updated, rep.Removed, shimCustom,
		grokRep.Added, grokRep.Updated, grokRep.Removed, grokShimCustom,
		skillRep.Added, skillRep.Updated, skillRep.Removed, skillCustom)
	return cli.ExitOK
}

func printBlockPlan(w io.Writer, changes []commands.BlockChange) {
	if len(changes) == 0 {
		return
	}
	fmt.Fprintln(w, "\nAgent-file managed blocks:")
	for _, c := range changes {
		switch c.Kind {
		case commands.BlockMissing:
			fmt.Fprintf(w, "  missing   %s  (will add sha=%s)\n",
				c.Target.DisplayName, c.ExpectedSha)
		case commands.BlockStale:
			fmt.Fprintf(w, "  stale     %s  (%s → %s)\n",
				c.Target.DisplayName, c.PresentSha, c.ExpectedSha)
		case commands.BlockCurrent:
			fmt.Fprintf(w, "  current   %s\n", c.Target.DisplayName)
		}
	}
}

// planGrokCommandShims builds the Grok plugin command shim plan (parallel
// to planShims). When only is non-empty the plan is filtered.
func planGrokCommandShims(resolver *vpctx.Resolver, projectRoot, only string) ([]shims.Change, error) {
	if projectRoot == "" {
		return nil, nil
	}
	slug, _ := project.DetectProject(projectRoot)
	summaries, err := commands.List(resolver, "command", slug, "", "", 60)
	if err != nil {
		return nil, err
	}
	plan, err := shims.PlanGrokCommands(summaries, projectRoot)
	if err != nil {
		return nil, err
	}
	if only == "" {
		return plan, nil
	}
	filtered := plan[:0]
	for _, c := range plan {
		if c.Name == only {
			filtered = append(filtered, c)
		}
	}
	return filtered, nil
}

// planShims builds the shim plan for projectRoot. When only is non-empty,
// the plan is filtered to the matching command name so a targeted upgrade
// does not sweep in unrelated Stale/Custom entries.
func planShims(resolver *vpctx.Resolver, projectRoot, only string) ([]shims.Change, error) {
	if projectRoot == "" {
		return nil, nil
	}
	// Detect the owning project so project-scoped commands enter the desired
	// set (and render a project="<slug>" param). Empty slug on failure ->
	// global-only, preserving prior behavior.
	slug, _ := project.DetectProject(projectRoot)
	summaries, err := commands.List(resolver, "command", slug, "", "", 60)
	if err != nil {
		return nil, err
	}
	plan, err := shims.Plan(summaries, projectRoot)
	if err != nil {
		return nil, err
	}
	if only == "" {
		return plan, nil
	}
	filtered := plan[:0]
	for _, c := range plan {
		if c.Name == only {
			filtered = append(filtered, c)
		}
	}
	return filtered, nil
}

// planSkillShims builds the combined per-project skill-shim plan across every
// detected skill target (ClaudeSkill always; CursorRule when a Cursor layout
// is present). Mirrors initSkillShimWiring in cmd_init.go for the item set and
// target selection, but returns a flat plan so runCommandsUpgrade can present,
// prompt, and apply it through the same UX as command shims. When only is
// non-empty the plan is filtered to the matching skill name so a targeted
// upgrade does not sweep in unrelated skills.
func planSkillShims(resolver *vpctx.Resolver, projectRoot, only string) ([]shims.SkillChange, error) {
	if projectRoot == "" {
		return nil, nil
	}
	items, err := skillShimItems(resolver)
	if err != nil {
		return nil, err
	}
	targets := []shims.TargetKind{shims.ClaudeSkill}
	if shims.CursorPresent(projectRoot) {
		targets = append(targets, shims.CursorRule)
	}
	var plan []shims.SkillChange
	for _, target := range targets {
		changes, err := shims.PlanSkills(target, items, projectRoot)
		if err != nil {
			return nil, err
		}
		plan = append(plan, changes...)
	}
	// GrokSkill: when Grok is detected, append the per-persona vps-* shims
	// AND the /vpc command hub so the same dry-run/prompt/apply flow handles
	// them. The hub's Name is "vpc", so `--only vpc` correctly targets it.
	if shims.GrokPresent(projectRoot) {
		grokChanges, err := shims.PlanSkills(shims.GrokSkill, items, projectRoot)
		if err != nil {
			return nil, err
		}
		plan = append(plan, grokChanges...)
		hub, err := shims.PlanGrokHub(projectRoot)
		if err != nil {
			return nil, err
		}
		plan = append(plan, hub)
	}
	if only == "" {
		return plan, nil
	}
	filtered := plan[:0]
	for _, c := range plan {
		if c.Name == only {
			filtered = append(filtered, c)
		}
	}
	return filtered, nil
}

// skillShimItems resolves the skill set into the SkillItem inputs the
// ClaudeSkill/CursorRule renderers need. Mirrors the item-building loop in
// initSkillShimWiring: unresolvable skills are skipped silently.
func skillShimItems(resolver *vpctx.Resolver) ([]shims.SkillItem, error) {
	names, err := resolver.ListResourcesScoped("skill", "", "", "")
	if err != nil {
		return nil, err
	}
	items := make([]shims.SkillItem, 0, len(names))
	for _, ri := range names {
		sd, _, err := resolver.ResolveSkillDir(ri.Name, "", "", "")
		if err != nil {
			continue
		}
		vaultPath := filepath.Join(resolver.VaultRoot(),
			"Templates", "skills", ri.Name, "SKILL.md")
		items = append(items, shims.SkillItem{
			Name:        ri.Name,
			Frontmatter: sd.Frontmatter,
			VaultPath:   vaultPath,
		})
	}
	return items, nil
}

// printSkillShimPlan renders the skill-shim plan for --dry-run, mirroring
// printShimPlan's layout so command and skill drift read consistently.
func printSkillShimPlan(w io.Writer, plan []shims.SkillChange) {
	if len(plan) == 0 {
		return
	}
	fmt.Fprintln(w, "\nSkill shims:")
	for _, c := range plan {
		switch c.Kind {
		case shims.New:
			fmt.Fprintf(w, "  new       %s\n", c.Path)
		case shims.Modified:
			fmt.Fprintf(w, "  modified  %s  (prev sha=%s)\n", c.Path, c.PrevSha)
		case shims.UnchangedChange:
			fmt.Fprintf(w, "  unchanged %s\n", c.Path)
		case shims.Stale:
			fmt.Fprintf(w, "  stale     %s  (sha=%s — prompts for removal)\n",
				c.Path, c.PrevSha)
		case shims.CustomChange:
			fmt.Fprintf(w, "  custom    %s  (left untouched)\n", c.Path)
		}
	}
}

// printShimChanges renders a command-shim plan under the given header. Shared
// by the Claude (printShimPlan) and Grok (printGrokCommandShimPlan) paths so
// the row formatting stays in one place.
func printShimChanges(w io.Writer, header string, plan []shims.Change) {
	if len(plan) == 0 {
		return
	}
	fmt.Fprintln(w, header)
	for _, c := range plan {
		switch c.Kind {
		case shims.New:
			fmt.Fprintf(w, "  new       %s\n", c.Path)
		case shims.Modified:
			fmt.Fprintf(w, "  modified  %s  (prev sha=%s)\n", c.Path, c.PrevSha)
		case shims.UnchangedChange:
			fmt.Fprintf(w, "  unchanged %s\n", c.Path)
		case shims.Stale:
			fmt.Fprintf(w, "  stale     %s  (sha=%s — prompts for removal)\n",
				c.Path, c.PrevSha)
		case shims.CustomChange:
			fmt.Fprintf(w, "  custom    %s  (left untouched)\n", c.Path)
		}
	}
}

func printShimPlan(w io.Writer, plan []shims.Change) {
	printShimChanges(w, "\nSlash-command shims:", plan)
}

func printGrokCommandShimPlan(w io.Writer, plan []shims.Change) {
	printShimChanges(w, "\nGrok command shims (.grok/plugins/vibe-palace/commands/):", plan)
}

func printUpgradePlan(w io.Writer, plan []commands.Change) {
	fmt.Fprintln(w, "Upgrade plan:")
	for _, c := range plan {
		switch c.Kind {
		case commands.ChangeNew:
			fmt.Fprintf(w, "  new       %s  (hash %s)\n", c.Name, c.EmbeddedHash)
		case commands.ChangeUpdated:
			fmt.Fprintf(w, "  updated   %s  (vault %s → embedded %s)\n",
				c.Name, c.VaultHash, c.EmbeddedHash)
		case commands.ChangeUnchanged:
			fmt.Fprintf(w, "  unchanged %s\n", c.Name)
		case commands.ChangeUnneeded:
			fmt.Fprintf(w, "  unneeded  %s  (no local override; embedded floor serves it)\n", c.Name)
		}
	}
}

// vaultCommandsDirty reports whether the vault git working tree has
// uncommitted changes under Templates/commands/ or Projects/*/commands/.
// Returns (false, nil) when the vault is not a git repo or git is absent.
func vaultCommandsDirty(vaultRoot string) (bool, []string) {
	gitDir := filepath.Join(vaultRoot, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return false, nil
	}
	cmd := exec.Command("git", "-C", vaultRoot, "status", "--porcelain", "--",
		"Templates/commands", "Projects")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return false, nil
	}
	var paths []string
	for line := range strings.SplitSeq(strings.TrimRight(out.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		// Status is "XY path" (at least 3 chars).
		if len(line) < 4 {
			continue
		}
		p := strings.TrimSpace(line[3:])
		// Filter further to only /commands/ paths under Projects/.
		if strings.HasPrefix(p, "Templates/commands/") ||
			(strings.HasPrefix(p, "Projects/") && strings.Contains(p, "/commands/")) {
			paths = append(paths, p)
		}
	}
	return len(paths) > 0, paths
}
