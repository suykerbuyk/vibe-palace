// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestIntegrationAuditDetectsMismatches(t *testing.T) {
	h := newHarness(t, false)

	// kubernetes content in wrong room (api instead of devops).
	h.addDrawer(t, "proj", "proj", "api",
		"Set up the kubernetes cluster for deployment.",
		"facts", "2026-04-10")

	// Correctly classified content.
	h.addDrawer(t, "proj", "proj", "data",
		"Run the SQL migration on the production database.",
		"facts", "2026-04-10")

	rc := buildClassifier(h.Config)
	report, err := palace.RunAudit(h.Vault, rc, palace.AuditOptions{
		Project:  "proj",
		Keywords: h.Config.PalaceRoomKeywords,
	})
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}

	if report.TotalDrawers != 2 {
		t.Errorf("TotalDrawers = %d, want 2", report.TotalDrawers)
	}
	if len(report.Mismatches) != 1 {
		t.Fatalf("Mismatches = %d, want 1", len(report.Mismatches))
	}
	m := report.Mismatches[0]
	if m.CurrentRoom != "api" {
		t.Errorf("CurrentRoom = %q, want api", m.CurrentRoom)
	}
	if m.BestRoom != "devops" {
		t.Errorf("BestRoom = %q, want devops", m.BestRoom)
	}
}

func TestIntegrationAuditApplyFixes(t *testing.T) {
	h := newHarness(t, false)

	// Misclassified: kubernetes content in api room.
	h.addDrawer(t, "proj", "proj", "api",
		"Set up the kubernetes cluster for deployment.",
		"facts", "2026-04-10")

	rc := buildClassifier(h.Config)
	report, err := palace.RunAudit(h.Vault, rc, palace.AuditOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}

	candidates := report.Candidates()
	if len(candidates) != 1 {
		t.Fatalf("Candidates = %d, want 1", len(candidates))
	}

	// Apply the move.
	for _, c := range candidates {
		if err := h.Vault.MoveDrawer("proj", c.Wing, c.FromRoom, c.ToRoom, c.DrawerID); err != nil {
			t.Fatalf("MoveDrawer: %v", err)
		}
	}

	// Verify drawer moved.
	apiDrawers, _ := h.Vault.ListDrawers("proj", "proj", "api")
	if len(apiDrawers) != 0 {
		t.Errorf("api room should be empty after apply, got %d", len(apiDrawers))
	}
	devopsDrawers, _ := h.Vault.ListDrawers("proj", "proj", "devops")
	if len(devopsDrawers) != 1 {
		t.Errorf("devops room should have 1 drawer, got %d", len(devopsDrawers))
	}

	// Re-audit: should be zero mismatches.
	report2, err := palace.RunAudit(h.Vault, rc, palace.AuditOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("RunAudit (post-apply): %v", err)
	}
	if len(report2.Mismatches) != 0 {
		t.Errorf("Mismatches after apply = %d, want 0", len(report2.Mismatches))
	}
}

func TestIntegrationAuditWithScoringOverrides(t *testing.T) {
	h := newHarness(t, false, func(cfg *storage.Config) {
		cfg.PalaceScoringOverrides = map[string]storage.ScoringRoomOverride{
			"ml": {
				High:   []string{"neural network", "transformer"},
				Medium: []string{"training"},
			},
		}
	})

	// Content that matches the new "ml" room, placed in "general".
	h.addDrawer(t, "proj", "proj", "general",
		"train the neural network transformer model",
		"facts", "2026-04-10")

	rc := buildClassifier(h.Config)
	report, err := palace.RunAudit(h.Vault, rc, palace.AuditOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}

	if len(report.Mismatches) != 1 {
		t.Fatalf("Mismatches = %d, want 1", len(report.Mismatches))
	}
	if report.Mismatches[0].BestRoom != "ml" {
		t.Errorf("BestRoom = %q, want ml", report.Mismatches[0].BestRoom)
	}
}

func TestIntegrationAuditKeywordCoverage(t *testing.T) {
	h := newHarness(t, false)

	h.addDrawer(t, "proj", "proj", "devops",
		"deploy the kubernetes cluster using terraform and ansible",
		"facts", "2026-04-10")

	rc := buildClassifier(h.Config)
	report, err := palace.RunAudit(h.Vault, rc, palace.AuditOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}

	if len(report.Coverage) == 0 {
		t.Fatal("expected keyword coverage report")
	}

	var devopsCov *palace.RoomKeywordReport
	for i := range report.Coverage {
		if report.Coverage[i].Room == "devops" {
			devopsCov = &report.Coverage[i]
			break
		}
	}
	if devopsCov == nil {
		t.Fatal("expected devops in coverage")
	}

	firedSet := make(map[string]bool)
	for _, kw := range devopsCov.Fired {
		firedSet[kw] = true
	}
	for _, expected := range []string{"kubernetes", "terraform", "ansible", "deploy"} {
		if !firedSet[expected] {
			t.Errorf("expected %q in devops fired keywords", expected)
		}
	}
	if len(devopsCov.Dead) == 0 {
		t.Error("expected some dead keywords in devops")
	}
}

// buildClassifier creates a RoomClassifier from test config.
func buildClassifier(cfg storage.Config) *palace.RoomClassifier {
	return palace.BuildClassifierFromConfig(cfg)
}
