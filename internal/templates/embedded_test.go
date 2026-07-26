// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

var hexSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestWalkEmbedded_MinimumCount(t *testing.T) {
	resources, err := WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded returned error: %v", err)
	}
	if len(resources) < 3 {
		t.Fatalf("expected >= 3 embedded resources, got %d", len(resources))
	}
}

func TestWalkEmbedded_ResourceInvariants(t *testing.T) {
	resources, err := WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded returned error: %v", err)
	}
	seen := make(map[string]bool)
	for _, r := range resources {
		if len(r.Bytes) == 0 {
			t.Errorf("resource %q has empty Bytes", r.RelPath)
		}
		if !hexSHA.MatchString(r.SHA256) {
			t.Errorf("resource %q SHA256 %q is not 64 hex chars", r.RelPath, r.SHA256)
		}
		sum := sha256.Sum256(r.Bytes)
		want := hex.EncodeToString(sum[:])
		if r.SHA256 != want {
			t.Errorf("resource %q SHA256 mismatch: got %s want %s", r.RelPath, r.SHA256, want)
		}
		if strings.HasPrefix(r.RelPath, "/") {
			t.Errorf("resource %q has leading slash", r.RelPath)
		}
		if strings.HasPrefix(r.RelPath, "templates/") {
			t.Errorf("resource %q still has templates/ prefix", r.RelPath)
		}
		if strings.Contains(r.RelPath, "..") {
			t.Errorf("resource %q contains ..", r.RelPath)
		}
		if !strings.HasSuffix(r.RelPath, ".md") {
			t.Errorf("resource %q does not end in .md", r.RelPath)
		}
		if seen[r.RelPath] {
			t.Errorf("resource %q appears more than once", r.RelPath)
		}
		seen[r.RelPath] = true
	}
}

func TestEmbeddedSHA_KnownAndUnknown(t *testing.T) {
	resources, err := WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded returned error: %v", err)
	}
	if len(resources) == 0 {
		t.Fatal("no embedded resources to pivot from")
	}
	known := resources[0]
	got, ok := EmbeddedSHA(known.RelPath)
	if !ok {
		t.Fatalf("EmbeddedSHA(%q) returned not-found for a known resource", known.RelPath)
	}
	if got != known.SHA256 {
		t.Errorf("EmbeddedSHA(%q) = %s, want %s", known.RelPath, got, known.SHA256)
	}

	got, ok = EmbeddedSHA("definitely/not/a/real/resource.md")
	if ok {
		t.Errorf("EmbeddedSHA for unknown path returned ok=true, got sha=%q", got)
	}
	if got != "" {
		t.Errorf("EmbeddedSHA for unknown path returned non-empty sha %q", got)
	}
}

// lookupThroughHook calls EmbeddedSHA indirectly so we exercise the
// package-level function-variable seam — an override installed by a
// test must be visible to arbitrary callers in the same process.
func lookupThroughHook(relPath string) (string, bool) {
	return EmbeddedSHA(relPath)
}

func TestEmbeddedSHA_OverrideHook(t *testing.T) {
	original := EmbeddedSHA
	defer func() { EmbeddedSHA = original }()

	const sentinel = "deadbeef"
	EmbeddedSHA = func(relPath string) (string, bool) {
		if relPath == "virtual/resource.md" {
			return sentinel, true
		}
		return "", false
	}

	got, ok := lookupThroughHook("virtual/resource.md")
	if !ok || got != sentinel {
		t.Fatalf("override not honored: got (%q, %v) want (%q, true)", got, ok, sentinel)
	}
	if _, ok := lookupThroughHook("commands/wrap.md"); ok {
		t.Fatalf("override should have hidden real resources")
	}

	// Restore via defer and confirm real lookups work again via a
	// secondary assertion inside this test (defer fires after).
	EmbeddedSHA = original
	if _, ok := lookupThroughHook("commands/wrap.md"); !ok {
		t.Fatalf("restored EmbeddedSHA failed to resolve a known resource")
	}
}

