// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Unit A of hnsw-graph-file-envelope-and-warm-start: the on-disk hnsw.idx
// envelope, its save/load/delete, and warm-start. It adds NO per-project
// selection, off-e.mu wiring, catch-up, dual-write or heal — those are Units
// B–E. It writes nothing except through indexstore's Tx.WriteGraph /
// Tx.DeleteGraph (no parallel writer), and Load is lock-free.

package search

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// --- envelope format (§1) ---------------------------------------------------
//
// Little-endian throughout. CRC is CRC-32 Castagnoli (hardware-accelerated,
// stdlib, integrity-only). The header is fixed-width so a reader needs only its
// first graphHeaderSize bytes to read the fingerprint.
//
//	Header (56 B): magic[8] | envelopeVersion u32 | fingerprint[32] |
//	               bodyLen u64 | headerCRC u32 (crc32c over header[0:52])
//	Body (bodyLen B): idMap (u64 count, then u16 idLen|id|u64 key) | nextKey u64
//	               | tombstones (u64 count, then u64 keys) | sortedIDHash[32] |
//	               exportStream (library Graph.Export) | bodyCRC u32
//	               (crc32c over body[:len-4]).

// graphFileName is the on-disk graph file, under index/<project>/. It mirrors
// indexstore's unexported graphFile; indexstore owns the writes (Tx.WriteGraph),
// this package only stats it for the save debounce (graphFileModTime).
const graphFileName = "hnsw.idx"

var graphMagic = [8]byte{'V', 'P', 'H', 'N', 'S', 'W', 'G', '\n'}

const (
	graphEnvelopeVersion uint32 = 1 // the envelope's own version (16-N1), NOT the library's.
	graphHeaderSize             = 8 + 4 + 32 + 8 + 4
	graphFPOffset               = 12
	graphBodyLenOffset          = 44
	graphHeaderCRCOffset        = 52
	maxFPStringLen              = 255 // the library-version string in the fingerprint preimage.
)

var graphCRCTable = crc32.MakeTable(crc32.Castagnoli)

// loadResult is how a graph file was classified.
type loadResult int

const (
	loaded   loadResult = iota // a valid graph imported into a warm index.
	noGraph                    // no file present.
	mismatch                   // the fingerprint differs — NOT corrupt; leave the file for the heal (Unit E).
	corrupt                    // unreadable/bad CRC/truncated/failed Import or Validate — the caller deletes it.
)

func (r loadResult) String() string {
	switch r {
	case loaded:
		return "loaded"
	case noGraph:
		return "noGraph"
	case mismatch:
		return "mismatch"
	case corrupt:
		return "corrupt"
	default:
		return "unknown"
	}
}

// saveOutcome is why a save or delete wrote or did not write. Distinct reasons
// (S1) let Unit B re-arm a transient skip (skipTimeout, skipRename) while never
// re-arming a deliberate drop (skipEpoch, skipStale, skipMissingDir).
type saveOutcome int

const (
	written saveOutcome = iota
	skipTimeout
	skipRename
	skipEpoch
	skipStale
	skipMissingDir
	skipGenRead  // the store counter could not be read; the save did not happen.
	skipWriteErr // the write itself failed (not a post-rename sync); nothing to trust.
	skipDebounce // within the ≥30s save window (Unit C); the in-memory index stays current.
)

func (o saveOutcome) String() string {
	switch o {
	case written:
		return "written"
	case skipTimeout:
		return "skipTimeout"
	case skipRename:
		return "skipRename"
	case skipEpoch:
		return "skipEpoch"
	case skipStale:
		return "skipStale"
	case skipMissingDir:
		return "skipMissingDir"
	case skipGenRead:
		return "skipGenRead"
	case skipWriteErr:
		return "skipWriteErr"
	case skipDebounce:
		return "skipDebounce"
	default:
		return "unknown"
	}
}

// saveWitness is the live-graph state captured under the same RLock as a
// snapshot's Export (B4). saveGraph re-checks it under h.mu before writing and
// drops a save whose graph has moved on (a mutation, a swap, or a rebuild in
// flight), so a snapshot taken before a swap is never written over a newer one.
type saveWitness struct {
	gen        uint64
	rebuilding bool
	swaps      int64
}

// idKey is one persisted id→key pair.
type idKey struct {
	id  string
	key uint64
}

