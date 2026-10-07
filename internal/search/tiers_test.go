// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// Tests for the tier table, the completeness record and the stale flag (task
// search-index-completeness-and-build-serialization, Scopes 1-6 and 10). The
// host-local store has no production writer yet, so every row seeds it
// through indexstore's Tx, as the ingester and the importer will.

var (
	missingVectors = indexstore.StaleReason{Kind: indexstore.StaleMissingVectors}
	staleChunks    = indexstore.StaleReason{Kind: indexstore.StaleFingerprint, Fingerprint: indexstore.FingerprintChunks}
	staleEmbed     = indexstore.StaleReason{Kind: indexstore.StaleFingerprint, Fingerprint: indexstore.FingerprintEmbed}
)

// projectRecipe is the recipe palace.ProjectIndexing assembles for project, the
// one a writer records in chunks.fingerprint.
func projectRecipe(t *testing.T, v *storage.Vault, project string) index.ChunkRecipe {
	t.Helper()
	ix, err := palace.ProjectIndexing(v, project)
	if err != nil {
		t.Fatal(err)
	}
	return ix.Recipe
}

func chunkOf(content, sourceType string) indexstore.OwnedChunk {
	return indexstore.OwnedChunk{
		Chunk:     indexstore.Chunk{ID: index.ChunkID(content), Content: content, Wing: "store-wing", Room: "store-room", Hall: "facts"},
		Ownership: indexstore.Ownership{SourceType: sourceType, SourceRef: "ref-" + index.ChunkID(content)[:8]},
	}
}

