// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace_test

import (
	"fmt"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func seedVault(t *testing.T, v *storage.Vault, project string, layout map[string][]string) {
	t.Helper()
	seq := 0
	for wing, rooms := range layout {
		for _, room := range rooms {
			seq++
			d := storage.Drawer{
				Content:    fmt.Sprintf("content-%d", seq),
				Hall:       "facts",
				SourceType: "manual",
				FiledAt:    "2026-01-01T00:00:00Z",
			}
			if err := v.AppendDrawer(project, wing, room, d); err != nil {
				t.Fatalf("AppendDrawer(%s/%s): %v", wing, room, err)
			}
		}
	}
}

func TestBuildGraphEmpty(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	g, err := palace.BuildGraph(v, "proj")
	if err != nil {
		t.Fatal(err)
	}
	s := g.Stats()
	if s.Wings != 0 || s.Rooms != 0 || s.Drawers != 0 {
		t.Errorf("empty graph stats = %+v, want all zeros", s)
	}
	if len(g.FindTunnels()) != 0 {
		t.Error("empty graph should have no tunnels")
	}
}

func TestBuildGraphSingleWing(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	seedVault(t, v, "proj", map[string][]string{
		"alpha": {"api", "testing", "config"},
	})

	g, err := palace.BuildGraph(v, "proj")
	if err != nil {
		t.Fatal(err)
	}
	s := g.Stats()
	if s.Wings != 1 {
		t.Errorf("Wings = %d, want 1", s.Wings)
	}
	if s.Rooms != 3 {
		t.Errorf("Rooms = %d, want 3", s.Rooms)
	}
	if s.RoomNodes != 3 {
		t.Errorf("RoomNodes = %d, want 3", s.RoomNodes)
	}
	if s.Drawers != 3 {
		t.Errorf("Drawers = %d, want 3", s.Drawers)
	}
	// 3 rooms in one wing → 3 edges (complete graph of 3)
	if s.Edges != 3 {
		t.Errorf("Edges = %d, want 3", s.Edges)
	}
	if s.Tunnels != 0 {
		t.Errorf("Tunnels = %d, want 0 (single wing)", s.Tunnels)
	}
}

func TestBuildGraphMultiWingWithTunnels(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	seedVault(t, v, "proj", map[string][]string{
		"alpha": {"api", "testing"},
		"beta":  {"testing", "data"},
	})

	g, err := palace.BuildGraph(v, "proj")
	if err != nil {
		t.Fatal(err)
	}
	s := g.Stats()
	if s.Wings != 2 {
		t.Errorf("Wings = %d, want 2", s.Wings)
	}
	if s.Rooms != 3 {
		t.Errorf("unique Rooms = %d, want 3 (api, testing, data)", s.Rooms)
	}
	if s.RoomNodes != 4 {
		t.Errorf("RoomNodes = %d, want 4", s.RoomNodes)
	}
	if s.Tunnels != 1 {
		t.Errorf("Tunnels = %d, want 1 (testing)", s.Tunnels)
	}

	tunnels := g.FindTunnels()
	if len(tunnels) != 1 {
		t.Fatalf("got %d tunnels, want 1", len(tunnels))
	}
	if tunnels[0].Room != "testing" {
		t.Errorf("tunnel room = %q, want testing", tunnels[0].Room)
	}
	if len(tunnels[0].Wings) != 2 {
		t.Errorf("tunnel wings = %v, want 2 wings", tunnels[0].Wings)
	}
}

func TestTraverseBFS(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	// Linear chain via tunnel: alpha/api → alpha/testing → beta/testing → beta/data
	seedVault(t, v, "proj", map[string][]string{
		"alpha": {"api", "testing"},
		"beta":  {"testing", "data"},
	})

	g, err := palace.BuildGraph(v, "proj")
	if err != nil {
		t.Fatal(err)
	}

	nodes, err := g.Traverse("alpha/api", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 4 {
		t.Fatalf("Traverse returned %d nodes, want 4", len(nodes))
	}

	// Hop 0: alpha/api
	if nodes[0].Wing != "alpha" || nodes[0].Room != "api" || nodes[0].HopDist != 0 {
		t.Errorf("node[0] = %+v, want alpha/api at hop 0", nodes[0])
	}
	// Hop 1: alpha/testing (intra-wing)
	if nodes[1].Wing != "alpha" || nodes[1].Room != "testing" || nodes[1].HopDist != 1 {
		t.Errorf("node[1] = %+v, want alpha/testing at hop 1", nodes[1])
	}
	// Hop 2: beta/testing (via tunnel from alpha/testing)
	if nodes[2].Wing != "beta" || nodes[2].Room != "testing" || nodes[2].HopDist != 2 {
		t.Errorf("node[2] = %+v, want beta/testing at hop 2", nodes[2])
	}
	// Hop 3: beta/data (via intra-wing from beta/testing)
	if nodes[3].Wing != "beta" || nodes[3].Room != "data" || nodes[3].HopDist != 3 {
		t.Errorf("node[3] = %+v, want beta/data at hop 3", nodes[3])
	}
}

func TestTraverseMaxHops(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	seedVault(t, v, "proj", map[string][]string{
		"alpha": {"api", "testing"},
		"beta":  {"testing", "data"},
	})

	g, err := palace.BuildGraph(v, "proj")
	if err != nil {
		t.Fatal(err)
	}

	nodes, err := g.Traverse("alpha/api", 1)
	if err != nil {
		t.Fatal(err)
	}
	// Should only include hop 0 (alpha/api) and hop 1 (alpha/testing)
	if len(nodes) != 2 {
		t.Fatalf("Traverse(maxHops=1) returned %d nodes, want 2", len(nodes))
	}
	for _, n := range nodes {
		if n.HopDist > 1 {
			t.Errorf("got node at hop %d, maxHops was 1", n.HopDist)
		}
	}
}

func TestTraverseInvalidStart(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	g, err := palace.BuildGraph(v, "proj")
	if err != nil {
		t.Fatal(err)
	}

	_, err = g.Traverse("nonexistent/room", 3)
	if err == nil {
		t.Error("expected error for invalid start node")
	}
}

func TestBuildGraphMultipleDrawersPerRoom(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	for i := range 5 {
		d := storage.Drawer{
			Content:    fmt.Sprintf("content-%d", i),
			Hall:       "facts",
			SourceType: "manual",
			FiledAt:    "2026-01-01T00:00:00Z",
		}
		if err := v.AppendDrawer("proj", "alpha", "api", d); err != nil {
			t.Fatal(err)
		}
	}

	g, err := palace.BuildGraph(v, "proj")
	if err != nil {
		t.Fatal(err)
	}
	node := g.Nodes["alpha/api"]
	if node == nil {
		t.Fatal("node alpha/api not found")
	}
	if node.Drawers != 5 {
		t.Errorf("Drawers = %d, want 5", node.Drawers)
	}
}

func TestFindTunnelsSorted(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	seedVault(t, v, "proj", map[string][]string{
		"alpha": {"config", "api", "testing"},
		"beta":  {"testing", "api", "data"},
	})

	g, err := palace.BuildGraph(v, "proj")
	if err != nil {
		t.Fatal(err)
	}

	tunnels := g.FindTunnels()
	if len(tunnels) != 2 {
		t.Fatalf("got %d tunnels, want 2", len(tunnels))
	}
	// Should be sorted alphabetically: api, testing
	if tunnels[0].Room != "api" {
		t.Errorf("tunnel[0].Room = %q, want api", tunnels[0].Room)
	}
	if tunnels[1].Room != "testing" {
		t.Errorf("tunnel[1].Room = %q, want testing", tunnels[1].Room)
	}
}
