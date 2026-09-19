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

// `vp migrate task-header-shape` repairs the ARCHIVED task files whose first
// whole-file-validator failure is a HEADER-SHAPE defect — a duplicated field, a
// second title, prose wedged into the field run, a missing Priority, or an
// unterminated fence. Report by default; writes only under --apply.
//
// # ARCHIVED ONLY. The name does not say so, and this comment is the scope fence.
//
// The walk covers Projects/*/tasks/{done,cancelled} and never the active
// directory. Active files do not carry this corpus's defects — the class exists
// because these files predate the current header contract and no typed writer
// has been able to touch them since.
//
// # WHY THIS IS A SIBLING COMMAND AND NOT ARMS IN AN EXISTING ONE
//
// `vp migrate task-header` dispatches on a single switch over ScanLegacyHeader's
// class, and three of this unit's files are ALREADY classified into arms that run
// first and `continue` — so new arms appended there would be structurally
// unreachable for the only files they exist to repair. That command also carries
// none of the four guards below, so landing here would mean editing all five of
// its existing arms.
//
// `vp migrate task-sections` cannot host the field classes either: its
// TestPlanTaskSections_RefusalsCiteATrueCause ships a fixture pinning a
// duplicate-**Priority:** file as NOT that command's defect, and its
// TestTaskSectionsUsesTheStrictSeam asserts by source text that the permissive
// wrapper appears nowhere in that file. The INSERT class here genuinely requires
// the permissive wrapper, so hosting this work there would invert one shipped
// assertion and falsify the other.
//
// The BOLD pseudo-heading class is the exception and stays in task-sections,
// whose own file comment reserves it by name.
//
// # THE SEAM IS DATA, NOT A GREP
//
// Every class names its seam in taskHeaderShapeSeam, and there is exactly one
// write site per seam behind a switch on that table. A source-text assertion
// cannot express "strict for these classes, permissive for that one" — the
// sibling's absence-grep idiom is not transferable to a file that legitimately
// contains both wrappers — so the pin is a DATA assertion over the table plus an
// exhaustiveness check. A class added without a seam entry is a test failure, not
// a silent default to permissive.

var migrateTaskHeaderShapeFlags = []cli.FlagDef{
	{Name: "--vault", Arg: "PATH", Help: "Vault root to scan and repair (default: the configured vault_path)"},
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Limit to one project (default: every project in the vault)"},
	{Name: "--apply", Help: "WRITE the repairs. Without this the command only reports."},
}

// taskHeaderShapeDirs is the ARCHIVED scope fence.
var taskHeaderShapeDirs = []string{"done", "cancelled"}

// taskHeaderShapeClass names one damage class. The zero value is deliberately
// "not this command's defect" so a file that falls through every detector is
// reported rather than silently transformed.
type taskHeaderShapeClass int

const (
	shapeNoWork taskHeaderShapeClass = iota
	// shapeRelabelPriority / shapeRelabelStatus: a well-formed header block and a
	// SECOND occurrence of the same field below it, outside the block.
	shapeRelabelPriority
	shapeRelabelStatus
	// shapeDupTitle: more than one unfenced "# " H1. The extras are body sections
	// written at the wrong level; they demote FLAT.
	shapeDupTitle
	// shapeFence: a code-fence delimiter with prose glued onto it, which can
	// neither open a fence nor close one.
	shapeFence
	// shapeInsertRelocate: a bare legacy status line directly under the title AND
	// no Priority field. Neither half alone validates.
	shapeInsertRelocate
)

func (c taskHeaderShapeClass) String() string {
	switch c {
	case shapeRelabelPriority:
		return "relabel-priority"
	case shapeRelabelStatus:
		return "relabel-status"
	case shapeDupTitle:
		return "demote-extra-titles"
	case shapeFence:
		return "split-glued-fence"
	case shapeInsertRelocate:
		return "construct-priority+relocate-legacy-status"
	default:
		return "no-work"
	}
}

// taskHeaderShapeSeam names which locked writer a class goes through.
type taskHeaderShapeSeam int

