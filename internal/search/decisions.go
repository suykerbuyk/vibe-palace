// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Decision chunks in the host-local store
// (decision-chunks-in-the-host-local-store). A session note's decisions are
// written to the project's chunk store as chunks with source_type "decision",
// owned by the note, under the index commit lock. This is the ONE builder; it
// lives here, not in internal/capture, because the notes-tier rebuild
// (Engine.Rebuild) regenerates the same chunks and internal/search cannot import
// internal/capture (capture imports search). It is not in internal/index either
// — the leaf index package cannot import internal/palace, which owns HallDecisions
// and DetectWing, because palace imports index.
//
// The capture-time writers (capture.fileDecisionDrawers and the enrichment drain)
// take the commit lock themselves and call WriteNoteDecisions; the rebuild's
// notes tier calls it once per note under a lock it already holds. There is no
// second implementation, so a backfilled, a live and a rebuilt decision agree by
// construction.

// DecisionRoom is the palace room every decision chunk is filed into. It is a
// PATH SEGMENT and it is FIXED rather than classified from content: a decision
// is filed here because the writer KNOWS it is a decision.
//
// It is one exported symbol precisely because it would otherwise be a bare
// "decisions" literal at the writer, the resolver that defaults an unqualified
// query to this room, and the query that reads it back. A drift in any one of
// them yields an empty default query, indistinguishable at the wire from an
// honestly-empty palace. A constant makes them agree by construction.
const DecisionRoom = "decisions"

// decisionSourceRef builds the search identity of ONE decision:
// "session/{id}#decision/{n}/{drawerID}".
//
// It is per DECISION, not per session: the engine's dedup keeps only the first
// result it sees for an exact SourceRef and drops the rest, so a session-level
// ref would make a note's second and later decisions unreachable through search.
//
// The last segment is storage.DrawerID(wing, content), a content discriminator,
// so a revised decision at the same position carries a different ref and both the
// revision and the superseded original stay visible. The "#" keeps the decision
// ref lexically disjoint from the bare session id capture's transcript chunks
// file under, so a decision can never collide in dedup with a tape chunk of its
// own session.
func decisionSourceRef(wing, sessionID, content string, n int) string {
	return fmt.Sprintf("session/%s#decision/%d/%s", sessionID, n, storage.DrawerID(wing, content))
}

// decisionDrawer builds the drawer-shaped value for one decision, the single
// definition of what a decision record IS. The store id is recomputed from the
// content alone by WriteNoteDecisions (index.ChunkID); the 32-bit DrawerID here
// survives only in the source_ref discriminator and on the legacy tracked
// drawers the glide path still reads.
//
// Content is stored VERBATIM — a decision is the operator's own sentence.
func decisionDrawer(wing, sessionID, content, filedAt string, n int) storage.Drawer {
	return storage.Drawer{
		Hall:       palace.HallDecisions,
		SourceType: storage.SourceTypeDecision,
		Content:    content,
		SourceRef:  decisionSourceRef(wing, sessionID, content, n),
		AddedBy:    "capture",
		FiledAt:    filedAt,
	}
}

// DecisionFiledAt renders a session note's calendar day as the RFC3339 stamp a
// decision drawer's FiledAt carries: midnight UTC on that day. It answers "which
// day's work is this decision from", not "when did the row happen to be written",
// so it is NEVER time.Now(): the enrichment drain can file a note's decisions
// days after the note was written, and a date-bounded palace query would then
// miss them.
//
// A malformed day is an ERROR rather than a silent fallback to now, which would
// reintroduce exactly the wall-clock stamp this exists to prevent on the one
// input nobody is watching.
//
// Note: on the store path its RFC3339 RESULT is now vestigial — the drawer's
// FiledAt it fills is discarded in noteDecisionChunks, where the store owner-line
// day comes from decisionDay (the ledger's start day, YYYY-MM-DD). The call is
// kept for its side effect: it is the guard that rejects a malformed note date
// before a chunk is built.
func DecisionFiledAt(noteDate string) (string, error) {
	day, err := time.Parse("2006-01-02", noteDate)
	if err != nil {
		return "", fmt.Errorf("decision filed_at: note date %q is not YYYY-MM-DD: %w", noteDate, err)
	}
	return day.UTC().Format(time.RFC3339), nil
}

