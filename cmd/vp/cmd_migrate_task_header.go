// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
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
// The corpus carries five classes (`storage.ScanLegacyHeader`). This command
// REPORTS all five and WRITES two, each through its own repair in
// `internal/storage`, each refusing every class but its own:
//
//   - BOTH — a true un-bolded value sits above a stale bolded one, so carrying it
//     across and dropping the bare line loses nothing.
//   - BARE-ONLY — there is no bolded field to carry anything onto, so the repair
//     CONSTRUCTS the header block. It is not a smaller version of the Both
//     repair, and it is not the deletion an earlier plan proposed for the whole
//     population: deleting the bare line destroys the file's only status and
//     leaves it refused at the validator's "missing Status" arm instead of
//     repaired.
//
// MULTI-TITLE needs a per-file judgment call between two disagreeing headers.
// INVERTED carries a bolded value that is already terminal, so the Both repair
// would overwrite a correct "retired"/"cancelled" with the legacy line —
// manufacturing the very finding `vaultaudit.DimTaskStatusDirectory` rule 1
// exists to report. Both are separate tasks, and the refusals in
// `storage.RepairLegacyBothHeader` / `storage.RepairLegacyBareOnlyHeader` are
// what keep this command inside that split rather than relying on anyone
// remembering it.
//
// # The bare-only repair MANUFACTURES work for a sibling, and says so
//
// None of the legacy bare values is terminal, and the live class sits entirely in
// `done/`. `DimTaskStatusDirectory` skips a file whose status is absent — absence
// is the older format, not a claim — so every one of those files is invisible to
// it today and becomes a rule-1 finding the moment the field exists. That is not
// a defect in the repair; it is the finding surfacing at last. But it means
// `vp migrate task-status` is the mandatory SECOND HALF of the operation, and
// this command's report says so after any run that constructed a header.
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
			"FIVE CLASSES are reported and THREE are written. \"both\" (a true bare line above " +
			"a stale bolded field) is repaired by dropping the bare line and carrying its value " +
			"onto the bolded field, in a SINGLE write — a two-step would leave the file asserting " +
			"only the stale value, and a crash between the steps would make that permanent. " +
			"\"bare-only\" (the bare line is the file's ONLY status, and the file has no bolded " +
			"field at all) is repaired by CONSTRUCTING the header block: the Status value's first " +
			"line becomes **Status:**, a bare \"Priority:\" line or a YAML priority becomes " +
			"**Priority:** (defaulting to \"medium\" when the file states none), and any remaining " +
			"prose from the legacy run is RELOCATED verbatim under a body heading rather than " +
			"flattened onto the field. \"multi-title\" (a modern header prepended above an intact " +
			"legacy document) is repaired by DEMOTING the second title to an H2 and relabelling the " +
			"legacy **Status:**/**Priority:** lines beneath it to **Legacy status:**/**Legacy " +
			"priority:**, which preserves both values instead of choosing between them; a file whose " +
			"later H1s are ordinary section headings, or whose transformed bytes the validator still " +
			"refuses, is listed for SIGN-OFF instead of written. \"inverted\" (a bolded value that is already terminal " +
			"beside a non-terminal bare line) would have a correct status overwritten and is " +
			"reported, never written. \"clean\" files are left alone.\n\n" +
			"🔴 PAIRED COMMAND: a bare-only repair makes files VISIBLE to the " +
			"task-status-directory audit dimension for the first time — none of the legacy values " +
			"is terminal and the class sits in done/, so each repaired file becomes a rule-1 " +
			"finding. Run `vp migrate task-status --apply` immediately after this command's " +
			"--apply, then re-run `vp audit vault` and confirm no net new findings.\n\n" +
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
			{Cmd: "vp migrate task-header -p vibe-palace --apply", Comment: "Repair every repairable class in one project; the rest are listed for sign-off"},
			{Cmd: "vp migrate task-status -p vibe-palace --apply", Comment: "The mandatory second half after a bare-only repair"},
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
	// Reported per class. Both and BareOnly are the two this command can write;
	// MultiTitle and Inverted are counted so the report sizes the work it is NOT
	// doing.
	Both       int
	BareOnly   int
	MultiTitle int
	Inverted   int
	Applied    int
	// AppliedBareOnly counts the constructed headers specifically, because they
	// are the writes that make files newly visible to DimTaskStatusDirectory and
	// so the ones that oblige the operator to run the paired command.
	AppliedBareOnly int
	// AppliedMultiTitle counts the demoted second titles. Tracked apart from
	// AppliedBareOnly because the two have OPPOSITE audit consequences: a
	// constructed header manufactures task-status-directory findings, a demoted
	// title manufactures none (the modern Status stays first, so every reader's
	// answer is unchanged). Only one of them obliges the paired command.
	AppliedMultiTitle int
	// SignOff are the files this command REFUSES to write and hands to a human,
	// with the detail needed to decide without opening them.
	SignOff []taskHeaderSignOff
	// AppliedPaths are the vault-relative paths this run WROTE, in write order.
	// They are what the operator has to commit before the paired command will
	// run, and printing them exactly is the difference between an instruction
	// that works and one that names a directory holding other people's dirt.
	AppliedPaths []string
	// PrioritySources tallies where each constructed **Priority:** came from.
	// The supplied-default count is the one genuinely fabricated value in the
	// whole operation, and an operator should not have to grep a 480-line report
	// to learn how many there were.
	PrioritySources map[storage.LegacyPrioritySource]int
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
	sum := taskHeaderSummary{PrioritySources: map[storage.LegacyPrioritySource]int{}}

	projects, err := taskPreambleProjects(root, only)
	if err != nil {
		return sum, err
	}

	printVaultRoot(out, root)
	if apply {
		fmt.Fprintln(out, "Mode:  APPLY — repairable \"both\", \"bare-only\" and \"multi-title\" files will be "+
			"rewritten; every other file is reported only.")
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

					// Planned BEFORE the apply branch so the report describes the
					// same construction the write performs — including the
					// priority SOURCE, because "medium" that was read from the
					// file and "medium" that was supplied for it are different
					// facts and only one of them is an operator decision.
					fix, rerr := storage.RepairLegacyBareOnlyHeader(before)
					if rerr != nil {
						fmt.Fprintf(out, "  !!    %s: %v\n", taskHeaderWhere(project, sub, taskSlug), rerr)
						plan.Failed = true
						sum.Failed++
						sum.Plans = append(sum.Plans, plan)
						continue
					}
					sum.PrioritySources[fix.PrioritySource]++
					fmt.Fprintf(out, "  FIX   %s\n        bare-only: construct the header block — "+
						"**Status:** %q, **Priority:** %q (%s), relocating the %d-line legacy run into the body\n",
						taskHeaderWhere(project, sub, taskSlug), fix.Status, fix.Priority,
						fix.PrioritySource, fix.Relocated)

					if !apply {
						sum.Plans = append(sum.Plans, plan)
						continue
					}
					if taskHeaderShadowed(out, root, project, sub, taskSlug, name) {
						plan.Failed = true
						sum.Failed++
						sum.Plans = append(sum.Plans, plan)
						continue
					}
					if werr := vault.OverwriteTaskFile(project, taskSlug, fix.Content); werr != nil {
						fmt.Fprintf(out, "  !!    %s: write: %v\n", taskHeaderWhere(project, sub, taskSlug), werr)
						plan.Failed = true
						sum.Failed++
						sum.Plans = append(sum.Plans, plan)
						continue
					}
					plan.Applied = true
					sum.Applied++
					sum.AppliedBareOnly++
					taskHeaderRecordWrite(&sum, root, project, sub, name)
					sum.Plans = append(sum.Plans, plan)
					continue
				case storage.LegacyHeaderMultiTitle:
					sum.MultiTitle++

					// Planned before the apply branch, like the bare-only case, so
					// the report describes the same transform the write performs.
					//
					// 🔴 A REFUSAL HERE IS A REPORTED ROW, NEVER A SILENCE. The
					// classifier returns `clean` for every one of these files
					// after the transform, including the ones the validator still
					// refuses — so keying anything off the classifier would drop
					// a file that needs a human out of the only report that names
					// it. The verdict is the repair's, which is the validator's.
					mfix, merr := storage.RepairLegacyMultiTitleHeader(before)
					if merr != nil {
						sum.SignOff = append(sum.SignOff, taskHeaderSignOff{
							Where:      taskHeaderWhere(project, sub, taskSlug),
							Kind:       mfix.Refusal,
							Reason:     merr.Error(),
							Titles:     mfix.Titles,
							FieldLines: mfix.FieldLines,
						})
						fmt.Fprintf(out, "  HUMAN %s\n        multi-title: %v\n",
							taskHeaderWhere(project, sub, taskSlug), merr)
						sum.Plans = append(sum.Plans, plan)
						continue
					}
					fmt.Fprintf(out, "  FIX   %s\n        multi-title: demote the second title to %q%s\n",
						taskHeaderWhere(project, sub, taskSlug), mfix.DemotedTitle,
						taskHeaderRelabelNote(mfix))

					if !apply {
						sum.Plans = append(sum.Plans, plan)
						continue
					}
					if taskHeaderShadowed(out, root, project, sub, taskSlug, name) {
						plan.Failed = true
						sum.Failed++
						sum.Plans = append(sum.Plans, plan)
						continue
					}
					if werr := vault.OverwriteTaskFile(project, taskSlug, mfix.Content); werr != nil {
						fmt.Fprintf(out, "  !!    %s: write: %v\n", taskHeaderWhere(project, sub, taskSlug), werr)
						plan.Failed = true
						sum.Failed++
						sum.Plans = append(sum.Plans, plan)
						continue
					}
					plan.Applied = true
					sum.Applied++
					sum.AppliedMultiTitle++
					taskHeaderRecordWrite(&sum, root, project, sub, name)
					sum.Plans = append(sum.Plans, plan)
					continue
				case storage.LegacyHeaderInverted:
					// Printed with BOTH values because the whole point of the
					// class is that a human has to decide which one is true.
					sum.Inverted++
					fmt.Fprintf(out, "  SKIP  %s\n        inverted: bolded %q is already terminal and bare %q is not; "+
						"repairing would overwrite the terminal status — separate task.\n",
						taskHeaderWhere(project, sub, taskSlug), scan.BoldValue, scan.BareValue)
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

				if taskHeaderShadowed(out, root, project, sub, taskSlug, name) {
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
				taskHeaderRecordWrite(&sum, root, project, sub, name)
				sum.Plans = append(sum.Plans, plan)
			}
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Scanned %d file(s): %d clean, %d both, %d bare-only, %d multi-title, %d inverted.\n",
		sum.Scanned, sum.Clean, sum.Both, sum.BareOnly, sum.MultiTitle, sum.Inverted)
	if apply {
		fmt.Fprintf(out, "Applied %d rewrite(s).\n", sum.Applied)
	} else if sum.Both > 0 || sum.BareOnly > 0 || sum.MultiTitle > len(sum.SignOff) {
		fmt.Fprintln(out, "Re-run with --apply to write the \"both\", \"bare-only\" and repairable "+
			"\"multi-title\" files.")
	}
	if sum.Inverted > 0 {
		fmt.Fprintln(out, "inverted is reported by design: a bolded value that is already terminal cannot "+
			"be adjudicated mechanically, and it is filed separately. This command will never write it.")
	}
	taskHeaderPrioritySourceTally(out, sum)
	taskHeaderSignOffSection(out, sum)
	taskHeaderPairingBanner(out, apply, sum)
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

