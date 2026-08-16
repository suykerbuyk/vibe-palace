// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/check"
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
//   - DOUBLED PREFIXES — "## Iteration 40 — Iteration 40 — ...". These PASS the
//     canonicity oracle, because the round trip is idempotent over the corruption,
//     so neither of the other two rules can ever find them.
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
		return fmt.Sprintf("%q carries the \"Iteration N —\" prefix TWICE. This is invisible to every "+
			"other rule here: titleFromHeader strips exactly one prefix and FormatIterationHeader "+
			"re-adds exactly one, so the corruption is a FIXED POINT of the round trip and the "+
			"canonicity oracle reports it as perfectly canonical. A check built as re-emit-and-compare "+
			"is structurally blind to whatever it is idempotent over. Expected %s.", d.Text, d.Want)

	default:
		// Unreachable while wrapstate owns the class set; a new class must not
		// vanish silently just because this switch has not been taught about it.
		return fmt.Sprintf("%q breaks the iterations.md heading contract (%s). Expected %s.",
			d.Text, d.Class, d.Want)
	}
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
