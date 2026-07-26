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

// writeCore creates <vault>/Projects/<slug>/{resume.md,workflow.md} at the given
// sizes. A size of -1 omits that file entirely, so a test can exercise a project
// carrying only one half of the core.
func writeCore(t *testing.T, vaultRoot, slug string, resumeBytes, workflowBytes int) {
	t.Helper()
	dir := filepath.Join(vaultRoot, "Projects", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	write := func(name, head string, n int) {
		if n < 0 {
			return
		}
		if n < len(head) {
			t.Fatalf("%s: n=%d smaller than the %d-byte header", name, n, len(head))
		}
		body := head + strings.Repeat("x", n-len(head))
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("resume.md", "# proj — Working Context\n\n", resumeBytes)
	write("workflow.md", "# proj — Workflow\n\n", workflowBytes)
}

// TestCheckCoreFloorNoProjectsDir verifies a vault without Projects/ passes
// quietly — absence is never a violation.
func TestCheckCoreFloorNoProjectsDir(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	r := CheckCoreFloor(v)
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

// TestCheckCoreFloorMissingBothFiles verifies a project directory carrying
// neither core artifact reports nothing — not a violation, not even scanned.
func TestCheckCoreFloorMissingBothFiles(t *testing.T) {
	vaultRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vaultRoot, "Projects", "bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := CheckCoreFloor(storage.NewVault(vaultRoot))
	if r.Status != Pass {
		t.Errorf("status = %v, want Pass", r.Status)
	}
	if r.Summary != "0 core within cap" {
		t.Errorf("summary = %q", r.Summary)
	}
	if len(r.Details) != 0 {
		t.Errorf("healthy state must be silent, got details %v", r.Details)
	}
}

// TestCheckCoreFloorHalfCorePresent verifies a project with only one of the two
// artifacts is still MEASURED — a project can blow the floor on resume.md alone,
// and skipping it because workflow.md is absent would hide exactly that case.
func TestCheckCoreFloorHalfCorePresent(t *testing.T) {
	vaultRoot := t.TempDir()
	writeCore(t, vaultRoot, "resumeonly", CoreMaxBytes+1, -1)
	r := CheckCoreFloor(storage.NewVault(vaultRoot))
	if r.Status != Info {
		t.Fatalf("status = %v, want Info — a resume alone can exceed the floor", r.Status)
	}
	if !strings.Contains(strings.Join(r.Details, "\n"), "resumeonly:") {
		t.Errorf("details must name the project:\n%v", r.Details)
	}
}

// TestCheckCoreFloorUnderCap verifies a core within the cap is SILENT: Pass, no
// details, no numbers.
func TestCheckCoreFloorUnderCap(t *testing.T) {
	vaultRoot := t.TempDir()
	writeCore(t, vaultRoot, "thin", CoreMaxBytes/4, CoreMaxBytes/4)
	r := CheckCoreFloor(storage.NewVault(vaultRoot))
	if r.Status != Pass {
		t.Errorf("status = %v, want Pass", r.Status)
	}
	if r.Summary != "1 core within cap" {
		t.Errorf("summary = %q", r.Summary)
	}
	if len(r.Details) != 0 {
		t.Errorf("healthy state must be silent, got details %v", r.Details)
	}
}

// TestCheckCoreFloorAtCapBoundary verifies a core of EXACTLY CoreMaxBytes
// passes: the comparison is strictly-greater, so the bound itself still fits.
func TestCheckCoreFloorAtCapBoundary(t *testing.T) {
	vaultRoot := t.TempDir()
	writeCore(t, vaultRoot, "edge", CoreMaxBytes/2, CoreMaxBytes-CoreMaxBytes/2)
	r := CheckCoreFloor(storage.NewVault(vaultRoot))
	if r.Status != Pass {
		t.Errorf("status = %v, want Pass at exactly the cap", r.Status)
	}
	if len(r.Details) != 0 {
		t.Errorf("healthy state must be silent, got details %v", r.Details)
	}
}

// TestCheckCoreFloorOverCapNamesBothHalves verifies the advisory fires — Info,
// never Fail — and breaks the total down into resume and workflow, so a reader
// can tell WHICH half to act on rather than guessing.
func TestCheckCoreFloorOverCapNamesBothHalves(t *testing.T) {
	vaultRoot := t.TempDir()
	writeCore(t, vaultRoot, "fatproj", CoreMaxBytes, 64)
	r := CheckCoreFloor(storage.NewVault(vaultRoot))
	if r.Status != Info {
		t.Fatalf("status = %v, want Info (advisory, never Fail)", r.Status)
	}
	if r.Summary != "1 of 1 core over cap" {
		t.Errorf("summary = %q", r.Summary)
	}
	joined := strings.Join(r.Details, "\n")
	for _, want := range []string{"fatproj:", "resume", "workflow", "ADR-009", "iterations.md"} {
		if !strings.Contains(joined, want) {
			t.Errorf("details missing %q:\n%s", want, joined)
		}
	}
	// The remedy must point AWAY from shrinking the contract — that inversion is
	// the mistake this check exists to prevent a future session from repeating.
	if !strings.Contains(joined, "contract sets the budget") {
		t.Errorf("details must steer away from shrinking the contract:\n%s", joined)
	}
}

// TestCheckCoreFloorMixedAndSorted verifies only over-cap projects are reported,
// in sorted order, with under-cap and hidden/underscore entries left out.
func TestCheckCoreFloorMixedAndSorted(t *testing.T) {
	vaultRoot := t.TempDir()
	writeCore(t, vaultRoot, "zeta", CoreMaxBytes, CoreMaxBytes)
	writeCore(t, vaultRoot, "alpha", CoreMaxBytes, CoreMaxBytes/2)
	writeCore(t, vaultRoot, "smallproj", 200, 200)
	writeCore(t, vaultRoot, ".hidden", CoreMaxBytes, CoreMaxBytes)
	writeCore(t, vaultRoot, "_shared", CoreMaxBytes, CoreMaxBytes)

	r := CheckCoreFloor(storage.NewVault(vaultRoot))
	if r.Status != Info {
		t.Fatalf("status = %v, want Info", r.Status)
	}
	if r.Summary != "2 of 3 core over cap" {
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
