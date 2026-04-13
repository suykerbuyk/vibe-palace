// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func cmdConfig() *cli.Command {
	return &cli.Command{
		Name:        "config",
		Synopsis:    "vp config <command>",
		Description: "Manage the global vibe-palace configuration.",
		Subcommands: []string{"config upgrade", "config sync"},
		Run: func(args []string) int {
			fmt.Fprintln(os.Stderr, "Usage: vp config <command>\n\nRun 'vp config --help' for details.")
			return cli.ExitOK
		},
	}
}

var configUpgradeFlags = []cli.FlagDef{
	{Name: "--dry-run", Help: "Show what would be added without writing"},
	{Name: "--cwd", Arg: "DIR", Help: "Upgrade the cwd project config (default: current directory). Mutually exclusive with --project."},
	{Name: "--project", Arg: "SLUG", Help: "Upgrade the vault-project config for SLUG. Mutually exclusive with --cwd."},
	{Name: "--legacy", Help: "Use the pre-reconciler upgrade path (no deprecation notice). For testing only."},
}

// upgradeTarget captures the inputs needed to upgrade a specific config
// file against its canonical schema template.
type upgradeTarget struct {
	configPath    string
	canonicalText string
	templateText  string
	notFoundHint  string
}

func cmdConfigUpgrade() *cli.Command {
	return &cli.Command{
		Name:        "config upgrade",
		Synopsis:    "vp config upgrade [--dry-run] [--cwd [DIR] | --project SLUG]",
		Description: "DEPRECATED — use `vp config sync` instead. Adds missing settings to a single config file. Delegates to `vp config sync --tier <resolved>` and will be removed in the next release.",
		Flags:       configUpgradeFlags,
		Examples: []cli.Example{
			{Cmd: "vp config sync", Comment: "Preferred replacement (reconciles all tiers)"},
			{Cmd: "vp config sync --tier global --dry-run", Comment: "Preview global-tier drift only"},
			{Cmd: "vp config upgrade --cwd", Comment: "Legacy: upgrade .vibe-palace.toml in the current directory"},
			{Cmd: "vp config upgrade --project myapp", Comment: "Legacy: upgrade vault-project config for myapp"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(configUpgradeFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
				return cli.ExitUser
			}
			dryRun := fv.Bool("--dry-run")

			// Phase 5 deprecation: by default, delegate to `vp config sync`
			// with the resolved tier and the same addressing flags. The
			// --legacy escape hatch keeps the old single-file path
			// available for callers that haven't migrated yet.
			if !fv.Bool("--legacy") {
				return delegateUpgradeToSync(fv)
			}

			// --cwd and --project are mutually exclusive.
			cwdFlag := fv.Get("--cwd")
			projectFlag := fv.Get("--project")
			cwdSet := fv.IsSet("--cwd")
			if cwdSet && projectFlag != "" {
				fmt.Fprintln(os.Stderr, "vp config upgrade: --cwd and --project are mutually exclusive")
				return cli.ExitUser
			}

			target, code := resolveUpgradeTarget(cwdSet, cwdFlag, projectFlag)
			if code != cli.ExitOK {
				return code
			}

			data, err := os.ReadFile(target.configPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
				if target.notFoundHint != "" {
					fmt.Fprintln(os.Stderr, target.notFoundHint)
				}
				return cli.ExitUser
			}
			userText := string(data)

			canonical, err := storage.CanonicalKeysFrom(target.canonicalText)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: parse schema: %v\n", err)
				return cli.ExitSystem
			}
			present := storage.PresentKeys(userText)
			missing := storage.MissingKeys(canonical, present)

			if len(missing) == 0 {
				fmt.Fprintln(os.Stderr, "Config is up to date.")
				return cli.ExitOK
			}

			total := 0
			for _, keys := range missing {
				total += len(keys)
			}

			templateBlocks := storage.ParseTemplateBlocks(target.templateText)
			upgraded := storage.UpgradeConfig(userText, missing, templateBlocks)

			if dryRun {
				fmt.Fprintf(os.Stderr, "%d setting(s) would be added:\n", total)
				for _, keys := range missing {
					for _, k := range keys {
						fmt.Fprintf(os.Stderr, "  %s\n", k)
					}
				}
				return cli.ExitOK
			}

			backupPath := target.configPath + ".bak"
			if err := os.WriteFile(backupPath, data, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: create backup: %v\n", err)
				return cli.ExitSystem
			}

			tmpPath := target.configPath + ".tmp"
			if err := os.WriteFile(tmpPath, []byte(upgraded), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: write temp: %v\n", err)
				if rmErr := os.Remove(tmpPath); rmErr != nil {
					slog.Error("cleanup temp config file", "path", tmpPath, "err", rmErr)
				}
				return cli.ExitSystem
			}
			if err := os.Rename(tmpPath, target.configPath); err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: rename: %v\n", err)
				if rmErr := os.Remove(tmpPath); rmErr != nil {
					slog.Error("cleanup temp config file", "path", tmpPath, "err", rmErr)
				}
				return cli.ExitSystem
			}

			fmt.Fprintf(os.Stderr, "Added %d setting(s). Backup at %s\n", total, backupPath)
			return cli.ExitOK
		},
	}
}

