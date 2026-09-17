// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// 🔴 THIS IS THE HALF THAT REACHES A USER.
//
// The fail-closed readers in internal/storage were the visible half of this
// defect; the half that actually did damage is a consumer that cannot tell
// EMPTY from UNREADABLE. vp_bootstrap_context read the knowledge graph as:
//
//	// KG snapshot — Phase 7 may not exist yet, graceful.
//	if stats, err := vault.KGStats(project); err == nil {
//	    result.KGSnapshot = &stats
//	}
//
// That `err == nil` was written for one reason — a project with no graph must
// not fail bootstrap — and silently covered a second: a graph that could not be
// READ produced the identical payload, no snapshot and no word about why. It is
// the same shape as the session index returning zero rows and the absence
// reading as normal.

// unreadableKGVault returns a vault whose project has an entities file that
// cannot be read at all (a host-level failure, which stays fatal by design —
// see ListEntities' doc comment on read-fatal versus parse-skip).
func unreadableKGVault(t *testing.T) *storage.Vault {
	t.Helper()
	v := bornCurrentTestVault(t, t.TempDir())
	const project = "test-proj"
	if err := v.AddEntity(project, storage.Entity{
		ID: "e0", Name: "n0", Type: "concept", CreatedAt: "2026-06-06T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed entity: %v", err)
	}
	path, err := v.KGEntitiesFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestBootstrapDistinguishesNoKGFromUnreadableKG is T6.
//
// Break: restore the bare `if stats, err := vault.KGStats(project); err == nil`
// at context_tools.go. Both cases then produce a payload with no kg_snapshot and
// no kg_unreadable, and this test can no longer tell them apart — which is
// exactly the ambiguity it exists to remove.
func TestBootstrapDistinguishesNoKGFromUnreadableKG(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unreadable file is still readable")
	}

	// Case 1: a project with NO knowledge graph. Graceful, and silent is correct.
	clean := bornCurrentTestVault(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(clean.Root, "Projects", "test-proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	noKG := bootstrapFor(t, clean)
	// A ZERO-VALUED SNAPSHOT IS CORRECT HERE and is pre-existing behaviour:
	// KGStats returns a nil error and an all-zero block for a project with no
	// graph, so bootstrap attaches "a graph with nothing in it". That is a true
	// statement and this unit does not change it. What it must NOT do is claim
	// the graph is unreadable.
	if noKG.KGUnreadable != "" {
		t.Errorf("a project with no knowledge graph must not claim the graph is unreadable, got %q",
			noKG.KGUnreadable)
	}

	// Case 2: a project whose graph exists and CANNOT BE READ.
	broken := bootstrapFor(t, unreadableKGVault(t))
	if broken.KGSnapshot != nil {
		t.Errorf("an unreadable knowledge graph reported a snapshot anyway: %+v", broken.KGSnapshot)
	}
	if broken.KGUnreadable == "" {
		t.Fatal("an UNREADABLE knowledge graph produced the same payload as a project that simply " +
			"has none: no snapshot, no explanation. That ambiguity is the defect — a real failure " +
			"reads as normal and nobody is told anything")
	}

	// And the discriminator has to be legible, not merely present.
	if !strings.Contains(broken.KGUnreadable, "entities") {
		t.Errorf("kg_unreadable must say what could not be read, got %q", broken.KGUnreadable)
	}
}

// TestBootstrapKGUnreadableReachesTheWire pins that the discriminator survives
// serialization. A field that exists in the struct and not on the wire tells the
// agent reading the payload nothing at all.
//
// Break: add `json:"-"` to KGUnreadable, or drop the field from the struct.
func TestBootstrapKGUnreadableReachesTheWire(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unreadable file is still readable")
	}
	br := bootstrapFor(t, unreadableKGVault(t))

	raw, err := json.Marshal(br)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kg_unreadable"`) {
		t.Errorf("kg_unreadable never reached the wire, so the payload still cannot distinguish an "+
			"absent graph from an unreadable one.\n  wire: %s", raw)
	}

	// The clean case must NOT carry the key: omitempty is what keeps "no graph"
	// from reading as "a graph with an empty problem".
	clean := bornCurrentTestVault(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(clean.Root, "Projects", "test-proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	rawClean, err := json.Marshal(bootstrapFor(t, clean))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawClean), `"kg_unreadable"`) {
		t.Errorf("a project with no knowledge graph emitted kg_unreadable: %s", rawClean)
	}
}

// bootstrapFor runs the real vp_bootstrap_context handler against a vault.
func bootstrapFor(t *testing.T, v *storage.Vault) BootstrapResult {
	t.Helper()
	resolver := vpctx.NewResolver(v.Root)
	tool := BootstrapContextTool(resolver, v, nil)
	out, err := tool.Handler(context.Background(), json.RawMessage(`{"project": "test-proj"}`))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	br, ok := out.(BootstrapResult)
	if !ok {
		t.Fatalf("unexpected result type %T", out)
	}
	return br
}
