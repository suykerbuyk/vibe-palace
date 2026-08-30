// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"fmt"
	"io/fs"
	"os"
	"path"
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

	// DimPalaceStoreDrawers — a project with a palace/ store must actually hold
	// drawer records. Earned by palace-store-exists-with-empty-drawers-passes-coherence:
	// DimProjectTreeCoherence asks only whether the two trees agree a project EXISTS,
	// so a project present in BOTH trees whose palace/<slug>/ carries no drawers at all
	// is Complete() == true and produces no finding — while `vp search` walks
	// wings × rooms × drawers and finds nothing there. This dimension is the inverse of
	// that one, not a duplicate: coherence audits presence, this audits contents.
	DimPalaceStoreDrawers = "palace-store-drawers"

	// DimTaskPreamble — an ACTIVE task file may not carry text between its header
	// block and its first H2. Earned by
	// vaultaudit-does-not-flag-a-claim-bearing-preamble: the preamble is the region
	// `vp_manage_task action: overwrite` was built to repair, and until now NOTHING
	// measured it. A claim parked above the first heading reaches every agent at
	// session start, and no instrument reported it.
	//
	// 🔴 IT IS NOT A DUPLICATE OF DimTaskHeadingMarkers, AND THE TWO ARE DISJOINT BY
	// CONSTRUCTION. That one walks H2 heading TEXT for unresolved-status tokens; this
	// one reads the region ABOVE the first H2, which by definition contains no H2 and
	// which that dimension therefore cannot see at all. Neither can ever report the
	// same byte as the other.
	//
	// The predicate is storage.MovePreambleUnderContext, whose rewritten string is
	// DISCARDED here — only the outcome matters. That is deliberate: the migrator
	// moves ALL preamble text with no claim-versus-provenance classifier (no such rule
	// survives contact with real files — see its doc comment), which is exactly what
	// makes a NON-EMPTY preamble mechanically the finding. A "claim-bearing" heuristic
	// would be unmutatable, and there is no such predicate in this tree to borrow.
	//
	// PreambleEmpty is the shape CreateTask produces unconditionally, so the
	// structural zero is already guaranteed for every task born from here on.
	//
	// PreambleSkippedNoH2 is reported as its OWN class with a DISTINCT detail, never
	// folded into the ordinary non-empty case. Under that outcome the region
	// definition degenerates — "everything above the first H2" becomes the entire body
	// — so there is no measured preamble to report, and a detail that reported one
	// would be describing a region the predicate refused to define.
	//
	// 🔴 SCOPE IS ACTIVE TASKS ONLY, on the same ruling DimTaskHeadingMarkers records:
	// OverwriteTaskFile and every other sanctioned task writer refuse an archived
	// task, so a finding under tasks/done/ or tasks/cancelled/ would be unrepairable by
	// every path and would be permanent, un-actionable red.
	DimTaskPreamble = "task-preamble"
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
	// Prints every palace/ project whose drawer store holds nothing to walk. It does
	// NOT distinguish an absent drawers/ directory from a present-but-empty one — the
	// two findings' DETAILS carry that, because a shell one-liner that split them would
	// be longer than the rule it reproduces. It approximates "no drawer RECORDS" as "no
	// non-empty drawers.jsonl", which is exact for every store this vault has produced.
	// It deliberately ignores Projects/<slug>/iterations.md and
	// Projects/<slug>/sessions/*.md: those are SEPARATE ingest sources for search.Rebuild
	// and neither fills an empty drawer store.
	EvidencePalaceStoreDrawers = `for d in palace/*/; do s=$(basename "$d"); ` +
		`find "palace/$s/drawers" -name drawers.jsonl -size +0c -print -quit 2>/dev/null | grep -q . || echo "$s"; ` +
		`done   # palace/ projects with an empty or absent drawer store; iterations.md and sessions/ are separate corpora`
	// The migrator's own dry run. This is the strongest evidence command available to
	// this dimension: it is an INDEPENDENT second walk over the same corpus by a
	// different caller — cmd/vp/cmd_migrate_task_preamble.go enumerates Projects/*/tasks
	// itself, while this dimension goes through vault.ListAllProjects — running the same
	// predicate, so the two agreeing is a real cross-check rather than one
	// implementation quoting itself. A shell grep cannot stand in here: the region is
	// defined against storage.headerBlock and is fence-aware, and a grep faking either
	// invents findings on the markdown these task bodies routinely quote.
	EvidenceTaskPreamble = `vp migrate task-preamble   # REPORT ONLY, writes nothing. ` +
		`MOVE rows are this dimension's findings; SKIP rows are its no-H2 class`
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

