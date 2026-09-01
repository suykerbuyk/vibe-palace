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
	"github.com/suykerbuyk/vibe-palace/internal/mdfence"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The one-time pass that makes an ARCHIVED task's `**Status:**` line agree with
// the directory it sits in.
//
// # Why this command exists at all, and why it is archive-scoped
//
// Every typed task writer refuses an archived task on purpose: the MCP
// `overwrite` handler refuses on `Done`, `vp tasks edit` refuses on `meta.Done`,
// and `vaultfs.IsTaskFilePath` gates the generic file tools off
// `Projects/*/tasks/` including `done/` and `cancelled/`. That is correct —
// an archived body is a record of what happened.
//
// The storage primitive underneath them is deliberately NOT scoped that way:
// `Vault.OverwriteTaskFile` resolves a task across active/done/cancelled and
// documents that "whether writing to an ARCHIVED task is permitted is the
// CALLER's concern". So the way to repair archived files without weakening a
// single guard is a new, deliberately archive-scoped CALLER — which is this
// command, built on the shape `vp migrate task-preamble` already uses one
// directory over.
//
// # Plan-first
//
// The bare command REPORTS and mutates nothing; --apply writes. A command whose
// default is "write" gets its report skimmed afterwards instead of read before.
//
// # One definition, shared with the detector
//
// This command and `vaultaudit.DimTaskStatusDirectory` must have the SAME
// population: the repair exists to drive rule 1 to zero, so a file either tool
// treats differently is drift. All three shared decisions therefore come from
// `internal/storage`, never from a local copy:
//
//   - WHICH LINE is the status — `storage.TaskStatusValue`, the narrow export of
//     headerFieldValue that the parser, the writers and the validator all use.
//   - WHETHER IT AGREES — `storage.IsTerminalStatus`, which folds case and trims,
//     because the real corpus spells its values inconsistently.
//   - WHAT TO WRITE — `storage.StatusRetired` / `storage.StatusCancelled`.
//
// An earlier revision asked `found == ad.status` instead. That is a second
// definition, and it diverges on real inputs: it rewrites "Retired", rewrites a
// done/ file reading "cancelled" into "retired", and disagrees on trailing
// whitespace — each time editing a file the audit never flagged.
// `TestRepairPopulationMatchesTheDetector` pins the two populations together over
// a fixture built from exactly those shapes, rather than over clean data where
// they agree by luck.
//
// What stays local is the repair TARGET, which is genuinely per-directory:
// done/ repairs to StatusRetired, cancelled/ to StatusCancelled.
//
// # Fence-awareness is load-bearing, not defensive
//
// Task bodies quote metadata-shaped lines inside code fences. Measured against
// the live vault on 2026-09-01, two archived files carry a `**Status:**` line
// inside a fence, and one of them (`task-relationships-epics-and-dependencies`)
// is `**Status:** pending` — sample text in a file whose real header says
// `retired`. A fence-blind detector reports that file as a disagreement, and a
// fence-blind rewriter can edit the sample. Neither `storage.replaceStatusLine`
// nor `parseTaskMeta` is fence-aware (both scan raw lines), which is why this
// command walks `mdfence.OutsideFences` — the same walk the detector does.

// archiveDirs maps the archive subdirectory to the status a task filed there
// must carry. It is the whole of this command's notion of "terminal", kept in
// one place and pinned against the real writer by test.
var archiveDirs = []struct {
	dir    string
	status string
}{
	{"done", storage.StatusRetired},
	{"cancelled", storage.StatusCancelled},
}

var migrateTaskStatusFlags = []cli.FlagDef{
	{Name: "--vault", Arg: "PATH", Help: "Vault root to scan and repair (default: the configured vault_path)"},
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Limit to one project (default: every project in the vault)"},
	{Name: "--apply", Help: "WRITE the repair. Without this the command only reports."},
}

