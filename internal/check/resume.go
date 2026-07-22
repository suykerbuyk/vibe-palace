// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/mdfence"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// resume.md growth caps, mirroring the `/vpc-wrap` Step 3 contract
// ("Prune before you append"): resume.md is a thin gateway, not an archive —
// the full record lives in iterations.md, tasks/done/ and tasks/cancelled/.
//
// These are DETECTION thresholds, not enforcement. There is no typed write
// path left to gate: routine resume edits go through the generic
// vp_vault_read + vp_vault_edit pair, and any agent holding Bash can write
// the file directly regardless. Prevention is unachievable in-process;
// detection is achievable, so a violation is reported as Info — never Fail —
// and CheckResumeCaps never writes, moves, or repairs anything.
const (
	// ResumeMaxBytes is the stated size target for a single resume.md.
	ResumeMaxBytes = 25 * 1024

	// ResumeMaxHistoryRows caps the data rows of `## Project History`.
	ResumeMaxHistoryRows = 15

	// ResumeMaxCompletedRows caps the data rows of `## Completed Plans`.
	ResumeMaxCompletedRows = 12
)

// resumeHistorySection and resumeCompletedSection are the H2 headings whose
// tables carry a row cap.
const (
	resumeHistorySection   = "Project History"
	resumeCompletedSection = "Completed Plans"
)

// resumeViolation records one project's cap breaches. Zero breaches means the
// project is silent — it is never added to the report.
type resumeViolation struct {
	Project  string
	Breaches []string
}

// CheckResumeCaps scans every project under <vault>/Projects/ and reports the
// ones whose resume.md has outgrown its caps: total size over ResumeMaxBytes,
// `## Project History` over ResumeMaxHistoryRows data rows, or
// `## Completed Plans` over ResumeMaxCompletedRows data rows.
//
// It is strictly READ-ONLY and advisory: Pass when every resume is within its
// caps, Info when one or more are not. Never Fail — pruning is a wrap-time
// judgement call that only a human (or an agent running /vpc-wrap) can make,
// and a fat resume is a tax, not a breakage.
//
// Absence is never a violation: a project with no resume.md, a resume with no
// `## Project History` / `## Completed Plans` heading, and a section holding
// an empty (or header-only) table all report nothing.
func CheckResumeCaps(v *storage.Vault) Result {
	r := Result{Name: "Resume caps"}

	projectsDir := filepath.Join(v.Root, "Projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			r.Status = Pass
			r.Summary = "no Projects/ directory"
			return r
		}
		r.Status = Info
		r.Summary = fmt.Sprintf("scan Projects/: %v", err)
		return r
	}

	scanned := 0
	var violations []resumeViolation
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		path := filepath.Join(projectsDir, name, "resume.md")
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			// Missing resume.md is not a violation — nothing to report.
			continue
		}
		scanned++
		if breaches := resumeBreaches(data); len(breaches) > 0 {
			violations = append(violations, resumeViolation{Project: name, Breaches: breaches})
		}
	}

	if len(violations) == 0 {
		r.Status = Pass
		r.Summary = fmt.Sprintf("%d resume.md within caps", scanned)
		return r
	}

	sort.Slice(violations, func(i, j int) bool { return violations[i].Project < violations[j].Project })
	r.Status = Info
	r.Summary = fmt.Sprintf("%d of %d resume.md over cap", len(violations), scanned)
	for _, viol := range violations {
		r.Details = append(r.Details,
			fmt.Sprintf("  %s: %s", viol.Project, strings.Join(viol.Breaches, "; ")))
	}
	r.Details = append(r.Details,
		fmt.Sprintf("Caps: %d KB total, Project History %d rows, Completed Plans %d rows.",
			ResumeMaxBytes/1024, ResumeMaxHistoryRows, ResumeMaxCompletedRows),
		"resume.md is a gateway, not an archive — prune at the next wrap (/vpc-wrap Step 3);",
		"the full record already lives in iterations.md and tasks/done/.")
	return r
}

// resumeBreaches returns one human-readable line per cap the resume exceeds,
// in report order (size, history, completed). An empty slice means the resume
// is within every cap.
func resumeBreaches(data []byte) []string {
	var out []string
	if n := len(data); n > ResumeMaxBytes {
		out = append(out, fmt.Sprintf("%s (cap %d KB)", humanKB(n), ResumeMaxBytes/1024))
	}
	if n := countSectionTableRows(data, resumeHistorySection); n > ResumeMaxHistoryRows {
		out = append(out, fmt.Sprintf("%s %d rows (cap %d)",
			resumeHistorySection, n, ResumeMaxHistoryRows))
	}
	if n := countSectionTableRows(data, resumeCompletedSection); n > ResumeMaxCompletedRows {
		out = append(out, fmt.Sprintf("%s %d rows (cap %d)",
			resumeCompletedSection, n, ResumeMaxCompletedRows))
	}
	return out
}

