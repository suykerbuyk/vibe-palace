// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/mdfence"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// Every dimension below is EVIDENCE-BACKED: it exists because a real defect got
// through, and it is named with the iteration that earned it. A dimension without a
// defect behind it is speculative coverage, and speculative coverage is how an audit
// becomes a position paper nobody runs.

const (
	// DimProjectTreeCoherence — a project must appear in BOTH of the vault's trees.
	// Earned by 204's REVERSAL 1: the two trees had silently diverged and NOTHING in
	// the system would ever have noticed.
	DimProjectTreeCoherence = "project-tree-coherence"

	// DimKGPortability — no KG triple filename may contain a character that makes the
	// vault unmountable on NTFS/exFAT. Earned by kg-triple-filename-sanitization
	// (critical): this is the defect that blocks ALL Windows work.
	DimKGPortability = "kg-portability"

	// DimResumeDiscipline — resume.md within its size cap, and its placeholder tokens
	// INTACT on disk. Earned by 186/194, and by the standing overage.
	DimResumeDiscipline = "resume-discipline"

	// DimIterationHeadings — every heading in iterations.md is the header the writer
	// would have emitted: no frame orphan, no non-canonical numbered heading, no
	// doubled "Iteration N —" prefix. Earned by 191 (110 H2 against 81 H3 meant the
	// counter read a level nobody wrote and reported "fresh project" on 18 iterations
	// of real history) and widened by the frame-orphan census, where six numbered
	// entries were being served with a whole following narrative glued to their tail.
	//
	// It has never checked for duplicate numbers, and that is deliberate — see the
	// prohibition on auditIterationHeadings before re-adding it.
	DimIterationHeadings = "iteration-headings"

	// DimMemoryPortability — no memory filename may be unrepresentable on NTFS/exFAT,
	// and no two in one directory may collide case-insensitively. Earned by
	// memory-filenames-unaudited-charset-and-case (high): MemoryFile accepted reserved
	// chars and Windows device names, WriteMemory had no case-fold guard, and NO
	// dimension covered Projects/*/memory/ — so a vault that synced cleanly on Linux
	// could silently become un-checkout-able on the Windows/darwin release targets.
	DimMemoryPortability = "memory-portability"

	// DimTaskHeadingMarkers — no H2 heading in an ACTIVE task file may carry an
	// unresolved-status marker: a token asserting an open state that a later event
	// closes. Earned by amend-cannot-rewrite-an-h2-heading-so-stale-markers-strand
	// and its 2026-08-23 census.
	//
	// The defect this reports is not cosmetic and not repairable by the writer that
	// created it: `vp_manage_task action: amend` is KEYED on the H2 heading text, so
	// amending under the old text replaces the body and leaves the stale heading,
	// while amending under a corrected text APPENDS a second section and leaves both.
	// A heading is therefore written exactly once — at create, or at the first amend
	// that introduces it — and a status marker baked into one is false from the moment
	// the state it names changes, permanently, with the reader obeying the heading.
	//
	// 🔴 SCOPE IS ACTIVE TASKS ONLY, and that is a ruling rather than a convenience.
	// `AmendTask` resolves through the active path unconditionally, `vp tasks edit`
	// refuses on meta.Done, and the adopted `overwrite` action is active-scoped — so a
	// finding under tasks/done/ or tasks/cancelled/ is unrepairable by every sanctioned
	// path and would be permanent, un-actionable red. The funnel task in done/ carries
	// five such headings today; they are deliberately out of scope.
	//
	// This dimension sees CLASS A only — a claim phrased with a token. A claim
	// carrying NO token ("The anchor set already exists — reuse it, do not
	// re-derive it", whose body now instructs the exact opposite) is Class C, is
	// invisible here BY CONSTRUCTION, and is answered by the topic-not-claim
	// discipline rather than by code. Do not widen the token list to chase it; that
	// road ends at flagging the correction idiom itself.
	DimTaskHeadingMarkers = "task-heading-markers"
)

