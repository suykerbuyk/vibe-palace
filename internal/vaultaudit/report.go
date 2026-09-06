// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"fmt"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// ReportRelPath returns where a report for the given date lands, vault-relative.
//
// Phase 0 taught tidy and the surface stamp about exactly this shape — a FLAT
// `Audits/*.md`. If it ever becomes nested, the tidy SweepRule must change with it,
// or the first report written is permanent vault dirt that blocks `vault push`.
func ReportRelPath(date string) string { return "Audits/" + date + "-vault-audit.md" }

// Render writes the report as markdown.
//
// Three properties, each earned:
//
//   - DIFFABLE. Dimensions are in registry order and findings are sorted, so
//     `git log -p Audits/` shows week-over-week DRIFT rather than churn. A report
//     that reorders its own rows is not a report, it is noise.
//
//   - `unknown` IS VISUALLY LOUD and cannot be confused with `pass`. An audit that
//     cannot see something and says so quietly is the vp_health bug in a new costume.
//
//   - EVERY NUMBER CARRIES THE COMMAND THAT PRODUCED IT. Hand-recorded censuses in
//     this project have rotted every single time — including both figures in the plan
//     this package implements, stale one iteration after they were written.
//
// The date is passed in rather than read from the clock: a caller that stamps the
// report and a caller that names the file must agree, and a function that reads the
// clock twice can disagree with itself across midnight. Both shipping callers get it
// from storage.Vault.CalendarDay — see that method for why the WRITER owns the day and a
// client-supplied date is not an authority.
//
// An EMPTY date renders as an UNDATED report. No shipping caller passes one any more:
// the MCP handler and the CLI both stamp the writer's calendar day, on the inline path
// as well as the written one. The undated branch survives because it is exercised
// directly by tests, and because a rendering function that silently invented a date
// when handed none would be the defect this package exists to catch.
//
// It must NOT render as an empty field. The first MCP run of this tool emitted
// `date:` with nothing after it and a title ending in a bare em-dash: a document
// asserting it has a date, and then not having one. A hollow field is worse than an
// absent one — it looks like data, so a reader (or a parser) trusts it — and shipping
// one from THIS package, whose entire subject is instruments that claim more than they
// know, would have been the joke writing itself. Absent means absent.
func (r Report) Render(date, vaultRoot string) string {
	var b strings.Builder

	if date == "" {
		b.WriteString("---\ntype: vault-audit\n---\n\n")
		b.WriteString("# Vault Audit — undated\n\n")
		b.WriteString("*This report was rendered inline and carries no date. Write it with " +
			"`vp audit vault --write` (or `vp_audit_vault` with `write: true`) to stamp and " +
			"commit it, which is what makes `git log -p Audits/` show drift.*\n\n")
	} else {
		// session_notes is the CHURN ANCHOR the staleness nag subtracts from. It is
		// stamped only on a DATED (i.e. written) report: an undated inline render is
		// not a record of anything, and a nag that anchored on one would silently
		// reset its own clock every time someone merely LOOKED at the audit.
		fmt.Fprintf(&b, "---\ntype: vault-audit\ndate: %s\nsession_notes: %d\n---\n\n", date, r.SessionNotes)
		fmt.Fprintf(&b, "# Vault Audit — %s\n\n", date)
	}

	// The headline verdict, before any detail. A reader who stops here must not be
	// misled: accepted debt PASSES but is not CLEAN, and the summary says both.
	newTotal, staleTotal, acceptedTotal, unknownTotal := r.totals()
	switch {
	case newTotal > 0 || staleTotal > 0:
		fmt.Fprintf(&b, "## 🔴 FAIL — %d new finding(s), %d stale baseline entr(ies)\n\n", newTotal, staleTotal)
	case unknownTotal > 0:
		fmt.Fprintf(&b, "## ⚠️ UNKNOWN — the auditor could not read %d thing(s)\n\n", unknownTotal)
	default:
		b.WriteString("## ✅ PASS — no new drift\n\n")
	}
	fmt.Fprintf(&b, "%d finding(s) are ACCEPTED debt (`%s`). **Passing is not the same as clean.**\n\n",
		acceptedTotal, BaselineRelPath)
	fmt.Fprintf(&b, "Vault: `%s` (resolved at run time — never recorded, because an absolute path is a "+
		"fact about the host that wrote it).\n\n", redactHome(vaultRoot))

	b.WriteString("| Dimension | Status | New | Stale | Accepted | Unknown |\n")
	b.WriteString("|---|---|---|---|---|---|\n")
	for _, d := range r.Dimensions {
		fmt.Fprintf(&b, "| `%s` | %s | %d | %d | %d | %d |\n",
			d.Name, statusBadge(d.Status), len(d.New), len(d.Stale), d.Accepted, len(d.Unknowns))
	}
	b.WriteString("\n")

	r.renderUnknownDimensions(&b)

	for _, d := range r.Dimensions {
		fmt.Fprintf(&b, "## `%s` — %s\n\n", d.Name, statusBadge(d.Status))
		fmt.Fprintf(&b, "**Verify, never trust this report:**\n\n```sh\n%s\n```\n\n", d.Evidence)

		if len(d.Unknowns) > 0 {
			b.WriteString("### ⚠️ UNKNOWN — the auditor could not look here\n\n")
			b.WriteString("*\"I have no information\" is not \"nothing is wrong.\"*\n\n")
			for _, u := range d.Unknowns {
				fmt.Fprintf(&b, "- %s\n", u)
			}
			b.WriteString("\n")
		}

		if len(d.New) > 0 {
			fmt.Fprintf(&b, "### 🔴 NEW — %d finding(s) not in the baseline\n\n", len(d.New))
			for _, f := range d.New {
				fmt.Fprintf(&b, "- `%s` — %s\n", f.Artifact, f.Detail)
			}
			b.WriteString("\n")
		}

		if len(d.Stale) > 0 {
			fmt.Fprintf(&b, "### 🔴 STALE — %d accepted entr(ies) that are NO LONGER findings\n\n", len(d.Stale))
			b.WriteString("**These were FIXED and the baseline was not updated.** The baseline may only " +
				"SHRINK — that is what keeps it honest. Remove them.\n\n")
			for _, s := range d.Stale {
				fmt.Fprintf(&b, "- `%s`\n", s.Artifact)
			}
			b.WriteString("\n")
		}

		if len(d.New) == 0 && len(d.Stale) == 0 && len(d.Unknowns) == 0 {
			if d.Accepted > 0 {
				fmt.Fprintf(&b, "No new drift. **%d accepted finding(s) remain** — this dimension passes "+
					"because the debt is recorded, not because it is gone.\n\n", d.Accepted)
			} else {
				b.WriteString("Clean.\n\n")
			}
		}
	}

	b.WriteString("## The ratchet — the most valuable output of this audit is not this report\n\n")
	b.WriteString("**For each finding you made by JUDGMENT rather than by a check: write the check.**\n" +
		"Name where it goes (a `vaultaudit` dimension, a `sourceaudit` rule, a test). A finding that\n" +
		"does not become a check will be rediscovered from scratch next week, and in six months this\n" +
		"report will say PASS while something rots.\n\n" +
		"**A ratchet-less audit is a rubber stamp with extra steps.**\n")

	return b.String()
}

