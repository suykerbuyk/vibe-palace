// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

// ONE-SHOT. `vp migrate project-slug` moves every piece of one project's vault
// history to another slug (task
// migrate-quantum-ng-vault-history-to-qa-metabuild-system). Delete, in ONE
// commit: this file and cmd_migrate_project_slug_test.go;
// internal/storage/project_slug_migration.go, its two tests and its two
// project_slug_migration_owner_*.go helpers; the `// ONE-SHOT`
// registration line in commands.go; the "migrate project-slug" wantMutating
// row in main_test.go; and the PlanProjectSlugMigration entry in
// internal/sourceaudit's plannerFuncs; and the whole
// scripts/oneshot-project-slug/ harness directory.
//
// All logic lives in storage (see that file's header). This file only parses
// flags and routes output: report counts to stdout (--json: tracked vault
// state only), the LIVE classification line to stderr, and the K0/K1/K2 and
// cache progress lines to stdout.

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// slugProcDir and slugHomeDir are TEST SEAMS: the process table the guard
// scans and the home whose config.toml decides LIVE.
var (
	slugProcDir = "/proc"
	slugHomeDir = os.UserHomeDir
)

var migrateProjectSlugFlags = []cli.FlagDef{
	{Name: "--from", Arg: "SLUG", Help: "Source project slug (required)"},
	{Name: "--to", Arg: "SLUG", Help: "Destination project slug (required)"},
	{Name: "--vault", Arg: "PATH", Help: "Vault root (default: the configured vault_path)"},
	{Name: "--json", Help: "Report mode: print the tracked-state counts as JSON (counts.json) on stdout"},
	{Name: "--apply", Help: "Run K0, K1, K2 and the cache step (needs --expect, --expect-head, --journal); with --phase cache, run the per-host cache step"},
	{Name: "--expect", Arg: "FILE", Help: "counts.json the fresh report must equal byte for byte"},
	{Name: "--expect-head", Arg: "SHA", Help: "The vault HEAD the apply must start from"},
	{Name: "--journal", Arg: "FILE", Help: "Journal for the operations git cannot undo; must not exist or be empty"},
	{Name: "--phase", Arg: "PHASE", Help: "Only \"cache\": the per-host embed-cache step"},
	{Name: "--revert-journal", Arg: "FILE", Help: "Replay a journal in reverse (rollback), after git has restored the tracked tree"},
	{Name: "--force-root", Help: "--revert-journal only: replay a journal whose recorded vault is not this one (say so only when the vault itself was moved)"},
	{Name: "--check-guard", Help: "Only classify the vault and run the process guard (M3); exit 0 when it passes. Writes nothing."},
	{Name: "--attest-no-agents", Help: "--phase cache and --revert-journal only: proceed where processes cannot be enumerated (non-Linux); the operator attests no agent runs"},
}

func cmdMigrateProjectSlug() *cli.Command {
	return &cli.Command{
		Name:     "migrate project-slug",
		Synopsis: "vp migrate project-slug --from SLUG --to SLUG [--vault PATH] [--json | --apply ... | --phase cache --apply ... | --revert-journal FILE]",
		Description: "ONE-SHOT: move all vault history of one project slug to another. The bare command " +
			"reports and writes nothing (--json prints the tracked-state counts). --apply makes three vault " +
			"commits (0/2 make room, 1/2 rename, 2/2 identifier rewrite), then renames the host-local embed " +
			"cache; it never pushes. --phase cache --apply is the per-host cache step. --revert-journal " +
			"replays a journal in reverse after git has restored the tracked tree.",
		Flags: migrateProjectSlugFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate project-slug --from quantum-ng --to qa-metabuild-system --json > counts.json", Comment: "Report"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateProjectSlugFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate project-slug: %v\n", err)
				return cli.ExitUser
			}
			return runMigrateProjectSlug(fv, os.Stdout, os.Stderr)
		},
	}
}

// prefixRouter sends log lines that start with "vault=" (the LIVE line) to
// errw and every other line to outw. It splits on newlines, so a writer that
// batches several lines into one Write still routes each of them.
type prefixRouter struct{ outw, errw io.Writer }

