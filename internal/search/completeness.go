// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The completeness record and the stale flag (task
// search-index-completeness-and-build-serialization, Scopes 1-6; ADR-014
// decisions 2, 3, 7 and 8). indexstore owns the file and its lock; this file
// owns what it means.
//
// THE TIER TABLE (ADR-014 decision 7 cites it). What a build on the search
// path reads, and what each tier costs:
//
//	tier                                   search path                     explicit vp index rebuild
//	session notes, decision chunks          yes; misses are embedded        built
//	  (persisted in the store, note-owned),
//	  iterations
//	host-local chunks of ledgered sources   yes, embed-cache hits only;     built; misses embedded
//	  (archives, import batches)              a miss sets missing_vectors
//	tracked drawers (the glide path)        yes, only while the vault       built while there is
//	                                          carries no migration marker;    no marker
//	                                          misses embedded
//	archives not yet in the ledger, and     no: not loaded, never stale     ingested
//	  chunks with no live owner
//
// Every vector the search path embeds is committed in batches under the index
// commit lock, with a timeout (Rebuild, commitVectors).

// Tier names in the completeness record.
const (
	tierNotes          = "notes"
	tierDecisions      = "decisions"
	tierIterations     = "iterations"
	tierLocal          = "local"
	tierTrackedDrawers = "tracked_drawers"
)

// readStoreFn is the engine's one read of the host-local store, a seam so a
// test can count reads.
var readStoreFn = indexstore.ReadStore

// fingerprintCheck is how a project's two fingerprints compare with this
// binary's, without loading a model.
type fingerprintCheck struct {
	reasons []indexstore.StaleReason // the fingerprint reasons found
	chunks  indexstore.FingerprintStatus
	recipe  string // the chunk recipe's Sum
	regime  cacheRegime
	embedFP string // embedder.Fingerprint(cfg)
}

// embedFingerprint is this engine's embedding regime, a pure function of its
// config: no model is loaded.
func (e *Engine) embedFingerprint() string {
	return embedder.Fingerprint(e.config.EmbedderModel, e.config.EmbedderMaxSeqLen)
}

// checkFingerprints compares the project's chunks.fingerprint and embed-cache
// sidecar with this binary's recipe and regime, falling back to the values rec
// recorded when a sidecar is gone. A missing value on either side is not a
// mismatch (it means not built), with one exception: vectors with no sidecar.
func (e *Engine) checkFingerprints(project string, rec indexstore.Completeness) (fingerprintCheck, error) {
	var fc fingerprintCheck
	ix, err := palace.ProjectIndexing(e.vault, project)
	if err != nil {
		return fc, err
	}
	fc.recipe = ix.Recipe.Sum()
	fc.chunks, err = indexstore.ReadFingerprint(e.vault, project, ix.Recipe)
	if err != nil {
		return fc, err
	}
	if fc.chunks == indexstore.FingerprintMismatch ||
		(fc.chunks == indexstore.FingerprintMissing && rec.ChunksFingerprint != "" && rec.ChunksFingerprint != fc.recipe) {
		fc.reasons = append(fc.reasons, indexstore.StaleReason{Kind: indexstore.StaleFingerprint, Fingerprint: indexstore.FingerprintChunks})
	}

	fc.embedFP = e.embedFingerprint()
	dir, err := e.vault.EmbedCacheDir(project)
	if err != nil {
		return fc, err
	}
	fc.regime, err = classifyRegime(dir, fc.embedFP)
	if err != nil {
		return fc, err
	}
	if fc.regime == regimeMismatch ||
		(fc.regime == regimeUnbuilt && rec.EmbedFingerprint != "" && rec.EmbedFingerprint != fc.embedFP) {
		fc.reasons = append(fc.reasons, indexstore.StaleReason{Kind: indexstore.StaleFingerprint, Fingerprint: indexstore.FingerprintEmbed})
	}
	return fc, nil
}