// humanKB renders a byte count as a one-decimal KB string.
func humanKB(n int) string {
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}

// countSectionTableRows counts the markdown table DATA rows under the H2
// heading `## <section>` — excluding every header row and every `|---|---|`
// delimiter row.
//
// The scan is deliberately line-oriented rather than a real markdown parser:
// resume tables carry escaped pipes (`\|`), inline code spans and bold runs
// inside their cells, and a cell-splitting parser would trip over all three.
// Only three structural facts are needed, and each is decidable from the line
// alone:
//
//   - Fenced code blocks are tracked and skipped, so a ``` block that happens
//     to contain pipe-leading lines never inflates the count.
//   - The section runs from its heading to the next H1/H2 heading (a `###`
//     sub-heading does not close it) or to EOF.
//   - Within the section, each contiguous run of pipe-leading lines is a
//     candidate table; it counts only if it holds a delimiter row, and only
//     the lines AFTER that delimiter are data. So an empty table (header +
//     delimiter) counts 0, and a bare header with no delimiter — which GFM
//     does not render as a table at all — also counts 0.
//
// A missing section counts 0. Multiple tables in one section sum.
func countSectionTableRows(data []byte, section string) int {
	heading := "## " + section
	inSection := false
	rows := 0
	run := 0      // pipe-leading lines seen in the current contiguous run
	delimAt := -1 // index within the run of its delimiter row, or -1

	flush := func() {
		if delimAt >= 0 {
			rows += run - (delimAt + 1)
		}
		run, delimAt = 0, -1
	}

	// Fence classification lives in internal/mdfence — the one definition in this
	// codebase. It is NOT "the trimmed line starts with ```": that rule misreads
	// a line beginning with an INLINE code run as an opening fence, inverts its
	// state, and treats the whole rest of the file as fenced — which here means
	// silently counting ZERO rows and never firing the cap warning. Fail-open, in
	// the one check whose entire job is to notice. See the mdfence package doc.
	var fences mdfence.Scanner

	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))

		switch fences.Step(line) {
		case mdfence.Delimiter:
			// A fence boundary breaks any contiguous table run.
			flush()
			continue
		case mdfence.Fenced:
			continue
		}

		if isH1OrH2(trimmed) {
			flush()
			inSection = trimmed == heading
			continue
		}
		if !inSection {
			continue
		}

		if !strings.HasPrefix(trimmed, "|") {
			flush()
			continue
		}
		if delimAt < 0 && isTableDelimiter(trimmed) {
			delimAt = run
		}
		run++
	}
	flush()
	return rows
}

// isH1OrH2 reports whether a trimmed line opens an ATX heading of level 1 or
// 2. `### Sub` is level 3 and does NOT close a section.
func isH1OrH2(trimmed string) bool {
	return strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ")
}

// isTableDelimiter reports whether a trimmed pipe-leading line is a GFM
// delimiter row — one made of nothing but `|`, `-`, `:` and spaces, holding at
// least one dash (so a genuinely empty `| |` data row is not mistaken for one).
func isTableDelimiter(trimmed string) bool {
	dash := false
	for _, c := range trimmed {
		switch c {
		case '-':
			dash = true
		case '|', ':', ' ', '\t':
		default:
			return false
		}
	}
	return dash
}

// resume.md is a COMMITTED, shared file — vp_bootstrap_context reads it on every
// host, and it travels in the vault git history. A reference to a plan under a
// host-local `~/.claude/plans/…` (or an absolute `/home/…/.claude/plans/…`) is
// dead weight anywhere but the machine that wrote it: the path does not resolve
// on another host, and it leaks a local home layout into a shared artifact. The
// durable pointer is vault-relative (e.g. `tasks/done/…`).
//
// These two patterns are the only ones flagged (see CheckResumeRefs). Like every
// resume check this is detection, not prevention — reported as Info, never Fail.
var (
	// resumeRefHomeRe matches a home-relative plan reference: `~/.claude/plans/…`.
	resumeRefHomeRe = regexp.MustCompile(`~/\.claude/plans/\S*`)

	// resumeRefAbsRe matches an absolute plan reference: `/…/.claude/plans/…`.
	// The `(^|[^~])` guard keeps it from also firing on the tail of a
	// `~/.claude/plans/` token (whose leading `/` is preceded by `~`), so a
	// home-relative reference is counted once, as a home breach — not twice.
	// The path itself is captured in group 2.
	resumeRefAbsRe = regexp.MustCompile(`(^|[^~])(/\S*\.claude/plans/\S*)`)
)

