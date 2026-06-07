// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	mcpkg "github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// toolSurfaceGoldenPath is the repo location of the build-time golden
// tool-surface manifest, expressed relative to this test's package directory
// (cmd/vp). The golden lives under internal/mcp/ — next to the Registry it
// pins — even though it is generated from cmd/vp (the only package that can
// build the full tool set without an import cycle: tools imports mcp, so mcp
// cannot import tools to enumerate them).
const toolSurfaceGoldenPath = "../../internal/mcp/tool_surface.golden.json"

// toolSurfaceEntry is the per-tool golden record. It deliberately omits the
// tool Description and the full Schema: Description is prose that churns
// without changing the callable surface, and the full schema is reduced to a
// content hash (schema_sha256) that still catches any param-schema drift.
// Mutating is the Phase 2 flag — the build-time invariant that a tool's
// write-gate classification cannot change unnoticed.
type toolSurfaceEntry struct {
	Name         string `json:"name"`
	Mutating     bool   `json:"mutating"`
	SchemaSHA256 string `json:"schema_sha256"`
}

// toolSurfaceManifest is the golden document: the surface version plus the
// name-sorted tool entries.
type toolSurfaceManifest struct {
	SurfaceVersion int                `json:"surface_version"`
	Tools          []toolSurfaceEntry `json:"tools"`
}

