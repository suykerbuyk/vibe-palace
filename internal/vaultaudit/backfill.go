// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// This file is the ONE home of the backfill candidate predicate for
// capture-note-archive-link-never-closes Phase 4. A pair (note N, manifest M)
// is a candidate iff, within one project:
//
//	N.session_key_source == "caller"  ∧  N.session_key == M.session_id
//	∧  N.archive_session_id == ""     ∧  M.vault_rel_session_note == ""
//
// The audit annotation, `vp archive backfill`, and the applier ALL derive from
// this file — the note-side half lives in storage
// (ListCallerKeyedUnstampedNotes), the manifest-side half in archive
// (ListEntries), and the pairing here, because vaultaudit is the one package
// that already imports both and all three consumers already import vaultaudit.
// Two implementations of one rule silently diverge (the templates.Classify
// lesson); do not copy this predicate anywhere.

// BackfillCandidate is one RECOVERABLE session: a stranded transcript manifest
// whose harness session id was recorded — exactly — as a caller-pushed
// session_key on a note that never got stamped. Recovering it is a derivation,
// not a guess.
type BackfillCandidate struct {
	Project   string   `json:"project"`
	SessionID string   `json:"session_id"`
	NoteRels  []string `json:"notes"`

	// StrandedManifests lists every stranded manifest of this session,
	// vault-relative, oldest first (archive.ListEntries order).
	// TargetManifest is the NEWEST stranded one — the deterministic rule the
	// applier follows. Older stranded manifests of the same session stay
	// stranded by design and fall into the accepted-debt set: the note can only
	// point one place, and it points at the fullest transcript.
	StrandedManifests []string `json:"stranded_manifests"`
	TargetManifest    string   `json:"target_manifest"`
}

// BackfillCandidates enumerates candidates VAULT-GLOBAL — the stranded set
// spans projects. A transcripts directory that exists but cannot be read is an
// ERROR, never "no candidates": filepath.Glob (under archive.ListEntries)
// swallows permission errors, so readability is probed first, exactly as
// auditArchiveRoundTrip does.
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
		dir := filepath.Join(v.Root, "Projects", p.Slug, "transcripts")
		if _, err := os.ReadDir(dir); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("%s: cannot read transcripts: %w", p.Slug, err)
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

	// Note side: the storage half of the predicate.
	notes, err := v.ListCallerKeyedUnstampedNotes(project)
	if err != nil {
		return nil, fmt.Errorf("%s: scan session notes: %w", project, err)
	}
	notesByKey := make(map[string][]string)
	for _, n := range notes {
		notesByKey[n.SessionKey] = append(notesByKey[n.SessionKey], n.NoteRel)
	}

	var out []BackfillCandidate
	for sessionID, manifests := range stranded {
		noteRels, ok := notesByKey[sessionID]
		if !ok {
			continue
		}
		out = append(out, BackfillCandidate{
			Project:           project,
			SessionID:         sessionID,
			NoteRels:          noteRels,
			StrandedManifests: manifests,
			TargetManifest:    manifests[len(manifests)-1],
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
// (archive.LinkSessionNote, which takes no vaultlock). Sequential, never
// nested.
func ApplyBackfill(v *storage.Vault, project, sessionID string) (*ApplyBackfillResult, error) {
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}

	res := &ApplyBackfillResult{Project: project, SessionID: sessionID}

	dir := filepath.Join(v.Root, "Projects", project, "transcripts")
	if _, err := os.ReadDir(dir); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no transcripts directory for project %q", project)
		}
		return nil, fmt.Errorf("cannot read transcripts for %q: %w", project, err)
	}
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
	for _, e := range entries {
		if e.Manifest.SessionID != sessionID {
			continue
		}
		sawSession = true
		if e.Manifest.VaultRelSessionNote == "" {
			strandedEntries = append(strandedEntries, e)
		} else if linkedRel == "" {
			linkedRel = archive.VaultRelPath(v.Root, e.ManifestPath)
		}
	}
	if !sawSession {
		return nil, fmt.Errorf("no manifest for session id %q in project %q", sessionID, project)
	}
	// A session that already back-links a note ANYWHERE is done — including on a
	// re-run of this very applier, whose first pass linked the newest stranded
	// manifest. Without this, a re-run would walk down the sibling list and link
	// one more manifest per invocation; the plan says older strandings stay
	// stranded by design (they are accepted debt), and re-running an applied
	// pair must change nothing.
	if linkedRel != "" {
		suffix := ""
		if n := len(strandedEntries); n > 0 {
			suffix = fmt.Sprintf("; %d older stranded manifest(s) stay stranded by design", n)
		}
		res.NothingToDo = fmt.Sprintf("session %s already back-links a note via %s%s",
			sessionID, linkedRel, suffix)
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

	if err := archive.LinkSessionNote(v.Root, target.ManifestPath, link.Canonical.NotePath); err != nil {
		return nil, fmt.Errorf("write manifest back-link: %w", err)
	}
	return res, nil
}
