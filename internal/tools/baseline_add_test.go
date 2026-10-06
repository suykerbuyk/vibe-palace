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

// The baseline add for incoming archives (ADR-014 decision 2). It is a lifecycle
// step shared by the receiver-run copy; its only surviving caller is
// vp vault copy's apply.

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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resume.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