// auditPalaceStoreDrawers: a project with a palace/ store that holds NO drawer
// records.
//
// DimProjectTreeCoherence answers "do the two trees agree this project exists?" and
// stops there. A project present in BOTH trees satisfies it — ProjectPresence.Complete
// is true — even when palace/<slug>/ has no drawers directory at all, or has one with
// nothing in it. `vp search` walks exactly ListWings × ListRooms × ListDrawers, the
// composition below, so that store contributes NOTHING and the audit says nothing.
// This is the inverse of coherence, not a duplicate of it: coherence audits presence,
// this audits contents.
//
// 🔴 THE WORDING OF BOTH DETAILS IS DELIBERATELY RESTRAINED, AND MUST STAY THAT WAY.
// Neither may claim the project is UNSEARCHABLE or has no searchable corpus.
// Projects/<slug>/iterations.md AND the bodies of Projects/<slug>/sessions/*.md are
// INDEPENDENT ingest sources for search.Rebuild (see the "Second corpus source" and
// "Third corpus source" blocks in internal/search/engine.go), and the index is dropped
// only when ALL THREE sources come back empty (Rebuild's `len(vecs) == 0` branch) — so
// an empty drawer store does not imply an unsearchable project. DimProjectTreeCoherence
// gets to say UNSEARCHABLE because a project with no palace/ store has no drawer index
// at all; this dimension does not. Report what is missing (the drawer half), never the
// consequence that would follow only if the other corpora were also empty. A future
// reader will otherwise "fix" this wording back.
//
// 🔴 THE GATE IS p.InPalace ALONE — p.InProjects is deliberately NOT required. A
// palace store with no history under Projects/ is already a project-tree-coherence
// finding, and the overlap is accepted on purpose: the two details say DIFFERENT
// things ("no history was ever written here" versus "the drawer store is empty"), and
// suppressing this one on that project would make the dimension's answer depend on an
// unrelated tree.
//
// HasPalaceStore is asked FIRST and is the discriminator. ListWings returns (nil, nil)
// for BOTH "no drawers directory" and "a drawers directory with no wings"
// (internal/storage/drawers.go:296-315), so without that stat the two findings could
// not be told apart. A project the auditor could not look at lands in unknowns and is
// neither reported nor passed — unknown is not a shade of pass (audit.go:21-25).
func auditPalaceStoreDrawers(vault *storage.Vault) ([]Finding, []string, error) {
	projects, err := vault.ListAllProjects()
	if err != nil {
		return nil, nil, fmt.Errorf("enumerate projects: %w", err)
	}

	var findings []Finding
	var unknowns []string

	for _, p := range projects {
		if !p.InPalace {
			// No palace tree at all is project-tree-coherence's finding, not ours.
			continue
		}

		has, err := vault.HasPalaceStore(p.Slug)
		if err != nil {
			// Could not even stat the store. Say so; do not report, do not pass.
			unknowns = append(unknowns, fmt.Sprintf("%s: cannot stat %s: %v",
				p.Slug, path.Join("palace", p.Slug, "drawers"), err))
			continue
		}
		if !has {
			findings = append(findings, Finding{
				Dimension: DimPalaceStoreDrawers,
				Artifact:  p.Slug,
				Detail: fmt.Sprintf("has a palace/ store but NO drawers directory (%s is absent), so "+
					"its sessions were never drawer-indexed and the drawer half of `vp search` "+
					"covers nothing for it. %s", path.Join("palace", p.Slug, "drawers"),
					storeContextNote(p)),
			})
			continue
		}

		// The store exists. Walk the SAME composition search.Rebuild walks and count
		// actual Drawer records — a zero-length or absent drawers.jsonl contributes
		// zero, which is the whole point: files are not records.
		records, blind := countDrawerRecords(vault, p.Slug)
		if blind != "" {
			unknowns = append(unknowns, blind)
			continue
		}
		if records == 0 {
			findings = append(findings, Finding{
				Dimension: DimPalaceStoreDrawers,
				Artifact:  p.Slug,
				Detail: fmt.Sprintf("has a palace/ store whose drawers directory is PRESENT BUT EMPTY "+
					"(%s exists and the wing × room × drawer walk yields 0 drawer records), so its "+
					"sessions were never drawer-indexed and the drawer half of `vp search` covers "+
					"nothing for it. %s", path.Join("palace", p.Slug, "drawers"),
					storeContextNote(p)),
			})
		}
	}
	return findings, unknowns, nil
}

