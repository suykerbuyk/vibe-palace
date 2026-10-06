// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"testing"
)

// TestDecisionTrackedWriterReachIsClean is the gate: no note-time, ingest or
// import entry point in this repository reaches a tracked-tree writer except
// through an allow-listed cut. It runs only without -short, alongside the
// derived gate, because that is the sourceaudit run `make test` skips (the
// unit's gate 4). The baseline is empty for this kind, so a clean tree reports
// nothing and the test asserts exactly that.
func TestDecisionTrackedWriterReachIsClean(t *testing.T) {
	skipUnlessFullSuite(t)

	files, err := loadPackages(repoRoots...)
	if err != nil {
		t.Fatalf("load packages: %v", err)
	}
	findings := decisionTrackedReach(files)
	for _, f := range findings {
		t.Errorf("NEW decision-tracked-reach finding\n  %s\n  at %s\n  %s\n"+
			"  A note-time/ingest/import root reaches a tracked-tree writer. Route the write through "+
			"the host-local store (indexstore.Tx), or add the function on the path to "+
			"decisionTrackedWriterAllow with a reason.",
			f.Symbol, f.Pos, f.Detail)
	}
}

// TestIndexTranscriptAllowIsPresentlyInert locks in the review finding that the
// capture.IndexTranscript allow-list entry changes NOTHING on the real tree
// today: buildCallGraph cannot see IndexTranscript's field-receiver write
// (idx.vault.AppendDrawers, internal/capture/indexer.go:145 — a selector call
// whose receiver is not a bare ident), so the rule is clean with or without the
// entry. It is the runtime proof behind decisionTrackedWriterAllow's comment.
//
// 🔴 It will START FAILING the day sourceaudit-callgraph-blind-to-field-receiver-
// method-calls fixes that blind spot — which is the SIGNAL that the entry has
// become genuinely load-bearing (the rule would then fire for WriteSession et al.
// without it). When that happens, this test documents the transition: delete it
// and keep the entry, don't re-add a cut-removal.
func TestIndexTranscriptAllowIsPresentlyInert(t *testing.T) {
	skipUnlessFullSuite(t)

	files, err := loadPackages(repoRoots...)
	if err != nil {
		t.Fatalf("load packages: %v", err)
	}

	orig := decisionTrackedWriterAllow["capture.IndexTranscript"]
	delete(decisionTrackedWriterAllow, "capture.IndexTranscript")
	defer func() {
		if orig {
			decisionTrackedWriterAllow["capture.IndexTranscript"] = true
		}
	}()

	if findings := decisionTrackedReach(files); len(findings) != 0 {
		var syms []string
		for _, f := range findings {
			syms = append(syms, f.Symbol)
		}
		t.Fatalf("removing capture.IndexTranscript from the allow-list changed the result: %v.\n"+
			"The field-receiver blind spot is likely fixed, so the entry is now LOAD-BEARING — keep it, "+
			"and update/remove this test to document the transition rather than treating it as a regression.", syms)
	}
}

