// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func testVault(t *testing.T) *Vault {
	t.Helper()
	return bornCurrentVault(t, t.TempDir())
}

func TestDrawerID(t *testing.T) {
	id1 := DrawerID("wing-a", "hello world")
	id2 := DrawerID("wing-a", "hello world")
	id3 := DrawerID("wing-b", "hello world")
	id4 := DrawerID("wing-a", "different content")

	if id1 != id2 {
		t.Errorf("same inputs produced different IDs: %q vs %q", id1, id2)
	}
	if id1 == id3 {
		t.Error("different wings should produce different IDs")
	}
	if id1 == id4 {
		t.Error("different content should produce different IDs")
	}
	if len(id1) != 8 {
		t.Errorf("ID length = %d, want 8", len(id1))
	}
}

func TestDrawerIDRoomIndependent(t *testing.T) {
	// Drawer ID must be stable across room reclassification.
	// Same wing+content should produce identical IDs regardless of room.
	id := DrawerID("wing-a", "hello world")
	if len(id) != 8 {
		t.Fatalf("ID length = %d, want 8", len(id))
	}
	// Verify determinism across multiple calls.
	for i := range 10 {
		if got := DrawerID("wing-a", "hello world"); got != id {
			t.Errorf("call %d: got %q, want %q", i, got, id)
		}
	}
}

func TestAppendAndGetDrawer(t *testing.T) {
	v := testVault(t)
	d := Drawer{
		Hall:       "facts",
		Content:    "test content",
		SourceType: "session",
		FiledAt:    "2026-03-15T14:00:00Z",
	}

	if err := v.AppendDrawer("proj", "wing-a", "room-1", d); err != nil {
		t.Fatalf("AppendDrawer: %v", err)
	}

	expectedID := DrawerID("wing-a", d.Content)
	got, err := v.GetDrawer("proj", "wing-a", "room-1", expectedID)
	if err != nil {
		t.Fatalf("GetDrawer: %v", err)
	}
	if got.Content != d.Content {
		t.Errorf("Content = %q, want %q", got.Content, d.Content)
	}
	if got.ID != expectedID {
		t.Errorf("ID = %q, want %q", got.ID, expectedID)
	}
	if got.Hall != "facts" {
		t.Errorf("Hall = %q, want %q", got.Hall, "facts")
	}
}

func TestAppendDrawerDuplicate(t *testing.T) {
	v := testVault(t)
	d := Drawer{
		Hall:       "facts",
		Content:    "same content",
		SourceType: "session",
		FiledAt:    "2026-03-15T14:00:00Z",
	}

	if err := v.AppendDrawer("proj", "wing-a", "room-1", d); err != nil {
		t.Fatalf("first append: %v", err)
	}
	err := v.AppendDrawer("proj", "wing-a", "room-1", d)
	if err == nil {
		t.Error("duplicate append should return error")
	}
}

