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
// Cross-platform scope: ValidateRelPath ALSO rejects filenames that cannot be
// represented on NTFS/exFAT — reserved characters, Windows reserved device names
// (CON, PRN, AUX, NUL, COM1-9, LPT1-9), and trailing dot/space — via
// ValidatePortableSegment. Windows and darwin are shipped release targets, so the
// vault must stay checkout-able there; a name legal only on the Linux host would
// break the sync everywhere else. ValidatePortableSegment is exported so the
// vaultaudit memory-portability dimension checks against the SAME rules the write
// path enforces, keeping one source of truth for what "portable" means.
//
// IsRefusedWritePath enforces the refused-segment policy for ".git" and
// ".vp-locks" (case-insensitive, segment-equality only — substring matches such
// as "foo.git/bar" are allowed).
package vaultfs

import (
	"fmt"
	"path/filepath"
	"slices"
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
	if slices.Contains(strings.Split(p, "/"), "..") {
		return fmt.Errorf("%w: %q segment in path", ErrPathTraversal, "..")
	}
	cleaned := filepath.Clean(p)
	if cleaned == "." {
		return fmt.Errorf("%w: path resolves to vault root", ErrPathTraversal)
	}
	// Defense-in-depth: also re-check segments of the cleaned form.
	if slices.Contains(strings.Split(cleaned, string(filepath.Separator)), "..") {
		return fmt.Errorf("%w: %q segment in path", ErrPathTraversal, "..")
	}
	// Cross-platform portability: every segment (each directory name and the
	// leaf) must be representable on NTFS/exFAT, or the synced vault cannot be
	// checked out on the Windows/darwin release targets.
	for seg := range strings.SplitSeq(cleaned, string(filepath.Separator)) {
		if seg == "" {
			continue
		}
		if err := ValidatePortableSegment(seg); err != nil {
			return err
		}
	}
	return nil
}

// portableReservedChars are the characters no NTFS/exFAT filename may contain.
// The path separator "/" is deliberately absent — it delimits segments and is
// handled structurally by ValidateRelPath. A literal backslash IS rejected: it is
// a separator on Windows, so a "\" inside a vault-relative segment would change
// the path's meaning on checkout. Control characters are rejected separately by
// ValidateRelPath.
const portableReservedChars = `<>:"\|?*`

// reservedDeviceNames are the Windows reserved device basenames. A file whose
// name (or whose stem before the first ".") equals one of these — case-insensitively,
// with or without an extension, e.g. "NUL", "nul.md", "COM1.txt" — cannot be
// created or checked out on Windows.
var reservedDeviceNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidatePortableSegment reports whether a single path segment — one filename or
// directory name, never a multi-segment path — is representable on NTFS/exFAT.
// It is the ONE definition of "portable name" shared by the write path
// (ValidateRelPath) and the vaultaudit memory-portability dimension, so the
// audit flags exactly what a write would refuse and the two cannot drift.
//
// Rejects (all with ErrUnportableName): reserved characters, a Windows reserved
// device name, a trailing dot or space (Windows silently strips these, so the
// name you wrote is not the name on disk), and a segment over 255 bytes (the
// common per-component filesystem limit).
func ValidatePortableSegment(seg string) error {
	if seg == "" {
		return fmt.Errorf("%w: empty segment", ErrUnportableName)
	}
	if i := strings.IndexAny(seg, portableReservedChars); i >= 0 {
		return fmt.Errorf("%w: segment %q contains %q, illegal on NTFS/exFAT",
			ErrUnportableName, seg, seg[i])
	}
	if last := seg[len(seg)-1]; last == '.' || last == ' ' {
		return fmt.Errorf("%w: segment %q ends with a dot or space, illegal on Windows",
			ErrUnportableName, seg)
	}
	stem, _, _ := strings.Cut(seg, ".")
	if reservedDeviceNames[strings.ToLower(stem)] {
		return fmt.Errorf("%w: segment %q is a Windows reserved device name",
			ErrUnportableName, seg)
	}
	if len(seg) > 255 {
		return fmt.Errorf("%w: segment is %d bytes, over the 255-byte filesystem limit",
			ErrUnportableName, len(seg))
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
// The refused segments are ".git" and ".vp-locks" (both case-insensitive).
// ".vp-locks" is the vaultlock sidecar directory; the generic vaultfs surface
// must never write content into it. Substring matches are NOT refused:
// "foo.git/bar" and "foo.vp-locks/bar" are allowed because the segments are not
// equal to ".git"/".vp-locks".
//
// p is filepath.Cleaned and split on path separators; segments are compared
// via strings.EqualFold to catch ".GIT", ".Git", ".gIt" cross-filesystem
// hazards (macOS/NTFS resolve these to the same physical directory; a Linux
// host could mount a case-insensitive filesystem).
func IsRefusedWritePath(p string) bool {
	cleaned := filepath.Clean(p)
	for seg := range strings.SplitSeq(cleaned, string(filepath.Separator)) {
		if strings.EqualFold(seg, ".git") || strings.EqualFold(seg, ".vp-locks") {
			return true
		}
	}
	return false
}