// computeFingerprint is the 32-byte graph fingerprint: SHA-256 over the
// canonical preimage {hnswLibraryVersion, dims, M, EfSearch} (S4). EfSearch
// stays in the fingerprint per ADR-014 (R1). A changed library version, dims, M
// or EfSearch therefore reads back as a mismatch, not a load.
func computeFingerprint(dims, m, ef int) [32]byte {
	h := sha256.New()
	ver := hnswLibraryVersion
	if len(ver) > maxFPStringLen {
		ver = ver[:maxFPStringLen]
	}
	var u2 [2]byte
	binary.LittleEndian.PutUint16(u2[:], uint16(len(ver)))
	h.Write(u2[:])
	h.Write([]byte(ver))
	var u4 [4]byte
	for _, x := range []int{dims, m, ef} {
		binary.LittleEndian.PutUint32(u4[:], uint32(x))
		h.Write(u4[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// sortedIDHash hashes the ids in ascending order, each length-prefixed, so a
// reordered or dropped id is caught even when every CRC still passes. ids must
// already be sorted ascending.
func sortedIDHash(sortedIDs []string) [32]byte {
	h := sha256.New()
	var u2 [2]byte
	for _, id := range sortedIDs {
		binary.LittleEndian.PutUint16(u2[:], uint16(len(id)))
		h.Write(u2[:])
		h.Write([]byte(id))
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func putU16(b *bytes.Buffer, v uint16) {
	var x [2]byte
	binary.LittleEndian.PutUint16(x[:], v)
	b.Write(x[:])
}
func putU32(b *bytes.Buffer, v uint32) {
	var x [4]byte
	binary.LittleEndian.PutUint32(x[:], v)
	b.Write(x[:])
}
func putU64(b *bytes.Buffer, v uint64) {
	var x [8]byte
	binary.LittleEndian.PutUint64(x[:], v)
	b.Write(x[:])
}

// encodeBody assembles the body region (ending in its own CRC) from a live
// id→key map, nextKey, the tombstoned keys, and the library export stream.
func encodeBody(live map[string]hnswEntry, nextKey uint64, tombs []uint64, export []byte) []byte {
	ids := make([]string, 0, len(live))
	for id := range live {
		ids = append(ids, id)
	}
	slices.Sort(ids) // deterministic bytes, and the order sortedIDHash expects.

	var b bytes.Buffer
	putU64(&b, uint64(len(ids)))
	for _, id := range ids {
		putU16(&b, uint16(len(id)))
		b.WriteString(id)
		putU64(&b, live[id].key)
	}
	putU64(&b, nextKey)
	putU64(&b, uint64(len(tombs)))
	for _, k := range tombs {
		putU64(&b, k)
	}
	sh := sortedIDHash(ids)
	b.Write(sh[:])
	b.Write(export)
	putU32(&b, crc32.Checksum(b.Bytes(), graphCRCTable))
	return b.Bytes()
}

// encodeEnvelope wraps a body in the fixed header with both CRCs.
func encodeEnvelope(fp [32]byte, body []byte) []byte {
	buf := make([]byte, graphHeaderSize+len(body))
	copy(buf[0:8], graphMagic[:])
	binary.LittleEndian.PutUint32(buf[8:12], graphEnvelopeVersion)
	copy(buf[graphFPOffset:graphFPOffset+32], fp[:])
	binary.LittleEndian.PutUint64(buf[graphBodyLenOffset:graphBodyLenOffset+8], uint64(len(body)))
	binary.LittleEndian.PutUint32(buf[graphHeaderCRCOffset:graphHeaderCRCOffset+4], crc32.Checksum(buf[0:graphHeaderCRCOffset], graphCRCTable))
	copy(buf[graphHeaderSize:], body)
	return buf
}

// snapshot serialises the live graph into a complete envelope and records the
// save witness, both under one RLock (B4). Export and Search are both reads;
// concurrent map reads are safe. Keys are assigned 0..nextKey-1 contiguously
// and never removed from the library graph, so the tombstoned keys are exactly
// [0,nextKey) minus the live keys — no graph iterator is needed.
func (h *hnswIndex) snapshot() ([]byte, saveWitness) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	w := saveWitness{gen: h.gen, rebuilding: h.rebuild != nil, swaps: h.swaps.Load()}

	var exp bytes.Buffer
	// Export only fails for an unregistered distance; cosineNaNGuard is
	// registered at package init and is the only distance production uses, so a
	// failure here cannot occur on any real path. A truncated stream would be
	// caught by the body CRC on load regardless.
	_ = h.st.g.Export(&exp)

	liveKeys := make(map[uint64]struct{}, len(h.st.live))
	for _, e := range h.st.live {
		liveKeys[e.key] = struct{}{}
	}
	var tombs []uint64
	for k := uint64(0); k < h.st.nextKey; k++ {
		if _, ok := liveKeys[k]; !ok {
			tombs = append(tombs, k)
		}
	}

	body := encodeBody(h.st.live, h.st.nextKey, tombs, exp.Bytes())
	fp := computeFingerprint(h.dims, h.params.M, h.params.EfSearch)
	return encodeEnvelope(fp, body), w
}

// --- header parsing (lock-free seams) ---------------------------------------

// parseHeader validates the fixed header and returns the stored fingerprint and
// body length. ok=false means a corrupt header (bad magic/version/CRC, or the
// file is shorter than one header).
func parseHeader(data []byte) (fp [32]byte, bodyLen uint64, ok bool) {
	if len(data) < graphHeaderSize {
		return fp, 0, false
	}
	if !bytes.Equal(data[0:8], graphMagic[:]) {
		return fp, 0, false
	}
	if binary.LittleEndian.Uint32(data[8:12]) != graphEnvelopeVersion {
		return fp, 0, false
	}
	if binary.LittleEndian.Uint32(data[graphHeaderCRCOffset:graphHeaderSize]) != crc32.Checksum(data[0:graphHeaderCRCOffset], graphCRCTable) {
		return fp, 0, false
	}
	copy(fp[:], data[graphFPOffset:graphFPOffset+32])
	bodyLen = binary.LittleEndian.Uint64(data[graphBodyLenOffset : graphBodyLenOffset+8])
	return fp, bodyLen, true
}

// readGraphHeader is the header-only, lock-free seam Unit E's heal uses to read
// a graph's fingerprint without parsing the body ("a few dozen bytes", N3).
func readGraphHeader(data []byte) (fp [32]byte, ok bool) {
	fp, _, ok = parseHeader(data)
	return
}

// decodeBodyPrefix reads everything before the export stream and returns the
// offset where the export stream begins. body excludes the trailing body CRC.
func decodeBodyPrefix(body []byte) (ids []idKey, nextKey uint64, tombs []uint64, exportStart int, err error) {
	p := 0
	readU16 := func() (uint16, bool) {
		if p+2 > len(body) {
			return 0, false
		}
		v := binary.LittleEndian.Uint16(body[p : p+2])
		p += 2
		return v, true
	}
	readU64 := func() (uint64, bool) {
		if p+8 > len(body) {
			return 0, false
		}
		v := binary.LittleEndian.Uint64(body[p : p+8])
		p += 8
		return v, true
	}

	n, ok := readU64()
	if !ok {
		return nil, 0, nil, 0, fmt.Errorf("truncated id-map count")
	}
	ids = make([]idKey, 0, n)
	// N-CR2: a crafted file with valid CRCs can carry a crossed map (a repeated
	// id or key) that sortedIDHash and Len do not catch; reject either repeat.
	seenIDs := make(map[string]struct{}, n)
	seenKeys := make(map[uint64]struct{}, n)
	for i := uint64(0); i < n; i++ {
		idLen, ok := readU16()
		if !ok {
			return nil, 0, nil, 0, fmt.Errorf("truncated id length")
		}
		if p+int(idLen) > len(body) {
			return nil, 0, nil, 0, fmt.Errorf("truncated id bytes")
		}
		id := string(body[p : p+int(idLen)])
		p += int(idLen)
		key, ok := readU64()
		if !ok {
			return nil, 0, nil, 0, fmt.Errorf("truncated id key")
		}
		if _, dup := seenIDs[id]; dup {
			return nil, 0, nil, 0, fmt.Errorf("duplicate id %q in id map", id)
		}
		if _, dup := seenKeys[key]; dup {
			return nil, 0, nil, 0, fmt.Errorf("duplicate key %d in id map", key)
		}
		seenIDs[id] = struct{}{}
		seenKeys[key] = struct{}{}
		ids = append(ids, idKey{id: id, key: key})
	}
	nextKey, ok = readU64()
	if !ok {
		return nil, 0, nil, 0, fmt.Errorf("truncated nextKey")
	}
	tn, ok := readU64()
	if !ok {
		return nil, 0, nil, 0, fmt.Errorf("truncated tombstone count")
	}
	tombs = make([]uint64, 0, tn)
	for i := uint64(0); i < tn; i++ {
		k, ok := readU64()
		if !ok {
			return nil, 0, nil, 0, fmt.Errorf("truncated tombstone key")
		}
		tombs = append(tombs, k)
	}
	if p+32 > len(body) {
		return nil, 0, nil, 0, fmt.Errorf("truncated sorted-id hash")
	}
	var stored [32]byte
	copy(stored[:], body[p:p+32])
	p += 32

	sortedIDs := make([]string, len(ids))
	for i, e := range ids {
		sortedIDs[i] = e.id
	}
	slices.Sort(sortedIDs)
	if sortedIDHash(sortedIDs) != stored {
		return nil, 0, nil, 0, fmt.Errorf("sorted-id hash mismatch")
	}
	return ids, nextKey, tombs, p, nil
}

// --- load (§3) --------------------------------------------------------------

// loadGraph reads hnsw.idx lock-free and classifies it. It never writes or
// deletes and never panics on bad bytes. A corrupt file is reported as
// `corrupt` for the caller to delete under the commit lock via deleteGraph (a
// held *indexstore.Tx; Load stays lock-free and TestLockDiscipline forbids this
// package from taking the commit lock except through (*projectLock).Tx). A
// real I/O error is surfaced and the file is NOT deleted.
func loadGraph(vault *storage.Vault, project string, dims int, params hnswParams) (*hnswIndex, loadResult, error) {
	data, err := indexstore.ReadGraph(vault, project)
	if err != nil {
		return nil, corrupt, err
	}
	if data == nil {
		return nil, noGraph, nil
	}

	fp, bodyLen, ok := parseHeader(data)
	if !ok {
		return nil, corrupt, nil
	}
	// B1: the file is exactly one header plus its declared body.
	if uint64(len(data)) != uint64(graphHeaderSize)+bodyLen {
		return nil, corrupt, nil
	}
	if fp != computeFingerprint(dims, params.M, params.EfSearch) {
		return nil, mismatch, nil // leave the file byte-identical for Unit E.
	}

	body := data[graphHeaderSize:]
	if len(body) < 4 {
		return nil, corrupt, nil
	}
	if binary.LittleEndian.Uint32(body[len(body)-4:]) != crc32.Checksum(body[:len(body)-4], graphCRCTable) {
		return nil, corrupt, nil
	}
	prefix := body[:len(body)-4]
	ids, nextKey, tombs, exportStart, derr := decodeBodyPrefix(prefix)
	if derr != nil {
		return nil, corrupt, nil
	}
	exportSlice := prefix[exportStart:] // B1: exactly the export bytes, no trailing tail.

	g := params.newGraph()
	if err := g.Import(bytes.NewReader(exportSlice)); err != nil {
		return nil, corrupt, nil
	}
	if err := g.Validate(); err != nil {
		return nil, corrupt, nil
	}

	// B2: the library overwrote these from the file on Import; re-apply the
	// configured regime (Ml is not in hnswParams — use defaultMl).
	g.M = params.M
	g.EfSearch = params.EfSearch
	g.Ml = defaultMl
	g.Distance = cosineNaNGuard
	if params.distance != nil {
		g.Distance = params.distance
	}
	g.Rng = rand.New(rand.NewSource(params.Seed))

	st := hnswState{
		g:       g,
		live:    make(map[string]hnswEntry, len(ids)),
		ids:     make(map[uint64]string, len(ids)),
		nextKey: nextKey,
	}
	for _, e := range ids {
		v, ok := g.Lookup(e.key) // B3: a live id absent from the imported graph is corrupt.
		if !ok {
			return nil, corrupt, nil
		}
		st.live[e.id] = hnswEntry{key: e.key, vec: v}
		st.ids[e.key] = e.id
	}
	// Cross-check the tombstone set: every tombstone present in the graph and
	// not live, and the graph holds exactly live+tombstoned nodes.
	if g.Len() != len(ids)+len(tombs) {
		return nil, corrupt, nil
	}
	for _, k := range tombs {
		if _, live := st.ids[k]; live {
			return nil, corrupt, nil
		}
		if _, ok := g.Lookup(k); !ok {
			return nil, corrupt, nil
		}
	}
	return newHNSWIndexFromState(dims, params, st), loaded, nil
}

// newHNSWIndexFromDisk warm-starts a project's HNSW index from hnsw.idx. It
// returns the index and the file's load classification (loadResult): a non-nil
// idx only on loaded; (nil, mismatch) for a fingerprint mismatch, (nil, corrupt)
// for an unreadable/bad file, (nil, noGraph) for no file. Unit B's selector
// switches on that classification (loaded/mismatch/corrupt/noGraph) to pick the
// kind and the file action. Deleting a corrupt or below-threshold file is the
// caller's job (deleteGraph under a held Tx) — this keeps Load lock-free. An I/O
// fault returns a non-nil error (res is corrupt); the caller falls back to brute
// for that build and does NOT delete (Unit A's do-not-delete rule).
func newHNSWIndexFromDisk(vault *storage.Vault, project string, dims int, params hnswParams) (*hnswIndex, loadResult, error) {
	return loadGraph(vault, project, dims, params)
}

// --- save / delete (§2), each on a held commit-lock Tx -----------------------

// saveGraph writes data as hnsw.idx through the caller's held Tx. It drops the
// save when the live graph has moved past the snapshot (B4) or when the store's
// epoch has changed under the lock, and classifies a write failure (S2/N5). The
// caller must Commit or Release the Tx (the gen bump happens there).
func saveGraph(tx *indexstore.Tx, vault *storage.Vault, project string, h *hnswIndex, data []byte, w saveWitness, builtAt indexstore.Gen) (saveOutcome, error) {
	// B4: re-check under h.mu, not the commit lock.
	h.mu.RLock()
	stale := h.gen != w.gen || h.rebuild != nil || h.swaps.Load() != w.swaps
	h.mu.RUnlock()
	if stale {
		return skipStale, nil
	}

	dir, err := vault.IndexDir(project)
	if err != nil {
		return skipMissingDir, err
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return skipMissingDir, nil // never recreate index/<p>.
		}
		return skipMissingDir, statErr
	}

	cur, err := indexstore.ReadGeneration(vault, project)
	if err != nil {
		return skipGenRead, err // N-CR4: the counter could not be read — the save did not happen.
	}
	if cur.Epoch != builtAt.Epoch {
		return skipEpoch, nil // a supersede/discard/relabel happened; the subset save is void.
	}

	if werr := tx.WriteGraph(data); werr != nil {
		return classifyWriteError(werr)
	}
	return written, nil
}

// deleteGraph removes hnsw.idx through the caller's held Tx (a corrupt file, or
// later Unit B's below-L deletion). The caller Commits/Releases.
func deleteGraph(tx *indexstore.Tx) (saveOutcome, error) {
	if err := tx.DeleteGraph(); err != nil {
		return classifyWriteError(err)
	}
	return written, nil
}

// classifyWriteError maps an indexstore write failure to a save outcome by its
// error chain (S2/N5). A "sync dir: " failure means the rename already
// succeeded, so the NEW bytes are in place: treat it as written (the caller
// logs; the next Load re-reads), never as "the old file survived". A "rename: "
// failure means the new bytes never landed — a transient skip to re-arm. Any
// other failure is a genuine write error (B-CR1): it is NOT `written` — the
// bytes did not land and nothing may trust the save — so it returns the
// error-carrying skipWriteErr, which the caller never counts as a successful
// write.
func classifyWriteError(err error) (saveOutcome, error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "sync dir:"):
		return written, nil
	case strings.Contains(msg, "rename:"):
		return skipRename, nil
	default:
		return skipWriteErr, err
	}
}

// --- debounced save window (Unit C, Scope 5) --------------------------------

// graphSaveDebounce is the minimum interval between two graph saves of one
// project: a converting rebuild or a catch-up saves at most once per window, so
// a busy project does not rewrite hnsw.idx on every rebuild. It is a fixed
// policy constant (a sibling of provisionalHNSWParams), not a config knob. The
// in-memory index stays current across a skipped save, and a restart reconciles
// the slightly-stale file by catch-up, so the window never costs correctness.
const graphSaveDebounce = 30 * time.Second

// nowFn is the clock the debounce reads; a test seam. Production is time.Now.
var nowFn = time.Now

// graphFileModTime reports hnsw.idx's modification time, the host-local,
// per-project "last saved" timestamp the debounce compares against nowFn. The
// file is written under the commit lock (Tx.WriteGraph's atomic rename), so a
// caller holding that lock reads a value no concurrent saver is mid-changing.
// Contract: a MISSING file is (zero, false, nil) — not debounced, so the first
// convert always saves; ANY OTHER stat error is (_, false, err) — the caller
// must treat err as "do not skip" and fall through to saveGraph (the safe
// direction). A stat failure is never reported as present==true, so a flaky
// stat can never silently suppress a save. The store generation counter is NOT
// used here: it is a change counter, not a clock, and bumps on every write.
func graphFileModTime(vault *storage.Vault, project string) (time.Time, bool, error) {
	dir, err := vault.IndexDir(project)
	if err != nil {
		return time.Time{}, false, err
	}
	fi, err := os.Stat(filepath.Join(dir, graphFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return fi.ModTime(), true, nil
}