// Evidence commands. RECORD THE GREP, NEVER THE COUNT (invariant 3) — every number
// in the report carries the command that reproduces it, or it rots by the next run.
const (
	EvidenceProjectTreeCoherence = `comm -3 <(ls -d Projects/*/ | xargs -n1 basename | sort) <(ls -d palace/*/ | xargs -n1 basename | sort)`
	EvidenceKGPortability        = `find palace/*/kg/triples -name '*:*' -o -name '*' -newer /dev/null | grep ':'`
	EvidenceResumeDiscipline     = `wc -c Projects/*/resume.md; grep -c '{{[A-Z]*}}' Projects/*/resume.md`
	// Two of the three conditions are expressible as a grep and are recorded as one.
	// The third is NOT: a frame orphan is defined against the writer's "---" frame and
	// must be fence-aware and front-matter-aware, and a grep that fakes it invents
	// findings on quoted sample text. The reproducing command for that one is the
	// check itself, which runs the same scan the dimension does.
	EvidenceIterationHeadings = `grep -nE '^#{2,3}[[:space:]]+Iteration [0-9]+' Projects/*/iterations.md | grep -vE ':## Iteration [0-9]+ — .' ; ` +
		`grep -nE '^## Iteration [0-9]+ *[—–-] *Iteration [0-9]+ *[—–-]' Projects/*/iterations.md ; ` +
		`vp check --check iteration-headings   # frame orphans: fence-aware, no grep is honest here`
	EvidenceMemoryPortability = `find Projects/*/memory -type f -printf '%f\n' | grep -Ei '[<>:"\\|?*]|[. ]$|^(con|prn|aux|nul|com[1-9]|lpt[1-9])([.]|$)'; ` +
		`for d in Projects/*/memory; do find "$d" -type f -printf '%f\n' | tr 'A-Z' 'a-z' | sort | uniq -d; done   # case collisions`
)

// unresolvedStatusMarkers is the DECLARED marker set for DimTaskHeadingMarkers: the
// tokens that assert an open state a later event closes.
//
// 🔴 THIS LIST IS EXTENSIBLE, NOT COMPLETE, AND SAYING SO IS PART OF THE CONTRACT.
// The 2026-08-23 census set out with four markers (UNCOMMITTED, WIP, TODO, "not yet
// decided") and had to grow to this eleven on a single pass over ONE project's active
// tasks — every addition driven by a specimen rather than by enumeration. UNRESOLVED
// in particular was invisible to the original four and sits on the positive-control
// file. So the failure mode here is NOT the ratchet disease of too many findings; it
// is a rule that under-fires silently and reports zero as though it had looked. That
// is why EvidenceTaskHeadingMarkers prints this list into every report: a reader must
// be able to see WHICH rule produced the number, and extend it.
//
// 🔴 MATCHING IS CASE-SENSITIVE, AS DECLARED, AND THAT IS A RULING (chair, 2026-08-23)
// EARNED BY TWO LIVE FALSE POSITIVES. The first implementation folded case over the
// whole alternation, which turned the shout-token BLOCKED into the ordinary English
// word "blocked" and fired on
// "## Inventory (live `make` blocked section + BUILDABLE RED)" — where "blocked"
// is ADJECTIVAL, naming a section of the build rather than asserting that section's
// own status. Each token is therefore matched exactly as it is written here: the
// all-caps ones stay all-caps, and a title-case status tag is declared separately.
//
// The same pass declared the phrase "awaiting human commit" rather than a bare
// "awaiting", for the same reason — the bare word is not a status.
//
// The known cost of case-sensitivity is UNDER-firing: "## blocked on the operator"
// is not matched. That is the accepted trade, recorded rather than discovered — an
// auditor that invents findings is worse than one that misses them
// (internal/check/iteration_headings.go:69-73, where a rule that invented 17 findings
// against a deliberate operator pattern was DELETED). Extend the list when a specimen
// appears; do not reach for (?i) to catch a shape nobody has seen.
//
// The second false positive is why lowercase "not decided" is absent: the live heading
// "## The open architectural choice — child 1 decides it, deliberately not decided
// here" is PERMANENTLY TRUE. It delegates a decision to a child task rather than
// deferring one, so it can never go stale. A pending state and a scope statement read
// alike in prose and are opposites in fact.
//
// REFUTED is deliberately ABSENT. It is the token this project's correction idiom is
// written in ("options 1 and 2 are REFUTED"), so admitting it would flag the very
// practice of recording a reversal — the opposite of the intent. A Decision heading
// whose trailing conclusion was later reversed but which carries no marker from this
// list stays Class C.
var unresolvedStatusMarkers = []string{
	"UNCOMMITTED",
	"UNRULED",
	"UNRESOLVED",
	"UNDECIDED",
	"BLOCKED",
	"WIP",
	"TODO",
	// A title-case status tag heading a section ("## Blocked on"). All-caps BLOCKED
	// cannot see it, and it is a true positive: it goes stale the moment the task
	// unblocks. Declared separately rather than solved with (?i), which is what
	// produced the adjectival false positive above.
	"Blocked on",
	"not yet decided",
	"NOT decided",
	"none of these is decided",
	"awaiting human commit",
}