const (
	// seamStrict is storage.Vault.OverwriteTaskFile (headerMustMatch).
	seamStrict taskHeaderShapeSeam = iota
	// seamPermissive is storage.Vault.OverwriteTaskFileRewritingHeader
	// (headerMayChange), required only where a repair moves a header field from
	// absent to present.
	seamPermissive
)

// taskHeaderShapeSeam maps every writing class to its seam.
//
// 🔴 STRICT WHEREVER IT SUFFICES, AND THAT IS A DECISION. The strict policy runs
// refuseHeaderChange and refuseUnknownHeaderFieldChange, so a mis-targeted repair
// that reached a header field is REFUSED rather than silently written. For the
// relabel classes the surviving field is first in the file, so first-wins still
// binds it, and the renamed line is outside extraHeaderFields — strict accepts
// the output and costs nothing. Measured, not assumed: see
// TestTaskHeaderShapeStrictSeamIsEarned.
//
// The permissive wrapper is NOT what grants archived reach. Both wrappers resolve
// through the same resolveTaskFile; the archived refusal lives at the CLI and MCP
// layers. `migrate task-header` writes into done/ through the strict seam today.
var taskHeaderShapeSeamFor = map[taskHeaderShapeClass]taskHeaderShapeSeam{
	shapeRelabelPriority: seamStrict,
	shapeRelabelStatus:   seamStrict,
	shapeDupTitle:        seamStrict,
	shapeFence:           seamStrict,
	// 🔴 THE ONLY PERMISSIVE CLASS, and the reason the seam is a table rather than
	// a constant. This repair moves meta.Priority from absent to present, which is
	// exactly what refuseHeaderChange refuses — set_meta owns that field. Every
	// other class leaves every bound header value byte-identical and therefore
	// goes through the strict writer, which is what makes a mis-targeted repair a
	// refusal rather than a silent write.
	shapeInsertRelocate: seamPermissive,
}

// taskHeaderShapeWritingClasses is the roster the exhaustiveness test compares
// against. A new class must appear here AND in taskHeaderShapeSeamFor.
var taskHeaderShapeWritingClasses = []taskHeaderShapeClass{
	shapeRelabelPriority,
	shapeRelabelStatus,
	shapeDupTitle,
	shapeFence,
	shapeInsertRelocate,
}

type taskHeaderShapeDecision struct {
	Project string
	Sub     string
	Slug    string
	Class   taskHeaderShapeClass
	Reason  string
	After   string
	Applied bool
	Failed  bool
	// Skipped marks a file left alone because it carries uncommitted changes. It
	// is deliberately NOT Failed: nothing went wrong, and one commit makes the
	// next run repair it.
	Skipped bool
}

type taskHeaderShapeSummary struct {
	Scanned int
	NoWork  int
	// OtherDefect counts files that are malformed but NOT this command's defect.
	// They are printed, never silently dropped: a roster nobody sees is what lets
	// the next unit inherit a false cause.
	OtherDefect  int
	Fix          int
	Dirty        int
	Applied      int
	Failed       int
	Decisions    []taskHeaderShapeDecision
	AppliedPaths []string
}