// delegateUpgradeToSync prints a one-line deprecation notice and forwards
// the upgrade-flavored args to `vp config sync` with the appropriate
// --tier and addressing flag. Default (no --cwd, no --project) maps to
// --tier global; --cwd to --tier project --cwd; --project to --tier
// project --project. --dry-run carries through verbatim.
func delegateUpgradeToSync(fv *cli.FlagValues) int {
	cwdFlag := fv.Get("--cwd")
	projectFlag := fv.Get("--project")
	cwdSet := fv.IsSet("--cwd")
	if cwdSet && projectFlag != "" {
		fmt.Fprintln(os.Stderr, "vp config upgrade: --cwd and --project are mutually exclusive")
		return cli.ExitUser
	}

	args := []string{}
	switch {
	case projectFlag != "":
		if err := storage.ValidateSlug(projectFlag); err != nil {
			fmt.Fprintf(os.Stderr, "vp config upgrade: invalid --project slug %q: %v\n", projectFlag, err)
			return cli.ExitUser
		}
		args = append(args, "--tier", "project", "--project", projectFlag)
	case cwdSet:
		args = append(args, "--tier", "project")
		if cwdFlag != "" {
			args = append(args, "--cwd", cwdFlag)
		}
	default:
		args = append(args, "--tier", "global")
	}
	if fv.Bool("--dry-run") {
		args = append(args, "--dry-run")
	} else {
		args = append(args, "--yes")
	}

	fmt.Fprintln(os.Stderr,
		"vp config upgrade: deprecated — delegating to `vp config sync "+strings.Join(args, " ")+"` (will be removed next release)")
	return runConfigSync(args)
}

// resolveUpgradeTarget selects the config file, canonical schema source,
// and template based on the --cwd / --project flags.
func resolveUpgradeTarget(cwdSet bool, cwdFlag, projectFlag string) (upgradeTarget, int) {
	switch {
	case projectFlag != "":
		if err := storage.ValidateSlug(projectFlag); err != nil {
			fmt.Fprintf(os.Stderr, "vp config upgrade: invalid --project slug %q: %v\n", projectFlag, err)
			return upgradeTarget{}, cli.ExitUser
		}
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
			return upgradeTarget{}, cli.ExitSystem
		}
		vault, err := storage.OpenVaultFromCwd(cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp config upgrade: open vault: %v\n", err)
			return upgradeTarget{}, cli.ExitUser
		}
		cfgPath, err := vault.ProjectConfigFile(projectFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
			return upgradeTarget{}, cli.ExitUser
		}
		// Canonical schema for vault-project = active keys in the
		// vault-project template ([meta] only today). Snippets are
		// drawn from the same template.
		return upgradeTarget{
			configPath:    cfgPath,
			canonicalText: storage.VaultProjectTemplateContent(),
			templateText:  storage.VaultProjectTemplateContent(),
			notFoundHint:  fmt.Sprintf("Run 'vp migrate' or initialize the project to create %s first.", cfgPath),
		}, cli.ExitOK

	case cwdSet:
		dir := cwdFlag
		if dir == "" {
			var err error
			dir, err = os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
				return upgradeTarget{}, cli.ExitSystem
			}
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
			return upgradeTarget{}, cli.ExitSystem
		}
		cfgPath := filepath.Join(abs, ".vibe-palace.toml")
		return upgradeTarget{
			configPath:    cfgPath,
			canonicalText: storage.CwdProjectTemplateContent(),
			templateText:  storage.CwdProjectTemplateContent(),
			notFoundHint:  fmt.Sprintf("Run 'vp init' in %s to create a project config first.", abs),
		}, cli.ExitOK

	default:
		cfgPath, err := storage.VaultConfigFilePath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
			return upgradeTarget{}, cli.ExitSystem
		}
		// Global config retains the original behavior: defaults.toml as
		// canonical schema, template.toml for snippets.
		defaultsText, _ := storage.DefaultsTomlContent()
		return upgradeTarget{
			configPath:    cfgPath,
			canonicalText: defaultsText,
			templateText:  storage.TemplateTomlContent(),
			notFoundHint:  "Run 'vp init' to create a config file first.",
		}, cli.ExitOK
	}
}

