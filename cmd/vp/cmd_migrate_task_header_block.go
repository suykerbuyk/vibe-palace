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
	"github.com/suykerbuyk/vibe-palace/internal/mdfence"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// `vp migrate task-header-block` CONSTRUCTS the header field run of an ARCHIVED
// task file that predates the header format and carries none at all. Report by
// default; writes only under --apply.
//
// # CONSTRUCTION, NOT REPAIR, AND THE DISTINCTION IS THE WHOLE UNIT
//
// The population is the validator's `missing Status` class: files whose first
// failure is rule 4 and which therefore already satisfy rules 1-3 — balanced
// fences, exactly one H1, at least one H2. They carry ZERO **Status:** lines and
// ZERO **Priority:** lines, so promoting a value is not available: there is no
// value to promote. Both fields are built.
//
// The Status value is DERIVED from the directory the file sits in, which is the
// operator's ruling — trust the directory over anything the body claims. The
// Priority value is a FABRICATION: a uniform storage.LegacyPriorityDefault across
// every file, operator-approved, derived from nothing, and NOT recoverable by
// inspection afterwards. Nothing in the written bytes marks it. Its provenance
// lives in the unit's vault task and in the commit message, once, for the whole
// set — see taskHeaderBlockConstructedRun.
//
// # THE NAME IS THE RULE, NOT THE POPULATION
//
// `task-header-block` names the validator rule it repairs (a missing contiguous
// header-field run after the title), so a second detector of the same rule can
// join later as another class here. The runner-up, `task-preformat-header`, named
// the population instead, which B1 argued against for exactly that reason.
//
// # ARCHIVED ONLY
//
// The walk covers Projects/*/tasks/{done,cancelled} and never the active
// directory, for the same disjointness reason task-sections records: an active
// file with no header block is a different surface's problem, and two commands
// writing the same byte is what that fence prevents. Adding "" to the directory
// list reintroduces the collision — and it would additionally break the Status
// derivation, which has no answer for a file that is not archived.
//
// # WHY THE PERMISSIVE SEAM, AND WHY THAT IS NOT THE ARCHIVED-REACH ARGUMENT
//
// Writes go through storage.Vault.OverwriteTaskFileRewritingHeader. The strict
// wrapper cannot express this transform: creating a Status field where none
// existed moves meta.Status from "" to a value, and refuseHeaderChange refuses
// that outright. It is NOT because the permissive wrapper is needed to reach
// archived files — both wrappers resolve through the same active-first resolver,
// the archived refusal lives at the CLI and MCP layers, and task-header already
// ships a strict-seam write into done/. That claim is false and is recorded here
// because it is in shipped help text elsewhere and has misled one plan already.
//
// # THE POST-CONDITION IS THE SAFETY PROPERTY
//
// A file is written only when storage.ValidateWholeTaskFile accepts the
// TRANSFORMED bytes. The shape checks below exist to give a readable reason, not
// to be the guarantee: a shape this file fails to anticipate still cannot be
// written. Note the seam restamps ModTime AFTER validating, so the bytes
// validated are not the bytes written — every output assertion here is about the
// pre-restamp bytes, and the on-disk run is three lines, not two.

var migrateTaskHeaderBlockFlags = []cli.FlagDef{
	{Name: "--vault", Arg: "PATH", Help: "Vault root to scan and repair (default: the configured vault_path)"},
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Limit to one project (default: every project in the vault)"},
	{Name: "--apply", Help: "WRITE the constructed header blocks. Without this the command only reports."},
}

// taskHeaderBlockDirs is the ARCHIVED scope. See the scope fence above; this is
// also what makes the directory-derived Status total.
var taskHeaderBlockDirs = []string{"done", "cancelled"}

