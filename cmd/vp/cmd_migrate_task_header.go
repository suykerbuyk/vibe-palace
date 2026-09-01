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

// The pass that normalizes a task header written before the current contract.
//
// # The defect this exists for
//
// A task file whose header predates the contract carries its status as a BARE,
// un-bolded "Status: value" line under the title. `storage.headerFieldValue` is
// THE definition of a metadata line for that package and is prefix-matched on
// "**Status:**", so the legacy line is invisible to every reader — but not to
// `headerBlock`, which stops at the first non-field line and so reports an EMPTY
// header block. `validateWholeTaskFile` then refuses the file, and since every
// whole-file writer goes through that validator, no tool could repair these
// files, including the migrations built to repair them.
//
// # Scope, and why it is narrower than the population
//
// The corpus carries four classes (`storage.ScanLegacyHeader`) and only ONE has a
// provably lossless mechanical repair. This command REPORTS all four and WRITES
// only `LegacyHeaderBoth`, where a true un-bolded value sits above a stale bolded
// one so carrying it across loses nothing.
//
// BARE-ONLY is not a smaller version of that. Its bare line is the file's only
// status declaration, so the deletion an earlier plan proposed for the whole
// population would destroy the status and leave the file refused at the
// validator's "missing Status" arm instead of repaired. Its repair is PROMOTION,
// and the values are free prose that wraps across lines. MULTI-TITLE needs a
// per-file judgment call between two disagreeing headers. Both are separate
// tasks, and the refusal in `storage.RepairLegacyBothHeader` is what keeps this
// command inside that split rather than relying on anyone remembering it.
//
// # Plan-first
//
// The bare command REPORTS and mutates nothing; --apply writes. A command whose
// default is "write" gets its report skimmed afterwards instead of read before.
//
// # Why there is deliberately NO whole-vault clean-tree precondition
//
// Its siblings call `requireCleanVaultTree`, which reaches `storage.GitStatusClean`
// — repo-wide — and that is unsatisfiable in a live agent session: pane captures,
// SessionEnd hooks and transcript flushes write under `Projects/*/sessions/` and
// `transcripts/` continuously, so the tree re-dirties within seconds of being
// cleaned. An operator hit it twice in a row doing nothing wrong.
//
// This command does not need one. Every write is a single `atomicfile.Write`
// through the locked task writer; each file's repair is independently valid and
// idempotent; and a repaired file classifies as `clean`, so re-running converges.
// A run interrupted halfway therefore leaves every file in one of two CORRECT
// states — repaired or untouched — never a partial one. There is no torn state
// for a precondition to protect, which is the honest reason to omit the guard
// rather than merely declining to inherit it.
//
// # One definition, shared with the classifier
//
// Every structural decision — what a legacy line is, which class a file falls in,
// what the repair writes — comes from `internal/storage` and never from a local
// copy. This file walks directories and prints; it does not re-decide anything.

// taskHeaderDirs are the task directories this command walks. Unlike
// `migrate task-status` it is NOT archive-only: the multi-title class it reports
// includes ACTIVE tasks, and a legacy header is not made harmless by living in a
// directory this command happens not to visit.
var taskHeaderDirs = []string{"", "done", "cancelled"}

var migrateTaskHeaderFlags = []cli.FlagDef{
	{Name: "--vault", Arg: "PATH", Help: "Vault root to scan and repair (default: the configured vault_path)"},
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Limit to one project (default: every project in the vault)"},
	{Name: "--apply", Help: "WRITE the repair. Without this the command only reports."},
}

