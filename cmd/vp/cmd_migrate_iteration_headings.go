// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// The one-shot repair of the live iterations.md archives.
//
// # Why a CLI one-shot and not an MCP tool
//
// This runs ONCE, against a specific archive, under a human who reads the report
// before and after. An MCP tool would put "rewrite the history file" on the
// surface an agent reaches unprompted, forever, to close a defect that exists
// once — and every agent-reachable writer is a writer that eventually fires when
// nobody is looking. The check producer (`vp check --check iteration-headings`)
// is the permanent, READ-ONLY half; this is the deliberate, human-driven half.
//
// # Plan-first
//
// The bare command is a report and mutates nothing; --apply writes. That is the
// same posture as `vp migrate kg-filenames`, and for the same reason: the
// interesting output of this command is the REFUSAL LIST, which a human has to
// read and act on, and a command whose default is "write" gets its report
// skimmed after the fact instead of read before it.

var migrateIterationHeadingsFlags = []cli.FlagDef{
	{Name: "--vault", Arg: "PATH", Help: "Vault root to scan and repair (default: the configured vault_path)"},
	{Name: "--apply", Help: "WRITE the repairs. Without this the command only reports."},
}

// cmdMigrateIterationHeadings repairs the iterations.md headings that do not
// match the header FormatIterationHeader would have emitted.
func cmdMigrateIterationHeadings() *cli.Command {
	return &cli.Command{
		Name:     "migrate iteration-headings",
		Synopsis: "vp migrate iteration-headings [--vault PATH] [--apply]",
		Description: "Repair the iteration-narrative headings in every project's iterations.md so each " +
			"one is the header the writer would have emitted (\"## Iteration N — Title\").\n\n" +
			"PLAN-FIRST: the bare command REPORTS and writes nothing; pass --apply to write.\n\n" +
			"Three defect classes are repaired, and in every case the NUMBER IS NEVER DERIVED. A " +
			"heading that already carries its number is normalised in place (the separator is " +
			"consumed, an infix is moved into the title, a doubled \"Iteration N —\" prefix is " +
			"stripped). A heading that carries NO number is repaired only if the operator " +
			"explicitly assigned one for that exact (project, line, heading text) — and the recorded " +
			"text must still match byte-for-byte, or the row is refused rather than written onto " +
			"drifted content.\n\n" +
			"Everything else is REFUSED and listed for a human: a framed narrative with no number and " +
			"no operator assignment, and a numbered-but-titleless heading (no placeholder title is " +
			"ever written). Refusals are the designed output, not a failure.\n\n" +
			"Only heading LINES are rewritten; every other byte of the archive is left identical. " +
			"Under --apply the vault git working tree must be clean, so `git checkout .` is a " +
			"guaranteed rollback, and each file is written through the locked, surface-stamping " +
			"vault write path with a compare-and-set guard against the bytes that were planned.",
		Flags: migrateIterationHeadingsFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate iteration-headings", Comment: "Report what would change and what is refused; writes nothing"},
			{Cmd: "vp migrate iteration-headings --apply", Comment: "Apply the repairs to the configured vault"},
			{Cmd: "vp migrate iteration-headings --vault /tmp/scratchvault --apply", Comment: "Apply against an explicit vault root (a scratch copy)"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateIterationHeadingsFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate iteration-headings: %v\n", err)
				return cli.ExitUser
			}
			root, err := resolveMigrationVaultRoot(fv.Get("--vault"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate iteration-headings: %v\n", err)
				return cli.ExitUser
			}
			if _, err := runIterationHeadingMigration(root, fv.Bool("--apply"), os.Stderr); err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate iteration-headings: %v\n", err)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

// resolveMigrationVaultRoot returns the vault root a migration will operate on:
// the --vault value when given (tilde-expanded and made absolute), otherwise the
// configured vault.
//
// --vault exists so the repair can be rehearsed against a THROWAWAY COPY of the
// archives before it is pointed at the real ones. That rehearsal is the only way
// to prove "heading-only diff" on real data, and a command that can only ever
// address the live vault cannot be rehearsed at all.
func resolveMigrationVaultRoot(flagValue string) (string, error) {
	if strings.TrimSpace(flagValue) != "" {
		return expandAndAbsPath(flagValue)
	}
	vault, err := openProjectVault()
	if err != nil {
		return "", fmt.Errorf("open vault: %w", err)
	}
	if vault.Root == "" {
		return "", fmt.Errorf("no vault configured; pass --vault PATH")
	}
	return vault.Root, nil
}

// refusedHeading is one refusal with the archive it came from, so the closing
// action list can be read on its own without scrolling back to find which
// project a line number belongs to.
type refusedHeading struct {
	Rel     string
	Refusal wrapstate.HeadingRefusal
}

// iterationHeadingMigrationSummary is the roll-up a caller (and a test) can
// assert on without re-parsing the printed report.
type iterationHeadingMigrationSummary struct {
	Projects int // archives scanned
	Rewrites int // heading lines rewritten (or that would be)
	Refusals int // defective headings deliberately left alone
	Written  int // files actually written (0 unless --apply)
	ByClass  map[wrapstate.HeadingDefectClass]int
	BySource map[wrapstate.RewriteSource]int
	Refused  []refusedHeading
}

// runIterationHeadingMigration plans every archive under root, prints the
// report to out, and — when apply is set — writes the repaired files.
//
// # The write path, and the two ways it could have been wrong
//
// Each file is written with vaultfs.Write, which takes the per-path vaultlock,
// runs the placeholder write-back guard, and routes through atomicfile.Write so
// the .surface stamp is recorded. Two things about that are deliberate:
//
//   - The plan is computed from an UNLOCKED read, and the sha256 of exactly
//     those bytes is handed back as the compare-and-set guard. If anything
//     changed the file between the read and the write, the write is REFUSED
//     loudly instead of pasting a plan computed against a file that no longer
//     exists. A migration cannot hold a lock across its own report — a human
//     reads the report — so CAS, not a longer lock, is the right instrument.
//   - Nothing here calls a locked write while holding that path's lock.
//     vaultlock.Acquire is a blocking LOCK_EX with no timeout (ADR-003), so
//     re-entry is a permanent hang rather than an error: the failure mode is a
//     process that never returns and logs nothing. vaultfs.Write owns the lock
//     for the whole of its own read-compare-write and this function holds none.
func runIterationHeadingMigration(root string, apply bool, out io.Writer) (iterationHeadingMigrationSummary, error) {
	sum := iterationHeadingMigrationSummary{
		ByClass:  make(map[wrapstate.HeadingDefectClass]int),
		BySource: make(map[wrapstate.RewriteSource]int),
	}

	projects, err := iterationArchiveProjects(root)
	if err != nil {
		return sum, err
	}

	if apply {
		if err := requireCleanVaultTree(root); err != nil {
			return sum, err
		}
	}

	fmt.Fprintf(out, "Vault: %s\n", root)
	if apply {
		fmt.Fprintln(out, "Mode:  APPLY — heading lines will be rewritten.")
	} else {
		fmt.Fprintln(out, "Mode:  REPORT ONLY — nothing is written. Pass --apply to write.")
	}
	fmt.Fprintln(out)

	seen := make(map[string]bool, len(projects))
	for _, slug := range projects {
		rel := filepath.Join("Projects", slug, "iterations.md")
		data, rerr := os.ReadFile(filepath.Join(root, rel))
		if rerr != nil {
			// A project with no narrative archive is normal, not a defect.
			continue
		}
		seen[slug] = true
		sum.Projects++
		content := string(data)
		plan := wrapstate.PlanHeadingMigration(slug, content)
		// A project the operator ruled on keeps its block forever, even once it
		// is fully migrated. Those rows are the audit trail for nine numbers a
		// human assigned by hand, and "the archive still reads the way I said it
		// should" is only visible if the run says so — a report that goes silent
		// on success cannot distinguish "still correct" from "no longer checked".
		if plan.Empty() && len(plan.Refusals) == 0 && len(plan.Authorized) == 0 {
			continue
		}

		fmt.Fprintf(out, "%s — %d to rewrite, %d refused\n", rel, len(plan.Rewrites), len(plan.Refusals))
		for _, r := range plan.Rewrites {
			sum.Rewrites++
			sum.ByClass[r.Class]++
			sum.BySource[r.Source]++
			fmt.Fprintf(out, "  L%-6d [%s | %s]\n      - %s\n      + %s\n", r.Line, r.Class, r.Source, r.From, r.To)
		}
		for _, r := range plan.Refusals {
			sum.Refusals++
			sum.Refused = append(sum.Refused, refusedHeading{Rel: rel, Refusal: r})
			fmt.Fprintf(out, "  L%-6d REFUSED [%s] %s\n      %s\n", r.Line, r.Class, r.Text, r.Reason)
		}
		for _, a := range plan.Authorized {
			fmt.Fprintf(out, "  operator row %d @L%d: %s (found: %s)\n", a.Row.N, a.Row.Line, a.State, a.Found)
		}
		fmt.Fprintln(out)

		if !apply || plan.Empty() {
			continue
		}
		updated, aerr := plan.Apply(content)
		if aerr != nil {
			return sum, aerr
		}
		digest := sha256.Sum256(data)
		if _, werr := vaultfs.Write(root, rel, updated, hex.EncodeToString(digest[:])); werr != nil {
			return sum, fmt.Errorf("write %s: %w", rel, werr)
		}
		sum.Written++
	}

	reportUnreachedAuthorizedRows(out, seen)
	printIterationHeadingSummary(out, sum, apply)
	return sum, nil
}

// reportUnreachedAuthorizedRows names every operator assignment whose PROJECT
// was not scanned at all.
//
// The per-project loop can only report on archives it opened, so a row naming a
// project that is absent from this vault — a partial clone, a renamed slug, a
// project not yet synced — would otherwise vanish from the report entirely. That
// is the silent half of the failure: the run prints "rewrote 13" and looks
// finished while nine narratives that a human deliberately ruled on are still
// unaddressable, and nothing on screen says so.
func reportUnreachedAuthorizedRows(out io.Writer, seen map[string]bool) {
	var missing []wrapstate.AuthorizedAssignment
	for _, a := range wrapstate.AuthorizedAssignments() {
		if !seen[a.Project] {
			missing = append(missing, a)
		}
	}
	if len(missing) == 0 {
		return
	}
	fmt.Fprintf(out, "%d operator assignment(s) name a project with no iterations.md in this vault:\n", len(missing))
	for _, a := range missing {
		fmt.Fprintf(out, "  %s @L%d -> Iteration %d (%s)\n", a.Project, a.Line, a.N, a.Text)
	}
	fmt.Fprintln(out)
}

// printIterationHeadingSummary renders the per-class roll-up and the standing
// warning about what the refusals mean.
func printIterationHeadingSummary(out io.Writer, sum iterationHeadingMigrationSummary, apply bool) {
	verb := "would rewrite"
	if apply {
		verb = "rewrote"
	}
	fmt.Fprintf(out, "Scanned %d archives; %s %d heading lines, refused %d.\n", sum.Projects, verb, sum.Rewrites, sum.Refusals)
	for _, c := range []wrapstate.HeadingDefectClass{
		wrapstate.DefectFrameOrphan, wrapstate.DefectNonCanonicalNumbered, wrapstate.DefectDoubledPrefix,
	} {
		fmt.Fprintf(out, "  %-24s %d\n", c, sum.ByClass[c])
	}
	fmt.Fprintf(out, "  number recovered from heading: %d\n", sum.BySource[wrapstate.SourceRecovered])
	fmt.Fprintf(out, "  number operator-authorized   : %d\n", sum.BySource[wrapstate.SourceAuthorized])
	if apply {
		fmt.Fprintf(out, "  files written                : %d\n", sum.Written)
	}
	if sum.Refusals > 0 {
		// The refusals are ALSO listed per project above, in the context that
		// explains them. They are repeated here as one flat action list because
		// that is what a human actually leaves with: the report can run to
		// hundreds of lines, and a to-do split across five project blocks is a
		// to-do that gets half-done.
		fmt.Fprintln(out, "\nUnresolved headings, in one list:")
		for _, r := range sum.Refused {
			fmt.Fprintf(out, "  %s:%d  %s\n", r.Rel, r.Refusal.Line, r.Refusal.Text)
		}
		fmt.Fprintf(out, "\n%d heading(s) were REFUSED and are unchanged. Each needs a human decision:\n"+
			"an iteration number derived from file position, from a numeric gap, or from the\n"+
			"neighbouring entries would be a guess that nothing downstream could ever tell\n"+
			"apart from a number you chose. A titleless heading gets no placeholder title for\n"+
			"the same reason. Assign these by hand, then re-run.\n", sum.Refusals)
	}
}

// iterationArchiveProjects lists the project slugs under {root}/Projects in
// sorted order, skipping dot- and underscore-prefixed dirs (the same exclusion
// check.CheckIterationHeadings applies, so the two surfaces scan the same set).
func iterationArchiveProjects(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "Projects"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan Projects/: %w", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// requireCleanVaultTree refuses an --apply run unless the vault is a git repo
// with a clean working tree.
//
// This rewrites the project's own narrative history — the one file in the vault
// with no other copy — so the cheapest possible undo has to exist BEFORE the
// first byte is written. On a clean tree that undo is `git checkout .`; on a
// dirty one it is "pick my migration's changes out of yours by hand", which is
// not an undo. Same gate, same reasoning, as `vp migrate kg-filenames`.
func requireCleanVaultTree(root string) error {
	if !storage.GitAvailable() || !storage.GitIsRepo(root) {
		return fmt.Errorf("vault at %s is not a git repository; --apply requires git so `git checkout .` is a guaranteed rollback", root)
	}
	clean, err := storage.GitStatusClean(root)
	if err != nil {
		return fmt.Errorf("check vault git status: %w", err)
	}
	if !clean {
		return fmt.Errorf("vault git working tree at %s is dirty; commit or stash first so `git checkout .` cleanly rolls back the rewrite", root)
	}
	return nil
}
