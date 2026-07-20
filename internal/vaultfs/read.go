// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
)

// Size caps for Read: 1 MiB default, settable up to 10 MiB.
const (
	defaultReadCap = 1 << 20        // 1 MiB
	maxReadCap     = 10 * (1 << 20) // 10 MiB
)

// Read returns the file contents at relPath under vaultPath.
//
// maxBytes: if 0, defaults to defaultReadCap (1 MiB). Up to maxReadCap
// (10 MiB) is allowed; values above maxReadCap return an error suggesting the
// caller cap the read explicitly. Files larger than the active cap also
// return an error.
//
// Returns ErrFileNotFound (wrapping fs.ErrNotExist) when the file is missing.
func Read(vaultPath, relPath string, maxBytes int64) (Content, error) {
	abs, err := ResolveSafePath(vaultPath, relPath)
	if err != nil {
		return Content{}, err
	}

	cap := maxBytes
	if cap <= 0 {
		cap = defaultReadCap
	}
	if cap > maxReadCap {
		// CALLER error: the caller asked for a cap above the hard limit.
		return Content{}, apperr.Caller(fmt.Errorf("vaultfs: max_bytes %d exceeds hard limit %d (10 MiB)", cap, maxReadCap))
	}

	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Content{}, fmt.Errorf("%w: %s", ErrFileNotFound, relPath)
		}
		return Content{}, fmt.Errorf("vaultfs: stat %s: %w", relPath, err)
	}
	if info.IsDir() {
		return Content{}, fmt.Errorf("vaultfs: %s is a directory, not a file", relPath)
	}
	if info.Size() > cap {
		// CALLER error: the file exceeds the caller's OWN cap. The remedy is in the
		// message (raise max_bytes); it is not a fault of the tool.
		return Content{}, apperr.Caller(fmt.Errorf("vaultfs: file %s is %d bytes, exceeds cap %d (set max_bytes up to %d)", relPath, info.Size(), cap, maxReadCap))
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return Content{}, fmt.Errorf("vaultfs: read %s: %w", relPath, err)
	}
	sum := sha256.Sum256(data)
	return Content{
		Content: string(data),
		Bytes:   info.Size(),
		Sha256:  hex.EncodeToString(sum[:]),
		Mtime:   info.ModTime(),
	}, nil
}

// List returns the entries one level below relPath under vaultPath.
//
// Entries whose name matches ".git" case-insensitively are filtered out. If
// includeSha256 is true, each file entry's contents are read and hashed;
// directory entries never have a SHA-256.
func List(vaultPath, relPath string, includeSha256 bool) ([]Entry, error) {
	abs, err := ResolveSafePath(vaultPath, relPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrFileNotFound, relPath)
		}
		return nil, fmt.Errorf("vaultfs: stat %s: %w", relPath, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("vaultfs: %s is not a directory", relPath)
	}
	dirents, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("vaultfs: read dir %s: %w", relPath, err)
	}
	out := make([]Entry, 0, len(dirents))
	for _, d := range dirents {
		if strings.EqualFold(d.Name(), ".git") {
			continue
		}
		fullPath := filepath.Join(abs, d.Name())
		fi, ferr := os.Stat(fullPath)
		if ferr != nil {
			// Skip entries we can't stat (e.g. dangling symlink); maintain
			// best-effort listing.
			continue
		}
		entry := Entry{
			Name:  d.Name(),
			Bytes: fi.Size(),
		}
		if fi.IsDir() {
			entry.Type = "dir"
			entry.Bytes = 0
		} else {
			entry.Type = "file"
			if includeSha256 {
				data, rerr := os.ReadFile(fullPath)
				if rerr == nil {
					sum := sha256.Sum256(data)
					entry.Sha256 = hex.EncodeToString(sum[:])
				}
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// Exists reports whether relPath exists under vaultPath.
//
// Uses os.Lstat first to detect symlink presence (succeeds even on dangling
// links), then filepath.EvalSymlinks to verify the link resolves to a
// reachable target inside the vault. Dangling symlinks (or symlinks whose
// realpath escapes the vault) are reported as {exists: false, type: ""}.
func Exists(vaultPath, relPath string) (Existence, error) {
	if err := ValidateRelPath(relPath); err != nil {
		return Existence{}, err
	}
	if !filepath.IsAbs(vaultPath) {
		return Existence{}, fmt.Errorf("vaultfs: vault path must be absolute, got %q", vaultPath)
	}
	absVault, err := filepath.EvalSymlinks(vaultPath)
	if err != nil {
		return Existence{}, fmt.Errorf("vaultfs: resolve vault root: %w", err)
	}
	joined := filepath.Join(absVault, relPath)

	if _, lerr := os.Lstat(joined); lerr != nil {
		if errors.Is(lerr, fs.ErrNotExist) {
			return Existence{Exists: false}, nil
		}
		return Existence{}, fmt.Errorf("vaultfs: lstat %s: %w", relPath, lerr)
	}

	real, err := filepath.EvalSymlinks(joined)
	if err != nil {
		// Dangling symlink (or other resolution failure): not reachable.
		return Existence{Exists: false}, nil
	}
	if !pathIsUnder(real, absVault) {
		// Symlink escapes vault: from the vault's perspective, it's not
		// addressable.
		return Existence{Exists: false}, nil
	}
	info, err := os.Stat(real)
	if err != nil {
		return Existence{Exists: false}, nil
	}
	if info.IsDir() {
		return Existence{Exists: true, Type: "dir"}, nil
	}
	return Existence{Exists: true, Type: "file"}, nil
}

// Sha256 returns the SHA-256, size, and mtime of the file at relPath under
// vaultPath without reading the content over the MCP boundary. Useful for
// large-file compare-and-set workflows where the caller only needs the
// fingerprint.
func Sha256(vaultPath, relPath string) (Sha256Result, error) {
	abs, err := ResolveSafePath(vaultPath, relPath)
	if err != nil {
		return Sha256Result{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Sha256Result{}, fmt.Errorf("%w: %s", ErrFileNotFound, relPath)
		}
		return Sha256Result{}, fmt.Errorf("vaultfs: stat %s: %w", relPath, err)
	}
	if info.IsDir() {
		return Sha256Result{}, fmt.Errorf("vaultfs: %s is a directory, not a file", relPath)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return Sha256Result{}, fmt.Errorf("vaultfs: read %s: %w", relPath, err)
	}
	sum := sha256.Sum256(data)
	return Sha256Result{
		Sha256: hex.EncodeToString(sum[:]),
		Bytes:  info.Size(),
		Mtime:  info.ModTime(),
	}, nil
}
