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

// `vp audit task-files` reports ARCHIVED task files that fail
// storage.ValidateWholeTaskFile. It REPORTS. It has no --apply, and it must never
// grow one.
//
// # Why it lives under `vp audit` and not `vp migrate`
//
// Every `vp migrate task-*` command is report-by-default with a write mode behind
// --apply. This command has no write mode and no path to one, so parking it beside
// them would invite exactly that flag. The placement IS the scope fence: the repair
// is a separate unit (repair-24-legacy-task-files-failing-whole-file-validation),
// gated on operator answers this command must not pre-empt.
//
// # Why it exists at all: it is DimTaskFileValidity's evidence command
//
// vaultaudit's registry requires every dimension to publish a shell command a
// reader can run instead of trusting the report. For this dimension a grep cannot
// be that command, in either direction: the validator is fence-aware across eight
// ordered rules, so a "## " inside a fence makes a bad file look good and a
// "**Status:**" inside a fence makes a good file look bad. An evidence command that
// lies is worse than one that is merely incomplete, so the honest option is a real
// reporter — the same shape EvidenceTaskPreamble already points at.
//
// # What agreeing with the dimension does and does NOT prove
//
// 🔴 READ THIS BEFORE TRUSTING THE DIFFERENTIAL. This command and the dimension
// call the SAME predicate, storage.ValidateWholeTaskFile. That is deliberate — one
// definition, two callers — so the two agreeing proves nothing whatever about the
// rules. What it proves is that the two ENUMERATIONS agree: this command walks
// Projects/*/tasks/{done,cancelled} directly with os.ReadDir, while the dimension
// goes through vault.ListAllProjects and TaskDoneDir/TaskCancelledDir. Project
// filtering, InProjects, symlinks and subdirectory handling are where those two can
// really diverge, and that divergence is what the differential catches. If anyone
// "simplifies" this walk to call ListAllProjects, the differential becomes one
// implementation quoting itself and reports confidence it has not earned.
//
// # Exit code: findings exit 0
//
// Ported from cmdAuditVault rather than invented: that command states the ruling —
// "a blocking auditor gets disabled the first time it is wrong at a bad moment, and
// a disabled auditor audits nothing." A malformed archived file is a finding in the
// report, loudly, not in the exit code. Only a failure to READ the corpus is
// non-zero, because that is the case where the report itself is untrustworthy.
var auditTaskFilesFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Limit the report to one project (default: every project in the vault)"},
}

// taskFileFailure is one archived file the validator refuses.
type taskFileFailure struct {
	Rel    string // vault-relative path
	Reason string // the validator's message, VERBATIM
}

// class is the roll-up label: the validator's message up to the first colon.
//
// 🔴 DERIVED FROM THE MESSAGE, NEVER A COPIED LIST. A hardcoded set of class names
// in this file would be a second definition of the validator's rules, and it would
// drift the first time a message is reworded — silently, because a stale label
// still prints. Splitting the message instead means a reworded rule RELABELS this
// roll-up rather than misfiling a file into a class it no longer belongs to.
//
// It deliberately collapses the three "malformed header block: …" variants into one
// label, which is how the repair task's own class table already names them. Nothing
// is lost: the per-file line, and the audit finding's Detail, both carry the full
// message.
func (f taskFileFailure) class() string {
	if i := strings.Index(f.Reason, ":"); i > 0 {
		return f.Reason[:i]
	}
	return f.Reason
}

// taskFileValidityReport is the whole result, returned so tests — and the
// evidence differential in particular — can assert on it without parsing stdout.
type taskFileValidityReport struct {
	Scanned  int
	Failures []taskFileFailure
	Unread   []string // files that could not be read; reported, never counted as valid
}

// runTaskFileValidityReport walks the ARCHIVED task directories and validates each
// file. It writes nothing, anywhere, ever.
//
// The walk is INDEPENDENT of vault.ListAllProjects on purpose — see the package
// comment above. It mirrors taskPreambleProjects' shape (cmd_migrate_task_preamble.go)
// rather than sharing it, because that helper enumerates the ACTIVE directory and
// this command is archived-only.
func runTaskFileValidityReport(root, only string) (taskFileValidityReport, error) {
	var rep taskFileValidityReport

	entries, err := os.ReadDir(filepath.Join(root, "Projects"))
	if err != nil {
		return rep, fmt.Errorf("enumerate projects: %w", err)
	}
	var projects []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if only != "" && e.Name() != only {
			continue
		}
		if _, serr := os.Stat(filepath.Join(root, "Projects", e.Name(), "tasks")); serr != nil {
			continue
		}
		projects = append(projects, e.Name())
	}
	sort.Strings(projects)

	for _, proj := range projects {
		// ARCHIVED ONLY. The active directory is deliberately absent: task-preamble's
		// PreambleSkippedNoH2 class already reports a missing H2 for active files, and
		// two dimensions reporting the same byte is what that disjointness idiom exists
		// to prevent. Adding "" to this list reintroduces the collision.
		for _, sub := range []string{"done", "cancelled"} {
			dir := filepath.Join(root, "Projects", proj, "tasks", sub)
			ents, rerr := os.ReadDir(dir)
			if rerr != nil {
				// A project with no done/ or cancelled/ is ordinary, not a defect.
				continue
			}
			for _, e := range ents {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
					continue
				}
				rel := "Projects/" + proj + "/tasks/" + sub + "/" + e.Name()
				rep.Scanned++
				data, ferr := os.ReadFile(filepath.Join(dir, e.Name()))
				if ferr != nil {
					// "I could not look here" is not "this is clean" — it is reported
					// as its own class, never silently counted as valid.
					rep.Unread = append(rep.Unread, rel)
					continue
				}
				if verr := storage.ValidateWholeTaskFile(string(data)); verr != nil {
					rep.Failures = append(rep.Failures, taskFileFailure{Rel: rel, Reason: verr.Error()})
				}
			}
		}
	}
	sort.Slice(rep.Failures, func(i, j int) bool { return rep.Failures[i].Rel < rep.Failures[j].Rel })
	sort.Strings(rep.Unread)
	return rep, nil
}

