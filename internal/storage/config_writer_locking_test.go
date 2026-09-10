// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestWriteVaultProjectConfig_StatUnderLock is the acceptance test for
// WriteVaultProjectConfig's TOCTOU, which is character for character the
// CreateTask defect recorded in ADR-003.
//
// The "already exists" os.Stat used to run OUTSIDE the per-path vaultlock (the
// lock was taken inside v.lockedWrite, after the check). N concurrent callers
// therefore all passed the stat, all reported wrote=true, and all wrote the file
// — breaking the documented "refuses to overwrite an existing file, wrote=false
// if it already existed" contract that callers use to distinguish "I created
// this project" from "it was already there".
//
// The race detector cannot see this: a file-level overwrite is not a memory data
// race. The wrote= results are the only detector, so exactly one true is the
// assertion. N is large enough that the pre-fix code loses on every run.
func TestWriteVaultProjectConfig_StatUnderLock(t *testing.T) {
	const n = 32
	const proj = "contended"

	v := testVault(t)

	// Pre-create the project dir so every goroutine races on config.toml only,
	// not on MkdirAll.
	if err := os.MkdirAll(filepath.Join(v.Root, "Projects", proj), 0o755); err != nil {
		t.Fatal(err)
	}

	type result struct {
		path  string
		wrote bool
		err   error
	}
	results := make([]result, n)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			p, wrote, err := v.WriteVaultProjectConfig(proj)
			results[i] = result{p, wrote, err}
		})
	}
	close(start)
	wg.Wait()

	wrote := 0
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("caller %d: WriteVaultProjectConfig: %v", i, r.err)
		}
		if r.wrote {
			wrote++
		}
	}
	if wrote != 1 {
		t.Fatalf("WriteVaultProjectConfig reported wrote=true %d times across %d concurrent callers, want exactly 1 — the already-exists stat is racing the write", wrote, n)
	}

	cfgPath, err := v.ProjectConfigFile(proj)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != vaultProjectTemplate {
		t.Errorf("config.toml is not the embedded template verbatim (len %d, want %d)", len(got), len(vaultProjectTemplate))
	}
	if _, err := os.Stat(cfgPath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("config.toml.tmp survived (err=%v)", err)
	}
}
