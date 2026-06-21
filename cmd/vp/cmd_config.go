// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

func cmdConfig() *cli.Command {
	return &cli.Command{
		Name:        "config",
		Synopsis:    "vp config <command>",
		Description: "Manage the global vibe-palace configuration.",
		Subcommands: []string{"config upgrade", "config sync"},
	}
}

var configUpgradeFlags = []cli.FlagDef{
	{Name: "--dry-run", Help: "Show what would be added without writing"},
	{Name: "--cwd", Arg: "DIR", Help: "Upgrade the cwd project config (default: current directory). Mutually exclusive with --project."},
	{Name: "--project", Arg: "SLUG", Help: "Upgrade the vault-project config for SLUG. Mutually exclusive with --cwd."},
}

// cmdConfigUpgrade is a thin alias for `vp config sync`. The original
// TOML-parsing implementation was retired once a byte-identical
// migration test (TestLegacyVsSyncByteIdentical) proved the
// reconciler-based sync path produces identical output on the same
// fixture input across all three target resolutions (global, cwd,
// project). The alias is retained for backward compatibility with
// tutorials, PRD text, `vp check` hints, and staleness warnings that
// instruct users to "run 'vp config upgrade'".
func cmdConfigUpgrade() *cli.Command {
	return &cli.Command{
		Name:        "config upgrade",
		Synopsis:    "vp config upgrade [--dry-run] [--cwd [DIR] | --project SLUG]",
		Description: "Alias for `vp config sync` scoped to a single config tier. Translates --cwd / --project into the equivalent sync addressing flags and delegates to the reconciler.",
		Flags:       configUpgradeFlags,
		Examples: []cli.Example{
			{Cmd: "vp config sync", Comment: "Preferred form (reconciles all tiers)"},
			{Cmd: "vp config upgrade", Comment: "Global-tier alias — same as `vp config sync --tier global --yes`"},
			{Cmd: "vp config upgrade --cwd", Comment: "Project-tier alias for the current cwd"},
			{Cmd: "vp config upgrade --project myapp", Comment: "Project-tier alias addressed by slug"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(configUpgradeFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
				return cli.ExitUser
			}
			return aliasUpgradeToSync(fv)
		},
	}
}

// aliasUpgradeToSync translates the upgrade-flavored addressing flags
// (--cwd / --project / neither) into the corresponding `vp config sync`
// invocation and delegates. --dry-run carries through; when absent, --yes
// is implied so the alias preserves the legacy non-interactive behavior.
func aliasUpgradeToSync(fv *cli.FlagValues) int {
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
		if err := slug.Validate(projectFlag); err != nil {
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
	return runConfigSync(args)
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
	// Phase 3: TemplateTree is vault-tier and requires an open vault.
	// When vault isn't resolvable (global-only scope, no vault yet) we
	// skip adding it — matches the policy used by VaultSettings above.
	var vaultPathForTemplates string
	if vault != nil {
		vaultPathForTemplates = vault.Root
	} else if vp, _, err := storage.ResolveVaultPath(absRoot); err == nil && vp != "" {
		vaultPathForTemplates = vp
	} else if err != nil {
		// Not fatal — we simply skip the Templates reconciler — but
		// log so a misconfigured global config doesn't silently drop
		// the whole Templates tier from sync.
		slog.Error("resolve vault path for TemplateTree", "err", err, "root", absRoot)
	}
	if vaultPathForTemplates != "" {
		all["Templates"] = reconcile.NewTemplateTree(vaultPathForTemplates, "Templates",
			reconcile.TemplateTreeSeed{Mode: reconcile.TemplateModeMaterialize})
	}

	// Phase 4: project-scoped TemplateTree scaffolders. When addressing
	// flags pin to a single project, emit exactly one scaffold reconciler;
	// otherwise enumerate every directory under <vault>/Projects/ and
	// emit one per slug.
	var projectScaffolds []reconcile.Reconciler
	if vaultPathForTemplates != "" {
		if projectSlug != "" && (cwdSet || projectFlag != "") {
			projectScaffolds = append(projectScaffolds,
				reconcile.NewTemplateTree(vaultPathForTemplates, "Projects/"+projectSlug,
					reconcile.TemplateTreeSeed{Mode: reconcile.TemplateModeScaffold}))
		} else {
			for _, slug := range enumerateVaultProjectSlugs(vaultPathForTemplates) {
				projectScaffolds = append(projectScaffolds,
					reconcile.NewTemplateTree(vaultPathForTemplates, "Projects/"+slug,
						reconcile.TemplateTreeSeed{Mode: reconcile.TemplateModeScaffold}))
			}
		}
	}

	var order []reconcile.Reconciler
	appendIfPresent := func(out []reconcile.Reconciler, key string) []reconcile.Reconciler {
		if r, ok := all[key]; ok {
			return append(out, r)
		}
		return out
	}
	switch tier {
	case "all":
		order = []reconcile.Reconciler{all["GlobalConfig"], all["Vault"], all["VaultSettings"]}
		order = appendIfPresent(order, "Templates")
		order = append(order, all["CwdProject"], all["VaultProject"])
		order = append(order, projectScaffolds...)
	case "global":
		order = []reconcile.Reconciler{all["GlobalConfig"]}
	case "vault":
		order = []reconcile.Reconciler{all["Vault"], all["VaultSettings"]}
		order = appendIfPresent(order, "Templates")
	case "project":
		order = []reconcile.Reconciler{all["CwdProject"], all["VaultProject"]}
		order = append(order, projectScaffolds...)
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
	// batchMode is the separate S/O/N batch letter honored by the
	// Prompt resolver. Distinct from acceptAll because its letter
	// semantics differ (skip/overwrite/new vs. accept/skip/accept-all).
	var batchMode string
	if autoYes {
		// --yes implies "overwrite" for every Prompt action so the
		// non-interactive run never blocks on stdin.
		batchMode = "o"
	}
	var totalReport reconcile.Report

	for i, r := range order {
		actions := plans[i].Actions
		// Prompt resolver pre-pass: turn every ActionPrompt into a
		// concrete Create / Update / (drop) before the existing
		// accept/skip loop runs. TemplateTree is the only reconciler
		// that emits ActionPrompt today. Actions emitted by the
		// resolver are already user-approved and bypass the
		// PromptChoice gate below.
		resolved, preApproved, abort := resolveTemplatePrompts(r, actions, reader, &batchMode, os.Stdout)
		if abort {
			fmt.Fprintln(os.Stdout, "Aborting — no further changes applied.")
			return finishSync(totalReport)
		}
		actions = resolved

		filtered := reconcile.Plan{}
		for _, a := range actions {
			if !isActionable(a.Kind) {
				// Still pass Unchanged/Skip through so Apply's counters match.
				filtered.Actions = append(filtered.Actions, a)
				continue
			}
			if preApproved[a.Target] {
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

// resolveTemplatePrompts rewrites any ActionPrompt in actions into a
// concrete Create / Update action (or drops it for Skip) by consulting
// the user via cli.PromptTemplateChoice. Non-Prompt actions pass
// through unchanged.
//
// The 'n'/'N' branch writes the embedded bytes to <target>.new directly
// (bypassing Apply) because the reconciler's Apply keys its
// embedded-bytes lookup on the original target path; rewriting Target
// to <path>.new would cause Apply to fail to find the resource.
// Bypassing is safe because the .new write is a pure sidecar emission
// with no lock bookkeeping.
//
// batchMode carries the persistent S/O/N letter across reconcilers so
// a single uppercase answer suppresses prompts for every remaining
// Prompt action in this sync run. abort is true when the user picked
// 'q'; the orchestrator should stop and print a terminal summary.
func resolveTemplatePrompts(r reconcile.Reconciler, actions []reconcile.Action, reader *bufio.Reader, batchMode *string, w io.Writer) (resolved []reconcile.Action, preApproved map[string]bool, abort bool) {
	preApproved = map[string]bool{}
	// Lazily loaded embedded bytes map; only built when we actually
	// need to write a .new sidecar.
	var embBytes map[string][]byte
	ensureEmbeddedBytes := func() error {
		if embBytes != nil {
			return nil
		}
		rs, err := templates.WalkEmbedded()
		if err != nil {
			return err
		}
		embBytes = make(map[string][]byte, len(rs))
		for _, res := range rs {
			embBytes[res.RelPath] = res.Bytes
		}
		return nil
	}

	for _, a := range actions {
		if a.Kind != reconcile.ActionPrompt {
			resolved = append(resolved, a)
			continue
		}
		// Honor active batch mode.
		answer := *batchMode
		if answer == "" {
			fmt.Fprintf(w, "\n=== %s %s ===\n%s\n", r.Name(), a.Kind, a.Summary)
			for _, d := range a.Details {
				fmt.Fprintf(w, "  %s\n", d)
			}
			ans, err := cli.PromptTemplateChoice(w, reader)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp config sync: %v\n", err)
				return resolved, preApproved, true
			}
			answer = ans
		}
		switch answer {
		case "q":
			return resolved, preApproved, true
		case "S":
			*batchMode = "s"
			answer = "s"
		case "O":
			*batchMode = "o"
			answer = "o"
		case "N":
			*batchMode = "n"
			answer = "n"
		}
		switch answer {
		case "s":
			// Drop the action entirely.
		case "o":
			resolved = append(resolved, reconcile.Action{
				Kind:    reconcile.ActionUpdate,
				Target:  a.Target,
				Summary: a.Summary + " — overwrite",
				Details: a.Details,
			})
			preApproved[a.Target] = true
		case "n":
			// Write .new sidecar directly. The reconciler emits the
			// embedded RelPath in Details as "embedded_relpath=<rel>" so
			// we don't have to reverse-engineer it from the Target path.
			embRel := detailValue(a.Details, "embedded_relpath")
			if embRel == "" {
				slog.Error("template prompt missing embedded_relpath", "target", a.Target, "reconciler", r.Name())
				fmt.Fprintf(os.Stderr, "vp config sync: prompt action for %s missing embedded_relpath detail\n", a.Target)
				continue
			}
			if err := ensureEmbeddedBytes(); err != nil {
				slog.Error("walk embedded for .new sidecar", "err", err, "target", a.Target)
				fmt.Fprintf(os.Stderr, "vp config sync: walk embedded: %v\n", err)
				return resolved, preApproved, true
			}
			match, ok := embBytes[embRel]
			if !ok {
				slog.Error("embedded resource not found for prompt", "relpath", embRel, "target", a.Target)
				fmt.Fprintf(os.Stderr, "vp config sync: no embedded resource %q for %s\n", embRel, a.Target)
				continue
			}
			newPath := a.Target + ".new"
			if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "vp config sync: mkdir %s: %v\n", filepath.Dir(newPath), err)
				continue
			}
			// Rotate any existing .new to .new.bak first so users can
			// recover the prior sidecar body. Last-writer-wins: an
			// existing .new.bak is unconditionally replaced.
			if prior, err := os.ReadFile(newPath); err == nil {
				if werr := os.WriteFile(newPath+".bak", prior, 0o644); werr != nil {
					fmt.Fprintf(os.Stderr, "vp config sync: rotate %s.bak: %v\n", newPath, werr)
					continue
				}
			} else if !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "vp config sync: read %s: %v\n", newPath, err)
				continue
			}
			if err := os.WriteFile(newPath, match, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "vp config sync: write %s: %v\n", newPath, err)
				continue
			}
			fmt.Fprintf(w, "  wrote %s\n", newPath)
		}
	}
	return resolved, preApproved, false
}

// detailValue returns the value of a "<key>=<value>" entry in
// Action.Details, or "" if the key is not present. Used to parse the
// structured fields TemplateTree emits on ActionPrompt (see
// reconcile.ActionPrompt doc comment for the full contract).
func detailValue(details []string, key string) string {
	prefix := key + "="
	for _, d := range details {
		if strings.HasPrefix(d, prefix) {
			return strings.TrimPrefix(d, prefix)
		}
	}
	return ""
}

// enumerateVaultProjectSlugs lists directory entries under
// <vaultRoot>/Projects/ that look like project slugs. Entries that
// aren't directories, and entries whose names start with "." or "_",
// are skipped. Result is sorted alphabetically for deterministic
// reconciler ordering.
func enumerateVaultProjectSlugs(vaultRoot string) []string {
	projectsDir := filepath.Join(vaultRoot, "Projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		// ENOENT is normal on a fresh vault before any project has been
		// initialized; everything else is a real operational problem
		// (permission, IO) that would silently suppress every project
		// scaffold row — exactly the kind of mystery we want to avoid.
		if !os.IsNotExist(err) {
			slog.Error("enumerate vault projects", "err", err, "dir", projectsDir)
		}
		return nil
	}
	var slugs []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		slugs = append(slugs, name)
	}
	// ReadDir on Linux returns sorted entries, but sort explicitly to
	// stay platform-independent.
	sort.Strings(slugs)
	return slugs
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
			// ActionRelock is not "actionable" in the prompt sense (it never
			// prompts), but it does carry pending work that Apply must persist
			// — so a relock-only plan must still reach Apply rather than
			// short-circuit on "Nothing to do". It then flows through the
			// non-actionable passthrough in the apply loop and auto-applies.
			if isActionable(a.Kind) || a.Kind == reconcile.ActionRelock {
				return true
			}
		}
	}
	return false
}

func isActionable(k reconcile.ActionKind) bool {
	return k == reconcile.ActionCreate || k == reconcile.ActionUpdate || k == reconcile.ActionPrompt
}

func mergeReports(dst *reconcile.Report, src reconcile.Report) {
	dst.Created += src.Created
	dst.Updated += src.Updated
	dst.Unchanged += src.Unchanged
	dst.Skipped += src.Skipped
	dst.Relocked += src.Relocked
	dst.Errors = append(dst.Errors, src.Errors...)
}

func finishSync(rep reconcile.Report) int {
	fmt.Fprintf(os.Stdout, "Summary: created=%d updated=%d unchanged=%d skipped=%d relocked=%d\n",
		rep.Created, rep.Updated, rep.Unchanged, rep.Skipped, rep.Relocked)
	if len(rep.Errors) > 0 {
		for _, e := range rep.Errors {
			slog.Error("config sync apply error", "err", e)
			fmt.Fprintf(os.Stderr, "  error: %v\n", e)
		}
		return cli.ExitSystem
	}
	return cli.ExitOK
}