// taskHeaderShadowed is the shadow guard, shared by both repairs.
//
// 🔴 Vault.resolveTaskFile searches active FIRST, so overwriting an ARCHIVED
// slug that also exists under tasks/ would silently rewrite the active file
// instead. Refuse rather than guess which one the operator meant.
func taskHeaderShadowed(out io.Writer, root, project, sub, slug, name string) bool {
	if sub == "" || !fileExists(filepath.Join(root, "Projects", project, "tasks", name)) {
		return false
	}
	fmt.Fprintf(out, "  !!    %s: an ACTIVE task of the same slug exists; refusing (the writer resolves active first)\n",
		taskHeaderWhere(project, sub, slug))
	return true
}

// taskHeaderRelPath renders a task file's vault-relative path — what
// `vp vault commit --paths` takes, and what the pairing banner has to print.
func taskHeaderRelPath(project, sub, name string) string {
	if sub == "" {
		return "Projects/" + project + "/tasks/" + name
	}
	return "Projects/" + project + "/tasks/" + sub + "/" + name
}

// taskHeaderRecordWrite appends the paths one write dirtied: the task file, and
// the .surface stamp the writer touches alongside it.
//
// 🔴 THE STAMP IS NOT OPTIONAL AND ITS OMISSION IS INVISIBLE UNTIL THE OPERATOR
// IS STUCK. Projects/<p>/.surface is TRACKED in the vault, every schema-bearing
// write restamps it, and the paired command gates on a whole-vault clean tree —
// so a commit instruction naming only the task files leaves the stamp dirty and
// `migrate task-status` still exits 2. The first version of this banner did
// exactly that; the test that drives the PRINTED paths through
// storage.CommitAndPushPaths is what caught it, and a test that had cleaned the
// tree with `git add -A` would not have.
//
// The stamp path comes from surface.StampPath rather than a joined literal, so
// this does not own a second copy of the stamp filename.
func taskHeaderRecordWrite(sum *taskHeaderSummary, root, project, sub, name string) {
	sum.AppliedPaths = append(sum.AppliedPaths, taskHeaderRelPath(project, sub, name))

	stamp, err := surface.StampPath(root, filepath.Join(root, "Projects", project, "tasks", sub, name))
	if err != nil || stamp == "" {
		return
	}
	if !slices.Contains(sum.AppliedPaths, stamp) {
		sum.AppliedPaths = append(sum.AppliedPaths, stamp)
	}
}