// TestEmbeddedCommands_SurfacePreflight is the regression guard for the
// mcp-surface-handshake Phase 4 preflight: both the restart and wrap command
// templates must instruct the executor to run the read-only surface preflight
// via the `vp_surface_check` MCP tool, and halt on a `"fail"` verdict before
// touching the vault. The preflight is now a host-agnostic MCP call (no Bash),
// so the remediation text — the upgrade command and the VP_SURFACE_GATE=warn
// override — lives in the tool's `details` output rather than the template
// prose; the templates only instruct the executor to surface those details. If
// a future template edit drops the preflight or reorders it after Step 2, this
// test catches it.
func TestEmbeddedCommands_SurfacePreflight(t *testing.T) {
	resources, err := WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded returned error: %v", err)
	}
	body := make(map[string]string)
	for _, r := range resources {
		body[r.RelPath] = string(r.Bytes)
	}

	// Phrases every gated command template must carry. Kept loose enough to
	// survive copy edits but strict enough to fail if the preflight vanishes.
	// VP_SURFACE_GATE=warn is intentionally absent: the tool now supplies the
	// remediation via its `details` output, so it is no longer template prose.
	wantPhrases := []string{
		"vp_surface_check", // the read-only MCP preflight tool
		"Surface",          // the preflight subsection heading
		`"fail"`,           // the status it must halt on
	}
	for _, rel := range []string{"commands/restart.md", "commands/wrap.md"} {
		content, ok := body[rel]
		if !ok {
			t.Fatalf("embedded resource %q missing", rel)
		}
		for _, phrase := range wantPhrases {
			if !strings.Contains(content, phrase) {
				t.Errorf("%s: missing surface-preflight phrase %q", rel, phrase)
			}
		}
		// The preflight must precede any vault write. Assert the tool call runs
		// before the first numbered step that mutates the vault: in restart
		// that is "## Step 2" (Bootstrap), in wrap "## Step 2" (Capture).
		preflightIdx := strings.Index(content, "vp_surface_check")
		step2Idx := strings.Index(content, "## Step 2")
		if preflightIdx < 0 || step2Idx < 0 || preflightIdx > step2Idx {
			t.Errorf("%s: surface preflight must appear before '## Step 2' "+
				"(preflightIdx=%d step2Idx=%d)", rel, preflightIdx, step2Idx)
		}
	}
}

// TestEmbeddedWrap_PlanHygiene guards the plan-hygiene additions to the wrap
// command template: a narrow "Sweep Orphaned Plans" reconcile step that prefers
// the read-only vp_scan_plans reporter (with a glob fallback for older
// binaries) and only deletes a scratch plan promoted THIS session, plus the
// resume guardrail bullet that forbids committing a host-local plan/commit.msg
// path into the synced resume.md. If a future edit drops either, this catches
// it. (Prose enforcement — ADR-006: the template asks the executor to honor
// these; the test pins that the ask is still present.)
func TestEmbeddedWrap_PlanHygiene(t *testing.T) {
	resources, err := WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded returned error: %v", err)
	}
	var wrap string
	for _, r := range resources {
		if r.RelPath == "commands/wrap.md" {
			wrap = string(r.Bytes)
			break
		}
	}
	if wrap == "" {
		t.Fatal("embedded resource commands/wrap.md missing or empty")
	}

	// Edit A — the Sweep Orphaned Plans step.
	sweepPhrases := []string{
		"Sweep Orphaned Plans",  // the step heading
		"vp_scan_plans",         // the preferred read-only reporter
		"~/.claude/plans/*.md",  // the glob fallback for an older binary
		"Promoted this session", // the narrow promote-and-delete rule
		"/restart",              // strays are restart's job, not wrap's
	}
	for _, phrase := range sweepPhrases {
		if !strings.Contains(wrap, phrase) {
			t.Errorf("wrap.md: missing sweep-step phrase %q", phrase)
		}
	}

	// Edit B — the resume guardrail bullet.
	guardPhrases := []string{
		"dangling by",                  // committed/synced resume can't point at host scratch
		"vp check --check resume-refs", // the new check that flags .claude/plans refs
	}
	for _, phrase := range guardPhrases {
		if !strings.Contains(wrap, phrase) {
			t.Errorf("wrap.md: missing resume-guardrail phrase %q", phrase)
		}
	}

	// The sweep step must land after Step 6 (Retire) and before Step 7
	// (commit.msg) so it reads as a late reconcile pass, not a session-start
	// promote-everything sweep.
	sweepIdx := strings.Index(wrap, "## Step 6b: Sweep Orphaned Plans")
	step6Idx := strings.Index(wrap, "## Step 6: Retire Tasks")
	step7Idx := strings.Index(wrap, "## Step 7: Update commit.msg")
	if sweepIdx < 0 || step6Idx < 0 || step7Idx < 0 ||
		!(step6Idx < sweepIdx && sweepIdx < step7Idx) {
		t.Errorf("wrap.md: sweep step must sit between Step 6 and Step 7 "+
			"(step6=%d sweep=%d step7=%d)", step6Idx, sweepIdx, step7Idx)
	}
}

