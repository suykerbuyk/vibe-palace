// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

var migrateKGFilenamesFlags = []cli.FlagDef{
	{Name: "--dry-run", Help: "Alias of the bare command: a plan-only preview that mutates nothing"},
	{Name: "--yes", Help: "APPLY the migration (rename, verify, stamp, and git-stage). Without this the command only previews"},
}

// cmdMigrateKGFilenames renames the KG triple files of the configured vault into
// the current FLAT, portable filename encoding. It is PLAN-FIRST: the bare
// command (or --dry-run) previews and mutates nothing; --yes applies, verifies,
// stamps, and stages (without committing). It is idempotent and resumable, and
// reads the raw subject/predicate/object from each file body, so it is
// independent of the old on-disk encoding.
func cmdMigrateKGFilenames() *cli.Command {
	return &cli.Command{
		Name:     "migrate kg-filenames",
		Synopsis: "vp migrate kg-filenames [--dry-run | --yes]",
		Description: "Rename every knowledge-graph triple file in the configured vault into the " +
			"current FLAT, portable filename encoding (slug + content hash), collapsing the old " +
			"accidentally-nested layout back to a single directory.\n\n" +
			"PLAN-FIRST SAFETY GATE: the bare command (and --dry-run) is a PLAN-ONLY preview — it " +
			"prints the projects, the would-rename and would-collapse counts, and ANY detected " +
			"collisions or bad files, and mutates NOTHING. Pass --yes to apply.\n\n" +
			"Under --yes the run REFUSES unless the vault git working tree is clean (so " +
			"`git checkout .` is a guaranteed rollback), acquires exclusive vault access, pre-scans " +
			"and refuses on any unparseable/non-Triple file or filename collision (mutating nothing), " +
			"renames, runs a post-migration verification pass, stamps the vault data format " +
			"(.vibe-palace/vault.toml), and git-STAGES the changes without committing — you review " +
			"and commit deliberately. Idempotent and resumable; a file already in the new encoding is " +
			"left untouched.",
		Flags: migrateKGFilenamesFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate kg-filenames", Comment: "Plan-only preview; mutates nothing"},
			{Cmd: "vp migrate kg-filenames --yes", Comment: "Apply, verify, stamp, and stage (no commit)"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateKGFilenamesFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate kg-filenames: %v\n", err)
				return cli.ExitUser
			}
			apply := fv.Bool("--yes")

			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate kg-filenames: open vault: %v\n", err)
				return cli.ExitUser
			}

			if !apply {
				m, err := runKGFilenamePlan(vault)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\nvp migrate kg-filenames: %v\n", err)
					return cli.ExitSystem
				}
				printPlanPreview(os.Stderr, m)
				return cli.ExitOK
			}

			m, staged, err := runKGFilenameApply(vault)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\nvp migrate kg-filenames: %v\n", err)
				return cli.ExitSystem
			}
			fmt.Fprintf(os.Stderr,
				"Renamed %d triple file(s) and collapsed %d duplicate(s) across %d project(s); %d already current (%d scanned).\n",
				m.Renamed, m.Collapsed, m.Projects, m.AlreadyOK, m.Scanned)
			fmt.Fprintf(os.Stderr, "Vault stamped at data format v%d.\n", surface.RequiredDataFormat)
			fmt.Fprintf(os.Stderr,
				"Staged %d path group(s); review `git diff --staged --stat`, then commit.\n", staged)
			return cli.ExitOK
		},
	}
}

// runKGFilenamePlan performs the plan-only preview: it scans and reports what
// WOULD change (plus any collisions/bad files) and mutates nothing.
func runKGFilenamePlan(vault *storage.Vault) (storage.TripleFilenameMigration, error) {
	return vault.PlanTripleFilenameMigration()
}

