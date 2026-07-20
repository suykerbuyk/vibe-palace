// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

// TestReconcile_FreshEmit_ThenIdempotent verifies the session-start reconcile
// core: a first run over an empty project materializes a shim per embedded
// command, names each in the report, and a second run is a silent no-op.
func TestReconcile_FreshEmit_ThenIdempotent(t *testing.T) {
	vault := t.TempDir()
	projectRoot := t.TempDir()
	resolver := vpctx.NewResolver(vault)

	// --- Round 1: fresh emit ---
	rep := Reconcile(projectRoot, resolver, "")
	if len(rep.Errors) != 0 {
		t.Fatalf("round 1 unexpected errors: %v", rep.Errors)
	}
	if rep.Empty() {
		t.Fatal("round 1 must report the shims it created; got Empty()")
	}
	if len(rep.CommandsAdded) == 0 {
		t.Fatal("round 1 CommandsAdded is empty; expected embedded commands")
	}
	// "restart" is a canonical embedded command — it must be both reported and
	// on disk with the shim marker.
	if !slices.Contains(rep.CommandsAdded, "restart") {
		t.Errorf("round 1 CommandsAdded missing 'restart'; got %v", rep.CommandsAdded)
	}
	for _, name := range rep.CommandsAdded {
		path := filepath.Join(projectRoot, ShimDir, Filename(name))
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reported-added shim %s not on disk: %v", name, err)
		}
		if !strings.Contains(string(body), "vibe-palace:shim v=") {
			t.Errorf("shim %s missing marker", name)
		}
	}

	// --- Round 2: idempotent — nothing added/updated, and nothing to report ---
	rep2 := Reconcile(projectRoot, resolver, "")
	if !rep2.Empty() {
		t.Errorf("round 2 must be a silent no-op; got %+v", rep2)
	}
}

// TestReconcile_LeavesCustomAndStaleUntouched proves the additive contract:
// a hand-written (marker-less) shim at a managed command's path is never
// rewritten, and a marker-bearing shim for a command that no longer exists is
// never deleted (AllowStaleRemoval is false on the reconcile path).
func TestReconcile_LeavesCustomAndStaleUntouched(t *testing.T) {
	vault := t.TempDir()
	projectRoot := t.TempDir()
	resolver := vpctx.NewResolver(vault)

	cmdDir := filepath.Join(projectRoot, ShimDir)
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// A hand-written shim at the "restart" path, WITHOUT our marker.
	customPath := filepath.Join(cmdDir, Filename("restart"))
	const customBody = "my own restart command, hands off\n"
	if err := os.WriteFile(customPath, []byte(customBody), 0o644); err != nil {
		t.Fatal(err)
	}

	// A marker-bearing shim for a command name the vault does not have — Wire
	// stamps a real marker, so the planner classifies it Stale on reconcile.
	stalePath := filepath.Join(cmdDir, Filename("no-such-command-xyz"))
	if _, err := Wire(stalePath, "no-such-command-xyz", "gone", "", ""); err != nil {
		t.Fatalf("seed stale shim: %v", err)
	}

	rep := Reconcile(projectRoot, resolver, "")
	if len(rep.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", rep.Errors)
	}

	// The custom file must be byte-for-byte untouched. This is the real
	// additive contract; the report may still name "restart" if another target
	// (e.g. Grok, when GrokPresent) wrote its own shim where no custom file
	// sits — the report dedups capability names ACROSS targets, so it is not a
	// per-file claim.
	got, err := os.ReadFile(customPath)
	if err != nil {
		t.Fatalf("read custom shim: %v", err)
	}
	if string(got) != customBody {
		t.Errorf("custom shim was rewritten:\n got %q\nwant %q", got, customBody)
	}

	// The stale shim must survive — reconcile never removes.
	if _, err := os.Stat(stalePath); err != nil {
		t.Errorf("stale shim was removed by reconcile (must be additive-only): %v", err)
	}
}

// TestReconcile_NilAndEmptyInputs guards the cheap early return.
func TestReconcile_NilAndEmptyInputs(t *testing.T) {
	if got := Reconcile("", vpctx.NewResolver(t.TempDir()), ""); !got.Empty() {
		t.Errorf("empty projectRoot must be a no-op; got %+v", got)
	}
	if got := Reconcile(t.TempDir(), nil, ""); !got.Empty() {
		t.Errorf("nil resolver must be a no-op; got %+v", got)
	}
}