// TestEmbeddedDoctrine_GenericAndHostNeutral pins the ADR-008 doctrine split:
// the embedded doctrine.md carries the generic manual (the sections extracted
// from the old fat workflow template) and stays host-agnostic — it names MCP
// tools and capability classes, never a host's context files, config paths, or
// filesystem layout.
func TestEmbeddedDoctrine_GenericAndHostNeutral(t *testing.T) {
	resources, err := WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded returned error: %v", err)
	}
	var doctrine string
	for _, r := range resources {
		if r.RelPath == "doctrine.md" {
			doctrine = string(r.Bytes)
			break
		}
	}
	if doctrine == "" {
		t.Fatal("embedded resource doctrine.md missing or empty")
	}

	// The generic sections the split moved out of workflow.md.
	for _, want := range []string{
		"Pair Programming",
		"Investigation-First",
		"Task Management",
		"Core Principles",
		"air-gapped",
		"vp_manage_task",
	} {
		if !strings.Contains(doctrine, want) {
			t.Errorf("doctrine.md: missing generic-doctrine phrase %q", want)
		}
	}

	// Host-neutrality: no host names, host context files, or host-local
	// config/scratch paths. The leak grep from the ADR-008 plan, pinned.
	lower := strings.ToLower(doctrine)
	for _, leak := range []string{
		"claude", // CLAUDE.md, .claude/, Claude Code
		"grok",
		"zed",
		"agents.md",
		"~/.config",
		"obsidian",
	} {
		if strings.Contains(lower, leak) {
			t.Errorf("doctrine.md: host-specific leak %q — the doctrine must be host-agnostic", leak)
		}
	}
}

// TestEmbeddedWorkflow_ThinWithDoctrineContract pins the other half of the
// ADR-008 split: the embedded workflow floor is THIN (project-specific shape
// only) and carries the minimal bootstrap-contract paragraph pointing a fresh
// host at the on-demand doctrine surface. It must stay small enough that the
// bootstrap shed ladder can never excerpt it (bootstrapExcerptCap in
// internal/tools is 4000 bytes) — that size, not prose, is what makes the
// contract paragraph structurally un-sheddable at the embedded floor.
func TestEmbeddedWorkflow_ThinWithDoctrineContract(t *testing.T) {
	resources, err := WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded returned error: %v", err)
	}
	body := make(map[string]string)
	for _, r := range resources {
		body[r.RelPath] = string(r.Bytes)
	}

	wf, ok := body["workflow.md"]
	if !ok || wf == "" {
		t.Fatal("embedded resource workflow.md missing or empty")
	}
	if !strings.Contains(wf, "vp_get_doctrine") {
		t.Error("workflow.md: missing the doctrine-fetch contract paragraph — a fresh host would never learn to fetch the doctrine")
	}
	if !strings.Contains(wf, "vp_read_resource") {
		t.Error("workflow.md: contract paragraph must name vp_read_resource for paging vibe-palace:// URIs")
	}
	if strings.Contains(wf, "Pair Programming") {
		t.Error("workflow.md: still carries generic doctrine — the split moved that to doctrine.md")
	}
	const excerptCap = 4000 // internal/tools bootstrapExcerptCap
	if len(wf) >= excerptCap {
		t.Errorf("workflow.md is %d bytes, must stay under the %d-byte excerpt cap so the contract paragraph cannot be shed", len(wf), excerptCap)
	}

	// Ship-together gate (rollout ordering, iter-179 class): the embedded
	// command templates that open a session or lean on the doctrine must point
	// at the doctrine surface in the same corpus that ships it.
	for _, rel := range []string{
		"commands/restart.md",
		"commands/review-plan.md",
		"commands/cancel-plan.md",
	} {
		content, ok := body[rel]
		if !ok {
			t.Fatalf("embedded resource %q missing", rel)
		}
		if !strings.Contains(content, "vp_get_doctrine") {
			t.Errorf("%s: does not point at the vp_get_doctrine surface", rel)
		}
	}
}

func TestFS_ContainsTemplatesRoot(t *testing.T) {
	fsys := FS()
	// Reading through the exported FS should locate at least one
	// known embedded file under the templates/ root.
	entries, err := fsys.ReadDir("templates")
	if err != nil {
		t.Fatalf("ReadDir(templates): %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("templates/ is empty in exported FS")
	}
}