// printTaskFileValidityReport renders the report: the per-file list first, then the
// class roll-up.
//
// BOTH halves matter and neither replaces the other. The list is what a repairer
// acts on — a count is not re-derivable, a path is. The roll-up is what tells them
// WHICH of the validator's rules they are facing before they open anything, and on
// the live corpus it is the difference between a usable report and a wall: one class
// dominates the population, and an undifferentiated list hides that completely.
func printTaskFileValidityReport(out io.Writer, root string, rep taskFileValidityReport) {
	printVaultRoot(out, root)
	fmt.Fprintln(out, "Mode:  REPORT ONLY — this command never writes.")
	fmt.Fprintln(out)

	for _, f := range rep.Failures {
		fmt.Fprintf(out, "  INVALID  %s — %s\n", f.Rel, f.Reason)
	}
	for _, u := range rep.Unread {
		fmt.Fprintf(out, "  UNREAD   %s — could not be read; NOT counted as valid\n", u)
	}

	if len(rep.Failures) > 0 {
		fmt.Fprintln(out, "\nBy class (the validator's first failure — not necessarily the only one):")
		counts := map[string]int{}
		for _, f := range rep.Failures {
			counts[f.class()]++
		}
		labels := make([]string, 0, len(counts))
		for c := range counts {
			labels = append(labels, c)
		}
		// Most-populous first: the class a repairer should size up before the rest.
		sort.Slice(labels, func(i, j int) bool {
			if counts[labels[i]] != counts[labels[j]] {
				return counts[labels[i]] > counts[labels[j]]
			}
			return labels[i] < labels[j]
		})
		for _, c := range labels {
			fmt.Fprintf(out, "  %4d  %s\n", counts[c], c)
		}
	}

	fmt.Fprintf(out, "\n%d archived task file(s) scanned, %d invalid, %d unreadable.\n",
		rep.Scanned, len(rep.Failures), len(rep.Unread))
	if len(rep.Failures) > 0 {
		fmt.Fprintf(out, "These files hold the vault below data format %d until they are repaired; "+
			"the repair is a separate unit and this command is not it.\n", 1)
	}
}

func cmdAuditTaskFiles() *cli.Command {
	return &cli.Command{
		Name:     "audit task-files",
		Synopsis: "vp audit task-files [--project P]",
		Description: "Report ARCHIVED task files (tasks/done/, tasks/cancelled/) that fail the whole-file " +
			"task validator, with the validator's own first-failure message per file and a roll-up by " +
			"class. REPORT ONLY — it has no write mode. It is the evidence command for the " +
			"task-file-validity audit dimension, and it enumerates the corpus independently of that " +
			"dimension so the two agreeing is a real cross-check of the WALK rather than one " +
			"implementation quoting itself. The ACTIVE directory is deliberately out of scope: " +
			"task-preamble already reports a missing H2 there.",
		Flags: auditTaskFilesFlags,
		Examples: []cli.Example{
			{Cmd: "vp audit task-files", Comment: "Report every malformed archived task file in the vault"},
			{Cmd: "vp audit task-files -p vibe-palace", Comment: "Limit the report to one project"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(auditTaskFilesFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp audit task-files: %v\n", err)
				return cli.ExitUser
			}
			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp audit task-files: %v\n", err)
				return cli.ExitSystem
			}
			rep, err := runTaskFileValidityReport(vault.Root, fv.Get("--project"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp audit task-files: %v\n", err)
				return cli.ExitSystem
			}
			printTaskFileValidityReport(os.Stdout, vault.Root, rep)
			// Findings exit 0 — see the exit-code note at the top of this file. Only a
			// corpus this command could not read is a non-zero condition, and that is
			// handled above.
			return cli.ExitOK
		},
	}
}