// taskHeadingMarkerRE matches any declared marker as a whole word inside a heading.
//
// It is built FROM unresolvedStatusMarkers rather than written alongside it, so the
// matcher and the printed evidence cannot drift apart — two hand-maintained copies of
// one list is the defect class this vault keeps finding, and it would be absurd to
// ship it inside the dimension that exists to report stale hand-maintained claims.
//
// A nil regexp (empty marker list) matches nothing. That is the mutation seam: empty
// the list and every positive fixture must go unreported.
var taskHeadingMarkerRE = buildMarkerRE(unresolvedStatusMarkers)

func buildMarkerRE(markers []string) *regexp.Regexp {
	if len(markers) == 0 {
		return nil
	}
	parts := make([]string, 0, len(markers))
	for _, m := range markers {
		parts = append(parts, `\b`+regexp.QuoteMeta(m)+`\b`)
	}
	// NO (?i). Every token matches exactly as declared — see the ruling on
	// unresolvedStatusMarkers. Case-folding here is what made "blocked section" a
	// finding, and it is the one change to this function that must not be made
	// without a specimen and a chair ruling.
	return regexp.MustCompile(strings.Join(parts, "|"))
}

// EvidenceTaskHeadingMarkers is a var, not a const, because it is COMPOSED from the
// marker list above. RECORD THE GREP, NEVER THE COUNT — and here the grep is only
// honest if it names the same tokens the code matched.
// It is `grep -E`, NOT `grep -Ei`: the matcher is case-sensitive by ruling, and an
// evidence command that reproduces a DIFFERENT number than the code is worse than no
// evidence command at all.
var EvidenceTaskHeadingMarkers = `grep -nE '^## ' Projects/*/tasks/*.md | grep -E '` +
	strings.Join(unresolvedStatusMarkers, "|") + `'` +
	"   # markers are DECLARED AND EXTENSIBLE, never complete, and CASE-SENSITIVE as written: " +
	strings.Join(unresolvedStatusMarkers, ", ") +
	"   # H2 only; fence-aware; tasks/done/ and tasks/cancelled/ deliberately out of scope"

// auditProjectTreeCoherence: a project present in one tree and not the other.
//
// This dimension costs nothing — phase 2's union enumerator already computes the
// answer — and NOTHING ELSE IN THE SYSTEM would ever report it. A project with
// history and no palace/ store is unsearchable; a palace/ store with no history is a
// leftover. Both are real drift.
func auditProjectTreeCoherence(vault *storage.Vault) ([]Finding, []string, error) {
	projects, err := vault.ListAllProjects()
	if err != nil {
		return nil, nil, fmt.Errorf("enumerate projects: %w", err)
	}

	var findings []Finding
	for _, p := range projects {
		if p.Complete() {
			continue
		}
		var detail string
		switch {
		case p.InProjects:
			detail = "has session history under Projects/ but NO palace/ store — its sessions were " +
				"never drawer-indexed, so they are UNSEARCHABLE"
		default:
			detail = "has a palace/ store but NO history under Projects/ — a leftover, or a project " +
				"whose history was never written"
		}
		findings = append(findings, Finding{
			Dimension: DimProjectTreeCoherence,
			Artifact:  p.Slug,
			Detail:    detail,
		})
	}
	return findings, nil, nil
}

// portabilityHostile are the characters that make a filename unusable on NTFS/exFAT.
// A vault carrying any of them cannot be checked out on Windows AT ALL, which is why
// kg-triple-filename-sanitization is critical and blocks the whole Windows effort.
const portabilityHostile = `:*?"<>|` + "\n\r"

