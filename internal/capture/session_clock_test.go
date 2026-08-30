// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func denverOrSkip(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skipf("no tzdata for America/Denver on this host: %v", err)
	}
	return loc
}

// TestCaptureRetryDoesNotRestampAcrossMidnight pins mint-vs-retry: WriteSession
// always computes a calendar day, but a session_key retry must not rename the
// note if midnight moved.
func TestCaptureRetryDoesNotRestampAcrossMidnight(t *testing.T) {
	vault := testVault(t)
	evening := time.Date(2026, 8, 12, 22, 57, 0, 0, denverOrSkip(t))

	first, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj",
		Summary: "mint",
		Now:     evening,
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	nextDay := evening.Add(24 * time.Hour)
	second, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "retry after midnight",
		SessionKey: first.SessionKey,
		Now:        nextDay,
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if second.NotePath != first.NotePath {
		t.Errorf("retry renamed the note: %q vs %q", second.NotePath, first.NotePath)
	}
	if n := countNotes(t, vault, "test-proj"); n != 1 {
		t.Fatalf("after midnight retry: %d notes, want 1", n)
	}
	meta, _ := readNote(t, vault, second.NotePath)
	want := vault.CalendarDay(evening)
	if meta.Date != want {
		t.Errorf("YAML date = %q, want the mint day %q", meta.Date, want)
	}
}

func TestCaptureStampsProcessLocalCalendarDay(t *testing.T) {
	vault := testVault(t)
	evening := time.Date(2026, 8, 12, 22, 57, 0, 0, denverOrSkip(t))
	if err := os.WriteFile(filepath.Join(vault.Root, "clock.toml"), []byte("timezone = \"America/Denver\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj",
		Summary: "os-local evening",
		Now:     evening,
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	meta, _ := readNote(t, vault, result.NotePath)
	want := storage.CalendarDay(evening)
	if meta.Date != want {
		t.Errorf("date = %q, want process-local %q", meta.Date, want)
	}
	_, off := time.Now().Zone()
	if off == 0 && meta.Date == "2026-08-12" {
		t.Error("clock.toml forced America/Denver on a UTC-offset host — the OS zone must win")
	}
}
