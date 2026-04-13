// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

// absorbTestFixture is a CLAUDE.md with three classifiable sections plus a
// preamble. Three items reach the interactive prompt, enough to exercise the
// `y` → `A` → "rest auto-accepted" transition.
const absorbTestFixture = `# demoproj

Intro paragraph — preamble routes to resume scratch.

## Architecture (overview)

Some architecture text explaining the layout.
Second line of architecture body.
Third line.

## Testing strategy

Unit tests for every core path.

## Commands

- go build
- go test
`

func writeAbsorbFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"),
		[]byte(absorbTestFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// seedVaultProject writes the minimal Projects/{slug}/config.toml the absorb
// writer's guard requires.
func seedVaultProject(t *testing.T, vaultRoot, slug string) {
	t.Helper()
	projDir := filepath.Join(vaultRoot, "Projects", slug)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "config.toml"),
		[]byte("[project]\nname = \""+slug+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunAbsorb_InteractiveAcceptAll exercises the `A` accept-all path: the
// first item gets `y`, the second gets `A`, and the loop must consume the
// third without another prompt. The contract header and classification
// reasons must both appear in stdout.
func TestRunAbsorb_InteractiveAcceptAll(t *testing.T) {
	vaultRoot := setupTestVaultEnv(t)
	seedVaultProject(t, vaultRoot, "demoproj")
	projectRoot := writeAbsorbFixture(t)

	var stdout, stderr bytes.Buffer
	stdin := bytes.NewBufferString("y\nA\n")

	code := runAbsorb(absorbOpts{
		Project:     "demoproj",
		ProjectRoot: projectRoot,
		NoStage:     true,
		Stdin:       stdin,
		Stdout:      &stdout,
		Stderr:      &stderr,
	})
	if code != cli.ExitOK {
		t.Fatalf("runAbsorb exit=%d, stderr=%s", code, stderr.String())
	}

	out := stdout.String()

	// Contract header.
	if !strings.Contains(out, "Absorb will:") {
		t.Errorf("expected contract header in output, got:\n%s", out)
	}
	if !strings.Contains(out, "preserved in backups under .vibe-palace/") {
		t.Errorf("expected backup contract line, got:\n%s", out)
	}

	// Classification reasons (Phase 2B).
	if !strings.Contains(out, "matched: architecture") {
		t.Errorf("expected 'matched: architecture' reason, got:\n%s", out)
	}
	if !strings.Contains(out, "matched: testing") {
		t.Errorf("expected 'matched: testing' reason, got:\n%s", out)
	}
	if !strings.Contains(out, "matched: workflow-cmds") {
		t.Errorf("expected 'matched: workflow-cmds' reason, got:\n%s", out)
	}

	// `A` option visible in the prompt.
	if !strings.Contains(out, "[A]ccept all") {
		t.Errorf("expected [A]ccept all in prompt, got:\n%s", out)
	}

	// Exactly two prompt lines — one for `y`, one for `A`. The third
	// item should have been auto-accepted without another prompt.
	promptCount := strings.Count(out, "Accept this route?")
	if promptCount != 2 {
		t.Errorf("expected 2 prompts (y, A), got %d:\n%s", promptCount, out)
	}

	// Body preview lines appear (the `┆` gutter character).
	if !strings.Contains(out, "┆") {
		t.Errorf("expected body preview gutter '┆' in output, got:\n%s", out)
	}
}

// TestRunAbsorb_YesFlag verifies --yes prints no interactive prompts.
func TestRunAbsorb_YesFlag(t *testing.T) {
	vaultRoot := setupTestVaultEnv(t)
	seedVaultProject(t, vaultRoot, "demoproj")
	projectRoot := writeAbsorbFixture(t)

	var stdout, stderr bytes.Buffer
	code := runAbsorb(absorbOpts{
		Yes:         true,
		Project:     "demoproj",
		ProjectRoot: projectRoot,
		NoStage:     true,
		Stdin:       bytes.NewBuffer(nil),
		Stdout:      &stdout,
		Stderr:      &stderr,
	})
	if code != cli.ExitOK {
		t.Fatalf("runAbsorb exit=%d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "Accept this route?") {
		t.Errorf("--yes must not emit interactive prompts, got:\n%s", out)
	}
	// Contract header still appears.
	if !strings.Contains(out, "Absorb will:") {
		t.Errorf("expected contract header under --yes, got:\n%s", out)
	}
}

// TestRunAbsorb_DryRunContractHeader verifies --dry-run still prints the
// contract header and exits non-zero when items are present (preserving the
// pre-existing gate behavior at cmd_absorb.go).
func TestRunAbsorb_DryRunContractHeader(t *testing.T) {
	vaultRoot := setupTestVaultEnv(t)
	seedVaultProject(t, vaultRoot, "demoproj")
	projectRoot := writeAbsorbFixture(t)

	var stdout, stderr bytes.Buffer
	code := runAbsorb(absorbOpts{
		DryRun:      true,
		Project:     "demoproj",
		ProjectRoot: projectRoot,
		NoStage:     true,
		Stdin:       bytes.NewBuffer(nil),
		Stdout:      &stdout,
		Stderr:      &stderr,
	})
	if code != cli.ExitUser {
		t.Fatalf("--dry-run with items should exit ExitUser, got %d (stderr=%s)",
			code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Absorb will:") {
		t.Errorf("expected contract header under --dry-run, got:\n%s", out)
	}
	if strings.Contains(out, "Accept this route?") {
		t.Errorf("--dry-run must not prompt, got:\n%s", out)
	}
}

func TestBodyPreviewLines(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		maxLines int
		maxChars int
		want     []string
	}{
		{
			name:     "empty",
			body:     "",
			maxLines: 2,
			maxChars: 240,
			want:     nil,
		},
		{
			name:     "skip blank lines",
			body:     "\n\nfirst\n\nsecond\n",
			maxLines: 2,
			maxChars: 240,
			want:     []string{"first", "second"},
		},
		{
			name:     "cap at maxLines",
			body:     "a\nb\nc\nd\n",
			maxLines: 2,
			maxChars: 240,
			want:     []string{"a", "b"},
		},
		{
			name:     "truncate long line with ellipsis",
			body:     strings.Repeat("x", 300),
			maxLines: 2,
			maxChars: 20,
			want:     []string{strings.Repeat("x", 19) + "…"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := bodyPreviewLines(tc.body, tc.maxLines, tc.maxChars)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d lines %q, want %d %q",
					len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("line[%d]: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
