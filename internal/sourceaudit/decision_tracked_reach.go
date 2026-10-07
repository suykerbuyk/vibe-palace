// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import "go/ast"

// The call-graph rule for decision-chunks-in-the-host-local-store (XC16; ADR-014
// lines 1450-1469; 4b-S2).
//
// After the migration, note-time and ingest writers put their records in the
// HOST-LOCAL store (internal/indexstore's Tx methods), never in the tracked tree.
// This rule keeps them there: it reports a note-time, ingest or import ENTRY
// POINT (a "root") whose call graph reaches a TRACKED-TREE writer, which is a
// write that would land in git — exactly what the migration removed.
//
// It keys on the FUNCTION, not on a field value: whether a call writes the
// tracked tree is decided by which function it reaches, so the rule is a
// reachability question over buildCallGraph (the same graph ungatedVaultWriters
// uses), not a text match. indexstore.Tx methods (Append, ReplaceOwned,
// AppendKG, CommitArchive, Supersede, Rewrite, …) are NOT sinks: they write only
// the host-local store, and flagging them would fail the build on correct code.
//
// It runs only without -short (TestDecisionTrackedWriterReachIsClean), with the
// derived gate, because the gate `make test` skips is where its separate run
// lives.

// decisionTrackedWriterSinks are the tracked-tree writers (internal/storage).
// They are anchors only: internal/storage is out of this rule's reporting scope,
// so a sink is never itself a finding — only an in-scope root that reaches one.
// The extractor's tracked triple writer is AddTriple; the authored writer
// AddAuthoredTriple (vp_kg_add) is deliberately NOT here — it is
// authored-and-extracted-knowledge-graph-records' own writer and is not reached
// by any root of this rule.
var decisionTrackedWriterSinks = []string{
	"storage.AppendDrawers",
	"storage.AppendDrawer",
	"storage.AddEntities",
	"storage.AddTriple",
}

// decisionTrackedWriterRoots are the note-time, ingest and import entry points
// that must write the host-local store, never the tracked tree.
var decisionTrackedWriterRoots = []string{
	// The pending-archive ingester and its per-archive commit step.
	"ingest.IngestArchive",
	"ingest.Run",
	"ingest.RunHeld",
	// The explicit rebuild driver: it deletes backfillFromArchives, so its own
	// root must be clean once that function is gone.
	"ingest.Rebuild",
	// The decision-chunk writers.
	"capture.fileDecisionDrawers",
	"capture.WriteSession",
	"capture.DrainEnrichmentQueue",
	// The MCP handlers that drive them.
	"tools.refreshIndexHandler",   // vp_refresh_index
	"tools.captureSessionHandler", // vp_capture_session
	// The importer commands.
	"migrate.ImportMemPalace",
	"migrate.ImportVibeVault",
}

// decisionTrackedWriterAllow are functions PERMITTED to reach a tracked writer.
// They cut the reachability walk: a root whose only path to a sink runs through
// an allow-listed function is not a finding. Each entry is a reviewed, temporary
// exception that a named later child removes.
var decisionTrackedWriterAllow = map[string]bool{
	// capture still indexes transcripts into tracked drawers through
	// IndexTranscript until capture-and-backfill-write-host-local-index-only
	// (child 4) deletes it; whichever of that child and this one lands second
	// removes this entry.
	//
	// 🔴 PRESENTLY INERT, and honestly so: deleting this entry does not change
	// what the rule reports today. IndexTranscript's only tracked write is the
	// field-receiver selector call idx.vault.AppendDrawers
	// (internal/capture/indexer.go:145), and buildCallGraph records a selector
	// call's edge only when the receiver is a BARE ident — a field receiver
	// (idx.vault) is not — so that write is invisible to the graph with or
	// without this entry. The rule therefore cannot see WriteSession/the capture
	// handler/the refresh handler reach AppendDrawers through IndexTranscript
	// yet, and does not fire either way. The blind spot is a separate filed task,
	// sourceaudit-callgraph-blind-to-field-receiver-method-calls; do NOT work
	// around it here.
	//
	// The entry is KEPT because it becomes genuinely load-bearing the moment that
	// blind spot is fixed: the graph will then see the AppendDrawers write, those
	// roots will reach it through IndexTranscript, and only this cut keeps them
	// from firing — until child 4 removes IndexTranscript and this entry with it.
	"capture.IndexTranscript": true,
	// (`main.runAuditRooms` / `main.applyMoves` were removed by
	// palace-navigation-over-the-host-local-chunk-store, which repointed
	// `vp audit rooms --apply` at the host-local store: it relabels through
	// palace.Relabel → indexstore.Tx.Rewrite and no longer reaches a tracked
	// writer, so no cut is needed.)
}

// decisionTrackedReach reports each root whose call graph reaches a tracked-tree
// writer without passing through an allow-listed cut.
func decisionTrackedReach(files []file) []Finding {
	callees := buildCallGraph(files)

	// Reverse reachability to the sinks, to a fixpoint, with allow-listed
	// functions acting as cuts: they are never marked and never propagate, so a
	// path that reaches a sink only through one leaves its callers unmarked.
	reaches := map[string]bool{}
	for _, sink := range decisionTrackedWriterSinks {
		reaches[sink] = true
	}
	for changed := true; changed; {
		changed = false
		for key, cs := range callees {
			if reaches[key] || decisionTrackedWriterAllow[key] {
				continue
			}
			for c := range cs {
				if reaches[c] {
					reaches[key] = true
					changed = true
					break
				}
			}
		}
	}

	// A root's finding position: the first non-test declaration of a function
	// with that pkg.Name, so the error points at the offending entry point.
	pos := map[string]string{}
	for _, f := range files {
		if f.isTest {
			continue
		}
		pkg := f.ast.Name.Name
		for _, d := range f.ast.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name == nil {
				continue
			}
			key := pkg + "." + fd.Name.Name
			if _, seen := pos[key]; !seen {
				pos[key] = posOf(f, fd.Name.Pos())
			}
		}
	}

	var out []Finding
	for _, root := range decisionTrackedWriterRoots {
		if !reaches[root] {
			continue
		}
		out = append(out, Finding{
			Kind:   KindDecisionTrackedReach,
			Symbol: root,
			Pos:    pos[root],
			Detail: "a note-time, ingest or import entry point whose call graph reaches a tracked-tree " +
				"writer (storage.AppendDrawers/AppendDrawer/AddEntities/AddTriple). After the migration " +
				"these records belong in the host-local store (indexstore.Tx), not the git-tracked tree. " +
				"Route the write through the store, or — if the tracked write is a reviewed, temporary " +
				"exception — add the function on the path to decisionTrackedWriterAllow with a reason.",
		})
	}
	return out
}
