// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

func TestRunEffectivenessEmpty(t *testing.T) {
	v := testVault(t)
	var buf bytes.Buffer
	code := runEffectiveness(v, "test-proj", false, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(buf.String(), "No sessions found") {
		t.Errorf("expected no sessions message: %s", buf.String())
	}
}

func TestRunEffectivenessHuman(t *testing.T) {
	v := testVault(t)
	seedFrictionVault(t, v, "test-proj")

	var buf bytes.Buffer
	code := runEffectiveness(v, "test-proj", false, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "with vs without context") {
		t.Errorf("missing with-vs-without-context phrasing: %s", out)
	}
	if !strings.Contains(out, "with context:") {
		t.Errorf("missing with-context row: %s", out)
	}
	if !strings.Contains(out, "without context:") {
		t.Errorf("missing without-context row: %s", out)
	}
	if !strings.Contains(out, "delta") {
		t.Errorf("missing delta line: %s", out)
	}
}

func TestRunEffectivenessJSON(t *testing.T) {
	v := testVault(t)
	seedFrictionVault(t, v, "test-proj")

	var buf bytes.Buffer
	code := runEffectiveness(v, "test-proj", true, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var res capture.EffectivenessResult
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if res.Project != "test-proj" {
		t.Errorf("Project = %q, want test-proj", res.Project)
	}
	if res.Overall.TotalSessions != 3 {
		t.Errorf("Overall.TotalSessions = %d, want 3", res.Overall.TotalSessions)
	}
	// Two sessions carry context (decisions/files), one does not.
	if res.Overall.WithContext != 2 {
		t.Errorf("Overall.WithContext = %d, want 2", res.Overall.WithContext)
	}
	if res.Overall.WithoutContext != 1 {
		t.Errorf("Overall.WithoutContext = %d, want 1", res.Overall.WithoutContext)
	}
}
