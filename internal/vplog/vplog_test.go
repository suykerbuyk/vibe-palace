// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vplog

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCreatesLogAndWritesJSON(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "sub", "vp.log")

	if err := Init(logPath, slog.LevelInfo); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer Close()

	slog.Info("test entry", "key", "value")
	Close() // flush

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("log file is empty")
	}

	// Verify JSON is parseable.
	var entry map[string]any
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("parse JSON: %v (line: %s)", err, lines[0])
	}
	if entry["msg"] != "test entry" {
		t.Errorf("msg = %v, want 'test entry'", entry["msg"])
	}
}

func TestCloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "vp.log")

	if err := Init(logPath, slog.LevelInfo); err != nil {
		t.Fatalf("Init: %v", err)
	}

	Close()
	Close() // should not panic
}

func TestInitBadPathFallsBack(t *testing.T) {
	// /dev/null/impossible is not a valid directory.
	err := Init("/dev/null/impossible/vp.log", slog.LevelInfo)
	if err == nil {
		t.Fatal("expected error for bad path")
	}
	defer Close()

	// Should not panic — discard handler is active.
	slog.Warn("this should be silently discarded")
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "vp.log")

	// Create a file > 1MB.
	big := strings.Repeat("x", 1<<20+100)
	if err := os.WriteFile(logPath, []byte(big), 0644); err != nil {
		t.Fatalf("write big file: %v", err)
	}

	if err := Init(logPath, slog.LevelInfo); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer Close()

	// Old file should be renamed to .log.1
	backupPath := logPath + ".1"
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup not created: %v", err)
	}
	backupInfo, _ := os.Stat(backupPath)
	if backupInfo.Size() < 1<<20 {
		t.Errorf("backup too small: %d", backupInfo.Size())
	}

	// New log should be small (just opened, nothing written yet).
	newInfo, _ := os.Stat(logPath)
	if newInfo.Size() > 1024 {
		t.Errorf("new log too big: %d", newInfo.Size())
	}
}

func TestDiscardHandler(t *testing.T) {
	var h discardHandler
	ctx := context.Background()
	if h.Enabled(ctx, slog.LevelError) {
		t.Error("discard handler should never be enabled")
	}
	if err := h.Handle(ctx, slog.Record{}); err != nil {
		t.Errorf("Handle: %v", err)
	}
	h2 := h.WithAttrs([]slog.Attr{slog.String("k", "v")})
	if _, ok := h2.(discardHandler); !ok {
		t.Error("WithAttrs should return discardHandler")
	}
	h3 := h.WithGroup("g")
	if _, ok := h3.(discardHandler); !ok {
		t.Error("WithGroup should return discardHandler")
	}
}

func TestLevelFiltering(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "vp.log")

	if err := Init(logPath, slog.LevelWarn); err != nil {
		t.Fatalf("Init: %v", err)
	}

	slog.Info("should not appear")
	slog.Warn("should appear")
	Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	content := string(data)
	if strings.Contains(content, "should not appear") {
		t.Error("info message should have been filtered")
	}
	if !strings.Contains(content, "should appear") {
		t.Error("warn message should be present")
	}
}