// taskHeaderBlockStatusFor maps an archived directory to the Status value its
// files get. The mapping is the operator's ruling in one place rather than a
// literal at the insertion site, so a third archived directory would fail to
// compile here instead of silently constructing the wrong status.
func taskHeaderBlockStatusFor(sub string) (string, bool) {
	switch sub {
	case "done":
		return storage.StatusDone, true
	case "cancelled":
		return storage.StatusCancelled, true
	}
	return "", false
}

// taskHeaderBlockOutcome classifies one file's decision.
type taskHeaderBlockOutcome int

const (
	// blockNoWork: the file needs nothing from this command — it already
	// validates, or it is malformed for a reason that is not ours.
	blockNoWork taskHeaderBlockOutcome = iota
	// blockConstruct: no header field run at all, and the constructed result
	// validates. The only outcome that writes.
	blockConstruct
	// blockRefused: a shape this command must not reason about.
	blockRefused
)

// taskHeaderBlockDecision is one file's decision, kept so a test can assert the
// roll-up without re-parsing the printed report.
type taskHeaderBlockDecision struct {
	Project string
	Sub     string
	Slug    string
	Outcome taskHeaderBlockOutcome
	Reason  string // refusal/other-defect detail; empty for a clean construction
	Status  string // the directory-derived status that would be written
	After   string // transformed content, only for blockConstruct
	Applied bool
	Failed  bool
	// Skipped marks a file left alone because it carries uncommitted changes.
	// Deliberately NOT Failed: nothing went wrong, and one commit makes the next
	// run repair it.
	Skipped bool
}

type taskHeaderBlockSummary struct {
	Scanned int
	NoWork  int
	Fix     int
	Refused int
	// OtherDefect counts files that are malformed but NOT this command's defect.
	// They are printed rather than swallowed: the only roster an operator sees
	// is the one this command emits, and a silent "needs nothing" over a broken
	// file is how a refusal elsewhere ends up citing a cause that blocks nothing.
	OtherDefect  int
	Dirty        int
	Applied      int
	Failed       int
	Decisions    []taskHeaderBlockDecision
	AppliedPaths []string
}

