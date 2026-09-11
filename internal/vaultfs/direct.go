// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrIndirectPath means a vault path is not reached directly: a component of
// it is a symlink, the file is not a regular file, or — on Windows — the path
// resolves to a different spelling (letter case, a short 8.3 name). A caller
// about to remove or back up the file must not: removing through a link
// deletes whatever the link points at (a project-tier file elsewhere in the
// vault, say), and a vp that follows it has changed a file it never judged.
var ErrIndirectPath = errors.New("vaultfs: path not reached directly")

// indirectPathError carries the precise reason and matches ErrIndirectPath,
// without prefixing the sentinel's text to the message: callers wrap it in
// their own sentinel (commands.ErrUnsafeResetPath) and keep their wording.
type indirectPathError struct{ msg string }

func (e *indirectPathError) Error() string        { return e.msg }
func (e *indirectPathError) Is(target error) bool { return target == ErrIndirectPath }

// CheckDirectPath refuses rel unless the path to it, under the vault root with
// symlinks resolved, is the path itself — no component of it a symlink — and
// the file, if present, is a regular file. It returns nil when any component
// is absent: there is nothing further down to follow or remove, and callers
// (a pending removal, an absent template) rely on that.
//
// The comparison is made under the RESOLVED vault root, so a vault whose root
// directory is itself a symlink (a supported layout; see raw.go) is not
// refused wholesale: only links inside the vault are.
//
// A refusal matches ErrIndirectPath; an I/O failure inspecting the path is a
// plain error.
func CheckDirectPath(vaultRoot, rel string) error {
	realRoot, err := filepath.EvalSymlinks(vaultRoot)
	if err != nil {
		return fmt.Errorf("resolve vault root: %w", err)
	}
	abs := filepath.Join(realRoot, filepath.FromSlash(rel))

	// Walk each component with Lstat: the first symlink is the one to name.
	cur := realRoot
	for part := range strings.SplitSeq(filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel))), "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return nil // nothing further down to follow or remove
		}
		if err != nil {
			return fmt.Errorf("inspect %s: %w", rel, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			comp, _ := filepath.Rel(realRoot, cur)
			return &indirectPathError{fmt.Sprintf("%s is reached through a symlink (%s); vp does not follow or remove it",
				rel, filepath.ToSlash(comp))}
		}
		if cur == abs && !fi.Mode().IsRegular() {
			return &indirectPathError{fmt.Sprintf("%s is not a regular file (%s); vp does not remove it",
				rel, fi.Mode().Type())}
		}
	}

	// The resolved path must be the path itself. This catches what the walk
	// cannot name. On Windows EvalSymlinks also normalises letter case and
	// short (8.3) names, so a vault path spelled differently from the disk is
	// refused here too — fail-safe: nothing is removed or written.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", rel, err)
	}
	if resolved != filepath.Clean(abs) {
		return &indirectPathError{fmt.Sprintf("%s resolves to %s, not to itself — a symlink, or on Windows a letter-case "+
			"or short-name difference in the path; vp does not follow or remove it", rel, resolved)}
	}
	return nil
}
