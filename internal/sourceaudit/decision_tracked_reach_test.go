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

// capture.IndexTranscript used to carry a (presently inert) allow-list entry,
// with a companion test proving the entry changed nothing on the real tree.
// capture-and-backfill-write-host-local-index-only deleted IndexTranscript and
// the entry with it, so that test is gone; the allow-list-cut MECHANISM it
// stood behind is still covered by TestDecisionTrackedReachAllowListCutsADirectReach.

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

// TestDecisionTrackedReachAllowListCutsADirectReach exercises the allow-list
// cut on the DIRECT-CALL shape, with a SYNTHETIC cut it installs itself, so the
// test proves the mechanism without depending on any particular real entry
// surviving in decisionTrackedWriterAllow (the only real entries today belong to
// a different, concurrently-evolving child).
//
// 🔴 HONEST SCOPE: buildCallGraph records a selector edge only for a bare-ident
// receiver, so a real field-receiver write (e.g. idx.vault.AppendDrawers) is
// invisible to it today (sourceaudit-callgraph-blind-to-field-receiver-method-
// calls). This fixture uses a package-qualified DIRECT call,
// storage.AppendDrawers(), the only shape the graph traverses, so it proves the
// cut works on a reach the graph can actually see; capture.WriteSession is a
// real decisionTrackedWriterRoot, which is why the fixture uses that name.
func TestDecisionTrackedReachAllowListCutsADirectReach(t *testing.T) {
	src := map[string]string{
		"storage": `package storage

func AppendDrawers() error { return nil }
`,
		"capture": `package capture

import "example.com/fixture/storage"

// A DIRECT package-qualified call (the only shape the call graph traverses).
func filesDrawers() error { return storage.AppendDrawers() }
func WriteSession() error { return filesDrawers() }
`,
	}

	// No cut: WriteSession reaches the tracked writer and fires.
	if syms := decisionReachSymbols(t, src); !containsStr(syms, "capture.WriteSession") {
		t.Errorf("expected capture.WriteSession to fire without a cut; got %v", syms)
	}

	// Install a synthetic cut on the writer-reaching function; WriteSession goes quiet.
	const cut = "capture.filesDrawers"
	decisionTrackedWriterAllow[cut] = true
	defer delete(decisionTrackedWriterAllow, cut)
	if syms := decisionReachSymbols(t, src); len(syms) != 0 {
		t.Errorf("rule fired despite the allow-list cut on %s: %v", cut, syms)
	}
}
