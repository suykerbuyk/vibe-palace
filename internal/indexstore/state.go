// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
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
type state struct {
	gen        Gen
	ids        map[string]struct{}
	ownerPairs map[string]struct{}
	kgPairs    map[string]struct{}
	ledgerRecs []ledgerRecord
	ledger     *Ledger
}

func pairKey(id string, o Owner) string { return id + "\x00" + o.key() }

var (
	stateMu    sync.Mutex
	stateCache = map[string]*state{}
)

func stateKey(root, project string) string { return root + "\x00" + project }

// cachedState returns the cached state for (root, project) if it matches g.
func cachedState(root, project string, g Gen) *state {
	stateMu.Lock()
	defer stateMu.Unlock()
	if s, ok := stateCache[stateKey(root, project)]; ok && s.gen == g {
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

// loadState reads the store's files into a fresh state.
func loadState(pf projectFiles, g Gen) (*state, error) {
	st, err := readStoreFiles(pf)
	if err != nil {
		return nil, err
	}
	s := &state{gen: g, ledgerRecs: st.ledgerRecs, ledger: st.ledger}
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