// auditKGPortability: KG triple filenames that no Windows filesystem can represent.
//
// 🔴 THE ARTIFACT IS THE TRIPLES DIRECTORY, NOT THE FILE — and the granularity is the
// design, not a shortcut.
//
// The live vault holds ~21,700 offending filenames. One baseline entry per file would
// be a two-megabyte accepted-debt list that no human can read, review, or diff — and
// it would carry no more information than one line per project, because THE FIX IS A
// SINGLE MIGRATION (kg-triple-filename-sanitization) that repairs all of them at once.
// They become clean together or not at all.
//
// So the ratchet works at the unit the FIX works at: when the rename lands, each
// project's entry stops being a finding in one step, goes STALE, and the baseline is
// forced to shrink. A per-file baseline would instead demand 21,700 individual
// removals to record one migration.
//
// The COUNT lives in the detail, which is recomputed every run — so it cannot rot, and
// a growing count is visible in the report's diff even while the entry stays accepted.
func auditKGPortability(vault *storage.Vault) ([]Finding, []string, error) {
	projects, err := vault.ListAllProjects()
	if err != nil {
		return nil, nil, fmt.Errorf("enumerate projects: %w", err)
	}

	var findings []Finding
	var unknowns []string

	for _, p := range projects {
		if !p.InPalace {
			continue // no palace/ store ⇒ no KG. An absence, not a blindness.
		}
		root := filepath.Join(vault.Root, "palace", p.Slug, "kg", "triples")
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}

		hostile := 0
		total := 0
		worst := ""
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// Could not look. Say so; do not pass. Never abort the whole sweep for
				// one unreadable subtree — a partial answer plus an explicit unknown
				// beats no answer at all.
				unknowns = append(unknowns, fmt.Sprintf("%s: cannot read %s: %v",
					p.Slug, relTo(vault.Root, path), err))
				return nil
			}
			if d.IsDir() {
				return nil
			}
			total++
			if bad := strings.IndexAny(d.Name(), portabilityHostile); bad >= 0 {
				hostile++
				if worst == "" {
					worst = relTo(vault.Root, path)
				}
			}
			return nil
		})
		if err != nil {
			unknowns = append(unknowns, fmt.Sprintf("%s: walk failed: %v", p.Slug, err))
		}

		if hostile > 0 {
			findings = append(findings, Finding{
				Dimension: DimKGPortability,
				Artifact:  relTo(vault.Root, root),
				Detail: fmt.Sprintf("%d of %d triple filenames contain a character NTFS/exFAT cannot "+
					"represent (e.g. %q) — THE VAULT CANNOT BE CHECKED OUT ON WINDOWS while these "+
					"exist, which is why kg-triple-filename-sanitization is critical and blocks all "+
					"Windows work. One migration fixes them together, so they are recorded together.",
					hostile, total, worst),
			})
		}
	}
	return findings, unknowns, nil
}

