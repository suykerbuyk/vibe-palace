// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// registryToolNames returns the names of every tool RegisterAll installs.
func registryToolNames(t *testing.T) ([]string, *mcp.Server) {
	t.Helper()
	vault := storage.NewVault(t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)
	srv := mcp.NewServer(vault)
	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := search.NewEngine(embedder.NewMock(384), vault, cfg)

	RegisterAll(srv.Registry(), resolver, vault, eng)

	var names []string
	for _, ti := range srv.Registry().List() {
		names = append(names, ti.Name)
	}
	sort.Strings(names)
	return names, srv
}

// TestReadOnlyServeToolNamesMatchRegistry is the completeness pin for the SERVE
// predicate — the second of the two pins, and it may not be dropped.
//
// It asserts the allow-list names only tools that actually exist. A stale entry
// (a renamed or deleted tool) is the failure mode that matters: it looks
// harmless, and it hides the fact that the replacement tool is now unclassified
// and being stripped, or worse that a name was recycled.
func TestReadOnlyServeToolNamesMatchRegistry(t *testing.T) {
	registered, _ := registryToolNames(t)
	inRegistry := make(map[string]bool, len(registered))
	for _, n := range registered {
		inRegistry[n] = true
	}

	for _, n := range ReadOnlyServeToolNames {
		if !inRegistry[n] {
			t.Errorf("ReadOnlyServeToolNames names %q, which no longer exists in the registry — "+
				"a stale allow-list entry hides whichever tool replaced it being stripped as unclassified", n)
		}
	}
}

// TestReadOnlyServeToolNamesNoDuplicates guards the allow-list itself, the same
// way TestMutatingToolNamesNoDuplicates guards the gate list.
func TestReadOnlyServeToolNamesNoDuplicates(t *testing.T) {
	seen := make(map[string]bool, len(ReadOnlyServeToolNames))
	for _, n := range ReadOnlyServeToolNames {
		if seen[n] {
			t.Errorf("duplicate entry in ReadOnlyServeToolNames: %q", n)
		}
		seen[n] = true
	}
}

// TestReadOnlyServeStripsAnUnclassifiedTool is the FAIL-CLOSED proof, and it is
// the reason this predicate is declared as an allow-list rather than reusing
// the mutating flag.
//
// It registers a tool that is in NEITHER list and carries Mutating: false — the
// exact shape of a tool somebody adds and forgets to classify, and the shape a
// "delete the tools flagged mutating" filter would happily serve. The filter
// must strip it anyway, because it is unrecognised.
func TestReadOnlyServeStripsAnUnclassifiedTool(t *testing.T) {
	registered, srv := registryToolNames(t)

	const unclassified = "vp_hypothetical_unclassified_writer"
	err := srv.Registry().Register(mcp.Tool{
		Name:        unclassified,
		Description: "a tool nobody classified",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return nil, nil
		},
		// Deliberately false: this is what "nobody thought about it" looks like,
		// and it is precisely the case a deny-list filter fails open on.
		Mutating: false,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	registered = append(registered, unclassified)

	strip := ToolsToStripForReadOnlyServe(registered)
	found := false
	for _, n := range strip {
		if n == unclassified {
			found = true
		}
	}
	if !found {
		t.Fatalf("the read-only serve filter did NOT strip %q — an unclassified tool reached a "+
			"surface an operator believes is read-only. The filter must fail CLOSED: served only "+
			"if affirmatively allow-listed. strip list was %v", unclassified, strip)
	}
}

// TestReadOnlyServeAgreesWithSurfaceGateToday pins the "no functional change"
// half of the split: the two predicates were one boolean, and today they still
// produce the same answer for every registered tool.
//
// 🔴 WHEN THIS TEST FAILS, THAT IS THE MOMENT THE TWO PREDICATES DIVERGE.
// It is expected to fail exactly once, deliberately, when the derivation work
// lands and computes Mutating: false for the git-channel tools (vp_vault_sync,
// vp_vault_tidy) that must nonetheless stay unserved. Updating it then is the
// visible, reviewed record of that divergence — which is the entire reason the
// two are declared separately. Do NOT update it to make an unexplained red
// build green; find out which predicate moved and why.
func TestReadOnlyServeAgreesWithSurfaceGateToday(t *testing.T) {
	registered, _ := registryToolNames(t)

	strip := ToolsToStripForReadOnlyServe(registered)
	want := append([]string(nil), MutatingToolNames...)
	sort.Strings(want)

	if len(strip) != len(want) {
		t.Fatalf("read-only serve strips %d tool(s), the surface gate lists %d\n strip: %v\n gate:  %v",
			len(strip), len(want), strip, want)
	}
	for i := range want {
		if strip[i] != want[i] {
			t.Errorf("divergence at %d: strip=%q gate=%q\n strip: %v\n gate:  %v",
				i, strip[i], want[i], strip, want)
		}
	}
}

// TestReadOnlyServePartitionsTheRegistry proves the two lists together account
// for every registered tool, so "unclassified" means genuinely new rather than
// merely missed by one of them today.
func TestReadOnlyServePartitionsTheRegistry(t *testing.T) {
	registered, _ := registryToolNames(t)

	allowed := make(map[string]bool, len(ReadOnlyServeToolNames))
	for _, n := range ReadOnlyServeToolNames {
		allowed[n] = true
	}
	gated := make(map[string]bool, len(MutatingToolNames))
	for _, n := range MutatingToolNames {
		gated[n] = true
	}

	for _, n := range registered {
		switch {
		case allowed[n] && gated[n]:
			t.Errorf("%q is BOTH allow-listed read-only and surface-gated — one of the two "+
				"declarations is wrong about what this tool does", n)
		case !allowed[n] && !gated[n]:
			t.Errorf("%q is in NEITHER declaration. It is stripped from read-only serve "+
				"(correct, fail-closed) but it is also UNGATED for the surface check — "+
				"classify it in one of the two lists", n)
		}
	}
}
