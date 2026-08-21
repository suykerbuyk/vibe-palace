// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package atomicfile provides the whole-file write primitives that vault
// content goes through. Each writes via a temp-file + same-directory rename
// and, on success, best-effort stamps the MCP surface version for the vault
// containing the target (see internal/surface). It is the consolidation point
// for vibe-palace's whole-file vault writers so surface stamping is structural
// rather than per-call discipline.
//
// Two shapes, one core:
//
//   - Write takes the bytes. Use it whenever the content is already in memory.
//   - WriteStream takes a writer callback, for content that must not be
//     buffered whole — today a compressed transcript, which is the largest
//     thing the vault holds.
//
// Append-style writers (JSONL appends, O_EXCL single-file creates) cannot use a
// whole-file replace primitive; they call surface.StampForPath directly after a
// successful write instead.
//
// Neither primitive GATES. The MCP surface-compatibility fail-stop lives at the
// dispatch seam and stays there (doc/adr/010-surface-gate-at-the-dispatch-seam.md);
// a write primitive that refused would put a fail-stop underneath every CLI
// path and every recovery path at once.
//
// atomicfile imports surface (a leaf). Nothing surface depends on imports
// atomicfile, so the dependency graph stays acyclic.
package atomicfile

import (
	"fmt"
	"io"
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
	return writeAtomic(vaultRoot, absPath, cfg, func(f *os.File) error {
		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("write temp: %w", err)
		}
		return nil
	})
}

// WriteStream is Write for content that must not be held in memory: it opens
// the temp file, hands it to fill as an io.Writer, and then completes the same
// chmod + rename + stamp that Write does. Pass vaultRoot == "" to skip stamping
// (non-vault destinations).
//
// fill's error is returned unwrapped, so the caller's own vocabulary survives —
// a compression failure reads as a compression failure, not as "write temp".
// Any failure leaves absPath untouched and removes the temp file.
//
// # It stamps, and it takes no options
//
// A streamed file is CONTENT, so this behaves like Write and not like the
// removal sink (vaultfs.RemoveNoLock / RenameNoLock), which deliberately does
// not stamp because it writes no content.
//
// There is deliberately no opts parameter. This primitive has exactly one
// caller (archive.compressFile), and options nobody passes are the shape
// sourceaudit's `uninvoked` rule exists to catch. In particular it does NOT
// fsync: the hand-rolled temp+rename it replaced did not either, and matching
// existing durability rather than silently improving it is the same parity rule
// the append primitive followed. Add an option here when a second caller needs
// one, not before.
func WriteStream(vaultRoot, absPath string, fill func(io.Writer) error) error {
	return writeAtomic(vaultRoot, absPath, config{perm: 0o644}, func(f *os.File) error {
		return fill(f)
	})
}

// writeAtomic is the shared temp-plus-rename core behind Write and WriteStream.
// It owns every step except producing the bytes: parent directories, the temp
// file, the optional fsync, the permission mode, the retrying rename, and the
// surface stamp. A caller that hand-rolls any of those is the bypass this
// package exists to delete.
//
// fill's error is returned UNWRAPPED. Each caller therefore keeps its own error
// vocabulary, and the temp file is removed on every failure path.
func writeAtomic(vaultRoot, absPath string, cfg config, fill func(*os.File) error) error {
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

	if err := fill(tmp); err != nil {
		tmp.Close()
		return err
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
