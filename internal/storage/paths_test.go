// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPalacePath(t *testing.T) {
	v := NewVault("/vault")
	tests := []struct {
		name    string
		project string
		want    string
		wantErr bool
	}{
		{"valid", "recmeet", filepath.Join("/vault", "palace", "recmeet"), false},
		{"with hyphens", "my-project", filepath.Join("/vault", "palace", "my-project"), false},
		{"invalid slug", "My Project", "", true},
		{"empty", "", "", true},
		{"path traversal", "../evil", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := v.PalacePath(tt.project)
			if (err != nil) != tt.wantErr {
				t.Errorf("PalacePath(%q) error = %v, wantErr %v", tt.project, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("PalacePath(%q) = %q, want %q", tt.project, got, tt.want)
			}
		})
	}
}

func TestDrawerDir(t *testing.T) {
	v := NewVault("/vault")
	tests := []struct {
		name              string
		project, wing, rm string
		want              string
		wantErr           bool
	}{
		{
			"valid", "recmeet", "wing-recmeet", "audio-pipeline",
			filepath.Join("/vault", "palace", "recmeet", "drawers", "wing-recmeet", "audio-pipeline"),
			false,
		},
		{"invalid project", "BAD", "wing", "room", "", true},
		{"invalid wing", "ok", "BAD WING", "room", "", true},
		{"invalid room", "ok", "wing", "BAD ROOM", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := v.DrawerDir(tt.project, tt.wing, tt.rm)
			if (err != nil) != tt.wantErr {
				t.Errorf("DrawerDir error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("DrawerDir = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDrawerFile(t *testing.T) {
	v := NewVault("/vault")
	got, err := v.DrawerFile("recmeet", "wing-recmeet", "audio-pipeline")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/vault", "palace", "recmeet", "drawers", "wing-recmeet", "audio-pipeline", "drawers.jsonl")
	if got != want {
		t.Errorf("DrawerFile = %q, want %q", got, want)
	}

	// Invalid slug propagates error.
	_, err = v.DrawerFile("BAD", "wing", "room")
	if err == nil {
		t.Error("DrawerFile with invalid project should return error")
	}
}

func TestKGEntitiesFile(t *testing.T) {
	v := NewVault("/vault")
	got, err := v.KGEntitiesFile("recmeet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/vault", "palace", "recmeet", "kg", "entities.jsonl")
	if got != want {
		t.Errorf("KGEntitiesFile = %q, want %q", got, want)
	}

	_, err = v.KGEntitiesFile("BAD")
	if err == nil {
		t.Error("KGEntitiesFile with invalid project should return error")
	}
}

func TestKGTriplePath(t *testing.T) {
	v := NewVault("/vault")
	tests := []struct {
		name                        string
		project, subj, pred, obj    string
		want                        string
		wantErr                     bool
	}{
		{
			"basic encoding", "recmeet", "Kai", "works on", "Orion",
			filepath.Join("/vault", "palace", "recmeet", "kg", "triples", "kai--works_on--orion.json"),
			false,
		},
		{
			"already lowercase", "recmeet", "kai", "knows", "orion",
			filepath.Join("/vault", "palace", "recmeet", "kg", "triples", "kai--knows--orion.json"),
			false,
		},
		{
			"multiple spaces", "recmeet", "kai", "is friend of", "orion",
			filepath.Join("/vault", "palace", "recmeet", "kg", "triples", "kai--is_friend_of--orion.json"),
			false,
		},
		{"invalid project", "BAD", "kai", "knows", "orion", "", true},
		{"empty subject", "recmeet", "", "knows", "orion", "", true},
		{"empty predicate", "recmeet", "kai", "", "orion", "", true},
		{"empty object", "recmeet", "kai", "knows", "", "", true},
		{"subject contains delimiter", "recmeet", "kai--evil", "knows", "orion", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := v.KGTriplePath(tt.project, tt.subj, tt.pred, tt.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("KGTriplePath error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("KGTriplePath = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLocalDir(t *testing.T) {
	v := NewVault("/vault")
	got, err := v.LocalDir("recmeet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/vault", "palace", "recmeet", ".local")
	if got != want {
		t.Errorf("LocalDir = %q, want %q", got, want)
	}

	_, err = v.LocalDir("BAD")
	if err == nil {
		t.Error("LocalDir with invalid project should return error")
	}
}

func TestVaultLocalDir(t *testing.T) {
	v := NewVault("/vault")
	got := v.VaultLocalDir()
	want := filepath.Join("/vault", "palace", ".local")
	if got != want {
		t.Errorf("VaultLocalDir = %q, want %q", got, want)
	}
}

func TestEnsureDir(t *testing.T) {
	base := t.TempDir()

	// Create nested directory.
	nested := filepath.Join(base, "a", "b", "c")
	if err := EnsureDir(nested); err != nil {
		t.Fatalf("EnsureDir(%q) = %v", nested, err)
	}
	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("Stat after EnsureDir: %v", err)
	}
	if !info.IsDir() {
		t.Error("EnsureDir did not create a directory")
	}

	// Idempotent — calling again on existing dir succeeds.
	if err := EnsureDir(nested); err != nil {
		t.Fatalf("EnsureDir idempotent call = %v", err)
	}
}

func TestKGTriplesDir(t *testing.T) {
	v := NewVault("/vault")
	got, err := v.KGTriplesDir("recmeet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/vault", "palace", "recmeet", "kg", "triples")
	if got != want {
		t.Errorf("KGTriplesDir = %q, want %q", got, want)
	}

	_, err = v.KGTriplesDir("BAD")
	if err == nil {
		t.Error("KGTriplesDir with invalid project should return error")
	}
}

func TestSessionDir(t *testing.T) {
	v := NewVault("/vault")
	got, err := v.SessionDir("recmeet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/vault", "Projects", "recmeet", "sessions")
	if got != want {
		t.Errorf("SessionDir = %q, want %q", got, want)
	}

	_, err = v.SessionDir("BAD")
	if err == nil {
		t.Error("SessionDir with invalid project should return error")
	}
}

func TestSessionFile(t *testing.T) {
	v := NewVault("/vault")
	tests := []struct {
		name      string
		project   string
		date      string
		iteration int
		want      string
		wantErr   bool
	}{
		{
			"valid", "recmeet", "2026-03-15", 2,
			filepath.Join("/vault", "Projects", "recmeet", "sessions", "2026-03-15-02.md"),
			false,
		},
		{"invalid project", "BAD", "2026-03-15", 1, "", true},
		{"invalid date format", "recmeet", "March 15", 1, "", true},
		{"zero iteration", "recmeet", "2026-03-15", 0, "", true},
		{"negative iteration", "recmeet", "2026-03-15", -1, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := v.SessionFile(tt.project, tt.date, tt.iteration)
			if (err != nil) != tt.wantErr {
				t.Errorf("SessionFile error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("SessionFile = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTasksDir(t *testing.T) {
	v := NewVault("/vault")
	got, err := v.TasksDir("recmeet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/vault", "Projects", "recmeet", "tasks")
	if got != want {
		t.Errorf("TasksDir = %q, want %q", got, want)
	}

	_, err = v.TasksDir("BAD")
	if err == nil {
		t.Error("TasksDir with invalid project should return error")
	}
}

func TestTaskFile(t *testing.T) {
	v := NewVault("/vault")
	got, err := v.TaskFile("recmeet", "my-task")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/vault", "Projects", "recmeet", "tasks", "my-task.md")
	if got != want {
		t.Errorf("TaskFile = %q, want %q", got, want)
	}

	_, err = v.TaskFile("BAD", "my-task")
	if err == nil {
		t.Error("TaskFile with invalid project should return error")
	}
	_, err = v.TaskFile("recmeet", "BAD SLUG")
	if err == nil {
		t.Error("TaskFile with invalid slug should return error")
	}
}

func TestTaskDoneDir(t *testing.T) {
	v := NewVault("/vault")
	got, err := v.TaskDoneDir("recmeet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/vault", "Projects", "recmeet", "tasks", "done")
	if got != want {
		t.Errorf("TaskDoneDir = %q, want %q", got, want)
	}
}

func TestTaskCancelledDir(t *testing.T) {
	v := NewVault("/vault")
	got, err := v.TaskCancelledDir("recmeet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/vault", "Projects", "recmeet", "tasks", "cancelled")
	if got != want {
		t.Errorf("TaskCancelledDir = %q, want %q", got, want)
	}
}

func TestProjectConfigFile(t *testing.T) {
	v := NewVault("/vault")
	got, err := v.ProjectConfigFile("recmeet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/vault", "Projects", "recmeet", "config.toml")
	if got != want {
		t.Errorf("ProjectConfigFile = %q, want %q", got, want)
	}

	_, err = v.ProjectConfigFile("BAD")
	if err == nil {
		t.Error("ProjectConfigFile with invalid project should return error")
	}
}

func TestEncodeTripleComponent(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Kai", "kai"},
		{"works on", "works_on"},
		{"Already Lower", "already_lower"},
		{"ALLCAPS", "allcaps"},
		{"no-change", "no-change"},
		{"multi  space", "multi__space"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := encodeTripleComponent(tt.input)
			if got != tt.want {
				t.Errorf("encodeTripleComponent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
