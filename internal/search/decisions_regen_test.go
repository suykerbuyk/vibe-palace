// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// regenerateDecisionChunks had NO test of its own before this unit
// (`grep -rn regenerateDecisionChunks --include=*_test.go` returned nothing),
// even though it is the loop the fold cache exists for. These two cover the
// correctness bar the fold change has to preserve: a full regeneration, and a
// regeneration cancelled part-way.

// rawChunksFile returns a project's chunks.jsonl byte for byte. Assertions here
// are on RAW BYTES, not on the folded Store view: foldChunkLines keeps only the
// first chunk line per id, so a duplicated line — what a broken write-through
// produces — is invisible through Store.Chunks.
func rawChunksFile(t *testing.T, v *storage.Vault, project string) []byte {
	t.Helper()
	dir, err := v.IndexDir(project)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "chunks.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return data
}

// decisionOwnerIDs is the sorted set of note owners that own a decision chunk.
func decisionOwnerIDs(t *testing.T, v *storage.Vault, project string) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, c := range storeDecisions(t, v, project) {
		for _, o := range c.Owners {
			if o.Kind == indexstore.OwnerNote {
				seen[o.ID] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// runRegenerate drives regenerateDecisionChunks exactly as rebuild does:
// under the project lock, with one genChain, and finishing the Tx whatever the
// loop returned (decisions.go calls pl.finishTx BEFORE it inspects writeErr,
// which is why a cancelled regeneration is durable at all).
func runRegenerate(t *testing.T, e *Engine, ctx context.Context, project string) error {
	t.Helper()
	pl, err := e.lockProject(context.Background(), project, 5*time.Second)
	if err != nil {
		t.Fatalf("lockProject: %v", err)
	}
	defer pl.release()
	chain := genChain{}
	return e.regenerateDecisionChunks(ctx, pl, 5*time.Second, &chain, project)
}

// writeDecisionsPerNoteTx files each note's decisions in its OWN Tx, the way
// the capture path does (internal/capture/decisions.go's fileDecisionDrawers).
// It is the equivalence target for the one-Tx loop.
func writeDecisionsPerNoteTx(t *testing.T, v *storage.Vault, project string, stems []string, decisions map[string][]string, dates map[string]string) {
	t.Helper()
	ix, err := palace.ProjectIndexing(v, project)
	if err != nil {
		t.Fatal(err)
	}
	for _, stem := range stems {
		tx, err := indexstore.Lock(context.Background(), v, project, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		tx.UseRecipe(ix.Recipe)
		if err := WriteNoteDecisions(tx, ix.Recipe, project, stem, "", dates[stem], decisions[stem]); err != nil {
			_ = tx.Release()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}

// A full regeneration in ONE Tx must produce byte-for-byte the same
// chunks.jsonl as the same work done one Tx per note — over a corpus that
// exercises every branch: notes added, a note whose decision set changed
// (rewrite), and a note deleted from disk (the gone loop, a rewrite by
// construction).
func TestRegenerateDecisionChunksFullRegenerationMatchesPerNoteTxs(t *testing.T) {
	const project = "proj"
	stems := []string{
		"2026-05-13-aaaa0001-01",
		"2026-05-13-aaaa0002-01",
		"2026-05-13-aaaa0003-01",
		"2026-05-13-aaaa0004-01",
	}
	dates := map[string]string{}
	for _, s := range stems {
		dates[s] = "2026-05-13"
	}
	// The state the store starts in: note 1 carries two decisions, note 3 is
	// present and will later be deleted from disk.
	seeded := map[string][]string{
		stems[0]: {"D0 keep"},
		stems[1]: {"D1 keep", "D1 drop"},
		stems[2]: {"D2 keep"},
		stems[3]: {"D3 gone"},
	}
	// The state the notes on disk will describe at regeneration time: note 1
	// lost a decision (rewrite), note 2 gained one (append), note 3 is gone.
	final := map[string][]string{
		stems[0]: {"D0 keep"},
		stems[1]: {"D1 keep"},
		stems[2]: {"D2 keep", "D2 added"},
	}

	build := func(t *testing.T, oneTx bool) ([]byte, *storage.Vault) {
		t.Helper()
		eng, v := testEngine(t)
		ensureProjectDir(t, v, project)
		// Seed the store through per-note Txs, as capture would.
		var seedStems []string
		seedStems = append(seedStems, stems...)
		writeDecisionsPerNoteTx(t, v, project, seedStems, seeded, dates)

		// Now put the notes on disk in their final shape; note 3 is absent.
		for _, s := range stems[:3] {
			writeDecisionNoteWithSession(t, v.Root, project, s, dates[s], "", final[s]...)
		}

		if oneTx {
			if err := runRegenerate(t, eng, context.Background(), project); err != nil {
				t.Fatalf("regenerateDecisionChunks: %v", err)
			}
		} else {
			writeDecisionsPerNoteTx(t, v, project, stems[:3], final, dates)
			// The gone note, exactly as the regenerator's gone loop does it.
			tx, err := indexstore.Lock(context.Background(), v, project, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			ix, err := palace.ProjectIndexing(v, project)
			if err != nil {
				t.Fatal(err)
			}
			tx.UseRecipe(ix.Recipe)
			if err := tx.ReplaceOwned(indexstore.NoteOwner(noteSourceRef(stems[3])), nil); err != nil {
				_ = tx.Release()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		return rawChunksFile(t, v, project), v
	}

	oneTxBytes, vOne := build(t, true)
	perTxBytes, _ := build(t, false)

	if !bytes.Equal(oneTxBytes, perTxBytes) {
		t.Fatalf("one-Tx regeneration differs from per-note Txs.\none-Tx:\n%s\nper-note:\n%s", oneTxBytes, perTxBytes)
	}

	// And the result is actually right, not merely consistent.
	got := decisionOwnerIDs(t, vOne, project)
	want := []string{noteSourceRef(stems[0]), noteSourceRef(stems[1]), noteSourceRef(stems[2])}
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("decision owners %v, want %v", got, want)
	}
	var texts []string
	for _, c := range storeDecisions(t, vOne, project) {
		texts = append(texts, c.Content)
	}
	sort.Strings(texts)
	if wantTexts := []string{"D0 keep", "D1 keep", "D2 added", "D2 keep"}; !slices.Equal(texts, wantTexts) {
		t.Fatalf("decision texts %v, want %v: the dropped and gone decisions survived", texts, wantTexts)
	}
	// No id may sit on two chunk lines.
	for _, c := range storeDecisions(t, vOne, project) {
		line := []byte(`{"kind":"chunk","id":"` + c.ID + `"`)
		if n := bytes.Count(oneTxBytes, line); n != 1 {
			t.Fatalf("chunk line for %s appears %d times in the raw file, want 1", c.ID, n)
		}
	}
}

// cancelAfterNDecisions is a ctx that reports Canceled once the project's
// chunks.jsonl already holds n decision owner lines.
//
// It gives the loop a DETERMINISTIC cancellation point with no production hook:
// regenerateDecisionChunks checks ctx.Err() at the TOP of each iteration
// (decisions.go), so when the file already carries n notes' decisions the check
// at the start of iteration n fires. Boundary, stated because it is easy to get
// off by one: notes 0..n-1 are written and note n is never started — n notes
// are durable, not n-1.
type cancelAfterNDecisions struct {
	context.Context
	t       *testing.T
	v       *storage.Vault
	project string
	n       int
	done    chan struct{}
	closed  bool
}

func (c *cancelAfterNDecisions) tripped() bool {
	if c.closed {
		return true
	}
	data := rawChunksFile(c.t, c.v, c.project)
	if bytes.Count(data, []byte(`"source_type":"`+storage.SourceTypeDecision+`"`)) >= c.n {
		c.closed = true
		close(c.done)
		return true
	}
	return false
}

func (c *cancelAfterNDecisions) Err() error {
	if c.tripped() {
		return context.Canceled
	}
	return nil
}

func (c *cancelAfterNDecisions) Done() <-chan struct{} {
	c.tripped()
	return c.done
}

// A regeneration cancelled part-way is DURABLE for the notes it reached and
// leaves the rest untouched — neither corrupted nor half-written — and a later
// full regeneration finishes the job.
//
// This is not a hypothetical path: pl.finishTx() runs at decisions.go before
// writeErr is inspected, so the Tx commits whatever the loop managed.
func TestRegenerateDecisionChunksCancelledMidLoopCommitsThePrefix(t *testing.T) {
	const project = "proj"
	const k = 2
	eng, v := testEngine(t)
	ensureProjectDir(t, v, project)

	var stems []string
	for i := range 5 {
		s := fmt.Sprintf("2026-05-13-bbbb000%d-01", i)
		stems = append(stems, s)
		writeDecisionNoteWithSession(t, v.Root, project, s, "2026-05-13", "", fmt.Sprintf("decision %d", i))
	}
	// filepath.Glob is sorted, and collectNoteDecisionSets iterates it in that
	// order, so the prefix the loop reaches is the lexicographic one.
	if !sort.StringsAreSorted(stems) {
		t.Fatal("fixture: stems must be in glob order")
	}

	ctx := &cancelAfterNDecisions{
		Context: context.Background(),
		t:       t, v: v, project: project, n: k,
		done: make(chan struct{}),
	}
	err := runRegenerate(t, eng, ctx, project)
	if err == nil {
		t.Fatal("a cancelled regeneration returned nil")
	}
	if !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("err = %v, want a context cancellation", err)
	}

	gotOwners := decisionOwnerIDs(t, v, project)
	var wantOwners []string
	for _, s := range stems[:k] {
		wantOwners = append(wantOwners, noteSourceRef(s))
	}
	sort.Strings(wantOwners)
	if !slices.Equal(gotOwners, wantOwners) {
		t.Fatalf("after the cancel, decision owners %v, want exactly the first %d (%v)", gotOwners, k, wantOwners)
	}

	// The untouched tail must be absent, not half-written.
	raw := rawChunksFile(t, v, project)
	for _, s := range stems[k:] {
		if bytes.Contains(raw, []byte(noteSourceRef(s))) {
			t.Fatalf("note %s was never reached but appears in chunks.jsonl:\n%s", s, raw)
		}
	}
	// Every line still parses and no chunk line is duplicated.
	for _, c := range storeDecisions(t, v, project) {
		if n := bytes.Count(raw, []byte(`{"kind":"chunk","id":"`+c.ID+`"`)); n != 1 {
			t.Fatalf("chunk line for %s appears %d times, want 1", c.ID, n)
		}
	}

	// A later, uncancelled regeneration finishes the remaining notes.
	if err := runRegenerate(t, eng, context.Background(), project); err != nil {
		t.Fatalf("second regeneration: %v", err)
	}
	gotOwners = decisionOwnerIDs(t, v, project)
	wantOwners = nil
	for _, s := range stems {
		wantOwners = append(wantOwners, noteSourceRef(s))
	}
	sort.Strings(wantOwners)
	if !slices.Equal(gotOwners, wantOwners) {
		t.Fatalf("after the full regeneration, decision owners %v, want all %d (%v)", gotOwners, len(stems), wantOwners)
	}
}
