// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/planscan"
)

// runPlansScan drives `vp plans scan` with the given args, capturing stdout.
// CLAUDE_HOME must already be set by the caller so the real ~/.claude is never
// touched.
func runPlansScan(t *testing.T, args ...string) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	code := cmdPlansScan().Run(args)

	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out), code
}

// setClaudeHome points CLAUDE_HOME at dir for the duration of the test.
func setClaudeHome(t *testing.T, dir string) {
	t.Helper()
	old, had := os.LookupEnv("CLAUDE_HOME")
	os.Setenv("CLAUDE_HOME", dir)
	t.Cleanup(func() {
		if had {
			os.Setenv("CLAUDE_HOME", old)
		} else {
			os.Unsetenv("CLAUDE_HOME")
		}
	})
}

// TestPlansScanEmpty: an absent plans dir reports empty cleanly, exit 0.
func TestPlansScanEmpty(t *testing.T) {
	claudeHome := t.TempDir() // no plans/ subdir
	setClaudeHome(t, claudeHome)

	out, code := runPlansScan(t)
	if code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK", code)
	}
	if !strings.Contains(out, "No stray plans") {
		t.Errorf("output = %q, want a no-strays message", out)
	}
}

// TestPlansScanJSONShapeAndNoMutation: a stray plan is reported in the JSON
// shape and the plan file is left byte-for-byte unchanged (strictly read-only).
func TestPlansScanJSONShapeAndNoMutation(t *testing.T) {
	claudeHome := t.TempDir()
	setClaudeHome(t, claudeHome)

	plansDir := filepath.Join(claudeHome, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	planFile := filepath.Join(plansDir, "orphan.md")
	body := "# Plan\n\nRefactor the loader. No absolute paths in this body.\n"
	if err := os.WriteFile(planFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	before, err := os.ReadFile(planFile)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}

	out, code := runPlansScan(t, "--json")
	if code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK", code)
	}

	var rep planscan.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("unmarshal report: %v\noutput: %s", err, out)
	}
	if rep.PlansDir != plansDir {
		t.Errorf("PlansDir = %q, want %q", rep.PlansDir, plansDir)
	}
	if len(rep.Strays) != 1 {
		t.Fatalf("got %d strays, want 1", len(rep.Strays))
	}
	s := rep.Strays[0]
	if s.File != planFile {
		t.Errorf("File = %q, want %q", s.File, planFile)
	}
	if s.Resolution.Kind != "none" {
		t.Errorf("Kind = %q, want none (path-free body)", s.Resolution.Kind)
	}

	// The scan must not have written to the plan file.
	after, err := os.ReadFile(planFile)
	if err != nil {
		t.Fatalf("re-read plan: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("plan file mutated by scan:\nbefore=%q\nafter=%q", before, after)
	}
	// The plans dir must still contain exactly the one file we created.
	entries, err := os.ReadDir(plansDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "orphan.md" {
		t.Errorf("plans dir changed: %v", entries)
	}
}