// decisionDrawersForNote builds — and does NOT file — the decision drawers of
// ONE session note, returning the wing they belong in alongside them. n is the
// index of the decision in the slice passed in; an entry blank after TrimSpace
// is skipped and its index left unused, so a ref is a stable pointer back to the
// note's Nth decision. A malformed noteDate is returned as an error.
func decisionDrawersForNote(project, sessionID, noteDate string, decisions []string) (string, []storage.Drawer, error) {
	filedAt, err := DecisionFiledAt(noteDate)
	if err != nil {
		return "", nil, err
	}
	// DetectWing(project, "") is the project's own wing, the same wing the
	// transcript indexer files into, so a session's decisions and its tape sit
	// under one roof. The source_ref discriminator is keyed on it.
	wing := palace.DetectWing(project, "")

	ds := make([]storage.Drawer, 0, len(decisions))
	for n, d := range decisions {
		if strings.TrimSpace(d) == "" {
			continue
		}
		ds = append(ds, decisionDrawer(wing, sessionID, d, filedAt, n))
	}
	return wing, ds, nil
}

// decisionDay is a decision chunk's filed_at (its store owner-line day,
// YYYY-MM-DD) for the note that owns it.
//
//   - The start day the ledger records for the note's archiveSessionID, when that
//     session is ledgered LIVE on this host. The day was computed once, by the
//     ingester, from immutable archive content, so every host that ledgered the
//     session reads the same day, and this writer never opens an archive.
//   - Otherwise the note's own calendar day: a note that names no session, or a
//     session that is not ledgered live here (an archive present but not
//     ledgered, a failing archive, or one not yet reached). The chunk is re-dated
//     at the next notes-tier build once its session is ledgered.
func decisionDay(led *indexstore.Ledger, archiveSessionID, noteDate string) (string, error) {
	day := noteDate
	if len(day) >= 10 {
		day = day[:10]
	}
	if archiveSessionID != "" && led != nil {
		if d, ok := led.StartDay(archiveSessionID); ok {
			day = d
		}
	}
	if !dayPattern.MatchString(day) {
		return "", fmt.Errorf("decision filed_at: day %q is not YYYY-MM-DD", day)
	}
	return day, nil
}

// noteDecisionChunks builds the OwnedChunks of a note's decisions, ready for
// Tx.ReplaceOwned: the content-only store id, the note's wing, DecisionRoom, the
// decisions hall, the per-decision source_ref, and the day decisionDay derives.
// An empty or all-blank decisions slice yields no chunks, which removes the
// note's decision chunks when passed to ReplaceOwned.
func noteDecisionChunks(tx *indexstore.Tx, project, noteStem, archiveSessionID, noteDate string, decisions []string) ([]indexstore.OwnedChunk, error) {
	wing, drawers, err := decisionDrawersForNote(project, noteStem, noteDate, decisions)
	if err != nil {
		return nil, err
	}
	led, err := tx.Ledger()
	if err != nil {
		return nil, err
	}
	day, err := decisionDay(led, archiveSessionID, noteDate)
	if err != nil {
		return nil, err
	}
	out := make([]indexstore.OwnedChunk, 0, len(drawers))
	for _, d := range drawers {
		out = append(out, indexstore.OwnedChunk{
			Chunk: indexstore.Chunk{
				ID:      index.ChunkID(d.Content),
				Content: d.Content,
				Wing:    wing,
				Room:    DecisionRoom,
				Hall:    d.Hall,
			},
			Ownership: indexstore.Ownership{
				SourceRef:  d.SourceRef,
				SourceType: d.SourceType,
				ChunkIndex: d.ChunkIndex,
				AddedBy:    d.AddedBy,
				Day:        day,
			},
		})
	}
	return out, nil
}

// WriteNoteDecisions makes recs exactly the note's current decision chunks in the
// store, under the held commit lock tx: it adds the ones that are missing,
// removes the note from any decision chunk it no longer carries, deletes a chunk
// left with no owner, and re-dates when the note's session was ledgered since the
// last build. An empty decisions slice removes the note's decision chunks (a
// deleted or decision-less note). recipe writes chunks.fingerprint on a store
// that has none yet, and must come from palace.ProjectIndexing.
//
// The owner key is the note's project-relative path, noteSourceRef(noteStem) =
// "sessions/<stem>.md" (the same identity the note corpus uses), so a project
// rename changes no owner; archiveSessionID is the note's host session id, used
// only to date the chunks from the ledger.
func WriteNoteDecisions(tx *indexstore.Tx, recipe index.ChunkRecipe, project, noteStem, archiveSessionID, noteDate string, decisions []string) error {
	tx.UseRecipe(recipe)
	recs, err := noteDecisionChunks(tx, project, noteStem, archiveSessionID, noteDate, decisions)
	if err != nil {
		return err
	}
	return tx.ReplaceOwned(indexstore.NoteOwner(noteDecisionOwnerKey(noteStem)), recs)
}

