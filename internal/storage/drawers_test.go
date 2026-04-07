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
	return NewVault(t.TempDir())
}

func TestDrawerID(t *testing.T) {
	id1 := drawerID("wing-a", "room-1", "hello world")
	id2 := drawerID("wing-a", "room-1", "hello world")
	id3 := drawerID("wing-a", "room-2", "hello world")

	if id1 != id2 {
		t.Errorf("same inputs produced different IDs: %q vs %q", id1, id2)
	}
	if id1 == id3 {
		t.Error("different inputs produced same ID")
	}
	if len(id1) != 8 {
		t.Errorf("ID length = %d, want 8", len(id1))
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

	expectedID := drawerID("wing-a", "room-1", d.Content)
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

func TestListDrawersByWing(t *testing.T) {
	v := testVault(t)
	rooms := []string{"room-1", "room-2"}
	for _, room := range rooms {
		d := Drawer{Content: "content-" + room, Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z"}
		if err := v.AppendDrawer("proj", "wing-a", room, d); err != nil {
			t.Fatalf("AppendDrawer(%q): %v", room, err)
		}
	}

	got, err := v.ListDrawersByWing("proj", "wing-a")
	if err != nil {
		t.Fatalf("ListDrawersByWing: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListDrawersByWing returned %d drawers, want 2", len(got))
	}
}

func TestListDrawersByWingEmpty(t *testing.T) {
	v := testVault(t)
	got, err := v.ListDrawersByWing("proj", "nonexistent")
	if err != nil {
		t.Fatalf("ListDrawersByWing: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %d drawers", len(got))
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

	deleteID := drawerID("wing-a", "room-1", "delete-me")
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

func TestDrawerExists(t *testing.T) {
	v := testVault(t)
	d := Drawer{Content: "test", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T00:00:00Z"}
	if err := v.AppendDrawer("proj", "wing-a", "room-1", d); err != nil {
		t.Fatal(err)
	}

	id := drawerID("wing-a", "room-1", "test")
	exists, err := v.DrawerExists("proj", "wing-a", "room-1", id)
	if err != nil {
		t.Fatalf("DrawerExists: %v", err)
	}
	if !exists {
		t.Error("DrawerExists returned false for existing drawer")
	}

	exists, err = v.DrawerExists("proj", "wing-a", "room-1", "nonexist")
	if err != nil {
		t.Fatalf("DrawerExists: %v", err)
	}
	if exists {
		t.Error("DrawerExists returned true for nonexistent drawer")
	}
}

func TestDrawerExistsNoFile(t *testing.T) {
	v := testVault(t)
	exists, err := v.DrawerExists("proj", "wing-a", "room-1", "anything")
	if err != nil {
		t.Fatalf("DrawerExists: %v", err)
	}
	if exists {
		t.Error("DrawerExists should return false when file doesn't exist")
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
