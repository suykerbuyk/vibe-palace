// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

func cmdConfig() *cli.Command {
	return &cli.Command{
		Name:        "config",
		Synopsis:    "vp config <command>",
		Description: "Manage the global vibe-palace configuration.",
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
		Description: "Reconcile managed config files (global / vault / project tiers) against their canonical schemas. Idempotent on repeat runs. The vault tier also tops up the vault .gitignore and reconciles vault Templates/ override-only: vp never writes a template there. A copy identical, line endings aside, to the embedded copy or to an earlier version vibe-palace shipped is pruned, with no prompt and no backup; anything else is an operator override and is kept. On a git vault a copy is removed only after its committed copy (and each remote tip's) is checked to be vp's too, and the removal is then committed; when the committed copy is operator content it is restored from HEAD instead, and a prune git cannot verify is deferred. On a vault that is its own repository, a removal of vp-shipped bytes already pending in the worktree is committed the same way, and an untracked, un-ignored .vibe-palace/templates.lock (retired; no vp from this release reads it) is removed. Does not touch agent-files or slash-command shims — those are handled by `vp commands upgrade`.",
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
	// How git sees the vault decides who prunes a Templates mirror. On a vault
	// git can read, the reconciler hands every Delete to pruneOnGitVault,
	// which checks HEAD and each remote tip before removing anything; on an
	// unversioned vault the reconciler prunes directly; and on a git vault git
	// cannot read, every prune is deferred.
	//
	// On a vault that is its own repository two more things are read, both
	// read-only: the tracked Templates/ removals already pending in the
	// worktree (so a prune whose commit did not land — or a hand removal of
	// vp-shipped bytes — is committed now), and the retired templates.lock
	// (removed when untracked and not ignored). Neither runs on a nested,
	// broken or git-less vault. A git failure listing either is reported and
	// the run exits non-zero; the reconcile still runs, without it.
	var templatesTree *reconcile.TemplateTreeReconciler
	var templatesGit storage.VaultGit
	var templatesGitErr error
	var preErrors []error
	var retiredLock *reconcile.Action
	if vaultPathForTemplates != "" {
		templatesGit, templatesGitErr = storage.InspectVaultGit(vaultPathForTemplates)
		var pending map[string][]byte
		// Only when the Templates reconciler is part of this run: a
		// `--tier global` or `--tier project` sync never reads Templates/,
		// so a Templates/ git problem is not its error.
		if templatesGit == storage.VaultGitOK && (tier == "all" || tier == "vault") {
			var perr error
			pending, perr = storage.UncommittedRemovals(vaultPathForTemplates, "Templates", isBuiltinTemplateKey)
			if perr != nil {
				fmt.Fprintf(os.Stderr, "could not list uncommitted Templates/ removals (%v); none is committed this run\n", perr)
				preErrors = append(preErrors, fmt.Errorf("list uncommitted Templates/ removals: %w", perr))
				pending = nil
			}
			retiredLock, perr = planRetiredLockRemoval(vaultPathForTemplates)
			if perr != nil {
				fmt.Fprintf(os.Stderr, "could not check the retired %s (%v); it is left\n", storage.RetiredTemplatesLockRel, perr)
				preErrors = append(preErrors, fmt.Errorf("check the retired %s: %w", storage.RetiredTemplatesLockRel, perr))
			}
		}
		templatesTree = reconcile.NewTemplateTree(vaultPathForTemplates, "Templates",
			reconcile.TemplateTreeSeed{
				Mode:            reconcile.TemplateModeMaterialize,
				ExternalPrune:   templatesGit == storage.VaultGitOK || templatesGit == storage.VaultGitNested,
				PendingRemovals: pending,
			})
		all["Templates"] = templatesTree
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

	// A git vault whose committed copies cannot be read defers every Templates
	// prune: the removal could neither be checked against HEAD nor committed,
	// and a deletion left in the worktree blocks the next vault sync. The file
	// stays; a later sync that can read the repository prunes it. git missing
	// from PATH is an environment the operator chose, so it is reported and
	// the run succeeds; a repository git cannot use is an error to fix.
	if templatesTree != nil && (templatesGit == storage.VaultGitUnavailable || templatesGit == storage.VaultGitBroken) {
		reason := "prune deferred: the vault is a git repo and git is unavailable, so the committed copy cannot be verified"
		if templatesGit == storage.VaultGitBroken {
			reason = fmt.Sprintf("prune deferred: git cannot read the vault's repository (%v), so the committed copy cannot be verified", templatesGitErr)
		}
		for i, r := range order {
			if r != reconcile.Reconciler(templatesTree) {
				continue
			}
			var n int
			plans[i], n = deferPrunes(plans[i], vaultPathForTemplates, reason)
			if n > 0 && templatesGit == storage.VaultGitBroken {
				preErrors = append(preErrors, fmt.Errorf("%d Templates prune(s) deferred: git cannot read the vault's repository: %w", n, templatesGitErr))
			}
		}
	}

	// The retired lock's removal is planned beside the Templates prunes, so
	// --dry-run shows it. It names no built-in (no embedded_relpath), so no
	// prune takes it: runConfigSync removes it itself, after the Apply.
	if retiredLock != nil {
		for i, r := range order {
			if r == reconcile.Reconciler(templatesTree) {
				plans[i].Actions = append(plans[i].Actions, *retiredLock)
			}
		}
	}

	// Render plans.
	printSyncPlans(os.Stdout, order, plans)

	// A pre-run error (a Templates/ git read that failed) is an exit 2 on
	// every path — a dry run and "Nothing to do" included — never a green
	// run that only printed a line on stderr.
	if dryRun {
		return preErrorsExit(preErrors)
	}

	// Determine whether any action requires prompting.
	if !autoYes && !anyActionable(plans) {
		fmt.Fprintln(os.Stdout, "Nothing to do — all tiers in sync.")
		return preErrorsExit(preErrors)
	}

	reader := bufio.NewReader(os.Stdin)
	acceptAll := autoYes
	var totalReport reconcile.Report
	totalReport.Errors = append(totalReport.Errors, preErrors...)

	// No reconciler prompts about a Templates/ file. There used to be a
	// keep/.new prompt for an override of a built-in on a host whose
	// templates.lock did not record it — every host but the one that did,
	// on every interactive sync. Provenance is now decided by the binary
	// alone (the frozen shipped-version manifest), and an operator's copy is
	// always kept, so there is nothing to ask.
	for i, r := range order {
		actions := plans[i].Actions
		var lockAction *reconcile.Action
		if r == reconcile.Reconciler(templatesTree) {
			actions, lockAction = splitRetiredLock(actions)
		}

		filtered := reconcile.Plan{}
		for _, a := range actions {
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
		for _, n := range rep.Notes {
			fmt.Fprintf(os.Stdout, "  %s: %s\n", r.Name(), n)
		}
		// On a git vault the Templates prunes happen HERE, right after the
		// Apply that handed them over, not at the end of the run: the loop can
		// still hit an abort that returns early (the 'q' branch). A failure is
		// recorded rather than returned on the spot: the rest of the run still
		// happens, and finishSync turns the recorded error into ExitSystem.
		if templatesTree != nil && r == reconcile.Reconciler(templatesTree) && (templatesGit == storage.VaultGitOK || templatesGit == storage.VaultGitNested) {
			pruned, skipped, err := pruneOnGitVault(vaultPathForTemplates, templatesTree, filtered.Actions, templatesGit == storage.VaultGitNested)
			totalReport.Pruned += pruned
			totalReport.Skipped += skipped
			if err != nil {
				totalReport.Errors = append(totalReport.Errors, fmt.Errorf("prune vault mirrors: %w", err))
			}
		}
		if lockAction != nil {
			if err := removeRetiredLock(vaultPathForTemplates, *lockAction); err != nil {
				totalReport.Errors = append(totalReport.Errors, err)
			}
		}
	}
	return finishSync(totalReport)
}

// preErrorsExit is the exit status of a run that stops before any Apply:
// ExitSystem, with each error on stderr, when a pre-run check failed, and
// ExitOK otherwise.
func preErrorsExit(preErrors []error) int {
	if len(preErrors) == 0 {
		return cli.ExitOK
	}
	for _, e := range preErrors {
		slog.Error("config sync pre-run error", "err", e)
		fmt.Fprintf(os.Stderr, "  error: %v\n", e)
	}
	return cli.ExitSystem
}

// isBuiltinTemplateKey reports whether the vault-relative path rel is the
// Templates/ copy of a built-in — the only paths whose uncommitted removal
// `vp config sync` may commit.
func isBuiltinTemplateKey(rel string) bool {
	embeddedRel, ok := strings.CutPrefix(rel, "Templates/")
	if !ok {
		return false
	}
	_, builtin := templates.EmbeddedSHA(embeddedRel)
	return builtin
}

// planRetiredLockRemoval plans the removal of the retired
// .vibe-palace/templates.lock when storage.RetiredTemplatesLock says it may
// go: untracked, not ignored, on a vault that is its own repository (the
// caller's check). A lock left there is host-local dirt no vp from this
// release reads, and on a canonically configured git vault it makes `vp vault
// sync` refuse. A tracked or ignored lock, and any lock on a non-git, nested
// or broken vault — where a lagging host may still share it — is left.
func planRetiredLockRemoval(vaultPath string) (*reconcile.Action, error) {
	content, removable, err := storage.RetiredTemplatesLock(vaultPath)
	if err != nil || !removable {
		return nil, err
	}
	sum := sha256.Sum256(content)
	return &reconcile.Action{
		Kind:    reconcile.ActionDelete,
		Target:  filepath.Join(vaultPath, filepath.FromSlash(storage.RetiredTemplatesLockRel)),
		Summary: "remove the retired " + storage.RetiredTemplatesLockRel + " (untracked and not ignored; no vp from this release reads it, and it blocks vp vault sync)",
		Details: []string{"retired_lock=true", "vault_sha=" + hex.EncodeToString(sum[:])},
	}, nil
}

// splitRetiredLock takes the retired-lock removal out of a Templates plan, so
// the reconciler's Apply never sees a Delete that names no built-in.
func splitRetiredLock(actions []reconcile.Action) (rest []reconcile.Action, lock *reconcile.Action) {
	for i := range actions {
		if actions[i].Kind == reconcile.ActionDelete && actions[i].Detail("retired_lock") == "true" {
			a := actions[i]
			lock = &a
			continue
		}
		rest = append(rest, actions[i])
	}
	return rest, lock
}

// removeRetiredLock removes the retired templates.lock a plan named, after
// checking again that it is still untracked and not ignored, through the
// removal funnel's compare-and-set on the bytes the plan read: a lock changed
// since (a lagging vp on this host rewrote it) is kept, with a note.
func removeRetiredLock(vaultPath string, planned reconcile.Action) error {
	rel := storage.RetiredTemplatesLockRel
	_, removable, err := storage.RetiredTemplatesLock(vaultPath)
	if err != nil {
		return fmt.Errorf("check the retired %s: %w", rel, err)
	}
	if !removable {
		if _, serr := os.Lstat(filepath.Join(vaultPath, filepath.FromSlash(rel))); serr == nil {
			fmt.Fprintf(os.Stdout, "  left %s: it is no longer untracked and un-ignored\n", rel)
		}
		return nil // or already gone: nothing to do
	}
	if _, err := vaultfs.Delete(vaultPath, rel, planned.Detail("vault_sha")); err != nil {
		switch {
		case errors.Is(err, vaultfs.ErrShaConflict):
			fmt.Fprintf(os.Stdout, "  left %s: it changed since plan; kept\n", rel)
			return nil
		case errors.Is(err, vaultfs.ErrFileNotFound):
			return nil
		}
		return fmt.Errorf("remove the retired %s: %w", rel, err)
	}
	fmt.Fprintf(os.Stdout, "removed the retired %s\n", rel)
	return nil
}

// pruneOnGitVault removes the vault template mirrors a just-applied plan handed
// over (ExternalPrune), and commits the removal so it survives a sync to
// another host. It returns the counts to add to the run's Summary: pruned
// counts the paths this run removed and the pending removals it committed.
//
// Without the commit the prune is a worktree-only operation, and a pruned
// mirror returns on the next clone or pull. Scope is deliberately narrow, per
// the operator grant: the prune path of a mutates()-gated reconciler command
// commits THE PATHS IT PRUNES, and nothing else. WIDENED 2026-09-11 by the
// operator (template-provenance-manifest-retires-the-host-local-lock, M6): on
// a vault that is its own repository it may also commit a tracked removal
// already pending in the worktree whose committed copy classifies as
// vp-shipped (the current embedded copy or an earlier shipped version) — made
// by hand, by a reset whose commit failed, or by an earlier prune — and the
// commit message says so in a paragraph of its own. A pending removal of
// operator content is never committed (planMaterialize plans it Unchanged, and
// pruneMirrors' HEAD check would refuse it anyway).
//
// 🔴 NOTHING IS REMOVED BEFORE GIT HAS BEEN ASKED. The removal used to happen
// in Apply and the HEAD check at commit time, so any git failure in between —
// no identity, a corrupt index, a repository git refuses — left a committed
// operator override deleted in the worktree with nothing telling anyone.
// storage.PruneMirrorsVerified checks the worktree bytes, HEAD's copy and each
// remote tip's copy — each as git would check it out — with the one accept
// rule (reconcile.PruneAccepts: vp-shipped bytes for that built-in) BEFORE it
// removes a file, restores HEAD's copy where HEAD holds operator content, and
// defers (keeps) a path on any git error.
//
// What is guaranteed, exactly: a file whose committed copy is operator content
// is never removed, and no path is removed unless every check passed —
// including a fresh fetch of every remote. A removal can stay uncommitted only
// through a stage or commit failure (whose staging is undone) or a failed
// re-check after HEAD moved; such a path is printed with its manual restore
// command and the run exits non-zero — and the next sync finds it pending in
// the worktree (storage.UncommittedRemovals) and commits it.
//
// A vault nested inside another repository (nested) is verified and restored
// the same way, but that repository is never fetched, staged, committed or
// pushed: a tracked mirror there is kept, with a [Skip] row that says why.
func pruneOnGitVault(vaultPath string, tt *reconcile.TemplateTreeReconciler, applied []reconcile.Action, nested bool) (pruned, skipped int, err error) {
	var paths []string
	actionByRel := map[string]reconcile.Action{}
	for _, a := range applied {
		// Only a prune names the built-in it removes. Anything else — the
		// retired-lock removal — never reaches the verified prune.
		if a.Kind != reconcile.ActionDelete || a.Detail("embedded_relpath") == "" {
			continue
		}
		rel := vaultRelOf(vaultPath, a.Target)
		paths = append(paths, rel)
		actionByRel[rel] = a
	}
	if len(paths) == 0 {
		return 0, 0, nil
	}
	verifier := storage.PruneVerifier{
		Accept: func(rel string, content []byte) bool {
			return reconcile.PruneAccepts(actionByRel[rel], content)
		},
		Message: func(committed []string) string {
			basisOf := map[string]string{}
			pending := map[string]bool{}
			for _, rel := range committed {
				a := actionByRel[rel]
				pending[rel] = a.Detail("pending") == "true"
				if blob, found, rerr := storage.ReadCommittedContent(vaultPath, rel); rerr == nil && found {
					basisOf[rel] = pruneBasisWords(a.Detail("embedded_relpath"), templates.ClassifyVaultCopy(a.Detail("embedded_relpath"), blob))
				}
			}
			return prunedMirrorCommitMessage(committed, basisOf, pending)
		},
	}
	var res *storage.PushResult
	var out storage.PruneOutcome
	var downgraded bool
	var perr error
	if nested {
		// The repository is not the vault's: verify and restore only, never
		// fetch, stage, commit or push it.
		out, perr = storage.PruneMirrorsInEnclosingRepo(vaultPath, paths, verifier)
	} else {
		res, out, downgraded, perr = storage.PruneMirrorsVerifiedWithDowngrade(vaultPath, paths, true, verifier)
	}

	for _, rel := range out.Restored {
		fmt.Fprintf(os.Stdout, "restored %s from HEAD — the committed copy is operator content, not a vp mirror; it is kept\n", rel)
	}
	for _, k := range out.Kept {
		fmt.Fprintf(os.Stdout, "  [Skip] %s: %s — %s\n", tt.Name(), k.Path, k.Reason)
	}
	for _, f := range out.Failed {
		fmt.Fprintf(os.Stderr, "could not commit the prune of %s (%v): it is removed from the worktree and NOT committed — restore it with: git -C %s checkout HEAD -- %s\n",
			f.Path, f.Err, vaultPath, f.Path)
	}
	// The audit record of an earlier-version prune that no commit carries (an
	// untracked copy, or a nested vault's): no backup is kept, so say what
	// went, once, after it went.
	committed := map[string]bool{}
	for _, rel := range out.Committed {
		committed[rel] = true
	}
	removed := map[string]bool{}
	for _, rel := range out.Removed {
		removed[rel] = true
		if a := actionByRel[rel]; !committed[rel] && a.Detail("provenance") == templates.ProvenanceEarlier.String() {
			fmt.Fprintf(os.Stdout, "pruned %s (earlier shipped version of %s; no backup)\n", rel, a.Detail("embedded_relpath"))
		}
	}
	// A removal found pending in the worktree and committed now was not made
	// by this run, so pruneMirrors counts it Gone — but the commit that
	// publishes it is this run's, under the widened grant. Say so, one line
	// per path, and count it.
	var pendingCommitted int
	for _, rel := range out.Committed {
		if removed[rel] {
			continue
		}
		pendingCommitted++
		a := actionByRel[rel]
		basis := pruneBasisWords(a.Detail("embedded_relpath"), provenanceOf(a.Detail("provenance")))
		fmt.Fprintf(os.Stdout, "committed the pending removal of %s (%s)\n", rel, basis)
	}
	if res != nil && res.CommitSHA != "" {
		if downgraded {
			fmt.Fprintf(os.Stdout, "Pruned mirrors committed locally only (no remotes configured): %d path(s)\n", len(out.Committed))
		}
		if pushErr := reportPrunePush(res, out.Committed); pushErr != nil && perr == nil {
			perr = pushErr
		}
	}
	return len(out.Removed) + pendingCommitted, len(out.Kept), perr
}

// provenanceOf reads a Delete's provenance= detail back.
func provenanceOf(detail string) templates.Provenance {
	switch detail {
	case templates.ProvenanceCurrent.String():
		return templates.ProvenanceCurrent
	case templates.ProvenanceEarlier.String():
		return templates.ProvenanceEarlier
	}
	return templates.ProvenanceOperator
}

// pruneBasisWords is how a prune commit names a path's basis.
func pruneBasisWords(embeddedRel string, prov templates.Provenance) string {
	switch prov {
	case templates.ProvenanceCurrent:
		return "current embedded copy"
	case templates.ProvenanceEarlier:
		return "earlier shipped version of " + embeddedRel
	}
	return ""
}

// reportPrunePush makes a prune commit that did not reach every remote loud.
// A rejected push, an aborted rebase or an autostash conflict after the commit
// leaves the local prune commit stranded next to whatever the remote holds —
// and if the remote changed these paths, that copy is an operator's override:
// resolving the eventual merge conflict as a deletion would remove it from
// every host.
func reportPrunePush(res *storage.PushResult, committed []string) error {
	var failed []string
	for remote, e := range res.RemoteResults {
		if e != nil {
			failed = append(failed, remote)
		}
	}
	sort.Strings(failed)
	for _, remote := range failed {
		fmt.Fprintf(os.Stderr, "the prune commit %s (%s) did not reach %s: %v\n", res.CommitSHA, strings.Join(committed, ", "), remote, res.RemoteResults[remote])
	}
	if res.PopConflict {
		fmt.Fprintf(os.Stderr, "the prune commit %s landed, but re-applying your uncommitted changes conflicted in: %s (they are kept in `git stash list`)\n",
			res.CommitSHA, strings.Join(res.PopConflictPaths, ", "))
	}
	if len(failed) == 0 && !res.PopConflict {
		return nil
	}
	fmt.Fprintf(os.Stderr, "If a remote changed %s, its copy is operator content: keep it. When you pull, resolve any conflict on these paths by keeping the remote's file, never by deleting it.\n",
		strings.Join(committed, ", "))
	if len(failed) == 0 {
		return errors.New("re-applying uncommitted changes after the prune commit conflicted")
	}
	return fmt.Errorf("the prune commit %s did not reach %s", res.CommitSHA, strings.Join(failed, ", "))
}

// prunedMirrorCommitMessage composes the vault commit message for a prune.
// Nobody reviews this message at write time — it is machine-authored
// permanent history — which argues for it being more self-explanatory than a
// message a human approves, not less. It states only what was checked, and
// is composed from the paths that survived the HEAD check, so it cannot list
// a path the commit does not carry. A removal vp found already pending in the
// worktree gets a paragraph of its own: this sync did not make it.
func prunedMirrorCommitMessage(paths []string, basisOf map[string]string, pending map[string]bool) string {
	line := func(p string) string {
		l := "- " + p
		if basis := basisOf[p]; basis != "" {
			l += " (" + basis + ")"
		}
		return l + "\n"
	}
	var pruned, found []string
	for _, p := range paths {
		if pending[p] {
			found = append(found, p)
		} else {
			pruned = append(pruned, p)
		}
	}
	var b strings.Builder
	b.WriteString("chore(templates): prune vault mirrors superseded by the embedded floor\n\n")
	if len(pruned) > 0 {
		b.WriteString("`vp config sync` removed these vault Templates/ files. Each one's bytes,\n")
		b.WriteString("both in the worktree and in the commit it is removed from, equalled —\n")
		b.WriteString("line endings aside — the embedded copy this binary serves, or an earlier\n")
		b.WriteString("version vibe-palace shipped (internal/templates/shipped.txt, frozen at\n")
		b.WriteString("1f3bb62). So no operator content is removed, and the embedded floor\n")
		b.WriteString("serves each resource.\n\n")
		for _, p := range pruned {
			b.WriteString(line(p))
		}
		b.WriteString("\n")
	}
	if len(found) > 0 {
		b.WriteString("vp found these removals already pending in the worktree — made before\n")
		b.WriteString("this sync, by hand, by a reset whose commit failed, or by an earlier\n")
		b.WriteString("prune — and committed them because the committed copy of each is\n")
		b.WriteString("vp-shipped bytes (the embedded copy this binary serves, or an earlier\n")
		b.WriteString("version vibe-palace shipped, line endings aside):\n\n")
		for _, p := range found {
			b.WriteString(line(p))
		}
		b.WriteString("\n")
	}
	b.WriteString("The removal is committed here rather than left in the worktree because a\n")
	b.WriteString("prune that stops at one host's disk is undone by the next clone or pull on\n")
	b.WriteString("any other host. An earlier version's bytes are recoverable from\n")
	b.WriteString("vibe-palace's git history.\n")
	return b.String()
}

// deferPrunes rewrites a Templates plan's Delete actions to Skip with reason:
// the vault is a git repo whose committed copies cannot be read, and a prune
// whose committed copy cannot be checked is not made. It returns how many.
func deferPrunes(p reconcile.Plan, vaultPath, reason string) (reconcile.Plan, int) {
	out := reconcile.Plan{Actions: make([]reconcile.Action, 0, len(p.Actions))}
	n := 0
	for _, a := range p.Actions {
		if a.Kind == reconcile.ActionDelete {
			n++
			a = reconcile.Action{
				Kind:    reconcile.ActionSkip,
				Target:  a.Target,
				Summary: vaultRelOf(vaultPath, a.Target) + " " + reason,
			}
		}
		out.Actions = append(out.Actions, a)
	}
	return out, n
}

// vaultRelOf renders target relative to vaultPath with forward slashes, or
// target itself when it is not under vaultPath.
func vaultRelOf(vaultPath, target string) string {
	if vaultPath != "" {
		if rel, err := filepath.Rel(vaultPath, target); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return target
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
			// ActionDelete (prune) is not "actionable" in the prompt sense
			// (it never prompts), but it carries pending work that Apply must
			// persist — so a prune-only plan must still reach Apply rather than
			// short-circuit on "Nothing to do". It then flows through the
			// non-actionable passthrough in the apply loop and auto-applies.
			if isActionable(a.Kind) || a.Kind == reconcile.ActionDelete {
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
	dst.Pruned += src.Pruned
	dst.Errors = append(dst.Errors, src.Errors...)
}

func finishSync(rep reconcile.Report) int {
	fmt.Fprintf(os.Stdout, "Summary: created=%d updated=%d unchanged=%d skipped=%d pruned=%d\n",
		rep.Created, rep.Updated, rep.Unchanged, rep.Skipped, rep.Pruned)
	if len(rep.Errors) > 0 {
		for _, e := range rep.Errors {
			slog.Error("config sync apply error", "err", e)
			fmt.Fprintf(os.Stderr, "  error: %v\n", e)
		}
		return cli.ExitSystem
	}
	return cli.ExitOK
}
