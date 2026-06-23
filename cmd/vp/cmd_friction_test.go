// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// seedFrictionVault writes a handful of sessions with varied friction scores,
// some with Model set and some with Breakdown non-nil, used by the friction,
// trends, and effectiveness CLI tests.
func seedFrictionVault(t *testing.T, v *storage.Vault, proj string) {
	t.Helper()
	recent := time.Now().AddDate(0, 0, -2).Format("2006-01-02")
	older := time.Now().AddDate(0, 0, -45).Format("2006-01-02")

	if _, err := v.WriteSession(proj, storage.SessionMeta{
		Date: recent, Title: "High friction", FrictionScore: 80,
		Model:     "claude-opus-4-6",
		Breakdown: &storage.FrictionBreakdown{Corrections: 12},
		Decisions: []string{"chose X"},
	}, "body"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteSession(proj, storage.SessionMeta{
		Date: recent, Title: "Low friction", FrictionScore: 10,
		Model:     "claude-opus-4-6",
		Breakdown: &storage.FrictionBreakdown{Corrections: 1},
	}, "body"); err != nil {
		t.Fatal(err)
	}
	// Older session with a different model and no breakdown (predates capture).
	if _, err := v.WriteSession(proj, storage.SessionMeta{
		Date: older, Title: "Old session", FrictionScore: 40,
		Model:        "claude-opus-4-5",
		FilesChanged: []string{"a.go"},
	}, "body"); err != nil {
		t.Fatal(err)
	}
}

func TestRunFrictionEmpty(t *testing.T) {
	v := testVault(t)
	var buf bytes.Buffer
	code := runFriction(v, "test-proj", 10, false, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(buf.String(), "No sessions found") {
		t.Errorf("expected no sessions message: %s", buf.String())
	}
}

func TestRunFrictionHuman(t *testing.T) {
	v := testVault(t)
	seedFrictionVault(t, v, "test-proj")

	var buf bytes.Buffer
	code := runFriction(v, "test-proj", 10, false, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "Recent week") {
		t.Errorf("missing recent-week line: %s", out)
	}
	if !strings.Contains(out, "Top-friction triage") {
		t.Errorf("missing triage header: %s", out)
	}
	if !strings.Contains(out, "High friction") {
		t.Errorf("missing high-friction session: %s", out)
	}
}

func TestRunFrictionJSON(t *testing.T) {
	v := testVault(t)
	seedFrictionVault(t, v, "test-proj")

	var buf bytes.Buffer
	code := runFriction(v, "test-proj", 2, true, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var fr frictionResult
	if err := json.Unmarshal(buf.Bytes(), &fr); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if fr.RecentWeek.Days != 7 {
		t.Errorf("RecentWeek.Days = %d, want 7", fr.RecentWeek.Days)
	}
	if len(fr.Top) != 2 {
		t.Fatalf("Top = %d, want 2 (capped by --top)", len(fr.Top))
	}
	// Highest friction first.
	if fr.Top[0].FrictionScore != 80 {
		t.Errorf("Top[0].FrictionScore = %d, want 80", fr.Top[0].FrictionScore)
	}
	var _ capture.FrictionSession = fr.Top[0]
}
