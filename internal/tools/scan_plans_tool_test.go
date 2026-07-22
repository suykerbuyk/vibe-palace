// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/planscan"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestScanPlansTool_NotMutating(t *testing.T) {
	if got := ScanPlansTool(storage.NewVault(t.TempDir())).Mutating; got {
		t.Fatalf("vp_scan_plans must be read-only (Mutating=false), got %v", got)
	}
}

// TestScanPlansTool_HonorsClaudeHome drives the handler with CLAUDE_HOME
// pointed at a temp dir holding one path-free plan and asserts a well-formed
// Report — never touching the real ~/.claude.
func TestScanPlansTool_HonorsClaudeHome(t *testing.T) {
	claudeHome := t.TempDir()
	plansDir := filepath.Join(claudeHome, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	if err := os.WriteFile(filepath.Join(plansDir, "p.md"), []byte("no paths here\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	old, had := os.LookupEnv("CLAUDE_HOME")
	os.Setenv("CLAUDE_HOME", claudeHome)
	t.Cleanup(func() {
		if had {
			os.Setenv("CLAUDE_HOME", old)
		} else {
			os.Unsetenv("CLAUDE_HOME")
		}
	})

	tool := ScanPlansTool(storage.NewVault(t.TempDir()))
	res, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	rep, ok := res.(planscan.Report)
	if !ok {
		t.Fatalf("result type = %T, want planscan.Report", res)
	}
	if len(rep.Strays) != 1 {
		t.Fatalf("got %d strays, want 1", len(rep.Strays))
	}
	if rep.Strays[0].Resolution.Kind != "none" {
		t.Errorf("Kind = %q, want none", rep.Strays[0].Resolution.Kind)
	}
}