// storeContextNote spells out, for ONE project, what an empty drawer store does and
// does not imply. It branches on p.InProjects because BOTH of the sentences it can
// emit are false for a palace-only project, and a detail that asserts them anyway
// would be the same false report this dimension exists to catch:
//
//   - "project-tree-coherence cannot see this" is only true when the project is in
//     both trees. A palace store with no Projects/ history is NOT Complete(), so
//     coherence reports it too — loudly, and first.
//   - "iterations.md may still be indexed" presumes a Projects/<slug>/ tree to hold
//     one. A palace-only project has no such file to be searchable by instead — and
//     for the same reason it has no Projects/<slug>/sessions/ notes either.
//
// The gate above is p.InPalace alone and stays that way; this is how the detail tells
// the truth for both shapes that gate admits.
func storeContextNote(p storage.ProjectPresence) string {
	if !p.InProjects {
		return fmt.Sprintf("project-tree-coherence reports this project separately, for having no "+
			"history under Projects/ at all — and there is neither a Projects/%s/iterations.md "+
			"nor a Projects/%s/sessions/ to carry the corpus instead, so nothing indexes it.",
			p.Slug, p.Slug)
	}
	return fmt.Sprintf("project-tree-coherence cannot see this: the project is present in both "+
		"trees, so it is Complete(). This is NOT a claim that the project is unsearchable — "+
		"Projects/%s/iterations.md and the bodies of Projects/%s/sessions/*.md are separate "+
		"ingest sources and may still be indexed.", p.Slug, p.Slug)
}

// countDrawerRecords walks one project's drawer store the way search.Rebuild does and
// returns how many Drawer RECORDS it holds. A non-empty second return is the reason
// the walk is undecidable, in which case the count is meaningless and the caller must
// record an unknown rather than a pass.
//
// It counts records, not files: a room whose drawers.jsonl is absent or zero-length
// contributes nothing, which is exactly the hole this dimension exists to report.
func countDrawerRecords(vault *storage.Vault, slug string) (int, string) {
	wings, err := vault.ListWings(slug)
	if err != nil {
		return 0, fmt.Sprintf("%s: cannot list wings: %v", slug, err)
	}

	records := 0
	for _, wing := range wings {
		rooms, err := vault.ListRooms(slug, wing)
		if err != nil {
			return 0, fmt.Sprintf("%s: cannot list rooms in wing %s: %v", slug, wing, err)
		}
		for _, room := range rooms {
			drawers, err := vault.ListDrawers(slug, wing, room)
			if err != nil {
				return 0, fmt.Sprintf("%s: cannot list drawers in %s/%s: %v", slug, wing, room, err)
			}
			records += len(drawers)
		}
	}
	return records, ""
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

// auditTaskPreamble: an ACTIVE task file carrying anything between its header block
// and its first H2.
//
// The walk is auditTaskHeadingMarkers' walk, structurally line for line, and for the
// same reasons: the tasks DIRECTORY rather than a task listing (vp_list_tasks hides
// the icebox by default, and an iceboxed task is an active file with a preamble),
// subdirectories never descended (which is what excludes tasks/done/ and
// tasks/cancelled/ — a ruling, see the dimension doc), and a read error recorded as an
// UNKNOWN rather than as either a finding or a pass.
//
// The predicate is storage.MovePreambleUnderContext and nothing else. Its rewritten
// string is DISCARDED — only the outcome is read, so this dimension can never write,
// and it can never disagree with the migrator about what a preamble is.
func auditTaskPreamble(vault *storage.Vault) ([]Finding, []string, error) {
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

			before := string(data)
			after, outcome := storage.MovePreambleUnderContext(before)
			switch outcome {
			case storage.PreambleEmpty:
				// The structural zero CreateTask produces. Not a finding.
				continue
			case storage.PreambleSkippedNoH2:
				findings = append(findings, Finding{
					Dimension: DimTaskPreamble,
					Artifact:  rel,
					Detail:    taskPreambleNoH2Detail(before),
				})
			default:
				findings = append(findings, Finding{
					Dimension: DimTaskPreamble,
					Artifact:  rel,
					Detail:    taskPreambleDetail(taskPreambleText(before, after)),
				})
			}
		}
	}
	return findings, unknowns, nil
}

// firstUnfencedH2 returns the 1-indexed line number of the first H2 OUTSIDE a code
// fence, or 0 when the content has none.
//
// The H2 test matches storage.isH2Line — "## " after trimming leading space, so an
// indented heading still counts — and the fence filter is mdfence. That pair is
// exactly what MovePreambleUnderContext uses to locate the boundary; any other rule
// here would answer a different question than the predicate answered.
func firstUnfencedH2(content string) int {
	for _, l := range mdfence.OutsideFences(content) {
		if strings.HasPrefix(strings.TrimSpace(l.Text), "## ") {
			return l.Num
		}
	}
	return 0
}

