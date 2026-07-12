// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// resumeCASFixture is the seeded resume.md this test contends on.
//
// It deliberately carries a LIVE {{DATE}} placeholder token. vp_get_resume and
// vp_bootstrap_context serve placeholder-EXPANDED content (expandScoped
// substitutes {{PROJECT}}/{{DATE}}) while computing their sha256 over the RAW,
// UNEXPANDED bytes (internal/context/precedence.go). So a body composed from
// vp_get_resume round-trips its sha — it PASSES the compare-and-set — while
// silently baking today's date onto disk and destroying the token. The token is
// here so the test can prove that distinction bites, and so the test itself is
// forced to source its round-trip body from vp_vault_read (raw bytes, sha over
// those same bytes), which is what the tool description tells agents to do.
const resumeCASFixture = `# Resume: demo

Last updated: {{DATE}}

## Open Threads

### alpha

alpha body

## Project History

history
`

// TestIntegration_UpdateResumeStaleWriteRefused is the full-stack acceptance test
// for the compare-and-set guard on vp_update_resume — the last blind whole-file
// resume writer in the repo.
//
// WHY IT EXISTS: iteration 179 (commit 577c989) closed the resume.md stale-read
// hole. Before it, an agent could call vp_get_resume, compose a full-body rewrite
// from what it read, and — while it was thinking — have a concurrent editor land
// a change to the same file. The stale whole-body rewrite was then ACCEPTED and
// silently reverted that change. vp_update_resume now requires expected_sha256
// and REFUSES a write whose expectation no longer matches the on-disk sha,
// returning a machine-parseable conflict payload carrying the current sha.
//
// This drives the refusal through the PRODUCTION MCP WIRING — tools.RegisterAll
// on a real mcp.Server, a real JSON-RPC tools/call message, the registry's schema
// validation and mutating-write gate, then the handler — because that is the path
// an agent actually takes. The storage layer is already covered by
// TestWriteResumeCAS / TestWriteResumeRefusesStaleRead; those would still pass if
// the tool dropped expected_sha256 from its schema, mapped the conflict to a
// SUCCESS result, or lost the error on the way back through dispatch. This test
// is what makes those failure modes visible.
//
// WHY THE RACER CHANGED: the original version of this test (in the now-deleted
// internal/integration/resume_editors_concurrent_test.go) used vp_thread_insert
// as the racing writer. That tool — along with the rest of the vp_thread_* /
// vp_carried_* surgical editors — has been removed, so the racer is now
// vp_vault_edit against Projects/<project>/resume.md. It is not an arbitrary
// substitution: vp_vault_edit is the tool the vp_update_resume description now
// points agents at for section-level edits, it takes the SAME per-path vaultlock
// on resume.md, and it is itself CAS-capable — so it is the writer that will
// realistically be racing a whole-file rewrite in production. (A second
// vp_update_resume would also race, but it would only prove CAS against itself;
// vp_vault_edit proves the guard holds against the OTHER writer on the same
// lock, which is the case that actually bit.)
//
// NON-VACUITY: before asserting the refusal, the test asserts the composed stale
// body genuinely does NOT contain writer B's edit — i.e. accepting it WOULD have
// clobbered it. Without that, a passing test could be proving nothing.
func TestIntegration_UpdateResumeStaleWriteRefused(t *testing.T) {
	const (
		project    = "demo"
		resumePath = "Projects/demo/resume.md"
	)

	h := newHarness(t, false)
	h.registerAllTools(t)
	h.initMCP(t)

	if err := h.Vault.WriteResume(project, resumeCASFixture, ""); err != nil {
		t.Fatalf("seed resume.md: %v", err)
	}
	resumeFile, err := h.Vault.ResumeFile(project)
	if err != nil {
		t.Fatalf("ResumeFile: %v", err)
	}

	// ---------------------------------------------------------------------
	// Writer A reads. It sources the body+sha pair from vp_vault_read, which
	// returns RAW bytes and a sha over those same bytes — the only pair that
	// round-trips.
	// ---------------------------------------------------------------------
	var readA struct {
		Content string `json:"content"`
		Sha256  string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(h.callTool(t, "vp_vault_read", map[string]any{
		"path": resumePath,
	})), &readA); err != nil {
		t.Fatalf("parse vp_vault_read result: %v", err)
	}
	if readA.Sha256 == "" {
		t.Fatal("vp_vault_read returned an empty sha for a seeded resume")
	}
	if !strings.Contains(readA.Content, "{{DATE}}") {
		t.Fatalf("vp_vault_read must serve RAW bytes; the {{DATE}} token is missing:\n%s", readA.Content)
	}

	// The trap, pinned: vp_get_resume serves EXPANDED content under the SAME sha
	// as the raw bytes. Its body therefore passes compare-and-set while destroying
	// the live token — which is exactly why writer A above read via vp_vault_read.
	var readGet struct {
		Content string `json:"content"`
		Sha256  string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(h.callTool(t, "vp_get_resume", map[string]any{
		"project": project,
	})), &readGet); err != nil {
		t.Fatalf("parse vp_get_resume result: %v", err)
	}
	if strings.Contains(readGet.Content, "{{DATE}}") {
		t.Error("vp_get_resume is expected to serve placeholder-EXPANDED content, but the {{DATE}} token survived; if that changed, this test's warning comment is stale")
	}
	if readGet.Sha256 != readA.Sha256 {
		t.Errorf("vp_get_resume's sha (%s) is expected to be computed over the RAW bytes and match vp_vault_read's (%s); if that changed, this test's warning comment is stale", readGet.Sha256, readA.Sha256)
	}

	// ---------------------------------------------------------------------
	// Writer B lands an edit on the same file, through the same production
	// wiring and the same per-path vaultlock. Writer A's read is now STALE.
	// ---------------------------------------------------------------------
	const landedMarker = "### landed-by-vault-edit"
	var editB struct {
		Sha256 string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(h.callTool(t, "vp_vault_edit", map[string]any{
		"path":            resumePath,
		"old_string":      "## Project History",
		"new_string":      landedMarker + "\n\nthis edit must survive the stale rewrite\n\n## Project History",
		"expected_sha256": readA.Sha256, // B is NOT stale; its CAS must pass
	})), &editB); err != nil {
		t.Fatalf("parse vp_vault_edit result: %v", err)
	}
	if editB.Sha256 == "" || editB.Sha256 == readA.Sha256 {
		t.Fatalf("vp_vault_edit did not move the sha (before=%s after=%s)", readA.Sha256, editB.Sha256)
	}

	// ---------------------------------------------------------------------
	// Writer A submits a whole-body rewrite composed from its now-stale read.
	// It MUST be refused.
	// ---------------------------------------------------------------------
	const staleMarker = "### composed-from-stale-read"
	staleBody := readA.Content + "\n" + staleMarker + "\n\nwriter A's rewrite\n"
	if strings.Contains(staleBody, landedMarker) {
		t.Fatal("non-vacuity check failed: the stale body already contains writer B's edit, so accepting it could not clobber anything")
	}

	text, isErr := h.callToolRaw(t, "vp_update_resume", map[string]any{
		"project":         project,
		"content":         staleBody,
		"expected_sha256": readA.Sha256, // the STALE sha
	})
	if !isErr {
		t.Fatalf("the stale vp_update_resume SUCCEEDED; writer B's edit was silently reverted. result: %s", text)
	}

	// The refusal must be the structured, machine-parseable conflict payload —
	// not prose an agent has to regex — and it must carry the CURRENT sha so the
	// retry is mechanical.
	if !strings.Contains(text, `"conflict":true`) {
		t.Errorf("conflict result text %q is not the machine-parseable conflict payload", text)
	}
	if !strings.Contains(text, `"current_sha256":"`+editB.Sha256+`"`) {
		t.Errorf("conflict payload %q does not carry writer B's current sha %q", text, editB.Sha256)
	}
	if !strings.Contains(text, `"expected_sha256":"`+readA.Sha256+`"`) {
		t.Errorf("conflict payload %q does not echo the stale expected sha %q", text, readA.Sha256)
	}

	// ---------------------------------------------------------------------
	// On disk: B survived intact, A did not land, and the live token is intact.
	// ---------------------------------------------------------------------
	data, err := os.ReadFile(resumeFile)
	if err != nil {
		t.Fatalf("read resume.md: %v", err)
	}
	final := string(data)
	if !strings.Contains(final, landedMarker) {
		t.Errorf("writer B's edit was clobbered by the refused stale rewrite:\n%s", final)
	}
	if strings.Contains(final, staleMarker) {
		t.Errorf("the refused rewrite landed anyway:\n%s", final)
	}
	if !strings.Contains(final, "{{DATE}}") {
		t.Errorf("the live {{DATE}} token was baked out on disk:\n%s", final)
	}

	// ---------------------------------------------------------------------
	// The remedy the payload prescribes actually works: re-read, recompose
	// against the CURRENT body, resubmit.
	// ---------------------------------------------------------------------
	var readA2 struct {
		Content string `json:"content"`
		Sha256  string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(h.callTool(t, "vp_vault_read", map[string]any{
		"path": resumePath,
	})), &readA2); err != nil {
		t.Fatalf("parse vp_vault_read (retry) result: %v", err)
	}
	if readA2.Sha256 != editB.Sha256 {
		t.Errorf("re-read sha %q does not match writer B's post-edit sha %q", readA2.Sha256, editB.Sha256)
	}

	const freshMarker = "### composed-from-fresh-read"
	h.callTool(t, "vp_update_resume", map[string]any{
		"project":         project,
		"content":         readA2.Content + "\n" + freshMarker + "\n\nwriter A's rewrite\n",
		"expected_sha256": readA2.Sha256,
	})

	data, err = os.ReadFile(resumeFile)
	if err != nil {
		t.Fatalf("read resume.md after retry: %v", err)
	}
	final = string(data)
	for _, want := range []string{landedMarker, freshMarker, "### alpha", "{{DATE}}"} {
		if !strings.Contains(final, want) {
			t.Errorf("after the recomposed retry, %q is missing:\n%s", want, final)
		}
	}
}