func mockVec(t *testing.T, text string) []float32 {
	t.Helper()
	v, err := embedder.NewMock(384).Embed(context.Background(), text)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// lockStore takes project's commit lock with its recipe and ledger set, as a
// writer of chunks does.
func lockStore(t *testing.T, v *storage.Vault, project string) *indexstore.Tx {
	t.Helper()
	ensureProjectDir(t, v, project)
	tx, err := indexstore.Lock(context.Background(), v, project, indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	tx.UseRecipe(projectRecipe(t, v, project))
	if _, err := tx.EnsureLedger(nil); err != nil {
		t.Fatal(err)
	}
	return tx
}

// commitArchive commits one ledgered archive of contents, with their vectors
// written through c's Writer when withVecs is set.
func commitArchive(t *testing.T, c *EmbedCache, v *storage.Vault, project, session, sha string, contents []string, withVecs bool) {
	t.Helper()
	tx := lockStore(t, v, project)
	defer tx.Release()
	ac := indexstore.ArchiveCommit{SessionID: session, SHA: sha, ArchivePath: "Projects/" + project + "/transcripts/" + session + ".jsonl.zst",
		StartDay: "2026-05-13", StartDaySource: indexstore.DayFromTranscript, Vectors: map[string][]float32{}}
	for _, s := range contents {
		ch := chunkOf(s, "session")
		ac.Chunks = append(ac.Chunks, ch)
		if withVecs {
			ac.Vectors[ch.ID] = mockVec(t, s)
		}
	}
	var vw indexstore.VectorWriter
	if withVecs {
		vw = mustWriter(t, c, tx)
	}
	if err := tx.CommitArchive(ac, vw); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// appendDecision stores one decision chunk owned by a session note.
func appendDecision(t *testing.T, v *storage.Vault, project, content string) {
	t.Helper()
	tx := lockStore(t, v, project)
	defer tx.Release()
	ch := chunkOf(content, storage.SourceTypeDecision)
	ch.Day = "2026-05-14"
	if err := tx.Append(indexstore.NoteOwner("sessions/2026-05-14-aaaa0000-01.md"), []indexstore.OwnedChunk{ch}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func staleOf(t *testing.T, eng *Engine, project string) []indexstore.StaleReason {
	t.Helper()
	_, reasons, err := eng.Stale(project)
	if err != nil {
		t.Fatal(err)
	}
	return reasons
}

func search(t *testing.T, eng *Engine, project, q string) []SearchResult {
	t.Helper()
	res, err := eng.Search(context.Background(), q, SearchFilters{Project: project, Limit: 20, IncludeRaw: true})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func hasContent(res []SearchResult, content string) bool {
	return slices.ContainsFunc(res, func(r SearchResult) bool { return r.Content == content })
}

func record(t *testing.T, v *storage.Vault, project string) indexstore.Completeness {
	t.Helper()
	c, err := indexstore.ReadCompleteness(v, project)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestOnlyLedgeredChunksLoad (R2): chunks of archive A (ledgered), archive B
// (chunks written, no ledger entry yet) and import batch M (chunks written, no
// batch record yet), none with a vector. A lazy search counts A's chunks as
// missing and never B's or M's. Once B and M are ledgered, the next search
// counts their misses too.
func TestOnlyLedgeredChunksLoad(t *testing.T) {
	eng, v := testEngine(t)
	commitArchive(t, eng.cache, v, "proj", "sess-a", "sha-a", []string{"archive a one", "archive a two"}, false)
	tx := lockStore(t, v, "proj")
	b := []indexstore.OwnedChunk{chunkOf("archive b one", "session")}
	m := []indexstore.OwnedChunk{chunkOf("batch m one", "mempalace")}
	if err := tx.Append(indexstore.ArchiveOwner("sha-b"), b); err != nil {
		t.Fatal(err)
	}
	if err := tx.Append(indexstore.BatchOwner("batch-m"), m); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	search(t, eng, "proj", "anything")
	if got := record(t, v, "proj").LocalMisses; got != 2 {
		t.Fatalf("misses with only A ledgered = %d, want 2 (A's chunks; never B's or M's)", got)
	}
	if r := staleOf(t, eng, "proj"); !slices.Equal(r, []indexstore.StaleReason{missingVectors}) {
		t.Fatalf("stale = %v, want only missing_vectors", r)
	}

	commitArchive(t, eng.cache, v, "proj", "sess-b", "sha-b", []string{"archive b one"}, false)
	tx = lockStore(t, v, "proj")
	if err := tx.CommitBatch(indexstore.BatchCommit{BatchID: "batch-m", StartDay: "2026-05-13", Chunks: m}, nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	search(t, eng, "proj", "anything")
	if got := record(t, v, "proj").LocalMisses; got != 4 {
		t.Fatalf("misses once B and M are ledgered = %d, want 4", got)
	}
}

// TestLocalTierMissSetsMissingVectors (R5): a ledgered store chunk with no
// vector is not embedded on the search path, is not returned, and sets
// exactly the missing-vector reason, which a second search leaves set.
func TestLocalTierMissSetsMissingVectors(t *testing.T) {
	eng, v, emb := countingEngine(t, storage.Config{})
	const text = "a ledgered transcript chunk with no vector"
	commitArchive(t, eng.cache, v, "proj", "sess-a", "sha-a", []string{text}, false)
	for range 2 {
		if hasContent(search(t, eng, "proj", text), text) {
			t.Fatal("a local-tier miss was returned")
		}
		if r := staleOf(t, eng, "proj"); !slices.Equal(r, []indexstore.StaleReason{missingVectors}) {
			t.Fatalf("stale = %v, want exactly [missing_vectors]", r)
		}
	}
	if emb.sawText(text) {
		t.Fatal("the search path embedded a local-tier miss")
	}
}

// TestRepairClearsOnlyItsOwnReason (R5): ClearMissingVectors takes a proof
// from a scan under the commit lock. With a vector still missing the scan
// refuses; once it is written the reason clears; and a fingerprint reason set
// beside it survives.
func TestRepairClearsOnlyItsOwnReason(t *testing.T) {
	eng, v := testEngine(t)
	const text = "a chunk the repair pass embeds"
	commitArchive(t, eng.cache, v, "proj", "sess-a", "sha-a", []string{text}, false)
	search(t, eng, "proj", text)

	tx := lockStore(t, v, "proj")
	if _, err := eng.RepairScan(tx); err == nil {
		t.Fatal("a repair scan with a vector still missing produced a proof")
	}
	if err := ClearMissingVectors(tx, RepairProof{}); err == nil {
		t.Fatal("the zero RepairProof was accepted")
	}
	rec, err := tx.Completeness()
	if err != nil {
		t.Fatal(err)
	}
	rec.Stale = append(rec.Stale, staleChunks)
	if _, err := tx.WriteCompleteness(rec); err != nil {
		t.Fatal(err)
	}
	if err := tx.PutVectors(mustWriter(t, eng.cache, tx), map[string][]float32{index.ChunkID(text): mockVec(t, text)}); err != nil {
		t.Fatal(err)
	}
	proof, err := eng.RepairScan(tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ClearMissingVectors(tx, proof); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := record(t, v, "proj").Stale; !slices.Equal(got, []indexstore.StaleReason{staleChunks}) {
		t.Fatalf("stale after the repair = %v, want only the fingerprint reason left", got)
	}
}

// TestDecisionChunksAreNotesTier (X3): a decision chunk's miss is embedded
// and it is searchable; a ledgered transcript chunk's miss is not; and only
// the transcript chunk sets missing_vectors. Without it, nothing is stale.
func TestDecisionChunksAreNotesTier(t *testing.T) {
	const decision = "we decided to keep the commit lock a leaf"
	const transcript = "a ledgered transcript chunk"
	for _, withTranscript := range []bool{true, false} {
		t.Run(fmt.Sprintf("transcript=%v", withTranscript), func(t *testing.T) {
			eng, v, emb := countingEngine(t, storage.Config{})
			// A decision reaches the store through its session note: the rebuild's
			// notes tier regenerates the chunk from the note's frontmatter.
			writeDecisionNote(t, v.Root, "proj", "2026-05-14-aaaa0000-01", "2026-05-14", decision)
			if withTranscript {
				commitArchive(t, eng.cache, v, "proj", "sess-a", "sha-a", []string{transcript}, false)
			}
			if !hasContent(search(t, eng, "proj", decision), decision) {
				t.Fatal("the decision chunk is not searchable")
			}
			if !emb.sawText(decision) {
				t.Fatal("the decision chunk's miss was not embedded")
			}
			if emb.sawText(transcript) {
				t.Fatal("the transcript chunk's miss was embedded on the search path")
			}
			want := []indexstore.StaleReason(nil)
			if withTranscript {
				want = []indexstore.StaleReason{missingVectors}
			}
			if r := staleOf(t, eng, "proj"); !slices.Equal(r, want) {
				t.Fatalf("stale = %v, want %v", r, want)
			}
		})
	}
}

// TestEmbedderChangeKeepsVectors (R5): after an embedder change the search
// reports stale for the embed fingerprint, every vector and the sidecar are
// byte-identical, the writer is refused, notes are embedded (in memory) and
// local-chunk text is not. Restoring the old setting serves hits again.
func TestEmbedderChangeKeepsVectors(t *testing.T) {
	v := testVault(t)
	const local = "a local chunk with its vector"
	writeSessionNote(t, v.Root, "proj", "2026-05-13-aaaa0000-01", "2026-05-13", "wrap", "a session note body")
	a := NewEngine(newCountingEmbedder(384), v, storage.Config{SearchDefaultLimit: 10, EmbedderMaxSeqLen: 256})
	t.Cleanup(func() { a.Close() })
	commitArchive(t, a.cache, v, "proj", "sess-a", "sha-a", []string{local}, true)
	search(t, a, "proj", local)
	dir, _ := v.EmbedCacheDir("proj")
	before := dirHashes(t, dir)

	embB := newCountingEmbedder(384)
	b := NewEngine(embB, v, storage.Config{SearchDefaultLimit: 10, EmbedderMaxSeqLen: 512})
	t.Cleanup(func() { b.Close() })
	if hasContent(search(t, b, "proj", local), local) {
		t.Fatal("another regime's vector was served")
	}
	if r := staleOf(t, b, "proj"); !slices.Contains(r, staleEmbed) {
		t.Fatalf("stale = %v, want the embed fingerprint reason", r)
	}
	if !embB.sawText("a session note body") || embB.sawText(local) {
		t.Fatal("under another regime the notes must be embedded and local chunks must not")
	}
	if err := cachePut(t, b.cache, "proj", "x", unitVec(0)); !errors.Is(err, ErrEmbedRegimeMismatch) {
		t.Fatalf("a write under another regime: %v, want ErrEmbedRegimeMismatch", err)
	}
	if after := dirHashes(t, dir); !mapsEqual(before, after) {
		t.Fatalf("the cache changed under another regime:\n%v\n%v", before, after)
	}

	c := NewEngine(newCountingEmbedder(384), v, storage.Config{SearchDefaultLimit: 10, EmbedderMaxSeqLen: 256})
	t.Cleanup(func() { c.Close() })
	if !hasContent(search(t, c, "proj", local), local) {
		t.Fatal("back under the old regime the vector is not served")
	}
}

// dirHashes hashes every regular file in dir (vectors and the sidecar).
func dirHashes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.Type().IsRegular() {
			b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
			s := md5.Sum(b)
			out[e.Name()] = hex.EncodeToString(s[:])
		}
	}
	return out
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestMissingSidecarAndNoVectorsIsNotStale (R5): a fresh project with no
// sidecar and no vector: after a search the sidecar exists and nothing is
// stale.
func TestMissingSidecarAndNoVectorsIsNotStale(t *testing.T) {
	eng, v := testEngine(t)
	writeSessionNote(t, v.Root, "proj", "2026-05-13-aaaa0000-01", "2026-05-13", "wrap", "a note")
	search(t, eng, "proj", "a note")
	dir, _ := v.EmbedCacheDir("proj")
	if _, err := os.Stat(filepath.Join(dir, storage.EmbedCacheFingerprintFile)); err != nil {
		t.Fatalf("the first build wrote no sidecar: %v", err)
	}
	if stale, r, err := eng.Stale("proj"); err != nil || stale {
		t.Fatalf("a fresh project reads stale = %v %v, %v", stale, r, err)
	}
}

// TestVectorsWithNoSidecarAreStale (ADR-014 lines 202-204): vectors with no
// sidecar are a fingerprint mismatch, never missing_vectors; the search keeps
// them byte-identical, writes no sidecar and embeds no local-chunk text. A
// rebuild-driver stub holding a RebuildProof then discards them and clears
// stale; the next write puts the sidecar down.
func TestVectorsWithNoSidecarAreStale(t *testing.T) {
	eng, v, emb := countingEngine(t, storage.Config{})
	const local = "a local chunk whose vector lost its sidecar"
	commitArchive(t, eng.cache, v, "proj", "sess-a", "sha-a", []string{local}, true)
	dir, _ := v.EmbedCacheDir("proj")
	if err := os.Remove(filepath.Join(dir, storage.EmbedCacheFingerprintFile)); err != nil {
		t.Fatal(err)
	}
	before := dirHashes(t, dir)
	search(t, eng, "proj", local)
	if r := staleOf(t, eng, "proj"); !slices.Equal(r, []indexstore.StaleReason{staleEmbed}) {
		t.Fatalf("stale = %v, want only the embed fingerprint reason", r)
	}
	if after := dirHashes(t, dir); !mapsEqual(before, after) {
		t.Fatal("the search changed unattributed vectors or wrote a sidecar")
	}
	if emb.sawText(local) {
		t.Fatal("the search path embedded local-chunk text")
	}

	rl, ok, err := indexstore.TryRunLock(v, indexstore.KindRebuild, "proj")
	if err != nil || !ok {
		t.Fatalf("run lock: %v %v", ok, err)
	}
	defer rl.Release()
	proof, err := rl.CompletedRebuild("proj", nil)
	if err != nil {
		t.Fatal(err)
	}
	tx := lockStore(t, v, "proj")
	if err := tx.Discard(indexstore.DiscardVectors); err != nil {
		t.Fatal(err)
	}
	if err := tx.ClearStale(proof); err != nil {
		t.Fatal(err)
	}
	if err := tx.PutVectors(mustWriter(t, eng.cache, tx), map[string][]float32{index.ChunkID(local): mockVec(t, local)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if stale, r, err := eng.Stale("proj"); err != nil || stale {
		t.Fatalf("after the rebuild stub: stale = %v %v, %v", stale, r, err)
	}
}

// TestPersistedEmbedFingerprint (search-S3): a build records the regime the
// local tier was built under. With the cache's sidecar and vectors deleted by
// hand, an engine of another regime still reads stale for the embed
// fingerprint, from the completeness record alone.
func TestPersistedEmbedFingerprint(t *testing.T) {
	v := testVault(t)
	a := NewEngine(newCountingEmbedder(384), v, storage.Config{SearchDefaultLimit: 10, EmbedderMaxSeqLen: 256})
	t.Cleanup(func() { a.Close() })
	commitArchive(t, a.cache, v, "proj", "sess-a", "sha-a", []string{"a local chunk"}, true)
	search(t, a, "proj", "a local chunk")
	if record(t, v, "proj").EmbedFingerprint == "" {
		t.Fatal("the build recorded no embed fingerprint")
	}
	dir, _ := v.EmbedCacheDir("proj")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	b := NewEngine(nil, v, storage.Config{EmbedderMaxSeqLen: 512})
	if r := staleOf(t, b, "proj"); !slices.Equal(r, []indexstore.StaleReason{staleEmbed}) {
		t.Fatalf("stale = %v, want the embed reason from the record", r)
	}
	same := NewEngine(nil, v, storage.Config{EmbedderMaxSeqLen: 256})
	if r := staleOf(t, same, "proj"); len(r) != 0 {
		t.Fatalf("same regime: stale = %v, want none", r)
	}
}

// TestPersistedChunksFingerprint (2-S2): a build records the chunk recipe.
// With chunks.fingerprint deleted by hand and the recipe changed through
// config (chunker.max_chars), Stale reads the chunks reason from the record;
// with the recipe unchanged, nothing. The record holds no graph fingerprint.
func TestPersistedChunksFingerprint(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("HOME", t.TempDir())
	eng, v := testEngine(t)
	commitArchive(t, eng.cache, v, "proj", "sess-a", "sha-a", []string{"a local chunk"}, true)
	search(t, eng, "proj", "a local chunk")
	rec := record(t, v, "proj")
	if rec.ChunksFingerprint != projectRecipe(t, v, "proj").Sum() {
		t.Fatalf("recorded chunks fingerprint %q, want the recipe's", rec.ChunksFingerprint)
	}
	idir, _ := v.IndexDir("proj")
	raw, err := os.ReadFile(filepath.Join(idir, "completeness.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("graph")) || bytes.Contains(raw, []byte("hnsw")) {
		t.Fatalf("the completeness record names a graph fingerprint:\n%s", raw)
	}
	if err := os.Remove(filepath.Join(idir, "chunks.fingerprint")); err != nil {
		t.Fatal(err)
	}
	if r := staleOf(t, eng, "proj"); len(r) != 0 {
		t.Fatalf("recipe unchanged, sidecar gone: stale = %v, want none", r)
	}
	cfg := filepath.Join(cfgHome, "vibe-palace", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[meta]\nversion_major = 1\n\n[chunker]\nmax_chars = 1234\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if projectRecipe(t, v, "proj").Sum() == rec.ChunksFingerprint {
		t.Fatal("precondition: chunker.max_chars did not change the recipe")
	}
	if r := staleOf(t, eng, "proj"); !slices.Equal(r, []indexstore.StaleReason{staleChunks}) {
		t.Fatalf("recipe changed, sidecar gone: stale = %v, want the chunks reason from the record", r)
	}
}

// panicEmbedder fails the test if anything embeds or loads it.
type panicEmbedder struct{ embedder.Embedder }

func (panicEmbedder) Embed(context.Context, string) ([]float32, error) {
	panic("Stale loaded the model")
}
func (panicEmbedder) EmbedBatch(context.Context, []string) ([][]float32, error) {
	panic("Stale loaded the model")
}
func (panicEmbedder) Dimensions() (int, error) { panic("Stale loaded the model") }

// TestStaleLoadsNoModel (coverage-S2): Stale reads fingerprints as strings.
// With a mismatch on disk and no search ever run it reports it; with
// everything matching, or every fingerprint missing and no vector, nothing.
func TestStaleLoadsNoModel(t *testing.T) {
	v := testVault(t)
	ensureProjectDir(t, v, "proj")
	eng := NewEngine(panicEmbedder{}, v, storage.Config{EmbedderMaxSeqLen: 256})
	if stale, r, err := eng.Stale("proj"); err != nil || stale {
		t.Fatalf("nothing built: %v %v %v", stale, r, err)
	}
	dir, _ := v.EmbedCacheDir("proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	side := filepath.Join(dir, storage.EmbedCacheFingerprintFile)
	if err := os.WriteFile(side, []byte(eng.embedFingerprint()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if stale, r, err := eng.Stale("proj"); err != nil || stale {
		t.Fatalf("matching: %v %v %v", stale, r, err)
	}
	if err := os.WriteFile(side, []byte("vp-embed behaviour=0 model=other max_seq_len=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := staleOf(t, eng, "proj"); !slices.Equal(r, []indexstore.StaleReason{staleEmbed}) {
		t.Fatalf("mismatch: stale = %v", r)
	}
}

// TestGraphMismatchIsNotStale (ADR-014 lines 179-182): an hnsw.idx built
// under other graph parameters is not a stale reason, and a search sets none.
// The graph's fingerprint is not even read: no stale code names it.
func TestGraphMismatchIsNotStale(t *testing.T) {
	eng, v := testEngine(t)
	commitArchive(t, eng.cache, v, "proj", "sess-a", "sha-a", []string{"a local chunk"}, true)
	idir, _ := v.IndexDir("proj")
	if err := os.WriteFile(filepath.Join(idir, "hnsw.idx"), []byte("VPGRAPH fingerprint=other-M-and-ef\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	search(t, eng, "proj", "a local chunk")
	if stale, r, err := eng.Stale("proj"); err != nil || stale {
		t.Fatalf("graph mismatch: stale = %v %v, %v", stale, r, err)
	}
	src, err := os.ReadFile("completeness.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"hnsw", "Graph", "graph"} {
		for _, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, word) && !strings.HasPrefix(strings.TrimSpace(line), "//") {
				t.Errorf("completeness.go code mentions %q: %s", word, line)
			}
		}
	}
}

// TestStaleFlagLifecycle: set by a fingerprint mismatch on a search, still set
// after another search (even under the matching regime again), refused by the
// zero proof and by another project's proof, cleared by a completed rebuild's.
func TestStaleFlagLifecycle(t *testing.T) {
	v := testVault(t)
	ensureProjectDir(t, v, "other")
	writeSessionNote(t, v.Root, "proj", "2026-05-13-aaaa0000-01", "2026-05-13", "wrap", "a note")
	a := NewEngine(newCountingEmbedder(384), v, storage.Config{SearchDefaultLimit: 10, EmbedderMaxSeqLen: 256})
	t.Cleanup(func() { a.Close() })
	search(t, a, "proj", "a note")
	b := NewEngine(newCountingEmbedder(384), v, storage.Config{SearchDefaultLimit: 10, EmbedderMaxSeqLen: 512})
	t.Cleanup(func() { b.Close() })
	search(t, b, "proj", "a note")
	if !slices.Contains(record(t, v, "proj").Stale, staleEmbed) {
		t.Fatal("a search under another regime did not set the embed reason")
	}
	search(t, a, "proj", "a note")
	if !slices.Contains(record(t, v, "proj").Stale, staleEmbed) {
		t.Fatal("the next search cleared the flag; only a completed rebuild may")
	}

	tx := lockStore(t, v, "proj")
	if err := tx.ClearStale(indexstore.RebuildProof{}); err == nil {
		t.Fatal("the zero RebuildProof cleared stale")
	}
	rl, ok, err := indexstore.TryRunLock(v, indexstore.KindRebuild, "other")
	if err != nil || !ok {
		t.Fatalf("run lock: %v %v", ok, err)
	}
	otherProof, err := rl.CompletedRebuild("other", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.ClearStale(otherProof); err == nil {
		t.Fatal("another project's proof cleared stale")
	}
	proof, err := rl.CompletedRebuild("proj", []string{"sha-of-an-archive-that-failed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.ClearStale(proof); err != nil {
		t.Fatalf("a completed rebuild's proof (with a failed archive) was refused: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = rl.Release()
	if got := record(t, v, "proj").Stale; len(got) != 0 {
		t.Fatalf("stale after ClearStale = %v", got)
	}
}

// TestNoDiscardOnTheSearchPath (R5): a chunks.fingerprint mismatch found by a
// search sets the chunks reason and changes nothing else: the store's files
// are byte-identical, and the stored chunk is still served.
func TestNoDiscardOnTheSearchPath(t *testing.T) {
	eng, v := testEngine(t)
	const local = "a stored chunk that stays"
	commitArchive(t, eng.cache, v, "proj", "sess-a", "sha-a", []string{local}, true)
	idir, _ := v.IndexDir("proj")
	if err := os.WriteFile(filepath.Join(idir, "chunks.fingerprint"), []byte("vp-chunks indexer=0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := dirHashes(t, idir)
	if !hasContent(search(t, eng, "proj", local), local) {
		t.Fatal("the stored chunk is not served after a recipe mismatch")
	}
	after := dirHashes(t, idir)
	for _, f := range []string{"chunks.jsonl", "ledger.jsonl", "chunks.fingerprint"} {
		if before[f] != after[f] {
			t.Errorf("%s changed on the search path", f)
		}
	}
	if r := staleOf(t, eng, "proj"); !slices.Equal(r, []indexstore.StaleReason{staleChunks}) {
		t.Fatalf("stale = %v, want the chunks reason", r)
	}
}

// TestTrulyEmpty: true only with no notes, no iterations, no tracked
// transcript archive, no store chunk, and (with no marker) no tracked drawer.
// It loads no model.
func TestTrulyEmpty(t *testing.T) {
	marker := func(t *testing.T, v *storage.Vault) {
		writeMarker(t, v)
	}
	cases := []struct {
		name  string
		seed  func(t *testing.T, v *storage.Vault)
		empty bool
	}{
		{"nothing", func(*testing.T, *storage.Vault) {}, true},
		{"a note", func(t *testing.T, v *storage.Vault) {
			writeSessionNote(t, v.Root, "proj", "2026-05-13-aaaa0000-01", "2026-05-13", "wrap", "a note")
		}, false},
		{"an iteration", func(t *testing.T, v *storage.Vault) {
			writeIterationsMD(t, v.Root, "proj", "## Iteration 1 — one\n\nbody\n\n---\n")
		}, false},
		{"one tracked archive", func(t *testing.T, v *storage.Vault) {
			dir := filepath.Join(v.Root, "Projects", "proj", "transcripts")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "s.jsonl.zst"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"a store chunk", func(t *testing.T, v *storage.Vault) {
			appendDecision(t, v, "proj", "a decision")
		}, false},
		{"a tracked drawer", func(t *testing.T, v *storage.Vault) {
			addDrawer(t, v, "proj", "w", "r", "a drawer", "facts")
		}, false},
		{"a tracked drawer under the marker", func(t *testing.T, v *storage.Vault) {
			addDrawer(t, v, "proj", "w", "r", "a drawer", "facts")
			marker(t, v)
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := testVault(t)
			ensureProjectDir(t, v, "proj")
			tc.seed(t, v)
			eng := NewEngine(panicEmbedder{}, v, storage.Config{})
			got, err := eng.TrulyEmpty("proj")
			if err != nil || got != tc.empty {
				t.Fatalf("TrulyEmpty = %v, %v; want %v", got, err, tc.empty)
			}
		})
	}
}

func writeMarker(t *testing.T, v *storage.Vault) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(v.Root, ".vibe-palace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v.Root, ".vibe-palace", "vault.toml"), []byte("format = 2\nauthored_only = \"2026-10-03\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLiveSetCoversEveryTier (XC3b): the reaper's live set holds notes,
// iterations and, with no marker, tracked drawers (Tx.Reap adds every store
// chunk, ledgered or not); with the marker the tracked drawers drop out.
func TestLiveSetCoversEveryTier(t *testing.T) {
	eng, v := testEngine(t)
	writeSessionNote(t, v.Root, "proj", "2026-05-13-aaaa0000-01", "2026-05-13", "wrap", "a note")
	writeIterationsMD(t, v.Root, "proj", "## Iteration 1 — one\n\nbody\n\n---\n")
	d := addDrawer(t, v, "proj", "w", "r", "a drawer", "facts")
	noteIDs, _, _, _ := collectNoteCorpus(v, "proj")
	iterIDs, _, _, _ := collectIterationCorpus(v, "proj")
	live, err := eng.liveSet("proj")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range append(append([]string{d.ID}, noteIDs...), iterIDs...) {
		if !live[id] {
			t.Errorf("live set lacks %s", id)
		}
	}
	writeMarker(t, v)
	live, err = eng.liveSet("proj")
	if err != nil {
		t.Fatal(err)
	}
	if live[d.ID] {
		t.Error("with the marker, the tracked drawer is still live")
	}
	if !live[noteIDs[0]] {
		t.Error("with the marker, the note dropped out")
	}
}

// TestReaperKeepsTranscriptVectors (XC3b) is a REGRESSION GUARD, not a repro:
// the probe of 2026-10-04 passed at HEAD, because 1a's Tx.Reap already adds
// every store chunk. A lazy notes build and its reap leave all 100 ledgered
// transcript vectors in place.
func TestReaperKeepsTranscriptVectors(t *testing.T) {
	eng, v := testEngine(t)
	var contents []string
	for i := range 100 {
		contents = append(contents, fmt.Sprintf("transcript chunk %d", i))
	}
	commitArchive(t, eng.cache, v, "proj", "sess-a", "sha-a", contents, true)
	writeSessionNote(t, v.Root, "proj", "2026-05-13-aaaa0000-01", "2026-05-13", "wrap", "a note")
	search(t, eng, "proj", "a note")
	for _, c := range contents {
		p, _ := eng.cache.path("proj", index.ChunkID(c))
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("a lazy build reaped a transcript vector: %v", err)
		}
	}
}

// TestRebuildPrefersTheStoreAndDedupIgnoresTheWing (C1, 2-S1): a store chunk
// and a tracked drawer with the same content but another wing, room and date
// are one result, carrying the store's labels; one changed byte makes two.
func TestRebuildPrefersTheStoreAndDedupIgnoresTheWing(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%v", changed), func(t *testing.T) {
			eng, v := testEngine(t)
			const content = "the same words in the store and in a tracked drawer"
			commitArchive(t, eng.cache, v, "proj", "sess-a", "sha-a", []string{content}, true)
			dc := content
			if changed {
				dc = content + "!"
			}
			testutil.InitProject(t, v.Root, "proj")
			if err := v.AppendDrawer("proj", "beta", "beta-room", storage.Drawer{Content: dc, Hall: "facts", SourceType: "session", FiledAt: "2026-07-01T00:00:00Z"}); err != nil {
				t.Fatal(err)
			}
			st, err := eng.Rebuild(context.Background(), "proj")
			if err != nil {
				t.Fatal(err)
			}
			res := search(t, eng, "proj", content)
			var hits []SearchResult
			for _, r := range res {
				if strings.HasPrefix(r.Content, content) {
					hits = append(hits, r)
				}
			}
			if eng.collisions.Load() != 0 {
				t.Fatal("the dedup counted as a collision")
			}
			if changed {
				if len(hits) != 2 {
					t.Fatalf("different content: %d results, want 2", len(hits))
				}
				return
			}
			if len(hits) != 1 || st.DedupedDrawers != 1 {
				t.Fatalf("same content: %d results, %d deduped; want 1 and 1", len(hits), st.DedupedDrawers)
			}
			if hits[0].Wing != "store-wing" || hits[0].Room != "store-room" || hits[0].Date != "2026-05-13" {
				t.Fatalf("the surviving result carries %s/%s %s, want the store's labels", hits[0].Wing, hits[0].Room, hits[0].Date)
			}
		})
	}
}

// TestGlideDedupIgnores32BitCollisions: a store chunk and a tracked drawer of
// DIFFERENT content whose 32-bit drawer ids collide are both searchable: the
// dedup matches on the content hash, never on the drawer id.
func TestGlideDedupIgnores32BitCollisions(t *testing.T) {
	a, b := collidingContents(t, "wing")
	eng, v := testEngine(t)
	commitArchive(t, eng.cache, v, "proj", "sess-a", "sha-a", []string{a}, true)
	testutil.InitProject(t, v.Root, "proj")
	if err := v.AppendDrawer("proj", "wing", "room", storage.Drawer{Content: b, Hall: "facts", SourceType: "session", FiledAt: "2026-07-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if storage.DrawerID("wing", a) != storage.DrawerID("wing", b) {
		t.Fatal("precondition: the drawer ids do not collide")
	}
	if !hasContent(search(t, eng, "proj", a), a) || !hasContent(search(t, eng, "proj", b), b) {
		t.Fatal("one of two contents with colliding drawer ids is missing")
	}
}

// collidingContents finds two contents whose 32-bit DrawerIDs in wing collide
// (a birthday search, about 2^16 tries).
func collidingContents(t *testing.T, wing string) (string, string) {
	t.Helper()
	seen := map[string]string{}
	for i := 0; i < 1<<22; i++ {
		c := fmt.Sprintf("collision candidate %d", i)
		id := storage.DrawerID(wing, c)
		if prev, ok := seen[id]; ok {
			return prev, c
		}
		seen[id] = c
	}
	t.Fatal("no 32-bit collision found")
	return "", ""
}

// TestGlidePathReadsBothWithNoMarker: with no marker, a store chunk and a
// different tracked drawer are both searchable.
func TestGlidePathReadsBothWithNoMarker(t *testing.T) {
	eng, v := testEngine(t)
	commitArchive(t, eng.cache, v, "proj", "sess-a", "sha-a", []string{"chunk from the store"}, true)
	addDrawer(t, v, "proj", "w", "r", "drawer from the tracked tree", "facts")
	if !hasContent(search(t, eng, "proj", "chunk from the store"), "chunk from the store") ||
		!hasContent(search(t, eng, "proj", "drawer from the tracked tree"), "drawer from the tracked tree") {
		t.Fatal("with no marker, the store and the tracked drawers must both be read")
	}
}

// TestMarkerEndsTheGlidePath: with the marker, tracked drawers are not read.
func TestMarkerEndsTheGlidePath(t *testing.T) {
	eng, v := testEngine(t)
	ensureProjectDir(t, v, "proj")
	addDrawer(t, v, "proj", "w", "r", "drawer from the tracked tree", "facts")
	writeMarker(t, v)
	st, err := eng.Rebuild(context.Background(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	if st.Drawers != 0 {
		t.Fatalf("Rebuild read %d tracked drawers on a migrated vault", st.Drawers)
	}
	// On a migrated vault, tracked drawers are not read, so a project whose
	// only content is tracked drawers is truly empty: the search refuses it
	// with NothingIndexableError rather than serving the drawer (ADR-014
	// decision 8). This is the search-path counterpart of st.Drawers == 0.
	_, err = eng.Search(context.Background(), "drawer from the tracked tree", SearchFilters{Project: "proj", Limit: 20, IncludeRaw: true})
	var none *NothingIndexableError
	if !errors.As(err, &none) {
		t.Fatalf("marked vault, drawers only: got err %v, want *NothingIndexableError", err)
	}
}
