// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vplog

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var (
	mu      sync.Mutex
	logFile *os.File
)

// Init configures the global slog default with a JSON handler writing to
// the given path. Call once from cmd/vp/main.go after config is loaded.
// Creates parent directories via EnsureDir. Opens file with O_APPEND for
// POSIX atomic writes (safe for concurrent vp instances).
// If file creation fails, falls back to a no-op handler (never blocks startup).
// Rotates: if file exceeds 1MB, renames to .log.1 first.
func Init(logPath string, level slog.Level) error {
	mu.Lock()
	defer mu.Unlock()

	if err := storage.EnsureDir(filepath.Dir(logPath)); err != nil {
		slog.SetDefault(slog.New(discardHandler{}))
		return err
	}

	// Rotate if existing file exceeds 1MB.
	if info, err := os.Stat(logPath); err == nil && info.Size() > 1<<20 {
		_ = os.Rename(logPath, logPath+".1")
	}

	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		slog.SetDefault(slog.New(discardHandler{}))
		return err
	}

	logFile = f
	handler := slog.NewJSONHandler(f, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
	return nil
}

// Close flushes and closes the log file. Defer from main. Idempotent.
func Close() {
	mu.Lock()
	defer mu.Unlock()

	if logFile != nil {
		logFile.Close()
		logFile = nil
	}
}

// discardHandler is a no-op slog handler used as fallback.
type discardHandler struct{}

func (discardHandler) Enabled(_ context.Context, _ slog.Level) bool  { return false }
func (discardHandler) Handle(_ context.Context, _ slog.Record) error { return nil }
func (discardHandler) WithAttrs(_ []slog.Attr) slog.Handler          { return discardHandler{} }
func (discardHandler) WithGroup(_ string) slog.Handler               { return discardHandler{} }
