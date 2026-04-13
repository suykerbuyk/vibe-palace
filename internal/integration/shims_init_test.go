// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/shims"
)

// TestIntegrationShimsLifecycle drives the full shim lifecycle against a
// fixture vault + project directory: emit, re-emit (idempotent), introduce
// a vault-tier override that changes a brief (Modified path), simulate an
// upstream command rename (Stale + New paths), and verify Apply respects
// AllowStaleRemoval=false. This is the cross-layer assertion that the
// init-time wiring keeps the on-disk shim set converged with the
// resolver's command list.
func TestIntegrationShimsLifecycle(t *testing.T) {
	vault := t.TempDir()
	projectRoot := t.TempDir()
	resolver := vpctx.NewResolver(vault)

	// --- Round 1: fresh emit ---
	summaries, err := commands.List(resolver, "command", "", "", "", 60)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) == 0 {
		t.Fatal("expected at least one embedded command")
	}

	if err := os.MkdirAll(filepath.Join(projectRoot, shims.ShimDir), 0o755); err != nil {
		t.Fatal(err)
	}

	plan, err := shims.Plan(summaries, projectRoot)
	if err != nil {
		t.Fatalf("Plan (round 1): %v", err)
	}
	for _, c := range plan {
		if c.Kind != shims.New {
			t.Errorf("round 1 kind for %s = %s, want new", c.Name, c.Kind)
		}
	}
	rep, err := shims.Apply(plan, shims.ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply (round 1): %v", err)
	}
	if rep.Added != len(summaries) {
		t.Errorf("round 1 Added = %d, want %d", rep.Added, len(summaries))
	}
	if rep.Updated != 0 || rep.Stale != 0 {
		t.Errorf("round 1 unexpected counts: %+v", rep)
	}

	// Each shim file exists on disk with the marker.
	for _, s := range summaries {
		path := filepath.Join(projectRoot, shims.ShimDir, shims.Filename(s.Name))
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read shim %s: %v", path, err)
		}
		if !strings.Contains(string(body), "vibe-palace:shim v=") {
			t.Errorf("shim %s missing marker", path)
		}
	}

	// --- Round 2: idempotent re-emit ---
	plan2, err := shims.Plan(summaries, projectRoot)
	if err != nil {
		t.Fatalf("Plan (round 2): %v", err)
	}
	for _, c := range plan2 {
		if c.Kind != shims.UnchangedChange {
			t.Errorf("round 2 kind for %s = %s, want unchanged", c.Name, c.Kind)
		}
	}
	rep2, err := shims.Apply(plan2, shims.ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply (round 2): %v", err)
	}
	if rep2.Added != 0 || rep2.Updated != 0 {
		t.Errorf("round 2 must be idempotent; got %+v", rep2)
	}

	// --- Round 3: vault-tier override changes a brief => Modified ---
	target := summaries[0].Name
	overridePath := filepath.Join(vault, "Templates", "commands", target+".md")
	if err := os.MkdirAll(filepath.Dir(overridePath), 0o755); err != nil {
		t.Fatal(err)
	}
	const overriddenBrief = "INTEGRATION-OVERRIDE brief replaces embedded"
	overrideBody := "# " + target + "\n\n" + overriddenBrief + "\n"
	if err := os.WriteFile(overridePath, []byte(overrideBody), 0o644); err != nil {
		t.Fatal(err)
	}
	summaries3, err := commands.List(resolver, "command", "", "", "", 60)
	if err != nil {
		t.Fatalf("List (round 3): %v", err)
	}
	plan3, err := shims.Plan(summaries3, projectRoot)
	if err != nil {
		t.Fatalf("Plan (round 3): %v", err)
	}
	var modCount int
	for _, c := range plan3 {
		if c.Name == target {
			if c.Kind != shims.Modified {
				t.Errorf("round 3 target %s kind = %s, want modified", target, c.Kind)
			}
			modCount++
		} else if c.Kind != shims.UnchangedChange {
			t.Errorf("round 3 non-target %s kind = %s, want unchanged", c.Name, c.Kind)
		}
	}
	if modCount != 1 {
		t.Errorf("expected exactly 1 modified, got %d", modCount)
	}
	if _, err := shims.Apply(plan3, shims.ApplyOptions{}); err != nil {
		t.Fatalf("Apply (round 3): %v", err)
	}

	// --- Round 4: upstream rename simulation => Stale + AllowStaleRemoval contract ---
	// Drop a shim for a name not in summaries; re-Plan must classify Stale.
	stalePath := filepath.Join(projectRoot, shims.ShimDir, shims.Filename("ghostcmd"))
	if err := os.WriteFile(stalePath, []byte(shims.Render("ghostcmd", "ghost brief")), 0o644); err != nil {
		t.Fatal(err)
	}
	plan4, err := shims.Plan(summaries3, projectRoot)
	if err != nil {
		t.Fatalf("Plan (round 4): %v", err)
	}
	var staleSeen bool
	for _, c := range plan4 {
		if c.Name == "ghostcmd" && c.Kind == shims.Stale {
			staleSeen = true
		}
	}
	if !staleSeen {
		t.Error("round 4: expected ghostcmd to be classified Stale")
	}

	// AllowStaleRemoval=false: file persists, counted but not deleted.
	rep4, err := shims.Apply(plan4, shims.ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply (round 4, additive): %v", err)
	}
	if rep4.Stale != 1 {
		t.Errorf("round 4 Stale count = %d, want 1", rep4.Stale)
	}
	if _, err := os.Stat(stalePath); err != nil {
		t.Errorf("additive Apply must leave Stale shim on disk: %v", err)
	}

	// AllowStaleRemoval=true: file removed.
	rep5, err := shims.Apply(plan4, shims.ApplyOptions{AllowStaleRemoval: true})
	if err != nil {
		t.Fatalf("Apply (round 5, AllowStaleRemoval): %v", err)
	}
	if rep5.Removed != 1 {
		t.Errorf("round 5 Removed = %d, want 1", rep5.Removed)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Errorf("AllowStaleRemoval Apply must delete Stale shim; stat err = %v", err)
	}
}
