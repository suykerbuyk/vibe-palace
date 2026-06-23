// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

func TestRunTrendsEmpty(t *testing.T) {
	v := testVault(t)
	var buf bytes.Buffer
	code := runTrends(v, "test-proj", false, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "Friction windows") {
		t.Errorf("missing windows header: %s", out)
	}
	if !strings.Contains(out, "no model data yet") {
		t.Errorf("expected no-model-data line on empty vault: %s", out)
	}
}

func TestRunTrendsHuman(t *testing.T) {
	v := testVault(t)
	seedFrictionVault(t, v, "test-proj")

	var buf bytes.Buffer
	code := runTrends(v, "test-proj", false, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "Correction density") {
		t.Errorf("missing correction-density section: %s", out)
	}
	// One session predates breakdown capture (nil Breakdown).
	if !strings.Contains(out, "predate breakdown capture") {
		t.Errorf("expected missing-data disclosure: %s", out)
	}
	// Two models present, so a boundary + coverage label.
	if !strings.Contains(out, "model coverage:") {
		t.Errorf("expected model coverage label: %s", out)
	}
}

func TestRunTrendsJSON(t *testing.T) {
	v := testVault(t)
	seedFrictionVault(t, v, "test-proj")

	var buf bytes.Buffer
	code := runTrends(v, "test-proj", true, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var tr trendsResult
	if err := json.Unmarshal(buf.Bytes(), &tr); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(tr.Windows) != 3 {
		t.Errorf("Windows = %d, want 3 (7/30/90)", len(tr.Windows))
	}
	// Two sessions have breakdown data, one does not.
	if len(tr.CorrectionDensity.Points) != 2 {
		t.Errorf("CorrectionDensity.Points = %d, want 2", len(tr.CorrectionDensity.Points))
	}
	if tr.CorrectionDensity.MissingCount != 1 {
		t.Errorf("MissingCount = %d, want 1", tr.CorrectionDensity.MissingCount)
	}
	// All three sessions have a Model set.
	if tr.ModelRegressions.ModeledCount != 3 {
		t.Errorf("ModeledCount = %d, want 3", tr.ModelRegressions.ModeledCount)
	}
}