// taskHeaderPrioritySourceTally prints where the constructed priorities came
// from, in aggregate.
//
// The supplied-default count is the reason this exists. It is the one value in
// the whole operation that is INVENTED rather than read, the operator authorized
// it as a policy rather than file by file, and leaving it visible only per-file
// means learning "half the class got a fabricated priority" requires grepping a
// several-hundred-line report.
func taskHeaderPrioritySourceTally(out io.Writer, sum taskHeaderSummary) {
	if sum.BareOnly == 0 {
		return
	}
	fmt.Fprintf(out, "Priority sources: %d from the legacy run, %d from YAML frontmatter, %d SUPPLIED DEFAULT (%q).\n",
		sum.PrioritySources[storage.PriorityFromRun],
		sum.PrioritySources[storage.PriorityFromFrontmatter],
		sum.PrioritySources[storage.PriorityFromDefault],
		storage.LegacyPriorityDefault)
}

// taskHeaderPairingBanner prints the mandatory second half of the operation.
//
// 🔴 IT PRINTS THE COMMIT STEP, AND THAT STEP IS NOT OPTIONAL PADDING. The writes
// this command just made are what dirty the vault, and `vp migrate task-status`
// gates on a WHOLE-VAULT clean tree (requireCleanVaultTree -> GitStatusClean), so
// it exits 2 on exactly the state this command guarantees. An earlier version of
// this banner said "RUN NEXT: vp migrate task-status --apply" and an operator
// following it literally would see that exit 2 and reasonably read it as the
// repair having failed.
//
// The asymmetry is deliberate on both sides and worth naming: THIS command
// documents why it has no clean-tree precondition (a live agent session
// re-dirties the vault within seconds), and it now hands off to a sibling that
// has one. Scoping that sibling's gate to the paths it writes is the real fix and
// is a filed open item; it is not this change's to make. What this change owes
// the operator is an instruction that runs.
//
// `vp vault tidy` is NOT the commit step and must not be suggested: its sweep
// rules cover session summaries, transcripts, KG files, drawers, audits and
// .surface stamps — no Projects/*/tasks/** pattern — so it would REPORT these
// files as dirt and commit none of them. The paths are printed exactly, rather
// than as a directory, because a directory would also stage whatever else is
// dirty under it, which is the "never git add -A" rule one level up.
func taskHeaderPairingBanner(out io.Writer, apply bool, sum taskHeaderSummary) {
	if len(sum.AppliedPaths) == 0 {
		if !apply && sum.BareOnly > 0 {
			fmt.Fprintf(out, "%d bare-only file(s) would become task-status-directory findings on --apply; "+
				"vp migrate task-status --apply is the mandatory second half, and it needs a clean vault "+
				"tree — see the banner this prints after an --apply.\n", sum.BareOnly)
		}
		return
	}
	// The two writable classes have OPPOSITE audit consequences, so the banner
	// asks for different things. A constructed bare-only header makes a file
	// visible to task-status-directory for the first time and OBLIGES the paired
	// migration; a demoted title changes no reader's answer and obliges nothing
	// beyond the commit. Printing the task-status step after a multi-title-only
	// run would send an operator to a command with nothing to do.
	if sum.AppliedBareOnly > 0 {
		fmt.Fprintf(out, "\n🔴 %d constructed header(s) are now VISIBLE to the task-status-directory audit "+
			"dimension, and none of the legacy values is terminal. This run also dirtied the vault, and the "+
			"paired command refuses a dirty tree — so RUN ALL THREE, IN ORDER:\n\n", sum.AppliedBareOnly)
	} else {
		fmt.Fprintf(out, "\n%d file(s) were rewritten, which dirtied the vault. COMMIT THEM:\n\n",
			len(sum.AppliedPaths))
	}
	// 🔴 THE --paths VALUE IS QUOTED, AND THAT IS NOT COSMETIC. The list is printed
	// over backslash-continued lines to stay readable; a shell removes the
	// backslash-newline but KEEPS the indentation that follows, so an unquoted
	// value word-splits and `--paths` receives only the text up to the first
	// space. Pasted unquoted, this command commits ONE file and reports success.
	// Measured: it committed 1 of 68 paths and the paired command still exited 2.
	// Inside quotes the spaces survive into a single argument, and the flag
	// parser trims each comma-separated entry.
	step := "  1. "
	if sum.AppliedBareOnly == 0 {
		step = "  "
	}
	fmt.Fprintf(out, "%svp vault commit --message %q \\\n       --paths \"%s\"\n",
		step, "migrate task-header: normalize legacy task headers",
		strings.Join(sum.AppliedPaths, ",\\\n                "))
	if sum.AppliedBareOnly == 0 {
		return
	}
	fmt.Fprintln(out, "  2. vp migrate task-status --apply")
	fmt.Fprintln(out, "  3. vp audit vault          # expect no net new findings")
	// The honest limit of this instruction. Step 2 gates on the WHOLE vault being
	// clean, and this command can only name what IT wrote — anything another
	// session left dirty has to be dealt with too, which is the standing open
	// item about that precondition being unsatisfiable in a live session.
	fmt.Fprintln(out, "\nStep 2 requires the WHOLE vault tree to be clean, and step 1 commits only what "+
		"this run wrote.\nCommit or stash anything else dirty first (vp vault status) or step 2 will "+
		"exit 2 on someone else's work.")
}

