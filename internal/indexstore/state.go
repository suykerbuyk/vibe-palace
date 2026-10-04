// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"errors"
	"io/fs"
	"os"
	"sync"
)

// state is what a writer needs to know without rereading the store on every
// append: the chunk ids, the (id, owner) pairs and the KG (id, owner) pairs
// already present, and the ledger. It is held in memory per process, per
// vault and project, and is valid for one value of the store change counter.
//
// The cache is its own holder in the sense of Tx.Generation: it remembers the
// counter it matches, and Lock reloads it when the counter moved because
// another process wrote the store. After a Commit whose every write succeeded
// it adopts the counter that Commit left, because it saw every one of that
// Tx's writes; after a failed write it is dropped.
//
// The counter alone is not enough. A process that appends and dies before
// its Commit bumps the counter leaves records no counter announces, and a
// cache valid on the counter alone would then miss them: re-ingest an
// archive the ledger already holds, record a failure count from a stale
// value, or commit a session's other archive where it must supersede. So the
// cache also remembers the sizes of the three files a Tx appends to
// (storeSizes), and is valid only while they are unchanged. Appends only grow
// those files, and every write that is more than an append bumps the counter
// first (beginDestructive), so the counter and the sizes together announce
// every change.
type state struct {
	gen        Gen
	sizes      storeSize
	ids        map[string]struct{}
	ownerPairs map[string]struct{}
	kgPairs    map[string]struct{}
	ledgerRecs []ledgerRecord
	ledger     *Ledger
}

// storeSize is the sizes of a project's chunks.jsonl, KG records file and
// ledger.jsonl, -1 for a file that is absent; ok is false when one could not
// be stat'ed, and such a size matches nothing.
type storeSize struct {
	chunks, kg, ledger int64
	ok                 bool
}

// storeSizes stats the three files a Tx appends to. Taken under the commit
// lock, so no writer can change them in between.
func storeSizes(pf projectFiles) storeSize {
	sz := storeSize{ok: true}
	for _, f := range []struct {
		path string
		out  *int64
	}{{pf.chunks, &sz.chunks}, {pf.kg, &sz.kg}, {pf.ledger, &sz.ledger}} {
		fi, err := os.Stat(f.path)
		switch {
		case err == nil:
			*f.out = fi.Size()
		case errors.Is(err, fs.ErrNotExist):
			*f.out = -1
		default:
			sz.ok = false
		}
	}
	return sz
}

func (a storeSize) matches(b storeSize) bool { return a.ok && b.ok && a == b }

func pairKey(id string, o Owner) string { return id + "\x00" + o.key() }

var (
	stateMu    sync.Mutex
	stateCache = map[string]*state{}
)

func stateKey(root, project string) string { return root + "\x00" + project }

// cachedState returns the cached state for (root, project) if it matches the
// counter g and the store files pf are still the sizes it recorded. The
// caller holds the commit lock.
func cachedState(root, project string, g Gen, pf projectFiles) *state {
	stateMu.Lock()
	s, ok := stateCache[stateKey(root, project)]
	stateMu.Unlock()
	if ok && s.gen == g && s.sizes.matches(storeSizes(pf)) {
		return s
	}
	return nil
}

func putState(root, project string, s *state) {
	stateMu.Lock()
	defer stateMu.Unlock()
	if s == nil {
		delete(stateCache, stateKey(root, project))
		return
	}
	stateCache[stateKey(root, project)] = s
}

// loadState reads the store's files into a fresh state. The sizes are taken
// before the read, under the commit lock, so they describe what was read.
func loadState(pf projectFiles, g Gen) (*state, error) {
	sizes := storeSizes(pf)
	st, err := readStoreFiles(pf)
	if err != nil {
		return nil, err
	}
	s := &state{gen: g, sizes: sizes, ledgerRecs: st.ledgerRecs, ledger: st.ledger}
	s.indexChunks(st.chunks)
	s.indexKG(st.kg)
	return s, nil
}

func (s *state) indexChunks(f *chunkFold) {
	s.ids = make(map[string]struct{}, len(f.chunks))
	s.ownerPairs = map[string]struct{}{}
	for id := range f.chunks {
		s.ids[id] = struct{}{}
		for _, ol := range f.owners[id] {
			s.ownerPairs[pairKey(id, ol.Owner)] = struct{}{}
		}
	}
}

func (s *state) indexKG(f *kgFold) {
	s.kgPairs = map[string]struct{}{}
	for id, os := range f.owners {
		for _, o := range os {
			s.kgPairs[pairKey(id, o)] = struct{}{}
		}
	}
}

func (s *state) addLedger(recs ...ledgerRecord) {
	s.ledgerRecs = append(s.ledgerRecs, recs...)
	s.ledger = foldLedger(s.ledgerRecs)
}