func cmdMigrateTaskHeaderShape() *cli.Command {
	return &cli.Command{
		Name:     "migrate task-header-shape",
		Synopsis: "vp migrate task-header-shape [--vault PATH] [--project P] [--apply]",
		Description: "Repair the ARCHIVED task files whose first whole-file-validator failure is a " +
			"HEADER-SHAPE defect, so storage.ValidateWholeTaskFile accepts them and the status and " +
			"board-fields migrations can reach them.\n\n" +
			"PLAN-FIRST: the bare command REPORTS and writes nothing; pass --apply to write.\n\n" +
			"SCOPE IS ARCHIVED ONLY — Projects/*/tasks/{done,cancelled}, never the active directory.\n\n" +
			"A DUPLICATED \"**Status:**\" or \"**Priority:**\" line is repaired by RELABELLING the " +
			"occurrence that sits OUTSIDE the contiguous header block, never by deleting it. Both " +
			"values survive verbatim and the effective value is unchanged: deleting would be " +
			"equivalent for every parsed reader while destroying what may be the only human-typed " +
			"value in the file, and the task-read surface returns the raw bytes.\n\n" +
			"Direction is a correctness precondition rather than a preference — the relabelled line " +
			"stops being a header field line, so relabelling the IN-BLOCK occurrence would truncate " +
			"the header block and orphan every field below it. A file whose two occurrences are BOTH " +
			"inside the block is REFUSED rather than guessed at.\n\n" +
			"Writes go through the locked task writers, never the generic vault file tools, and each " +
			"class names its seam explicitly. A file is written only if the transformed bytes PASS " +
			"the whole-file validator. Files with uncommitted changes are skipped — git holds the " +
			"only copy. A slug that also exists as an ACTIVE task is refused in BOTH modes. A run in " +
			"which any file failed exits non-zero; a run with nothing to migrate exits 0.",
		Flags: migrateTaskHeaderShapeFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate task-header-shape", Comment: "Report what would be repaired; writes nothing"},
			{Cmd: "vp migrate task-header-shape -p vibe-palace", Comment: "Report for one project"},
			{Cmd: "vp migrate task-header-shape --apply", Comment: "Apply to the configured vault"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateTaskHeaderShapeFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-header-shape: %v\n", err)
				return cli.ExitUser
			}
			root, err := resolveMigrationVaultRoot(fv.Get("--vault"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-header-shape: %v\n", err)
				return cli.ExitUser
			}
			sum, err := runTaskHeaderShapeMigration(root, fv.Get("--project"), fv.Bool("--apply"), os.Stdout)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-header-shape: %v\n", err)
				return cli.ExitSystem
			}
			if sum.Failed > 0 {
				fmt.Fprintf(os.Stderr, "vp migrate task-header-shape: %d file(s) failed\n", sum.Failed)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

// ---------------------------------------------------------------------------
// The planner — a pure function of one file's bytes.
//
// It derives nothing from git, nothing from the filesystem and nothing from any
// other file, which is why this command needs no planner/executor split: there is
// no footprint for a derivation to read back.
// ---------------------------------------------------------------------------

// planTaskHeaderShape classifies one file and returns the repaired bytes.
//
// 🔴 ESCAPE BEFORE REFUSING. A file this command would never touch cannot be
// "refused" for a shape that is none of its business — the validator's own
// first-failure message is the true cause, and anything guessed here would be a
// false one that the next reader inherits.
func planTaskHeaderShape(content string) (after string, class taskHeaderShapeClass, reason string) {
	verr := storage.ValidateWholeTaskFile(content)
	if verr == nil {
		return "", shapeNoWork, ""
	}

	var repaired string
	var rerr error

	switch {
	case strings.Contains(verr.Error(), "two Priority lines"):
		class = shapeRelabelPriority
		repaired, rerr = storage.RepairDuplicateHeaderField(content, "Priority")

	case strings.Contains(verr.Error(), "two Status lines"):
		class = shapeRelabelStatus
		repaired, rerr = storage.RepairDuplicateHeaderField(content, "Status")

	case strings.Contains(verr.Error(), "two title lines"):
		// 🔴 FIRST FAILURE IS NOT ONLY FAILURE. Demoting the extra titles is the
		// whole repair for one corpus file and only half of it for another, where
		// prose also splits the header field run. Re-validate after the first
		// transform rather than assuming one fix per file, and let the class name
		// what was actually done.
		// 🔴 FIRST FAILURE IS NOT ONLY FAILURE. Demoting the extra titles is the
		// whole repair for one corpus file and only half of it for another, where
		// prose also splits the header field run. The second half is a REFUSAL,
		// not a transform — so when the demotion leaves the file invalid, name the
		// wedge rather than reporting that the demotion "did not work". A refusal
		// that cites the wrong cause misdirects whoever consumes the roster.
		class = shapeDupTitle
		repaired, rerr = storage.RepairExtraTitles(content)
		if rerr == nil && storage.ValidateWholeTaskFile(repaired) != nil {
			if w := storage.HeaderRunProseWedges(repaired); len(w) > 0 {
				return "", shapeNoWork, fmt.Sprintf("prose wedged into the header field run at line(s) %v "+
					"(after demoting the extra titles) — indistinguishable from a wrapped field value's "+
					"continuation, so relocating it could silently re-attribute that value; this file "+
					"needs a reviewed hand edit", w)
			}
		}

	case strings.Contains(verr.Error(), "malformed header block"):
		// 🔴 REFUSED DETERMINISTICALLY, AT ANY WEDGE COUNT. A non-field line inside
		// the field run is either interleaved prose or the CONTINUATION of a
		// wrapped field value, and nothing in the bytes tells them apart. Moving a
		// continuation strands the remainder of one field's value directly beneath
		// a different field, where it reads as that field's value — a file that
		// PASSES the whole-file validator while mis-attributing a value, which no
		// validator, audit dimension or test reports.
		//
		// The only available discriminator is semantic, and a capitalisation or
		// punctuation heuristic for it would encode a reading as a rule. These
		// files are hand-edited; this refusal is the repeatable, testable half.
		if w := storage.HeaderRunProseWedges(content); len(w) > 0 {
			return "", shapeNoWork, fmt.Sprintf("prose wedged into the header field run at line(s) %v — "+
				"indistinguishable from a wrapped field value's continuation, so relocating it could "+
				"silently re-attribute that value; this file needs a reviewed hand edit", w)
		}
		return "", shapeNoWork, verr.Error()

	case strings.Contains(verr.Error(), "unterminated code fence"):
		class = shapeFence
		repaired, rerr = storage.RepairGluedFenceDelimiter(content)

	case strings.Contains(verr.Error(), "missing Priority"):
		class = shapeInsertRelocate
		repaired, rerr = storage.RepairBareLegacyStatusLine(content)

	default:
		// Malformed, but not this command's defect. Report the validator's own
		// message rather than a shape this command inferred.
		return "", shapeNoWork, verr.Error()
	}

	if rerr != nil {
		return "", shapeNoWork, rerr.Error()
	}

	// 🔴 THE POST-CONDITION. Shape checks above can be wrong; this cannot. A file
	// is a repair candidate only when the transformed bytes actually satisfy the
	// whole-file validator, so a transform that would not fix the file is
	// reported as someone else's defect rather than written.
	if verr2 := storage.ValidateWholeTaskFile(repaired); verr2 != nil {
		return "", shapeNoWork, fmt.Sprintf("%s would not make the file valid: %v", class, verr2)
	}
	return repaired, class, ""
}

// ---------------------------------------------------------------------------
// The walk.
// ---------------------------------------------------------------------------

// runTaskHeaderShapeMigration is the whole command, injectable for tests.
//
// ONE renderer, both modes: --apply prints the identical plan and then executes
// it, so the report cannot promise something the write does not deliver. The
// corpus is re-scanned fresh under --apply rather than replayed from a printed
// list, so it cannot drift between review and write.
func runTaskHeaderShapeMigration(root, only string, apply bool, out io.Writer) (taskHeaderShapeSummary, error) {
	var sum taskHeaderShapeSummary

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
		for _, sub := range taskHeaderShapeDirs {
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

				after, class, reason := planTaskHeaderShape(string(data))
				d := taskHeaderShapeDecision{
					Project: slug, Sub: sub, Slug: taskSlug,
					Class: class, Reason: reason, After: after,
				}

				if class == shapeNoWork {
					sum.NoWork++
					if reason != "" {
						sum.OtherDefect++
						fmt.Fprintf(out, "  --    %s/%s (%s/) — not this command's defect: %s\n",
							slug, taskSlug, sub, reason)
					}
					sum.Decisions = append(sum.Decisions, d)
					continue
				}

				// 🔴 THE SHADOW GUARD RUNS IN BOTH MODES. The writer resolves by
				// slug, so a slug that also exists elsewhere would rewrite the
				// WRONG file; this file is refused under --apply, so a REPORT
				// that printed FIX and said "re-run with --apply" would promise
				// something apply categorically refuses. The guard is called
				// before the FIX accounting for that reason, and the refused file
				// is still recorded in Decisions so a test can assert on the
				// roll-up without re-parsing the printed report.
				if taskHeaderShadowed(out, root, slug, sub, taskSlug, name) {
					// 🔴 DERIVED, NEVER A LITERAL. taskHeaderShadowReason and the
					// line printed one statement earlier come from the same walk,
					// so the sentence the operator reads and the reason this
					// decision stores cannot disagree about WHICH directory wins.
					// A literal here reads as true and silently drops that — the
					// shape the helper's own doc forbids by name.
					d.Reason = taskHeaderShadowReason(root, slug, sub, name)
					d.Failed = true
					sum.Failed++
					sum.Decisions = append(sum.Decisions, d)
					continue
				}

				sum.Fix++
				fmt.Fprintf(out, "  FIX   %s/%s (%s/) — %s\n", slug, taskSlug, sub, class)

				if apply {
					// git holds the only copy of whatever a concurrent session
					// has written but not committed, and this is a whole-file
					// overwrite. Per FILE, not per vault: a dirty note in another
					// project must not block this repair.
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
					if werr := writeTaskHeaderShape(vault, class, slug, taskSlug, after); werr != nil {
						fmt.Fprintf(out, "  !!    %s/%s (%s/): write: %v\n", slug, taskSlug, sub, werr)
						d.Failed = true
						sum.Failed++
						sum.Decisions = append(sum.Decisions, d)
						continue
					}
					d.Applied = true
					sum.Applied++
					taskHeaderShapeRecordWrite(&sum, root, rel)
				}
				sum.Decisions = append(sum.Decisions, d)
			}
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Scanned %d archived task file(s): %d need nothing (%d of them malformed for another reason), %d to repair.\n",
		sum.Scanned, sum.NoWork, sum.OtherDefect, sum.Fix)
	if apply {
		fmt.Fprintf(out, "Applied %d rewrite(s).\n", sum.Applied)
		if sum.Dirty > 0 {
			fmt.Fprintf(out, "%d file(s) SKIPPED for uncommitted changes.\n", sum.Dirty)
		}
		taskHeaderShapeRollbackBanner(out, root, sum)
	} else if sum.Fix > 0 {
		fmt.Fprintln(out, "Nothing was written. Re-run with --apply to write.")
	}
	if sum.Failed > 0 {
		fmt.Fprintf(out, "%d file(s) FAILED.\n", sum.Failed)
	}
	return sum, nil
}

// writeTaskHeaderShape is the ONLY place this command writes, and the seam comes
// from the table rather than from a literal at the call site. A class with no
// entry is a programming error reported as one, never a silent fallthrough to the
// permissive writer.
func writeTaskHeaderShape(vault *storage.Vault, class taskHeaderShapeClass, project, slug, after string) error {
	seam, ok := taskHeaderShapeSeamFor[class]
	if !ok {
		return fmt.Errorf("no write seam is declared for class %s: add it to taskHeaderShapeSeamFor", class)
	}
	switch seam {
	case seamStrict:
		return vault.OverwriteTaskFile(project, slug, after)
	case seamPermissive:
		return vault.OverwriteTaskFileRewritingHeader(project, slug, after)
	default:
		return fmt.Errorf("unknown write seam %d for class %s", seam, class)
	}
}

// taskHeaderShapeRecordWrite appends the paths one write dirtied: the task file,
// and the .surface stamp the locked writer touches alongside it.
//
// The stamp is listed ONLY when git already tracks it. `git checkout -- <untracked>`
// is a pathspec error that git applies to the WHOLE command, so one such path makes
// the undo restore none of the task files either, while looking like it worked.
func taskHeaderShapeRecordWrite(sum *taskHeaderShapeSummary, root, rel string) {
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

// taskHeaderShapeRollbackBanner prints the undo, scoped to what the run wrote.
//
// Each path is separately quoted so the whitespace between them is an argument
// separator, and the command names `git -C <root>` because the operator's shell is
// generally in the PROJECT repo, not the vault.
func taskHeaderShapeRollbackBanner(out io.Writer, root string, sum taskHeaderShapeSummary) {
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