var configSyncFlags = []cli.FlagDef{
	{Name: "--dry-run", Help: "Print what would change and exit without writing."},
	{Name: "--tier", Arg: "TIER", Default: "all", Help: "Reconcile only a single tier: global | vault | project | all."},
	{Name: "--yes", Help: "Accept every proposed action non-interactively."},
	{Name: "--project-root", Arg: "PATH", Help: "Project root used for cwd-local vault_path resolution (default: current directory)."},
	{Name: "--cwd", Arg: "DIR", Help: "Project directory for --tier project; filesystem-addressed. Mutually exclusive with --project."},
	{Name: "--project", Arg: "SLUG", Help: "Vault-project slug for --tier project; vault-addressed. Mutually exclusive with --cwd."},
}

// cmdConfigSync is the unified Check→Plan→Apply orchestrator for the
// config-file tiers. Agent-file and shim drift are handled by
// `vp commands upgrade` and stay out of scope here.
func cmdConfigSync() *cli.Command {
	return &cli.Command{
		Name:        "config sync",
		Synopsis:    "vp config sync [--dry-run] [--tier TIER] [--yes] [--project-root PATH] [--cwd DIR | --project SLUG]",
		Description: "Reconcile managed config files (global / vault / project tiers) against their canonical schemas. Idempotent on repeat runs. Does not touch agent-files or slash-command shims — those are handled by `vp commands upgrade`.",
		Flags:       configSyncFlags,
		Examples: []cli.Example{
			{Cmd: "vp config sync --dry-run", Comment: "Preview drift across all tiers"},
			{Cmd: "vp config sync --yes", Comment: "Accept every proposed action non-interactively"},
			{Cmd: "vp config sync --tier global", Comment: "Only reconcile the global config"},
			{Cmd: "vp config sync --tier project --cwd ~/code/myapp", Comment: "Project tier addressed by filesystem path"},
			{Cmd: "vp config sync --tier project --project myapp", Comment: "Project tier addressed by vault slug"},
		},
		Run: runConfigSync,
	}
}

