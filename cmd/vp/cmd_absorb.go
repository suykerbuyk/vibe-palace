// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/absorb"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
)

var absorbFlags = []cli.FlagDef{
	{Name: "--dry-run", Help: "Print the routing plan without writing"},
	{Name: "--yes", Help: "Apply all proposed routes without prompting"},
	{Name: "--project", Arg: "SLUG", Help: "Override detected project slug"},
	{Name: "--project-root", Arg: "PATH", Help: "Override project root (default: cwd)"},
	{Name: "--no-stage", Help: "Skip `git add` on rewritten source files"},
}

func cmdAbsorb() *cli.Command {
	return &cli.Command{
		Name:        "absorb",
		Synopsis:    "vp absorb [--dry-run] [--yes] [--project SLUG] [--project-root PATH] [--no-stage]",
		Description: "Migrate legacy agent-context files (CLAUDE.md, AGENTS.md, .cursorrules, .rules, copilot instructions) into the palace vault. Sections are routed to resume/workflow/knowledge/doc files; resume-bound content is queued for manual merge. Source files are rewritten down to preamble + managed block, with backups under .vibe-palace/. Use --yes to accept every proposed route non-interactively; during interactive use, press A at any prompt to accept the rest.",
		Flags:       absorbFlags,
		Examples: []cli.Example{
			{Cmd: "vp absorb --dry-run", Comment: "Preview the routing plan"},
			{Cmd: "vp absorb", Comment: "Interactive absorption (per-section prompt)"},
			{Cmd: "vp absorb --yes", Comment: "Accept all proposed routes"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(absorbFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp absorb: %v\n", err)
				return cli.ExitUser
			}
			return runAbsorb(absorbOpts{
				DryRun:      fv.Bool("--dry-run"),
				Yes:         fv.Bool("--yes"),
				Project:     fv.Get("--project"),
				ProjectRoot: fv.Get("--project-root"),
				NoStage:     fv.Bool("--no-stage"),
				Stdin:       os.Stdin,
				Stdout:      os.Stdout,
				Stderr:      os.Stderr,
			})
		},
	}
}

type absorbOpts struct {
	DryRun      bool
	Yes         bool
	Project     string
	ProjectRoot string
	NoStage     bool
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
}

func runAbsorb(opts absorbOpts) int {
	projectRoot := opts.ProjectRoot
	if projectRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(opts.Stderr, "vp absorb: %v\n", err)
			return cli.ExitSystem
		}
		projectRoot = cwd
	}

	slug := opts.Project
	if slug == "" {
		detected, err := project.DetectProject(projectRoot)
		if err != nil {
			fmt.Fprintf(opts.Stderr, "vp absorb: detect project: %v\n", err)
			fmt.Fprintln(opts.Stderr, "  pass --project SLUG to override.")
			return cli.ExitUser
		}
		slug = detected
	}

	vault, err := openProjectVault()
	if err != nil {
		fmt.Fprintf(opts.Stderr, "vp absorb: %v\n", err)
		return cli.ExitUser
	}

	plan, err := absorb.BuildPlan(projectRoot)
	if err != nil {
		fmt.Fprintf(opts.Stderr, "vp absorb: build plan: %v\n", err)
		return cli.ExitSystem
	}

	if len(plan.Sources) == 0 {
		fmt.Fprintln(opts.Stdout, "No agent-context files detected — nothing to absorb.")
		return cli.ExitOK
	}
	if len(plan.Items) == 0 && allSourcesEmpty(plan) {
		fmt.Fprintln(opts.Stdout, "All detected agent-context files contain only a managed block — nothing to migrate.")
		return cli.ExitOK
	}

	printPlan(opts.Stdout, plan)

	if opts.DryRun {
		if len(plan.Items) > 0 {
			// Non-zero exit signals "manual review required" so scripts
			// can gate on dry-run output.
			return cli.ExitUser
		}
		return cli.ExitOK
	}

	// Per-section interactive pruning. Items removed here never reach
	// absorb.Apply. --yes accepts every item; pressing `A` at any prompt
	// accepts every remaining item.
	items := plan.Items
	if !opts.Yes {
		reader := bufio.NewReader(opts.Stdin)
		kept := items[:0]
		autoAccept := false
		for _, it := range items {
			printItemPrompt(opts.Stdout, it)
			if autoAccept {
				kept = append(kept, it)
				continue
			}
			choice, err := absorbPrompt(opts.Stdout, reader)
			if err != nil {
				fmt.Fprintf(opts.Stderr, "vp absorb: %v\n", err)
				return cli.ExitSystem
			}
			switch choice {
			case "y":
				kept = append(kept, it)
			case "a":
				kept = append(kept, it)
				autoAccept = true
			case "s":
				// Drop this item from the plan.
			case "q":
				fmt.Fprintln(opts.Stdout, "Aborting — no changes applied.")
				return cli.ExitUser
			}
		}
		plan.Items = kept
	}

	report, err := absorb.Apply(plan, absorb.WriteOptions{
		Vault:       vault,
		Project:     slug,
		ProjectRoot: projectRoot,
		Stage:       !opts.NoStage,
	})
	if err != nil {
		fmt.Fprintf(opts.Stderr, "vp absorb: apply: %v\n", err)
		return cli.ExitSystem
	}
	printReport(opts.Stdout, report)
	return cli.ExitOK
}

func allSourcesEmpty(plan *absorb.Plan) bool {
	for _, ps := range plan.Sources {
		if ps.Supported {
			return true
		}
	}
	return false
}

