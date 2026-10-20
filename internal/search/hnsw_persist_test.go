// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// --- fixtures ---------------------------------------------------------------

// smallParams are the cheap, multi-layer-friendly test parameters (16-N4): a
// real graph under -race without production's (32,100) build cost.
func smallParams(ef int) hnswParams {
	return hnswParams{M: 8, EfSearch: ef, Seed: 1}
}

// newGraphVault returns a hermetic vault with one existing project and its
// index dir, so indexstore.Lock succeeds and Tx.WriteGraph has somewhere to go.
func newGraphVault(t *testing.T) (*storage.Vault, string) {
	t.Helper()
	root := t.TempDir()
	const project = "graphproj"
	if err := os.MkdirAll(filepath.Join(root, "Projects", project), 0o755); err != nil {
		t.Fatal(err)
	}
	v := storage.NewVault(root)
	dir, err := v.IndexDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return v, project
}

func graphFilePath(t *testing.T, v *storage.Vault, project string) string {
	t.Helper()
	dir, err := v.IndexDir(project)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "hnsw.idx")
}

// writeGraphFile writes raw envelope bytes to hnsw.idx through a held Tx (tests
// may take the commit lock directly, unlike package code).
func writeGraphFile(t *testing.T, v *storage.Vault, project string, data []byte) {
	t.Helper()
	tx, err := indexstore.Lock(context.Background(), v, project, indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.WriteGraph(data); err != nil {
		_ = tx.Release()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// deleteViaTx deletes hnsw.idx through a held Tx, the way Unit B will after a
// `corrupt` classification.
func deleteViaTx(t *testing.T, v *storage.Vault, project string) {
	t.Helper()
	tx, err := indexstore.Lock(context.Background(), v, project, indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deleteGraph(tx); err != nil {
		_ = tx.Release()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// buildHNSW builds an n-vector, dims-dimensional index with the given params.
func buildHNSW(t *testing.T, dims, n int, params hnswParams) *hnswIndex {
	t.Helper()
	h := newHNSWIndex(dims, params)
	rng := rand.New(rand.NewSource(42))
	vecs := make([][]float32, n)
	ids := make([]string, n)
	for i := range vecs {
		v := make([]float32, dims)
		for j := range v {
			v[j] = rng.Float32()*2 - 1
		}
		vecs[i] = v
		ids[i] = fmt.Sprintf("id-%06d", i)
	}
	if err := h.Build(context.Background(), vecs, ids); err != nil {
		t.Fatal(err)
	}
	return h
}

// saveBuilt snapshots h and writes the envelope, returning the bytes written.
func saveBuilt(t *testing.T, v *storage.Vault, project string, h *hnswIndex) []byte {
	t.Helper()
	data, _ := h.snapshot()
	writeGraphFile(t, v, project, data)
	return data
}

// --- craft helpers for the structural-defect tests -------------------------

type craftNode struct {
	key       uint64
	vec       []float32
	neighbors []uint64
}

func putVarint(b *bytes.Buffer, v int64) {
	var x [binary.MaxVarintLen64]byte
	n := binary.PutVarint(x[:], v)
	b.Write(x[:n])
}

// craftExportStream emits exactly the coder/hnsw Graph.Export format for a
// one-layer graph, so a test can inject a stream the real library would never
// produce (a dangling neighbour, an unknown distance name).
func craftExportStream(distName string, m int, ml float64, ef int, nodes []craftNode) []byte {
	var b bytes.Buffer
	putVarint(&b, 1) // encodingVersion
	putVarint(&b, int64(m))
	_ = binary.Write(&b, binary.LittleEndian, ml)
	putVarint(&b, int64(ef))
	putVarint(&b, int64(len(distName)))
	b.WriteString(distName)
	putVarint(&b, 1)                 // nLayers
	putVarint(&b, int64(len(nodes))) // nNodes in layer 0
	for _, nd := range nodes {
		_ = binary.Write(&b, binary.LittleEndian, nd.key)
		putVarint(&b, int64(len(nd.vec)))
		_ = binary.Write(&b, binary.LittleEndian, nd.vec)
		putVarint(&b, int64(len(nd.neighbors)))
		for _, nk := range nd.neighbors {
			_ = binary.Write(&b, binary.LittleEndian, nk)
		}
	}
	return b.Bytes()
}

// craftEnvelope wraps a crafted export stream in a valid envelope whose header
// fingerprint matches (dims, m, ef), with correct CRCs, so Load reaches Import.
func craftEnvelope(dims, m, ef int, live map[string]hnswEntry, export []byte) []byte {
	body := encodeBody(live, uint64(len(live)), nil, export)
	return encodeEnvelope(computeFingerprint(dims, m, ef), body)
}

// --- the tests --------------------------------------------------------------

// 1. Round-trip parity (S6): a loaded graph answers like the source. Compared
// as membership (ties in neighbour-map iteration are not byte-stable, N1).
func TestGraphRoundTrip(t *testing.T) {
	v, project := newGraphVault(t)
	const dims, n = 384, 1200
	params := smallParams(16)
	h := buildHNSW(t, dims, n, params)
	saveBuilt(t, v, project, h)

	idx, res, err := loadGraph(v, project, dims, params)
	if err != nil {
		t.Fatal(err)
	}
	if res != loaded {
		t.Fatalf("res = %v, want loaded", res)
	}
	if idx.Len() != h.Len() {
		t.Fatalf("loaded Len %d, source Len %d", idx.Len(), h.Len())
	}
	// Deterministic round-trip: every id maps back to its own key and its own
	// vector, byte-for-byte (random 384-d vectors are near-equidistant, so an
	// approximate-recall assertion would be flaky; state equality is stronger
	// and is exactly what the idMap-mispairing and skipped-Lookup mutants break).
	if idx.st.nextKey != h.st.nextKey {
		t.Fatalf("loaded nextKey %d, source %d", idx.st.nextKey, h.st.nextKey)
	}
	for id, src := range h.st.live {
		got, ok := idx.st.live[id]
		if !ok {
			t.Fatalf("id %q missing after load", id)
		}
		if got.key != src.key {
			t.Fatalf("id %q key %d after load, want %d", id, got.key, src.key)
		}
		if !equalVectors(got.vec, src.vec) {
			t.Fatalf("id %q vector differs after load", id)
		}
		if idx.st.ids[got.key] != id {
			t.Fatalf("reverse map: key %d -> %q, want %q", got.key, idx.st.ids[got.key], id)
		}
	}
	// A query on each graph still answers without error, exercising the search
	// path over a warm-loaded graph.
	for i, q := range sampleVectors(h, 20) {
		if _, err := idx.Search(q, 5); err != nil {
			t.Fatalf("search %d on loaded graph: %v", i, err)
		}
	}
}

// sampleVectors returns up to n stored vectors from h.
func sampleVectors(h *hnswIndex, n int) [][]float32 {
	out := make([][]float32, 0, n)
	for _, e := range h.st.live {
		if len(out) >= n {
			break
		}
		out = append(out, e.vec)
	}
	return out
}

// 2. Length bound (B1): trailing bytes past the declared body are rejected.
// Mutant: bound body to bodyLen and drop the exact-length check (accept longer).
func TestGraphLoadRejectsTrailingBytes(t *testing.T) {
	v, project := newGraphVault(t)
	const dims = 32
	params := smallParams(16)
	h := buildHNSW(t, dims, 200, params)
	data := saveBuilt(t, v, project, h)

	writeGraphFile(t, v, project, append(append([]byte{}, data...), 0xAA, 0xBB, 0xCC))
	_, res, err := loadGraph(v, project, dims, params)
	if err != nil {
		t.Fatal(err)
	}
	if res != corrupt {
		t.Fatalf("res = %v, want corrupt (trailing bytes)", res)
	}
}

// 3. Body CRC (B1): a corrupted body CRC trailer is rejected. Mutant: skip the
// body-CRC check — the rest of the body is intact, so the file would load.
func TestGraphLoadRejectsBadBodyCRC(t *testing.T) {
	v, project := newGraphVault(t)
	const dims = 32
	params := smallParams(16)
	h := buildHNSW(t, dims, 200, params)
	data := saveBuilt(t, v, project, h)

	bad := append([]byte{}, data...)
	bad[len(bad)-1] ^= 0xFF // flip a byte of the stored body CRC
	writeGraphFile(t, v, project, bad)
	_, res, err := loadGraph(v, project, dims, params)
	if err != nil {
		t.Fatal(err)
	}
	if res != corrupt {
		t.Fatalf("res = %v, want corrupt (bad body CRC)", res)
	}
}

// 4. Header CRC: a corrupted header CRC is rejected. Mutant: skip the header-CRC
// check — the header "parses", the fingerprint matches, and the file loads.
func TestGraphLoadRejectsBadHeaderCRC(t *testing.T) {
	v, project := newGraphVault(t)
	const dims = 32
	params := smallParams(16)
	h := buildHNSW(t, dims, 200, params)
	data := saveBuilt(t, v, project, h)

	bad := append([]byte{}, data...)
	bad[graphHeaderCRCOffset] ^= 0xFF // flip a byte of the stored header CRC
	writeGraphFile(t, v, project, bad)
	_, res, err := loadGraph(v, project, dims, params)
	if err != nil {
		t.Fatal(err)
	}
	if res != corrupt {
		t.Fatalf("res = %v, want corrupt (bad header CRC)", res)
	}
}

// 5. Fingerprint mismatch leaves the file. Mutant: treat a mismatch as corrupt
// and delete it.
func TestGraphLoadFingerprintMismatchLeavesFile(t *testing.T) {
	v, project := newGraphVault(t)
	const dims = 32
	h := buildHNSW(t, dims, 200, smallParams(16))
	data := saveBuilt(t, v, project, h)
	path := graphFilePath(t, v, project)

	for _, tc := range []struct {
		name   string
		params hnswParams
		dims   int
	}{
		{"EfSearch", smallParams(100), dims},
		{"M", hnswParams{M: 16, EfSearch: 16, Seed: 1}, dims},
		{"dims", smallParams(16), dims + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx, res, err := loadGraph(v, project, tc.dims, tc.params)
			if err != nil {
				t.Fatal(err)
			}
			if res != mismatch {
				t.Fatalf("res = %v, want mismatch", res)
			}
			if idx != nil {
				t.Fatal("want nil index on mismatch")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, data) {
				t.Fatal("hnsw.idx was modified on a fingerprint mismatch")
			}
		})
	}
}

// 6. Warm start performs no build (B5a/R2). Mutant: a loader that rebuilds from
// vectors (calls g.Add) instead of Importing.
func TestGraphWarmStartSkipsBuild(t *testing.T) {
	v, project := newGraphVault(t)
	const dims = 128
	params := smallParams(16)
	h := buildHNSW(t, dims, 400, params)
	saveBuilt(t, v, project, h)

	idx, res, err := newHNSWIndexFromDisk(v, project, dims, params)
	if err != nil {
		t.Fatal(err)
	}
	if res != loaded || idx == nil {
		t.Fatalf("warm start did not load: res=%v idx=%v", res, idx)
	}
	if got := idx.libraryAdds.Load(); got != 0 {
		t.Fatalf("libraryAdds = %d after a warm start, want 0 (load Imports, never Adds)", got)
	}
	if idx.Len() != h.Len() {
		t.Fatalf("warm Len %d, source Len %d", idx.Len(), h.Len())
	}
}

// 7. Parameters survive Import (B2). The export stream carries EfSearch/Ml that
// differ from the configured regime; Load must re-apply the configured values.
// Mutant: omit any single re-apply.
func TestGraphLoadReappliesParams(t *testing.T) {
	v, project := newGraphVault(t)
	const dims = 32
	params := smallParams(100) // configured EfSearch 100
	h := buildHNSW(t, dims, 200, params)

	// Bake DIFFERENT values into the export stream before snapshotting; the
	// header fingerprint still reflects the configured params.
	h.st.g.EfSearch = 16
	h.st.g.Ml = 0.5
	data, _ := h.snapshot()
	writeGraphFile(t, v, project, data)

	idx, res, err := loadGraph(v, project, dims, params)
	if err != nil || res != loaded {
		t.Fatalf("load: res=%v err=%v", res, err)
	}
	g := idx.st.g
	if g.EfSearch != 100 {
		t.Fatalf("EfSearch = %d, want 100", g.EfSearch)
	}
	if g.Ml != defaultMl {
		t.Fatalf("Ml = %v, want %v", g.Ml, defaultMl)
	}
	if g.M != params.M {
		t.Fatalf("M = %d, want %d", g.M, params.M)
	}
	if reflect.ValueOf(g.Distance).Pointer() != reflect.ValueOf(cosineNaNGuard).Pointer() {
		t.Fatal("Distance was not re-applied to cosineNaNGuard")
	}
	if g.Rng == nil {
		t.Fatal("Rng was not re-seeded")
	}
}

// 8. Validate rejects a dangling neighbour (B3). Mutant: skip g.Validate().
func TestGraphLoadValidateCatchesDanglingNeighbour(t *testing.T) {
	v, project := newGraphVault(t)
	const dims, m, ef = 4, 8, 16
	nodes := []craftNode{
		{key: 0, vec: []float32{1, 0, 0, 0}, neighbors: []uint64{999}}, // 999 is not a node
		{key: 1, vec: []float32{0, 1, 0, 0}, neighbors: []uint64{0}},
	}
	export := craftExportStream(distanceName, m, 0.25, ef, nodes)
	live := map[string]hnswEntry{"a": {key: 0}, "b": {key: 1}}
	data := craftEnvelope(dims, m, ef, live, export)
	writeGraphFile(t, v, project, data)

	_, res, err := loadGraph(v, project, dims, hnswParams{M: m, EfSearch: ef, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res != corrupt {
		t.Fatalf("res = %v, want corrupt (dangling neighbour)", res)
	}
}

// 9. Unknown distance name (Import failure → corrupt). Mutant: classify an
// Import error as mismatch (leave the file) instead of corrupt.
func TestGraphLoadRejectsUnknownDistance(t *testing.T) {
	v, project := newGraphVault(t)
	const dims, m, ef = 4, 8, 16
	nodes := []craftNode{{key: 0, vec: []float32{1, 0, 0, 0}}}
	export := craftExportStream("no-such-distance", m, 0.25, ef, nodes)
	live := map[string]hnswEntry{"a": {key: 0}}
	data := craftEnvelope(dims, m, ef, live, export)
	writeGraphFile(t, v, project, data)

	_, res, err := loadGraph(v, project, dims, hnswParams{M: m, EfSearch: ef, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res != corrupt {
		t.Fatalf("res = %v, want corrupt (unknown distance)", res)
	}
}

// 10. Save during rebuild is dropped (B4). A gen change after the snapshot must
// drop the save. Mutant: saveGraph ignores the witness and writes anyway.
func TestGraphSaveDroppedWhenGraphMoves(t *testing.T) {
	v, project := newGraphVault(t)
	const dims = 32
	params := smallParams(16)
	h := buildHNSW(t, dims, 200, params)

	data, w := h.snapshot()

	tx, err := indexstore.Lock(context.Background(), v, project, indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	// B-CR1: read the counter AFTER Lock (it now exists) so builtAt's epoch
	// equals the one saveGraph re-reads under the lock. Then the epoch check
	// passes and ONLY the witness drop can stop the write — so the
	// witness-bypass mutant is actually killed (it would otherwise be masked by
	// skipEpoch, since a freshly ensured counter carries a random non-zero epoch).
	builtAt := tx.Generation()

	// The live graph moves on after the snapshot (a new write bumps h.gen), so
	// the witness no longer matches.
	if err := h.Insert("late-arrival", randVec(dims)); err != nil {
		t.Fatal(err)
	}

	out, err := saveGraph(tx, v, project, h, data, w, builtAt)
	if err != nil {
		_ = tx.Release()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if out != skipStale {
		t.Fatalf("outcome = %v, want skipStale", out)
	}
	if _, err := os.Stat(graphFilePath(t, v, project)); !os.IsNotExist(err) {
		t.Fatalf("hnsw.idx should not exist after a dropped save, stat err = %v", err)
	}
}

func randVec(dims int) []float32 {
	rng := rand.New(rand.NewSource(7))
	v := make([]float32, dims)
	for i := range v {
		v[i] = rng.Float32()*2 - 1
	}
	return v
}

// S-CR1. classifyWriteError maps an indexstore write error by its chain.
// Mutant: swap the "rename:"/"sync dir:" Contains cases.
func TestClassifyWriteError(t *testing.T) {
	if o, err := classifyWriteError(fmt.Errorf("indexstore: write x: %w", fmt.Errorf("rename: %w", errors.New("EXDEV")))); o != skipRename || err != nil {
		t.Fatalf("rename chain -> (%v, %v), want (skipRename, nil)", o, err)
	}
	if o, err := classifyWriteError(fmt.Errorf("indexstore: write x: %w", fmt.Errorf("sync dir: %w", errors.New("EIO")))); o != written || err != nil {
		t.Fatalf("sync-dir chain -> (%v, %v), want (written, nil)", o, err)
	}
	// B-CR1: a genuine (non-sync-dir) write failure is NOT `written` — the bytes
	// did not land, so nothing may trust the save. It carries the error.
	plain := errors.New("disk on fire")
	o, err := classifyWriteError(plain)
	if o != skipWriteErr || err == nil {
		t.Fatalf("plain error -> (%v, %v), want (skipWriteErr, non-nil)", o, err)
	}
}

// S-CR2. deleteGraph removes hnsw.idx through a held Tx (the corrupt-file path
// depends on it). Its DeleteGraph error, like saveGraph's, maps through
// classifyWriteError (TestClassifyWriteError).
func TestGraphDeleteRemovesFile(t *testing.T) {
	v, project := newGraphVault(t)
	h := buildHNSW(t, 32, 100, smallParams(16))
	saveBuilt(t, v, project, h)
	path := graphFilePath(t, v, project)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("precondition: hnsw.idx should exist, stat err = %v", err)
	}
	deleteViaTx(t, v, project) // exercises deleteGraph
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("hnsw.idx should be gone after deleteGraph, stat err = %v", err)
	}
}

// craftBodyRaw encodes a body from an explicit (id,key) list, which — unlike
// encodeBody's map input — can carry a duplicate id or key.
func craftBodyRaw(pairs []idKey, nextKey uint64, export []byte) []byte {
	var b bytes.Buffer
	putU64(&b, uint64(len(pairs)))
	for _, e := range pairs {
		putU16(&b, uint16(len(e.id)))
		b.WriteString(e.id)
		putU64(&b, e.key)
	}
	putU64(&b, nextKey)
	putU64(&b, 0) // no tombstones
	sortedIDs := make([]string, len(pairs))
	for i, e := range pairs {
		sortedIDs[i] = e.id
	}
	slices.Sort(sortedIDs)
	sh := sortedIDHash(sortedIDs)
	b.Write(sh[:])
	b.Write(export)
	putU32(&b, crc32.Checksum(b.Bytes(), graphCRCTable))
	return b.Bytes()
}

// N-CR2. A crafted file (valid CRCs, matching sortedIDHash and Len) with a
// repeated id is a crossed map and must be corrupt. Mutant: drop the duplicate
// check in decodeBodyPrefix — the crossed map would then load.
func TestGraphLoadRejectsDuplicateID(t *testing.T) {
	v, project := newGraphVault(t)
	const dims, m, ef = 4, 8, 16
	nodes := []craftNode{
		{key: 0, vec: []float32{1, 0, 0, 0}},
		{key: 1, vec: []float32{0, 1, 0, 0}},
	}
	export := craftExportStream(distanceName, m, 0.25, ef, nodes)
	body := craftBodyRaw([]idKey{{id: "a", key: 0}, {id: "a", key: 1}}, 2, export)
	data := encodeEnvelope(computeFingerprint(dims, m, ef), body)
	writeGraphFile(t, v, project, data)

	_, res, err := loadGraph(v, project, dims, hnswParams{M: m, EfSearch: ef, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res != corrupt {
		t.Fatalf("res = %v, want corrupt (duplicate id)", res)
	}
}