// runConfigSync is the top-level handler, extracted so tests can call it
// directly without going through cli.Run.
func runConfigSync(args []string) int {
	fv, err := cli.ParseFlags(configSyncFlags, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp config sync: %v\n", err)
		return cli.ExitUser
	}

	tier := fv.Get("--tier")
	if tier == "" {
		tier = "all"
	}
	switch tier {
	case "all", "global", "vault", "project":
	default:
		fmt.Fprintf(os.Stderr, "vp config sync: unknown --tier %q (expected global|vault|project|all)\n", tier)
		return cli.ExitUser
	}

	dryRun := fv.Bool("--dry-run")
	autoYes := fv.Bool("--yes")

	cwdFlag := fv.Get("--cwd")
	projectFlag := fv.Get("--project")
	cwdSet := fv.IsSet("--cwd")
	if cwdSet && projectFlag != "" {
		fmt.Fprintln(os.Stderr, "vp config sync: --cwd and --project are mutually exclusive")
		return cli.ExitUser
	}

	root := fv.Get("--project-root")
	if root == "" {
		root, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp config sync: %v\n", err)
			return cli.ExitSystem
		}
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp config sync: resolve --project-root: %v\n", err)
		return cli.ExitUser
	}

	// Build the reconciler list in dependency order. Vault-backed reconcilers
	// skip cleanly when the vault isn't open yet.
	var vault *storage.Vault
	if v, err := storage.OpenVaultFromCwd(absRoot); err == nil {
		vault = v
	}

	projectDir := absRoot
	if cwdSet && cwdFlag != "" {
		if abs, err := filepath.Abs(cwdFlag); err == nil {
			projectDir = abs
		}
	}

	projectSlug := projectFlag
	if projectSlug == "" {
		if slug, err := project.DetectProject(projectDir); err == nil {
			projectSlug = slug
		}
	}

	all := map[string]reconcile.Reconciler{
		"GlobalConfig":  reconcile.NewGlobalConfig(absRoot, reconcile.GlobalSeed{}),
		"Vault":         reconcile.NewVault(absRoot, reconcile.VaultSeed{}),
		"VaultSettings": reconcile.NewVaultSettings(vault),
		"CwdProject":    reconcile.NewCwdProject(projectDir, reconcile.CwdProjectSeed{}),
		"VaultProject":  reconcile.NewVaultProject(vault, projectSlug),
	}

	var order []reconcile.Reconciler
	switch tier {
	case "all":
		order = []reconcile.Reconciler{all["GlobalConfig"], all["Vault"], all["VaultSettings"], all["CwdProject"], all["VaultProject"]}
	case "global":
		order = []reconcile.Reconciler{all["GlobalConfig"]}
	case "vault":
		order = []reconcile.Reconciler{all["Vault"], all["VaultSettings"]}
	case "project":
		order = []reconcile.Reconciler{all["CwdProject"], all["VaultProject"]}
	}

	// Collect plans.
	plans := make([]reconcile.Plan, len(order))
	for i, r := range order {
		p, err := r.Plan(context.Background())
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp config sync: %s Plan: %v\n", r.Name(), err)
			return cli.ExitSystem
		}
		plans[i] = p
	}

	// Render plans.
	printSyncPlans(os.Stdout, order, plans)

	if dryRun {
		return cli.ExitOK
	}

	// Determine whether any action requires prompting.
	if !autoYes && !anyActionable(plans) {
		fmt.Fprintln(os.Stdout, "Nothing to do — all tiers in sync.")
		return cli.ExitOK
	}

	reader := bufio.NewReader(os.Stdin)
	acceptAll := autoYes
	var totalReport reconcile.Report

	for i, r := range order {
		filtered := reconcile.Plan{}
		for _, a := range plans[i].Actions {
			if !isActionable(a.Kind) {
				// Still pass Unchanged/Skip through so Apply's counters match.
				filtered.Actions = append(filtered.Actions, a)
				continue
			}
			if acceptAll {
				fmt.Fprintf(os.Stdout, "[accept] %s %s — %s\n", r.Name(), a.Kind, a.Summary)
				filtered.Actions = append(filtered.Actions, a)
				continue
			}
			fmt.Fprintf(os.Stdout, "\n=== %s %s ===\n%s\n", r.Name(), a.Kind, a.Summary)
			if len(a.Details) > 0 {
				for _, d := range a.Details {
					fmt.Fprintf(os.Stdout, "  %s\n", d)
				}
			}
			choice, perr := cli.PromptChoice(os.Stdout, reader)
			if perr != nil {
				fmt.Fprintf(os.Stderr, "vp config sync: %v\n", perr)
				return cli.ExitSystem
			}
			switch choice {
			case "a":
				filtered.Actions = append(filtered.Actions, a)
			case "A":
				filtered.Actions = append(filtered.Actions, a)
				acceptAll = true
			case "s":
				// skip — drop the action
			case "q":
				fmt.Fprintln(os.Stdout, "Aborting — no further changes applied.")
				return finishSync(totalReport)
			}
		}
		rep, err := r.Apply(context.Background(), filtered)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp config sync: %s Apply: %v\n", r.Name(), err)
			return cli.ExitSystem
		}
		mergeReports(&totalReport, rep)
	}
	return finishSync(totalReport)
}

func printSyncPlans(w *os.File, reconcilers []reconcile.Reconciler, plans []reconcile.Plan) {
	fmt.Fprintln(w, "Plan:")
	for i, r := range reconcilers {
		for _, a := range plans[i].Actions {
			fmt.Fprintf(w, "  [%s] %s: %s", a.Kind, r.Name(), a.Summary)
			if a.Target != "" && a.Target != r.Name() {
				fmt.Fprintf(w, " (%s)", a.Target)
			}
			fmt.Fprintln(w)
			for _, d := range a.Details {
				fmt.Fprintf(w, "    - %s\n", d)
			}
		}
	}
}

func anyActionable(plans []reconcile.Plan) bool {
	for _, p := range plans {
		for _, a := range p.Actions {
			if isActionable(a.Kind) {
				return true
			}
		}
	}
	return false
}

func isActionable(k reconcile.ActionKind) bool {
	return k == reconcile.ActionCreate || k == reconcile.ActionUpdate
}

func mergeReports(dst *reconcile.Report, src reconcile.Report) {
	dst.Created += src.Created
	dst.Updated += src.Updated
	dst.Unchanged += src.Unchanged
	dst.Skipped += src.Skipped
	dst.Errors = append(dst.Errors, src.Errors...)
}

func finishSync(rep reconcile.Report) int {
	fmt.Fprintf(os.Stdout, "Summary: created=%d updated=%d unchanged=%d skipped=%d\n",
		rep.Created, rep.Updated, rep.Unchanged, rep.Skipped)
	if len(rep.Errors) > 0 {
		for _, e := range rep.Errors {
			fmt.Fprintf(os.Stderr, "  error: %v\n", e)
		}
		return cli.ExitSystem
	}
	return cli.ExitOK
}

