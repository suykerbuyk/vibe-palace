// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"fmt"
	"os"
	"path/filepath"
)

// ResultKind classifies what Wire decided to do for a single shim file.
type ResultKind int

const (
	// Unchanged: file exists with the marker and the current content hash.
	Unchanged ResultKind = iota
	// Added: no file previously existed at path; Wire created one.
	Added
	// Updated: file existed with our marker but a stale sha (or malformed
	// marker). Wire replaced the full file body.
	Updated
	// Custom: file exists without our marker. Wire left it strictly
	// alone. Apply/Plan surface it for informational reporting only.
	Custom
)

// String gives each kind a stable short name for status rows and tests.
func (k ResultKind) String() string {
	switch k {
	case Unchanged:
		return "unchanged"
	case Added:
		return "added"
	case Updated:
		return "updated"
	case Custom:
		return "custom"
	default:
		return fmt.Sprintf("kind(%d)", int(k))
	}
}

// Result is what Wire returns. PrevSha is populated on Updated outcomes so
// callers can report "updated (was abc1234)"; it stays empty for Added,
// Unchanged, and Custom.
type Result struct {
	Kind    ResultKind
	PrevSha string
}

// Wire ensures path holds a current shim for (name, brief). Semantics:
//
//   - File absent          -> write rendered, return Added.
//   - File present, no marker -> leave untouched, return Custom.
//   - File present, marker sha matches expected -> return Unchanged (no write).
//   - File present, marker sha mismatches or marker malformed -> rewrite,
//     return Updated with PrevSha set to whatever sha we extracted.
//
// Writes are atomic (tmp + fsync + rename in the same directory) so a crash
// mid-write cannot leave a half-written shim visible to Claude Code's
// directory watcher.
func Wire(path, name, brief string) (Result, error) {
	rendered := Render(name, brief)
	expected := ExpectedSha(name, brief)

	scan, err := ScanShim(path)
	if err != nil {
		return Result{}, fmt.Errorf("scan %s: %w", path, err)
	}

	switch {
	case !scan.Exists:
		if err := atomicWrite(path, []byte(rendered)); err != nil {
			return Result{}, err
		}
		return Result{Kind: Added}, nil

	case !scan.HasMarker:
		return Result{Kind: Custom}, nil

	case scan.Sha == expected && scan.Version == shimVersion:
		return Result{Kind: Unchanged, PrevSha: scan.Sha}, nil

	default:
		if err := atomicWrite(path, []byte(rendered)); err != nil {
			return Result{}, err
		}
		return Result{Kind: Updated, PrevSha: scan.Sha}, nil
	}
}

// Remove deletes a previously-emitted shim at path. It refuses to delete
// files that do not carry our marker — "custom" files are sacrosanct.
// Returns (true, nil) when the file was removed, (false, nil) when the
// file did not exist or lacked our marker, and (false, err) only on IO
// errors that are not NotExist.
func Remove(path string) (bool, error) {
	scan, err := ScanShim(path)
	if err != nil {
		return false, fmt.Errorf("scan %s: %w", path, err)
	}
	if !scan.Exists || !scan.HasMarker {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("remove %s: %w", path, err)
	}
	return true, nil
}

// atomicWrite writes data to a sibling tempfile, fsyncs it, then renames
// into place. Parent directory is created (mode 0o755) if missing — init
// is allowed to bring `.claude/commands/` into being but no other
// `.claude/` subdirectory, per plan.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".vp-shim-*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmp := f.Name()
	cleanup := func() { _ = os.Remove(tmp) }

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("write temp %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("fsync temp %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp %s: %w", tmp, err)
	}
	// Inherit existing file's mode on updates so permissions don't surprise
	// users; new shims default to 0o644 via CreateTemp's default.
	if info, err := os.Stat(path); err == nil {
		_ = os.Chmod(tmp, info.Mode())
	}
	if err := os.Rename(tmp, path); err != nil {
		cleanup()
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}
