// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// fpVault holds three drawers in project "proj".
func fpVault(t *testing.T) *storage.Vault {
	t.Helper()
	v := testVault(t)
	for _, c := range []string{"alpha drawer text", "beta drawer text", "gamma drawer text"} {
		addDrawer(t, v, "proj", "proj", "room-1", c, "facts")
	}
	return v
}

// fpRebuild runs one fresh engine with model m over v and returns its stats.
func fpRebuild(t *testing.T, v *storage.Vault, model string) RebuildStats {
	t.Helper()
	eng := NewEngine(newCountingEmbedder(384), v, storage.Config{SearchDefaultLimit: 10, EmbedderModel: model})
	t.Cleanup(func() { eng.Close() })
	stats, err := eng.Rebuild(context.Background(), "proj")
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	return stats
}

func fpCacheDir(v *storage.Vault) string {
	return filepath.Join(v.Root, "palace", ".local", "embed-cache", "proj")
}

func fpSidecar(t *testing.T, v *storage.Vault) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fpCacheDir(v), storage.EmbedCacheFingerprintFile))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// fpVecs hashes every *.vec in the project cache, keyed by name.
func fpVecs(t *testing.T, v *storage.Vault) map[string]string {
	t.Helper()
	out := map[string]string{}
	ents, err := os.ReadDir(fpCacheDir(v))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".vec") && e.Type().IsRegular() {
			b, _ := os.ReadFile(filepath.Join(fpCacheDir(v), e.Name()))
			s := sha256.Sum256(b)
			out[e.Name()] = hex.EncodeToString(s[:])
		}
	}
	return out
}

// T1: vectors written under fingerprint A are re-embedded once when the
// embedder reports B, and never again after that. The invalidation removes
// only *.vec files: every other entry in the directory survives.
func TestEmbedCache_FingerprintMismatchReembedsOnce(t *testing.T) {
	v := fpVault(t)
	if st := fpRebuild(t, v, "model-a"); st.Embedded != 3 {
		t.Fatalf("first build embedded %d, want 3", st.Embedded)
	}
	if got, want := fpSidecar(t, v), embedder.Fingerprint("model-a", 0); got != want {
		t.Fatalf("sidecar %q, want %q", got, want)
	}
	dir := fpCacheDir(v)
	others := map[string]string{"notes.txt": "operator note\n", "x.vec.tmp": "partial\n"}
	for name, body := range others {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub.vec"), 0o755); err != nil {
		t.Fatal(err)
	}

	st := fpRebuild(t, v, "model-b")
	if st.Embedded != 3 || st.CacheHits != 0 {
		t.Fatalf("after a regime change stats = %+v, want all 3 re-embedded and no stale hit", st)
	}
	if got, want := fpSidecar(t, v), embedder.Fingerprint("model-b", 0); got != want {
		t.Errorf("sidecar %q, want %q", got, want)
	}
	for name, body := range others {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != body {
			t.Errorf("non-vector file %s must survive invalidation: %q, %v", name, got, err)
		}
	}
	if st, err := os.Stat(filepath.Join(dir, "sub.vec")); err != nil || !st.IsDir() {
		t.Errorf("a directory named *.vec must survive invalidation: %v", err)
	}

	if st := fpRebuild(t, v, "model-b"); st.Embedded != 0 || st.CacheHits != 3 {
		t.Errorf("a second engine under B: stats %+v, want 3 hits and no re-embed", st)
	}
}

// T2: a matching fingerprint is a hit: no re-embed, vectors byte-identical.
func TestEmbedCache_FingerprintMatchIsAHit(t *testing.T) {
	v := fpVault(t)
	fpRebuild(t, v, "model-a")
	before := fpVecs(t, v)
	if st := fpRebuild(t, v, "model-a"); st.Embedded != 0 || st.CacheHits != 3 {
		t.Fatalf("same fingerprint: stats %+v, want 3 hits", st)
	}
	after := fpVecs(t, v)
	if len(after) != 3 {
		t.Fatalf("vectors %v", after)
	}
	for k, h := range before {
		if after[k] != h {
			t.Errorf("vector %s changed under a matching fingerprint", k)
		}
	}
}

// T3: a directory with no sidecar is the pre-fingerprint regime: invalidated
// once, then fingerprinted and served as hits.
func TestEmbedCache_UnfingerprintedCacheInvalidatesOnce(t *testing.T) {
	v := fpVault(t)
	fpRebuild(t, v, "model-a")
	if err := os.Remove(filepath.Join(fpCacheDir(v), storage.EmbedCacheFingerprintFile)); err != nil {
		t.Fatal(err)
	}
	if st := fpRebuild(t, v, "model-a"); st.Embedded != 3 || st.CacheHits != 0 {
		t.Fatalf("legacy cache: stats %+v, want all 3 re-embedded once", st)
	}
	if fpSidecar(t, v) != embedder.Fingerprint("model-a", 0) {
		t.Error("the sidecar must be written after the one-time invalidation")
	}
	if st := fpRebuild(t, v, "model-a"); st.Embedded != 0 || st.CacheHits != 3 {
		t.Errorf("after the one-time invalidation: stats %+v, want 3 hits", st)
	}
}

// T4: an engine with NO embedder never validates the regime, so it never
// deletes a vector or writes a sidecar. This is exactly the engine vp check
// builds in registeredToolCount (cmd/vp/cmd_check.go:
// search.NewEngine(nil, v, storage.Config{})), driven here through the cache
// operations a tool would reach.
func TestEmbedCache_NilEmbedderEngineNeverInvalidates_CmdCheckRegisteredToolCount(t *testing.T) {
	v := fpVault(t)
	fpRebuild(t, v, "model-a")
	side := filepath.Join(fpCacheDir(v), storage.EmbedCacheFingerprintFile)
	// A foreign regime AND, in the second pass, no sidecar at all: neither may
	// provoke a nil-embedder engine into removing anything.
	for _, sidecar := range []string{"vp-embed behaviour=0 model=other max_seq_len=0\n", ""} {
		if sidecar == "" {
			_ = os.Remove(side)
		} else if err := os.WriteFile(side, []byte(sidecar), 0o644); err != nil {
			t.Fatal(err)
		}
		before := fpVecs(t, v)

		eng := NewEngine(nil, v, storage.Config{})
		if eng.cache.fingerprint != "" {
			t.Fatalf("a nil-embedder engine carries fingerprint %q, want none", eng.cache.fingerprint)
		}
		ids := make([]string, 0, len(before))
		for k := range before {
			ids = append(ids, strings.TrimSuffix(k, ".vec"))
		}
		sort.Strings(ids)
		for _, id := range ids {
			if vec, err := eng.cache.Get("proj", id); err != nil || vec == nil {
				t.Errorf("Get %s through a nil-embedder engine: %v, %v", id, vec, err)
			}
		}
		if _, err := eng.cache.dir("proj"); err != nil {
			t.Fatal(err)
		}
		// Not closed: vp check never closes this engine, and Close would reach
		// the nil embedder.

		after := fpVecs(t, v)
		if len(after) != len(before) {
			t.Errorf("sidecar %q: vectors %d -> %d", sidecar, len(before), len(after))
		}
		got, err := os.ReadFile(side)
		switch {
		case sidecar == "" && !os.IsNotExist(err):
			t.Errorf("a nil-embedder engine wrote a sidecar: %q", got)
		case sidecar != "" && string(got) != sidecar:
			t.Errorf("a nil-embedder engine rewrote the sidecar: %q", got)
		}
	}
}
