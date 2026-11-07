// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// This file is the ONE home of the backfill candidate predicate for
// capture-note-archive-link-never-closes Phase 4. A pair (note N, manifest M)
// is a candidate iff, within one project, it matches EITHER class:
//
//	CLASS 1 — UNSTAMPED CALLER-KEYED
//	N.session_key_source == "caller"  ∧  N.session_key == M.session_id
//	∧  N.archive_session_id == ""     ∧  M.vault_rel_session_note == ""
//
//	CLASS 2 — HALF-LINKED
//	N.archive_session_id == M.session_id  ∧  N.archive == rel(M)
//	∧  M.vault_rel_session_note == ""
//
// 🔴 CLASS 2 IS NOT A SOFTENING OF CLASS 1. It is a different, stronger kind of
// evidence. Class 1 DERIVES identity from one key match, which is exactly why it
// needs session_key_source == "caller": a minted key is a random UUID, and one
// that collides with a manifest's session id identifies nothing. Class 2 derives
// nothing — the note already RECORDS the identity (archive_session_id) and
// independently RECORDS the exact manifest (archive:, an exact vault-relative
// path equality, never a prefix and never a session-level match). Two pointers,
// both written by the production path that wrote the manifest, must agree. A
// note naming a DIFFERENT manifest of the same session fails the second clause;
// a note of a different session fails the first. Class 2 therefore needs no
// session_key clause at all, and adding one would only exclude the Codex
// receipts (whose key is "codex-session-end:<id>", not the bare id) that are the
// whole reason it exists.
//
// The classes are DISJOINT PER NOTE — class 1 requires archive_session_id == ""
// and class 2 requires archive_session_id == M.session_id, so no single note can
// match both. They are NOT disjoint per SESSION: one session may hold an
// unstamped caller-keyed note and a half-linked one at once. Class 1 wins there,
// because its note is the one that still needs stamping, and because that order
// makes class 2 purely ADDITIVE — it can introduce new candidates but can never
// re-route a pair that already matched before this predicate was widened.
//
// Class is carried on the candidate because the APPLIER must branch on it: a
// class-2 note is already complete and must not be written to.
//
// The audit annotation, `vp archive backfill`, and the applier ALL derive from
// this file — the note-side halves live in storage
// (ListCallerKeyedUnstampedNotes, ListArchiveForwardLinkedNotes), the
// manifest-side half in archive (ListEntries), and the pairing here, because
// vaultaudit is the one package that already imports both and all three
// consumers already import vaultaudit. Two implementations of one rule silently
// diverge (the lesson of the since-deleted templates.Classify, a dead parallel
// copy of reconcile's live decision table); do not copy this predicate anywhere.

// BackfillClass names which half of the predicate above a candidate matched.
// The applier branches on it, so it is data, not description.
type BackfillClass string

const (
	// ClassUnstampedCallerKeyed is class 1: the note records the session id as a
	// caller-pushed key and carries no archive_session_id. The applier must
	// STAMP the note (identity + forward link) and then write the back-link.
	ClassUnstampedCallerKeyed BackfillClass = "unstamped-caller-keyed"

	// ClassHalfLinked is class 2: the note already names this exact manifest and
	// already records this session id, so the note -> transcript direction is
	// closed and only the manifest's own back-link is missing. The applier must
	// write the MANIFEST ONLY and must not touch a note byte.
	ClassHalfLinked BackfillClass = "half-linked"
)

// BackfillCandidate is one RECOVERABLE session: a stranded transcript manifest
// that a note of the same project provably describes. Class says how that was
// established — a caller-pushed session_key on an unstamped note (class 1), or
// a note that already names this exact manifest (class 2). Either way the pair
// is a derivation from what the vault recorded, not a guess.
type BackfillCandidate struct {
	Project   string        `json:"project"`
	SessionID string        `json:"session_id"`
	Class     BackfillClass `json:"class"`
	NoteRels  []string      `json:"notes"`

	// StrandedManifests lists every stranded manifest of this session,
	// vault-relative, oldest first (archive.ListEntries order).
	// TargetManifest is the NEWEST stranded one — the deterministic rule the
	// applier follows. Older stranded manifests of the same session stay
	// stranded by design and fall into the accepted-debt set: the note can only
	// point one place, and it points at the fullest transcript.
	//
	// For CLASS 2 the newest stranded manifest A NOTE ACTUALLY NAMES is the
	// target, which is not always the newest stranded manifest overall: a
	// manifest no note describes is not recoverable, so pointing it at a note
	// would be the guess this predicate exists to avoid.
	StrandedManifests []string `json:"stranded_manifests"`
	TargetManifest    string   `json:"target_manifest"`

	// Canonical is the note the manifest should point back at, ranked by
	// storage.PreferCanonicalNote — a real note over an auto-capture stub, then
	// later (date, iteration). Set for CLASS 2 only, where the applier writes it
	// directly because no note needs stamping. Class 1's canonical is chosen
	// inside storage.BackfillArchiveLink, under the sessions-directory lock it
	// takes to do the stamping, and must not be second-guessed from out here.
	Canonical string `json:"canonical,omitempty"`
}

