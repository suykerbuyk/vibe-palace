// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"sort"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestMutatingToolNamesMatchRegistry is the mechanical safeguard for the surface
// gate: it asserts that the set of registered tools whose Mutating flag is true
// is exactly MutatingToolNames. A new mutating tool that forgets the constructor
// flag — or one added to the flag but not the canonical list — fails here, so
// the gate set can never silently drift (the failure mode the hand-maintained
// pre-iter-93 plan suffered).
func TestMutatingToolNamesMatchRegistry(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)
	srv := mcp.NewServer(vault)
	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := search.NewEngine(embedder.NewMock(384), vault, cfg)

	RegisterAll(srv.Registry(), resolver, vault, eng)

	var registeredMutating []string
	for _, ti := range srv.Registry().List() {
		if ti.Mutating {
			registeredMutating = append(registeredMutating, ti.Name)
		}
	}

	want := append([]string(nil), MutatingToolNames...)
	sort.Strings(want)
	sort.Strings(registeredMutating)

	if len(registeredMutating) != len(want) {
		t.Fatalf("registry has %d mutating tools, MutatingToolNames has %d\n got:  %v\n want: %v",
			len(registeredMutating), len(want), registeredMutating, want)
	}
	for i := range want {
		if registeredMutating[i] != want[i] {
			t.Errorf("mutating set mismatch at %d: registry=%q want=%q\n got:  %v\n want: %v",
				i, registeredMutating[i], want[i], registeredMutating, want)
		}
	}
}

// TestMutatingToolNamesNoDuplicates guards the canonical list itself.
func TestMutatingToolNamesNoDuplicates(t *testing.T) {
	seen := make(map[string]bool, len(MutatingToolNames))
	for _, n := range MutatingToolNames {
		if seen[n] {
			t.Errorf("duplicate entry in MutatingToolNames: %q", n)
		}
		seen[n] = true
	}
}
