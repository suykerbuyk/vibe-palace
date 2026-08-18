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

// TestBootstrapLiveVaultStillRestoresASession is the live gate on what a real
// restart receives: "a restart on the largest live project is actionable".
//
// 🔴 IT MEASURES DELIVERY, NOT SIZE. Its predecessor asserted that the payload
// fit a token budget and that no ADR-009 "core" rung was shed. Both subjects are
// gone — first-principles Phase 2 deleted the shed ladder, the budget, the tier
// vocabulary and the workflow digest — so the assertions went with them rather
// than being rewritten into a smaller ceiling. There is no ceiling here on
// purpose: re-introducing one would re-create the disease (PRD §1.10), and this
// canary must never become the place a budget grows back.
//
// 🔴 ITS BODY ASSERTION IS INVERTED FROM PHASE 2's, AND THAT IS THE POINT.
// Through Phase 2 this test asserted that the resume and workflow BODIES were
// present, which was that phase's gate: nothing may reduce them. Phase 3's gate
// is the opposite half — no document body is inlined unconditionally, because
// the payload is an index and the bodies are fetched through their handles (PRD
// §1.9, epic DoD item 3). The Phase 2 assertion was a gate, never a standing
// invariant, and it was replaced in the same commit that made it false rather
// than left to lie.
//
// What is left is the claim that still has a subject: an agent calling
// vp_bootstrap_context against the real vault gets an index it can act on,
// knows where every body it did not receive lives, and can tell whether the
// payload arrived whole.
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

	if br.ResumeSha256 == "" {
		t.Error("resume_sha256 is empty — a caller that pages the body back through resume_uri " +
			"cannot compare-and-set it against disk, which is the handle's other half")
	}

	// 2. NO DOCUMENT BODY, which is Phase 3's gate and the inverse of Phase 2's.
	//
	//    🔴 THE KEY CHECK ALONE WOULD PASS A RENAMED FIELD, so the content check
	//    is the one that matters: a distinctive line of the live resume must not
	//    appear ANYWHERE in the marshalled payload. That catches a body that came
	//    back under another name, folded into the directive, or quoted into a row.
	for _, key := range []string{`"resume":`, `"workflow":`} {
		if strings.Contains(wire, key) {
			t.Errorf("the wire carries %s — bootstrap is inlining a document body again. "+
				"The payload is an index: the bodies are reachable through resume_uri / workflow_uri "+
				"and restart.md fetches them (PRD §1.9, first-principles DoD item 3)", key)
		}
	}
	if line := distinctiveResumeLine(t, filepath.Join(dir, "resume.md")); strings.Contains(wire, line) {
		t.Errorf("a distinctive line of the live resume is on the wire, so a body reached the payload "+
			"under some other field: %q", line)
	}

	// 3. The index is actionable: head of queue, with a handle on every row.
	if br.ActiveTaskCount > 0 && len(br.HeadOfQueue) == 0 {
		t.Errorf("%d open task(s) and an EMPTY head_of_queue — the payload reports a backlog and then "+
			"says nothing about what to do next, which is the whole point of the index", br.ActiveTaskCount)
	}
	for i, row := range br.HeadOfQueue {
		if row.Slug == "" {
			t.Errorf("head_of_queue[%d] has no slug", i)
		}
		if row.URI == "" {
			t.Errorf("head_of_queue[%d] (%s) carries no uri — a row with no handle is a row whose body "+
				"an agent cannot reach", i, row.Slug)
		}
	}
	for i, s := range br.RecentSessions {
		if s.URI == "" {
			t.Errorf("recent_sessions[%d] (iteration %d) carries no uri — the summary was dropped from "+
				"the row, so without a handle the session is unreachable", i, s.Iteration)
		}
	}

	// 4. The ranking report, which is never silent: an ordered list that does not
	//    say what ordered it is indistinguishable from recency order.
	if br.Ranking == nil {
		t.Fatal("ranking is absent — the payload orders rows and does not say by what")
	}
	if br.Ranking.Ranker != rankerStructural {
		t.Errorf("ranking.ranker = %q, want %q — slice 1 of Phase 3 ships the deterministic ranker only",
			br.Ranking.Ranker, rankerStructural)
	}
	if br.Ranking.Returned != len(br.RecentSessions) {
		t.Errorf("ranking.returned = %d but %d session rows were emitted — the instrument does not describe "+
			"the payload it rides with", br.Ranking.Returned, len(br.RecentSessions))
	}
	if br.Ranking.Candidates < br.Ranking.Returned {
		t.Errorf("ranking.candidates (%d) < returned (%d) — the ranker cannot return more rows than it scored",
			br.Ranking.Candidates, br.Ranking.Returned)
	}

	// 5. No budget field, in the payload or on the wire. This is the deletion
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

	// 6. The sentinel, and its position. `complete` is what lets an agent tell a
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

	t.Logf("live payload: %d bytes — index only, no document body. head_of_queue %d of %d open task(s), "+
		"session index %d of %d candidate(s), ranker %q, head %q",
		len(raw), len(br.HeadOfQueue), br.ActiveTaskCount,
		br.Ranking.Returned, br.Ranking.Candidates, br.Ranking.Ranker, br.Ranking.RankedAgainst)
}

// distinctiveResumeLine returns the longest line of the live resume, which is
// what the body assertion above searches the payload for.
//
// 🔴 IT MUST FAIL, NOT SKIP, ON A RESUME WITH NOTHING DISTINCTIVE IN IT. A
// needle short enough to appear by chance would make the assertion above
// meaningless in the direction that matters — it would go green whether or not a
// body was inlined. A real project resume has a long line; one that does not is
// a broken premise, and this says so rather than measuring nothing (290, 296).
func distinctiveResumeLine(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the live resume at %q to build the body needle: %v", path, err)
	}
	longest := ""
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if len(line) > len(longest) {
			longest = line
		}
	}
	const minNeedle = 40
	if len(longest) < minNeedle {
		t.Fatalf("the live resume's longest line is %d B (%q), under the %d B needle floor — a needle that "+
			"short can match by accident, so the no-body assertion would prove nothing",
			len(longest), longest, minNeedle)
	}
	return longest
}
