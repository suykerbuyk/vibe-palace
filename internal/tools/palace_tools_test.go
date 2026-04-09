// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
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
