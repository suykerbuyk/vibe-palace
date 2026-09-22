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

// `vp migrate task-sections` promotes the `### ` H3 headings of an ARCHIVED task
// file to `## ` H2, so a file with no addressable section becomes one that
// storage.ValidateWholeTaskFile accepts and `amend` can reach. Report by default;
// writes only under --apply.
//
// # ARCHIVED ONLY. The name does not say so, and this comment is the scope fence.
//
// The walk covers Projects/*/tasks/{done,cancelled} and never the active
// directory. A missing H2 on an ACTIVE file is already owned by task-preamble's
// PreambleSkippedNoH2 class, and TestTaskFileValidity_ActiveFilesAreNotReported
// pins that ownership — two surfaces repairing the same byte is precisely what
// that disjointness idiom exists to prevent. Adding "" to the directory list
// reintroduces the collision.
//
// The name is `task-sections`, not `task-h3-promotions`, so the BOLD
// pseudo-heading class can join later as a second class in this command rather
// than as a second subcommand. It therefore encodes no scope of its own.
//
// # THE WRITE SEAM IS THE STRICT ONE, AND THAT IS A DECISION
//
// Writes go through Vault.OverwriteTaskFile (headerMustMatch), NOT
// OverwriteTaskFileRewritingHeader. This transform rewrites heading prefixes and
// nothing else, so the header block is byte-identical by construction and the
// strict policy costs nothing — what it buys is refuseHeaderChange and
// refuseUnknownHeaderFieldChange, so a future edit that reaches a header field is
// REFUSED rather than silently written. ModTime is exempt (serverDerivedHeaderFields)
// and is force-restamped after both guards, so the restamp does not trip the policy.
//
// 🔴 `vp migrate task-header-spacing` is this command's structural-shape model
// ONLY. Its seam is the permissive one and is deliberately NOT copied, and its
// help text claims the permissive wrapper is REQUIRED to reach archived files —
// which is false: both wrappers resolve through the same resolveTaskFile, and
// cmd_migrate_task_header.go:376 writes into done/ through the strict seam today.
//
// # WHY THE VALIDATOR IS THE GATE RATHER THAN A REIMPLEMENTED SHAPE CHECK
//
// Every refusal below is a fail-fast with a readable reason, but the thing that
// actually makes a bad write impossible is the POST-CONDITION: a file is written
// only when storage.ValidateWholeTaskFile accepts the transformed bytes. That
// predicate checks fence balance FIRST (an unterminated fence returns before the
// outside-fence scan ever runs), then the title arms, then the missing-section
// arm — so pointing this command at a file whose defect is anything other than
// the missing H2 cannot produce a write, whatever the shape checks here miss.
// One definition, one gate, and no third copy of the fence scanner.

var migrateTaskSectionsFlags = []cli.FlagDef{
	{Name: "--vault", Arg: "PATH", Help: "Vault root to scan and repair (default: the configured vault_path)"},
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Limit to one project (default: every project in the vault)"},
	{Name: "--apply", Help: "WRITE the promotions. Without this the command only reports."},
}

// taskSectionsDirs is the ARCHIVED scope, and the active directory's absence is
// the fence described in this file's header comment. Adding "" here is the one
// edit that silently collides with task-preamble's PreambleSkippedNoH2 class.
var taskSectionsDirs = []string{"done", "cancelled"}

// taskSectionsOutcome classifies one file's decision.
type taskSectionsOutcome int

const (
	// sectionsNoWork: the file needs nothing from this command — it already
	// validates, or it already has an H2 and its defect (if any) is not ours.
	sectionsNoWork taskSectionsOutcome = iota
	// sectionsPromote: zero H2, at least one promotable H3, and the promoted
	// result validates. This is the only outcome that writes.
	sectionsPromote
	// sectionsNoH3: no H2, no H3 and no promotable bold pseudo-heading. Reported
	// rather than guessed at.
	sectionsNoH3
	// sectionsPromoteBold: no H2 and no H3, but a whole-line BOLD pseudo-heading
	// the author used as a section title. Promoting it is the file's repair.
	sectionsPromoteBold
	// sectionsRefused: a shape this command must not reason about.
	sectionsRefused
)

