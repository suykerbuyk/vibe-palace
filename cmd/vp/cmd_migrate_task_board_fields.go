// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
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
			"renames the active-directory legacy \"pending\" status to \"planning\", backfills " +
			"CreateTime/ModTime from git history (with cross-project-move detection, including a " +
			"multi-hop tombstone chase), and stamps the per-file DataFormat marker.\n\n" +
			"PLAN-FIRST: the bare command REPORTS and writes nothing; pass --apply to write.\n\n" +
			"--apply runs two phases in order. Phase 1 folds in \"vp migrate task-status --apply\" " +
			"(the already-shipped archived retired/pending->done/cancelled repair) so a single " +
			"coordinated-window command handles both. If phase 1 reports any failure, phase 2 does " +
			"not run and the DataFormat marker is not stamped. Phase 2 walks every task file in " +
			"every project (active, done/, cancelled/): a file that already carries a CreateTime is " +
			"skipped entirely (already migrated); a done/cancelled candidate whose slug also exists " +
			"in the active directory is refused, matching phase 1's own shadow-slug guard, because " +
			"the underlying writer resolves active first.\n\n" +
			"A file with no derivable git history is left with an unknown CreateTime/ModTime but " +
			"still gets its Status rename and DataFormat stamp — never a fabricated date. The final " +
			"vault-wide DataFormat stamp (surface.WriteFormat) runs exactly once, at the very end, " +
			"ONLY if both phases report zero failures AND zero files skipped for uncommitted " +
			"changes: this is a one-time, non-repeating operation, so a Dirty-skipped file has no " +
			"scheduled path back to correctness the way it does for the everyday, re-runnable " +
			"\"migrate task-status\".\n\n" +
			"--apply requires the vault to be a git repo (both phases derive from and depend on git " +
			"history) and writes go through the locked, surface-stamping task writer, never the " +
			"generic vault file tools. --apply writes directly (no staging, no auto-commit) and " +
			"prints a combined rollback banner naming every path either phase wrote, so `git " +
			"checkout -- ...` undoes exactly this run.\n\n" +
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
			statusSum, fieldsSum, err := runTaskBoardFieldsMigration(root, fv.Get("--project"), fv.Bool("--apply"), os.Stdout)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-board-fields: %v\n", err)
				return cli.ExitSystem
			}
			if statusSum.Failed > 0 || fieldsSum.Failed > 0 {
				fmt.Fprintf(os.Stderr, "vp migrate task-board-fields: %d file(s) failed\n", statusSum.Failed+fieldsSum.Failed)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

// boardFieldsPlan is one task file's phase-2 decision, kept so a test can
// assert on the roll-up without re-parsing the printed report.
type boardFieldsPlan struct {
	Project string
	Slug    string
	Dir     string // "", "done", or "cancelled"

	StatusFrom string
	StatusTo   string // empty = unchanged (archived files, or already-valid active values)

	CreateTime string // empty = unknown (no history, or a cycle)
	CreateWhy  string // human-readable derivation source, or the unknown/cycle reason
	ModTime    string // empty = unknown (no history)
	DataFormat string

	AlreadyMigrated bool
	ShadowRefused   bool
	Applied         bool
	Failed          bool
	Skipped         bool // Dirty
}

// boardFieldsSummary is phase 2's roll-up, mirroring taskStatusSummary's
// shape for phase 1.
type boardFieldsSummary struct {
	Scanned         int
	AlreadyMigrated int
	ToMigrate       int
	Applied         int
	Failed          int
	Dirty           int
	UnknownDates    int // includes cycle-detected
	ShadowRefusals  int
	AppliedPaths    []string
	Plans           []boardFieldsPlan
}

// runTaskBoardFieldsMigration is the whole command, injectable for tests.
func runTaskBoardFieldsMigration(root, only string, apply bool, out io.Writer) (taskStatusSummary, boardFieldsSummary, error) {
	var statusSum taskStatusSummary
	var fieldsSum boardFieldsSummary

	if apply {
		if err := requireVaultGitRepo(root, "this migration derives CreateTime/ModTime from git history and "+
			"backfills header fields whose only other copy is that history"); err != nil {
			return statusSum, fieldsSum, err
		}
	}

	projects, err := taskPreambleProjects(root, only)
	if err != nil {
		return statusSum, fieldsSum, err
	}
	// The tombstone chase must be able to find a source project's tombstone
	// regardless of --project scoping: --project limits which files phase 2
	// MIGRATES, not which projects can hold a cross-project move's other half.
	allProjects, err := taskPreambleProjects(root, "")
	if err != nil {
		return statusSum, fieldsSum, err
	}

	printVaultRoot(out, root)
	if apply {
		fmt.Fprintln(out, "Mode:  APPLY — task files will be rewritten.")
	} else {
		fmt.Fprintln(out, "Mode:  REPORT ONLY — nothing is written. Pass --apply to write.")
	}

	fmt.Fprintln(out, "\n=== Phase 1: archived Status repair (vp migrate task-status) ===")
	statusSum, err = runTaskStatusMigration(root, only, apply, out)
	if err != nil {
		return statusSum, fieldsSum, err
	}
	if apply && statusSum.Failed > 0 {
		fmt.Fprintln(out, "\nPhase 1 reported failures — phase 2 will NOT run, and the DataFormat marker will NOT be stamped.")
		return statusSum, fieldsSum, nil
	}

	fmt.Fprintln(out, "\n=== Phase 2: Status rename + CreateTime/ModTime/DataFormat backfill ===")
	vault := storage.NewVault(root)
	dataFormat := strconv.Itoa(surface.RequiredDataFormat)

	// 🔴 A PATH PHASE 1 ITSELF JUST WROTE IS EXPECTED TO BE DIRTY, AND THAT IS
	// NOT THE HAZARD THE PER-FILE PRECONDITION EXISTS TO CATCH. Both phases
	// write directly with no staging and no auto-commit (Operator Decision 4),
	// so an archived file phase 1 just repaired is, by construction,
	// uncommitted the moment phase 2 reaches it — on every real run, not an
	// edge case. A naive per-file HasUncommittedChanges check can't tell that
	// dirt apart from a genuinely unrelated in-flight operator edit, so it
	// would skip phase 2's own backfill for every single file phase 1 touched,
	// defeating Operator Decision 1's entire point (one coordinated-window
	// command) on the very files it folded in. Since phase 1's write is OUR
	// OWN, from earlier in this same invocation, it is safe to build on: both
	// writes land in the same rollback banner and the same eventual human
	// commit, so nothing is silently mixed with work this run did not make.
	phase1Written := make(map[string]bool, len(statusSum.AppliedPaths))
	for _, p := range statusSum.AppliedPaths {
		phase1Written[p] = true
	}

	for _, proj := range projects {
		for _, sub := range []string{"", "done", "cancelled"} {
			dir := filepath.Join(root, "Projects", proj, "tasks", sub)
			entries, rerr := os.ReadDir(dir)
			if rerr != nil {
				// A project with no done/ or cancelled/ (or, degenerately, no
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
				slug := strings.TrimSuffix(name, ".md")
				relPath := boardFieldsRelPath(proj, sub, name)
				data, ferr := os.ReadFile(filepath.Join(root, filepath.FromSlash(relPath)))
				if ferr != nil {
					fmt.Fprintf(out, "  !!    %s/%s: read: %v\n", proj, slug, ferr)
					fieldsSum.Failed++
					continue
				}
				fieldsSum.Scanned++
				content := string(data)
				meta := storage.ParseTaskMetaFromContent(slug, content, sub != "")

				plan := boardFieldsPlan{Project: proj, Slug: slug, Dir: sub}

				// §6 idempotency: a non-empty CreateTime means either
				// server-stamped-at-creation (post-dates this feature) or
				// already migrated by an earlier partial run. Either way,
				// skip entirely — including the DataFormat re-stamp.
				if meta.CreateTime != "" {
					plan.AlreadyMigrated = true
					fieldsSum.AlreadyMigrated++
					fieldsSum.Plans = append(fieldsSum.Plans, plan)
					continue
				}

				// Shadow-slug guard: resolveTaskFile (and so
				// SetTaskMigrationFields) resolves active before done before
				// cancelled, so a done/cancelled candidate whose slug ALSO
				// exists in the active directory would silently write the
				// active file with this archived file's derived values.
				// Refuse rather than repair, exactly like phase 1's own
				// identical guard — no equivalent check is needed for an
				// active-directory candidate, since active always resolves
				// to itself first regardless of what else shares its slug.
				if sub != "" {
					active := filepath.Join(root, "Projects", proj, "tasks", name)
					if fileExists(active) {
						fmt.Fprintf(out, "  !!    %s/%s: also present in tasks/ (%s) — refusing, the writer resolves active first\n",
							proj, slug, boardFieldsRelPath(proj, "", name))
						plan.ShadowRefused = true
						plan.Failed = true
						fieldsSum.Failed++
						fieldsSum.ShadowRefusals++
						fieldsSum.Plans = append(fieldsSum.Plans, plan)
						continue
					}
				}

				// Status rename: active directory only, literal "pending"
				// (case-insensitive) -> "planning". Phase 1 already owns the
				// archived retired/pending->done/cancelled repair. Anything
				// else (already-valid, or genuinely free-text/legacy) is
				// left alone — never guessed at.
				plan.StatusFrom = meta.Status
				if sub == "" && strings.EqualFold(strings.TrimSpace(meta.Status), "pending") {
					plan.StatusTo = "planning"
				}

				createTime, createWhy, cerr := deriveCreateTime(root, allProjects, proj, slug, relPath)
				if cerr != nil {
					fmt.Fprintf(out, "  !!    %s/%s: derive CreateTime: %v\n", proj, slug, cerr)
					plan.Failed = true
					fieldsSum.Failed++
					fieldsSum.Plans = append(fieldsSum.Plans, plan)
					continue
				}
				plan.CreateTime = createTime
				plan.CreateWhy = createWhy
				if createTime == "" {
					fieldsSum.UnknownDates++
				}

				modTime, merr := deriveModTime(root, relPath)
				if merr != nil {
					fmt.Fprintf(out, "  !!    %s/%s: derive ModTime: %v\n", proj, slug, merr)
					plan.Failed = true
					fieldsSum.Failed++
					fieldsSum.Plans = append(fieldsSum.Plans, plan)
					continue
				}
				plan.ModTime = modTime
				plan.DataFormat = dataFormat

				fieldsSum.ToMigrate++
				fmt.Fprintf(out, "  FIX   %s/%s (%s/) — Status %q->%q  CreateTime=%s (%s)  ModTime=%s  DataFormat=%s\n",
					proj, slug, sub, plan.StatusFrom, orUnchanged(plan.StatusTo), orUnknown(createTime), createWhy, orUnknown(modTime), dataFormat)

				if apply {
					var dirty bool
					if !phase1Written[relPath] {
						var dierr error
						dirty, dierr = storage.HasUncommittedChanges(root, relPath)
						if dierr != nil {
							fmt.Fprintf(out, "  !!    %s/%s: git status: %v\n", proj, slug, dierr)
							plan.Failed = true
							fieldsSum.Failed++
							fieldsSum.Plans = append(fieldsSum.Plans, plan)
							continue
						}
					}
					if dirty {
						fmt.Fprintf(out, "  SKIP  %s/%s: uncommitted changes — this migration backfills "+
							"history-derived fields; commit or stash %s, then re-run\n", proj, slug, relPath)
						plan.Skipped = true
						fieldsSum.Dirty++
						fieldsSum.Plans = append(fieldsSum.Plans, plan)
						continue
					}
					if werr := vault.SetTaskMigrationFields(proj, slug, plan.StatusTo, createTime, modTime, dataFormat); werr != nil {
						fmt.Fprintf(out, "  !!    %s/%s: write: %v\n", proj, slug, werr)
						plan.Failed = true
						fieldsSum.Failed++
						fieldsSum.Plans = append(fieldsSum.Plans, plan)
						continue
					}
					plan.Applied = true
					fieldsSum.Applied++
					boardFieldsRecordWrite(&fieldsSum, root, relPath)
				}
				fieldsSum.Plans = append(fieldsSum.Plans, plan)
			}
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Phase 1: %d archived file(s) scanned, %d repaired, %d dirty.\n",
		statusSum.Scanned, statusSum.Applied, statusSum.Dirty)
	fmt.Fprintf(out, "Phase 2: %d task file(s) scanned, %d already migrated, %d to migrate, "+
		"%d with unknown dates (no git history or cycle), %d shadow-slug refusal(s).\n",
		fieldsSum.Scanned, fieldsSum.AlreadyMigrated, fieldsSum.ToMigrate, fieldsSum.UnknownDates, fieldsSum.ShadowRefusals)

	totalFailed := statusSum.Failed + fieldsSum.Failed
	totalDirty := statusSum.Dirty + fieldsSum.Dirty

	if apply {
		fmt.Fprintf(out, "Applied %d rewrite(s) in phase 2.\n", fieldsSum.Applied)
		switch {
		case totalFailed > 0:
			fmt.Fprintf(out, "%d file(s) FAILED across both phases; RequiredDataFormat was NOT advanced.\n", totalFailed)
		case totalDirty > 0:
			fmt.Fprintf(out, "%d file(s) skipped for uncommitted changes; RequiredDataFormat was NOT advanced — "+
				"commit or stash them and re-run --apply.\n", totalDirty)
		default:
			if werr := surface.WriteFormat(root, surface.RequiredDataFormat); werr != nil {
				return statusSum, fieldsSum, fmt.Errorf("stamp vault data format: %w", werr)
			}
			fmt.Fprintf(out, "RequiredDataFormat stamped at %d.\n", surface.RequiredDataFormat)
		}
	} else if fieldsSum.ToMigrate > 0 || statusSum.Fix > 0 {
		fmt.Fprintln(out, "Nothing was written. Re-run with --apply to write.")
	}

	// A path phase 1 wrote and phase 2 ALSO wrote (the phase1Written
	// interaction above) is a real, separate write from each phase, but it
	// is still one path on disk — de-duplicated here so the rollback banner
	// (and the `git checkout --` argument list it prints) names each path
	// exactly once.
	seen := make(map[string]bool, len(statusSum.AppliedPaths)+len(fieldsSum.AppliedPaths))
	allPaths := make([]string, 0, len(statusSum.AppliedPaths)+len(fieldsSum.AppliedPaths))
	for _, p := range statusSum.AppliedPaths {
		if !seen[p] {
			seen[p] = true
			allPaths = append(allPaths, p)
		}
	}
	for _, p := range fieldsSum.AppliedPaths {
		if !seen[p] {
			seen[p] = true
			allPaths = append(allPaths, p)
		}
	}
	boardFieldsRollbackBanner(out, root, allPaths)

	return statusSum, fieldsSum, nil
}

// orUnknown renders an empty derived date as "unknown" for the report line.
func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// orUnchanged renders an empty target Status as "unchanged" for the report line.
func orUnchanged(s string) string {
	if s == "" {
		return "unchanged"
	}
	return s
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
func boardFieldsRecordWrite(sum *boardFieldsSummary, root, rel string) {
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
