// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultaudit"
)

// TestTaskFileValidityEvidenceReproducesTheGoRule enrolls DimTaskFileValidity in the
// differential discipline: the Evidence string must reproduce the Go rule.
//
// It lives in cmd/vp, not internal/vaultaudit, and that placement is FORCED rather
// than preferred. The evidence is a `vp` subcommand, so reproducing it means calling
// runTaskFileValidityReport, which is package main. cmd/vp imports vaultaudit and
// vaultaudit does not import cmd/vp, so this is the only package that can see both
// sides.
//
// 🔴 WHAT THIS PROVES, STATED SO NOBODY READS IT AS MORE. Both sides call the same
// storage.ValidateWholeTaskFile, so this does NOT cross-check the eight rules — that
// is the funnel rule working as intended, one definition and two callers. What it
// cross-checks is the ENUMERATION: the command walks Projects/*/tasks/{done,cancelled}
// with os.ReadDir while the dimension goes through vault.ListAllProjects and
// TaskDoneDir/TaskCancelledDir. That is where the two can really diverge, and the
// fixture below carries the shapes that make them: a cancelled/ population, a project
// with no archive directories at all, a non-.md file, and a subdirectory under done/.
//
// 🔴 THIS TEST CANNOT GUARD ITS OWN PREMISE, AND THAT IS WHY A SOURCEAUDIT RULE
// EXISTS. If the command is "simplified" to call ListAllProjects, the two sides
// become one implementation quoting itself — and this test does not go red, it goes
// green more reliably, because the only thing it compared is now shared. Output
// comparison is structurally incapable of catching that. internal/sourceaudit's
// evidenceWalkIndependence rule is what catches it; see that file for why the check
// is a finding rather than an assertion.
func TestTaskFileValidityEvidenceReproducesTheGoRule(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	var wantInvalid []string
	wantInvalid = append(wantInvalid, seedTaskFile(t, vault, "alpha", "done", "nosection", noSectionFile))
	wantInvalid = append(wantInvalid, seedTaskFile(t, vault, "alpha", "done", "fenced", fencedH2File))
	// cancelled/ is a real part of the corpus and a real chance to diverge.
	wantInvalid = append(wantInvalid, seedTaskFile(t, vault, "beta", "cancelled", "twopri", twoPriorityFile))
	// Valid files, which neither side may report.
	seedTaskFile(t, vault, "alpha", "done", "fine", validArchived)
	seedTaskFile(t, vault, "beta", "cancelled", "alsofine", validArchived)
	// An ACTIVE malformed file: out of scope for BOTH sides. If either grows the
	// active directory, they diverge from this expectation together — which is what
	// TestTaskFileValidity_ActiveFilesAreNotReported pins on the dimension side.
	seedTaskFile(t, vault, "alpha", "", "activebad", noSectionFile)
	// A project with a tasks/ tree but no archive directories at all.
	seedTaskFile(t, vault, "gamma", "", "onlyactive", validArchived)
	// Shapes the walk must skip identically on both sides.
	if err := os.WriteFile(filepath.Join(vault.Root, "Projects", "alpha", "tasks", "done", "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(vault.Root, "Projects", "alpha", "tasks", "done", "attic"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault.Root, "Projects", "alpha", "tasks", "done", "attic", "buried.md"), []byte(noSectionFile), 0o600); err != nil {
		t.Fatal(err)
	}
	sort.Strings(wantInvalid)

	// --- the Go rule ---
	report, err := vaultaudit.Run(vault)
	if err != nil {
		t.Fatal(err)
	}
	var dimArtifacts []string
	var sawDimension bool
	for _, d := range report.Dimensions {
		if d.Name != vaultaudit.DimTaskFileValidity {
			continue
		}
		sawDimension = true
		for _, f := range d.New {
			dimArtifacts = append(dimArtifacts, f.Artifact)
		}
	}
	if !sawDimension {
		t.Fatalf("%s is not registered in the audit", vaultaudit.DimTaskFileValidity)
	}
	sort.Strings(dimArtifacts)

	// --- the evidence command ---
	rep, rerr := runTaskFileValidityReport(vault.Root, "")
	if rerr != nil {
		t.Fatal(rerr)
	}
	var cmdArtifacts []string
	for _, f := range rep.Failures {
		cmdArtifacts = append(cmdArtifacts, f.Rel)
	}
	sort.Strings(cmdArtifacts)

	if len(wantInvalid) == 0 || len(dimArtifacts) == 0 {
		t.Fatal("precondition: the fixture must produce findings, or agreement is vacuous")
	}
	if !slices.Equal(dimArtifacts, wantInvalid) {
		t.Errorf("the DIMENSION reports %v, the fixture declares %v", dimArtifacts, wantInvalid)
	}
	if !slices.Equal(cmdArtifacts, wantInvalid) {
		t.Errorf("the EVIDENCE COMMAND reports %v, the fixture declares %v", cmdArtifacts, wantInvalid)
	}
	if !slices.Equal(cmdArtifacts, dimArtifacts) {
		t.Errorf("evidence prints %v, the dimension reports %v", cmdArtifacts, dimArtifacts)
	}
}
