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
// # Why the precondition is PER-FILE, and not a whole-vault clean tree
//
// Until 2026-09-05 this command called `requireCleanVaultTree` -> `GitStatusClean`,
// which is repo-wide over a vault that holds EVERY project. So the precondition
// was not "the file I am about to rewrite is recoverable" but "nobody, in any
// project, has uncommitted work" — unsatisfiable by construction in a live agent
// session, where pane captures and transcript flushes re-dirty the tree within
// seconds. It fired for real: a vibe-palace migration was refused because a
// dotfiles task file was open in another session. The unblock was a
// stash-run-restore of a path this work did not own.
//
// The sibling `vp migrate task-header` carries no precondition at all, and its
// four-legged argument (`6fdf569`) transfers here intact: every write is a single
// `atomicfile.Write` through the locked task writer, each file's repair is
// independently valid and idempotent, a repaired file classifies clean so
// re-running converges, and an interrupted run leaves every file either repaired
// or untouched. There is no torn state for a precondition to protect.
//
// 🔴 BUT ONE LEG DOES NOT TRANSFER, AND IT IS WHY THE GATE NARROWS INSTEAD OF
// VANISHING. `task-header` is BYTE-PRESERVING — its repairs carry the legacy
// values forward. This command is LOSSY PER FILE: it replaces
// `**Status:** In Progress — refined 2026-06-21; two reviews folded in` with
// `**Status:** retired`, and the prior value then exists nowhere in the file. Its
// only copy is git history. Overwriting a file that carries UNCOMMITTED operator
// edits therefore destroys work no `git checkout` can separate out afterwards.
//
// That concern is genuine; only its SCOPE was wrong. It is a per-file question —
// "can I restore THIS file" — and a whole-vault predicate is not that predicate.
// So:
//
//   - `--apply` still requires the vault to BE a git repo (`requireVaultGitRepo`).
//     Without git there is no copy of the prior status anywhere, and that floor is
//     unchanged.
//   - Immediately before each write, `storage.HasUncommittedChanges` is asked about
//     THAT ONE PATH. A dirty file is skipped and reported; every other file in the
//     run still gets repaired.
//
// The check is per-file rather than a plan-first pass at the top because the write
// set does not exist until the directories are walked, and because per-file
// degrades the failure from "the whole run refuses" to "this file is skipped and
// named" — the same shape as the active-slug shadow refusal further down.
//
// # `.surface` is deliberately OUT of the precondition and IN the rollback list
//
// `atomicfile.Write` calls `surface.StampForPath` on every content write, so a task
// rewrite also touches `Projects/<p>/.surface`. Capture traffic writes that stamp
// too, so the two path sets are NOT disjoint and a naive "check everything the run
// touches" would re-import the very cross-session block this change deletes.
//
// It is excluded from the PRECONDITION because the precondition asks what this
// command DESTROYS, and it destroys nothing there: `surface.WriteStamp` returns
// early when `existing.Surface >= version`, so on a vault already at the binary's
// surface version the stamp write is a byte no-op git never sees, and when it does
// fire it only advances a monotone marker regenerable from the binary. Nothing in
// it is unrecoverable, which is the whole basis of the per-file check.
//
// It is included in the printed ROLLBACK LIST because it is a byte this run wrote,
// and a `git checkout` over a list that omitted it would leave the stamp advanced
// after a rollback the operator was told was complete. `taskHeaderRecordWrite`
// makes the same split for the same reason.
//
// # The gate's real job was rollback-on-REGRET, so a path list replaces it
//
// The old error string named `git checkout .` — a pre-emptive undo for an operator
// who reads the report afterwards and wants the run gone, not a failure handler
// (`atomicfile`'s tmp+fsync+rename already means a failed write leaves no torn
// file, under every option). Nothing programmatic replaces that. What replaces it
// is PRINTING the exact paths this run wrote, so the operator's
// `git checkout -- <paths>` is both complete and narrow — narrow being the half a
// whole-tree `git checkout .` never was, since it would also revert whatever every
// other session had in flight.
//
// # The population this ruling does NOT cover
//
// Re-derive: `grep -rn 'requireCleanVaultTree\|GitStatusClean' cmd/ internal/ | grep -v _test.go`
//
// `vp migrate kg-filenames` is EXCLUDED and keeps its gate. It calls
// `storage.GitStatusClean` INLINE (`cmd_migrate_kg.go:109`) and never touches
// `requireCleanVaultTree`, so editing only that helper would not have reached it
// anyway — and its gate does double duty: it runs `storage.GitAdd(root, "palace")`,
// a DIRECTORY pathspec over the busiest capture destination in the vault, whose
// safety depends entirely on the tree being clean. Narrowing that one is a
// different, harder change and is not this one.
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
			"file tools (which refuse task paths). --apply requires the vault to be a git repo, and " +
			"checks each file for uncommitted changes immediately before rewriting it: this repair " +
			"is lossy per file, so a file carrying in-flight edits is SKIPPED and named rather than " +
			"overwritten. Unrelated dirt anywhere else in the vault does not block the run. After an " +
			"--apply the exact paths written are printed, so `git checkout -- ...` undoes this run " +
			"and nothing else. A run in which any file failed exits non-zero.",
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
			// `migrate task-preamble` once printed "  !!" per failure and still
			// returned nil, so a migration that wrote NOTHING reported success.
			// That was filed as
			// `migrate-task-preamble-exits-zero-on-total-write-failure` and has
			// since been fixed: it now keeps its own Failed counter and exits
			// non-zero the same way. The rule belongs to both commands.
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
	// Skipped marks a file left alone because it carries uncommitted changes.
	// It is deliberately NOT Failed: nothing went wrong, the file is simply not
	// recoverable right now, and one commit makes the next run repair it.
	Skipped bool
}

