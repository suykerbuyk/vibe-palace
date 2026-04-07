// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"os"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func testVault(t *testing.T) *storage.Vault {
	t.Helper()
	return storage.NewVault(t.TempDir())
}

func TestCachePutGet(t *testing.T) {
	v := testVault(t)
	c := NewEmbedCache(v)

	vec := []float32{0.1, 0.2, 0.3, -0.5}
	if err := c.Put("proj", "drawer-1", vec); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := c.Get("proj", "drawer-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != len(vec) {
		t.Fatalf("got len %d, want %d", len(got), len(vec))
	}
	for i := range vec {
		if got[i] != vec[i] {
			t.Errorf("index %d: got %f, want %f", i, got[i], vec[i])
		}
	}
}

func TestCacheMiss(t *testing.T) {
	v := testVault(t)
	c := NewEmbedCache(v)

	got, err := c.Get("proj", "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for cache miss, got %v", got)
	}
}

func TestCacheBinaryFormat(t *testing.T) {
	v := testVault(t)
	c := NewEmbedCache(v)

	dims := 384
	vec := make([]float32, dims)
	for i := range vec {
		vec[i] = float32(i) / float32(dims)
	}

	if err := c.Put("proj", "drawer-1", vec); err != nil {
		t.Fatal(err)
	}

	path, _ := c.path("proj", "drawer-1")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	expectedSize := int64(dims * 4) // 384 * 4 = 1536 bytes
	if info.Size() != expectedSize {
		t.Errorf("file size = %d, want %d", info.Size(), expectedSize)
	}
}

func TestCacheOverwrite(t *testing.T) {
	v := testVault(t)
	c := NewEmbedCache(v)

	v1 := []float32{1.0, 2.0, 3.0}
	v2 := []float32{4.0, 5.0, 6.0}

	_ = c.Put("proj", "d1", v1)
	_ = c.Put("proj", "d1", v2)

	got, _ := c.Get("proj", "d1")
	for i := range v2 {
		if got[i] != v2[i] {
			t.Errorf("index %d: got %f, want %f (should be overwritten)", i, got[i], v2[i])
		}
	}
}