func cmdMigrateTaskStatus() *cli.Command {
	return &cli.Command{
		Name:     "migrate task-status",
		Synopsis: "vp migrate task-status [--vault PATH] [--project P] [--apply]",
		Description: "Make every ARCHIVED task's \"**Status:**\" line agree with the directory it sits " +
			"in: tasks/done/ must say \"retired\", tasks/cancelled/ must say \"cancelled\".\n\n" +
			"PLAN-FIRST: the bare command REPORTS and writes nothing; pass --apply to write.\n\n" +
			"The directory is authoritative and every current reader derives `done` from it, so this " +
			"repairs what a file says about ITSELF. Legacy files archived before the writer stamped " +
			"the line still claim \"In Progress\", \"pending\", or free text such as \"Active — " +
			"execution started ...\"; any consumer keying off status rather than the directory " +
			"inherits that claim.\n\n" +
			"A file with NO \"**Status:**\" line outside code fences is LEFT ALONE and reported only " +
			"as a count. Absence is the older header format, not a false claim — inserting a line " +
			"there would turn a handful of repairs into dozens of writes.\n\n" +
			"Detection and rewriting are fence-aware: a \"**Status:**\" line quoted inside a ``` or " +
			"~~~ block is sample text, and real archived files carry such samples.\n\n" +
			"Scope is ARCHIVED tasks only. Active tasks are never read or written, and a slug that " +
			"exists in BOTH the active and an archive directory is REFUSED rather than repaired, " +
			"because the underlying writer resolves active first and would edit the wrong file.\n\n" +
			"Writes go through the locked, surface-stamping task writer, never the generic vault " +
			"file tools (which refuse task paths). Under --apply the vault git working tree must be " +
			"clean, so `git checkout .` is a guaranteed rollback. A run in which any file failed " +
			"exits non-zero.",
		Flags: migrateTaskStatusFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate task-status", Comment: "Report what would be repaired; writes nothing"},
			{Cmd: "vp migrate task-status -p vibe-palace", Comment: "Report for one project"},
			{Cmd: "vp migrate task-status -p vibe-palace --apply", Comment: "Repair one project"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateTaskStatusFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-status: %v\n", err)
				return cli.ExitUser
			}
			root, err := resolveMigrationVaultRoot(fv.Get("--vault"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-status: %v\n", err)
				return cli.ExitUser
			}
			sum, err := runTaskStatusMigration(root, fv.Get("--project"), fv.Bool("--apply"), os.Stdout)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-status: %v\n", err)
				return cli.ExitSystem
			}
			// 🔴 A run that failed every write must not exit 0. The sibling
			// `migrate task-preamble` prints "  !!" per failure and still
			// returns nil, so a migration that wrote NOTHING reports success —
			// a separate, unfiled finding, deliberately not fixed from here.
			if sum.Failed > 0 {
				fmt.Fprintf(os.Stderr, "vp migrate task-status: %d file(s) failed\n", sum.Failed)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

// taskStatusPlan is one archived file's decision, kept so a test can assert on
// the roll-up without re-parsing the printed report.
type taskStatusPlan struct {
	Project string
	Slug    string
	Dir     string // "done" or "cancelled"
	Found   string // the status the file claims
	Want    string // the status its directory requires
	Applied bool
	Failed  bool
}

type taskStatusSummary struct {
	Scanned  int // archived files read
	OK       int // status already agrees
	NoStatus int // no **Status:** line outside fences — left alone, by design
	Fix      int // disagreeing files
	Applied  int // rewrites that succeeded
	Failed   int // read errors, refused shadows, and failed writes
	Plans    []taskStatusPlan
}

// runTaskStatusMigration is the whole command, injectable for tests.
//
// It walks the archive DIRECTORIES rather than asking vp_list_tasks: the
// listing is built from the active graph, and these files are precisely the
// ones it does not carry.
func runTaskStatusMigration(root, only string, apply bool, out io.Writer) (taskStatusSummary, error) {
	var sum taskStatusSummary

	if apply {
		if err := requireCleanVaultTree(root); err != nil {
			return sum, err
		}
	}

	projects, err := taskPreambleProjects(root, only)
	if err != nil {
		return sum, err
	}

	printVaultRoot(out, root)
	if apply {
		fmt.Fprintln(out, "Mode:  APPLY — archived task files will be rewritten.")
	} else {
		fmt.Fprintln(out, "Mode:  REPORT ONLY — nothing is written. Pass --apply to write.")
	}
	fmt.Fprintln(out)

	vault := storage.NewVault(root)

	for _, slug := range projects {
		for _, ad := range archiveDirs {
			dir := filepath.Join(root, "Projects", slug, "tasks", ad.dir)
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
					fmt.Fprintf(out, "  !!    %s/%s: read: %v\n", slug, taskSlug, ferr)
					sum.Failed++
					continue
				}
				sum.Scanned++

				before := string(data)
				lineNum, found, ok := findStatusLineOutsideFences(before)
				if !ok {
					// Absence is the older header format, not a false claim.
					// Do not insert, do not repair, do not report per-file.
					sum.NoStatus++
					continue
				}
				// 🔴 AGREEMENT IS THE DETECTOR'S QUESTION, NOT STRING EQUALITY.
				// DimTaskStatusDirectory rule 1 flags an archived file only when
				// its status is NOT terminal, so this repair must have exactly
				// that population. Asking `found == ad.status` instead made this
				// tool a second definition: it would rewrite "Retired" (cased),
				// rewrite a done/ file reading "cancelled" into "retired", and
				// disagree on trailing whitespace — in every case editing a file
				// the audit never flagged.
				//
				// A done/ file reading "cancelled" is therefore LEFT ALONE. It
				// agrees with its directory in the only sense the detector
				// recognises, and a repair must not silently rewrite history the
				// audit does not report.
				if storage.IsTerminalStatus(found) {
					sum.OK++
					continue
				}

				sum.Fix++
				plan := taskStatusPlan{
					Project: slug, Slug: taskSlug, Dir: ad.dir,
					Found: found, Want: ad.status,
				}
				fmt.Fprintf(out, "  FIX   %s/%s (%s/) — %q -> %q\n",
					slug, taskSlug, ad.dir, found, ad.status)

				// 🔴 OverwriteTaskFile resolves active -> done -> cancelled and
				// returns the FIRST hit, so a slug that also exists in the
				// active directory would send this write to the wrong file.
				// Refuse rather than repair; a duplicated slug is its own defect.
				if active := filepath.Join(root, "Projects", slug, "tasks", name); fileExists(active) {
					fmt.Fprintf(out, "  !!    %s/%s: also present in tasks/ — refusing, the writer resolves active first\n",
						slug, taskSlug)
					plan.Failed = true
					sum.Failed++
					sum.Plans = append(sum.Plans, plan)
					continue
				}

				if apply {
					after := replaceLine(before, lineNum, "**Status:** "+ad.status)
					if werr := vault.OverwriteTaskFile(slug, taskSlug, after); werr != nil {
						fmt.Fprintf(out, "  !!    %s/%s: write: %v\n", slug, taskSlug, werr)
						plan.Failed = true
						sum.Failed++
						sum.Plans = append(sum.Plans, plan)
						continue
					}
					plan.Applied = true
					sum.Applied++
				}
				sum.Plans = append(sum.Plans, plan)
			}
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Scanned %d archived task file(s): %d already agree, %d to repair, %d with no Status line (left alone).\n",
		sum.Scanned, sum.OK, sum.Fix, sum.NoStatus)
	if apply {
		fmt.Fprintf(out, "Applied %d rewrite(s).\n", sum.Applied)
	} else if sum.Fix > 0 {
		fmt.Fprintln(out, "Nothing was written. Re-run with --apply to write.")
	}
	if sum.Failed > 0 {
		fmt.Fprintf(out, "%d file(s) FAILED.\n", sum.Failed)
	}
	return sum, nil
}

// findStatusLineOutsideFences returns the 1-indexed line number and value of the
// first "**Status:**" line that is NOT inside a code fence.
//
// First-wins matches what the readers and the writer already do; the difference
// here is that a fenced sample cannot win, because it is never a candidate.
func findStatusLineOutsideFences(content string) (line int, value string, ok bool) {
	for _, l := range mdfence.OutsideFences(content) {
		// storage.TaskStatusValue is THE key match — the narrow export of
		// headerFieldValue, which the parser, the writers and the validator all
		// use. A near-copy of it here is exactly how a repair tool and a
		// detector come to disagree about which lines are metadata.
		v, ok := storage.TaskStatusValue(l.Text)
		if !ok {
			continue
		}
		return l.Num, v, true
	}
	return 0, "", false
}

// replaceLine replaces the 1-indexed line n, preserving every other byte —
// including the file's trailing-newline shape, since the split/join round-trips.
func replaceLine(content string, n int, replacement string) string {
	lines := strings.Split(content, "\n")
	if n < 1 || n > len(lines) {
		return content
	}
	lines[n-1] = replacement
	return strings.Join(lines, "\n")
}

// fileExists reports whether path is an existing regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
