// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// producerNames returns the selector names the CLI (`vp check --check NAME`)
// accepts, straight from the shared registry — never a hand-written list.
func producerNames() []string {
	names := make([]string, 0, len(check.Producers))
	for name := range check.Producers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// schemaEnumNames returns the selector names vp_check ADVERTISES on the wire:
// the enum of its `checks` items, read out of the tool's own schema.
func schemaEnumNames(t *testing.T) []string {
	t.Helper()
	var schema struct {
		Properties struct {
			Checks struct {
				Items struct {
					Enum []string `json:"enum"`
				} `json:"items"`
			} `json:"checks"`
		} `json:"properties"`
	}
	raw := CheckTool(storage.NewVault("")).Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode vp_check schema: %v\n%s", err, raw)
	}
	names := append([]string(nil), schema.Properties.Checks.Items.Enum...)
	sort.Strings(names)
	return names
}

// TestCheckSelectorsAreOneRegistry is the point of the whole extraction: the
// names the CLI accepts and the names vp_check accepts are the SAME set,
// because they are the same map. Both sides here are enumerated from
// check.Producers / the tool's own schema — a hand-written expected-list would
// be the third copy of the concept this change exists to collapse, and would
// itself go stale.
//
// It fails if a producer is added to the registry but the tool's schema stops
// deriving from it, or if anyone filters a name out of the tool's set.
func TestCheckSelectorsAreOneRegistry(t *testing.T) {
	cli := producerNames()
	wire := schemaEnumNames(t)

	if strings.Join(cli, ",") != strings.Join(wire, ",") {
		t.Fatalf("selector sets diverged — the MCP tool is mirroring the registry, not sharing it\n CLI (check.Producers): %v\n vp_check schema enum:  %v", cli, wire)
	}

	// ProducerOrder must cover the map exactly, or the default-all path drops
	// or double-runs a check.
	order := append([]string(nil), check.ProducerOrder...)
	sort.Strings(order)
	if strings.Join(order, ",") != strings.Join(cli, ",") {
		t.Fatalf("ProducerOrder does not cover Producers exactly\n order:     %v\n producers: %v", order, cli)
	}

	// Every advertised name must actually dispatch.
	tool := CheckTool(storage.NewVault(t.TempDir()))
	for _, name := range wire {
		raw, _ := json.Marshal(map[string]any{"checks": []string{name}})
		if _, err := tool.Handler(context.Background(), raw); err != nil {
			t.Errorf("advertised selector %q does not dispatch: %v", name, err)
		}
	}
}

// TestSurfaceSelectorOverlapIsIntentional records a deliberate decision so it
// never reads as an oversight someone should "clean up".
//
// vp_check exposes the `surface` selector AND vp_surface_check remains
// registered. Both are true on purpose:
//
//   - Dropping `surface` from vp_check would make the tool's name set silently
//     diverge from the CLI's — the very drift the shared registry exists to
//     prevent, reappearing as a filter over the shared map.
//   - Deleting vp_surface_check is not on offer either: it is the embedder-free
//     surface preflight the surface gate itself depends on, called by the
//     restart/wrap/stage templates BEFORE vp_check's broader verdict is safe to
//     trust, and it returns the gate-specific binary_surface / vault_surface /
//     stamp_dir fields vp_check's uniform envelope does not carry.
func TestSurfaceSelectorOverlapIsIntentional(t *testing.T) {
	if _, ok := check.Producers["surface"]; !ok {
		t.Fatalf("the `surface` selector must stay in the shared registry")
	}

	vault := storage.NewVault(t.TempDir())
	srv := mcp.NewServer(vault)
	RegisterAll(srv.Registry(), vpctx.NewResolver(vault.Root), vault, nil)

	var haveCheck, haveSurface bool
	for _, tl := range srv.Registry().List() {
		switch tl.Name {
		case "vp_check":
			haveCheck = true
		case "vp_surface_check":
			haveSurface = true
		case "vp_check_resume_refs":
			t.Errorf("vp_check_resume_refs is subsumed by vp_check and must not be registered")
		}
	}
	if !haveCheck {
		t.Errorf("vp_check must be registered")
	}
	if !haveSurface {
		t.Errorf("vp_surface_check must stay registered: it is the preflight vp_check's own verdict depends on")
	}

	// The overlap is real, and the two tools agree on the verdict.
	out := callCheck(t, vault, map[string]any{"checks": []string{"surface"}})
	if len(out.Checks) != 1 {
		t.Fatalf("surface selector produced %d rows, want 1", len(out.Checks))
	}
	direct := SurfaceCheckTool(vault)
	res, err := direct.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("vp_surface_check: %v", err)
	}
	sc, ok := res.(SurfaceCheckResult)
	if !ok {
		t.Fatalf("result type = %T, want SurfaceCheckResult", res)
	}
	if sc.Status != out.Checks[0].Status {
		t.Errorf("overlapping verdicts disagree: vp_surface_check=%q vp_check[surface]=%q",
			sc.Status, out.Checks[0].Status)
	}
}
