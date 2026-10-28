// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// emptyHostVault is an initialized project with NO host-local chunk store and
// NO tracked drawers: the state of a checkout whose index was never built on
// this host.
func emptyHostVault(t *testing.T) *storage.Vault {
	t.Helper()
	v := storage.NewVault(t.TempDir())
	testutil.InitProject(t, v.Root, "proj")
	return v
}

// TestEmptyHost_PalaceToolsReturnEmptyNotError pins that each palace
// collection/overview tool answers empty — never an error — on a host with no
// store and no tracked drawers (the "index unbuilt on this host" state). It is
// the per-tool counterpart of BuildGraphEmpty, which covers the graph alone.
//
// vp_traverse is excluded on purpose: its contract requires an existing start
// node, so on an empty palace it returns an explicit not-found error, not an
// empty answer (that error path is covered elsewhere). Its description pointer
// is still asserted below.
func TestEmptyHost_PalaceToolsReturnEmptyNotError(t *testing.T) {
	v := emptyHostVault(t)

	t.Run("palace_status", func(t *testing.T) {
		raw := callPalaceTool(t, PalaceStatusTool, v, map[string]string{"project": "proj"})
		var r struct {
			Stats struct {
				Wings   int `json:"wings"`
				Rooms   int `json:"rooms"`
				Drawers int `json:"drawers"`
				Tunnels int `json:"tunnels"`
			} `json:"stats"`
			PerWing []json.RawMessage `json:"per_wing"`
			Tunnels []json.RawMessage `json:"tunnels"`
		}
		mustUnmarshal(t, raw, &r)
		if r.Stats.Wings != 0 || r.Stats.Rooms != 0 || r.Stats.Drawers != 0 || r.Stats.Tunnels != 0 {
			t.Errorf("empty-host stats = %+v, want all zero", r.Stats)
		}
		if len(r.PerWing) != 0 || len(r.Tunnels) != 0 {
			t.Errorf("empty-host per_wing=%d tunnels=%d, want 0/0", len(r.PerWing), len(r.Tunnels))
		}
	})

	t.Run("list_wings", func(t *testing.T) {
		raw := callPalaceTool(t, ListWingsTool, v, map[string]string{"project": "proj"})
		if n := countJSONArray(t, raw); n != 0 {
			t.Errorf("empty-host list_wings returned %d wings, want 0", n)
		}
	})

	t.Run("list_rooms", func(t *testing.T) {
		raw := callPalaceTool(t, ListRoomsTool, v, map[string]string{"project": "proj", "wing": "proj"})
		if n := countJSONArray(t, raw); n != 0 {
			t.Errorf("empty-host list_rooms returned %d rooms, want 0", n)
		}
	})

	t.Run("find_tunnels", func(t *testing.T) {
		raw := callPalaceTool(t, FindTunnelsTool, v, map[string]string{"project": "proj"})
		if n := countJSONArray(t, raw); n != 0 {
			t.Errorf("empty-host find_tunnels returned %d tunnels, want 0", n)
		}
	})

	t.Run("palace_query", func(t *testing.T) {
		raw := callPalaceTool(t, PalaceQueryTool, v, map[string]string{"project": "proj"})
		var r struct {
			Drawers  []json.RawMessage `json:"drawers"`
			Complete bool              `json:"complete"`
		}
		mustUnmarshal(t, raw, &r)
		if len(r.Drawers) != 0 {
			t.Errorf("empty-host palace_query returned %d drawers, want 0", len(r.Drawers))
		}
		if !r.Complete {
			t.Error("empty-host palace_query must still report complete=true")
		}
	})
}

// TestEmptyHost_DescriptionsPointAtCoverage pins that every palace tool's
// description tells the operator an empty answer may mean the index is unbuilt
// on this host, and names the coverage instrument (vp_index_status — the tool
// the task's planning note called vp_index_coverage) to tell the two apart.
func TestEmptyHost_DescriptionsPointAtCoverage(t *testing.T) {
	v := emptyHostVault(t)
	tools := map[string]func(*storage.Vault) mcp.Tool{
		"vp_palace_status": PalaceStatusTool,
		"vp_list_wings":    ListWingsTool,
		"vp_list_rooms":    ListRoomsTool,
		"vp_traverse":      TraverseTool,
		"vp_find_tunnels":  FindTunnelsTool,
		"vp_palace_query":  PalaceQueryTool,
	}
	for name, ctor := range tools {
		tool := ctor(v)
		if tool.Name != name {
			t.Errorf("tool name = %q, want %q", tool.Name, name)
		}
		if !strings.Contains(tool.Description, "vp_index_status") {
			t.Errorf("%s description does not point at vp_index_status:\n%s", name, tool.Description)
		}
	}
}

func mustUnmarshal(t *testing.T, raw json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("unmarshal %s: %v", string(raw), err)
	}
}

func countJSONArray(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("expected a JSON array, got %s: %v", string(raw), err)
	}
	return len(arr)
}
