// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestBootstrapContext_DoesNotWriteProjectShims pins Phase 4a / D4.
//
// The cwd MUST resolve to the bootstrapped project so a restored
// reconcileHostShims guard (DetectProject(cwd)==slug) would have *passed* and
// written shims. A bare temp dir fails that guard and makes the test pass for
// the wrong reason (review H1 / serve-wiring-test-passes-for-the-wrong-reason).
func TestBootstrapContext_DoesNotWriteProjectShims(t *testing.T) {
	vault, resolver := testSetup(t)

	// Directory basename == project slug so DetectProject basename fallback
	// matches even without a full git remote.
	parent := t.TempDir()
	projectDir := filepath.Join(parent, "test-proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// High-confidence marker naming the same slug.
	cfg := "name = \"test-proj\"\n"
	if err := os.WriteFile(filepath.Join(projectDir, ".vibe-palace.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(projectDir)

	tool := BootstrapContextTool(resolver, vault)
	params := json.RawMessage(`{"project":"test-proj"}`)
	if _, err := tool.Handler(context.Background(), params); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	shimDir := filepath.Join(projectDir, ".claude", "commands")
	if _, err := os.Stat(shimDir); !os.IsNotExist(err) {
		t.Errorf("bootstrap must not create %s even when cwd IS the project (err=%v)", shimDir, err)
	}
}