// renderUnknownDimensions emits the STALE-BINARY block, and writes nothing when the
// binary is current.
//
// It sits directly under the dimension table because the first thing it has to
// correct is what the reader just read: the table is not the audit's scope, it is the
// scope of the binary that produced it, and those differ right now.
//
// LOUD, AND STILL NOT A VERDICT. The headline above is computed by totals() from the
// rows that exist, and this block deliberately does not touch it — the audit stays
// advisory (operator decision, 204) and the dispatch-seam surface gate stays the one
// place a stale binary is refused. A second gate here would be two answers to one
// question, and the first one to be wrong at a bad moment gets both disabled.
//
// The wording has to carry the mechanism, not just the fact, because the failure is
// INVISIBLE by construction: an unknown dimension gets no row, its stale entries are
// computed inside Baseline.Diff and then dropped by newDimensionResult's
// `s.Dimension == name` filter, and Failed() therefore sees nothing. A reader who is
// told only "some dimensions were skipped" will reasonably assume the ✅ above still
// covers what it lists. It does — and that is the trap. It covers less than the vault.
func (r Report) renderUnknownDimensions(b *strings.Builder) {
	if len(r.UnknownDimensions) == 0 {
		return
	}

	fmt.Fprintf(b, "## 🔴🔴 THIS BINARY IS BEHIND THE VAULT — %d dimension(s) DID NOT RUN\n\n",
		len(r.UnknownDimensions))
	fmt.Fprintf(b, "`%s` records accepted debt for dimension(s) this binary's registry does not "+
		"contain. The baseline is written by whichever `vp` last accepted findings, so a key with "+
		"no registry entry can only mean **the vault has seen a newer `vp` than this one**.\n\n",
		BaselineRelPath)
	for _, name := range r.UnknownDimensions {
		fmt.Fprintf(b, "- `%s`\n", name)
	}
	b.WriteString("\n")
	b.WriteString("**What that means for everything above.** These dimensions have NO ROW in the " +
		"table — they were never run, so there is nothing to tabulate. Their accepted artifacts do " +
		"not surface as `STALE` either: those entries are computed and then discarded, because a " +
		"stale entry is only kept for the dimension currently being built. **So the verdict above " +
		"can read ✅ PASS while this report is silently partial.** It is a verdict about what this " +
		"binary can see, not about the vault.\n\n")
	b.WriteString("**About `vp audit vault --accept`.** Accepting rebuilds the baseline from the " +
		"findings of the dimensions that RAN, and a dimension that never ran produces no findings " +
		"— so its entire entry (the reason a human wrote, its accepted list, its exceptions, its " +
		"recorded measurements) is exactly what a regeneration drops. **A `vp` older than this one " +
		"deletes it.** This binary does not: `Baseline.Regenerate` carries an unrecognised " +
		"dimension's entry through verbatim, precisely because it never ran and therefore proved " +
		"nothing about it. Re-run the audit from an up-to-date binary anyway — the report is " +
		"recoverable by re-running, **the baseline is not**, and a partial report is not worth " +
		"accepting.\n\n")
	// The way out is NOT written here. It is sourced from the one place that prose
	// lives — surface.(*IncompatibleError).Remediation() — because the last time this
	// text existed in two copies they had already drifted (`action:` became
	// `Upgrade:`, `<original-command>` became `<command>`, the framing line vanished)
	// and nothing noticed. A stranded operator's only route back is text, so a second
	// copy here would be a message that is well-formed, plausible, and wrong.
	// A bare fence, not ```sh: Remediation() is a mixed block — one command line and
	// two lines of prose framing it — and labelling it as shell would be a small lie
	// told by the highlighter about text a stranded reader has to follow exactly.
	b.WriteString("**Fix it** — the standard stale-binary remediation, the same lines the surface " +
		"gate prints:\n\n```\n")
	for _, line := range (&surface.IncompatibleError{}).Remediation() {
		b.WriteString(line + "\n")
	}
	b.WriteString("```\n\n")
	b.WriteString("Then re-run the audit from the up-to-date binary. Only that report covers the " +
		"whole vault.\n\n")
}

// totals sums the report. Accepted is counted because a dimension carrying 82
// accepted findings and 0 new ones is PASSING but not CLEAN, and a report that
// renders those identically teaches its reader that the debt is gone.
func (r Report) totals() (newN, staleN, acceptedN, unknownN int) {
	for _, d := range r.Dimensions {
		newN += len(d.New)
		staleN += len(d.Stale)
		acceptedN += d.Accepted
		unknownN += len(d.Unknowns)
	}
	return
}

// statusBadge renders a status so UNKNOWN cannot be mistaken for PASS at a glance.
func statusBadge(s Status) string {
	switch s {
	case StatusPass:
		return "✅ pass"
	case StatusFail:
		return "🔴 **FAIL**"
	case StatusUnknown:
		return "⚠️ **UNKNOWN**"
	}
	return string(s)
}

// redactHome keeps a host-specific absolute path out of a synced document. The vault
// is read on machines other than the one that wrote this file, and an absolute path
// is a fact about the writer that is false everywhere else (188).
func redactHome(p string) string {
	if i := strings.Index(p, "/obsidian/"); i > 0 {
		return "…" + p[i:]
	}
	if i := strings.LastIndex(p, "/"); i > 0 {
		return "…/" + p[i+1:]
	}
	return p
}
