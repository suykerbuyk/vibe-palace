// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// DimArchiveRoundTrip is the archive round-trip dimension: can you get from a
// transcript to the note that describes it, and back?
//
// It is the audit's first dimension because the live vault already tells us the
// answer — 130 manifests, 48 back-linking a note — so the auditor can be checked
// against a KNOWN TRUTH rather than against its own reasoning. An auditor validated
// only by its own logic is the thing this epic exists to prevent.
const DimArchiveRoundTrip = "archive-roundtrip"

// EvidenceArchiveRoundTrip is the command a human can run to reproduce this
// dimension's numbers by hand.
//
// Invariant 3: RECORD THE GREP, NEVER THE COUNT. Every hand-recorded census in this
// project has rotted — 204's own figures were stale one iteration later. A report
// that states a number without the command that produced it is a report that will
// be wrong and unfalsifiable.
const EvidenceArchiveRoundTrip = `jq -r 'select(.vault_rel_session_note == null or .vault_rel_session_note == "") | input_filename' ` +
	`Projects/*/transcripts/*.manifest.json`

// auditArchiveRoundTrip checks, for every transcript manifest in the vault:
//
//   - it back-links to a session note (vault_rel_session_note is set), and
//   - that note actually EXISTS on disk.
//
// A manifest is the right artifact to key on, not a note. A session note without a
// transcript is often legitimate (a wrap note for a session whose transcript was
// never archived), but a transcript with no reachable note is ALWAYS a loss: the
// transcript is stranded, and nothing can navigate from the history to the record of
// it. Keying on the note instead would flag correct notes and miss stranded
// transcripts — the wrong direction on both counts.
//
// The vault is enumerated with ListAllProjects (the UNION of palace/ and Projects/),
// never ListProjects: the latter reads only palace/ and is blind to 5 projects and 73
// session notes in the live vault. An auditor that cannot see 73 notes is not an
// auditor.
//
// A project whose transcripts directory cannot be READ is reported as unknown, never
// as a pass. "I could not look" is not "there was nothing there."
func auditArchiveRoundTrip(vault *storage.Vault) ([]Finding, []string, error) {
	projects, err := vault.ListAllProjects()
	if err != nil {
		return nil, nil, fmt.Errorf("enumerate projects: %w", err)
	}

	var findings []Finding
	var unknowns []string

	for _, p := range projects {
		// A project with no Projects/ tree has no transcripts by construction --
		// transcripts live under Projects/<slug>/transcripts/. That is an absence,
		// not a blindness, so it is not an unknown.
		if !p.InProjects {
			continue
		}

		// archive.ListEntries now owns blindness: it is built on os.ReadDir and
		// returns an error for a transcripts directory it cannot read (EACCES, a
		// vanished mount), instead of the old filepath.Glob that swallowed the
		// permission error and reported an unreadable tree as an empty one. A
		// genuinely absent directory is an empty listing, not an error, so the
		// no-transcripts project stays clean.
		entries, err := archive.ListEntries(vault.Root, p.Slug)
		if err != nil {
			unknowns = append(unknowns, fmt.Sprintf("%s: cannot list transcripts: %v", p.Slug, err))
			continue
		}

		// Recoverable strandings get their finding MESSAGE annotated with the
		// exact repair command. The Artifact is untouched: finding identity is
		// (Dimension, Artifact), so annotating Detail cannot churn the accepted
		// baseline. A candidate scan that fails degrades to un-annotated
		// findings plus an unknown — the defects still surface.
		byTarget := make(map[string]BackfillCandidate)
		recoverableSession := make(map[string]string) // session id -> target manifest rel
		if cands, cerr := backfillCandidatesFromEntries(vault, p.Slug, entries); cerr != nil {
			unknowns = append(unknowns, fmt.Sprintf("%s: backfill candidate scan: %v", p.Slug, cerr))
		} else {
			for _, c := range cands {
				byTarget[c.TargetManifest] = c
				recoverableSession[c.SessionID] = c.TargetManifest
			}
		}

		for _, e := range entries {
			artifact := archive.VaultRelPath(vault.Root, e.ManifestPath)

			note := e.Manifest.VaultRelSessionNote
			if note == "" {
				detail := fmt.Sprintf("transcript for session %s back-links to NO session note "+
					"— the transcript is stranded: nothing can navigate from the note to it",
					e.Manifest.SessionID)
				if c, ok := byTarget[artifact]; ok {
					detail += fmt.Sprintf(" — RECOVERABLE: note %s carries a caller-pushed key; "+
						"apply with `vp archive link %s -p %s`", c.NoteRels[0], c.SessionID, p.Slug)
				} else if target, ok := recoverableSession[e.Manifest.SessionID]; ok {
					detail += fmt.Sprintf(" — session %s is recoverable via its newest stranded "+
						"manifest (%s); this older manifest stays stranded by design",
						e.Manifest.SessionID, target)
				}
				findings = append(findings, Finding{
					Dimension: DimArchiveRoundTrip,
					Artifact:  artifact,
					Detail:    detail,
				})
				continue
			}

			// The link exists. Does it point at anything?
			if _, err := os.Stat(filepath.Join(vault.Root, filepath.FromSlash(note))); err != nil {
				findings = append(findings, Finding{
					Dimension: DimArchiveRoundTrip,
					Artifact:  artifact,
					Detail: fmt.Sprintf("back-links to %q, which does not exist — a DANGLING link "+
						"is worse than a missing one: it reports success and delivers nothing", note),
				})
			}
		}
	}

	return findings, unknowns, nil
}