// auditMemoryPortability: memory filenames that no Windows filesystem can represent,
// or that collide case-insensitively within one directory.
//
// Unlike KG triples (one bulk migration fixes them together, so that dimension keys on
// the directory), memory files are individually authored and rare, so each offender is
// its own finding keyed on its own path — the ratchet clears them one fix at a time.
//
// The portable-name test is vaultfs.ValidatePortableSegment — the SAME predicate the
// write path (MemoryFile → ValidateRelPath) now enforces. Sharing it is the point: the
// audit reports exactly what a write would refuse, so the two can never drift into
// disagreeing about what "portable" means.
func auditMemoryPortability(vault *storage.Vault) ([]Finding, []string, error) {
	projects, err := vault.ListAllProjects()
	if err != nil {
		return nil, nil, fmt.Errorf("enumerate projects: %w", err)
	}

	var findings []Finding
	var unknowns []string

	for _, p := range projects {
		if !p.InProjects {
			continue // memory/ lives under Projects/<slug>/; no history ⇒ no memory dir
		}
		root := filepath.Join(vault.Root, "Projects", p.Slug, "memory")
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}

		// first[dir+"\x00"+lower(name)] = the first actual-cased name seen in that
		// directory, so a later differently-cased sibling is flagged as a collision.
		first := map[string]string{}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				unknowns = append(unknowns, fmt.Sprintf("%s: cannot read %s: %v",
					p.Slug, relTo(vault.Root, path), err))
				return nil
			}
			if d.IsDir() {
				return nil
			}
			name := d.Name()
			if perr := vaultfs.ValidatePortableSegment(name); perr != nil {
				findings = append(findings, Finding{
					Dimension: DimMemoryPortability,
					Artifact:  relTo(vault.Root, path),
					Detail: fmt.Sprintf("memory filename is not portable to NTFS/exFAT (%v) — the vault "+
						"cannot be checked out on the Windows/darwin release targets while this exists.", perr),
				})
			}
			key := filepath.Dir(path) + "\x00" + strings.ToLower(name)
			if prev, ok := first[key]; ok {
				if prev != name {
					findings = append(findings, Finding{
						Dimension: DimMemoryPortability,
						Artifact:  relTo(vault.Root, path),
						Detail: fmt.Sprintf("collides case-insensitively with %q in the same directory — "+
							"they are one file on macOS/Windows, so one silently clobbers the other.", prev),
					})
				}
			} else {
				first[key] = name
			}
			return nil
		})
		if err != nil {
			unknowns = append(unknowns, fmt.Sprintf("%s: walk failed: %v", p.Slug, err))
		}
	}
	return findings, unknowns, nil
}

// auditResumeDiscipline: resume.md within its size cap.
//
// 🔴 IT READS THE RAW BYTES FROM DISK, AND THAT IS NOT AN IMPLEMENTATION DETAIL.
//
// The resolver runs expandScoped() over everything it serves, substituting the very
// placeholder tokens resume.md carries. An audit that read resume.md through
// vp_get_resume or vp_bootstrap_context would see EXPANDED content — so any check on
// those tokens would report `pass` every single time, including on a vault where they
// had already been destroyed. It would be an auditor lying about the exact thing it
// exists to catch. os.ReadFile is the requirement, not a shortcut.
//
// ⚠ THE TOKEN CHECK ITSELF IS DELIBERATELY NOT IMPLEMENTED, and this is the honest
// reason rather than an oversight: a resume with zero tokens is INDISTINGUISHABLE, from
// the file alone, between "the tokens were baked out" (the bug) and "this resume never
// had any" (fine). Detecting it soundly needs the TEMPLATE's token set to compare
// against, which is a real piece of work and not v1. Claiming the check while shipping
// something weaker would be the failure this whole epic is named after — so the
// dimension reports what it actually verifies: the size cap.
func auditResumeDiscipline(vault *storage.Vault) ([]Finding, []string, error) {
	projects, err := vault.ListAllProjects()
	if err != nil {
		return nil, nil, fmt.Errorf("enumerate projects: %w", err)
	}

	var findings []Finding
	var unknowns []string

	for _, p := range projects {
		if !p.InProjects {
			continue
		}
		rel := "Projects/" + p.Slug + "/resume.md"
		data, err := os.ReadFile(filepath.Join(vault.Root, filepath.FromSlash(rel)))
		if os.IsNotExist(err) {
			continue // a project with no resume is not a project with a broken one
		}
		if err != nil {
			unknowns = append(unknowns, fmt.Sprintf("%s: cannot read resume.md: %v", p.Slug, err))
			continue
		}

		if len(data) > check.ResumeMaxBytes {
			findings = append(findings, Finding{
				Dimension: DimResumeDiscipline,
				Artifact:  rel,
				Detail: fmt.Sprintf("%d bytes against a %d-byte cap (%.1fx) — resume.md is a GATEWAY, "+
					"not a diary, and every byte here is paid for by every session's bootstrap",
					len(data), check.ResumeMaxBytes, float64(len(data))/float64(check.ResumeMaxBytes)),
			})
		}
	}
	return findings, unknowns, nil
}