func TestListDrawers(t *testing.T) {
	v := testVault(t)
	contents := []string{"alpha", "bravo", "charlie"}
	for _, c := range contents {
		d := Drawer{Content: c, Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z"}
		if err := v.AppendDrawer("proj", "wing-a", "room-1", d); err != nil {
			t.Fatalf("AppendDrawer(%q): %v", c, err)
		}
	}

	got, err := v.ListDrawers("proj", "wing-a", "room-1")
	if err != nil {
		t.Fatalf("ListDrawers: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListDrawers returned %d drawers, want 3", len(got))
	}
}

func TestDeleteDrawer(t *testing.T) {
	v := testVault(t)
	d1 := Drawer{Content: "keep", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z"}
	d2 := Drawer{Content: "delete-me", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z"}

	if err := v.AppendDrawer("proj", "wing-a", "room-1", d1); err != nil {
		t.Fatal(err)
	}
	if err := v.AppendDrawer("proj", "wing-a", "room-1", d2); err != nil {
		t.Fatal(err)
	}

	deleteID := DrawerID("wing-a", "delete-me")
	if err := v.DeleteDrawer("proj", "wing-a", "room-1", deleteID); err != nil {
		t.Fatalf("DeleteDrawer: %v", err)
	}

	remaining, err := v.ListDrawers("proj", "wing-a", "room-1")
	if err != nil {
		t.Fatalf("ListDrawers: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("expected 1 drawer after delete, got %d", len(remaining))
	}
	if remaining[0].Content != "keep" {
		t.Errorf("remaining drawer content = %q, want %q", remaining[0].Content, "keep")
	}
}

func TestDeleteDrawerAtomicWrite(t *testing.T) {
	v := testVault(t)
	contents := []string{"alpha", "bravo", "charlie"}
	for _, c := range contents {
		d := Drawer{Content: c, Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z"}
		if err := v.AppendDrawer("proj", "wing-a", "room-1", d); err != nil {
			t.Fatalf("AppendDrawer(%q): %v", c, err)
		}
	}

	// Delete the middle entry.
	deleteID := DrawerID("wing-a", "bravo")
	if err := v.DeleteDrawer("proj", "wing-a", "room-1", deleteID); err != nil {
		t.Fatalf("DeleteDrawer: %v", err)
	}

	// Verify raw file content: exactly 2 valid JSONL lines, no temp files left.
	path, _ := v.DrawerFile("proj", "wing-a", "room-1")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lines := splitNonEmpty(string(data))
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSONL lines, got %d", len(lines))
	}
	for _, line := range lines {
		var d Drawer
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			t.Fatalf("invalid JSONL line: %v", err)
		}
		if d.Content == "bravo" {
			t.Error("deleted drawer still present in file")
		}
	}

	// Verify no temp file remains.
	tmpPath := filepath.Join(filepath.Dir(path), ".tmp-"+filepath.Base(path))
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("temp file was not cleaned up after atomic rename")
	}
}

func TestDeleteDrawerNotFound(t *testing.T) {
	v := testVault(t)
	d := Drawer{Content: "hello", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z"}
	if err := v.AppendDrawer("proj", "wing-a", "room-1", d); err != nil {
		t.Fatal(err)
	}

	err := v.DeleteDrawer("proj", "wing-a", "room-1", "nonexist")
	if err == nil {
		t.Error("deleting nonexistent drawer should return error")
	}
}

func TestDeleteDrawerNoFile(t *testing.T) {
	v := testVault(t)
	err := v.DeleteDrawer("proj", "wing-a", "room-1", "anyid")
	if err == nil {
		t.Error("deleting from nonexistent file should return error")
	}
}

func TestDeleteDrawerAllEntries(t *testing.T) {
	v := testVault(t)
	d := Drawer{Content: "only-one", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z"}
	if err := v.AppendDrawer("proj", "wing-a", "room-1", d); err != nil {
		t.Fatal(err)
	}

	id := DrawerID("wing-a", "only-one")
	if err := v.DeleteDrawer("proj", "wing-a", "room-1", id); err != nil {
		t.Fatalf("DeleteDrawer: %v", err)
	}

	// File should exist but be empty.
	path, _ := v.DrawerFile("proj", "wing-a", "room-1")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty file after deleting all entries, got %d bytes", len(data))
	}
}

func TestAppendDrawerInvalidSlug(t *testing.T) {
	v := testVault(t)
	d := Drawer{Content: "test", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z"}
	err := v.AppendDrawer("BAD PROJECT", "wing-a", "room-1", d)
	if err == nil {
		t.Error("AppendDrawer with invalid slug should return error")
	}
}

func TestDrawerJSONLFormat(t *testing.T) {
	v := testVault(t)
	d := Drawer{Content: "test", Hall: "facts", SourceType: "session", SourceRef: "2026-03-15-01", FiledAt: "2026-01-01T00:00:00Z"}
	if err := v.AppendDrawer("proj", "wing-a", "room-1", d); err != nil {
		t.Fatal(err)
	}

	// Read raw file and verify it's valid JSONL (one line).
	path, _ := v.DrawerFile("proj", "wing-a", "room-1")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	lines := splitNonEmpty(string(data))
	if len(lines) != 1 {
		t.Fatalf("expected 1 JSONL line, got %d", len(lines))
	}

	var parsed Drawer
	if err := json.Unmarshal([]byte(lines[0]), &parsed); err != nil {
		t.Fatalf("JSONL line is not valid JSON: %v", err)
	}
	if parsed.SourceRef != "2026-03-15-01" {
		t.Errorf("SourceRef = %q, want %q", parsed.SourceRef, "2026-03-15-01")
	}
}

func TestListDrawersEmptyRoom(t *testing.T) {
	v := testVault(t)
	got, err := v.ListDrawers("proj", "wing-a", "room-1")
	if err != nil {
		t.Fatalf("ListDrawers: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for nonexistent room, got %v", got)
	}
}

func TestGetDrawerNotFound(t *testing.T) {
	v := testVault(t)
	d := Drawer{Content: "test", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z"}
	if err := v.AppendDrawer("proj", "wing-a", "room-1", d); err != nil {
		t.Fatal(err)
	}

	_, err := v.GetDrawer("proj", "wing-a", "room-1", "nonexist")
	if err == nil {
		t.Error("GetDrawer should return error for nonexistent ID")
	}
}

func TestListWings(t *testing.T) {
	v := testVault(t)
	d := Drawer{Content: "c1", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z"}

	if err := v.AppendDrawer("proj", "alpha", "room-1", d); err != nil {
		t.Fatal(err)
	}
	d.Content = "c2"
	if err := v.AppendDrawer("proj", "beta", "room-1", d); err != nil {
		t.Fatal(err)
	}

	wings, err := v.ListWings("proj")
	if err != nil {
		t.Fatalf("ListWings: %v", err)
	}
	if len(wings) != 2 {
		t.Fatalf("got %d wings, want 2", len(wings))
	}

	// Wings should include both alpha and beta (order from ReadDir).
	found := map[string]bool{}
	for _, w := range wings {
		found[w] = true
	}
	if !found["alpha"] || !found["beta"] {
		t.Errorf("wings = %v, want alpha and beta", wings)
	}
}

func TestListWingsEmpty(t *testing.T) {
	v := testVault(t)
	wings, err := v.ListWings("proj")
	if err != nil {
		t.Fatalf("ListWings: %v", err)
	}
	if wings != nil {
		t.Errorf("expected nil for nonexistent project, got %v", wings)
	}
}

func TestListRooms(t *testing.T) {
	v := testVault(t)
	d := Drawer{Content: "c1", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z"}

	if err := v.AppendDrawer("proj", "wing-a", "room-1", d); err != nil {
		t.Fatal(err)
	}
	d.Content = "c2"
	if err := v.AppendDrawer("proj", "wing-a", "room-2", d); err != nil {
		t.Fatal(err)
	}
	d.Content = "c3"
	if err := v.AppendDrawer("proj", "wing-a", "room-3", d); err != nil {
		t.Fatal(err)
	}

	rooms, err := v.ListRooms("proj", "wing-a")
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	if len(rooms) != 3 {
		t.Fatalf("got %d rooms, want 3", len(rooms))
	}
	found := map[string]bool{}
	for _, r := range rooms {
		found[r] = true
	}
	if !found["room-1"] || !found["room-2"] || !found["room-3"] {
		t.Errorf("rooms = %v, want room-1, room-2, room-3", rooms)
	}
}

func TestListRoomsEmpty(t *testing.T) {
	v := testVault(t)
	rooms, err := v.ListRooms("proj", "nonexistent")
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	if rooms != nil {
		t.Errorf("expected nil for nonexistent wing, got %v", rooms)
	}
}

func TestListProjects(t *testing.T) {
	v := testVault(t)
	d := Drawer{Content: "c1", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z"}

	if err := v.AppendDrawer("proj-a", "wing-1", "room-1", d); err != nil {
		t.Fatal(err)
	}
	d.Content = "c2"
	if err := v.AppendDrawer("proj-b", "wing-1", "room-1", d); err != nil {
		t.Fatal(err)
	}

	projects, err := v.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("got %d projects, want 2", len(projects))
	}
	found := map[string]bool{}
	for _, p := range projects {
		found[p] = true
	}
	if !found["proj-a"] || !found["proj-b"] {
		t.Errorf("projects = %v, want proj-a and proj-b", projects)
	}
}

func TestListProjectsSkipsLocal(t *testing.T) {
	v := testVault(t)
	// Create palace/.local directory — should be skipped.
	if err := os.MkdirAll(filepath.Join(v.Root, "palace", ".local"), 0755); err != nil {
		t.Fatal(err)
	}
	d := Drawer{Content: "c1", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z"}
	if err := v.AppendDrawer("real-proj", "wing-1", "room-1", d); err != nil {
		t.Fatal(err)
	}

	projects, err := v.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 || projects[0] != "real-proj" {
		t.Errorf("projects = %v, want [real-proj]", projects)
	}
}

func TestListProjectsEmpty(t *testing.T) {
	v := testVault(t)
	projects, err := v.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if projects != nil {
		t.Errorf("expected nil for empty palace, got %v", projects)
	}
}

// --- MoveDrawer tests ---

func TestMoveDrawer(t *testing.T) {
	v := testVault(t)
	d := Drawer{Content: "move me", Hall: "facts", SourceType: "manual", FiledAt: "2026-04-10T10:00:00Z"}
	if err := v.AppendDrawer("proj", "wing", "api", d); err != nil {
		t.Fatalf("AppendDrawer: %v", err)
	}
	drawers, _ := v.ListDrawers("proj", "wing", "api")
	id := drawers[0].ID

	if err := v.MoveDrawer("proj", "wing", "api", "testing", id); err != nil {
		t.Fatalf("MoveDrawer: %v", err)
	}

	// Should be gone from source.
	_, err := v.GetDrawer("proj", "wing", "api", id)
	if err == nil {
		t.Error("expected drawer to be removed from source room")
	}

	// Should be in destination.
	got, err := v.GetDrawer("proj", "wing", "testing", id)
	if err != nil {
		t.Fatalf("drawer not found in destination: %v", err)
	}
	if got.Content != "move me" {
		t.Errorf("content = %q, want %q", got.Content, "move me")
	}
}

func TestMoveDrawer_SameRoom(t *testing.T) {
	v := testVault(t)
	d := Drawer{Content: "stay here", Hall: "facts", SourceType: "manual", FiledAt: "2026-04-10T10:00:00Z"}
	if err := v.AppendDrawer("proj", "wing", "api", d); err != nil {
		t.Fatalf("AppendDrawer: %v", err)
	}
	drawers, _ := v.ListDrawers("proj", "wing", "api")
	id := drawers[0].ID

	// Same room should be a no-op.
	if err := v.MoveDrawer("proj", "wing", "api", "api", id); err != nil {
		t.Fatalf("MoveDrawer same room: %v", err)
	}

	// Should still be in original room.
	if _, err := v.GetDrawer("proj", "wing", "api", id); err != nil {
		t.Errorf("drawer should still be in room: %v", err)
	}
}

func TestMoveDrawer_NotFound(t *testing.T) {
	v := testVault(t)
	err := v.MoveDrawer("proj", "wing", "api", "testing", "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent drawer")
	}
}

func TestMoveDrawer_PreservesID(t *testing.T) {
	v := testVault(t)
	d := Drawer{Content: "same id check", Hall: "facts", SourceType: "manual", FiledAt: "2026-04-10T10:00:00Z"}
	if err := v.AppendDrawer("proj", "wing", "api", d); err != nil {
		t.Fatalf("AppendDrawer: %v", err)
	}
	drawers, _ := v.ListDrawers("proj", "wing", "api")
	originalID := drawers[0].ID

	if err := v.MoveDrawer("proj", "wing", "api", "data", originalID); err != nil {
		t.Fatalf("MoveDrawer: %v", err)
	}

	got, err := v.GetDrawer("proj", "wing", "data", originalID)
	if err != nil {
		t.Fatalf("GetDrawer after move: %v", err)
	}
	if got.ID != originalID {
		t.Errorf("ID changed after move: %q → %q", originalID, got.ID)
	}
}

// splitNonEmpty splits s by newline and returns non-empty lines.
func splitNonEmpty(s string) []string {
	var lines []string
	for _, line := range filepath.SplitList(s) {
		if line != "" {
			lines = append(lines, line)
		}
	}
	// filepath.SplitList is for PATH; use strings approach instead.
	lines = nil
	for _, line := range split(s) {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func split(s string) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		result = append(result, s[start:])
	}
	return result
}