// Stale reports whether project is stale, and why: the reasons the
// completeness record holds, together with a chunks.fingerprint or
// embed-cache fingerprint that is present and different now (ADR-014 decision
// 3). It loads no model and takes no lock, so bootstrap and coverage read
// stale after an embedder or recipe change before any search has run.
//
// A graph-fingerprint mismatch is never a reason: the next ingester or
// rebuild run rebuilds the graph from cached vectors with no embedding.
func (e *Engine) Stale(project string) (bool, []indexstore.StaleReason, error) {
	rec, err := indexstore.ReadCompleteness(e.vault, project)
	if err != nil {
		return false, nil, err
	}
	fc, err := e.checkFingerprints(project, rec)
	if err != nil {
		return false, nil, err
	}
	all := indexstore.Completeness{Stale: append(append([]indexstore.StaleReason(nil), rec.Stale...), fc.reasons...)}
	reasons := canonicalReasons(all)
	return len(reasons) > 0, reasons, nil
}

// canonicalReasons returns c's reasons sorted and without duplicates.
func canonicalReasons(c indexstore.Completeness) []indexstore.StaleReason {
	var out []indexstore.StaleReason
	for _, r := range c.Stale {
		if !containsReason(out, r) {
			out = append(out, r)
		}
	}
	sortReasons(out)
	return out
}

func containsReason(rs []indexstore.StaleReason, r indexstore.StaleReason) bool {
	return slices.Contains(rs, r)
}

func sortReasons(rs []indexstore.StaleReason) {
	slices.SortFunc(rs, func(a, b indexstore.StaleReason) int {
		if c := cmp.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		return cmp.Compare(a.Fingerprint, b.Fingerprint)
	})
}

// buildFacts is what one build found, for the completeness record.
type buildFacts struct {
	tiers       map[string]indexstore.TierRecord
	localHits   int
	localMisses int
}

// writeCompletenessLocked records what this build found, under the held
// commit lock tx. Stale reasons are only ever added here: the search path
// never clears one (a completed rebuild's ClearStale clears both reasons, the
// repair pass's ClearMissingVectors the missing-vector one). The fingerprints
// the local tier was built under are recorded the first time a build finds
// local vectors under a matching recipe and regime, and never overwritten
// here. An unchanged record is not rewritten (indexstore.Tx.WriteCompleteness).
func (e *Engine) writeCompletenessLocked(tx *indexstore.Tx, project string, b buildFacts) error {
	rec, err := tx.Completeness()
	if err != nil {
		return err
	}
	fc, err := e.checkFingerprints(project, rec)
	if err != nil {
		return err
	}
	rec.Stale = append(rec.Stale, fc.reasons...)
	embedMismatch := containsReason(fc.reasons, indexstore.StaleReason{Kind: indexstore.StaleFingerprint, Fingerprint: indexstore.FingerprintEmbed})
	if b.localMisses > 0 && !embedMismatch {
		rec.Stale = append(rec.Stale, indexstore.StaleReason{Kind: indexstore.StaleMissingVectors})
	}
	rec.Stale = canonicalReasons(rec)
	rec.Tiers = b.tiers
	rec.LocalMisses = b.localMisses
	if b.localHits > 0 {
		if rec.ChunksFingerprint == "" && fc.chunks == indexstore.FingerprintMatch {
			rec.ChunksFingerprint = fc.recipe
		}
		if rec.EmbedFingerprint == "" && fc.regime == regimeMatch {
			rec.EmbedFingerprint = fc.embedFP
		}
	}
	_, err = tx.WriteCompleteness(rec)
	return err
}

// RepairProof is what only a scan under the index commit lock that found a
// vector for every loaded local-tier chunk can hand over: the one thing that
// clears the missing-vector reason (ClearMissingVectors). Its fields are
// unexported, so the zero value is the only proof other code can make.
type RepairProof struct {
	tx *indexstore.Tx
}