// noteDecisionOwnerKey is the store owner key for a note's decision chunks: its
// project-relative path, so the capture writers and the rebuild agree on it and
// a project rename leaves it unchanged.
func noteDecisionOwnerKey(noteStem string) string { return noteSourceRef(noteStem) }

// dayPattern mirrors indexstore's: a decision chunk's day must be YYYY-MM-DD, so
// a note owner line validates the same way the store does. The store's
// Tx.ReplaceOwned enforces it again; this only gives a decision-specific error.
var dayPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// noteDecisionSet is one session note's decision frontmatter, the input a build
// needs to regenerate its decision chunks.
type noteDecisionSet struct {
	stem             string
	date             string
	archiveSessionID string
	decisions        []string
}

// collectNoteDecisionSets reads the decision frontmatter of every session note
// under Projects/<project>/sessions/*.md. A missing directory is empty-and-nil;
// an unparseable note contributes nothing without failing the build (matching
// collectNoteCorpus). It reads only the frontmatter the decision writer needs —
// never the body — so it files no body as a decision.
func collectNoteDecisionSets(vault *storage.Vault, project string) ([]noteDecisionSet, error) {
	dir, err := vault.SessionDir(project)
	if err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("glob session notes: %w", err)
	}
	var out []noteDecisionSet
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue // raced with a delete between Glob and read
			}
			return nil, fmt.Errorf("read session note %s: %w", filepath.Base(path), err)
		}
		meta, _, err := storage.ParseFrontmatter(data)
		if err != nil {
			continue // an unparseable note is not a build failure
		}
		out = append(out, noteDecisionSet{
			stem:             strings.TrimSuffix(filepath.Base(path), ".md"),
			date:             meta.Date,
			archiveSessionID: meta.ArchiveSessionID,
			decisions:        meta.Decisions,
		})
	}
	return out, nil
}

// regenerateDecisionChunks is the notes tier's decision-chunk writer, run before
// the build reads the store (ADR-014; this child's Scope 2). It makes the store
// hold exactly the decision chunks of the session notes on disk: every present
// note's current decision set (re-dated from the ledger), and nothing owned by a
// note that no longer exists. It is the SAME writer the capture path uses
// (WriteNoteDecisions), so a rebuilt decision matches a captured one by
// construction, and a fresh host or a host after a chunks.fingerprint discard has
// them with no capture.
//
// The whole regeneration is one commit step under the leaf commit lock pl holds
// for the build. A busy lock skips it (chain.skipped): the decisions from the
// last build stay and the next build retries, exactly as a skipped vector batch
// does. It embeds nothing; vectors for decision chunks come later from the store
// read.
func (e *Engine) regenerateDecisionChunks(ctx context.Context, pl *projectLock, timeout time.Duration, chain *genChain, project string) error {
	sets, err := collectNoteDecisionSets(e.vault, project)
	if err != nil {
		return err
	}
	present := make(map[string]bool, len(sets))
	for _, s := range sets {
		present[noteDecisionOwnerKey(s.stem)] = true
	}

	// Note owners of DECISION chunks whose note is gone: their decision chunks
	// are removed. Read from disk, so a note deleted since the last build is
	// seen. Scoped to decision chunks so this never touches a note that owns
	// some other kind of chunk.
	st, err := readStoreFn(e.vault, project)
	if err != nil {
		return err
	}
	gone := map[string]bool{}
	for _, c := range st.Chunks(false) {
		if c.SourceType != storage.SourceTypeDecision {
			continue
		}
		for _, o := range c.Owners {
			if o.Kind == indexstore.OwnerNote && !present[o.ID] {
				gone[o.ID] = true
			}
		}
	}
	if len(sets) == 0 && len(gone) == 0 {
		return nil
	}

	ix, err := palace.ProjectIndexing(e.vault, project)
	if err != nil {
		return err
	}

	tx, err := pl.Tx(ctx, timeout)
	if err != nil {
		if isLockTimeout(err) {
			chain.skipped = true
			return nil
		}
		if errors.Is(err, indexstore.ErrProjectGone) {
			return nil
		}
		return err
	}

	writeErr := func() error {
		tx.UseRecipe(ix.Recipe)
		for _, s := range sets {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := WriteNoteDecisions(tx, ix.Recipe, project, s.stem, s.archiveSessionID, s.date, s.decisions); err != nil {
				return err
			}
		}
		for ownerID := range gone {
			if err := tx.ReplaceOwned(indexstore.NoteOwner(ownerID), nil); err != nil {
				return err
			}
		}
		return nil
	}()

	before, after, ferr := pl.finishTx()
	if writeErr != nil {
		return writeErr
	}
	if ferr != nil {
		chain.broken = true
		return ferr
	}
	chain.step(before, after)
	return nil
}
