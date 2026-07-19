// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

// TestGenerateManPages generates man pages from command metadata.
// By default it writes to t.TempDir() to validate formatting.
// Set VP_GEN_MAN=1 to write to doc/man/man1/ in the repo root.
func TestGenerateManPages(t *testing.T) {
	info := cli.BuildInfo{Version: "0.1.0", Commit: "HEAD", BuildDate: "2026-04-10"}
	reg := cli.NewRegistry(info)
	registerAll(reg, info)

	outDir := t.TempDir()
	if os.Getenv("VP_GEN_MAN") != "" {
		// Resolve repo root via go.mod location.
		root, err := findRepoRoot()
		if err != nil {
			t.Fatalf("find repo root: %v", err)
		}
		outDir = filepath.Join(root, "doc", "man", "man1")
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		t.Logf("Writing man pages to %s", outDir)
	}

	// Build a map for resolving children.
	allCmds := make(map[string]*cli.Command)
	for _, name := range knownCommands() {
		if cmd, ok := reg.Lookup(name); ok {
			allCmds[name] = cmd
		}
	}

	for name, cmd := range allCmds {
		if name == "help" {
			continue
		}

		var children []*cli.Command
		for _, sub := range cmd.Subcommands {
			if child, ok := allCmds[sub]; ok {
				children = append(children, child)
			}
		}

		page := cli.FormatManPage(cmd, children, 1, "2026-04-10", info.Version)
		if page == "" {
			t.Errorf("empty man page for %q", name)
			continue
		}

		// Basic validation.
		if !strings.Contains(page, ".TH") {
			t.Errorf("%s: missing .TH header", name)
		}
		if !strings.Contains(page, ".SH NAME") {
			t.Errorf("%s: missing NAME section", name)
		}

		filename := "vp-" + strings.ReplaceAll(name, " ", "-") + ".1"
		path := filepath.Join(outDir, filename)
		if err := os.WriteFile(path, []byte(page), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

// knownCommands returns the command names that get man pages. Two-word entries
// (e.g. "archive backfill") must be listed explicitly: TestGenerateManPages
// resolves a parent's children by looking each subcommand up in THIS list, so a
// parent whose subcommands are absent here emits a page with no child sections.
func knownCommands() []string {
	return []string{
		"archive", "archive create", "archive list", "archive verify",
		"archive extract", "archive backfill", "archive link",
		"audit", "audit rooms", "audit vault",
		"check", "commands", "commands list", "commands upgrade",
		"init", "inject", "mcp", "mcp serve",
		"memory", "memory harvest",
		"migrate", "migrate mempalace", "migrate vibevault",
		"search", "sessions", "status", "tasks",
		"vault", "vault pull", "vault push", "vault status", "vault sync", "vault tidy",
		"version",
	}
}

// findRepoRoot walks up from the current directory to find go.mod.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
