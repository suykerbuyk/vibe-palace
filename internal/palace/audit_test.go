// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func auditVault(t *testing.T) *storage.Vault {
	t.Helper()
	return storage.NewVault(t.TempDir())
}

func addDrawer(t *testing.T, v *storage.Vault, project, wing, room, content string) {
	t.Helper()
	d := storage.Drawer{
		Content:    content,
		Hall:       "facts",
		SourceType: "manual",
		FiledAt:    "2026-04-10T10:00:00Z",
	}
	if err := v.AppendDrawer(project, wing, room, d); err != nil {
		t.Fatalf("AppendDrawer(%s/%s/%s): %v", project, wing, room, err)
	}
}

func TestRunAudit_Empty(t *testing.T) {
	v := auditVault(t)
	rc := NewRoomClassifier(nil, 0)
	report, err := RunAudit(v, rc, AuditOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}
	if report.TotalDrawers != 0 {
		t.Errorf("TotalDrawers = %d, want 0", report.TotalDrawers)
	}
	if len(report.Mismatches) != 0 {
		t.Errorf("Mismatches = %d, want 0", len(report.Mismatches))
	}
	if len(report.Borderlines) != 0 {
		t.Errorf("Borderlines = %d, want 0", len(report.Borderlines))
	}
}

func TestRunAudit_AllCorrect(t *testing.T) {
	v := auditVault(t)
	// kubernetes → devops (high weight, will classify as devops)
	addDrawer(t, v, "proj", "wing", "devops", "Set up the kubernetes cluster for deployment.")
	// SQL migration → data (high weight)
	addDrawer(t, v, "proj", "wing", "data", "Run the SQL migration on the production database.")

	rc := NewRoomClassifier(nil, 0)
	report, err := RunAudit(v, rc, AuditOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}
	if report.TotalDrawers != 2 {
		t.Errorf("TotalDrawers = %d, want 2", report.TotalDrawers)
	}
	if len(report.Mismatches) != 0 {
		t.Errorf("Mismatches = %d, want 0", len(report.Mismatches))
	}
}

