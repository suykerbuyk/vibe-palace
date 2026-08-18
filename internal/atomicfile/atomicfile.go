// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package atomicfile provides a single whole-file write primitive that writes
// via a temp-file + same-directory rename and, on success, best-effort stamps
// the MCP surface version for the vault containing the target (see
// internal/surface). It is the consolidation point for vibe-palace's
// whole-file vault writers so surface stamping is structural rather than
// per-call discipline.
//
// Append-style writers (JSONL appends, O_EXCL single-file creates) cannot use a
// whole-file replace primitive; they call surface.StampForPath directly after a
// successful write instead.
//
// atomicfile imports surface (a leaf). Nothing surface depends on imports
// atomicfile, so the dependency graph stays acyclic.
package atomicfile

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// config holds resolved write options.
type config struct {
	perm        os.FileMode
	inheritPerm bool
	fsync       bool
}

// Option configures a Write call.
type Option func(*config)

// WithPerm sets explicit file permissions (default 0o644).
func WithPerm(m os.FileMode) Option { return func(c *config) { c.perm = m } }

// WithInheritPerm makes Write inherit the existing target file's mode when the
// target exists, falling back to the configured/default perm when it does not.
// Used by writers that must preserve user-set permissions on managed files.
func WithInheritPerm() Option { return func(c *config) { c.inheritPerm = true } }

// WithFsync makes Write fsync the temp file before rename, for callers that
// want durability beyond rename atomicity.
func WithFsync() Option { return func(c *config) { c.fsync = true } }

// Write atomically writes data to absPath: it creates parent directories,
// writes a temp file in the same directory, optionally fsyncs, chmods, and
// renames it over absPath (retrying the rename on the transient Windows sharing
// failures classified by retryableRenameErr). On success it best-effort stamps
// the surface version for vaultRoot/absPath; pass vaultRoot == "" to skip
// stamping (non-vault writes). A stamp failure is logged and never fails the
// write.
func Write(vaultRoot, absPath string, data []byte, opts ...Option) error {
	cfg := config{perm: 0o644}
	for _, o := range opts {
		o(&cfg)
	}

	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	perm := cfg.perm
	if cfg.inheritPerm {
		if info, err := os.Stat(absPath); err == nil {
			perm = info.Mode().Perm()
		}
	}

	tmp, err := os.CreateTemp(dir, ".vp-atomic-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if cfg.fsync {
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			return fmt.Errorf("sync temp: %w", err)
		}
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := renameWithRetry(tmpPath, absPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	removeTemp = false

	if vaultRoot != "" {
		if err := surface.StampForPath(vaultRoot, absPath); err != nil {
			slog.Warn("surface stamp failed", "path", absPath, "err", err)
		}
	}
	return nil
}
