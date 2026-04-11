// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitAvailable(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
}

func TestGitIsRepo(t *testing.T) {
	dir := t.TempDir()
	if GitIsRepo(dir) {
		t.Error("fresh dir should not be a git repo")
	}

	if err := GitInit(dir); err != nil {
		t.Fatalf("GitInit: %v", err)
	}
	if !GitIsRepo(dir) {
		t.Error("dir should be a git repo after init")
	}
}

func TestGitInit(t *testing.T) {
	dir := t.TempDir()
	if err := GitInit(dir); err != nil {
		t.Fatalf("GitInit: %v", err)
	}

	gitDir := filepath.Join(dir, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		t.Fatalf(".git not created: %v", err)
	}
	if !info.IsDir() {
		t.Error(".git is not a directory")
	}
}

func TestWriteVaultGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := WriteVaultGitignore(dir); err != nil {
		t.Fatalf("WriteVaultGitignore: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(data), "palace/.local/") {
		t.Error(".gitignore should exclude palace/.local/")
	}
}