// taskHeaderSignOff is one file this command will not write, plus the evidence an
// operator needs to decide what to do with it.
type taskHeaderSignOff struct {
	Where string
	// Kind is WHY, and it is carried rather than inferred from the message: a
	// shape refusal and a validator refusal are different claims about the file,
	// and only one of them is the design's "the validator decides" contract.
	Kind       storage.LegacyRefusalKind
	Reason     string
	Titles     []storage.LegacyH1
	FieldLines []storage.LegacyH1
}

// taskHeaderRelabelNote renders the field-relabel half of a multi-title plan line.
func taskHeaderRelabelNote(fix storage.LegacyMultiTitleRepair) string {
	n := fix.RelabelledStatus + fix.RelabelledPriority
	if n == 0 {
		return " (its legacy block owns no field lines)"
	}
	return fmt.Sprintf(" and relabel %d legacy field line(s) beneath it", n)
}

// taskHeaderSignOffSection prints the files that need a human.
//
// 🔴 THIS SECTION EXISTS BECAUSE THE CLASSIFIER CANNOT SEE ANY OF IT. It counts
// titles, so a repair keyed on it going `clean` would write what it could, report
// nothing remaining, and leave these files unnamed — no tool able to write them
// and no tool mentioning them.
//
// 🔴 IT REPORTS TWO DIFFERENT REASONS AND MUST NOT BLUR THEM. The design's claim
// is "the write decision is the VALIDATOR's verdict, per file", and that claim is
// true of a row refused AFTER the transform ran. It is FALSE of a row refused on
// SHAPE: that decision is made from the file's own structure before any transform
// exists, so the validator never saw those bytes and there was nothing for it to
// refuse. An earlier version of this header asserted the validator reason for
// every row; it was wrong for half of them.
//
// The detail is chosen so the decision can be made from the report alone: the
// reason in the refusing check's own words, every unfenced H1 with its line
// number (which is what distinguishes "two documents" from "H1 used as section
// structure"), and every **Status:**/**Priority:** line with its line number
// (which is what makes a non-contiguous header block visible without opening the
// file).
func taskHeaderSignOffSection(out io.Writer, sum taskHeaderSummary) {
	if len(sum.SignOff) == 0 {
		return
	}
	fmt.Fprintf(out, "\n🔴 SIGN-OFF REQUIRED — %d file(s) this command refuses to write, "+
		"for TWO different reasons:\n"+
		"    shape     — not a modern header prepended above one legacy document. Decided from the "+
		"file's own structure,\n                  before any transform ran, so the validator never saw "+
		"these bytes.\n"+
		"    validator — the transform ran and validateWholeTaskFile refused its output, so the defect "+
		"is not the two titles.\n"+
		"Neither is visible to the classifier, which counts titles; a repair keyed on it would report "+
		"nothing remaining.\n", len(sum.SignOff))
	for _, so := range sum.SignOff {
		fmt.Fprintf(out, "\n  %s  [refused on %s]\n      why: %s\n", so.Where, so.Kind, so.Reason)
		for _, t := range so.Titles {
			fmt.Fprintf(out, "      H1  line %-5d %s\n", t.Line, t.Text)
		}
		for _, f := range so.FieldLines {
			fmt.Fprintf(out, "      fld line %-5d %s\n", f.Line, f.Text)
		}
	}
}