// BackfillCandidates enumerates candidates VAULT-GLOBAL — the stranded set
// spans projects. A transcripts directory that exists but cannot be read is an
// ERROR, never "no candidates": archive.ListEntries (on os.ReadDir) returns
// that read error rather than the empty listing filepath.Glob used to swallow
// it into. A genuinely absent directory is an empty listing, not an error.
func BackfillCandidates(v *storage.Vault) ([]BackfillCandidate, error) {
	projects, err := v.ListAllProjects()
	if err != nil {
		return nil, fmt.Errorf("enumerate projects: %w", err)
	}

	var out []BackfillCandidate
	for _, p := range projects {
		if !p.InProjects {
			continue
		}
		entries, err := archive.ListEntries(v.Root, p.Slug)
		if err != nil {
			return nil, fmt.Errorf("%s: list transcripts: %w", p.Slug, err)
		}
		cands, err := backfillCandidatesFromEntries(v, p.Slug, entries)
		if err != nil {
			return nil, err
		}
		out = append(out, cands...)
	}
	return out, nil
}

// backfillCandidatesFromEntries pairs one project's already-listed manifest
// entries against its caller-keyed unstamped notes. The audit dimension calls
// this directly with the entries it already loaded, so the predicate never
// re-lists a directory the caller just listed.
func backfillCandidatesFromEntries(v *storage.Vault, project string, entries []*archive.Entry) ([]BackfillCandidate, error) {
	// Manifest side: stranded manifests grouped by session id, preserving
	// ListEntries' oldest-first order so "last" is "newest".
	stranded := make(map[string][]string)
	for _, e := range entries {
		if e.Manifest.VaultRelSessionNote != "" || e.Manifest.SessionID == "" {
			continue
		}
		rel := archive.VaultRelPath(v.Root, e.ManifestPath)
		stranded[e.Manifest.SessionID] = append(stranded[e.Manifest.SessionID], rel)
	}
	if len(stranded) == 0 {
		return nil, nil
	}

	// Note side, CLASS 1: caller-keyed and never stamped.
	notes, err := v.ListCallerKeyedUnstampedNotes(project)
	if err != nil {
		return nil, fmt.Errorf("%s: scan session notes: %w", project, err)
	}
	notesByKey := make(map[string][]string)
	for _, n := range notes {
		notesByKey[n.SessionKey] = append(notesByKey[n.SessionKey], n.NoteRel)
	}

	// Note side, CLASS 2: already naming a manifest. Indexed by the EXACT
	// vault-relative manifest path the note stores, so the pairing below is a
	// map hit on a full path and never a session-level match.
	fwd, err := v.ListArchiveForwardLinkedNotes(project)
	if err != nil {
		return nil, fmt.Errorf("%s: scan forward-linked session notes: %w", project, err)
	}
	fwdByRel := make(map[string][]storage.ArchiveForwardLinkedNote)
	for _, n := range fwd {
		fwdByRel[n.ArchiveRel] = append(fwdByRel[n.ArchiveRel], n)
	}

	var out []BackfillCandidate
	for sessionID, manifests := range stranded {
		// CLASS 1 FIRST. A session can hold notes of both kinds — the classes
		// are disjoint per NOTE, not per session — and class 1 is the one whose
		// note still needs its identity and forward link stamped. Taking it
		// first keeps the pre-existing behaviour for every pair that already
		// matched, so class 2 can only ADD candidates, never re-route one.
		if noteRels, ok := notesByKey[sessionID]; ok {
			out = append(out, BackfillCandidate{
				Project:           project,
				SessionID:         sessionID,
				Class:             ClassUnstampedCallerKeyed,
				NoteRels:          noteRels,
				StrandedManifests: manifests,
				TargetManifest:    manifests[len(manifests)-1],
			})
			continue
		}

		// CLASS 2. Walk this session's stranded manifests oldest-first and keep
		// the LAST one some note actually names, so the target is the newest
		// NAMED manifest rather than the newest manifest. Both clauses are
		// checked here: the map key is N.archive == rel(M), and the loop body
		// tests N.archive_session_id == M.session_id.
		var target string
		var canonical storage.ArchiveForwardLinkedNote
		var namedRels []string
		for _, rel := range manifests {
			var best storage.ArchiveForwardLinkedNote
			found := false
			var rels []string
			for _, n := range fwdByRel[rel] {
				if n.ArchiveSessionID != sessionID {
					continue
				}
				rels = append(rels, n.NoteRel)
				if !found || storage.PreferCanonicalNote(best, n) {
					best, found = n, true
				}
			}
			if !found {
				continue
			}
			target, canonical, namedRels = rel, best, rels
		}
		if target == "" {
			continue
		}
		sort.Strings(namedRels)
		out = append(out, BackfillCandidate{
			Project:           project,
			SessionID:         sessionID,
			Class:             ClassHalfLinked,
			NoteRels:          namedRels,
			StrandedManifests: manifests,
			TargetManifest:    target,
			Canonical:         canonical.NoteRel,
		})
	}
	// Deterministic output: a report that reorders its own rows between runs is
	// not diffable.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out, nil
}