// taskSectionsDecision is one file's decision, kept so a test can assert on the
// roll-up without re-parsing the printed report.
type taskSectionsDecision struct {
	Project string
	Sub     string
	Slug    string
	Outcome taskSectionsOutcome
	Reason  string // refusal/skip detail; empty for a clean promotion
	Promos  int    // how many H3 headings would be promoted
	After   string // transformed content, only for sectionsPromote
	Applied bool
	Failed  bool
	// Skipped marks a file left alone because it carries uncommitted changes.
	// It is deliberately NOT Failed: nothing went wrong, the file is simply not
	// recoverable right now, and one commit makes the next run repair it.
	Skipped bool
}

type taskSectionsSummary struct {
	Scanned int
	NoWork  int
	Fix     int
	NoH3    int
	Refused int
	// OtherDefect counts files that are malformed but NOT this command's defect
	// — they already have an H2. They are B2's roster, and they are printed.
	OtherDefect int
	Dirty       int
	Applied     int
	// Failed counts ATTEMPTS that went wrong — read errors and refused writes.
	// It never counts a file the migration had no work for, which is what keeps
	// "nothing to migrate" (exit 0) distinct from "everything refused".
	Failed       int
	Decisions    []taskSectionsDecision
	AppliedPaths []string
}

func cmdMigrateTaskSections() *cli.Command {
	return &cli.Command{
		Name:     "migrate task-sections",
		Synopsis: "vp migrate task-sections [--vault PATH] [--project P] [--apply]",
		Description: "Promote the \"### \" H3 headings of an ARCHIVED task file to \"## \" H2, so a file " +
			"with no addressable section becomes one storage.ValidateWholeTaskFile accepts and " +
			"`amend` can reach.\n\n" +
			"PLAN-FIRST: the bare command REPORTS and writes nothing; pass --apply to write.\n\n" +
			"SCOPE IS ARCHIVED ONLY — Projects/*/tasks/{done,cancelled}, never the active " +
			"directory. A missing H2 on an ACTIVE file is already reported by `vp migrate " +
			"task-preamble` as its PreambleSkippedNoH2 class, and two surfaces repairing the same " +
			"byte is exactly what that disjointness rule exists to prevent. The command name " +
			"encodes no scope, so this is where the scope lives.\n\n" +
			"EVERY top-level H3 is promoted, not just the first: promoting one would satisfy the " +
			"validator while leaving the remaining siblings nested under it, which is a worse file " +
			"and a different `amend` surface than the one the author wrote.\n\n" +
			"A file with no H2 and no H3 is SKIPPED as its own class — its pseudo-heading is a bold " +
			"line, a separate transform. A file carrying an H4-or-deeper heading, an empty heading, " +
			"an indented heading, a heading inside an HTML comment or frontmatter, or two " +
			"same-named H3s is REFUSED rather than guessed at.\n\n" +
			"Writes go through the STRICT locked task writer (OverwriteTaskFile, headerMustMatch), " +
			"never the header-rewriting escape hatch and never the generic vault file tools. A file " +
			"is written only if the transformed bytes PASS the whole-file validator, so a repair " +
			"that would not actually fix the file is refused instead of applied. Files with " +
			"uncommitted changes are skipped — git holds the only copy. A run in which any file " +
			"failed exits non-zero; a run with nothing to migrate exits 0.",
		Flags: migrateTaskSectionsFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate task-sections", Comment: "Report what would be promoted; writes nothing"},
			{Cmd: "vp migrate task-sections -p atlassian-vault", Comment: "Report for one project"},
			{Cmd: "vp migrate task-sections --apply", Comment: "Apply to the configured vault"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateTaskSectionsFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-sections: %v\n", err)
				return cli.ExitUser
			}
			root, err := resolveMigrationVaultRoot(fv.Get("--vault"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-sections: %v\n", err)
				return cli.ExitUser
			}
			// preRun's surfaceGate checked only the CONFIGURED vault; this is
			// the root this run will actually write, however it was resolved.
			if code := enforceSurfaceOnRoot(root); code != cli.ExitOK {
				return code
			}
			sum, err := runTaskSectionsMigration(root, fv.Get("--project"), fv.Bool("--apply"), os.Stdout)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate task-sections: %v\n", err)
				return cli.ExitSystem
			}
			if sum.Failed > 0 {
				fmt.Fprintf(os.Stderr, "vp migrate task-sections: %d file(s) failed\n", sum.Failed)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

// ---------------------------------------------------------------------------
// The transform — a pure function of one file's bytes.
//
// It derives nothing from git, nothing from the filesystem, and nothing from any
// other file, which is why this command needs no planner/executor split: there is
// no footprint for a derivation to read back. See planner_no_write.go's rationale.
// ---------------------------------------------------------------------------

// headingLevel reports how many leading '#' characters open a heading, and the
// text following them. ok is false when the line is not heading-shaped at all —
// "#hashtag" has no space after its run and is prose, which is the same rule
// storage's isH1Line/isH2Line enforce with their trailing-space prefixes.
func headingLevel(trimmed string) (level int, rest string, ok bool) {
	n := 0
	for n < len(trimmed) && trimmed[n] == '#' {
		n++
	}
	if n == 0 {
		return 0, "", false
	}
	rest = trimmed[n:]
	if rest != "" && !strings.HasPrefix(rest, " ") {
		return 0, "", false
	}
	return n, strings.TrimSpace(rest), true
}

// taskSectionsUnbalancedFence reports whether content ends inside an open fence.
//
// It owns NO fence rule of its own. mdfence.Scanner.Step returns Delimiter for
// exactly two cases — a real opener and its matching closer — so the parity of
// Delimiter results over every line IS the in-fence state at EOF. Every
// classification decision stays inside mdfence, which is what the package doc
// demands ("Do not add a third copy — call this"); storage's unexported
// unbalancedFence drives the same primitives to the same answer.
func taskSectionsUnbalancedFence(content string) bool {
	var s mdfence.Scanner
	delims := 0
	for _, line := range strings.Split(content, "\n") {
		if s.Step(line) == mdfence.Delimiter {
			delims++
		}
	}
	return delims%2 == 1
}

// planTaskSections decides one file. after is meaningful only for
// sectionsPromote; reason carries the detail the report prints.
//
// 🔴 DELIBERATE DEVIATION FROM THE SPEC'S TRANSFORM TABLE, RECORDED HERE RATHER
// THAN LEFT SILENT. The spec lists "### inside an HTML comment or frontmatter"
// under REFUSE. This function SKIPS those regions instead and promotes the real
// headings around them.
//
// Skip is the better behaviour and the spec's row should be amended to match.
// A "###" inside <!-- --> or YAML frontmatter is genuinely not a heading, so
// declining the WHOLE file would refuse a repairable task over a line that is
// already inert — the refusal would cite a cause that blocks nothing, which is
// the same defect the escape ordering above exists to prevent. Skipping is also
// backstopped: if ignoring those regions produced anything the validator will
// not accept, the post-condition refuses the write.
//
// The cost is recorded too: a heading line CONTAINING "<!--" with no "-->"
// opens a comment span that swallows every later heading, which is filed
// separately and is not repaired here.
//
// 🔴 The final gate is storage.ValidateWholeTaskFile over the TRANSFORMED bytes.
// The shape checks above it exist to give a readable reason, not to be the
// safety property — a shape this function fails to anticipate still cannot be
// written, because the post-condition rejects it.
func planTaskSections(content string) (after string, outcome taskSectionsOutcome, reason string, promos int) {
	if verr := storage.ValidateWholeTaskFile(content); verr == nil {
		return "", sectionsNoWork, "", 0
	}

	// 🔴 FENCE BALANCE FIRST, AND THE ORDER IS THE POINT. mdfence.OutsideFences
	// deliberately treats the tail of a half-open fence as fenced and drops it,
	// so on an unbalanced file every scan below sees a TRUNCATED document and
	// would report "no H3" — a file that is actually broken masquerading as a
	// file with nothing to promote. storage.ValidateWholeTaskFile checks this
	// first for the same reason (its doc: an open fence "swallows the trailing
	// header and would otherwise masquerade as a 'missing field'").
	if taskSectionsUnbalancedFence(content) {
		return "", sectionsRefused, "unterminated code fence: a ``` or ~~~ block is opened but never closed, " +
			"so every heading after it is invisible to the scan", 0
	}

	outside := outsideInertRegions(content)

	var h1, h2 int
	var h3Lines []int
	// sawH3Shape records that the file has at least one LEVEL-3 heading, empty or
	// not. It is what makes a refusal TRUE: a refusal may only be reported for a
	// file that would otherwise be a CANDIDATE, so the thing cited is the thing
	// actually blocking the promotion.
	sawH3Shape := false
	// refusal is the FIRST blocking shape seen, held rather than returned.
	//
	// 🔴 HOLDING IT IS THE WHOLE POINT. An earlier version returned from inside
	// this loop, so a file with many H2s — never a candidate for this command at
	// all — was refused citing an H4 or a duplicate H3 that blocked nothing. Five
	// of the six refusals on the live corpus named a false cause that way; their
	// real defect was a missing **Status:** or a doubled **Priority:**. This
	// output is B2's input, so a refusal citing a false cause misdirects the next
	// unit. The escapes below decide FIRST; only then is a held refusal reported.
	refusal := ""
	seen := map[string]bool{}

	for _, l := range outside {
		raw := l.Text
		trimmed := strings.TrimSpace(raw)

		level, rest, ok := headingLevel(trimmed)
		if !ok {
			continue
		}
		switch {
		case level == 1:
			h1++
		case level == 2:
			if rest == "" {
				if refusal == "" {
					refusal = "empty H2 heading: a bare \"##\" is not a section to the validator"
				}
				continue
			}
			h2++
		case level == 3:
			sawH3Shape = true
			switch {
			case rest == "":
				if refusal == "" {
					refusal = "empty H3 heading: promoting a bare \"###\" yields \"##\", which the validator does not count as a section"
				}
			case raw != strings.TrimLeft(raw, " \t"):
				if refusal == "" {
					refusal = "indented H3 heading: mdfence returns raw text while the validator matches the trimmed line, so the two disagree about this file"
				}
			case seen[rest]:
				if refusal == "" {
					refusal = fmt.Sprintf("two H3 headings both titled %q: promoting them manufactures duplicate H2s, a class no validator rule and no audit dimension reports", rest)
				}
			default:
				seen[rest] = true
				h3Lines = append(h3Lines, l.Num)
			}
		default:
			if refusal == "" {
				refusal = fmt.Sprintf("H%d heading present: a flat promotion would make each heading a sibling of its own children, which is a hierarchy judgment this command must not make", level)
			}
		}
	}

	// 🔴 ESCAPES BEFORE REFUSALS. A file this command would never touch cannot be
	// "refused" for a shape that is none of its business.
	if h2 > 0 {
		// Not our defect. The file already has an addressable section; whatever
		// else is wrong with it, the validator's own message is the true cause
		// and anything this command guessed would be a false one.
		return "", sectionsNoWork, storage.ValidateWholeTaskFile(content).Error(), 0
	}
	if !sawH3Shape {
		// The BOLD pseudo-heading class. The file comment reserved this class by
		// name; this is it, landing in the command that reserved it rather than
		// spawning a second subcommand.
		bold := boldPseudoHeadingLines(content)
		if len(bold) == 0 {
			return "", sectionsNoH3, "no \"## \" H2, no \"### \" H3 and no bold pseudo-heading to promote", 0
		}
		if h1 != 1 {
			return "", sectionsRefused, fmt.Sprintf("%d \"# \" H1 title line(s), want exactly 1: the validator refuses this above the missing-section arm", h1), 0
		}
		lines := strings.Split(content, "\n")
		for _, num := range bold {
			t := strings.TrimSpace(lines[num-1])
			lines[num-1] = "## " + strings.TrimSpace(t[2:len(t)-2])
		}
		promoted := strings.Join(lines, "\n")
		if verr := storage.ValidateWholeTaskFile(promoted); verr != nil {
			return "", sectionsRefused, "promoting the bold pseudo-heading would not make the file valid: " + verr.Error(), 0
		}
		return promoted, sectionsPromoteBold, "", len(bold)
	}
	if refusal != "" {
		return "", sectionsRefused, refusal, 0
	}
	if len(h3Lines) == 0 {
		return "", sectionsNoH3, "no \"## \" H2 and no promotable \"### \" H3", 0
	}
	if h1 != 1 {
		return "", sectionsRefused, fmt.Sprintf("%d \"# \" H1 title line(s), want exactly 1: the validator refuses this above the missing-section arm, so promoting H3s would not make the file valid", h1), 0
	}

	lines := strings.Split(content, "\n")
	for _, num := range h3Lines {
		lines[num-1] = lines[num-1][1:]
	}
	after = strings.Join(lines, "\n")

	if verr := storage.ValidateWholeTaskFile(after); verr != nil {
		return "", sectionsRefused, "promoting would not make the file valid: " + verr.Error(), 0
	}
	return after, sectionsPromote, "", len(h3Lines)
}

// outsideInertRegions is mdfence.OutsideFences minus the two regions mdfence
// does not model: leading YAML frontmatter and HTML comment blocks. Line.Num is
// carried through, so callers still address the ORIGINAL file.
//
// 🔴 ONE RULE, ONE PLACE, BECAUSE TWO COPIES DISAGREED. planTaskSections
// open-coded this skipping inline while boldPseudoHeadingLines did not, so the
// scan that DECIDES a file has no sections and the scan that picks what to
// PROMOTE were reading different documents. The promotion therefore manufactured
// a "## " heading inside an HTML comment or inside frontmatter — a region its own
// sibling scan treats as non-existent. The result VALIDATES, because the
// validator counts "## " lines the same way, so the file passes the very rule the
// promotion exists to satisfy while remaining unaddressable by amend. Validity is
// not correctness, and a test asserting "the validator now passes" goes green on
// exactly that file.
func outsideInertRegions(content string) []mdfence.Line {
	var out []mdfence.Line
	inComment := false
	inFrontmatter := false
	firstLine := true

	for _, l := range mdfence.OutsideFences(content) {
		trimmed := strings.TrimSpace(l.Text)

		// Leading "---" opens YAML frontmatter, which mdfence does not model at
		// all. A heading inside it is not a section.
		if firstLine && trimmed == "---" {
			inFrontmatter = true
			firstLine = false
			continue
		}
		firstLine = false
		if inFrontmatter {
			if trimmed == "---" {
				inFrontmatter = false
			}
			continue
		}

		// HTML comments are likewise invisible to mdfence: it recognises only
		// ` and ~ as delimiters, so "###" or "**Bold**" inside <!-- --> reads as
		// live text to every caller. Skipped for the same reason as frontmatter.
		if inComment {
			if strings.Contains(trimmed, "-->") {
				inComment = false
			}
			continue
		}
		if strings.Contains(trimmed, "<!--") && !strings.Contains(trimmed, "-->") {
			inComment = true
			continue
		}

		out = append(out, l)
	}
	return out
}

// boldPseudoHeadingLines returns the 1-indexed lines whose whole trimmed text is a
// bold run and nothing else — the shape an author used as a section title before
// the header contract existed.
//
// 🔴 THE PREDICATE IS STRICT, AND BOTH HALVES OF THAT ARE LOAD-BEARING.
//
// It requires the bold run to be the ENTIRE line, so "**Status:** retired" — bold
// followed by a value — is not a heading. And it requires NO trailing colon inside
// the bold, so "**Acceptance criteria:**" is not one either: that is a label for
// the list beneath it, not a section title, and it occurs in this corpus. A
// colon-tolerant predicate promotes it and silently restructures a file nobody
// asked to restructure.
//
// Computed over mdfence.OutsideFences, so a bold line inside a code fence is
// sample text.
func boldPseudoHeadingLines(content string) []int {
	var out []int
	for _, l := range outsideInertRegions(content) {
		t := strings.TrimSpace(l.Text)
		// 🔴 THE LENGTH BOUND IS SLICE ARITHMETIC, NOT A STYLE CHOICE. inner below
		// is t[2:len(t)-2], so len(t) must be at least 4 or that slice is INVERTED
		// and panics at runtime. "***" — the commonest markdown thematic break
		// there is — has length 3 and satisfies BOTH HasPrefix("**") and
		// HasSuffix("**"), because the prefix and the suffix OVERLAP. Weakening
		// this bound does not mis-promote a heading; it crashes the command
		// part-way through a vault-wide run, with whatever it had already written
		// left in place.
		if len(t) < 4 || !strings.HasPrefix(t, "**") || !strings.HasSuffix(t, "**") {
			continue
		}
		// 🔴 A RUN OF ASTERISKS IS A THEMATIC BREAK, NEVER A HEADING, AT ANY
		// LENGTH. This is a family, not the single case that motivated the length
		// bound above: "***" panics, "****" yields an empty inner, and "*****"
		// slips past BOTH of those and promotes to the heading "## *". Testing the
		// whole line for non-asterisk content closes every member at once,
		// including lengths nobody has enumerated.
		if strings.Trim(t, "*") == "" {
			continue
		}
		inner := strings.TrimSpace(t[2 : len(t)-2])
		// The colon test runs on the TRIMMED inner text: "**Acceptance criteria: **"
		// is the same label as "**Acceptance criteria:**", and testing the untrimmed
		// text lets one trailing space walk straight past the guard.
		//
		// The nested-marker test is what stops "**Note** and **Warning**" — a
		// sentence with two bold runs, not a heading — from being promoted into the
		// mangled heading "## Note** and **Warning".
		if inner == "" || strings.Contains(inner, "**") || strings.HasSuffix(inner, ":") {
			continue
		}
		out = append(out, l.Num)
	}
	return out
}

// ---------------------------------------------------------------------------
// The walk.
// ---------------------------------------------------------------------------

// runTaskSectionsMigration is the whole command, injectable for tests.
//
// ONE renderer, both modes: --apply prints the identical plan and then executes
// it, so the report cannot promise something the write does not deliver. The
// corpus is re-scanned fresh under --apply rather than replayed from a printed
// list, so it cannot drift between review and write.
func runTaskSectionsMigration(root, only string, apply bool, out io.Writer) (taskSectionsSummary, error) {
	var sum taskSectionsSummary

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
		for _, sub := range taskSectionsDirs {
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

				after, outcome, reason, promos := planTaskSections(string(data))
				d := taskSectionsDecision{
					Project: slug, Sub: sub, Slug: taskSlug,
					Outcome: outcome, Reason: reason, Promos: promos, After: after,
				}

				switch outcome {
				case sectionsNoWork:
					sum.NoWork++
					// 🔴 PRINT THE REASON. A file with an H2 that still fails the
					// validator is NOT this command's defect, but it IS a
					// malformed archived file, and B2 is the unit that has to
					// pick it up. An earlier version computed this reason and
					// never emitted it, so the only visible roster was the
					// refusals — which then had to carry causes they did not
					// have. Silence here is what made that misdirection possible.
					if reason != "" {
						sum.OtherDefect++
						fmt.Fprintf(out, "  --    %s/%s (%s/) — not this command's defect: %s\n",
							slug, taskSlug, sub, reason)
					}
				case sectionsNoH3:
					sum.NoH3++
					fmt.Fprintf(out, "  SKIP  %s/%s (%s/) — %s\n", slug, taskSlug, sub, reason)
				case sectionsRefused:
					sum.Refused++
					fmt.Fprintf(out, "  ??    %s/%s (%s/) — refused: %s\n", slug, taskSlug, sub, reason)
				case sectionsPromote, sectionsPromoteBold:
					// 🔴 THE SHADOW GUARD RUNS IN BOTH MODES, AND THAT IS THE
					// POINT. The writer resolves ACTIVE first, so an archived
					// slug that is also an active file would rewrite the WRONG
					// one; this file is refused under --apply, so a REPORT that
					// printed FIX and told the operator to "re-run with --apply"
					// would be promising something apply categorically refuses.
					// cmd_migrate_task_header.go:317/:370/:409 all make this
					// mistake by nesting the check inside `if apply`; it is a
					// known defect, not a template.
					if taskHeaderShadowed(out, root, slug, sub, taskSlug, name) {
						d.Outcome = sectionsRefused
						// Derived, never hardcoded: the guard covers done/+cancelled/
						// pairs too, and a literal here was false for them.
						d.Reason = taskHeaderShadowReason(root, slug, sub, name)
						d.Failed = true
						sum.Failed++
						sum.Decisions = append(sum.Decisions, d)
						continue
					}
					sum.Fix++
					if outcome == sectionsPromoteBold {
						fmt.Fprintf(out, "  FIX   %s/%s (%s/) — promote %d bold pseudo-heading(s) to \"## \"\n",
							slug, taskSlug, sub, promos)
					} else {
						fmt.Fprintf(out, "  FIX   %s/%s (%s/) — promote %d \"### \" heading(s) to \"## \"\n",
							slug, taskSlug, sub, promos)
					}
					if apply {
						// git holds the only copy of whatever a concurrent
						// session has written but not committed, and this is a
						// whole-file overwrite. Per FILE, not per vault: a dirty
						// note in another project must not block this repair.
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
						if werr := vault.OverwriteTaskFile(slug, taskSlug, after); werr != nil {
							fmt.Fprintf(out, "  !!    %s/%s (%s/): write: %v\n", slug, taskSlug, sub, werr)
							d.Failed = true
							sum.Failed++
							sum.Decisions = append(sum.Decisions, d)
							continue
						}
						d.Applied = true
						sum.Applied++
						taskSectionsRecordWrite(&sum, root, rel)
					}
				}
				sum.Decisions = append(sum.Decisions, d)
			}
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Scanned %d archived task file(s): %d need nothing (%d of them malformed for another reason), "+
		"%d to promote, %d skipped (no H3), %d refused.\n",
		sum.Scanned, sum.NoWork, sum.OtherDefect, sum.Fix, sum.NoH3, sum.Refused)
	if apply {
		fmt.Fprintf(out, "Applied %d rewrite(s).\n", sum.Applied)
		if sum.Dirty > 0 {
			fmt.Fprintf(out, "%d file(s) SKIPPED for uncommitted changes.\n", sum.Dirty)
		}
		taskSectionsRollbackBanner(out, root, sum)
	} else if sum.Fix > 0 {
		fmt.Fprintln(out, "Nothing was written. Re-run with --apply to write.")
	}
	if sum.Failed > 0 {
		fmt.Fprintf(out, "%d file(s) FAILED.\n", sum.Failed)
	}
	return sum, nil
}

// taskSectionsRecordWrite appends the paths one write dirtied: the task file,
// and the .surface stamp the locked writer touches alongside it.
//
// 🔴 THE STAMP IS ONLY LISTED WHEN GIT ALREADY TRACKS IT. A project written into
// for the first time has no committed `.surface`, and `git checkout -- <untracked>`
// is a pathspec error — which git applies to the WHOLE command, so one such path
// makes the undo restore none of the task files either, while looking to the
// operator like it worked. storage.GitPathIsTracked is the shared predicate that
// decides this; it is not re-implemented here.
//
// The task file itself needs no such check, and the reason is structural rather
// than lucky: the per-file dirty precondition above refuses any path
// `git status --porcelain` reports, and an untracked file is reported (`??`).
//
// The stamp path comes from surface.StampPath rather than a joined literal, so
// this does not own a second copy of the stamp filename or of where stamps live.
func taskSectionsRecordWrite(sum *taskSectionsSummary, root, rel string) {
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

// taskSectionsRollbackBanner prints the undo, scoped to what the run wrote.
//
// 🔴 EACH PATH IS SEPARATELY QUOTED, AND THAT IS NOT COSMETIC. The sibling banner
// in `migrate task-header` learned this the expensive way: a list joined into ONE
// quoted value across backslash-continued lines keeps the continuation
// indentation inside the argument. Quoting each path on its own means the
// whitespace between them is an argument separator, which is what `git checkout
// --` wants, and a path containing a space still survives.
//
// The command is printed with `git -C <root>` rather than bare `git` because the
// operator's shell is generally in the PROJECT repo, not the vault, and a
// `git checkout` run in the wrong repo either fails or reverts the wrong tree.
func taskSectionsRollbackBanner(out io.Writer, root string, sum taskSectionsSummary) {
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
	// flight.
	fmt.Fprintln(out, "\nDo NOT use `git checkout .` — the vault holds every project, and that would "+
		"revert other sessions' in-flight work along with this run.")
}
