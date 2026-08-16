// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// writeTemplateMirror materializes one vault-side template copy with the given
// bytes, creating parent directories. relPath is embedded-relative
// ("commands/wrap.md"), so the vault key is "Templates/" + relPath.
func writeTemplateMirror(t *testing.T, vaultRoot, relPath, body string) {
	t.Helper()
	target := filepath.Join(vaultRoot, "Templates", filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatalf("write mirror %s: %v", relPath, err)
	}
}

// anEmbeddedTemplate returns the RelPath of some embedded resource, so the
// tests below name a real corpus member without hard-coding one that a later
// rename could quietly turn into a no-op.
func anEmbeddedTemplate(t *testing.T) string {
	t.Helper()
	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatalf("walk embedded: %v", err)
	}
	if len(resources) == 0 {
		t.Fatal("embedded corpus is empty")
	}
	return resources[0].RelPath
}

// TestCheckTemplateDriftSkipsWithoutVault covers the registry's degradation
// contract: every vault-scoped producer answers an empty root with Skip rather
// than resolving one from the process cwd.
func TestCheckTemplateDriftSkipsWithoutVault(t *testing.T) {
	got := CheckTemplateDrift("")
	if got.Status != Skip {
		t.Errorf("empty vault root: status = %v, want Skip (%+v)", got.Status, got)
	}
}

// TestCheckTemplateDriftCleanVaultPasses pins the override-only model's healthy
// state: NO vault mirror at all. A vault that has never been materialized is in
// sync, because the embedded floor serves every template.
func TestCheckTemplateDriftCleanVaultPasses(t *testing.T) {
	vault := t.TempDir()
	got := CheckTemplateDrift(vault)
	if got.Status != Pass {
		t.Errorf("bare vault: status = %v, want Pass (%+v)", got.Status, got)
	}
	if len(got.Details) != 0 {
		t.Errorf("bare vault: Details = %v, want none — a clean check must be "+
			"silent, or the remedy text trains the reader to skim it", got.Details)
	}
}

// TestCheckTemplateDriftReportsDivergedMirror is the branch that matters, and it
// is written to FAIL if the producer stops detecting drift.
//
// A mirror whose bytes differ from the embedded corpus, with no lock entry, is
// the rollout-ordering hazard this check replaced a prose paragraph with: the
// vault serves a template the binary did not ship, so a command can be handed
// arguments the binary no longer accepts.
func TestCheckTemplateDriftReportsDivergedMirror(t *testing.T) {
	vault := t.TempDir()
	rel := anEmbeddedTemplate(t)

	// Prove the negative first: with no mirror, this same vault passes. Without
	// this, a producer that returned Info unconditionally would look correct.
	if before := CheckTemplateDrift(vault); before.Status != Pass {
		t.Fatalf("precondition: bare vault should Pass, got %v (%+v)", before.Status, before)
	}

	writeTemplateMirror(t, vault, rel, "this is not the embedded body\n")

	got := CheckTemplateDrift(vault)
	if got.Status != Info {
		t.Fatalf("diverged mirror: status = %v, want Info (%+v)", got.Status, got)
	}
	key := "Templates/" + rel
	var named bool
	for _, d := range got.Details {
		if strings.Contains(d, key) {
			named = true
			break
		}
	}
	if !named {
		t.Errorf("diverged mirror: Details never name %s — an aggregate that "+
			"reports a count without the path is not actionable\nDetails: %v",
			key, got.Details)
	}
	if !strings.Contains(got.Summary, "1 of ") {
		t.Errorf("diverged mirror: Summary = %q, want it to report 1 drifting "+
			"template of the corpus total", got.Summary)
	}
}

// TestTemplateDriftRowsMatchAggregate pins that the two shapes cannot disagree.
// The CLI prints the per-resource rows and the MCP surface prints the aggregate;
// they are one classification, and a reader comparing the two outputs must not
// see a drift row in one and a clean verdict in the other.
func TestTemplateDriftRowsMatchAggregate(t *testing.T) {
	vault := t.TempDir()
	rel := anEmbeddedTemplate(t)
	writeTemplateMirror(t, vault, rel, "diverged\n")

	rows := TemplateDriftRows(vault, "Templates", "Templates")
	var rowDrift int
	for _, r := range rows {
		if r.Status == Info {
			rowDrift++
		}
	}

	agg := CheckTemplateDrift(vault)
	if rowDrift == 0 {
		t.Fatal("per-resource rows report no drift for a diverged mirror")
	}
	if agg.Status != Info {
		t.Fatalf("rows report %d drifting, aggregate says %v", rowDrift, agg.Status)
	}
	if len(agg.Details) < rowDrift {
		t.Errorf("aggregate Details carry %d lines for %d drifting rows — the "+
			"aggregate is dropping findings", len(agg.Details), rowDrift)
	}
}

// TestTemplateDriftProducerReachesRegistry covers the selector path end to end.
// The classification existing is not the point of this change; being reachable
// by an MCP caller is, and only the registry proves that.
func TestTemplateDriftProducerReachesRegistry(t *testing.T) {
	vault := t.TempDir()
	writeTemplateMirror(t, vault, anEmbeddedTemplate(t), "diverged\n")

	results, err := RunSelected(vault, "template-drift")
	if err != nil {
		t.Fatalf("RunSelected: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want exactly one aggregate row, got %d: %v", len(results), names(results))
	}
	if results[0].Name != "Template drift" {
		t.Errorf("row name = %q, want %q — the MCP envelope keys on it",
			results[0].Name, "Template drift")
	}
	if results[0].Status != Info {
		t.Errorf("status = %v, want Info for a diverged mirror", results[0].Status)
	}
}