func cmdMigrateTaskHeader() *cli.Command {
	return &cli.Command{
		Name:     "migrate task-header",
		Synopsis: "vp migrate task-header [--vault PATH] [--project P] [--apply]",
		Description: "Normalize task headers written before the current contract, where the status " +
			"sits on a BARE un-bolded \"Status:\" line under the title instead of inside the " +
			"contiguous \"**Field:**\" run.\n\n" +
			"Such a file is refused by validateWholeTaskFile — the bare line ends the header block " +
			"before it starts — and every whole-file writer goes through that validator, so no " +
			"other tool can repair these files.\n\n" +
			"PLAN-FIRST: the bare command REPORTS and writes nothing; pass --apply to write.\n\n" +
			"FOUR CLASSES are reported and exactly ONE is written. \"both\" (a true bare line above " +
			"a stale bolded field) is repaired by dropping the bare line and carrying its value " +
			"onto the bolded field, in a SINGLE write — a two-step would leave the file asserting " +
			"only the stale value, and a crash between the steps would make that permanent. " +
			"\"bare-only\" (the bare line is the file's ONLY status) needs PROMOTION rather than " +
			"deletion and is reported, never written. \"multi-title\" (a modern header prepended " +
			"above an intact legacy document) needs a per-file judgment call between two " +
			"disagreeing headers and is reported, never written. \"clean\" files are left alone.\n\n" +
			"Scope is every task directory — active, done/ and cancelled/ — because the classes " +
			"span all three. Writes go through the locked, surface-stamping task writer, never the " +
			"generic vault file tools (which refuse task paths), and the repaired file is checked " +
			"against validateWholeTaskFile before it is written.\n\n" +
			"There is deliberately NO whole-vault clean-tree precondition: every write is atomic " +
			"and idempotent, so an interrupted run leaves each file either repaired or untouched, " +
			"never torn. A run in which any file failed exits non-zero.",
		Flags: migrateTaskHeaderFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate task-header", Comment: "Report every class across the vault; writes nothing"},
			{Cmd: "vp migrate task-header -p vibe-palace", Comment: "Report for one project"},
			{Cmd: "vp migrate task-header -p vibe-palace --apply", Comment: "Repair the \"both\" class in one project"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateTaskHeaderFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-header: %v\n", err)
				return cli.ExitUser
			}
			root, err := resolveMigrationVaultRoot(fv.Get("--vault"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-header: %v\n", err)
				return cli.ExitUser
			}
			sum, err := runTaskHeaderMigration(root, fv.Get("--project"), fv.Bool("--apply"), os.Stdout)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-header: %v\n", err)
				return cli.ExitSystem
			}
			// The rule its sibling `migrate task-status` established and
			// `migrate task-preamble` was fixed to honour: a run that wrote
			// nothing must not report success.
			if sum.Failed > 0 {
				fmt.Fprintf(os.Stderr, "vp migrate task-header: %d file(s) failed\n", sum.Failed)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

// taskHeaderPlan is one file's classification, kept so a test can assert on the
// roll-up without re-parsing the printed report.
type taskHeaderPlan struct {
	Project string
	Slug    string
	Dir     string // "", "done" or "cancelled"
	Class   storage.LegacyHeaderClass
	Applied bool
	Failed  bool
}

type taskHeaderSummary struct {
	Scanned int
	Clean   int
	// Reported per class. Both is the only one this command can write; the
	// other two are counted so the report sizes the work it is NOT doing.
	Both       int
	BareOnly   int
	MultiTitle int
	Applied    int
	// Failed counts ATTEMPTS that went wrong — read errors, refused shadows and
	// failed writes — never a class this command declines to repair by design.
	Failed int
	Plans  []taskHeaderPlan
}

// runTaskHeaderMigration is the whole command, injectable for tests.
//
// It walks the task DIRECTORIES rather than asking vp_list_tasks: the listing is
// built from the active graph and hides both the icebox and every archived file,
// which is most of this population.
func runTaskHeaderMigration(root, only string, apply bool, out io.Writer) (taskHeaderSummary, error) {
	var sum taskHeaderSummary

	projects, err := taskPreambleProjects(root, only)
	if err != nil {
		return sum, err
	}

	printVaultRoot(out, root)
	if apply {
		fmt.Fprintln(out, "Mode:  APPLY — \"both\" files will be rewritten; every other class is reported only.")
	} else {
		fmt.Fprintln(out, "Mode:  REPORT ONLY — nothing is written. Pass --apply to write.")
	}
	fmt.Fprintln(out)

	vault := storage.NewVault(root)

	for _, project := range projects {
		for _, sub := range taskHeaderDirs {
			dir := filepath.Join(root, "Projects", project, "tasks", sub)
			entries, rerr := os.ReadDir(dir)
			if rerr != nil {
				// A project with no done/ or cancelled/ is normal, not a defect.
				continue
			}
			var names []string
			for _, e := range entries {
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
					fmt.Fprintf(out, "  !!    %s/%s: read: %v\n", project, taskSlug, ferr)
					sum.Failed++
					continue
				}
				sum.Scanned++

				before := string(data)
				scan := storage.ScanLegacyHeader(before)
				plan := taskHeaderPlan{Project: project, Slug: taskSlug, Dir: sub, Class: scan.Class}

				switch scan.Class {
				case storage.LegacyHeaderClean:
					sum.Clean++
					continue
				case storage.LegacyHeaderBareOnly:
					sum.BareOnly++
					fmt.Fprintf(out, "  SKIP  %s\n        bare-only: %q is this file's ONLY status. Promotion, not deletion — separate task.\n",
						taskHeaderWhere(project, sub, taskSlug), scan.BareValue)
					sum.Plans = append(sum.Plans, plan)
					continue
				case storage.LegacyHeaderMultiTitle:
					sum.MultiTitle++
					fmt.Fprintf(out, "  SKIP  %s\n        multi-title: two headers disagree; the choice is per-file — separate task.\n",
						taskHeaderWhere(project, sub, taskSlug))
					sum.Plans = append(sum.Plans, plan)
					continue
				}

				sum.Both++
				fmt.Fprintf(out, "  FIX   %s\n        drop bare %q, carry it onto **Status:** (was %q)\n",
					taskHeaderWhere(project, sub, taskSlug), scan.BareValue, scan.BoldValue)

				if !apply {
					sum.Plans = append(sum.Plans, plan)
					continue
				}

				// 🔴 The shadow guard. Vault.resolveTaskFile searches active
				// FIRST, so overwriting an ARCHIVED slug that also exists under
				// tasks/ would silently rewrite the active file instead. Refuse
				// rather than guess which one the operator meant.
				if sub != "" && fileExists(filepath.Join(root, "Projects", project, "tasks", name)) {
					fmt.Fprintf(out, "  !!    %s: an ACTIVE task of the same slug exists; refusing (the writer resolves active first)\n",
						taskHeaderWhere(project, sub, taskSlug))
					plan.Failed = true
					sum.Failed++
					sum.Plans = append(sum.Plans, plan)
					continue
				}

				after, rerr := storage.RepairLegacyBothHeader(before)
				if rerr != nil {
					fmt.Fprintf(out, "  !!    %s: %v\n", taskHeaderWhere(project, sub, taskSlug), rerr)
					plan.Failed = true
					sum.Failed++
					sum.Plans = append(sum.Plans, plan)
					continue
				}
				if werr := vault.OverwriteTaskFile(project, taskSlug, after); werr != nil {
					fmt.Fprintf(out, "  !!    %s: write: %v\n", taskHeaderWhere(project, sub, taskSlug), werr)
					plan.Failed = true
					sum.Failed++
					sum.Plans = append(sum.Plans, plan)
					continue
				}
				plan.Applied = true
				sum.Applied++
				sum.Plans = append(sum.Plans, plan)
			}
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Scanned %d file(s): %d clean, %d both, %d bare-only, %d multi-title.\n",
		sum.Scanned, sum.Clean, sum.Both, sum.BareOnly, sum.MultiTitle)
	if apply {
		fmt.Fprintf(out, "Applied %d rewrite(s).\n", sum.Applied)
	} else if sum.Both > 0 {
		fmt.Fprintln(out, "Re-run with --apply to write the \"both\" repairs.")
	}
	if sum.BareOnly > 0 || sum.MultiTitle > 0 {
		fmt.Fprintln(out, "bare-only and multi-title are reported by design: each needs its own repair, "+
			"filed separately. This command will never write them.")
	}
	if sum.Failed > 0 {
		fmt.Fprintf(out, "%d file(s) FAILED.\n", sum.Failed)
	}
	return sum, nil
}

// taskHeaderWhere renders a task's location for the report. The directory is
// part of the identity here: the same slug can exist active and archived, and
// that collision is exactly what the shadow guard refuses.
func taskHeaderWhere(project, sub, slug string) string {
	if sub == "" {
		return project + "/" + slug
	}
	return project + "/" + sub + "/" + slug
}