// absorbContract is the one-time header printed before the per-item plan.
// It states the file-level rewrite/backup contract once rather than
// repeating it per prompt. Keep copy in sync with the writer behavior in
// internal/absorb/writer.go.
const absorbContract = `Absorb will:
  1. Route every section listed below into the palace vault.
  2. Rewrite each source file down to its preamble plus the
     vibe-palace managed block. Original bodies are discarded from
     the source file but preserved in backups under .vibe-palace/.
  3. Stage rewritten source files with ` + "`git add`" + ` unless --no-stage.

Sections not listed (e.g. the managed block itself) are not migrated
but also not preserved in the source file — they are captured in
the backup only.
`

func printPlan(w io.Writer, plan *absorb.Plan) {
	fmt.Fprint(w, absorbContract)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Absorb plan:")
	fmt.Fprintln(w)
	for _, ps := range plan.Sources {
		if !ps.Supported {
			fmt.Fprintf(w, "  %s  (recognized but not yet supported — skipping)\n",
				ps.DisplayPath)
		}
	}
	if len(plan.Items) == 0 {
		fmt.Fprintln(w, "  (no sections to migrate)")
		return
	}
	for _, it := range plan.Items {
		head := it.Section.Heading
		if head == "" {
			head = "<preamble>"
		}
		fmt.Fprintf(w, "  %s:%d-%d  %-32s  →  %s",
			it.DisplayPath, it.Section.StartLine, it.Section.EndLine,
			truncate(head, 32), describeDest(it.Dest))
		if it.Reason != "" {
			fmt.Fprintf(w, "  (%s)", it.Reason)
		}
		fmt.Fprintln(w)
	}
}

func describeDest(d absorb.Destination) string {
	if d.IsZero() {
		return "KEEP"
	}
	if d.Scratch {
		return "absorbed/resume-suggestions.md  (manual merge into resume.md)"
	}
	if d.Section != "" {
		return fmt.Sprintf("%s  §%s", d.Path, d.Section)
	}
	return d.Path
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return s[:n-1] + "…"
}

// printItemPrompt renders one item header above the y/s/A/q prompt. Includes
// the source location, heading, destination, classification reason, and a
// short body preview so the user can decide without opening the source file.
func printItemPrompt(w io.Writer, it absorb.Item) {
	head := it.Section.Heading
	if head == "" {
		head = "<preamble>"
	}
	fmt.Fprintf(w, "\n%s:%d-%d  %q\n  → %s",
		it.DisplayPath, it.Section.StartLine, it.Section.EndLine,
		head, describeDest(it.Dest))
	if it.Reason != "" {
		fmt.Fprintf(w, "  (%s)", it.Reason)
	}
	fmt.Fprintln(w)
	for _, line := range bodyPreviewLines(it.Section.Body, 2, 240) {
		fmt.Fprintf(w, "  ┆ %s\n", line)
	}
}

// bodyPreviewLines returns up to maxLines trimmed, non-empty lines from body,
// capping the total displayed characters at maxChars. The last returned line
// gets a trailing ellipsis when truncated.
func bodyPreviewLines(body string, maxLines, maxChars int) []string {
	var out []string
	total := 0
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		remaining := maxChars - total
		if remaining <= 0 {
			break
		}
		if len(line) > remaining {
			if remaining > 1 {
				line = line[:remaining-1] + "…"
			} else {
				line = "…"
			}
			out = append(out, line)
			break
		}
		out = append(out, line)
		total += len(line)
		if len(out) >= maxLines {
			break
		}
	}
	return out
}

func absorbPrompt(w io.Writer, r *bufio.Reader) (string, error) {
	for i := 0; i < 5; i++ {
		fmt.Fprint(w, "Accept this route? [y]es / [s]kip / [A]ccept all / [q]uit: ")
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		line = strings.TrimSpace(strings.ToLower(line))
		switch line {
		case "y", "s", "a", "q":
			return line, nil
		}
		fmt.Fprintln(w, "Please answer y, s, A, or q.")
		if err == io.EOF {
			return "s", nil
		}
	}
	return "s", nil
}

func printReport(w io.Writer, r *absorb.WriteReport) {
	fmt.Fprintln(w)
	if len(r.VaultFilesCreated) > 0 {
		fmt.Fprintf(w, "Created: %s\n", strings.Join(r.VaultFilesCreated, ", "))
	}
	if len(r.VaultFilesAppended) > 0 {
		fmt.Fprintf(w, "Appended: %s\n", strings.Join(r.VaultFilesAppended, ", "))
	}
	if len(r.DuplicateSkipped) > 0 {
		fmt.Fprintf(w, "Skipped (already present): %d section(s)\n", len(r.DuplicateSkipped))
	}
	if r.ResumeScratchPath != "" {
		fmt.Fprintf(w, "Resume scratch: %s  ← review and merge into resume.md\n",
			r.ResumeScratchPath)
	}
	if len(r.PointerLines) > 0 {
		fmt.Fprintln(w, "Reference pointers queued in resume scratch (paste into resume.md):")
		for _, p := range r.PointerLines {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
	if len(r.SourceRewritten) > 0 {
		fmt.Fprintf(w, "Rewrote: %s\n", strings.Join(r.SourceRewritten, ", "))
	}
	if len(r.SourceBackedUp) > 0 {
		fmt.Fprintf(w, "Backups: %s\n", strings.Join(r.SourceBackedUp, ", "))
	}
	if len(r.UnsupportedSkipped) > 0 {
		fmt.Fprintf(w, "Unsupported (left untouched): %s\n",
			strings.Join(r.UnsupportedSkipped, ", "))
	}
}
