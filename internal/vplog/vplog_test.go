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

	// Size off MaxSize, not a hardcoded literal: this test previously pinned
	// 1<<20 and broke the moment the threshold moved.
	big := strings.Repeat("x", MaxSize+100)
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
	if backupInfo.Size() < MaxSize {
		t.Errorf("backup too small: %d", backupInfo.Size())
	}

	// New log should be small (just opened, nothing written yet).
	newInfo, _ := os.Stat(logPath)
	if newInfo.Size() > 1024 {
		t.Errorf("new log too big: %d", newInfo.Size())
	}
}

// TestInitBelowThresholdDoesNotRotate pins the other side of the boundary: a
// log under MaxSize is appended to, not rotated away. Without this, raising
// MaxSize could silently become "never rotate" and nobody would notice.
func TestInitBelowThresholdDoesNotRotate(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "vp.log")

	if err := os.WriteFile(logPath, []byte("existing\n"), 0644); err != nil {
		t.Fatalf("seed log: %v", err)
	}
	if err := Init(logPath, slog.LevelInfo); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer Close()

	if _, err := os.Stat(logPath + ".1"); !os.IsNotExist(err) {
		t.Error("small log was rotated; it should have been appended to")
	}
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "existing") {
		t.Error("O_APPEND lost the prior contents")
	}
}

// TestInitRepointsToNewPath is the behavior vp hook depends on. The CLI pre-run
// initializes logging against the process cwd's vault, but the hook only learns
// which vault it is actually writing to after parsing the CWD out of its stdin
// payload — so it re-Inits. A second Init must redirect subsequent output to the
// new file and leave the old one alone.
func TestInitRepointsToNewPath(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first", "vp.log")
	second := filepath.Join(dir, "second", "vp.log")

	if err := Init(first, slog.LevelInfo); err != nil {
		t.Fatalf("Init first: %v", err)
	}
	slog.Warn("before repoint")

	if err := Init(second, slog.LevelInfo); err != nil {
		t.Fatalf("Init second: %v", err)
	}
	slog.Warn("after repoint")
	Close()

	firstData, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("read first: %v", err)
	}
	secondData, err := os.ReadFile(second)
	if err != nil {
		t.Fatalf("read second: %v", err)
	}

	if !strings.Contains(string(firstData), "before repoint") {
		t.Error("first log missing the entry written before the re-point")
	}
	if strings.Contains(string(firstData), "after repoint") {
		t.Error("first log received an entry written AFTER the re-point")
	}
	if !strings.Contains(string(secondData), "after repoint") {
		t.Error("second log missing the entry written after the re-point")
	}
	if strings.Contains(string(secondData), "before repoint") {
		t.Error("second log received an entry written BEFORE the re-point")
	}
}

// TestInitTwiceDoesNotLeakHandles guards the fd leak: Init used to assign
// logFile without closing the handle it was replacing, so every re-Init leaked
// one. Close() only ever closed the last. Re-Init many times, then assert the
// log is still writable — a leak-free Init keeps exactly one handle open.
func TestInitTwiceDoesNotLeakHandles(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "vp.log")

	for range 64 {
		if err := Init(logPath, slog.LevelInfo); err != nil {
			t.Fatalf("Init: %v", err)
		}
	}
	slog.Warn("still writable after 64 inits")
	Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "still writable after 64 inits") {
		t.Error("log not writable after repeated Init")
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