// resumeRefBreaches returns one human-readable line per host-local plan
// reference committed into a resume body — a home-relative `~/.claude/plans/…`
// or an absolute `/…/.claude/plans/…` — each carrying the 1-indexed source line
// and the matched substring. An empty slice means the resume is clean.
//
// It is fence-aware: a path documented inside a Markdown code fence is a
// sample, not a live pointer, and is never flagged. Fence classification uses
// the shared mdfence.Scanner — the one definition in this codebase — so an
// inline code run is never misread as an opening fence.
func resumeRefBreaches(body []byte) []string {
	var out []string
	var fences mdfence.Scanner
	lineNo := 0
	for line := range strings.SplitSeq(string(body), "\n") {
		lineNo++
		// Advance the scanner on EVERY line so fence state stays correct;
		// only lines outside a fence (Text) are real references.
		if fences.Step(line) != mdfence.Text {
			continue
		}
		clean := strings.TrimSuffix(line, "\r")
		for _, m := range resumeRefHomeRe.FindAllString(clean, -1) {
			out = append(out, fmt.Sprintf("line %d: %s", lineNo, m))
		}
		for _, m := range resumeRefAbsRe.FindAllStringSubmatch(clean, -1) {
			out = append(out, fmt.Sprintf("line %d: %s", lineNo, m[2]))
		}
	}
	return out
}

// CheckResumeRefs scans every project under <vault>/Projects/ and reports the
// ones whose resume.md commits a host-local plan reference — a home-relative
// `~/.claude/plans/…` or an absolute `/…/.claude/plans/…` path. Because
// resume.md is a shared, committed artifact, such a path is unresolvable on any
// other host and leaks a local home layout into the vault.
//
// It is strictly READ-ONLY and advisory: Pass when every resume is clean, Info
// when one or more carry a host-local reference. Never Fail — the fix is a
// wrap-time judgement call (rewrite the pointer as vault-relative), and a stale
// path is a tax, not a breakage. Skip when no vault root is configured.
//
// It reads ONLY <vault>/Projects/*/resume.md; task files and everything else are
// out of scope. Absence is never a violation: a missing Projects/ dir, a project
// with no resume.md, and a clean resume all report nothing.
func CheckResumeRefs(v *storage.Vault) Result {
	r := Result{Name: "Resume refs"}

	if v.Root == "" {
		r.Status = Skip
		r.Summary = "no vault configured"
		return r
	}

	projectsDir := filepath.Join(v.Root, "Projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			r.Status = Pass
			r.Summary = "no Projects/ directory"
			return r
		}
		r.Status = Info
		r.Summary = fmt.Sprintf("scan Projects/: %v", err)
		return r
	}

	scanned := 0
	var violations []resumeViolation
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		path := filepath.Join(projectsDir, name, "resume.md")
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			// Missing resume.md is not a violation — nothing to report.
			continue
		}
		scanned++
		if breaches := resumeRefBreaches(data); len(breaches) > 0 {
			violations = append(violations, resumeViolation{Project: name, Breaches: breaches})
		}
	}

	if len(violations) == 0 {
		r.Status = Pass
		r.Summary = fmt.Sprintf("%d resume.md free of host-local plan refs", scanned)
		return r
	}

	sort.Slice(violations, func(i, j int) bool { return violations[i].Project < violations[j].Project })
	r.Status = Info
	r.Summary = fmt.Sprintf("%d of %d resume.md hold host-local plan refs", len(violations), scanned)
	for _, viol := range violations {
		r.Details = append(r.Details,
			fmt.Sprintf("  %s: %s", viol.Project, strings.Join(viol.Breaches, "; ")))
	}
	r.Details = append(r.Details,
		"resume.md is committed and shared — a `~/.claude/plans/…` or absolute plan path is host-local and unresolvable elsewhere;",
		"replace it with a vault-relative pointer (e.g. tasks/done/…) at the next wrap (/vpc-wrap).")
	return r
}