// ApplyBackfillResult reports exactly what ApplyBackfill wrote — and reports
// "nothing to do" honestly via NothingToDo, distinguishing its two shapes.
type ApplyBackfillResult struct {
	Project        string   `json:"project"`
	SessionID      string   `json:"session_id"`
	TargetManifest string   `json:"target_manifest,omitempty"`
	NotesUpdated   []string `json:"notes_updated,omitempty"`
	Canonical      string   `json:"canonical,omitempty"`

	// NothingToDo carries the human-readable reason when no write happened.
	// Empty means work was done. It is a field, not an error: an idempotent
	// re-run is an ordinary, expected outcome, and the caller already approved
	// the operation — but WHY nothing happened must never be guessed.
	NothingToDo string `json:"nothing_to_do,omitempty"`
}

// ApplyBackfill applies ONE candidate pair: it is the code path behind both
// `vp archive link` and `vp_archive_link`, and invoking it IS the human
// approval for that pair — there is deliberately no bulk mode.
//
// Sequence: stamp + forward-link every caller-keyed note of the session under
// the sessions-directory lock (storage.BackfillArchiveLink), then — after that
// lock is released — write the manifest back-link to the canonical note
// (archive.LinkSessionNote, which takes the MANIFEST's own per-path vaultlock).
// Sequential, never nested — and now load-bearing rather than incidental: both
// locks are blocking, timeout-free LOCK_EX, so holding the sessions-directory
// lock across the manifest acquire would create a real lock order for a future
// writer to invert. Release, then link.
func ApplyBackfill(v *storage.Vault, project, sessionID string) (*ApplyBackfillResult, error) {
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}

	res := &ApplyBackfillResult{Project: project, SessionID: sessionID}

	// archive.ListEntries owns transcripts-dir blindness: an unreadable tree is
	// an error (not the empty listing filepath.Glob swallowed a permission error
	// into), while an absent tree is an empty listing. An absent or empty tree
	// therefore falls through to the honest "no manifest for session id" below.
	entries, err := archive.ListEntries(v.Root, project)
	if err != nil {
		return nil, fmt.Errorf("list transcripts: %w", err)
	}

	// The applier selects the manifest by the PREDICATE, never by
	// archive.ResolveEntry: ResolveEntry resolves multiple matches
	// newest-CapturedAt-wins with no regard to vault_rel_session_note, so on a
	// multi-manifest session it can pick a manifest the report never named —
	// and the reported finding would never clear.
	var strandedEntries []*archive.Entry
	sawSession := false
	linkedRel := ""
	linkedNote := ""
	for _, e := range entries {
		if e.Manifest.SessionID != sessionID {
			continue
		}
		sawSession = true
		if e.Manifest.VaultRelSessionNote == "" {
			strandedEntries = append(strandedEntries, e)
		} else if linkedRel == "" {
			linkedRel = archive.VaultRelPath(v.Root, e.ManifestPath)
			linkedNote = e.Manifest.VaultRelSessionNote
		}
	}
	if !sawSession {
		return nil, fmt.Errorf("no manifest for session id %q in project %q", sessionID, project)
	}

	// Which CLASS is this pair? Re-derived from the one predicate rather than
	// re-decided here: a second copy of the class test is the divergence this
	// whole task exists to close.
	//
	// 🔴 RESOLVED BEFORE THE already-linked SHORT-CIRCUIT BELOW, and the order is
	// the fix for a defect, not a style choice. The short-circuit fires on ANY
	// sibling manifest of the session carrying a back-link. A class-2 target is
	// half-linked INDEPENDENTLY — a note names that manifest and only its own
	// reverse pointer is missing — so a linked sibling says nothing about it.
	// With the short-circuit first, `vp archive backfill` listed such a pair as
	// recoverable and `vp archive link` then refused it, naming the sibling and
	// leaving the target's vault_rel_session_note empty: a report naming a pair
	// the applier declines, which is the exact shape of the class-2 defect this
	// unit was filed to close, reintroduced inside the fix for it. Found by the
	// adversarial review, 2026-11-07.
	//
	// The class-1 short-circuit is NOT weakened by this. Class 1 selects the
	// newest stranded manifest and has no independent evidence for any older
	// one, so "this session already back-links somewhere, stop" remains right
	// for it — and a re-run after a class-2 application finds the target linked,
	// produces no candidate, and falls through to that same branch.
	cands, err := backfillCandidatesFromEntries(v, project, entries)
	if err != nil {
		return nil, err
	}
	var cand *BackfillCandidate
	for i := range cands {
		if cands[i].SessionID == sessionID {
			cand = &cands[i]
			break
		}
	}

	// CLASS 2 — HALF-LINKED. The note side is ALREADY COMPLETE: it records this
	// session id and already names this exact manifest. The only missing write
	// is the manifest's own back-link, so this branch writes the MANIFEST ONLY
	// and must not touch a note byte.
	//
	// It deliberately does NOT call BackfillArchiveLink. That applier matches on
	// the note's session_key against the manifest's session_id, and a Codex
	// receipt's key is "codex-session-end:<id>" — so it would report Found=false
	// and this function would error "not derivable" for a pair the report just
	// listed as recoverable. Routing class 2 through it is the defect this
	// branch exists to avoid, not an optimisation.
	//
	// LOCKING: no sessions-directory lock is taken at all, because nothing under
	// it is written. Only archive.LinkSessionNote's per-manifest vaultlock is
	// acquired, so the sequential-never-nested invariant below is satisfied
	// trivially rather than by ordering.
	if cand != nil && cand.Class == ClassHalfLinked {
		res.TargetManifest = cand.TargetManifest
		res.Canonical = cand.Canonical
		manifestPath := filepath.Join(v.Root, filepath.FromSlash(cand.TargetManifest))
		if err := archive.LinkSessionNote(v.Root, manifestPath, cand.Canonical); err != nil {
			return nil, fmt.Errorf("write manifest back-link: %w", err)
		}
		return res, nil
	}

	// A session that already back-links a note ANYWHERE is done — including on a
	// re-run of this very applier, whose first pass linked the newest stranded
	// manifest. Without this, a re-run would walk down the sibling list and link
	// one more manifest per invocation; the plan says older strandings stay
	// stranded by design (they are accepted debt), and re-running an applied
	// pair must change nothing.
	//
	// 🔴 THE MESSAGE NAMES THE NOTE, not only the manifest, and that matters
	// more since the Codex receipt began back-linking itself: a Codex session
	// whose only note is its auto-capture receipt now reaches this branch, and
	// an operator who is told merely "already back-links a note" cannot tell a
	// real wrap note from a stub without going and looking. Upgrading a stub
	// link is TryLinkArchiveToSessions's job, not this applier's — this branch
	// must therefore report precisely what it declined to change.
	if linkedRel != "" {
		suffix := ""
		if n := len(strandedEntries); n > 0 {
			suffix = fmt.Sprintf("; %d older stranded manifest(s) stay stranded by design", n)
		}
		res.NothingToDo = fmt.Sprintf("session %s already back-links note %s via %s%s",
			sessionID, linkedNote, linkedRel, suffix)
		return res, nil
	}
	if len(strandedEntries) == 0 {
		res.NothingToDo = fmt.Sprintf(
			"every manifest for session %s already back-links a note — nothing stranded", sessionID)
		return res, nil
	}

	// Newest stranded — ListEntries is sorted oldest-first.
	target := strandedEntries[len(strandedEntries)-1]
	res.TargetManifest = archive.VaultRelPath(v.Root, target.ManifestPath)

	link, err := v.BackfillArchiveLink(project, sessionID, res.TargetManifest)
	if err != nil {
		return nil, err
	}
	if !link.Found {
		// Not derivable: no note carries this id as a caller-pushed key. The
		// association was never recorded, and matching by anything softer is a
		// guess wearing the face of a measurement.
		return nil, fmt.Errorf(
			"no session note carries caller-pushed session_key %q — this session is not derivable; "+
				"the note<->transcript association was never recorded", sessionID)
	}
	for _, ref := range link.Updated {
		res.NotesUpdated = append(res.NotesUpdated, ref.NotePath)
	}
	res.Canonical = link.Canonical.NotePath

	// LOCKING POSTURE: BLOCKING. LinkSessionNote takes the MANIFEST's per-path
	// vaultlock across its read-modify-write. The sessions-directory lock taken
	// by BackfillArchiveLink above has ALREADY BEEN RELEASED by the time control
	// reaches here — that is the sequential-never-nested invariant this function
	// documents, and it is what makes a blocking acquire safe rather than a
	// lock-order hazard. Do not hoist this call under that lock.
	if err := archive.LinkSessionNote(v.Root, target.ManifestPath, link.Canonical.NotePath); err != nil {
		return nil, fmt.Errorf("write manifest back-link: %w", err)
	}
	return res, nil
}
