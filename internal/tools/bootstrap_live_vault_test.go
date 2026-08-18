// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestBootstrapLiveVaultStillRestoresASession is the Phase 2 gate's second
// clause, made runnable: "a live restart still restores a working session".
//
// 🔴 IT MEASURES DELIVERY, NOT SIZE. Its predecessor asserted that the payload
// fit a token budget and that no ADR-009 "core" rung was shed. Both subjects are
// gone — first-principles Phase 2 deleted the shed ladder, the budget, the tier
// vocabulary and the workflow digest — so the assertions went with them rather
// than being rewritten into a smaller ceiling. There is no ceiling here on
// purpose: re-introducing one would re-create the disease (PRD §1.10), and this
// canary must never become the place a budget grows back.
//
// What is left is the only claim that still has a subject: an agent calling
// vp_bootstrap_context against the real vault gets a payload it can act on, and
// can tell whether it arrived whole.
//
// 🔴 IT AUTO-DISCOVERS THE VAULT AND RUNS IN `make test`, ON PURPOSE. The obvious
// alternative — gate it behind an env var — makes it a test that runs when
// someone REMEMBERS to run it. This project has a name for that: capability
// built, nothing invokes it. It skips cleanly where there is no vault (CI, a
// fresh clone), which is the only case that needs the escape hatch.
//
// 🔴 -count=1 IS NOT OPTIONAL WHEN YOU EDIT THE VAULT. The vault lives OUTSIDE
// the module, so `go test` cannot see that its contents changed and will serve a
// CACHED verdict — an instrument confidently describing a vault it did not look
// at. `make live-canary` runs it uncached; `make test` invokes that target.
func TestBootstrapLiveVaultStillRestoresASession(t *testing.T) {
	// 🔴 EXACTLY ONE SKIP IS LEGITIMATE: this host has no vault at all (CI, a
	// fresh clone). Every OTHER way of not measuring is a FAILURE, deliberately.
	// `go test` prints a bare `ok` for a package whose tests all SKIPPED, so a
	// skip is visually indistinguishable from a pass — a canary that quietly
	// declines to measure is worth less than no canary, because it also supplies
	// false confidence.
	root := os.Getenv("VP_LIVE_VAULT")
	explicit := root != ""
	if root == "" {
		v, err := storage.OpenVaultGlobal()
		if err != nil {
			t.Skipf("no vault configured on this host — the one legitimate skip: %v", err)
		}
		root = v.Root
	}
	origin := "global config"
	if explicit {
		origin = "VP_LIVE_VAULT"
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("this host claims a vault at %q (via %s) but it is not present: %v — "+
			"the canary cannot measure, and skipping here would report a green gate that never ran",
			root, origin, err)
	}
	project := os.Getenv("VP_LIVE_PROJECT")
	if project == "" {
		project = "vibe-palace"
	}
	vault := storage.NewVault(root)
	resolver := vpctx.NewResolver(root)
	dir, err := vault.ProjectDir(project)
	if err != nil {
		t.Fatalf("project %q not resolvable in the vault at %q (via %s): %v — "+
			"set VP_LIVE_PROJECT if this vault uses a different slug", project, root, origin, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "resume.md")); err != nil {
		t.Fatalf("project %q has no resume.md in the vault at %q: %v — "+
			"there is nothing to measure, so this is a broken setup, not a skip", project, root, err)
	}

	br := AssembleBootstrap(resolver, vault, project, "", "")
	raw, err := json.Marshal(br)
	if err != nil {
		t.Fatalf("marshal live payload: %v", err)
	}
	wire := string(raw)

	// 1. The recovery handles. A payload whose bulk a host cuts is still usable
	//    if these survive, and they lead the payload for that reason.
	if br.ResumeURI == "" {
		t.Error("resume_uri is empty — an agent whose host cut the body has no way back to it")
	}
	if br.WorkflowURI == "" {
		t.Error("workflow_uri is empty — same hole, for the project's own rules")
	}

	// 2. The bodies arrive. Phase 2 deleted every path that could reduce them,
	//    so an empty one here is a real regression in assembly, not a shed.
	if strings.TrimSpace(br.Resume) == "" {
		t.Error("resume body is empty on a project that has a resume.md — bootstrap assembled nothing")
	}
	if strings.TrimSpace(br.Workflow) == "" {
		t.Error("workflow body is empty — the project's rules did not reach the payload")
	}

	// 3. No budget field, in the payload or on the wire. This is the deletion
	//    gate: if a budget ever returns, it returns here first, on the live vault.
	if strings.Contains(wire, `"budget"`) {
		t.Error(`the wire carries a "budget" field — the payload budget was deleted in Phase 2 ` +
			`and must not grow back; there is no ceiling on a session-start payload (PRD §1.10)`)
	}
	for _, gone := range []string{`"shed_core"`, `"max_tokens"`, "⚠ pinned sections only"} {
		if strings.Contains(wire, gone) {
			t.Errorf("the wire carries %s — a deleted rationing artifact reappeared on the live vault", gone)
		}
	}

	// 4. The sentinel, and its position. `complete` is what lets an agent tell a
	//    whole payload from a host-cut one, and it only works LAST.
	if !br.Complete {
		t.Error("complete is false on a successful assembly — the sentinel's only writer is the success path")
	}
	const sentinel = `,"complete":true}`
	if !strings.HasSuffix(wire, sentinel) {
		tail := wire
		if len(tail) > 120 {
			tail = tail[len(tail)-120:]
		}
		t.Errorf("the live payload does not END with %s — a field was declared after it, so a host cut "+
			"can now remove the sentinel while leaving the payload looking whole. Tail: %q", sentinel, tail)
	}

	t.Logf("live payload: %d bytes, resume %d B, workflow %d B, %d open task(s) — delivered whole, no budget",
		len(raw), len(br.Resume), len(br.Workflow), br.ActiveTaskCount)
}
