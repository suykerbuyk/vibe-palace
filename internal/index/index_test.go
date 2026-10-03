// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package index_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// collidingDrawerContents searches for two distinct contents whose 32-bit
// storage.DrawerID collide under one wing. It is a birthday search, so it
// finds one within a few hundred thousand candidates; the bound only stops a
// broken DrawerID from looping forever.
func collidingDrawerContents(t *testing.T, wing string) (string, string) {
	t.Helper()
	seen := make(map[string]string, 1<<18)
	for i := 0; i < 1<<22; i++ {
		c := fmt.Sprintf("chunk content %d", i)
		id := storage.DrawerID(wing, c)
		if prev, ok := seen[id]; ok {
			return prev, c
		}
		seen[id] = c
	}
	t.Fatal("no DrawerID collision found")
	return "", ""
}

// Two contents that collide as legacy drawers get distinct chunk ids, and the
// id is exactly the first 128 bits of sha256 over the content bytes alone, so
// equal content under any wing or room is one id (ADR-014, "Chunk ids are wide
// content hashes").
func TestChunkIDIsWideAndHashesContentAlone(t *testing.T) {
	a, b := collidingDrawerContents(t, "alpha")
	if index.ChunkID(a) == index.ChunkID(b) {
		t.Fatalf("ChunkID collides where DrawerID does: %q and %q", a, b)
	}

	for _, content := range []string{a, b, "", "the same decision text"} {
		sum := sha256.Sum256([]byte(content))
		want := hex.EncodeToString(sum[:])[:32]
		if got := index.ChunkID(content); got != want {
			t.Fatalf("ChunkID(%q) = %q, want the first 128 bits of sha256(content) %q", content, got, want)
		}
	}

	// The legacy id differs across wings for the same content; the chunk id
	// cannot, because nothing but the content reaches it.
	const content = "the same decision text"
	if storage.DrawerID("alpha", content) == storage.DrawerID("beta", content) {
		t.Fatal("fixture: DrawerID should differ across wings")
	}
}

// internal/index is a leaf: every import is from the standard library, so it
// can never import internal/capture or internal/search, which depend on it.
func TestIndexPackageIsALeaf(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if strings.Contains(strings.SplitN(p, "/", 2)[0], ".") {
				t.Errorf("%s imports %q; internal/index must import only the standard library", name, p)
			}
		}
	}
}
