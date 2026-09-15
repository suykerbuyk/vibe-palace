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

// The one-time pass ADR-011 Decision 1's pre-ship corpus check requires
// (doc/adr/011-open-task-header-schema-and-format-axis.md), and the permanent
// remediation for a defect class that can resurface: a non-blank,
// bold-field-shaped line sitting directly under a task file's header, with no
// blank-line separator, that the CLOSED four-field header parser correctly
// excluded as body prose but the OPEN header schema (storage.isHeaderFieldLine,
// generalized alongside this command) now absorbs as an unrecognized field.
//
// # Why this is a permanent command, not a throwaway script
//
// This project ships exactly this class of problem — "a defect exists once,
// fix it everywhere" — as a permanent CLI command: `vp migrate task-status`,
// `vp migrate task-header`, `vp migrate task-preamble` are the direct
// precedent, all still present after every one of their filing tasks closed.
// A future project onboarded into the vault, a manually edited archived file,
// or content imported from elsewhere could reintroduce the exact
// no-blank-line shape, and a permanent, idempotent command means the SAME
// verified detection logic is available the next time, at zero rebuild cost.
//
// # Detection reuses the real parser; it does not reimplement it
//
// storage.FindHeaderSpacingHazard is built directly from the same
// isStatusLine/isPriorityLine/isParentLine/isDependsLine and headerFieldName
// primitives the shipped open-schema parser uses — composition of existing,
// tested code, not a parallel implementation in another language.
//
// # Verification is honest about what it proves
//
// applyHeaderSpacingFix checks the reported (line, text) against the actual
// file content before touching anything, and refuses rather than guesses on
// any disagreement. It catches drift between two independently-obtained
// values — a stale report, a caller-side plumbing bug, the file changing
// underneath the run. It does NOT prove storage.FindHeaderSpacingHazard's own
// line-number computation is correct: a self-consistent bug in that function
// would report a wrong line and a text read from that same wrong line, and
// this check would pass vacuously. The real defense against that is
// storage.FindHeaderSpacingHazard's own hand-verified unit tests, plus this
// command's --apply-then-inspect-the-git-diff operational discipline.
//
// # Plan-first, like every sibling
//
// The bare command REPORTS and mutates nothing; --apply writes. --apply
// re-derives every hit FRESH in the same invocation it writes with — it never
// consumes a separately-printed --report list — so there is no persisted
// list for the corpus to drift out from under between review and write.
var migrateTaskHeaderSpacingFlags = []cli.FlagDef{
	{Name: "--vault", Arg: "PATH", Help: "Vault root to scan and repair (default: the configured vault_path)"},
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Limit to one project (default: every project in the vault)"},
	{Name: "--apply", Help: "WRITE the repair. Without this the command only reports."},
}