// taskPreambleText recovers the text the migrator would move, by reading the
// migrator's OWN output rather than re-deriving the region here.
//
// MovePreambleUnderContext copies the header block through byte-for-byte and then
// writes the conventional heading, so the longest run of leading LINES that `before`
// and `after` share IS that header block — give or take one blank line, which the trim
// below removes either way. The region runs from there to the first unfenced H2, and
// the migrator trims it the same way before moving it.
//
// 🔴 THE ALTERNATIVE WAS TO RE-IMPLEMENT headerBlock's "**Field:**" RUN HERE, and it is
// refused. That would put a second hand-maintained copy of a storage rule inside the
// dimension whose whole subject is text drifting out of agreement with the writer that
// produced it. storage exports no accessor for the boundary, and this dimension is not
// a reason to add one — the detail only needs a size and an excerpt.
func taskPreambleText(before, after string) string {
	h2 := firstUnfencedH2(before)
	if h2 <= 1 {
		return ""
	}
	beforeLines := strings.Split(before, "\n")
	afterLines := strings.Split(after, "\n")
	end := h2 - 1 // 0-indexed line of the first H2; the region ends just above it
	start := 0
	for start < end && start < len(afterLines) && beforeLines[start] == afterLines[start] {
		start++
	}
	return strings.TrimSpace(strings.Join(beforeLines[start:end], "\n"))
}

// taskPreambleDetail renders one non-empty preamble as the sentence a human repairing
// the task needs: how large the region is, enough of it to recognise without opening
// the file, and which writer can actually reach it.
//
// Size and excerpt are RECOMPUTED on every run rather than recorded anywhere, for the
// reason DimKGPortability's doc gives about counts: a number written down once is false
// by the next run, and a detail a reader cannot reproduce is a census, not evidence.
func taskPreambleDetail(preamble string) string {
	return fmt.Sprintf("A non-empty preamble sits between the header block and the first H2: "+
		"%d line(s), %d byte(s), opening %q. That region carries no H2 of its own, so no section "+
		"name addresses it and `vp_manage_task action: amend` — which is keyed on heading text — "+
		"can never revise it; only `action: overwrite` or `vp migrate task-preamble --apply` can "+
		"reach it. Whatever it asserts is served to every agent at session start, ahead of a body "+
		"that may already have superseded it. Move it under \"## %s\" and the file returns to the "+
		"structural zero CreateTask already guarantees for every new task.",
		len(strings.Split(preamble, "\n")), len(preamble),
		firstLineExcerpt(preamble, 72), storage.ConventionalFirstHeading)
}

// taskPreambleNoH2Detail renders the PreambleSkippedNoH2 class, which
// MovePreambleUnderContext returns from TWO distinct paths
// (internal/storage/preamble_migrate.go:88-102) and which this must not describe
// falsely:
//
//   - firstH2 < 0 — no "## " outside a code fence anywhere in the file. A "## " that
//     appears only INSIDE a fence is sample text, and task bodies quote markdown
//     constantly, so it does not count as a heading.
//   - firstH2 < hdrEnd — an unfenced "## " DOES exist, but above the end of the header
//     block. The migrator calls that degenerate and leaves it alone "rather than
//     guessing what the file meant".
//
// 🔴 A DETAIL SAYING "THIS FILE HAS NO ## HEADING" WOULD BE FALSE ON THE SECOND PATH,
// and a finding that asserts something untrue about its own artifact is the precise
// defect class this dimension exists to close. What the two paths share — and what this
// says — is that there is no USABLE first H2 above which a preamble region could be
// defined, so the region degenerates and the file is left alone.
func taskPreambleNoH2Detail(content string) string {
	where := "there is no \"## \" heading outside a code fence anywhere in the file " +
		"(a \"## \" inside a fence is sample text, not a heading)"
	if n := firstUnfencedH2(content); n > 0 {
		where = fmt.Sprintf("an unfenced \"## \" heading DOES exist, at line %d, but it sits ABOVE the "+
			"end of the header block (the H1 and the contiguous \"**Field:**\" run below it), so it "+
			"cannot bound a region that begins beneath that block — the migrator leaves such a file "+
			"alone rather than guess what it meant", n)
	}
	return fmt.Sprintf("No usable first H2: %s. \"Everything above the first H2\" therefore "+
		"degenerates to the ENTIRE task body, so there is NO measured preamble here and none is "+
		"reported — `vp migrate task-preamble` SKIPS this file rather than rewrite a whole task body "+
		"end to end, which is not what moving a preamble means. The repair is to give the file a real "+
		"first heading (\"## %s\") with `vp_manage_task action: overwrite`; the body then becomes "+
		"addressable by `amend`, and this dimension can measure the region for the first time. "+
		"CreateTask emits the conventional heading unconditionally and a whole-file write with no "+
		"unfenced \"## \" is now refused, so nothing new can join this class.",
		where, storage.ConventionalFirstHeading)
}

// firstLineExcerpt is the truncated first non-blank line of a region: enough for a
// reader to recognise the text without opening the file, and no more. The truncation is
// hard rather than generous — a detail is ONE LINE in a report, not a document.
func firstLineExcerpt(s string, max int) string {
	var line string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			line = t
			break
		}
	}
	if r := []rune(line); len(r) > max {
		return string(r[:max]) + "…"
	}
	return line
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
