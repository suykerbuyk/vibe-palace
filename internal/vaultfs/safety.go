// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package vaultfs implements safe, vault-relative file accessors used by the
// MCP vp_vault_* tool surface and the `vp vault` CLI subcommands.
//
// All callers supply a relative path (e.g. "Projects/foo/agentctx/notes/x.md")
// which is validated by ValidateRelPath, joined under the configured vault
// root, then resolved through filepath.EvalSymlinks via ResolveSafePath to
// guarantee the realpath stays under the vault.
//
// Functions are generic: they take vaultPath (an absolute vault root) as their
// first argument rather than a schema-typed storage handle, keeping generic
// file operations out of the storage API.
//
// Linux-primary scope: this package does NOT validate against
// Windows-reserved names (CON, PRN, AUX, NUL, COM1-9, LPT1-9). The vibe-palace
// project is Linux-primary and the vault is git-managed. If cross-platform
// support is added later, extend ValidateRelPath accordingly.
//
// IsRefusedWritePath enforces the .git-segment refusal policy
// (case-insensitive, segment-equality only — substring matches such as
// "foo.git/bar" are allowed).
package vaultfs

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidateRelPath checks p is a safe vault-relative path.
//
// Rejects:
//   - empty string
//   - absolute paths (leading "/")
//   - paths containing null bytes (\x00) or other control characters
//     (\x01-\x1f, \x7f)
//   - paths with ".." segments after filepath.Clean
//   - paths whose cleaned form is "." (vault-root reference is incoherent for
//     write/edit/delete)
//
// Returns ErrPathTraversal wrapped with context on rejection.
func ValidateRelPath(p string) error {
	if p == "" {
		return fmt.Errorf("%w: empty path", ErrPathTraversal)
	}
	if p[0] == '/' {
		return fmt.Errorf("%w: absolute path %q", ErrPathTraversal, p)
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == 0x00 {
			return fmt.Errorf("%w: null byte in path", ErrPathTraversal)
		}
		// 0x01-0x1f and 0x7f are control characters.
		if (c >= 0x01 && c <= 0x1f) || c == 0x7f {
			return fmt.Errorf("%w: control character %#x in path", ErrPathTraversal, c)
		}
	}
	// Reject any ".." segment in the raw input. This is stricter than only
	// checking after filepath.Clean: paths like "foo/../bar" clean to "bar"
	// and would otherwise slip through, but we refuse them to deny any
	// escape attempt regardless of whether Clean would absorb it.
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Errorf("%w: %q segment in path", ErrPathTraversal, "..")
		}
	}
	cleaned := filepath.Clean(p)
	if cleaned == "." {
		return fmt.Errorf("%w: path resolves to vault root", ErrPathTraversal)
	}
	// Defense-in-depth: also re-check segments of the cleaned form.
	for _, seg := range strings.Split(cleaned, string(filepath.Separator)) {
		if seg == ".." {
			return fmt.Errorf("%w: %q segment in path", ErrPathTraversal, "..")
		}
	}
	return nil
}

// ResolveSafePath joins relPath under vaultPath, then resolves the result via
// filepath.EvalSymlinks and verifies the realpath stays under vaultPath.
//
// vaultPath must be an absolute path; it is canonicalised through EvalSymlinks
// so the prefix check compares realpaths consistently.
//
// Returns ErrSymlinkEscape if the resolved path is outside the vault.
// Returns ErrPathTraversal via ValidateRelPath if relPath is unsafe.
//
// Note: if the target file does not yet exist (Write to a new file),
// EvalSymlinks fails. In that case, the parent directory's realpath is
// verified instead, which is the correct policy for new files.
func ResolveSafePath(vaultPath, relPath string) (string, error) {
	if err := ValidateRelPath(relPath); err != nil {
		return "", err
	}
	if !filepath.IsAbs(vaultPath) {
		return "", fmt.Errorf("vaultfs: vault path must be absolute, got %q", vaultPath)
	}
	absVault, err := filepath.EvalSymlinks(vaultPath)
	if err != nil {
		return "", fmt.Errorf("vaultfs: resolve vault root: %w", err)
	}

	joined := filepath.Join(absVault, relPath)
	real, err := filepath.EvalSymlinks(joined)
	if err != nil {
		// Target may not exist yet (new file) — fall back to verifying the
		// parent directory stays inside the vault.
		parent := filepath.Dir(joined)
		realParent, perr := filepath.EvalSymlinks(parent)
		if perr != nil {
			// Parent doesn't exist either; trust the cleaned join (atomicfile
			// will MkdirAll) but verify the lexical join stays under vault.
			cleaned := filepath.Clean(joined)
			if !pathIsUnder(cleaned, absVault) {
				return "", fmt.Errorf("%w: %q escapes vault", ErrSymlinkEscape, relPath)
			}
			return cleaned, nil
		}
		if !pathIsUnder(realParent, absVault) {
			return "", fmt.Errorf("%w: parent of %q escapes vault", ErrSymlinkEscape, relPath)
		}
		// Recombine the verified parent with the leaf name.
		return filepath.Join(realParent, filepath.Base(joined)), nil
	}
	if !pathIsUnder(real, absVault) {
		return "", fmt.Errorf("%w: %q escapes vault", ErrSymlinkEscape, relPath)
	}
	return real, nil
}

// pathIsUnder reports whether candidate equals root or is a descendant of it.
func pathIsUnder(candidate, root string) bool {
	if candidate == root {
		return true
	}
	sep := string(filepath.Separator)
	prefix := root
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	return strings.HasPrefix(candidate, prefix)
}

// IsRefusedWritePath reports whether p contains a refused path segment.
//
// Currently the only refused segment is ".git" (case-insensitive).
// Substring matches are NOT refused: "foo.git/bar" is allowed because
// "foo.git" is not equal to ".git" as a segment.
//
// p is filepath.Cleaned and split on path separators; segments are compared
// via strings.EqualFold to catch ".GIT", ".Git", ".gIt" cross-filesystem
// hazards (macOS/NTFS resolve these to the same physical directory; a Linux
// host could mount a case-insensitive filesystem).
func IsRefusedWritePath(p string) bool {
	cleaned := filepath.Clean(p)
	for _, seg := range strings.Split(cleaned, string(filepath.Separator)) {
		if strings.EqualFold(seg, ".git") {
			return true
		}
	}
	return false
}
