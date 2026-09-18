// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// The one-time, vault-wide migration that brings every existing task file up
// to the board-reporting schema: renames the active-directory legacy
// "pending" status to "planning" (the archived pending/retired->done/
// cancelled repair is reused, not re-implemented — see phase 1 below),
// backfills CreateTime/ModTime from git history, and stamps the per-file
// DataFormat marker.
//
// # Phase 1 is a fold-in, not a re-implementation
//
// `vp migrate task-status --apply` already makes every ARCHIVED task's
// "**Status:**" line agree with its directory. Its own scope note is
// structural, not empirical: it reads only tasks/done/ and tasks/cancelled/,
// so it is IMPOSSIBLE for it to have ever touched an active task's
// pending->planning rename. This command's own new logic therefore only
// needs to own that one active-directory rename; for archived files it calls
// runTaskStatusMigration directly (same package, no export needed) as its
// own first phase, per the operator's decision to fold the two into one
// coordinated-window command rather than leave them as separate runbook
// steps.
//
// # Phase 2's CreateTime backfill: the tombstone chase
//
// ModTime needs no rename-tracing: it is simply the most recent commit
// touching the file's CURRENT path. CreateTime is where the real work is.
// `MoveTaskToProject` is a bare rename with no provenance of its own — the
// `move` action's destination ADD and source DELETE land in two separate,
// sequential commits (destination via one commitTaskWrite call, source via a
// later, independent one), so git's own rename-similarity detection cannot
// bridge a cross-project move: there is no paired add+delete in one commit's
// diff for `--follow` to find. The recoverable signal is instead the
// tombstone `MoveProvenance.TombstoneSpec` files at the source: a task whose
// slug has no local history should check whether any OTHER project's
// tasks/cancelled/<slug>.md is titled exactly "Moved to <this project>", and
// if so walk that project's OWN history for the slug's origin instead —
// recursively, since a task can move more than once (A->B->C). A tombstone
// chase can cycle (two independent projects holding tombstones that name
// each other for an unrelated, coincidentally-same-slug pair of moves), so
// the walk carries a visited-set exactly like taskgraph's own
// parentCycles/supersededByCycles precedent, and reports "unknown, cycle
// detected" rather than looping.
//
// # Never fabricate a date
//
// A file with no derivable git history (a non-git vault, or a path git
// cannot find any commit for) gets its Status rename and DataFormat stamp
// applied as usual; CreateTime/ModTime are simply left absent. This does not
// block the run or the final format stamp — only a genuine write failure or
// a file skipped for uncommitted changes does (see the WriteFormat gate
// below).
//
// # The WriteFormat gate: Dirty blocks it exactly like Failed
//
// This is explicitly a ONE-TIME, non-repeating, coordinated-window
// operation (every other session shut down first) — unlike `migrate
// task-status`'s own everyday, independently re-runnable use, where a Dirty
// skip is an ordinary, self-healing condition. A Dirty-skipped file here
// gets NONE of this migration's work (no rename, no CreateTime/ModTime, no
// DataFormat stamp), which is a materially worse gap than the accepted
// "unknown date" case. So `surface.WriteFormat` runs, once, at the very end,
// only when BOTH phases report zero Failed AND zero Dirty.
var migrateTaskBoardFieldsFlags = []cli.FlagDef{
	{Name: "--vault", Arg: "PATH", Help: "Vault root to scan and migrate (default: the configured vault_path)"},
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Limit to one project (default: every project in the vault)"},
	{Name: "--apply", Help: "WRITE the migration. Without this the command only reports."},
}