func (r prefixRouter) Write(p []byte) (int, error) {
	rest := string(p)
	for rest != "" {
		line := rest
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[:i+1], rest[i+1:]
		} else {
			rest = ""
		}
		w := r.outw
		if strings.HasPrefix(line, "vault=") {
			w = r.errw
		}
		if _, err := io.WriteString(w, line); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func runMigrateProjectSlug(fv *cli.FlagValues, stdout, stderr io.Writer) int {
	const name = "vp migrate project-slug"
	root, err := resolveMigrationVaultRoot(fv.Get("--vault"))
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return cli.ExitUser
	}
	// The surface gate comes first, in every mode: preRun's gate checked only
	// the configured vault, and --vault may name another root.
	if code := enforceSurfaceOnRoot(root); code != cli.ExitOK {
		return code
	}
	from, to := fv.Get("--from"), fv.Get("--to")
	if from == "" || to == "" {
		fmt.Fprintf(stderr, "%s: --from and --to are required\n", name)
		return cli.ExitUser
	}
	home, err := slugHomeDir()
	if err != nil {
		home = ""
	}
	log := prefixRouter{outw: stdout, errw: stderr}
	phase := fv.Get("--phase")
	if phase != "" && phase != "cache" {
		fmt.Fprintf(stderr, "%s: --phase must be \"cache\"\n", name)
		return cli.ExitUser
	}

	if fv.Bool("--force-root") && fv.Get("--revert-journal") == "" {
		fmt.Fprintf(stderr, "%s: --force-root is accepted only with --revert-journal\n", name)
		return cli.ExitUser
	}

	if fv.Bool("--check-guard") {
		live, detail := storage.ClassifySlugVault(root, home)
		fmt.Fprintf(log, "vault=%s LIVE=%v (%s)\n", root, live, detail)
		if err := storage.SlugGuard(live, slugProcDir, false); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return cli.ExitUser
		}
		fmt.Fprintln(stdout, "GUARD OK")
		return cli.ExitOK
	}

	if jp := fv.Get("--revert-journal"); jp != "" {
		live, detail := storage.ClassifySlugVault(root, home)
		fmt.Fprintf(log, "vault=%s LIVE=%v (%s)\n", root, live, detail)
		// A rollback RESTORES; refusing it where processes cannot be
		// enumerated would strand a Windows host with a journal it may not
		// replay (code review round 1, C6). The attestation is the same one
		// --phase cache takes there, and the host sheet records it.
		if err := storage.SlugGuard(live, slugProcDir, fv.Bool("--attest-no-agents")); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return cli.ExitUser
		}
		res, err := storage.ReplaySlugJournal(root, jp, fv.Bool("--force-root"))
		for _, w := range res.Warnings {
			fmt.Fprintf(stderr, "%s: WARNING: %s\n", name, w)
		}
		fmt.Fprintln(stdout, res.String())
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return cli.ExitSystem
		}
		return cli.ExitOK
	}

	if phase == "cache" {
		if !fv.Bool("--apply") || fv.Get("--journal") == "" {
			fmt.Fprintf(stderr, "%s: --phase cache needs --apply and --journal\n", name)
			return cli.ExitUser
		}
		res, err := storage.RunSlugCachePhase(root, from, to, fv.Get("--journal"), slugProcDir, home, fv.Bool("--attest-no-agents"), log)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return cli.ExitSystem
		}
		fmt.Fprintf(stdout, "cache: %s\n", res)
		return cli.ExitOK
	}

	if fv.Bool("--apply") {
		if fv.Get("--expect") == "" || fv.Get("--expect-head") == "" || fv.Get("--journal") == "" {
			fmt.Fprintf(stderr, "%s: --apply needs --expect, --expect-head and --journal\n", name)
			return cli.ExitUser
		}
		if fv.Bool("--attest-no-agents") {
			fmt.Fprintf(stderr, "%s: --attest-no-agents is accepted only with --phase cache or --revert-journal\n", name)
			return cli.ExitUser
		}
		if err := refuseUnlessOwnGitRepo(root); err != nil {
			fmt.Fprintf(stderr, "%s: refusing: %v\n", name, err)
			return cli.ExitUser
		}
		expect, err := os.ReadFile(fv.Get("--expect"))
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return cli.ExitUser
		}
		res, err := storage.ApplyProjectSlugMigration(storage.SlugApplyOptions{
			Root: root, From: from, To: to,
			ExpectHead: fv.Get("--expect-head"), Expect: expect,
			JournalPath: fv.Get("--journal"), ProcDir: slugProcDir, Home: home, Log: log,
		})
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return cli.ExitSystem
		}
		if res.AlreadyApplied {
			fmt.Fprintln(stdout, "already applied")
		}
		return cli.ExitOK
	}

	plan, err := storage.PlanProjectSlugMigration(root, from, to)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return cli.ExitSystem
	}
	live, detail := storage.ClassifySlugVault(root, home)
	fmt.Fprintf(log, "vault=%s LIVE=%v (%s)\n", root, live, detail)
	counts, err := plan.Counts.JSON()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return cli.ExitSystem
	}
	if fv.Bool("--json") {
		_, _ = stdout.Write(counts)
		return cli.ExitOK
	}
	c := plan.Counts
	fmt.Fprintf(stdout, "K0 delete (%d):\n", len(c.K0Delete))
	for _, d := range c.K0Delete {
		fmt.Fprintf(stdout, "  %s\n", d)
	}
	fmt.Fprintf(stdout, "K0 rename (%d):\n", len(c.K0Rename))
	for _, r := range c.K0Rename {
		fmt.Fprintf(stdout, "  %s -> %s\n", r.Src, r.Dst)
	}
	fmt.Fprintf(stdout, "K1 tracked renames: %d (digest %s)\n", c.K1Tracked, c.K1TrackedDigest)
	fmt.Fprintf(stdout, "K1 untracked (journalled) moves (%d):\n", len(c.K1Untracked))
	for _, u := range c.K1Untracked {
		fmt.Fprintf(stdout, "  %s\n", u)
	}
	fmt.Fprintln(stdout, "K2 classes:")
	for _, k := range []string{"W1", "W1b", "W2", "W2b", "W3", "W4", "W5", "W6", "W7", "W8", "W9", "W10", "W10b", "W10c"} {
		fmt.Fprintf(stdout, "  %-5s %d\n", k, c.Classes[k])
	}
	fmt.Fprintln(stdout, "report only: nothing was written")
	return cli.ExitOK
}