type taskStatusSummary struct {
	Scanned  int // archived files read
	OK       int // status already agrees
	NoStatus int // no **Status:** line outside fences — left alone, by design
	Fix      int // disagreeing files
	Applied  int // rewrites that succeeded
	Failed   int // read errors, refused shadows, and failed writes
	// Dirty counts files skipped because they carry uncommitted changes, and it
	// is its OWN counter rather than another entry in Failed.
	//
	// 🔴 THE TWO REFUSALS IN THIS COMMAND ARE DIFFERENT CLAIMS ABOUT THE FILE AND
	// MUST NOT SHARE A BUCKET. A refused shadow (a slug present in BOTH the active
	// and an archive directory) is a genuine vault defect that will never
	// self-heal and needs a human. A dirty file is an operator with work in
	// flight; it heals the moment they commit, and the next run repairs it. Two
	// conditions with opposite remediations counted as one number is how a report
	// stops telling an operator what to do. Dirty therefore does not flip the exit
	// code either — `task-header` makes the same call for its sign-offs.
	Dirty int
	// AppliedPaths are the vault-relative paths this run WROTE, in write order:
	// each repaired task file, plus the .surface stamp the writer touches
	// alongside it. They are what the rollback banner prints, and printing them
	// exactly is the difference between an undo scoped to this run and a
	// whole-tree `git checkout .` that also reverts everyone else's work.
	AppliedPaths []string
	Plans        []taskStatusPlan
}