func cmdMigrateTaskBoardFields() *cli.Command {
	return &cli.Command{
		Name:     "migrate task-board-fields",
		Synopsis: "vp migrate task-board-fields [--vault PATH] [--project P] [--apply]",
		Description: "The one-time, vault-wide migration to the board-reporting task-header schema: " +
			"renames the legacy \"pending\" status to \"planning\" on active tasks, makes an ARCHIVED " +
			"task's status agree with its directory, backfills CreateTime/ModTime from git history " +
			"(with cross-project-move detection, including a multi-hop tombstone chase), and stamps " +
			"the per-file DataFormat marker.\n\n" +
			"PLAN-FIRST, AND THE PLAN IS THE WHOLE RUN: one planner computes every file's decision, " +
			"validates the bytes it would write, and predicts whether the vault ends up fully " +
			"migrated. The bare command prints that plan and writes nothing; --apply prints the same " +
			"plan and then executes it. Report and apply cannot disagree, because there is one " +
			"planner and both modes call it.\n\n" +
			"REFUSALS ARE WHOLE-RUN REFUSALS. If any file would fail the task-file validator, or a " +
			"slug exists in both the active and an archive directory, NOTHING is written and the " +
			"report names every offending file. A one-time migration that half-applies leaves a vault " +
			"with no safe recovery, so the refusal happens while the vault is still untouched.\n\n" +
			"Each task file is written EXACTLY ONCE, through the migration writer, which re-reads " +
			"under the per-path lock and refuses if the file changed between plan and write. Fields " +
			"are filled per-field: a value already on disk is authoritative, and the DataFormat " +
			"marker is refreshed only upward.\n\n" +
			"A file with no derivable git history keeps an unknown CreateTime/ModTime but still gets " +
			"its Status repair and DataFormat stamp — never a fabricated date. The vault-wide " +
			"RequiredDataFormat stamp is DERIVED, not asserted: it advances only when every task " +
			"file in the whole vault carries the marker, so a --project run cannot declare the vault " +
			"current, and a sequence of scoped runs correctly stamps when the last one completes.\n\n" +
			"--apply requires the vault to be a git repo (every date comes from git history), writes " +
			"directly (no staging, no auto-commit), and prints a rollback banner naming every path " +
			"it wrote, so `git checkout -- ...` undoes exactly this run.\n\n" +
			"Manual, operator-invoked, run at a coordinated maintenance window — see the task's own " +
			"Direction for the required two-tier testing strategy before ever pointing this at the " +
			"real vault.",
		Flags: migrateTaskBoardFieldsFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate task-board-fields", Comment: "Report what would migrate; writes nothing"},
			{Cmd: "vp migrate task-board-fields -p vibe-palace", Comment: "Report for one project"},
			{Cmd: "vp migrate task-board-fields --apply", Comment: "Migrate the whole vault"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateTaskBoardFieldsFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-board-fields: %v\n", err)
				return cli.ExitUser
			}
			root, err := resolveMigrationVaultRoot(fv.Get("--vault"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-board-fields: %v\n", err)
				return cli.ExitUser
			}
			ps, err := runTaskBoardFieldsMigration(root, fv.Get("--project"), fv.Bool("--apply"), os.Stdout)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-board-fields: %v\n", err)
				return cli.ExitSystem
			}
			if ps.Refusals > 0 || ps.Failed > 0 {
				fmt.Fprintf(os.Stderr, "vp migrate task-board-fields: %d file(s) refused or failed\n",
					ps.Refusals+ps.Failed)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

// boardFieldsPlan is one task file's decision, computed by the PLANNER and
// executed verbatim by the executor. It carries the derived values, the digest
// of the bytes they produce, and — after execution — that file's outcome.
type boardFieldsPlan struct {
	Project string
	Slug    string
	Dir     string // "", "done", or "cancelled"
	RelPath string

	StatusFrom string
	StatusTo   string // empty = unchanged

	CreateTime string // empty = unknown (no history, or a cycle)
	CreateWhy  string
	ModTime    string // empty = unknown (no history)
	DataFormat string

	// WantSHA256 is the digest of the bytes the planner simulated. The executor
	// re-derives it under the lock and refuses on any disagreement.
	WantSHA256 string

	NoWork        bool // the transform is a no-op: nothing to do for this file
	ShadowRefused bool
	// ShadowWinner is the directory the writer would resolve FIRST — "" for the
	// active dir. Stored rather than re-derived so the rendered cause and the
	// planner's decision come from one walk.
	ShadowWinner  string
	InvalidReason string // VALID before, INVALID after: a defect in this migration
	BrokenReason  string // ALREADY invalid before this run touched it

	// Execution outcomes, set only by executeBoardFieldsPlan.
	Applied bool
	Failed  bool
	Skipped bool // Dirty
	Drifted bool // changed between plan and write
}

// boardFieldsPlanSet is the whole plan: every file's decision plus the roll-up
// both the report and the executor read. It is produced by a function that does
// not write, and consumed by one that does not derive.
type boardFieldsPlanSet struct {
	Root string
	Only string

	Plans []boardFieldsPlan

	Scanned       int
	NoWork        int
	ToMigrate     int
	StatusRepairs int // files whose Status line this run would rewrite
	UnknownDates  int
	Refusals      int // shadow-slug refusals plus would-break-this-file refusals
	Preexisting   int // files already malformed before this run; skipped, not failed

	// WillStampFormat is the PREDICTION: after this plan is applied, will every
	// task file in the WHOLE vault carry a DataFormat marker at or above this
	// binary's RequiredDataFormat? Computed at plan time, over every project,
	// regardless of --project.
	WillStampFormat bool
	StampReason     string

	// Execution results.
	Applied      int
	Failed       int
	Dirty        int
	AppliedPaths []string
}

// boardFieldsFileIsCurrent reports whether a task file already carries a
// DataFormat marker at or above what this binary requires.
//
// 🔴 THIS, NOT CreateTime, IS THE "already migrated" PREDICATE. Keying off
// CreateTime stored a derived value and was wrong twice over: a file can carry
// CreateTime from CreateTask while never having been migrated, and a file with
// no derivable git history legitimately ends the migration with NO CreateTime at
// all ("never fabricate a date") — so CreateTime-presence both over- and
// under-reports. The DataFormat marker is the field that exists to answer
// exactly this question.
func boardFieldsFileIsCurrent(slug, content string, archived bool) bool {
	meta := storage.ParseTaskMetaFromContent(slug, content, archived)
	n, err := strconv.Atoi(strings.TrimSpace(meta.DataFormat))
	return err == nil && n >= surface.RequiredDataFormat
}

// boardFieldsStatusTarget returns the Status value this file should carry, or ""
// to leave it alone.
//
// ONE population, shared with `vp migrate task-status` rather than re-derived:
// the archived arm asks findStatusLineOutsideFences (fence-aware, built on
// storage.TaskStatusValue) and storage.IsTerminalStatus, which are the same two
// decisions that command's own header comment names as the single source. The
// active arm is this migration's own: the literal legacy "pending" spelling
// becomes "planning", and anything else — already-valid, or genuinely free-text
// legacy — is left alone rather than guessed at.
func boardFieldsStatusTarget(dir, content string) (from, to string) {
	_, found, ok := findStatusLineOutsideFences(content)
	if !ok {
		// No Status line outside a fence. Absence is the older header format,
		// not a false claim: never insert one.
		return "", ""
	}
	if dir == "" {
		if strings.EqualFold(strings.TrimSpace(found), "pending") {
			return found, "planning"
		}
		return found, ""
	}
	if storage.IsTerminalStatus(found) {
		return found, ""
	}
	for _, ad := range archiveDirs {
		if ad.dir == dir {
			return found, ad.status
		}
	}
	return found, ""
}

// boardFieldsTaskFiles lists one project directory's task files, sorted.
func boardFieldsTaskFiles(root, proj, sub string) []string {
	entries, err := os.ReadDir(filepath.Join(root, "Projects", proj, "tasks", sub))
	if err != nil {
		// A project with no done/ or cancelled/ (or no active tasks/) is normal.
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// planBoardFieldsMigration computes the COMPLETE migration and writes nothing.
//
// 🔴 IT TAKES A ROOT, NOT A VAULT, AND THAT IS A CONVENTION — NOT A GUARANTEE.
// A function holding a root path can still write: storage.NewVault(root) is
// exported and os.WriteFile needs nothing else. The enforcement that this
// function performs no writes is the sourceaudit ratchet (see
// internal/sourceaudit/planner_no_write.go), backed by a test that hashes the
// whole vault across a planning call. Neither is a compiler guarantee and this
// comment must not be read as claiming one.
//
// Why the split exists at all: every date is derived HERE, before the executor
// writes anything, so deriveModTime's `git log -1` cannot read a commit this run
// produced. That hazard is not hypothetical — it is how the shipped version
// corrupted ModTime on every file it repaired before aborting.
func planBoardFieldsMigration(root, only string) (*boardFieldsPlanSet, error) {
	projects, err := taskPreambleProjects(root, only)
	if err != nil {
		return nil, err
	}
	// The tombstone chase must reach a source project's tombstone regardless of
	// --project scoping: --project limits which files are MIGRATED, not which
	// projects can hold a cross-project move's other half. The stamp prediction
	// below needs the same unscoped list for its own reason.
	allProjects, err := taskPreambleProjects(root, "")
	if err != nil {
		return nil, err
	}

	ps := &boardFieldsPlanSet{Root: root, Only: only}
	dataFormat := strconv.Itoa(surface.RequiredDataFormat)
	planned := make(map[string]bool)

	for _, proj := range projects {
		for _, sub := range []string{"", "done", "cancelled"} {
			for _, name := range boardFieldsTaskFiles(root, proj, sub) {
				slug := strings.TrimSuffix(name, ".md")
				rel := boardFieldsRelPath(proj, sub, name)
				data, ferr := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
				if ferr != nil {
					return nil, fmt.Errorf("read %s: %w", rel, ferr)
				}
				ps.Scanned++
				content := string(data)

				plan := boardFieldsPlan{Project: proj, Slug: slug, Dir: sub, RelPath: rel}

				// Shadow-slug guard: resolveTaskFile resolves active before done
				// before cancelled and returns the FIRST hit, so a candidate an
				// earlier directory also holds would send this write to the wrong
				// file. Refuse rather than repair.
				//
				// 🔴 THE PURE PREDICATE, NOT THE PRINTING WRAPPER. This is a
				// PLANNER: it takes no io.Writer and must not acquire one, which
				// the plannerNoWrite ratchet exists to enforce. taskHeaderShadowed
				// prints; taskHeaderShadowWinner is the same single walk without
				// the printing, so the planner and the operator-facing line cannot
				// disagree about which directory wins.
				//
				// 🔴 THIS USED TO BE ACTIVE-ONLY, AND ITS OUTCOME WAS SAFE FOR THE
				// WRONG REASON. A done/+cancelled/ pair with no active twin passed
				// planning, and the write was then refused downstream by
				// ApplyTaskMigrationFields' WantSHA256 compare — which reports a
				// HASH MISMATCH. The operator was told the file changed under the
				// plan. It had not; the plan had resolved to a different file.
				// Naming the shadow here puts the CAS back to catching what it is
				// for.
				if winner, shadowed := taskHeaderShadowWinner(root, proj, sub, name); shadowed {
					plan.ShadowRefused = true
					plan.ShadowWinner = winner
					ps.Refusals++
					ps.Plans = append(ps.Plans, plan)
					continue
				}

				from, to := boardFieldsStatusTarget(sub, content)
				plan.StatusFrom, plan.StatusTo = from, to

				createTime, createWhy, cerr := deriveCreateTime(root, allProjects, proj, slug, rel)
				if cerr != nil {
					return nil, fmt.Errorf("%s: derive CreateTime: %w", rel, cerr)
				}
				modTime, merr := deriveModTime(root, rel)
				if merr != nil {
					return nil, fmt.Errorf("%s: derive ModTime: %w", rel, merr)
				}
				plan.CreateTime, plan.CreateWhy, plan.ModTime = createTime, createWhy, modTime
				plan.DataFormat = dataFormat

				// 🔴 TWO DIFFERENT FAILURES, AND CONFLATING THEM WAS WRONG.
				// An empty fill changes nothing, so this validates the file AS IT
				// STANDS. A file already malformed is a PRE-EXISTING condition,
				// not a defect in this migration: refusing the whole run for it
				// would hold a one-time migration hostage to damage it did not
				// cause, and the live corpus carries 30 such files whose only
				// fault is an older header format with no Status line — files the
				// shipped version migrated without complaint, because its write
				// path never validated at all.
				//
				// They are still SKIPPED rather than written: a file whose header
				// block cannot be parsed reliably is a file whose header block is
				// the wrong place to insert a field, and upsertHeaderField would
				// be guessing at the position. They also hold the format stamp
				// down, because a vault holding files this migration could not
				// process is not a migrated vault.
				if _, berr := storage.PlanTaskMigrationFields(content, storage.TaskMigrationFill{}); berr != nil {
					plan.BrokenReason = berr.Error()
					ps.Preexisting++
					ps.Plans = append(ps.Plans, plan)
					continue
				}

				fill := plan.fill()
				after, verr := storage.PlanTaskMigrationFields(content, fill)
				if verr != nil {
					// Valid before, invalid after: this migration would BREAK the
					// file. That is a defect in the migration and stops the run.
					plan.InvalidReason = verr.Error()
					ps.Refusals++
					ps.Plans = append(ps.Plans, plan)
					continue
				}
				if after == content {
					plan.NoWork = true
					ps.NoWork++
					planned[rel] = true
					ps.Plans = append(ps.Plans, plan)
					continue
				}

				plan.WantSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(after)))
				ps.ToMigrate++
				if to != "" {
					ps.StatusRepairs++
				}
				if createTime == "" {
					ps.UnknownDates++
				}
				planned[rel] = true
				ps.Plans = append(ps.Plans, plan)
			}
		}
	}

	ps.WillStampFormat, ps.StampReason = predictFormatStamp(root, allProjects, planned)
	return ps, nil
}

// fill is the write this plan entry asks for.
//
// FillAbsentOnly is always true here: a value already on disk is authoritative,
// and DataFormat refreshes only upward. That is defect 7's per-field fill, and
// it is what lets a file carrying CreateTime from CreateTask still receive the
// DataFormat marker the shipped all-or-nothing skip denied it.
func (p boardFieldsPlan) fill() storage.TaskMigrationFill {
	return storage.TaskMigrationFill{
		NewStatus:      p.StatusTo,
		CreateTime:     p.CreateTime,
		ModTime:        p.ModTime,
		DataFormat:     p.DataFormat,
		FillAbsentOnly: true,
	}
}

// predictFormatStamp answers, at PLAN time, whether the vault will be fully
// migrated once this plan is applied.
//
// 🔴 IT SCANS EVERY PROJECT, NOT THE --project SCOPE, and that is the whole
// point. The marker is vault-wide, so deciding it from a project-scoped file set
// asserts a vault-wide fact from project-local evidence — which is how a run
// that migrated 38 of 656 files stamped the entire vault current.
//
// It is a PREDICTION, not a promise: a Dirty skip or a drift refusal can falsify
// it at write time, which is why the executor re-derives the same predicate from
// disk before stamping and reports any divergence.
func predictFormatStamp(root string, allProjects []string, planned map[string]bool) (bool, string) {
	for _, proj := range allProjects {
		for _, sub := range []string{"", "done", "cancelled"} {
			for _, name := range boardFieldsTaskFiles(root, proj, sub) {
				rel := boardFieldsRelPath(proj, sub, name)
				if planned[rel] {
					continue
				}
				data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
				if err != nil {
					return false, fmt.Sprintf("cannot read %s", rel)
				}
				slug := strings.TrimSuffix(name, ".md")
				if !boardFieldsFileIsCurrent(slug, string(data), sub != "") {
					return false, fmt.Sprintf("%s is not covered by this run and is below data format %d",
						rel, surface.RequiredDataFormat)
				}
			}
		}
	}
	return true, ""
}

// vaultIsFullyMigrated re-derives the stamp predicate from DISK, after execution.
func vaultIsFullyMigrated(root string, allProjects []string) (bool, string) {
	return predictFormatStamp(root, allProjects, nil)
}

// printBoardFieldsPlan renders the plan. ONE renderer, called in both modes, so
// the report a human reads before --apply is the same plan --apply executes.
func printBoardFieldsPlan(out io.Writer, ps *boardFieldsPlanSet, apply bool) {
	printVaultRoot(out, ps.Root)
	if apply {
		fmt.Fprintln(out, "Mode:  APPLY — task files will be rewritten.")
	} else {
		fmt.Fprintln(out, "Mode:  REPORT ONLY — nothing is written. Pass --apply to write.")
	}
	fmt.Fprintln(out)

	for _, p := range ps.Plans {
		switch {
		case p.ShadowRefused && p.ShadowWinner == "":
			fmt.Fprintf(out, "  !!    %s/%s: also present in tasks/ (%s) — refusing, the writer resolves active first\n",
				p.Project, p.Slug, boardFieldsRelPath(p.Project, "", p.Slug+".md"))
		case p.ShadowRefused:
			fmt.Fprintf(out, "  !!    %s/%s (%s/): the same slug also exists in tasks/%s/ (%s) — refusing, "+
				"the writer resolves %s before %s\n",
				p.Project, p.Slug, p.Dir, p.ShadowWinner,
				boardFieldsRelPath(p.Project, p.ShadowWinner, p.Slug+".md"), p.ShadowWinner, p.Dir)
		case p.InvalidReason != "":
			fmt.Fprintf(out, "  !!    %s/%s (%s/): this migration would BREAK this file — %s\n",
				p.Project, p.Slug, p.Dir, p.InvalidReason)
		case p.BrokenReason != "":
			fmt.Fprintf(out, "  SKIP  %s/%s (%s/): already malformed before this run — %s\n",
				p.Project, p.Slug, p.Dir, p.BrokenReason)
		case p.NoWork:
			// Nothing to say per file; counted in the roll-up.
		default:
			fmt.Fprintf(out, "  FIX   %s/%s (%s/) — %s\n", p.Project, p.Slug, p.Dir, boardFieldsRowDetail(p))
		}
	}

	fmt.Fprintln(out)
	// 🔴 THE ROLL-UP REPORTS PLANNED WORK, NOT APPLIED WORK. The shipped version
	// printed statusSum.Applied here, which is 0 in report mode by construction —
	// so a report listing hundreds of repairs summarised them as "0 repaired",
	// at exactly the moment an operator decides whether to proceed.
	fmt.Fprintf(out, "Planned: %d task file(s) scanned, %d already current, %d to migrate "+
		"(%d of them a Status repair), %d with unknown dates (no git history or cycle), "+
		"%d already-malformed file(s) skipped, %d refusal(s).\n",
		ps.Scanned, ps.NoWork, ps.ToMigrate, ps.StatusRepairs, ps.UnknownDates,
		ps.Preexisting, ps.Refusals)
	if ps.Preexisting > 0 {
		fmt.Fprintf(out, "The %d skipped file(s) were malformed before this run and need their own "+
			"repair; they hold the vault below data format %d until they are fixed.\n",
			ps.Preexisting, surface.RequiredDataFormat)
	}

	if ps.WillStampFormat {
		fmt.Fprintf(out, "Data format: this run would advance the vault to %d.\n", surface.RequiredDataFormat)
	} else {
		fmt.Fprintf(out, "Data format: this run would NOT advance the vault — %s.\n", ps.StampReason)
	}
}

// executeBoardFieldsPlan applies a plan and derives NOTHING. Every value it
// writes came from the planner; its only decisions are per-file preconditions
// (uncommitted changes) and the guarded write's own drift refusal.
func executeBoardFieldsPlan(vault *storage.Vault, ps *boardFieldsPlanSet, allProjects []string, out io.Writer) error {
	for i := range ps.Plans {
		p := &ps.Plans[i]
		if p.ShadowRefused || p.InvalidReason != "" {
			p.Failed = true
			ps.Failed++
			continue
		}
		if p.NoWork || p.BrokenReason != "" {
			continue
		}

		// 🔴 NO phase1Written MAP, AND NONE IS NEEDED. The shipped version wrote
		// each archived file TWICE — a status repair, then a field backfill — so
		// the second write's dirty check saw dirt the same run had just made, and
		// a map of "paths we wrote" had to exist to tell that apart from a real
		// in-flight operator edit. This run writes each file exactly once, so no
		// file is dirty for a reason this run created, and the check can be taken
		// at face value again.
		dirty, derr := storage.HasUncommittedChanges(ps.Root, p.RelPath)
		if derr != nil {
			fmt.Fprintf(out, "  !!    %s/%s: git status: %v\n", p.Project, p.Slug, derr)
			p.Failed = true
			ps.Failed++
			continue
		}
		if dirty {
			fmt.Fprintf(out, "  SKIP  %s/%s: uncommitted changes — this migration backfills history-derived "+
				"fields and will not overwrite an edit it cannot see. Undo this run with the rollback "+
				"command printed below, resolve %s, then re-run.\n", p.Project, p.Slug, p.RelPath)
			p.Skipped = true
			ps.Dirty++
			continue
		}

		if werr := vault.ApplyTaskMigrationFields(p.Project, p.Slug, p.fill(), p.WantSHA256); werr != nil {
			var drift *storage.TaskMigrationDriftError
			if errors.As(werr, &drift) {
				fmt.Fprintf(out, "  !!    %s/%s: %v\n", p.Project, p.Slug, werr)
				p.Drifted = true
			} else {
				fmt.Fprintf(out, "  !!    %s/%s: write: %v\n", p.Project, p.Slug, werr)
			}
			p.Failed = true
			ps.Failed++
			continue
		}
		p.Applied = true
		ps.Applied++
		boardFieldsRecordWrite(ps, ps.Root, p.RelPath)
	}

	fmt.Fprintf(out, "\nApplied %d rewrite(s); %d failed, %d skipped for uncommitted changes.\n",
		ps.Applied, ps.Failed, ps.Dirty)

	// The stamp is DERIVED from disk, never asserted from this run's own scope.
	complete, why := vaultIsFullyMigrated(ps.Root, allProjects)
	switch {
	case complete && ps.WillStampFormat:
		if werr := surface.WriteFormat(ps.Root, surface.RequiredDataFormat); werr != nil {
			return fmt.Errorf("stamp vault data format: %w", werr)
		}
		fmt.Fprintf(out, "RequiredDataFormat stamped at %d.\n", surface.RequiredDataFormat)
	case complete && !ps.WillStampFormat:
		// Predicted no, outcome yes. Harmless but reported: the prediction is
		// part of the contract and a silent correction hides that it was wrong.
		if werr := surface.WriteFormat(ps.Root, surface.RequiredDataFormat); werr != nil {
			return fmt.Errorf("stamp vault data format: %w", werr)
		}
		fmt.Fprintf(out, "RequiredDataFormat stamped at %d — NOTE: the plan predicted this run would "+
			"not advance the format (%s); it did.\n", surface.RequiredDataFormat, ps.StampReason)
	case !complete && ps.WillStampFormat:
		fmt.Fprintf(out, "RequiredDataFormat was NOT advanced — the plan predicted it would be, but "+
			"%s. A skipped or refused file falsified the prediction; resolve it and re-run.\n", why)
	default:
		fmt.Fprintf(out, "RequiredDataFormat was NOT advanced — %s.\n", why)
	}

	boardFieldsRollbackBanner(out, ps.Root, ps.AppliedPaths)
	return nil
}

// runTaskBoardFieldsMigration plans, reports, and — when apply is set — executes.
func runTaskBoardFieldsMigration(root, only string, apply bool, out io.Writer) (*boardFieldsPlanSet, error) {
	if apply {
		if err := requireVaultGitRepo(root, "this migration derives CreateTime/ModTime from git history and "+
			"backfills header fields whose only other copy is that history"); err != nil {
			return nil, err
		}
	}

	ps, err := planBoardFieldsMigration(root, only)
	if err != nil {
		return nil, err
	}
	printBoardFieldsPlan(out, ps, apply)

	// 🔴 A REFUSAL IS A WHOLE-RUN REFUSAL, BEFORE ANY BYTE IS WRITTEN. The
	// shipped version discovered a file the writer would reject only by trying to
	// write it, 472 files into the run, and then abandoned the rest — leaving a
	// half-migrated vault whose documented recovery corrupted ModTime. Validating
	// every candidate during planning turns that into a report the operator reads
	// while the vault is still untouched.
	if ps.Refusals > 0 {
		fmt.Fprintf(out, "\n%d file(s) would not survive this migration. Nothing was written.\n", ps.Refusals)
		return ps, nil
	}

	if !apply {
		if ps.ToMigrate > 0 {
			fmt.Fprintln(out, "\nNothing was written. Re-run with --apply to write.")
		}
		return ps, nil
	}

	allProjects, err := taskPreambleProjects(root, "")
	if err != nil {
		return nil, err
	}
	if err := executeBoardFieldsPlan(storage.NewVault(root), ps, allProjects, out); err != nil {
		return ps, err
	}
	return ps, nil
}

// boardFieldsRowDetail renders one FIX row's columns.
//
// 🔴 A COLUMN NEVER NAMES A VALUE THE MIGRATION WILL NOT WRITE. The first cut
// rendered an empty target through a sentinel — Status %q->%q with the second
// %q holding the word "unchanged" — so 77 of 606 rows read
//
//	Status "cancelled"->"unchanged"
//
// which parses just as naturally as "sets Status to the string unchanged". This
// is the operator's primary gate before a one-time, vault-wide migration, so a
// row that has to be interpreted correctly is a row that can be interpreted
// wrongly.
//
// The rule, applied to EVERY column and not just the one that was reported: a
// column appears only when this run will write that field. A row showing no
// transition cannot be misread as one, and the 529 rows that DO repair a Status
// now stand out from the 77 that do not, which is the distinction the operator
// is scanning for. An underivable date is rendered without "=" and with its
// reason, so it cannot read as a value either.
func boardFieldsRowDetail(p boardFieldsPlan) string {
	var cols []string
	if p.StatusTo != "" {
		cols = append(cols, fmt.Sprintf("Status %q -> %q", p.StatusFrom, p.StatusTo))
	}
	if p.CreateTime != "" {
		cols = append(cols, fmt.Sprintf("CreateTime=%s (%s)", p.CreateTime, p.CreateWhy))
	} else {
		cols = append(cols, fmt.Sprintf("CreateTime unset (%s)", p.CreateWhy))
	}
	if p.ModTime != "" {
		cols = append(cols, "ModTime="+p.ModTime)
	} else {
		cols = append(cols, "ModTime unset (no git history)")
	}
	cols = append(cols, "DataFormat="+p.DataFormat)
	return strings.Join(cols, "  ")
}

// boardFieldsRelPath is the vault-relative path of one task file, active or
// archived. sub is "", "done", or "cancelled".
func boardFieldsRelPath(project, sub, name string) string {
	if sub == "" {
		return "Projects/" + project + "/tasks/" + name
	}
	return "Projects/" + project + "/tasks/" + sub + "/" + name
}

// boardFieldsRecordWrite appends the paths one write dirtied: the task file,
// and — if git already tracks it — the .surface stamp the locked writer
// touches alongside it. Same reasoning as taskStatusRecordWrite: the stamp is
// in the rollback list (a byte this run wrote) but never blocks anything (it
// holds nothing unrecoverable), and it is only listed when tracked, because
// `git checkout -- <untracked>` is a pathspec error that fails the WHOLE
// checkout command, silently leaving every other path un-rolled-back too.
func boardFieldsRecordWrite(sum *boardFieldsPlanSet, root, rel string) {
	sum.AppliedPaths = append(sum.AppliedPaths, rel)

	stamp, err := surface.StampPath(root, filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || stamp == "" {
		return
	}
	for _, p := range sum.AppliedPaths {
		if p == stamp {
			return
		}
	}
	if tracked, terr := storage.GitPathIsTracked(root, stamp); terr != nil || !tracked {
		return
	}
	sum.AppliedPaths = append(sum.AppliedPaths, stamp)
}

// boardFieldsRollbackBanner is taskStatusRollbackBanner's pattern, extended
// to cover both phases' paths in one combined list — Operator Decision 4: no
// staging, no auto-commit, the operator reviews this exact list and commits
// explicitly. The final .vibe-palace/vault.toml format stamp is deliberately
// NOT in this list (mirroring runKGFilenameApply's own treatment): it is
// monotone, and a `git checkout` over a list that included it would either
// fail outright (untracked on a first-ever migration) or, if somehow
// reverted, leave the vault claiming the NEW format while the content
// reverted to pre-migration — worse than simply leaving it out and saying so.
func boardFieldsRollbackBanner(out io.Writer, root string, paths []string) {
	if len(paths) == 0 {
		return
	}
	fmt.Fprintf(out, "\n%d path(s) were written. To UNDO this run — and nothing else:\n\n", len(paths))
	fmt.Fprintf(out, "  git -C %s checkout --", root)
	for _, p := range paths {
		fmt.Fprintf(out, " \\\n      %q", p)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "\nDo NOT use `git checkout .` — the vault holds every project, and that would "+
		"revert other sessions' in-flight work along with this run.")
	fmt.Fprintln(out, "\nNote: this list does NOT include .vibe-palace/vault.toml (the RequiredDataFormat "+
		"stamp, if it advanced) — that marker is monotone by design; reverting it here without also "+
		"reverting every vault this run touched would leave the vault claiming a format its content "+
		"no longer has.")
}

// deriveModTime is the most recent commit touching path's CURRENT location —
// no rename-tracing needed, since "most recent touch to the file as it
// exists now" is definitionally its own latest commit regardless of any
// earlier rename.
func deriveModTime(root, relPath string) (string, error) {
	return gitLogFirstDate(root, "-1", "--format=%ad", "--date=format:%Y-%m-%d", "--", relPath)
}

// deriveCreateTime derives a task's true creation date, walking a
// cross-project move-tombstone chain backward when one exists.
//
// Returns date="" for every "cannot know" outcome (no git history, or a
// cycle); why then carries a human-readable reason for the report — a bare
// "unknown" would not let an operator tell a genuinely undated legacy file
// apart from a detected data anomaly.
func deriveCreateTime(root string, allProjects []string, project, slug, currentRelPath string) (date, why string, err error) {
	visited := map[string]bool{project: true}
	chain := []string{project}
	cur := project
	hops := 0

	for {
		src, found, ferr := findTombstoneSource(root, allProjects, cur, slug)
		if ferr != nil {
			return "", "", ferr
		}
		if !found {
			break
		}
		if visited[src] {
			return "", fmt.Sprintf("cycle detected in move-tombstone chain: %s -> %s",
				strings.Join(chain, " -> "), src), nil
		}
		visited[src] = true
		chain = append(chain, src)
		cur = src
		hops++
	}

	if hops == 0 {
		d, derr := gitLogFirstDate(root, "--follow", "--reverse", "--format=%ad", "--date=format:%Y-%m-%d", "--", currentRelPath)
		if derr != nil {
			return "", "", derr
		}
		if d == "" {
			return "", "unknown — no git history", nil
		}
		return d, "git log --follow", nil
	}

	// An active task only ever moves while active, so the origin project's
	// true historical path is always the ACTIVE form — no archive-directory
	// ambiguity on the source side. This path need not exist on disk: `git
	// log -- <path>` reads reachable history, not the working tree.
	originPath := "Projects/" + cur + "/tasks/" + slug + ".md"
	d, derr := gitLogFirstDate(root, "--reverse", "--format=%ad", "--date=format:%Y-%m-%d", "--", originPath)
	if derr != nil {
		return "", "", derr
	}
	if d == "" {
		return "", "unknown — no git history at pre-move path", nil
	}
	hopDesc := "single-hop"
	if hops > 1 {
		hopDesc = fmt.Sprintf("%d-hop", hops)
	}
	return d, hopDesc + " tombstone-chase via " + strings.Join(chain[1:], " -> "), nil
}

// findTombstoneSource looks for exactly one thing: some OTHER project's
// tasks/cancelled/<slug>.md whose Title is precisely "Moved to <forProject>"
// — the exact, reproducible string MoveProvenance.TombstoneSpec renders, not
// a fuzzy guess. Multiple independent matches (two unrelated historical
// moves that happen to reuse the same slug and both name forProject as their
// destination) are resolved by taking the lexicographically first candidate
// project, matching this package's own established deterministic-iteration
// convention — this is a determinism tie-break, not a correctness claim
// about which match is "real"; both are equally valid, coincidental matches.
func findTombstoneSource(root string, allProjects []string, forProject, slug string) (sourceProject string, found bool, err error) {
	var candidates []string
	for _, p := range allProjects {
		if p == forProject {
			continue
		}
		path := filepath.Join(root, "Projects", p, "tasks", "cancelled", slug+".md")
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			if os.IsNotExist(rerr) {
				continue
			}
			return "", false, fmt.Errorf("read %s: %w", path, rerr)
		}
		meta := storage.ParseTaskMetaFromContent(slug, string(data), true)
		if meta.Title == "Moved to "+forProject {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return "", false, nil
	}
	sort.Strings(candidates)
	return candidates[0], true, nil
}

// gitLogFirstDate runs `git log <args...>` against root and returns the
// first line of output, trimmed — empty ("", nil) means git ran fine but the
// path has no matching history, never a fabricated date. args must include
// the log subcommand's own flags/pathspec (e.g. "-1", "--format=...",
// "--", "<path>"); this only supplies "-C <root> log".
func gitLogFirstDate(root string, args ...string) (string, error) {
	full := append([]string{"-C", root, "log"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = storage.SafeGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git log %s: %s: %w", strings.Join(args, " "), bytes.TrimSpace(out), err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line, nil
}
