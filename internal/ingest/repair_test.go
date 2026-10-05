// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
)

func (f *fixture) cacheDir(project string) string {
	return filepath.Join(f.v.Root, "palace", ".local", "embed-cache", project)
}

// dropVectors deletes n cached vectors of chunks owned by owner.
func (f *fixture) dropVectors(project string, owner indexstore.Owner, n int) []string {
	f.t.Helper()
	var gone []string
	for _, c := range f.store(project).Chunks(true) {
		if len(gone) == n {
			break
		}
		if slices.Contains(c.Owners, owner) {
			if err := os.Remove(filepath.Join(f.cacheDir(project), c.ID+".vec")); err != nil {
				f.t.Fatal(err)
			}
			gone = append(gone, c.ID)
		}
	}
	if len(gone) != n {
		f.t.Fatalf("only %d vectors to drop", len(gone))
	}
	return gone
}

// setStale writes completeness reasons for project.
func (f *fixture) setStale(project string, reasons ...indexstore.StaleReason) {
	f.t.Helper()
	tx, err := indexstore.Lock(context.Background(), f.v, project, indexstore.NoTimeout)
	if err != nil {
		f.t.Fatal(err)
	}
	defer tx.Release()
	rec, err := tx.Completeness()
	if err != nil {
		f.t.Fatal(err)
	}
	rec.Stale = append(rec.Stale, reasons...)
	if _, err := tx.WriteCompleteness(rec); err != nil {
		f.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) reasons(project string) []indexstore.StaleReason {
	f.t.Helper()
	rec, err := indexstore.ReadCompleteness(f.v, project)
	if err != nil {
		f.t.Fatal(err)
	}
	return rec.Stale
}

var (
	missingVectors = indexstore.StaleReason{Kind: indexstore.StaleMissingVectors}
	chunkFP        = indexstore.StaleReason{Kind: indexstore.StaleFingerprint, Fingerprint: indexstore.FingerprintChunks}
)

// TestRepairReembedsMissingVectors: a ledgered archive with 3 vectors deleted
// and the missing-vector reason set: the run re-embeds exactly those 3 and
// clears the reason. With a budget of one, two archives missing vectors get
// one repaired per run. With the fingerprint reason also set, the project is
// skipped and both reasons stay.
func TestRepairReembedsMissingVectors(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	a := f.archiveOf("alpha", "A", transcript(t0, "a", 6), day(1))
	f.run(RunOptions{})
	f.dropVectors("alpha", indexstore.ArchiveOwner(a.Manifest.SourceSHA256), 3)
	f.setStale("alpha", missingVectors)
	before := f.emb.texts.Load()
	f.run(RunOptions{})
	if got := f.emb.texts.Load() - before; got != 3 {
		t.Fatalf("re-embedded %d texts, want exactly the 3 missing", got)
	}
	if r := f.reasons("alpha"); len(r) != 0 {
		t.Fatalf("stale reasons %v after the repair, want none", r)
	}

	b := f.archiveOf("alpha", "B", transcript(t0, "b", 6), day(2))
	f.run(RunOptions{})
	f.dropVectors("alpha", indexstore.ArchiveOwner(a.Manifest.SourceSHA256), 2)
	f.dropVectors("alpha", indexstore.ArchiveOwner(b.Manifest.SourceSHA256), 2)
	f.setStale("alpha", missingVectors)
	before = f.emb.texts.Load()
	f.run(RunOptions{Budget: RunBudget{Archives: 1}})
	if got := f.emb.texts.Load() - before; got != 2 {
		t.Fatalf("a budget of one repaired %d vectors, want one archive's 2", got)
	}
	if r := f.reasons("alpha"); !slices.Contains(r, missingVectors) {
		t.Fatal("the reason was cleared with vectors still missing")
	}

	f.setStale("alpha", chunkFP)
	before = f.emb.texts.Load()
	f.run(RunOptions{})
	if got := f.emb.texts.Load() - before; got != 0 {
		t.Fatalf("a project stale for a fingerprint reason was repaired (%d texts)", got)
	}
	if r := f.reasons("alpha"); !slices.Contains(r, missingVectors) || !slices.Contains(r, chunkFP) {
		t.Fatalf("reasons %v, want both kept", r)
	}
}

// TestRepairReingestsAChunkShortfall: a ledgered archive whose record counts
// more chunks than it owns is re-ingested, and then owns them all.
func TestRepairReingestsAChunkShortfall(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	a := f.archiveOf("alpha", "A", transcript(t0, "a", 6), day(1))
	f.run(RunOptions{})
	owner := indexstore.ArchiveOwner(a.Manifest.SourceSHA256)
	want := f.store("alpha").CountChunks(owner)
	path := filepath.Join(f.v.Root, "palace", ".local", "index", "alpha", "chunks.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	dropped := 0
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.Contains(l, `"kind":"owner"`) && dropped < 3 {
			dropped++
			continue
		}
		keep = append(keep, l)
	}
	if err := os.WriteFile(path, []byte(strings.Join(keep, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := f.store("alpha").CountChunks(owner); got != want-3 {
		t.Fatalf("precondition: owns %d, want %d", got, want-3)
	}
	res := f.run(RunOptions{})
	if res.Repaired != 1 || f.store("alpha").CountChunks(owner) != want {
		t.Fatalf("repaired %d; owns %d, want %d", res.Repaired, f.store("alpha").CountChunks(owner), want)
	}
	if s, _ := f.live("alpha", "A"); s.Generation != 1 {
		t.Fatalf("a repair changed the generation: %+v", s)
	}
}

// TestRepairOfImportBatches: on a project of import batches alone, a batch
// with 2 missing vectors gets them re-embedded; a batch owning fewer chunks
// than recorded is reported with one Warn, and no archive is opened.
func TestRepairOfImportBatches(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("beta")
	ix, err := palace.ProjectIndexing(f.v, "beta")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := indexstore.Lock(context.Background(), f.v, "beta", indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	tx.UseRecipe(ix.Recipe)
	w, err := f.eng.CacheWriter(tx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"mempalace:x:0", "mempalace:x:1"} {
		c := indexstore.BatchCommit{BatchID: id, StartDay: "2026-04-01", Vectors: map[string][]float32{}}
		for i := range 3 {
			content := id + " drawer " + string(rune('a'+i))
			oc := indexstore.OwnedChunk{Chunk: indexstore.Chunk{ID: index.ChunkID(content), Content: content, Wing: "beta", Room: "general"}}
			c.Chunks = append(c.Chunks, oc)
			c.Vectors[oc.ID] = make([]float32, 384)
		}
		if err := tx.CommitBatch(c, w); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f.dropVectors("beta", indexstore.BatchOwner("mempalace:x:0"), 2)
	path := filepath.Join(f.v.Root, "palace", ".local", "index", "beta", "chunks.jsonl")
	data, _ := os.ReadFile(path)
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.Contains(l, `"kind":"owner"`) && strings.Contains(l, "mempalace:x:1") && strings.Contains(l, index.ChunkID("mempalace:x:1 drawer a")) {
			continue
		}
		keep = append(keep, l)
	}
	os.WriteFile(path, []byte(strings.Join(keep, "\n")+"\n"), 0o644)
	logs := captureLogs(t)
	opened := countOpens(t)
	before := f.emb.texts.Load()
	f.run(RunOptions{Project: "beta"})
	if got := f.emb.texts.Load() - before; got != 2 {
		t.Fatalf("re-embedded %d, want the batch's 2", got)
	}
	if w := logs.warns("import batch owns fewer chunks"); len(w) != 1 || w[0].attrs["batch"] != "mempalace:x:1" {
		t.Fatalf("warns %+v", w)
	}
	if len(opened()) != 0 {
		t.Fatal("an archive was opened for a batch")
	}
}

// recordingHealer records each HealGraph call and whether the run lock was
// held during it.
type recordingHealer struct {
	f     *fixture
	mu    sync.Mutex
	calls []string
	held  []bool
	live  []bool
}

func (h *recordingHealer) HealGraph(_ context.Context, vault, project string) (HealResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, project)
	_, held, _ := indexstore.TryRunLock(h.f.v, indexstore.KindIngest, "")
	h.held = append(h.held, !held)
	if l, ok, _ := indexstore.TryRunLock(h.f.v, indexstore.KindIngest, ""); ok {
		l.Release()
	}
	_, isLive := h.f.live(project, "S-"+project)
	h.live = append(h.live, isLive)
	return HealResult{}, nil
}

// TestGraphHealIsCalledPerProject: one HealGraph call per project the run
// visited, after its commit, with the run lock held; none for a project
// skipped as stale; SkipHeal skips only the named project; the heal embeds
// nothing; with no healer the run is unchanged.
func TestGraphHealIsCalledPerProject(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.ensureLedger("beta")
	f.archiveOf("alpha", "S-alpha", transcript(t0, "a", 2), day(1))
	f.archiveOf("beta", "S-beta", transcript(t0, "b", 2), day(2))
	h := &recordingHealer{f: f}
	d := f.deps()
	d.GraphHealer = h
	if _, err := Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha", Budget: RunBudget{Archives: 10}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.calls, []string{"alpha", "beta"}) || slices.Contains(h.held, false) || slices.Contains(h.live, false) {
		t.Fatalf("calls %v held %v live %v, want alpha then beta, held, after their commits", h.calls, h.held, h.live)
	}

	calls := f.emb.calls.Load()
	h.calls, h.held, h.live = nil, nil, nil
	if _, err := Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha", SkipHeal: true}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.calls, []string{"beta"}) || f.emb.calls.Load() != calls {
		t.Fatalf("SkipHeal for alpha: calls %v, embed calls %d -> %d", h.calls, calls, f.emb.calls.Load())
	}

	f.setStale("beta", chunkFP)
	h.calls = nil
	if _, err := Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(h.calls, "beta") {
		t.Fatalf("a stale project was healed: %v", h.calls)
	}

	held, ok, err := indexstore.TryRunLock(f.v, indexstore.KindRebuild, "alpha")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	h.calls = nil
	if _, err := RunHeld(context.Background(), d, held, RunOptions{VaultRoot: f.v.Root, Project: "alpha"}); err != nil {
		t.Fatal(err)
	}
	held.Release()
	if !slices.Equal(h.calls, []string{"alpha"}) {
		t.Fatalf("RunHeld calls %v, want alpha (beta is stale)", h.calls)
	}

	f.run(RunOptions{}) // no healer: completes
}
