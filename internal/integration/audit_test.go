// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

func TestIntegrationAuditDetectsMismatches(t *testing.T) {
	h := newHarness(t, false)

	// kubernetes content in wrong room (api instead of devops).
	h.seedProject(t, "proj")
	h.Seed(t,
		testinfra.WithDrawer("proj", "proj", "api",
			"Set up the kubernetes cluster for deployment.",
			"facts", "2026-04-10T10:00:00Z"),

		// Correctly classified content.
		testinfra.WithDrawer("proj", "proj", "data",
			"Run the SQL migration on the production database.",
			"facts", "2026-04-10T10:00:00Z"),
	)

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

// Before the migration marker, `vp audit rooms --apply` refuses and writes
// nothing: room relabelling is a host-local-store write available only after
// the migration (palace.Relabel). RunAudit still finds the candidate through
// the tracked-drawer fallback. The post-marker relabel over the store is
// covered by the palace-package unit tests (store_reader_test.go).
func TestIntegrationAuditApplyRefusesBeforeMarker(t *testing.T) {
	h := newHarness(t, false)

	// Misclassified: kubernetes content in api room, tracked (no marker).
	h.seedProject(t, "proj")
	h.Seed(t, testinfra.WithDrawer("proj", "proj", "api",
		"Set up the kubernetes cluster for deployment.",
		"facts", "2026-04-10T10:00:00Z"))

	rc := buildClassifier(h.Config)
	report, err := palace.RunAudit(h.Vault, rc, palace.AuditOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}

	candidates := report.Candidates()
	if len(candidates) != 1 {
		t.Fatalf("Candidates = %d, want 1", len(candidates))
	}

	// Apply before the marker refuses, and writes nothing.
	err = palace.Relabel(context.Background(), h.Vault, "proj", candidates, 5*time.Second)
	if !errors.Is(err, palace.ErrRelabelBeforeMarker) {
		t.Fatalf("Relabel before marker = %v, want ErrRelabelBeforeMarker", err)
	}

	// The tracked drawers are untouched: nothing moved.
	apiDrawers, _ := h.Vault.ListDrawers("proj", "proj", "api")
	if len(apiDrawers) != 1 {
		t.Errorf("api room should still hold the drawer, got %d", len(apiDrawers))
	}
	devopsDrawers, _ := h.Vault.ListDrawers("proj", "proj", "devops")
	if len(devopsDrawers) != 0 {
		t.Errorf("devops room should be empty (nothing relabelled), got %d", len(devopsDrawers))
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
	h.seedProject(t, "proj")
	h.Seed(t, testinfra.WithDrawer("proj", "proj", "general",
		"train the neural network transformer model",
		"facts", "2026-04-10T10:00:00Z"))

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

	h.seedProject(t, "proj")
	h.Seed(t, testinfra.WithDrawer("proj", "proj", "devops",
		"deploy the kubernetes cluster using terraform and ansible",
		"facts", "2026-04-10T10:00:00Z"))

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