func cmdMigrateTaskHeaderBlock() *cli.Command {
	return &cli.Command{
		Name:     "migrate task-header-block",
		Synopsis: "vp migrate task-header-block [--vault PATH] [--project P] [--apply]",
		Description: "CONSTRUCT the header field run of an ARCHIVED task file that predates the " +
			"header format and carries none — no \"**Status:**\" line and no \"**Priority:**\" line " +
			"anywhere outside code fences.\n\n" +
			"PLAN-FIRST: the bare command REPORTS and writes nothing; pass --apply to write.\n\n" +
			"SCOPE IS ARCHIVED ONLY — Projects/*/tasks/{done,cancelled}, never the active " +
			"directory. The Status value is DERIVED FROM THE DIRECTORY (done/ -> done, " +
			"cancelled/ -> cancelled), which is why the active directory has no answer here.\n\n" +
			"🔴 THE PRIORITY VALUE IS A FABRICATION. Every constructed file gets the same " +
			"operator-approved default, derived from nothing in the file, and NOTHING in the " +
			"written bytes distinguishes it afterwards from a priority someone chose. The " +
			"provenance belongs in the commit message.\n\n" +
			"Both fields are constructed together: inserting Status alone leaves the file invalid " +
			"at the very next validator arm. A file that already carries either field, or that " +
			"already has a header field run under its title, is REPORTED rather than touched.\n\n" +
			"Writes go through the header-rewriting locked task writer, because creating a field " +
			"where none existed is precisely the transition the strict writer refuses. A file is " +
			"written only if the transformed bytes PASS the whole-file validator. Files with " +
			"uncommitted changes are skipped — git holds the only copy. A run in which any file " +
			"failed exits non-zero; a run with nothing to migrate exits 0.",
		Flags: migrateTaskHeaderBlockFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate task-header-block", Comment: "Report what would be constructed; writes nothing"},
			{Cmd: "vp migrate task-header-block -p rezbldr", Comment: "Report for one project"},
			{Cmd: "vp migrate task-header-block --apply", Comment: "Apply to the configured vault"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateTaskHeaderBlockFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-header-block: %v\n", err)
				return cli.ExitUser
			}
			root, err := resolveMigrationVaultRoot(fv.Get("--vault"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-header-block: %v\n", err)
				return cli.ExitUser
			}
			// preRun's surfaceGate checked only the CONFIGURED vault; this is
			// the root this run will actually write, however it was resolved.
			if code := enforceSurfaceOnRoot(root); code != cli.ExitOK {
				return code
			}
			sum, err := runTaskHeaderBlockMigration(root, fv.Get("--project"), fv.Bool("--apply"), os.Stdout)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-header-block: %v\n", err)
				return cli.ExitSystem
			}
			if sum.Failed > 0 {
				fmt.Fprintf(os.Stderr, "vp migrate task-header-block: %d file(s) failed\n", sum.Failed)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

// ---------------------------------------------------------------------------
// The transform — a pure function of one file's bytes plus its directory.
//
// It derives nothing from git, nothing from the filesystem and nothing from any
// other file, so this command needs no planner/executor split: there is no
// footprint for a derivation to read back. See planner_no_write.go.
// ---------------------------------------------------------------------------

// taskHeaderBlockConstructedRun builds the two lines this command inserts.
//
// 🔴 The priority is storage.LegacyPriorityDefault rather than a literal, and
// that is not tidiness. The constant carries the operator decision in its own doc
// comment, so the fabricated value and its authority live in one place; a literal
// here would be a second, unattributed copy that could drift from it silently.
func taskHeaderBlockConstructedRun(status string) []string {
	return []string{
		"**Status:** " + status,
		"**Priority:** " + storage.LegacyPriorityDefault,
	}
}

// planTaskHeaderBlock decides one file. after is meaningful only for
// blockConstruct; reason carries the detail the report prints.
//
// 🔴 EVERY SHAPE QUESTION IS ASKED OVER mdfence.OutsideFences, NEVER OVER RAW
// LINES. Three files in the live population carry '#'-prefixed lines inside code
// fences — TOML comments and a shell usage banner — and a raw `^# ` count reads
// 4, 5 and 6 titles in them against a fence-aware 1. A blind precondition refuses
// all three, which is a refusal citing a cause that does not exist.
func planTaskHeaderBlock(content, dirStatus string) (after string, outcome taskHeaderBlockOutcome, reason string) {
	if verr := storage.ValidateWholeTaskFile(content); verr == nil {
		return "", blockNoWork, ""
	}

	// 🔴 FENCE BALANCE FIRST, AND THE ORDER MATCHES THE VALIDATOR'S. An
	// unterminated fence makes every later outside-fence question meaningless,
	// and ValidateWholeTaskFile returns on it before its own scan runs, so a
	// later arm can never be the true cause for such a file.
	if taskSectionsUnbalancedFence(content) {
		return "", blockNoWork, "unterminated code fence — a different repair, and every shape question below is unanswerable until it is fixed"
	}

	outside := mdfence.OutsideFences(content)
	h1, h2, statusLines, priorityLines := 0, 0, 0, 0
	titleNum := 0
	for _, l := range outside {
		trimmed := strings.TrimSpace(l.Text)
		if lvl, _, ok := headingLevel(trimmed); ok {
			switch lvl {
			case 1:
				h1++
				if titleNum == 0 {
					titleNum = l.Num
				}
			case 2:
				h2++
			}
		}
		if _, ok := storage.TaskStatusValue(l.Text); ok {
			statusLines++
		}
		if _, ok := storage.TaskPriorityValue(l.Text); ok {
			priorityLines++
		}
	}

	switch {
	case h1 != 1:
		return "", blockNoWork, fmt.Sprintf("has %d \"# \" H1 heading(s) outside fences, want exactly one — a title defect, not a missing header block", h1)
	case h2 == 0:
		return "", blockNoWork, "no \"## \" H2 heading outside fences — a missing-section defect, not a missing header block"
	case statusLines > 0 || priorityLines > 0:
		return "", blockNoWork, fmt.Sprintf("already declares %d \"**Status:**\" and %d \"**Priority:**\" line(s) — this command CONSTRUCTS a run where there is none, it does not repair one", statusLines, priorityLines)
	}

	lines := strings.Split(content, "\n")
	if titleNum < 1 || titleNum > len(lines) {
		return "", blockRefused, fmt.Sprintf("title line %d is out of range for a %d-line file", titleNum, len(lines))
	}

	// Insert after the title AND after whatever blank run already follows it, so
	// the file's existing spacing is preserved rather than replaced.
	at := titleNum // 0-indexed position of the line after the title
	for at < len(lines) && strings.TrimSpace(lines[at]) == "" {
		at++
	}

	// 🔴 REFUSE A FILE THAT ALREADY HAS A FIELD RUN, even though it declares
	// neither Status nor Priority. Such a file would VALIDATE after the insert —
	// the constructed run becomes the block and the pre-existing fields fall
	// outside it — while the author's run has been silently split in two. No live
	// file has this shape today, which is exactly why the arm needs a fixture.
	if at < len(lines) && storage.IsHeaderFieldLine(lines[at]) {
		return "", blockRefused, fmt.Sprintf("line %d already opens a header field run (%q); constructing a second one would split it", at+1, strings.TrimSpace(lines[at]))
	}

	// 🔴 THE TRAILING BLANK IS A CORRECTNESS REQUIREMENT AND THE VALIDATOR IS
	// BLIND TO IT. Without it, on a file whose next line is prose rather than a
	// heading, CommonMark folds both constructed fields into that paragraph: the
	// header stops being a header, the file still validates, and nothing in the
	// toolchain reports it. One file in the live population has that shape.
	run := append(taskHeaderBlockConstructedRun(dirStatus), "")
	out := make([]string, 0, len(lines)+len(run))
	out = append(out, lines[:at]...)
	out = append(out, run...)
	out = append(out, lines[at:]...)
	// Split/Join round-trips the file's trailing-newline shape exactly, so the
	// final byte is preserved. Four files in the population lack a final newline
	// and one carries a trailing blank; a writer that normalised them would do so
	// invisibly, since no validator or audit dimension looks.
	rebuilt := strings.Join(out, "\n")

	if verr := storage.ValidateWholeTaskFile(rebuilt); verr != nil {
		return "", blockRefused, fmt.Sprintf("the constructed file does not validate: %v", verr)
	}
	return rebuilt, blockConstruct, ""
}

// runTaskHeaderBlockMigration is the whole command, injectable for tests.
//
// 🔴 IT WALKS THE TASK DIRECTORIES ITSELF, AND MUST KEEP DOING SO. This command
// is an EVIDENCE REPORTER for the audit's task-file-validity dimension: its
// population differential compares this walk against `vp audit task-files`. The
// two deliberately share one predicate (storage.ValidateWholeTaskFile), so the
// only thing that differential can prove is that the two ENUMERATIONS agree —
// and a "simplification" that made this reach storage.Vault.ListAllProjects would
// turn the differential GREEN while destroying it. sourceaudit's
// evidenceWalkIndependence rule is what catches that, and it examines only the
// functions named in evidenceReporterFuncs; this function is registered there.
func runTaskHeaderBlockMigration(root, only string, apply bool, out io.Writer) (taskHeaderBlockSummary, error) {
	var sum taskHeaderBlockSummary

	if apply {
		if err := requireVaultGitRepo(root, "this command rewrites archived task files in place, and git "+
			"is what lets you inspect the exact diff — and revert it — before committing the result"); err != nil {
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
		for _, sub := range taskHeaderBlockDirs {
			dirStatus, ok := taskHeaderBlockStatusFor(sub)
			if !ok {
				continue
			}
			dir := filepath.Join(root, "Projects", slug, "tasks", sub)
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
				rel := "Projects/" + slug + "/tasks/" + sub + "/" + name
				data, ferr := os.ReadFile(filepath.Join(dir, name))
				if ferr != nil {
					fmt.Fprintf(out, "  !!    %s/%s (%s/): read: %v\n", slug, taskSlug, sub, ferr)
					sum.Failed++
					continue
				}
				sum.Scanned++

				after, outcome, reason := planTaskHeaderBlock(string(data), dirStatus)
				d := taskHeaderBlockDecision{
					Project: slug, Sub: sub, Slug: taskSlug,
					Outcome: outcome, Reason: reason, Status: dirStatus, After: after,
				}

				switch outcome {
				case blockNoWork:
					sum.NoWork++
					if reason != "" {
						sum.OtherDefect++
						fmt.Fprintf(out, "  --    %s/%s (%s/) — not this command's defect: %s\n",
							slug, taskSlug, sub, reason)
					}
				case blockRefused:
					sum.Refused++
					fmt.Fprintf(out, "  ??    %s/%s (%s/) — refused: %s\n", slug, taskSlug, sub, reason)
				case blockConstruct:
					// 🔴 THE SHADOW GUARD RUNS IN BOTH MODES. The writer resolves
					// ACTIVE first, so an archived slug that is also an active
					// file would rewrite the WRONG one; apply refuses it, so a
					// report printing FIX and inviting --apply would promise
					// something apply categorically refuses. task-header nests
					// this check inside `if apply` at three sites; that is a
					// known defect, not a template.
					if taskHeaderShadowed(out, root, slug, sub, taskSlug, name) {
						d.Outcome = blockRefused
						// 🔴 DERIVED, NEVER HARDCODED, AND THIS LINE HELD A
						// LITERAL THAT WAS FALSE. The guard covers a done/ +
						// cancelled/ pair with no active twin, so a fixed
						// "an ACTIVE task..." sentence named a file that does
						// not exist — a correct printed line beside a false
						// stored cause, with nothing reporting the
						// disagreement. Both halves now come from the single
						// walk in taskHeaderShadowWinner, so they cannot
						// diverge again.
						d.Reason = taskHeaderShadowReason(root, slug, sub, name)
						d.Failed = true
						// 🔴 BOTH COUNTERS, AND THAT IS NOT DOUBLE-COUNTING. This
						// file is refused — the decision is blockRefused and a
						// refusal row is printed — AND the run must exit non-zero,
						// because a shadowed slug is an operator problem that a
						// silent exit 0 would bury.
						//
						// It previously incremented Failed alone, so the summary
						// printed "0 refused" on the same run that printed a
						// refusal row and "1 file(s) FAILED". A counter that reads
						// zero beside its own visible evidence is worse than no
						// counter: the instrument is present and lying.
						sum.Refused++
						sum.Failed++
						sum.Decisions = append(sum.Decisions, d)
						continue
					}
					sum.Fix++
					fmt.Fprintf(out, "  FIX   %s/%s (%s/) — construct **Status:** %s and **Priority:** %s "+
						"(priority FABRICATED, derived from nothing in the file)\n",
						slug, taskSlug, sub, dirStatus, storage.LegacyPriorityDefault)
					if apply {
						// git holds the only copy of whatever a concurrent
						// session has written but not committed, and this is a
						// whole-file overwrite. Per FILE, not per vault.
						dirty, derr := storage.HasUncommittedChanges(root, rel)
						if derr != nil {
							fmt.Fprintf(out, "  !!    %s/%s (%s/): dirty check: %v\n", slug, taskSlug, sub, derr)
							d.Failed = true
							sum.Failed++
							sum.Decisions = append(sum.Decisions, d)
							continue
						}
						if dirty {
							d.Skipped = true
							sum.Dirty++
							fmt.Fprintf(out, "  SKIP  %s/%s (%s/): uncommitted changes — this rewrites the "+
								"whole file and git holds the only copy of %q; commit or stash it, then re-run\n",
								slug, taskSlug, sub, rel)
							sum.Decisions = append(sum.Decisions, d)
							continue
						}
						if werr := vault.OverwriteTaskFileRewritingHeader(slug, taskSlug, after); werr != nil {
							fmt.Fprintf(out, "  !!    %s/%s (%s/): write: %v\n", slug, taskSlug, sub, werr)
							d.Failed = true
							sum.Failed++
							sum.Decisions = append(sum.Decisions, d)
							continue
						}
						d.Applied = true
						sum.Applied++
						taskHeaderBlockRecordWrite(&sum, root, rel)
					}
				}
				sum.Decisions = append(sum.Decisions, d)
			}
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Scanned %d archived task file(s): %d need nothing (%d of them malformed for another reason), "+
		"%d to construct, %d refused.\n",
		sum.Scanned, sum.NoWork, sum.OtherDefect, sum.Fix, sum.Refused)
	if apply {
		fmt.Fprintf(out, "Applied %d construction(s).\n", sum.Applied)
		if sum.Dirty > 0 {
			fmt.Fprintf(out, "%d file(s) SKIPPED for uncommitted changes.\n", sum.Dirty)
		}
		if sum.Applied > 0 {
			fmt.Fprintf(out, "\n🔴 The **Priority:** %s written into %d file(s) is a FABRICATION, "+
				"operator-approved and derived from nothing in any of them. Nothing in the bytes marks it. "+
				"Say so in the commit message — it is the only record.\n",
				storage.LegacyPriorityDefault, sum.Applied)
		}
		taskHeaderBlockRollbackBanner(out, root, sum)
	} else if sum.Fix > 0 {
		fmt.Fprintln(out, "Nothing was written. Re-run with --apply to write.")
	}
	if sum.Failed > 0 {
		fmt.Fprintf(out, "%d file(s) FAILED.\n", sum.Failed)
	}
	return sum, nil
}

// taskHeaderBlockRecordWrite appends the paths one write dirtied: the task file,
// and the .surface stamp the locked writer touches alongside it.
//
// The stamp is listed ONLY when git already tracks it. A project written into for
// the first time has no committed .surface, and `git checkout -- <untracked>` is
// a pathspec error git applies to the WHOLE command — so one such path makes the
// undo restore none of the task files either, while looking like it worked.
func taskHeaderBlockRecordWrite(sum *taskHeaderBlockSummary, root, rel string) {
	sum.AppliedPaths = append(sum.AppliedPaths, rel)

	stamp, err := surface.StampPath(root, filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || stamp == "" {
		return
	}
	if slices.Contains(sum.AppliedPaths, stamp) {
		return
	}
	if tracked, terr := storage.GitPathIsTracked(root, stamp); terr != nil || !tracked {
		return
	}
	sum.AppliedPaths = append(sum.AppliedPaths, stamp)
}

// taskHeaderBlockRollbackBanner prints the undo, scoped to what the run wrote.
// Each path is separately quoted so the whitespace between them is an argument
// separator, which is what `git checkout --` wants; a joined single-quoted list
// keeps the continuation indentation inside the argument.
func taskHeaderBlockRollbackBanner(out io.Writer, root string, sum taskHeaderBlockSummary) {
	if len(sum.AppliedPaths) == 0 {
		return
	}
	fmt.Fprintf(out, "\n%d path(s) were written. To UNDO this run — and nothing else:\n\n",
		len(sum.AppliedPaths))
	fmt.Fprintf(out, "  git -C %s checkout --", root)
	for _, p := range sum.AppliedPaths {
		fmt.Fprintf(out, " \\\n      %q", p)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "\nDo NOT use `git checkout .` — the vault holds every project, and that would "+
		"revert other sessions' in-flight work along with this run.")
}