func cmdMigrateTaskHeaderSpacing() *cli.Command {
	return &cli.Command{
		Name:     "migrate task-header-spacing",
		Synopsis: "vp migrate task-header-spacing [--vault PATH] [--project P] [--apply]",
		Description: "Insert a blank line before any task-header line that the open header schema " +
			"would otherwise silently absorb as an unrecognized field: a bold-field-shaped line " +
			"sitting directly under Status/Priority/Parent/Depends with no blank-line separator.\n\n" +
			"PLAN-FIRST: the bare command REPORTS every hazard (file, line, exact text) and writes " +
			"nothing; pass --apply to write. --apply re-scans fresh in the same run rather than " +
			"replaying an earlier --report, so there is no window for the corpus to change between " +
			"review and write.\n\n" +
			"Detection reuses the real header parser (storage.FindHeaderSpacingHazard) — never a " +
			"reimplementation. Each write independently verifies the reported line against the file " +
			"before touching it and refuses rather than guesses on any disagreement; a file with no " +
			"hazard is left byte-for-byte untouched.\n\n" +
			"Scope is every project in the vault by default, matching every other `vp migrate` " +
			"command — this repair is vault-schema-shaped, not project-scoped. Writes go through the " +
			"migration-only OverwriteTaskFileRewritingHeader escape hatch, so ARCHIVED (done/, " +
			"cancelled/) files are reached without tripping the normal overwrite-refused-on-archived " +
			"rule. A run in which any file failed exits non-zero.",
		Flags: migrateTaskHeaderSpacingFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate task-header-spacing", Comment: "Report every hazard across every project; writes nothing"},
			{Cmd: "vp migrate task-header-spacing -p vibe-palace", Comment: "Report for one project"},
			{Cmd: "vp migrate task-header-spacing -p vibe-palace --apply", Comment: "Repair one project"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateTaskHeaderSpacingFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-header-spacing: %v\n", err)
				return cli.ExitUser
			}
			root, err := resolveMigrationVaultRoot(fv.Get("--vault"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-header-spacing: %v\n", err)
				return cli.ExitUser
			}
			sum, err := runTaskHeaderSpacingMigration(root, fv.Get("--project"), fv.Bool("--apply"), os.Stdout)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-header-spacing: %v\n", err)
				return cli.ExitSystem
			}
			if sum.Failed > 0 {
				fmt.Fprintf(os.Stderr, "vp migrate task-header-spacing: %d file(s) failed\n", sum.Failed)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

// taskHeaderSpacingPlan is one file's decision, kept so a test can assert on
// the roll-up without re-parsing the printed report.
type taskHeaderSpacingPlan struct {
	Project string
	Slug    string
	Dir     string // "" (active), "done", or "cancelled"
	Line    int
	Text    string
	Applied bool
	Failed  bool
}

type taskHeaderSpacingSummary struct {
	Scanned int // task files read, across every project/directory
	Clean   int // no hazard found
	Fix     int // hazard found
	Applied int // rewrites that succeeded
	Failed  int // read errors and failed writes/verifications
	Plans   []taskHeaderSpacingPlan
}

// taskHeaderSpacingDirs are the three per-project locations every task file
// can live in — the same set `cmd_migrate_task_status.go`'s archiveDirs walks
// for the archive half, extended here to the active directory too, since this
// hazard is not archive-specific.
var taskHeaderSpacingDirs = []string{"", "done", "cancelled"}

// runTaskHeaderSpacingMigration is the whole command, injectable for tests.
func runTaskHeaderSpacingMigration(root, only string, apply bool, out io.Writer) (taskHeaderSpacingSummary, error) {
	var sum taskHeaderSpacingSummary

	if apply {
		if err := requireVaultGitRepo(root, "this command rewrites the file in place to insert the missing blank "+
			"line, and git is what lets you inspect the exact diff — and revert it — before committing the result"); err != nil {
			return sum, err
		}
	}

	projects, err := taskPreambleProjects(root, only)
	if err != nil {
		return sum, err
	}

	printVaultRoot(out, root)
	if apply {
		fmt.Fprintln(out, "Mode:  APPLY — hazard task files will be rewritten.")
	} else {
		fmt.Fprintln(out, "Mode:  REPORT ONLY — nothing is written. Pass --apply to write.")
	}
	fmt.Fprintln(out)

	vault := storage.NewVault(root)

	for _, slug := range projects {
		for _, sub := range taskHeaderSpacingDirs {
			dir := filepath.Join(root, "Projects", slug, "tasks", sub)
			entries, rerr := os.ReadDir(dir)
			if rerr != nil {
				// A project with no done/ or cancelled/ (or, in principle, no
				// active tasks/ at all) is normal, not a defect.
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
				line, text, ok := storage.FindHeaderSpacingHazard(before)
				if !ok {
					sum.Clean++
					continue
				}

				sum.Fix++
				plan := taskHeaderSpacingPlan{Project: slug, Slug: taskSlug, Dir: sub, Line: line, Text: text}
				dirLabel := sub
				if dirLabel == "" {
					dirLabel = "tasks"
				} else {
					dirLabel = "tasks/" + dirLabel
				}
				fmt.Fprintf(out, "  FIX   %s/%s (%s/) line %d: %q\n", slug, taskSlug, dirLabel, line, text)

				if apply {
					after, aerr := applyHeaderSpacingFix(before, line, text)
					if aerr != nil {
						fmt.Fprintf(out, "  !!    %s/%s: %v\n", slug, taskSlug, aerr)
						plan.Failed = true
						sum.Failed++
						sum.Plans = append(sum.Plans, plan)
						continue
					}
					if werr := vault.OverwriteTaskFileRewritingHeader(slug, taskSlug, after); werr != nil {
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
	fmt.Fprintf(out, "Scanned %d task file(s): %d clean, %d with a header-spacing hazard.\n",
		sum.Scanned, sum.Clean, sum.Fix)
	if apply {
		fmt.Fprintf(out, "Applied %d fix(es).\n", sum.Applied)
	} else if sum.Fix > 0 {
		fmt.Fprintln(out, "Nothing was written. Re-run with --apply to write.")
	}
	if sum.Failed > 0 {
		fmt.Fprintf(out, "%d file(s) FAILED.\n", sum.Failed)
	}
	return sum, nil
}

// applyHeaderSpacingFix inserts a blank line before the reported hazard line,
// but ONLY after checking the report against the actual content — it never
// blindly trusts (line, text) as an opaque pair. Refuses (returns an error,
// writes nothing) if:
//   - line is out of range for content's line count;
//   - the line AT that position does not equal text byte-for-byte;
//   - the line at that position is itself blank (a hazard can never BE a
//     blank line);
//   - the line immediately BEFORE that position is already blank (there is
//     no missing separator to fix here).
//
// WHAT THIS DOES AND DOES NOT PROVE, stated precisely: these four checks
// catch DRIFT between two independently-obtained values — a stale (line,
// text) pair, a caller-side plumbing bug, or the file changing between
// detection and write. They do NOT catch a bug INSIDE
// storage.FindHeaderSpacingHazard's own line-number computation: if that
// function's index math is off by one, the text it returns is read from that
// SAME wrong index, so content[line] == text holds trivially and every check
// here passes vacuously — there is no independent second read to disagree
// with it. Real protection against THAT failure mode is
// storage.FindHeaderSpacingHazard's own hand-verified unit tests plus the
// mandatory apply-then-git-diff-inspection step in this command's
// operational use against the real vault. This function is a plumbing/
// staleness guard, not a substitute for either.
//
// On the success path, additionally asserts a write-pipeline sanity check
// (not part of the index verification above): the repaired line count is
// exactly len(original)+1, the inserted line is blank, and every other line
// is byte-identical to the original at its corresponding shifted position.
func applyHeaderSpacingFix(content string, line int, text string) (string, error) {
	lines := strings.Split(content, "\n")
	idx := line - 1
	if idx < 0 || idx >= len(lines) {
		return "", fmt.Errorf("reported line %d is out of range for a %d-line file", line, len(lines))
	}
	if lines[idx] != text {
		return "", fmt.Errorf("line %d does not match the reported text — refusing rather than guessing\n"+
			"  reported: %q\n  actual:   %q", line, text, lines[idx])
	}
	if strings.TrimSpace(lines[idx]) == "" {
		return "", fmt.Errorf("line %d is blank — a hazard can never be a blank line, this is a detector bug", line)
	}
	if idx == 0 || strings.TrimSpace(lines[idx-1]) == "" {
		return "", fmt.Errorf("line %d already has a blank line immediately before it — there is no missing separator to fix here", line)
	}

	repaired := make([]string, 0, len(lines)+1)
	repaired = append(repaired, lines[:idx]...)
	repaired = append(repaired, "")
	repaired = append(repaired, lines[idx:]...)

	// Write-pipeline sanity check — NOT an index-correctness proof, see the doc
	// comment above.
	if len(repaired) != len(lines)+1 {
		return "", fmt.Errorf("internal error: repaired line count %d, want %d", len(repaired), len(lines)+1)
	}
	if repaired[idx] != "" {
		return "", fmt.Errorf("internal error: inserted line is not blank")
	}
	for i := 0; i < idx; i++ {
		if repaired[i] != lines[i] {
			return "", fmt.Errorf("internal error: line %d changed ahead of the insertion point", i+1)
		}
	}
	for i := idx; i < len(lines); i++ {
		if repaired[i+1] != lines[i] {
			return "", fmt.Errorf("internal error: line %d changed after the insertion point", i+1)
		}
	}

	return strings.Join(repaired, "\n"), nil
}