// runTaskStatusMigration is the whole command, injectable for tests.
//
// It walks the archive DIRECTORIES rather than asking vp_list_tasks: the
// listing is built from the active graph, and these files are precisely the
// ones it does not carry.
func runTaskStatusMigration(root, only string, apply bool, out io.Writer) (taskStatusSummary, error) {
	var sum taskStatusSummary

	if apply {
		// Repo-existence only. The CLEANLINESS half moved to a per-file check
		// immediately before each write — see the header comment for why the
		// recoverability concern is real but its whole-vault scope was not.
		if err := requireVaultGitRepo(root); err != nil {
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
					// 🔴 THE PRECONDITION, AND IT IS ABOUT THIS ONE PATH. The repair
					// is lossy — the value in `found` survives nowhere in the file
					// afterwards — so the question that has to be answered before the
					// first byte is written is "can the operator get THIS file back".
					// Asking it of the whole vault answered a different question and
					// let another project's in-flight work refuse the run.
					rel := taskStatusRelPath(slug, ad.dir, name)
					dirty, derr := storage.HasUncommittedChanges(root, rel)
					if derr != nil {
						fmt.Fprintf(out, "  !!    %s/%s: git status: %v\n", slug, taskSlug, derr)
						plan.Failed = true
						sum.Failed++
						sum.Plans = append(sum.Plans, plan)
						continue
					}
					if dirty {
						fmt.Fprintf(out, "  SKIP  %s/%s: uncommitted changes — this repair is lossy and "+
							"git holds the only copy of %q; commit or stash %s, then re-run\n",
							slug, taskSlug, found, rel)
						plan.Skipped = true
						sum.Dirty++
						sum.Plans = append(sum.Plans, plan)
						continue
					}

					after := replaceLine(before, lineNum, "**Status:** "+ad.status)
					if werr := vault.OverwriteTaskFileRewritingHeader(slug, taskSlug, after); werr != nil {
						fmt.Fprintf(out, "  !!    %s/%s: write: %v\n", slug, taskSlug, werr)
						plan.Failed = true
						sum.Failed++
						sum.Plans = append(sum.Plans, plan)
						continue
					}
					plan.Applied = true
					sum.Applied++
					taskStatusRecordWrite(&sum, root, rel)
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
	if sum.Dirty > 0 {
		fmt.Fprintf(out, "%d file(s) SKIPPED for uncommitted changes — commit or stash them and re-run.\n", sum.Dirty)
	}
	if sum.Failed > 0 {
		fmt.Fprintf(out, "%d file(s) FAILED.\n", sum.Failed)
	}
	taskStatusRollbackBanner(out, root, sum)
	return sum, nil
}

// requireVaultGitRepo refuses an --apply run unless the vault is a git repo.
//
// This is the half of the old whole-tree gate that survived, and the reason is
// unchanged from `requireCleanVaultTree`'s: this repair is lossy per file, so the
// value it overwrites has to exist somewhere else BEFORE the first byte is
// written, and git history is that somewhere. What did NOT survive is asking
// whether the whole vault is clean — that answered "is anyone anywhere mid-edit",
// which is not the recoverability question and is unsatisfiable in a live session.
//
// It is deliberately its own function rather than a call into
// `requireCleanVaultTree` with a flag: that helper still guards three siblings
// whose gates are not being narrowed here, and a shared predicate with a mode
// switch is how two callers come to disagree about what the gate means.
func requireVaultGitRepo(root string) error {
	if !storage.GitAvailable() || !storage.GitIsRepo(root) {
		return fmt.Errorf("vault at %s is not a git repository; --apply requires git, because this "+
			"repair overwrites a status value whose only other copy is git history", root)
	}
	return nil
}

// taskStatusRelPath is the vault-relative path of one archived task file.
//
// Archive-only, so unlike `taskHeaderRelPath` there is no empty-subdirectory
// case: this command never reads or writes an active task.
func taskStatusRelPath(project, sub, name string) string {
	return "Projects/" + project + "/tasks/" + sub + "/" + name
}

// taskStatusRecordWrite appends the paths one write dirtied: the task file, and
// the .surface stamp the locked writer touches alongside it.
//
// The stamp is in the ROLLBACK list and out of the PRECONDITION, deliberately —
// the header comment carries the full argument. In one line: it is a byte this run
// wrote, so an undo that omitted it would leave the stamp advanced; but it holds
// nothing unrecoverable, so gating on it would put a capture-written path back in
// the way of a task repair.
//
// 🔴 THE STAMP IS ONLY LISTED WHEN GIT ALREADY TRACKS IT, AND THAT IS THE
// DIFFERENCE BETWEEN A ROLLBACK THAT RUNS AND ONE THAT SILENTLY DOES NOTHING.
// A project written into for the first time has no committed `.surface`, and
// `git checkout -- <untracked>` is a pathspec error — which git applies to the
// WHOLE command, so one such path makes the undo restore none of the task files
// either, while looking to the operator like it worked. A stamp with no committed
// state also has nothing to restore: it records a surface version, is regenerable
// from the binary, and is correct whatever the content rolls back to.
//
// The task file needs no such check, and the reason is structural rather than
// lucky: the per-file precondition above refuses any path `git status --porcelain`
// reports, and an untracked file is reported (`??`). Every file this function is
// reached for was therefore proved tracked-and-clean moments earlier.
//
// The stamp path comes from surface.StampPath rather than a joined literal, so this
// does not own a second copy of the stamp filename or of where stamps live.
func taskStatusRecordWrite(sum *taskStatusSummary, root, rel string) {
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

// taskStatusRollbackBanner prints the undo this command's precondition used to
// promise, scoped to what the run actually wrote.
//
// 🔴 IT REPLACES A GATE, IT IS NOT DECORATION. The old refusal existed so that
// `git checkout .` would be a guaranteed rollback — an operator's pre-emptive undo
// after reading the report, not a failure handler (atomicfile's tmp+fsync+rename
// already means a failed write leaves no torn file). Removing the gate without
// printing this list would delete the undo along with the over-broad precondition.
//
// 🔴 EACH PATH IS SEPARATELY QUOTED, AND THAT IS NOT COSMETIC. The sibling banner
// in `migrate task-header` learned this the expensive way: a list joined into ONE
// quoted value across backslash-continued lines keeps the continuation indentation
// inside the argument. Quoting each path on its own means the whitespace between
// them is an argument separator, which is exactly what `git checkout --` wants, and
// a path containing a space still survives.
//
// The command is printed with `git -C <root>` rather than bare `git` because the
// operator's shell is generally in the PROJECT repo, not the vault, and a
// `git checkout` run in the wrong repo either fails or reverts the wrong tree.
func taskStatusRollbackBanner(out io.Writer, root string, sum taskStatusSummary) {
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
	// The point of naming paths instead of `.`: a whole-tree checkout in a vault
	// that holds every project would also revert whatever other sessions have in
	// flight, which is the failure this command's precondition used to cause.
	fmt.Fprintln(out, "\nDo NOT use `git checkout .` — the vault holds every project, and that would "+
		"revert other sessions' in-flight work along with this run.")
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
