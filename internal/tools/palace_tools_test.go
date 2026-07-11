// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func testVaultWithPalace(t *testing.T) *storage.Vault {
	t.Helper()
	v := storage.NewVault(t.TempDir())

	// Seed: alpha has api, testing; beta has testing, data
	drawers := []struct {
		wing, room, content string
	}{
		{"alpha", "api", "api endpoint handler code"},
		{"alpha", "testing", "unit test for api"},
		{"beta", "testing", "integration test suite"},
		{"beta", "data", "database migration script"},
	}
	for _, d := range drawers {
		dr := storage.Drawer{
			Content:    d.content,
			Hall:       "facts",
			SourceType: "manual",
			FiledAt:    "2026-01-01T00:00:00Z",
		}
		if err := v.AppendDrawer("proj", d.wing, d.room, dr); err != nil {
			t.Fatalf("seed %s/%s: %v", d.wing, d.room, err)
		}
	}
	return v
}

func callPalaceTool(t *testing.T, tool func(*storage.Vault) mcp.Tool, vault *storage.Vault, params any) json.RawMessage {
	t.Helper()
	p, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool(vault).Handler(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPalaceStatusBasic(t *testing.T) {
	v := testVaultWithPalace(t)
	raw := callPalaceTool(t, PalaceStatusTool, v, map[string]string{"project": "proj"})

	var result struct {
		Stats struct {
			Wings     int `json:"wings"`
			Rooms     int `json:"rooms"`
			RoomNodes int `json:"room_nodes"`
			Drawers   int `json:"drawers"`
			Tunnels   int `json:"tunnels"`
		} `json:"stats"`
		PerWing []struct {
			Wing    string `json:"wing"`
			Rooms   int    `json:"rooms"`
			Drawers int    `json:"drawers"`
		} `json:"per_wing"`
		Tunnels []struct {
			Room string `json:"room"`
		} `json:"tunnels"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}

	if result.Stats.Wings != 2 {
		t.Errorf("Wings = %d, want 2", result.Stats.Wings)
	}
	if result.Stats.Rooms != 3 {
		t.Errorf("Rooms = %d, want 3", result.Stats.Rooms)
	}
	if result.Stats.Drawers != 4 {
		t.Errorf("Drawers = %d, want 4", result.Stats.Drawers)
	}
	if result.Stats.Tunnels != 1 {
		t.Errorf("Tunnels = %d, want 1", result.Stats.Tunnels)
	}
	if len(result.PerWing) != 2 {
		t.Errorf("PerWing count = %d, want 2", len(result.PerWing))
	}
}

// palaceKeyPresent reports whether the top-level JSON object in raw carries key
// k (present-but-empty/zeroed still counts as present).
func palaceKeyPresent(t *testing.T, raw []byte, k string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode raw keys: %v\n%s", err, raw)
	}
	_, ok := m[k]
	return ok
}

// TestPalaceStatusSections verifies the optional sections selector: it
// post-filters the result by ZEROING the unselected section's field
// (present-but-empty, never an absent key), leaves the default output
// byte-for-byte unchanged, and rejects unknown section names with a clean
// error. Presence/zero assertions decode into the real palaceStatusResult
// (not the anonymous mirror in TestPalaceStatusBasic) so a rename/omitempty on
// the production type would be caught.
func TestPalaceStatusSections(t *testing.T) {
	v := testVaultWithPalace(t)

	t.Run("default_carries_all_three_keys", func(t *testing.T) {
		raw := callPalaceTool(t, PalaceStatusTool, v, map[string]any{"project": "proj"})
		for _, k := range []string{"stats", "per_wing", "tunnels"} {
			if !palaceKeyPresent(t, raw, k) {
				t.Errorf("default output missing key %q: %s", k, raw)
			}
		}
	})

	t.Run("stats_only_zeroes_others", func(t *testing.T) {
		raw := callPalaceTool(t, PalaceStatusTool, v, map[string]any{
			"project":  "proj",
			"sections": []string{"stats"},
		})
		var got palaceStatusResult
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Stats.Wings != 2 || got.Stats.Drawers != 4 {
			t.Errorf("stats must be populated, got %+v", got.Stats)
		}
		if got.PerWing != nil {
			t.Errorf("per_wing must be nil, got %+v", got.PerWing)
		}
		if got.Tunnels != nil {
			t.Errorf("tunnels must be nil, got %+v", got.Tunnels)
		}
		if !palaceKeyPresent(t, raw, "per_wing") || !palaceKeyPresent(t, raw, "tunnels") {
			t.Errorf("zeroed sections must remain present as keys: %s", raw)
		}
	})

	t.Run("per_wing_only_zeroes_others", func(t *testing.T) {
		raw := callPalaceTool(t, PalaceStatusTool, v, map[string]any{
			"project":  "proj",
			"sections": []string{"per_wing"},
		})
		var got palaceStatusResult
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.PerWing) != 2 {
			t.Errorf("per_wing must be populated, got %+v", got.PerWing)
		}
		if got.Stats != (palace.PalaceStats{}) {
			t.Errorf("stats must be zeroed, got %+v", got.Stats)
		}
		if got.Tunnels != nil {
			t.Errorf("tunnels must be nil, got %+v", got.Tunnels)
		}
		if !palaceKeyPresent(t, raw, "stats") {
			t.Errorf("zeroed stats must remain present as a key: %s", raw)
		}
	})

	t.Run("tunnels_only_zeroes_others", func(t *testing.T) {
		raw := callPalaceTool(t, PalaceStatusTool, v, map[string]any{
			"project":  "proj",
			"sections": []string{"tunnels"},
		})
		var got palaceStatusResult
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Tunnels) != 1 {
			t.Errorf("tunnels must be populated, got %+v", got.Tunnels)
		}
		if got.Stats != (palace.PalaceStats{}) {
			t.Errorf("stats must be zeroed, got %+v", got.Stats)
		}
		if got.PerWing != nil {
			t.Errorf("per_wing must be nil, got %+v", got.PerWing)
		}
	})

	t.Run("all_sections_equal_default", func(t *testing.T) {
		rawAll := callPalaceTool(t, PalaceStatusTool, v, map[string]any{
			"project":  "proj",
			"sections": []string{"stats", "per_wing", "tunnels"},
		})
		rawDefault := callPalaceTool(t, PalaceStatusTool, v, map[string]any{"project": "proj"})
		if string(rawAll) != string(rawDefault) {
			t.Errorf("all-sections output must equal default output\n all=%s\n def=%s", rawAll, rawDefault)
		}
	})

	t.Run("unknown_section_errors", func(t *testing.T) {
		p, _ := json.Marshal(map[string]any{
			"project":  "proj",
			"sections": []string{"bogus"},
		})
		if _, err := PalaceStatusTool(v).Handler(context.Background(), p); err == nil {
			t.Fatal("expected error for unknown section, got nil")
		}
	})
}

func TestPalaceStatusEmpty(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	raw := callPalaceTool(t, PalaceStatusTool, v, map[string]string{"project": "empty"})

	var result struct {
		Stats struct {
			Wings   int `json:"wings"`
			Drawers int `json:"drawers"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Stats.Wings != 0 || result.Stats.Drawers != 0 {
		t.Errorf("empty palace should have zero stats, got wings=%d drawers=%d",
			result.Stats.Wings, result.Stats.Drawers)
	}
}

func TestPalaceStatusMissingProject(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	p, _ := json.Marshal(map[string]string{})
	_, err := PalaceStatusTool(v).Handler(context.Background(), p)
	if err == nil {
		t.Error("expected error for missing project")
	}
}

func TestListWingsBasic(t *testing.T) {
	v := testVaultWithPalace(t)
	raw := callPalaceTool(t, ListWingsTool, v, map[string]string{"project": "proj"})

	var result []struct {
		Wing    string `json:"wing"`
		Rooms   int    `json:"rooms"`
		Drawers int    `json:"drawers"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatalf("got %d wings, want 2", len(result))
	}
	// Should be alphabetically sorted.
	if result[0].Wing != "alpha" || result[1].Wing != "beta" {
		t.Errorf("wings = [%s, %s], want [alpha, beta]", result[0].Wing, result[1].Wing)
	}
	if result[0].Rooms != 2 {
		t.Errorf("alpha rooms = %d, want 2", result[0].Rooms)
	}
}

func TestListRoomsBasic(t *testing.T) {
	v := testVaultWithPalace(t)
	raw := callPalaceTool(t, ListRoomsTool, v, map[string]any{
		"project": "proj",
		"wing":    "alpha",
	})

	var result []struct {
		Room             string         `json:"room"`
		Drawers          int            `json:"drawers"`
		HallDistribution map[string]int `json:"hall_distribution"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatalf("got %d rooms, want 2", len(result))
	}
	// Alphabetically sorted.
	if result[0].Room != "api" {
		t.Errorf("first room = %q, want api", result[0].Room)
	}
	if result[0].HallDistribution["facts"] != 1 {
		t.Errorf("api facts count = %d, want 1", result[0].HallDistribution["facts"])
	}
}

func TestListRoomsMissingWing(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	p, _ := json.Marshal(map[string]string{"project": "proj"})
	_, err := ListRoomsTool(v).Handler(context.Background(), p)
	if err == nil {
		t.Error("expected error for missing wing")
	}
}

func TestListRoomsEmptyWing(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	raw := callPalaceTool(t, ListRoomsTool, v, map[string]any{
		"project": "proj",
		"wing":    "nonexistent",
	})

	var result []struct{}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d", len(result))
	}
}

func TestTraverseBasic(t *testing.T) {
	v := testVaultWithPalace(t)
	raw := callPalaceTool(t, TraverseTool, v, map[string]any{
		"project": "proj",
		"start":   "alpha/api",
	})

	var result []struct {
		Wing    string `json:"wing"`
		Room    string `json:"room"`
		HopDist int    `json:"hop_distance"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 4 {
		t.Fatalf("got %d nodes, want 4", len(result))
	}
	if result[0].Wing != "alpha" || result[0].Room != "api" || result[0].HopDist != 0 {
		t.Errorf("start node = %+v, want alpha/api at hop 0", result[0])
	}
}

func TestTraverseMaxHopsBound(t *testing.T) {
	v := testVaultWithPalace(t)
	raw := callPalaceTool(t, TraverseTool, v, map[string]any{
		"project":  "proj",
		"start":    "alpha/api",
		"max_hops": 1,
	})

	var result []struct {
		HopDist int `json:"hop_distance"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	for _, r := range result {
		if r.HopDist > 1 {
			t.Errorf("got hop_distance %d, max_hops was 1", r.HopDist)
		}
	}
}

func TestTraverseInvalidStart(t *testing.T) {
	v := testVaultWithPalace(t)
	p, _ := json.Marshal(map[string]any{
		"project": "proj",
		"start":   "nonexistent/room",
	})
	_, err := TraverseTool(v).Handler(context.Background(), p)
	if err == nil {
		t.Error("expected error for invalid start node")
	}
}

func TestFindTunnelsBasic(t *testing.T) {
	v := testVaultWithPalace(t)
	raw := callPalaceTool(t, FindTunnelsTool, v, map[string]string{"project": "proj"})

	var result []struct {
		Room  string   `json:"room"`
		Wings []string `json:"wings"`
		Count int      `json:"total_drawers"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d tunnels, want 1", len(result))
	}
	if result[0].Room != "testing" {
		t.Errorf("tunnel room = %q, want testing", result[0].Room)
	}
	if len(result[0].Wings) != 2 {
		t.Errorf("tunnel wings = %v, want 2", result[0].Wings)
	}
}

func TestFindTunnelsNoTunnels(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	// Single wing — no tunnels possible.
	d := storage.Drawer{Content: "c", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z"}
	if err := v.AppendDrawer("proj", "only-wing", "room-1", d); err != nil {
		t.Fatal(err)
	}

	raw := callPalaceTool(t, FindTunnelsTool, v, map[string]string{"project": "proj"})

	var result []struct{}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Errorf("expected no tunnels, got %d", len(result))
	}
}