// schemaSHA256 returns the hex sha256 of a tool's parameter JSON Schema in a
// canonical form: the raw schema is round-tripped through encoding/json so
// whitespace and object-key ordering are normalized (Go marshals map keys
// sorted), making the hash stable regardless of how the schema literal was
// formatted at the source. An empty/nil schema is normalized to the same
// permissive object schema the registry compiles in that case
// (compileSchema), so a tool that declares no schema hashes deterministically.
func schemaSHA256(raw json.RawMessage) string {
	canon := raw
	if len(canon) == 0 {
		canon = json.RawMessage(`{"type":"object"}`)
	}
	var v any
	if err := json.Unmarshal(canon, &v); err != nil {
		// Not valid JSON — fall back to hashing the raw bytes so the test
		// still pins *something* deterministic rather than panicking.
		sum := sha256.Sum256(canon)
		return hex.EncodeToString(sum[:])
	}
	b, err := json.Marshal(v)
	if err != nil {
		sum := sha256.Sum256(canon)
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// buildToolSurfaceManifest constructs the full MCP tool registry against a
// throwaway vault/engine (mirroring registeredToolCount in cmd_check.go — a
// nil embedder is harmless because tool constructors only stash the engine)
// and projects Registry.List() into the golden manifest shape, name-sorted
// for determinism. surface_version is read from internal/surface (never
// mutated here).
func buildToolSurfaceManifest() toolSurfaceManifest {
	v := storage.NewVault("")
	srv := mcpkg.NewServer(v)
	eng := search.NewEngine(nil, v, storage.Config{})
	tools.RegisterAll(srv.Registry(), vpctx.NewResolver(""), v, eng, storage.Config{})

	infos := srv.Registry().List()
	entries := make([]toolSurfaceEntry, 0, len(infos))
	for _, ti := range infos {
		entries = append(entries, toolSurfaceEntry{
			Name:         ti.Name,
			Mutating:     ti.Mutating,
			SchemaSHA256: schemaSHA256(ti.Schema),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	return toolSurfaceManifest{
		SurfaceVersion: surface.MCPSurfaceVersion,
		Tools:          entries,
	}
}

// marshalToolSurface renders a manifest as the canonical golden bytes:
// 2-space-indented JSON with a trailing newline (POSIX text file).
func marshalToolSurface(m toolSurfaceManifest) ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// diffToolSurface returns a human-readable, actionable description of how the
// live manifest differs from the golden, or "" if they are identical. It
// reports surface-version changes, added/removed tools, and per-tool mutating
// or schema-hash drift.
func diffToolSurface(live, golden toolSurfaceManifest) string {
	var b strings.Builder

	if live.SurfaceVersion != golden.SurfaceVersion {
		fmt.Fprintf(&b, "  surface_version: golden=%d live=%d\n",
			golden.SurfaceVersion, live.SurfaceVersion)
	}

	gByName := make(map[string]toolSurfaceEntry, len(golden.Tools))
	for _, e := range golden.Tools {
		gByName[e.Name] = e
	}
	lByName := make(map[string]toolSurfaceEntry, len(live.Tools))
	for _, e := range live.Tools {
		lByName[e.Name] = e
	}

	for _, e := range live.Tools {
		g, ok := gByName[e.Name]
		if !ok {
			fmt.Fprintf(&b, "  + added tool: %s (mutating=%v)\n", e.Name, e.Mutating)
			continue
		}
		if g.Mutating != e.Mutating {
			fmt.Fprintf(&b, "  ~ %s mutating: golden=%v live=%v\n", e.Name, g.Mutating, e.Mutating)
		}
		if g.SchemaSHA256 != e.SchemaSHA256 {
			fmt.Fprintf(&b, "  ~ %s schema_sha256: golden=%s live=%s\n", e.Name, g.SchemaSHA256, e.SchemaSHA256)
		}
	}
	for _, e := range golden.Tools {
		if _, ok := lByName[e.Name]; !ok {
			fmt.Fprintf(&b, "  - removed tool: %s\n", e.Name)
		}
	}

	return b.String()
}

// TestToolSurfaceGolden is the Phase 6 build-time golden invariant of the
// mcp-surface-handshake epic. It pins the full MCP tool surface
// ({surface_version, tools:[{name, mutating, schema_sha256}]}) so that any
// change to the callable surface — a tool added or removed, a tool's
// write-gate (Mutating) classification flipped, or a tool's parameter schema
// edited — fails the build until the golden is intentionally regenerated with
// `-update-golden`.
//
// Crucially, regenerating the golden does NOT bump MCPSurfaceVersion. The
// 1→2 (and onward) surface bump is a deliberate operator decision made
// separately; this test only forces a human to look at the diff and decide
// whether a bump is warranted. The drift message says exactly that.
//
//	go test ./cmd/vp -run TestToolSurfaceGolden -update-golden   # regenerate
//	go test ./cmd/vp -run TestToolSurfaceGolden                  # verify
func TestToolSurfaceGolden(t *testing.T) {
	live := buildToolSurfaceManifest()
	liveBytes, err := marshalToolSurface(live)
	if err != nil {
		t.Fatalf("marshal live manifest: %v", err)
	}

	if *updateGolden {
		if err := os.WriteFile(toolSurfaceGoldenPath, liveBytes, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote golden %s (surface=%d, %d tools)",
			toolSurfaceGoldenPath, live.SurfaceVersion, len(live.Tools))
		return
	}

	wantBytes, err := os.ReadFile(toolSurfaceGoldenPath)
	if err != nil {
		t.Fatalf("read golden (re-run with -update-golden to create): %v", err)
	}

	var golden toolSurfaceManifest
	if err := json.Unmarshal(wantBytes, &golden); err != nil {
		t.Fatalf("parse golden %s: %v", toolSurfaceGoldenPath, err)
	}

	if string(liveBytes) == string(wantBytes) {
		return
	}

	diff := diffToolSurface(live, golden)
	t.Errorf("MCP tool surface drifted from golden %s:\n%s\n"+
		"The callable MCP surface changed. If this change is intended:\n"+
		"  1. regenerate the golden:  go test ./cmd/vp -run TestToolSurfaceGolden -update-golden\n"+
		"  2. CONSIDER whether internal/surface.MCPSurfaceVersion must be bumped\n"+
		"     (a new/removed/altered vault-mutating tool can require a bump so older\n"+
		"      binaries gate against vaults written by this newer surface). Regenerating\n"+
		"      the golden does NOT bump the version — that is a deliberate operator decision.",
		toolSurfaceGoldenPath, diff)
}
