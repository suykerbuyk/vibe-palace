// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The one-time pass that moves every ACTIVE task's preamble under the
// conventional first heading.
//
// # Why a CLI one-shot and not an MCP tool
//
// Same reasoning as `vp migrate iteration-headings`: this runs ONCE, under a
// human who reads the report before and after. "Rewrite every active task's
// preamble" on the agent surface, forever, to close a defect that exists once,
// is a writer that eventually fires when nobody is looking.
//
// # Plan-first
//
// The bare command REPORTS and mutates nothing; --apply writes. The interesting
// output is the per-file plan and the SKIPPED list, and a command whose default
// is "write" gets its report skimmed afterwards instead of read before.
//
// # Why it moves ALL preamble text
//
// See storage.MovePreambleUnderContext. There is no claim/provenance
// classifier, deliberately: no rule separating the two survives contact with
// real files. Moving everything makes "non-empty preamble" itself the finding.

var migrateTaskPreambleFlags = []cli.FlagDef{
	{Name: "--vault", Arg: "PATH", Help: "Vault root to scan and repair (default: the configured vault_path)"},
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Limit to one project (default: every project in the vault)"},
	{Name: "--apply", Help: "WRITE the migration. Without this the command only reports."},
}

func cmdMigrateTaskPreamble() *cli.Command {
	return &cli.Command{
		Name:     "migrate task-preamble",
		Synopsis: "vp migrate task-preamble [--vault PATH] [--project P] [--apply]",
		Description: "Move every ACTIVE task's preamble — all text between the header block and the " +
			"first H2 — down under the conventional first heading (\"## " + storage.ConventionalFirstHeading + "\").\n\n" +
			"PLAN-FIRST: the bare command REPORTS and writes nothing; pass --apply to write.\n\n" +
			"A preamble is written once by `create`. Until `vp_manage_task action=overwrite` shipped, no " +
			"typed action could revise it, so a task whose body had been fully corrected still opened " +
			"with its original framing. Overwrite closed that hole for anything written from now on; " +
			"this pass is for the files that already exist. Tasks created before `create` began " +
			"emitting the conventional heading still carry whatever sits above their first H2, and " +
			"moving it under a heading is what makes it addressable by `amend`.\n\n" +
			"ALL preamble text moves, provenance included. There is deliberately no " +
			"claim-versus-provenance classifier: no rule separating them survives contact with real " +
			"files, and moving everything makes a NON-EMPTY preamble itself the finding — mechanical, " +
			"with the same structural zero `create` already guarantees for new tasks.\n\n" +
			"Scope is active tasks only (icebox included); tasks/done/ and tasks/cancelled/ are " +
			"excluded, because every task writer refuses an archived task and a finding there would " +
			"be unrepairable. A file with NO \"## \" heading at all is SKIPPED and reported as its own " +
			"class — its \"preamble\" under the region definition is the entire body, and rewriting " +
			"that end-to-end is not what this command means.\n\n" +
			"Writes go through the locked, surface-stamping task writer, never the generic vault file " +
			"tools (which refuse task paths). Under --apply the vault git working tree must be clean, " +
			"so `git checkout .` is a guaranteed rollback.",
		Flags: migrateTaskPreambleFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate task-preamble", Comment: "Report what would move; writes nothing"},
			{Cmd: "vp migrate task-preamble -p vibe-palace", Comment: "Report for one project"},
			{Cmd: "vp migrate task-preamble --apply", Comment: "Apply to the configured vault"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateTaskPreambleFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-preamble: %v\n", err)
				return cli.ExitUser
			}
			root, err := resolveMigrationVaultRoot(fv.Get("--vault"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-preamble: %v\n", err)
				return cli.ExitUser
			}
			if _, err := runTaskPreambleMigration(root, fv.Get("--project"), fv.Bool("--apply"), os.Stdout); err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-preamble: %v\n", err)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

// taskPreamblePlan is one file's decision, kept so a test can assert on the
// roll-up without re-parsing the printed report.
type taskPreamblePlan struct {
	Project string
	Slug    string
	Outcome storage.PreambleOutcome
	Moved   int // bytes of preamble text moved (0 for empty/skipped)
	Applied bool
}

type taskPreambleSummary struct {
	Scanned int
	Empty   int
	Moved   int
	Skipped int
	Applied int
	Plans   []taskPreamblePlan
}

// runTaskPreambleMigration is the whole command, injectable for tests.
//
// It walks the task DIRECTORY rather than asking vp_list_tasks, for the reason
// the sibling audit dimension records: the listing hides the icebox, and an
// iceboxed task is still an active file with a preamble.
func runTaskPreambleMigration(root, only string, apply bool, out io.Writer) (taskPreambleSummary, error) {
	var sum taskPreambleSummary

	if apply {
		if err := requireCleanVaultTree(root); err != nil {
			return sum, err
		}
	}

	projects, err := taskPreambleProjects(root, only)
	if err != nil {
		return sum, err
	}

	fmt.Fprintf(out, "Vault: %s\n", root)
	if apply {
		fmt.Fprintln(out, "Mode:  APPLY — task files will be rewritten.")
	} else {
		fmt.Fprintln(out, "Mode:  REPORT ONLY — nothing is written. Pass --apply to write.")
	}
	fmt.Fprintln(out)

	vault := storage.NewVault(root)

	for _, slug := range projects {
		dir := filepath.Join(root, "Projects", slug, "tasks")
		entries, rerr := os.ReadDir(dir)
		if rerr != nil {
			// A project with no tasks dir is normal, not a defect.
			continue
		}
		var names []string
		for _, e := range entries {
			// Directory entries are tasks/done and tasks/cancelled — excluded
			// by construction, since we never descend.
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			names = append(names, e.Name())
		}
		sort.Strings(names)

		for _, name := range names {
			taskSlug := strings.TrimSuffix(name, ".md")
			data, ferr := os.ReadFile(filepath.Join(dir, name))
			if ferr != nil {
				fmt.Fprintf(out, "  !! %s/%s: read: %v\n", slug, taskSlug, ferr)
				continue
			}
			sum.Scanned++

			before := string(data)
			after, outcome := storage.MovePreambleUnderContext(before)
			plan := taskPreamblePlan{Project: slug, Slug: taskSlug, Outcome: outcome}

			switch outcome {
			case storage.PreambleEmpty:
				sum.Empty++
			case storage.PreambleSkippedNoH2:
				sum.Skipped++
				fmt.Fprintf(out, "  SKIP  %s/%s — no \"## \" heading; its preamble would be the whole body\n", slug, taskSlug)
			default:
				sum.Moved++
				plan.Moved = movedBytes(before, after)
				verb := "insert ## " + storage.ConventionalFirstHeading
				if outcome == storage.PreambleMovedIntoExistingContext {
					verb = "prepend into existing ## " + storage.ConventionalFirstHeading
				}
				fmt.Fprintf(out, "  MOVE  %s/%s — %s\n", slug, taskSlug, verb)
				if apply {
					if werr := vault.OverwriteTaskFile(slug, taskSlug, after); werr != nil {
						fmt.Fprintf(out, "  !! %s/%s: write: %v\n", slug, taskSlug, werr)
						sum.Plans = append(sum.Plans, plan)
						continue
					}
					plan.Applied = true
					sum.Applied++
				}
			}
			sum.Plans = append(sum.Plans, plan)
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Scanned %d active task file(s): %d already empty, %d to move, %d skipped (no H2).\n",
		sum.Scanned, sum.Empty, sum.Moved, sum.Skipped)
	if apply {
		fmt.Fprintf(out, "Applied %d rewrite(s).\n", sum.Applied)
	} else if sum.Moved > 0 {
		fmt.Fprintln(out, "Nothing was written. Re-run with --apply to write.")
	}
	return sum, nil
}

// movedBytes reports how much preamble text was relocated, for the report only.
func movedBytes(before, after string) int {
	if len(after) > len(before) {
		return len(after) - len(before)
	}
	return len(before) - len(after)
}

// taskPreambleProjects lists the project slugs holding a tasks/ directory.
func taskPreambleProjects(root, only string) ([]string, error) {
	if strings.TrimSpace(only) != "" {
		return []string{strings.TrimSpace(only)}, nil
	}
	entries, err := os.ReadDir(filepath.Join(root, "Projects"))
	if err != nil {
		return nil, fmt.Errorf("read Projects/: %w", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, serr := os.Stat(filepath.Join(root, "Projects", e.Name(), "tasks")); serr != nil {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}
