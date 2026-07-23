// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// writeWorkflow creates <vault>/Projects/<slug>/workflow.md with the given body.
func writeWorkflow(t *testing.T, vaultRoot, slug, body string) {
	t.Helper()
	dir := filepath.Join(vaultRoot, "Projects", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workflow.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
}

// workflowBody renders a syntactically plausible workflow.md of exactly n bytes.
func workflowBody(t *testing.T, n int) string {
	t.Helper()
	head := "# proj — Workflow\n\n## Project-Specific Patterns\n\n"
	if n < len(head) {
		t.Fatalf("workflowBody: n=%d smaller than the %d-byte header", n, len(head))
	}
	return head + strings.Repeat("x", n-len(head))
}

// TestCheckWorkflowCapsNoProjectsDir verifies a vault without Projects/ passes
// quietly — absence is never a violation.
func TestCheckWorkflowCapsNoProjectsDir(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	r := CheckWorkflowCaps(v)
	if r.Status != Pass {
		t.Errorf("status = %v, want Pass", r.Status)
	}
	if r.Summary != "no Projects/ directory" {
		t.Errorf("summary = %q", r.Summary)
	}
	if len(r.Details) != 0 {
		t.Errorf("healthy state must be silent, got details %v", r.Details)
	}
}

// TestCheckWorkflowCapsMissingFile verifies a project directory with no
// workflow.md reports nothing — not a violation, not even a scanned file.
func TestCheckWorkflowCapsMissingFile(t *testing.T) {
	vaultRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vaultRoot, "Projects", "bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := CheckWorkflowCaps(storage.NewVault(vaultRoot))
	if r.Status != Pass {
		t.Errorf("status = %v, want Pass", r.Status)
	}
	if r.Summary != "0 workflow.md within cap" {
		t.Errorf("summary = %q", r.Summary)
	}
	if len(r.Details) != 0 {
		t.Errorf("healthy state must be silent, got details %v", r.Details)
	}
}

// TestCheckWorkflowCapsUnderCap verifies a workflow within the cap is SILENT:
// Pass, no details, no floor sizes, no numbers.
func TestCheckWorkflowCapsUnderCap(t *testing.T) {
	vaultRoot := t.TempDir()
	writeWorkflow(t, vaultRoot, "thin", workflowBody(t, WorkflowMaxBytes/2))
	r := CheckWorkflowCaps(storage.NewVault(vaultRoot))
	if r.Status != Pass {
		t.Errorf("status = %v, want Pass", r.Status)
	}
	if r.Summary != "1 workflow.md within cap" {
		t.Errorf("summary = %q", r.Summary)
	}
	if len(r.Details) != 0 {
		t.Errorf("healthy state must be silent, got details %v", r.Details)
	}
}

// TestCheckWorkflowCapsAtCapBoundary verifies a workflow of EXACTLY
// WorkflowMaxBytes passes: the shed ladder's rung fires only when the body
// EXCEEDS bootstrapExcerptCap, so at the bound the contract still ships whole
// and the advisory mirrors that strictly-greater comparison.
func TestCheckWorkflowCapsAtCapBoundary(t *testing.T) {
	vaultRoot := t.TempDir()
	writeWorkflow(t, vaultRoot, "edge", workflowBody(t, WorkflowMaxBytes))
	r := CheckWorkflowCaps(storage.NewVault(vaultRoot))
	if r.Status != Pass {
		t.Errorf("status = %v, want Pass at exactly the cap", r.Status)
	}
	if len(r.Details) != 0 {
		t.Errorf("healthy state must be silent, got details %v", r.Details)
	}
}

// TestCheckWorkflowCapsOverCap verifies the advisory fires — Info, never Fail —
// naming the project, its size, the cap, and the embedded ADR-008 floors.
func TestCheckWorkflowCapsOverCap(t *testing.T) {
	vaultRoot := t.TempDir()
	writeWorkflow(t, vaultRoot, "fatproj", workflowBody(t, WorkflowMaxBytes+1))
	r := CheckWorkflowCaps(storage.NewVault(vaultRoot))
	if r.Status != Info {
		t.Fatalf("status = %v, want Info (advisory, never Fail)", r.Status)
	}
	if r.Summary != "1 of 1 workflow.md over cap" {
		t.Errorf("summary = %q", r.Summary)
	}
	joined := strings.Join(r.Details, "\n")
	for _, want := range []string{"fatproj:", "cap 4000 bytes", "vp_get_doctrine", "Embedded floors:"} {
		if !strings.Contains(joined, want) {
			t.Errorf("details missing %q:\n%s", want, joined)
		}
	}
}

// TestCheckWorkflowCapsMixedAndSorted verifies only the over-cap projects are
// reported, in sorted order, while under-cap and hidden/underscore entries
// stay out of the report.
func TestCheckWorkflowCapsMixedAndSorted(t *testing.T) {
	vaultRoot := t.TempDir()
	writeWorkflow(t, vaultRoot, "zeta", workflowBody(t, WorkflowMaxBytes*3))
	writeWorkflow(t, vaultRoot, "alpha", workflowBody(t, WorkflowMaxBytes*2))
	writeWorkflow(t, vaultRoot, "smallproj", workflowBody(t, 200))
	writeWorkflow(t, vaultRoot, ".hidden", workflowBody(t, WorkflowMaxBytes*2))
	writeWorkflow(t, vaultRoot, "_shared", workflowBody(t, WorkflowMaxBytes*2))

	r := CheckWorkflowCaps(storage.NewVault(vaultRoot))
	if r.Status != Info {
		t.Fatalf("status = %v, want Info", r.Status)
	}
	if r.Summary != "2 of 3 workflow.md over cap" {
		t.Errorf("summary = %q", r.Summary)
	}
	joined := strings.Join(r.Details, "\n")
	for _, skip := range []string{"smallproj", ".hidden", "_shared"} {
		if strings.Contains(joined, skip) {
			t.Errorf("details must not mention %q:\n%s", skip, joined)
		}
	}
	alphaAt := strings.Index(joined, "alpha:")
	zetaAt := strings.Index(joined, "zeta:")
	if alphaAt < 0 || zetaAt < 0 || alphaAt > zetaAt {
		t.Errorf("violations not sorted by project (alpha@%d, zeta@%d):\n%s", alphaAt, zetaAt, joined)
	}
}

// TestEmbeddedWorkflowFloors verifies the ADR-008 floors are measurable and
// that the thin embedded workflow scaffold itself fits under the cap — a new
// project must never start life already over the advisory line.
func TestEmbeddedWorkflowFloors(t *testing.T) {
	wf, doc, ok := embeddedWorkflowFloors()
	if !ok {
		t.Fatal("embeddedWorkflowFloors: embedded workflow.md/doctrine.md not found")
	}
	if wf <= 0 || doc <= 0 {
		t.Fatalf("floors: workflow=%d doctrine=%d, want both positive", wf, doc)
	}
	if wf > WorkflowMaxBytes {
		t.Errorf("embedded thin workflow scaffold is %d bytes — over the %d-byte cap; a fresh project would bootstrap already over the advisory line", wf, WorkflowMaxBytes)
	}
}
