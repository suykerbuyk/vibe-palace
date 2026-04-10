// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
)

func TestInitCreatesConfig(t *testing.T) {
	dir := t.TempDir()
	cmd := cmdInit()
	code := cmd.Run([]string{dir, "--name", "test-proj"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	configPath := filepath.Join(dir, project.ConfigFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config not created: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `name = "test-proj"`) {
		t.Errorf("config missing project name: %s", content)
	}
}

func TestInitWithDomainAndTags(t *testing.T) {
	dir := t.TempDir()
	cmd := cmdInit()
	code := cmd.Run([]string{dir, "--name", "myapp", "--domain", "work", "--tags", "go,cli"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	data, _ := os.ReadFile(filepath.Join(dir, project.ConfigFileName))
	content := string(data)
	if !strings.Contains(content, `domain = "work"`) {
		t.Errorf("missing domain: %s", content)
	}
	if !strings.Contains(content, "tags") {
		t.Errorf("missing tags: %s", content)
	}
}

func TestInitRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	// Create existing config.
	os.WriteFile(filepath.Join(dir, project.ConfigFileName), []byte("exists"), 0o644)

	cmd := cmdInit()
	code := cmd.Run([]string{dir, "--name", "test"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (should refuse overwrite)", code, cli.ExitUser)
	}
}

func TestInitInvalidName(t *testing.T) {
	dir := t.TempDir()
	cmd := cmdInit()
	code := cmd.Run([]string{dir, "--name", "INVALID NAME!"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (invalid name)", code, cli.ExitUser)
	}
}

func TestInitAutoDetectsName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-project")
	os.MkdirAll(dir, 0o755)

	cmd := cmdInit()
	code := cmd.Run([]string{dir})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	data, _ := os.ReadFile(filepath.Join(dir, project.ConfigFileName))
	if !strings.Contains(string(data), "my-project") {
		t.Errorf("expected auto-detected name: %s", data)
	}
}

func TestInitBadFlags(t *testing.T) {
	cmd := cmdInit()
	code := cmd.Run([]string{"--unknown-flag"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (bad flags)", code, cli.ExitUser)
	}
}