func TestRunAudit_DetectsMismatches(t *testing.T) {
	v := auditVault(t)
	// kubernetes content in wrong room (api instead of devops).
	addDrawer(t, v, "proj", "wing", "api", "Set up the kubernetes cluster for deployment.")
	// Correctly classified content.
	addDrawer(t, v, "proj", "wing", "data", "Run the SQL migration on the production database.")

	rc := NewRoomClassifier(nil, 0)
	report, err := RunAudit(v, rc, AuditOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
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
	if !m.Mismatch {
		t.Error("Mismatch flag should be true")
	}
}

func TestRunAudit_DetectsBorderline(t *testing.T) {
	v := auditVault(t)
	// "api" alone is a medium keyword (0.6) — exactly at the default threshold.
	addDrawer(t, v, "proj", "wing", "api", "check the api for errors")

	rc := NewRoomClassifier(nil, 0)
	report, err := RunAudit(v, rc, AuditOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}
	if len(report.Borderlines) != 1 {
		t.Fatalf("Borderlines = %d, want 1", len(report.Borderlines))
	}
	if !report.Borderlines[0].Borderline {
		t.Error("Borderline flag should be true")
	}
}

func TestRunAudit_RoomDistribution(t *testing.T) {
	v := auditVault(t)
	addDrawer(t, v, "proj", "wing", "api", "content one")
	addDrawer(t, v, "proj", "wing", "api", "content two")
	addDrawer(t, v, "proj", "wing", "testing", "content three")

	rc := NewRoomClassifier(nil, 0)
	report, err := RunAudit(v, rc, AuditOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}
	if report.TotalDrawers != 3 {
		t.Errorf("TotalDrawers = %d, want 3", report.TotalDrawers)
	}
	if len(report.Distributions) != 2 {
		t.Fatalf("Distributions = %d, want 2", len(report.Distributions))
	}
	// Sorted by count descending — api (2) first.
	if report.Distributions[0].Room != "api" {
		t.Errorf("first distribution room = %q, want api", report.Distributions[0].Room)
	}
	if report.Distributions[0].Count != 2 {
		t.Errorf("api count = %d, want 2", report.Distributions[0].Count)
	}
}

func TestRunAudit_GeneralFallbackRate(t *testing.T) {
	v := auditVault(t)
	addDrawer(t, v, "proj", "wing", "general", "the sky is blue and water is wet")
	addDrawer(t, v, "proj", "wing", "general", "another generic sentence here")
	addDrawer(t, v, "proj", "wing", "api", "build the graphql schema")

	rc := NewRoomClassifier(nil, 0)
	report, err := RunAudit(v, rc, AuditOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}
	if report.GeneralCount != 2 {
		t.Errorf("GeneralCount = %d, want 2", report.GeneralCount)
	}
	// 2/3 ≈ 66.7%
	if report.GeneralPercent < 60 || report.GeneralPercent > 70 {
		t.Errorf("GeneralPercent = %.1f, want ~66.7", report.GeneralPercent)
	}
}

func TestRunAudit_KeywordCoverage(t *testing.T) {
	v := auditVault(t)
	addDrawer(t, v, "proj", "wing", "devops", "deploy the kubernetes cluster using terraform")

	rc := NewRoomClassifier(nil, 0)
	report, err := RunAudit(v, rc, AuditOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}
	if len(report.Coverage) == 0 {
		t.Fatal("expected keyword coverage report")
	}
	// Find devops coverage.
	var devopsCov *RoomKeywordReport
	for i := range report.Coverage {
		if report.Coverage[i].Room == "devops" {
			devopsCov = &report.Coverage[i]
			break
		}
	}
	if devopsCov == nil {
		t.Fatal("expected devops in coverage")
	}
	if len(devopsCov.Fired) == 0 {
		t.Error("expected fired keywords for devops")
	}
	firedSet := make(map[string]bool)
	for _, kw := range devopsCov.Fired {
		firedSet[kw] = true
	}
	if !firedSet["kubernetes"] {
		t.Error("expected 'kubernetes' in fired")
	}
	if !firedSet["terraform"] {
		t.Error("expected 'terraform' in fired")
	}
	if !firedSet["deploy"] {
		t.Error("expected 'deploy' in fired")
	}
}

func TestRunAudit_WithOverrides(t *testing.T) {
	v := auditVault(t)
	// Content that matches new "ml" room.
	addDrawer(t, v, "proj", "wing", "general", "train the neural network transformer model")

	overrides := map[string]WeightedOverride{
		"ml": {
			High:   []string{"neural network", "transformer"},
			Medium: []string{"training"},
		},
	}
	rc := NewRoomClassifier(overrides, 0)
	report, err := RunAudit(v, rc, AuditOptions{Project: "proj"})
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

func TestAuditReport_Candidates(t *testing.T) {
	v := auditVault(t)
	addDrawer(t, v, "proj", "wing", "api", "Set up the kubernetes cluster for deployment.")
	addDrawer(t, v, "proj", "wing", "data", "Run the SQL migration on the production database.")

	rc := NewRoomClassifier(nil, 0)
	report, err := RunAudit(v, rc, AuditOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}

	candidates := report.Candidates()
	if len(candidates) != 1 {
		t.Fatalf("Candidates = %d, want 1", len(candidates))
	}
	c := candidates[0]
	if c.FromRoom != "api" {
		t.Errorf("FromRoom = %q, want api", c.FromRoom)
	}
	if c.ToRoom != "devops" {
		t.Errorf("ToRoom = %q, want devops", c.ToRoom)
	}
	if c.NewScore <= 0 {
		t.Errorf("NewScore = %f, want > 0", c.NewScore)
	}
}
