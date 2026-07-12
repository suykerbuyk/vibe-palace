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

// MaxSize is the size past which the log is rotated to <path>.1 on the next
// Init. One generation is kept.
//
// Headroom matters more than it used to: Init now runs from the CLI dispatch
// pre-run, so every vp command emits here, not just the two that call
// bootstrap(). A 1 MiB cap and a single generation meant a chatty run could
// rotate away the warning history the log exists to preserve.
const MaxSize = 8 << 20

var (
	mu      sync.Mutex
	logFile *os.File
)

// Init points the global slog default at a JSON handler writing to logPath.
// Creates parent directories via EnsureDir. Opens with O_APPEND, so concurrent
// vp processes can share the file. If the file cannot be opened, installs a
// no-op handler and returns the error — logging never blocks startup.
//
// Init is safe to call more than once: it closes the handle it previously
// installed before opening the new one. Re-pointing is not just tolerated, it
// is required — `vp hook` resolves its vault from the CWD in the JSON payload
// on stdin, which is read *after* the pre-run hook has already initialized
// logging against the process's own cwd. The hook re-points to the vault it is
// actually writing to; without that, its warnings would land in a different
// vault's log.
func Init(logPath string, level slog.Level) error {
	mu.Lock()
	defer mu.Unlock()

	if err := storage.EnsureDir(filepath.Dir(logPath)); err != nil {
		slog.SetDefault(slog.New(discardHandler{}))
		return err
	}

	if info, err := os.Stat(logPath); err == nil && info.Size() > MaxSize {
		_ = os.Rename(logPath, logPath+".1")
	}

	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		slog.SetDefault(slog.New(discardHandler{}))
		return err
	}

	// Close the handle from a prior Init before dropping the reference, or a
	// re-point leaks the fd (and, on the MCP path, leaked one per call).
	if logFile != nil {
		logFile.Close()
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