// decisionReachSymbols runs the rule over a fixture tree and returns the symbols
// it flagged.
func decisionReachSymbols(t *testing.T, srcByPkg map[string]string) []string {
	t.Helper()
	root := writeMultiPkgFixture(t, srcByPkg)
	files, err := loadPackages(root)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	var syms []string
	for _, f := range decisionTrackedReach(files) {
		syms = append(syms, f.Symbol)
	}
	return syms
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestDecisionTrackedReachFiresOnAPlantedWriter is the positive case: a decision
// writer that reintroduces a tracked-tree append is caught.
func TestDecisionTrackedReachFiresOnAPlantedWriter(t *testing.T) {
	syms := decisionReachSymbols(t, map[string]string{
		"storage": `package storage

func AppendDrawers() error { return nil }
`,
		"capture": `package capture

import "example.com/fixture/storage"

func fileDecisionDrawers() error { return storage.AppendDrawers() }
`,
	})
	if !containsStr(syms, "capture.fileDecisionDrawers") {
		t.Errorf("rule did not fire on fileDecisionDrawers -> storage.AppendDrawers; got %v", syms)
	}
}

// TestDecisionTrackedReachIsQuietOnHostLocalWrites is the negative case: the real
// shape, where the ingester and the decision writer write the HOST-LOCAL store
// through indexstore.Tx methods. Flagging a Tx method would fail the build on
// correct code.
func TestDecisionTrackedReachIsQuietOnHostLocalWrites(t *testing.T) {
	syms := decisionReachSymbols(t, map[string]string{
		"storage": `package storage

func AppendDrawers() error { return nil }
func AddTriple() error     { return nil }
`,
		"indexstore": `package indexstore

type Tx struct{}

func (tx *Tx) Append() error   { return nil }
func (tx *Tx) AppendKG() error { return nil }
`,
		"capture": `package capture

import "example.com/fixture/indexstore"

func fileDecisionDrawers(tx *indexstore.Tx) error { return tx.Append() }
`,
		"ingest": `package ingest

import "example.com/fixture/indexstore"

func IngestArchive(tx *indexstore.Tx) error { return tx.AppendKG() }
`,
	})
	if len(syms) != 0 {
		t.Errorf("rule fired on host-local (indexstore.Tx) writes, which are not sinks: %v", syms)
	}
}

// TestDecisionTrackedReachCoversTheRebuildRoot pins ingest.Rebuild as a root:
// child 7 deletes backfillFromArchives, so a tracked write newly reachable from
// Rebuild must still be caught.
func TestDecisionTrackedReachCoversTheRebuildRoot(t *testing.T) {
	syms := decisionReachSymbols(t, map[string]string{
		"storage": `package storage

func AddTriple() error { return nil }
`,
		"ingest": `package ingest

import "example.com/fixture/storage"

func Rebuild() error { return storage.AddTriple() }
`,
	})
	if !containsStr(syms, "ingest.Rebuild") {
		t.Errorf("rule did not fire on ingest.Rebuild -> storage.AddTriple; got %v", syms)
	}
}

// TestDecisionTrackedReachIndexTranscriptAllowCutsADirectReach exercises the
// capture.IndexTranscript allow-list entry on the DIRECT-CALL shape, as a proxy.
//
// 🔴 HONEST SCOPE: the real capture.IndexTranscript reaches AppendDrawers only
// through the field-receiver selector call idx.vault.AppendDrawers, which
// buildCallGraph cannot see (it records a selector edge only for a bare-ident
// receiver) — so on the real tree the entry is presently INERT and the rule
// fires neither with nor without it (see decisionTrackedWriterAllow's comment
// and sourceaudit-callgraph-blind-to-field-receiver-method-calls). This fixture
// therefore uses a package-qualified DIRECT call, storage.AppendDrawers(), the
// only shape the graph traverses today, to prove the cut works once the write is
// visible: it is the shape the field-receiver case will take after that blind
// spot is fixed. It does NOT claim the real pre-4 tree fires without the entry.
func TestDecisionTrackedReachIndexTranscriptAllowCutsADirectReach(t *testing.T) {
	src := map[string]string{
		"storage": `package storage

func AppendDrawers() error { return nil }
`,
		"capture": `package capture

import "example.com/fixture/storage"

// A DIRECT package-qualified call (proxy for the real field-receiver write,
// which the call graph cannot yet see).
func IndexTranscript() error { return storage.AppendDrawers() }
func WriteSession() error    { return IndexTranscript() }
`,
	}

	// With the cut in place (the real allow-list), WriteSession is quiet.
	if syms := decisionReachSymbols(t, src); len(syms) != 0 {
		t.Errorf("rule fired despite the capture.IndexTranscript allow-list cut: %v", syms)
	}

	// Remove the cut: WriteSession now reaches the tracked writer and fires.
	orig := decisionTrackedWriterAllow["capture.IndexTranscript"]
	delete(decisionTrackedWriterAllow, "capture.IndexTranscript")
	defer func() {
		if orig {
			decisionTrackedWriterAllow["capture.IndexTranscript"] = true
		}
	}()
	if syms := decisionReachSymbols(t, src); !containsStr(syms, "capture.WriteSession") {
		t.Errorf("removing the IndexTranscript allow-list entry did not make WriteSession fire; got %v", syms)
	}
}