// RepairScan checks, under the held commit lock tx, that every local-tier
// chunk search loads (a chunk of a ledgered source, not a decision chunk) has
// a vector in the embed cache, and returns the proof if so. The ingester's
// repair pass calls it after re-embedding the missing vectors.
func (e *Engine) RepairScan(tx *indexstore.Tx) (RepairProof, error) {
	if tx == nil || !tx.Held() {
		return RepairProof{}, errors.New("search: a repair scan needs a held index commit lock")
	}
	project := tx.Project()
	st, err := readStoreFn(e.vault, project)
	if err != nil {
		return RepairProof{}, err
	}
	e.cache.Forget(project)
	missing := 0
	for _, c := range st.Chunks(true) {
		if c.SourceType == storage.SourceTypeDecision {
			continue
		}
		vec, err := e.cache.Get(project, c.ID)
		if err != nil {
			return RepairProof{}, err
		}
		if vec == nil {
			missing++
		}
	}
	if missing > 0 {
		return RepairProof{}, fmt.Errorf("search: %d local-tier chunks of %s still have no vector", missing, project)
	}
	return RepairProof{tx: tx}, nil
}

// ClearMissingVectors clears the missing-vector reason, and only that one: a
// fingerprint reason stays until a completed rebuild. proof must come from a
// RepairScan under this same held Tx.
func ClearMissingVectors(tx *indexstore.Tx, proof RepairProof) error {
	if proof.tx == nil || proof.tx != tx || !tx.Held() {
		return errors.New("search: not the repair proof of this commit")
	}
	rec, err := tx.Completeness()
	if err != nil {
		return err
	}
	var keep []indexstore.StaleReason
	for _, r := range rec.Stale {
		if r.Kind != indexstore.StaleMissingVectors {
			keep = append(keep, r)
		}
	}
	rec.Stale = keep
	rec.LocalMisses = 0
	_, err = tx.WriteCompleteness(rec)
	return err
}

// TrulyEmpty reports whether project has nothing any tier could ever index:
// no session notes, no iterations, no tracked transcript archives (ledgered
// or not), no host-local chunks, and, while the vault carries no migration
// marker, no tracked drawers (ADR-014 decision 8). It is the one definition;
// the search-first answer and the coverage instrument call it. It loads no
// embedder.
func (e *Engine) TrulyEmpty(project string) (bool, error) {
	sessions, err := e.vault.SessionDir(project)
	if err != nil {
		return false, err
	}
	if found, err := dirHasSuffix(sessions, ".md"); err != nil || found {
		return false, err
	}
	iterIDs, _, _, err := collectIterationCorpus(e.vault, project)
	if err != nil || len(iterIDs) > 0 {
		return false, err
	}
	pdir, err := e.vault.ProjectDir(project)
	if err != nil {
		return false, err
	}
	if found, err := dirHasSuffix(filepath.Join(pdir, "transcripts"), ".jsonl.zst"); err != nil || found {
		return false, err
	}
	st, err := readStoreFn(e.vault, project)
	if err != nil {
		return false, err
	}
	if len(st.Chunks(false)) > 0 {
		return false, nil
	}
	migrated, err := storage.VaultMigrated(e.vault.Root)
	if err != nil {
		return false, err
	}
	if !migrated {
		n, err := e.countTrackedDrawers(project)
		if err != nil || n > 0 {
			return false, err
		}
	}
	return true, nil
}

// dirHasSuffix reports whether dir holds a regular file whose name ends in
// suffix. A missing directory holds none.
func dirHasSuffix(dir, suffix string) (bool, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, e := range ents {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), suffix) {
			return true, nil
		}
	}
	return false, nil
}

// HasTrackedDrawers reports whether the project has any tracked drawer. It is
// the exported form of the glide-path presence test countTrackedDrawers that
// TrulyEmpty and the tier table already use, so the coverage instrument reads
// the same answer without re-deriving it (index-coverage-instrument, Design
// "Data sources"). It loads no embedder.
func (e *Engine) HasTrackedDrawers(project string) (bool, error) {
	n, err := e.countTrackedDrawers(project)
	return n > 0, err
}

func (e *Engine) countTrackedDrawers(project string) (int, error) {
	n := 0
	wings, err := e.vault.ListWings(project)
	if err != nil {
		return 0, err
	}
	for _, wing := range wings {
		rooms, err := e.vault.ListRooms(project, wing)
		if err != nil {
			return 0, err
		}
		for _, room := range rooms {
			ds, err := e.vault.ListDrawers(project, wing, room)
			if err != nil {
				return 0, err
			}
			n += len(ds)
		}
	}
	return n, nil
}