// auditIterationHeadings: every heading in iterations.md is the header the WRITER
// would have emitted.
//
// Earned by 191, where iterations.md held 110 H2 against 81 H3 — the counter read one
// level, saw a fraction of the history, and reported "fresh project" for a sibling
// with 18 iterations of real narrative. A wrap trusting that number would have
// renumbered from scratch ON TOP of the existing history. The live vault is now clean
// of H3 headers, so that half of this dimension is a REGRESSION GUARD: it exists to
// keep it that way, and a dimension whose value is that it stays at zero is worth
// having.
//
// The H3 rule was never the whole contract, though, and the two defect classes it
// could not see were both live. The dimension now delegates to
// wrapstate.ScanHeadingDefects, which tests THREE independent conditions — none of
// which subsumes another:
//
//   - FRAME ORPHANS. The writer composes every entry as "\n---\n" + the header +
//     the body, so an H2 on that frame that FormatIterationHeader would not have
//     emitted is a real entry boundary the reader cannot see. Its narrative is
//     unaddressable by number and is served as the tail of the PREVIOUS entry:
//     vp_get_iteration over-returned exactly that way for 108, 110, 125, 128, 145
//     and 154 while reporting success.
//   - NON-CANONICAL NUMBERED headings — "## Iteration 70: Concurrent Recording",
//     "## Iteration 215 (revision 2) — ...", "## Iteration 147", and the legacy H3s
//     191 earned. The ones that sit mid-body are invisible to the frame rule.
//   - DOUBLED PREFIXES — "## Iteration 40 — Iteration 40 — ...". These once PASSED
//     the canonicity oracle, because the round trip was idempotent over the
//     corruption and no other rule could find them. FormatIterationHeader now
//     strips a doubled prefix, so they are non-canonical too; the class is kept
//     because it NAMES the defect, and is reported in preference to the generic one.
//
// FENCE-AWARE, and that is not decoration: this project's own documents quote
// iteration headers inside code fences as SAMPLE TEXT (the wrap template does it
// while explaining the rule). A naive scan counts those and invents findings.
//
// 🔴 IT DOES NOT CHECK FOR DUPLICATE ITERATION NUMBERS, AND THAT RULE WAS DELETED
// AFTER ITS FIRST LIVE RUN — read this before re-adding it.
//
// The first version flagged 17 duplicates across 6 projects. Checking the ARTIFACT
// instead of trusting the auditor showed every one was DELIBERATE:
//
//	## Iteration 177 — `blind-writer-vault-lock-gap` shipped; repo swept to zero hints
//	## Iteration 177 — addendum: commit 1 landed; the lint sweep staged as commit 2
//
// A session with several work units writes several narratives under one iteration
// number. That is a pattern the operator has used 15 times, not an accident, and
// NextIteration (max+1) is untroubled by it.
//
// The rule also came from a MISREADING: the plan's "no dupes" governs SESSION-NOTE
// numbering — NextIteration is max(NN)+1 scoped per (date, fingerprint) — not
// narrative headings in iterations.md. Two different artifacts.
//
// **An auditor that invents findings is worse than one that misses them, because it
// teaches you to wave off the real ones.** The rule was killed by the discipline the
// audit is built on: ask the artifact, not the code — and not the auditor either.
func auditIterationHeadings(vault *storage.Vault) ([]Finding, []string, error) {
	projects, err := vault.ListAllProjects()
	if err != nil {
		return nil, nil, fmt.Errorf("enumerate projects: %w", err)
	}

	var findings []Finding
	var unknowns []string

	for _, p := range projects {
		if !p.InProjects {
			continue
		}
		rel := "Projects/" + p.Slug + "/iterations.md"
		data, err := os.ReadFile(filepath.Join(vault.Root, filepath.FromSlash(rel)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			unknowns = append(unknowns, fmt.Sprintf("%s: cannot read iterations.md: %v", p.Slug, err))
			continue
		}

		// ONE heading definition, shared with the `iteration-headings` check
		// producer: wrapstate.ScanHeadingDefects. It is fence-aware (via the same
		// scanner this dimension has always used), it reports each defective
		// heading exactly once even when it breaks two rules, and it is where
		// the three conditions are DEFINED. Re-testing any of them here would
		// recreate the two-private-definitions defect one layer up.
		for _, d := range wrapstate.ScanHeadingDefects(string(data)) {
			findings = append(findings, Finding{
				Dimension: DimIterationHeadings,
				Artifact:  fmt.Sprintf("%s:%d", rel, d.Line),
				Detail:    iterationHeadingDetail(d),
			})
		}
	}
	return findings, unknowns, nil
}

// iterationHeadingDetail renders one heading defect as the sentence a human
// repairing the archive needs: what is wrong with THIS heading, which incident
// earned the rule, and what the writer would have emitted instead.
//
// It decides WORDING ONLY. Every condition is tested in wrapstate; nothing here
// classifies anything, which is what keeps this package from growing a second
// copy of the contract. The one thing it reads off the heading text is the
// heading LEVEL, and only to choose which incident to cite — a legacy H3 and a
// colon-punctuated H2 are the same verdict with different history behind them.
func iterationHeadingDetail(d wrapstate.HeadingDefect) string {
	switch d.Class {
	case wrapstate.DefectFrameOrphan:
		return fmt.Sprintf("%q sits on the writer's \"---\" entry frame but is not a header "+
			"FormatIterationHeader would emit. That makes it a REAL entry boundary the reader cannot "+
			"see: the narrative under it is unaddressable by number AND is served as the TAIL of the "+
			"previous entry — vp_get_iteration over-returned exactly that way for 108, 110, 125, 128, "+
			"145 and 154, and reported success each time. Expected %s.", d.Text, d.Want)

	case wrapstate.DefectNonCanonicalNumbered:
		base := fmt.Sprintf("%q is matched by the reader but is not what the writer emits. Expected %s.",
			d.Text, d.Want)
		if strings.HasPrefix(strings.TrimSpace(d.Text), "###") {
			return base + " This is the legacy H3 level; the canonical level is H2. Mixed heading " +
				"levels defeat addressing (191: a strict matcher under-counted history and reported " +
				"a fresh project on 18 iterations of real narrative). New narratives are composed by " +
				"FormatIterationHeader; the reader stays H2+H3 tolerant so legacy is still counted."
		}
		return base + " A heading the reader can find but the writer would never have written is drift " +
			"between the two halves of one contract, and drift that nothing reports gets worse."

	case wrapstate.DefectDoubledPrefix:
		return fmt.Sprintf("%q carries the \"Iteration N —\" prefix TWICE. The narrative is still "+
			"addressable — the heading carries its number — so only the title is corrupt, and the "+
			"repair is mechanical: drop the redundant prefix. These headings were written by handing "+
			"FormatIterationHeader a title that already carried the prefix; the writer now strips it, "+
			"so no new ones can appear and what is left here is archive damage. Expected %s.",
			d.Text, d.Want)

	default:
		// Unreachable while wrapstate owns the class set; a new class must not
		// vanish silently just because this switch has not been taught about it.
		return fmt.Sprintf("%q breaks the iterations.md heading contract (%s). Expected %s.",
			d.Text, d.Class, d.Want)
	}
}

// auditTaskHeadingMarkers: an H2 heading in an active task file carrying a marker
// from unresolvedStatusMarkers.
//
// It walks the tasks DIRECTORY rather than a task listing, and that is load-bearing:
// `vp_list_tasks` excludes iceboxed tasks unless include_icebox is set, and on this
// vault the active directory held 34 files where the listing returned 25. A census
// driven off the listing would have been blind to nine files and reported a clean
// number — measuring the default filter, not the vault. Iceboxed tasks live in the
// active directory, are amendable, and are in scope.
//
// Subdirectories are skipped, which is what excludes tasks/done/ and tasks/cancelled/.
// See DimTaskHeadingMarkers for why that is a ruling and not an oversight.
func auditTaskHeadingMarkers(vault *storage.Vault) ([]Finding, []string, error) {
	projects, err := vault.ListAllProjects()
	if err != nil {
		return nil, nil, fmt.Errorf("enumerate projects: %w", err)
	}

	var findings []Finding
	var unknowns []string

	for _, p := range projects {
		if !p.InProjects {
			continue
		}
		tasksDir, err := vault.TasksDir(p.Slug)
		if err != nil {
			unknowns = append(unknowns, fmt.Sprintf("%s: cannot resolve tasks dir: %v", p.Slug, err))
			continue
		}
		entries, err := os.ReadDir(tasksDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			unknowns = append(unknowns, fmt.Sprintf("%s: cannot read tasks dir: %v", p.Slug, err))
			continue
		}

		for _, e := range entries {
			// Skips done/ and cancelled/. Deliberate — see the dimension doc.
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			rel := "Projects/" + p.Slug + "/tasks/" + e.Name()
			data, err := os.ReadFile(filepath.Join(tasksDir, e.Name()))
			if err != nil {
				unknowns = append(unknowns, fmt.Sprintf("%s: cannot read: %v", rel, err))
				continue
			}
			for _, ln := range mdfence.OutsideFences(string(data)) {
				text, ok := h2HeadingText(ln.Text)
				if !ok {
					continue
				}
				hits := headingMarkers(text)
				if len(hits) == 0 {
					continue
				}
				findings = append(findings, Finding{
					Dimension: DimTaskHeadingMarkers,
					Artifact:  fmt.Sprintf("%s:%d", rel, ln.Num),
					Detail:    taskHeadingMarkerDetail(text, hits),
				})
			}
		}
	}
	return findings, unknowns, nil
}

// h2HeadingText returns the text of an H2 heading, or ok=false for anything else.
//
// Exactly "## " at column 0. "### " does not match, by ruling: the marker rule is
// scoped to H2 because H2 is the level `amend` is keyed on, and it is precisely the
// unrewritability of THAT level that makes a stale marker permanent. An H3 sits
// inside a section an amend can replace wholesale, so it is not stranded.
//
// Fence filtering happens before this is ever called — this project's task files
// carry H2-shaped lines quoted inside code fences (that is why `amend` itself is
// fence-aware), and flagging sample text would be inventing findings.
func h2HeadingText(line string) (string, bool) {
	if !strings.HasPrefix(line, "## ") {
		return "", false
	}
	return strings.TrimSpace(line[len("## "):]), true
}

// headingMarkers returns the distinct markers present in a heading, in the order the
// heading mentions them.
//
// 🔴 IT SCANS THE WHOLE HEADING, AND THE CLASS B PREFIX EXEMPTION IS PREFIX-ONLY.
// `Decision (iter 205)` and `Phase 1 progress (2026-08-16, landed abcdef0)` are
// provenance — they record WHEN, not what is now, so they cannot go stale and they
// carry no marker, so they are not reported. But a provenance PREFIX does not bless
// what follows the dash: `Decision (2026-08-01) — still UNCOMMITTED` is a finding.
// Exempting a heading because it opens in a Class B shape is how a check blesses
// every claim appended after it, and that is the one implementation shortcut this
// dimension must not take.
func headingMarkers(heading string) []string {
	if taskHeadingMarkerRE == nil {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	for _, m := range taskHeadingMarkerRE.FindAllString(heading, -1) {
		// Exact-match dedupe: matching is case-sensitive, so two spellings that
		// differ only in case are two different DECLARED markers, not one marker
		// seen twice. Folding them here would re-introduce, in the reporting, the
		// case-blindness the matcher was just ruled out of.
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// taskHeadingMarkerDetail renders one finding as the sentence a human repairing the
// task needs: which heading, which marker, why a heading is the one place this cannot
// be fixed in passing, and the conversion that is already proven in production.
func taskHeadingMarkerDetail(heading string, markers []string) string {
	return fmt.Sprintf("H2 %q carries the unresolved-status marker(s) %s. A marker asserting an open "+
		"state is a derived value stored by hand, and a heading is the one place `vp_manage_task "+
		"action: amend` CANNOT revise it: amend is keyed on the heading text, so amending under the "+
		"old text leaves the stale heading and amending under a corrected one appends a second "+
		"section and leaves both. Convert it to immutable provenance — the production pattern is "+
		"UNCOMMITTED -> \"landed <sha>\" (iteration 304, eight headings in first-principles.md), "+
		"which cannot go stale because it records when rather than what is now. Markers are "+
		"DECLARED AND EXTENSIBLE, never complete: %s.",
		heading, strings.Join(markers, ", "), strings.Join(unresolvedStatusMarkers, ", "))
}

// relTo renders an absolute path as vault-relative. NEVER write an absolute vault
// path into a vault document: the vault syncs to every machine and lives somewhere
// different on each, so an absolute path is a fact about the writing host and false
// everywhere else.
func relTo(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}