// runKGFilenameApply enforces the git posture (refuse-on-dirty), applies the
// migration (exclusive lock, pre-scan refusal, rename, and the post-migration
// verification pass — all inside ApplyTripleFilenameMigration), stamps the vault
// at RequiredDataFormat ONLY after a verified success, then git-STAGES the
// renamed triples and the (otherwise-gitignored) format stamp WITHOUT
// committing. Returns the migration result and the number of staged path groups.
func runKGFilenameApply(vault *storage.Vault) (storage.TripleFilenameMigration, int, error) {
	root := vault.Root

	// REFUSE-ON-DIRTY: a clean tree makes `git checkout .` a guaranteed rollback.
	if !storage.GitAvailable() || !storage.GitIsRepo(root) {
		return storage.TripleFilenameMigration{}, 0, fmt.Errorf(
			"vault at %s is not a git repository; the migration requires git so `git checkout .` is a guaranteed rollback", root)
	}
	clean, err := storage.GitStatusClean(root)
	if err != nil {
		return storage.TripleFilenameMigration{}, 0, fmt.Errorf("check vault git status: %w", err)
	}
	if !clean {
		return storage.TripleFilenameMigration{}, 0, fmt.Errorf(
			"vault git working tree at %s is dirty; commit or stash first so `git checkout .` cleanly rolls back the rename", root)
	}

	m, err := vault.ApplyTripleFilenameMigration()
	if err != nil {
		return m, 0, err
	}

	// Stamp ONLY after a verified, error-free pass — a half-migrated vault must
	// still read as format 0.
	if err := surface.WriteFormat(root, surface.RequiredDataFormat); err != nil {
		return m, 0, fmt.Errorf("stamp vault data format: %w", err)
	}

	// STAGE (do not commit). `palace` covers every renamed/removed triple; the
	// tree was clean, so nothing else under it is staged. The stamp lives under
	// the gitignored `.vibe-palace/` dir, so it is force-added — once tracked it
	// syncs to every other host with the renamed data.
	staged := 0
	if err := storage.GitAdd(root, "palace"); err != nil {
		return m, 0, fmt.Errorf("stage renamed triples: %w", err)
	}
	staged++
	if err := storage.GitAddForce(root, filepath.Join(".vibe-palace", "vault.toml")); err != nil {
		return m, 0, fmt.Errorf("stage format stamp: %w", err)
	}
	staged++
	return m, staged, nil
}

// printPlanPreview renders the plan-only preview to w.
func printPlanPreview(w *os.File, m storage.TripleFilenameMigration) {
	fmt.Fprintf(w, "PLAN (no changes made). Run with --yes to apply.\n")
	fmt.Fprintf(w, "  Projects scanned    : %d\n", m.Projects)
	fmt.Fprintf(w, "  Triple files scanned: %d\n", m.Scanned)
	fmt.Fprintf(w, "  Would rename        : %d\n", m.Renamed)
	fmt.Fprintf(w, "  Would collapse dups : %d\n", m.Collapsed)
	fmt.Fprintf(w, "  Already current     : %d\n", m.AlreadyOK)
	if len(m.BadFiles) > 0 {
		fmt.Fprintf(w, "  BAD FILES (%d) — apply will REFUSE until these are resolved:\n", len(m.BadFiles))
		for _, b := range m.BadFiles {
			fmt.Fprintf(w, "    %s\n      %s\n", b.Path, b.Reason)
		}
	}
	if len(m.Collisions) > 0 {
		fmt.Fprintf(w, "  COLLISIONS (%d) — apply will REFUSE (a collapse would DELETE a distinct triple):\n", len(m.Collisions))
		for _, c := range m.Collisions {
			fmt.Fprintf(w, "    target %s\n      (%s, %s, %s)  vs  (%s, %s, %s)\n",
				c.NewPath, c.SourceS, c.SourceP, c.SourceO, c.OtherS, c.OtherP, c.OtherO)
		}
	}
	if !m.HasBlockers() {
		fmt.Fprintf(w, "  No collisions or bad files detected — safe to apply with --yes.\n")
	}
}
