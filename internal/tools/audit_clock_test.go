// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultaudit"
)

// TestCaptureAndAuditStampTheSameCalendarDay compares the two writer paths to
// each other at a frozen instant. Both must use the process-local day, and a
// leftover clock.toml must not split them.
func TestCaptureAndAuditStampTheSameCalendarDay(t *testing.T) {
	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skipf("no tzdata for America/Denver on this host: %v", err)
	}
	vault := storage.NewVault(t.TempDir())
	if err := os.WriteFile(filepath.Join(vault.Root, "clock.toml"),
		[]byte("timezone = \"America/Denver\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	evening := time.Date(2026, 8, 12, 22, 57, 0, 0, denver)

	capRes, err := capture.WriteSession(context.Background(), vault, nil, capture.SessionParams{
		Project: "test-proj",
		Summary: "same instant as the audit",
		Now:     evening,
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	day := storage.CalendarDay(evening)
	rel := vaultaudit.ReportRelPath(day)
	abs := filepath.Join(vault.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(vaultaudit.Report{}.Render(day, vault.Root)), 0o644); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(capRes.NotePath, day) {
		t.Errorf("capture note = %q, want it to carry %s", capRes.NotePath, day)
	}
	if !strings.Contains(rel, day) {
		t.Errorf("audit path = %q, want it to carry %s", rel, day)
	}
	capDay := filepath.Base(capRes.NotePath)[:10]
	auditDay := strings.TrimSuffix(filepath.Base(rel), "-vault-audit.md")
	if capDay != auditDay {
		t.Errorf("capture day %q and audit day %q disagree", capDay, auditDay)
	}
	_, off := time.Now().Zone()
	if off == 0 && day == "2026-08-12" {
		t.Error("clock.toml forced America/Denver on a UTC-offset host — the OS zone must win")
	}
}
