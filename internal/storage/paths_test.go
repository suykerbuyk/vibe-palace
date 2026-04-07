// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateSlug(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "myproject", false},
		{"valid with hyphens", "my-cool-project", false},
		{"valid single char", "a", false},
		{"valid alphanumeric", "project123", false},
		{"valid numeric start", "123project", false},
		{"empty", "", true},
		{"uppercase", "MyProject", true},
		{"mixed case", "myProject", true},
		{"contains dot dot", "my..project", true},
		{"contains forward slash", "my/project", true},
		{"contains backslash", "my\\project", true},
		{"leading hyphen", "-project", true},
		{"trailing hyphen", "project-", true},
		{"consecutive hyphens", "my--project", true},
		{"contains space", "my project", true},
		{"contains underscore", "my_project", true},
		{"contains dot", "my.project", true},
		{"absolute path unix", "/etc/passwd", true},
		{"path traversal", "../secret", true},
		{"too long", "abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstuvwxyz-abcdefghijk", true}, // 65 chars
		{"exactly 64 chars", "abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstuvwxyz-abcdefghij", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSlug(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSlug(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

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
