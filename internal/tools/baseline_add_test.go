// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The baseline add for incoming archives (task
// split-and-merge-exclude-derived-palace-paths, Scope 5; ADR-014 decision 2).

var incomingSHAs = []string{
	strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64),
}

// archiveManifests returns three transcript archive manifests for slug.
func archiveManifests(slug string) map[string]string {
	files := map[string]string{}
	for i, sha := range incomingSHAs {
		files[fmt.Sprintf("Projects/%s/transcripts/2026-10-0%d-s.manifest.json", slug, i+1)] =
			fmt.Sprintf(`{"schema_version":1,"source_sha256":%q}`, sha)
	}
	return files
}

// seedLedger gives this host a ledger for slug in vault, with an empty
// baseline set. The project must exist to be locked, so its Projects/ tree is
// created for the seed and removed again: the store under palace/.local/index/
// is host-local and outlives it, as a ledger for a slug that once lived here
// does.
func seedLedger(t *testing.T, vaultRoot, slug string) {
	t.Helper()
	dir := filepath.Join(vaultRoot, "Projects", slug)
	writeSplitFile(t, vaultRoot, "Projects/"+slug+"/resume.md", "x\n")
	tx, err := indexstore.Lock(context.Background(), storage.NewVault(vaultRoot), slug, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.EnsureLedger(nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
}

// baselineHolds reports which incoming archives slug's ledger in vault holds
// in its baseline set.
func baselineHolds(t *testing.T, vaultRoot, slug string) []string {
	t.Helper()
	tx, err := indexstore.Lock(context.Background(), storage.NewVault(vaultRoot), slug, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Release() }()
	l, err := tx.Ledger()
	if err != nil {
		t.Fatal(err)
	}
	var in []string
	for _, sha := range incomingSHAs {
		if l.InBaseline(sha) {
			in = append(in, sha)
		}
	}
	return in
}

// spyBaselineLock counts the index locks the baseline add takes.
func spyBaselineLock(t *testing.T) *int {
	t.Helper()
	n := 0
	prev := incomingBaselineLock
	incomingBaselineLock = func(ctx context.Context, v *storage.Vault, p string, d time.Duration) (*indexstore.Tx, error) {
		n++
		return prev(ctx, v, p, d)
	}
	t.Cleanup(func() { incomingBaselineLock = prev })
	return &n
}

// mergeWithArchives merges alpha, carrying three archives, from a migrated
// source into dest.
func mergeWithArchives(t *testing.T, dest string) *vaultMergeApplyResult {
	t.Helper()
	src := mergeSourceVault(t, "alpha")
	for rel, body := range archiveManifests("alpha") {
		writeSplitFile(t, src, rel, body)
	}
	p := mergePlanned(t, dest, src, "alpha")
	res, err := vaultMergeApply(storage.NewVault(dest), p)
	if err != nil {
		t.Fatalf("merge apply: %v", err)
	}
	return res
}

// A merge adds exactly the incoming archives to an existing ledger's
// baseline set, through one commit-lock acquisition. Mutant: no add, which
// leaves the incoming history pending for automatic ingest.
func TestBaselineAdd_MergeAddsTheIncomingArchives(t *testing.T) {
	dest := mergeDestVault(t, "gamma")
	seedLedger(t, dest, "alpha")
	locks := spyBaselineLock(t)
	res := mergeWithArchives(t, dest)
	if len(res.BaselineWarnings) > 0 {
		t.Fatalf("warnings: %v", res.BaselineWarnings)
	}
	if got := baselineHolds(t, dest, "alpha"); len(got) != len(incomingSHAs) {
		t.Errorf("baseline holds %d of the %d incoming archives", len(got), len(incomingSHAs))
	}
	if *locks != 1 {
		t.Errorf("the add took %d index locks for one project, want 1 (its commit lock)", *locks)
	}
}

// With no ledger for the slug, nothing is written under the slug's index
// store. Mutant: an add that creates a ledger (contrary to ADR lines 144-145).
func TestBaselineAdd_NoLedgerWritesNothing(t *testing.T) {
	dest := mergeDestVault(t, "gamma")
	mergeWithArchives(t, dest)
	if _, err := os.Stat(filepath.Join(dest, "palace", ".local", "index", "alpha")); !os.IsNotExist(err) {
		t.Errorf("the add created palace/.local/index/alpha (stat err %v)", err)
	}
}

// The add never fails the command: with the project's commit lock held and a
// zero timeout, the merge succeeds, warns naming the project and `vp index
// rebuild`, and the set is unchanged. Mutant: a merge that fails over a
// host-local write.
func TestBaselineAdd_NeverFailsTheCommand(t *testing.T) {
	dest := mergeDestVault(t, "gamma")
	seedLedger(t, dest, "alpha")
	prevTimeout := incomingBaselineTimeout
	incomingBaselineTimeout = 0
	t.Cleanup(func() { incomingBaselineTimeout = prevTimeout })
	// Hold alpha's commit lock across the merge. Lock checks that the project
	// exists, so take it once the merge has put alpha's tree in place: from a
	// lock seam that runs just before the add's own attempt.
	var held *indexstore.Tx
	prev := incomingBaselineLock
	incomingBaselineLock = func(ctx context.Context, v *storage.Vault, p string, d time.Duration) (*indexstore.Tx, error) {
		h, err := prev(ctx, v, p, time.Second)
		if err != nil {
			t.Fatalf("hold the lock: %v", err)
		}
		held = h
		return prev(ctx, v, p, d)
	}
	t.Cleanup(func() { incomingBaselineLock = prev })
	res := mergeWithArchives(t, dest)
	if held != nil {
		_ = held.Release()
	}
	if len(res.BaselineWarnings) != 1 || !strings.Contains(res.BaselineWarnings[0], "alpha") ||
		!strings.Contains(res.BaselineWarnings[0], "vp index rebuild alpha") {
		t.Fatalf("warnings = %q, want one naming alpha and `vp index rebuild alpha`", res.BaselineWarnings)
	}
	incomingBaselineLock = prev
	if got := baselineHolds(t, dest, "alpha"); len(got) != 0 {
		t.Errorf("the set changed though the add failed: %v", got)
	}
}

// Copy adds the incoming archives after its publish; a refused copy adds
// nothing. Mutants: no add on copy; an add placed before the publish.
func TestBaselineAdd_CopyAddsAfterThePublishOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	srcFiles := archiveManifests("p")
	srcFiles["Projects/p/resume.md"] = "p\n"
	_, srcBare := vcRepo(t, srcFiles)
	v, _ := vcRepo(t, map[string]string{"Projects/resident/resume.md": "r\n"})
	seedLedger(t, v, "p")
	tool := VaultCopyTool(storage.NewVault(v))

	// A refused copy (a wrong --expect) adds nothing: the ledger's bytes are
	// unchanged.
	ledger := filepath.Join(v, "palace", ".local", "index", "p", "ledger.jsonl")
	before, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatalf("fixture: no ledger at %s: %v", ledger, err)
	}
	bad := fmt.Sprintf(`{"action":"apply","slugs":["p"],"from":%q,"expect":%q}`, "file://"+srcBare, strings.Repeat("0", 64))
	if _, err := tool.Handler(context.Background(), json.RawMessage(bad)); err == nil {
		t.Fatal("a copy with a wrong digest must refuse")
	}
	if after, _ := os.ReadFile(ledger); string(after) != string(before) {
		t.Errorf("a refused copy changed the ledger:\n%s", after)
	}

	ok := fmt.Sprintf(`{"action":"apply","slugs":["p"],"from":%q}`, "file://"+srcBare)
	out, err := tool.Handler(context.Background(), json.RawMessage(ok))
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if res := out.(*vaultCopyResult); len(res.BaselineWarnings) > 0 {
		t.Fatalf("warnings: %v", res.BaselineWarnings)
	}
	if got := baselineHolds(t, v, "p"); len(got) != len(incomingSHAs) {
		t.Errorf("baseline holds %d of the %d incoming archives after the copy", len(got), len(incomingSHAs))
	}
}
