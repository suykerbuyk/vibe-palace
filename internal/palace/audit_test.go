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

// addDecisionDrawer files a drawer the way capture files a decision — into the
// fixed "decisions" room, tagged with storage.SourceTypeDecision — and returns
// it as stored so callers can assert against its generated ID.
func addDecisionDrawer(t *testing.T, v *storage.Vault, project, wing, content string) storage.Drawer {
	t.Helper()
	d := storage.Drawer{
		Content:    content,
		Hall:       HallDecisions,
		SourceType: storage.SourceTypeDecision,
		FiledAt:    "2026-04-10T10:00:00Z",
	}
	if err := v.AppendDrawer(project, wing, "decisions", d); err != nil {
		t.Fatalf("AppendDrawer(decision): %v", err)
	}
	stored, err := v.ListDrawers(project, wing, "decisions")
	if err != nil {
		t.Fatalf("ListDrawers(decisions): %v", err)
	}
	for _, s := range stored {
		if s.Content == content {
			return s
		}
	}
	t.Fatal("decision drawer not found after append")
	return storage.Drawer{}
}

func TestRunAudit_DecisionDrawerNeverProposedForMove(t *testing.T) {
	v := auditVault(t)
	addDrawer(t, v, "proj", "wing", "devops", "Set up the kubernetes cluster for deployment.")
	// The classifier can never return "decisions", so without the skip this
	// drawer is a guaranteed mismatch and --apply would move it out of the
	// room the palace query prunes to.
	dec := addDecisionDrawer(t, v, "proj", "wing",
		"Decided to standardize on the kubernetes cluster for every deployment.")

	rc := NewRoomClassifier(nil, 0)
	report, err := RunAudit(v, rc, AuditOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}
	for _, m := range report.Mismatches {
		if m.ID == dec.ID {
			t.Fatalf("decision drawer %s reported as mismatch (%s -> %s)",
				dec.ID, m.CurrentRoom, m.BestRoom)
		}
	}
	for _, c := range report.Candidates() {
		if c.DrawerID == dec.ID {
			t.Fatalf("decision drawer %s produced move candidate %s -> %s",
				dec.ID, c.FromRoom, c.ToRoom)
		}
	}
}

// TestRunAudit_DecisionDrawerExcludedFromArithmetic pins the skip to the TOP of
// the audit's drawer loop, above report.TotalDrawers++. A skip placed lower —
// merely before the classify call — still suppresses the move proposal, so
// TestRunAudit_DecisionDrawerNeverProposedForMove keeps passing while every
// percentage in the report silently drifts. This test is what reddens.
func TestRunAudit_DecisionDrawerExcludedFromArithmetic(t *testing.T) {
	v := auditVault(t)
	addDrawer(t, v, "proj", "wing", "devops", "Set up the kubernetes cluster for deployment.")
	addDrawer(t, v, "proj", "wing", "general", "the sky is blue and water is wet")
	addDecisionDrawer(t, v, "proj", "wing",
		"Decided to standardize on the kubernetes cluster for every deployment.")

	rc := NewRoomClassifier(nil, 0)
	report, err := RunAudit(v, rc, AuditOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("RunAudit: %v", err)
	}

	// TotalDrawers is the divisor for every percentage below.
	if report.TotalDrawers != 2 {
		t.Errorf("TotalDrawers = %d, want 2 (decision drawer must not count)",
			report.TotalDrawers)
	}

	// No "decisions" row, and the two real rooms split the corpus evenly.
	if len(report.Distributions) != 2 {
		t.Errorf("Distributions = %d, want 2: %+v",
			len(report.Distributions), report.Distributions)
	}
	for _, d := range report.Distributions {
		if d.Room == "decisions" {
			t.Errorf("distribution contains a decisions row: %+v", d)
			continue
		}
		if d.Count != 1 {
			t.Errorf("%s count = %d, want 1", d.Room, d.Count)
		}
		if d.Percent < 49.9 || d.Percent > 50.1 {
			t.Errorf("%s percent = %.2f, want 50.00", d.Room, d.Percent)
		}
	}

	// GeneralPercent uses the same divisor.
	if report.GeneralCount != 1 {
		t.Errorf("GeneralCount = %d, want 1", report.GeneralCount)
	}
	if report.GeneralPercent < 49.9 || report.GeneralPercent > 50.1 {
		t.Errorf("GeneralPercent = %.2f, want 50.00 (decision drawer must not "+
			"inflate the divisor)", report.GeneralPercent)
	}
}
