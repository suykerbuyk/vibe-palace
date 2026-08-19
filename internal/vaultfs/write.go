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
	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/scopetoken"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// Write places content at relPath under vaultPath atomically.
//
// All write-side operations refuse paths whose segments include ".git"
// case-insensitively and resolve through the safety layer for traversal and
// symlink-escape protection.
//
// expectedSha256, if non-empty, is the compare-and-set guard: the file's
// current SHA-256 must match or ErrShaConflict is returned and the file is
// left untouched. When the file does not exist and expectedSha256 is supplied,
// the call returns an error (the caller asserted a prior version that isn't
// there).
//
// Atomicity is delegated to atomicfile.Write; vaultfs always passes the vault
// root so the write triggers MCP surface stamping. Parent directories are
// created implicitly by atomicfile.
func Write(vaultPath, relPath, content, expectedSha256 string) (WriteResult, error) {
	if IsRefusedWritePath(relPath) {
		return WriteResult{}, fmt.Errorf("%w: %s", ErrRefusedPath, relPath)
	}
	abs, err := ResolveSafePath(vaultPath, relPath)
	if err != nil {
		return WriteResult{}, err
	}

	// Serialize the whole read→compute→write under an exclusive advisory lock
	// so concurrent writers cannot lose updates (TOCTOU between the sha pre-read
	// and atomicfile.Write). Acquired after path validation/resolution so a
	// rejected path never creates a lock sidecar.
	release, lerr := vaultlock.Acquire(vaultPath, abs)
	if lerr != nil {
		return WriteResult{}, fmt.Errorf("vaultfs: lock %s: %w", relPath, lerr)
	}
	defer release()

	var replacedSha string
	if existing, rerr := os.ReadFile(abs); rerr == nil {
		sum := sha256.Sum256(existing)
		replacedSha = hex.EncodeToString(sum[:])
		if expectedSha256 != "" && replacedSha != expectedSha256 {
			return WriteResult{}, fmt.Errorf("%w: have %s, expected %s", ErrShaConflict, replacedSha, expectedSha256)
		}
		// Checked whether or not a digest was supplied: an empty
		// expectedSha256 means "no CAS" on this path, so a bake through the
		// unguarded overwrite is the EASIER one, not an edge case.
		if err := scopetoken.CheckWriteBack(relPath, string(existing), content); err != nil {
			return WriteResult{}, err
		}
	} else if !errors.Is(rerr, fs.ErrNotExist) {
		return WriteResult{}, fmt.Errorf("vaultfs: pre-read %s: %w", relPath, rerr)
	} else if expectedSha256 != "" {
		return WriteResult{}, fmt.Errorf("%w: file %s does not exist but expected_sha256 was supplied", ErrShaConflict, relPath)
	}

	data := []byte(content)
	// atomicfile.Write is vibe-palace's 3-param whole-file primitive
	// (vaultRoot, absPath, data, opts...). Passing the vault root makes it
	// best-effort stamp the MCP surface version after a successful write.
	// absPath must be absolute; ResolveSafePath guarantees that.
	if err := atomicfile.Write(vaultPath, abs, data); err != nil {
		return WriteResult{}, fmt.Errorf("vaultfs: atomic write %s: %w", relPath, err)
	}
	sum := sha256.Sum256(data)
	res := WriteResult{
		VaultBinding: bind(vaultPath),
		Bytes:        int64(len(data)),
		Sha256:       hex.EncodeToString(sum[:]),
	}
	if replacedSha != "" {
		res.ReplacedSha256 = replacedSha
	}
	return res, nil
}

// Edit replaces oldString with newString in the file at relPath.
//
// If oldString occurs more than once, the call fails with an error suggesting
// replace_all=true. Setting replaceAll=true permits multi-occurrence
// replacement and returns the count of replacements made.
//
// expectedSha256 is the same compare-and-set guard as Write.
func Edit(vaultPath, relPath, oldString, newString string, replaceAll bool, expectedSha256 string) (EditResult, error) {
	if IsRefusedWritePath(relPath) {
		return EditResult{}, fmt.Errorf("%w: %s", ErrRefusedPath, relPath)
	}
	abs, err := ResolveSafePath(vaultPath, relPath)
	if err != nil {
		return EditResult{}, err
	}
	// Hold an exclusive advisory lock across the entire read→replace→write so
	// concurrent edits to the same file cannot clobber each other's changes.
	release, lerr := vaultlock.Acquire(vaultPath, abs)
	if lerr != nil {
		return EditResult{}, fmt.Errorf("vaultfs: lock %s: %w", relPath, lerr)
	}
	defer release()
	existing, err := os.ReadFile(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return EditResult{}, fmt.Errorf("%w: %s", ErrFileNotFound, relPath)
		}
		return EditResult{}, fmt.Errorf("vaultfs: read %s: %w", relPath, err)
	}
	if expectedSha256 != "" {
		sum := sha256.Sum256(existing)
		current := hex.EncodeToString(sum[:])
		if current != expectedSha256 {
			return EditResult{}, fmt.Errorf("%w: have %s, expected %s", ErrShaConflict, current, expectedSha256)
		}
	}
	// These three are CALLER errors — the caller passed an old_string the file
	// cannot satisfy. Wrapped as apperr.CallerError (propagated up via %w) so the
	// MCP seam counts them as friction, not health faults. old_string-not-found on
	// resume.md was the single highest-frequency amber-maker in the evidence.
	if oldString == "" {
		return EditResult{}, apperr.Caller(fmt.Errorf("vaultfs: edit %s: old_string must be non-empty", relPath))
	}
	count := strings.Count(string(existing), oldString)
	if count == 0 {
		return EditResult{}, apperr.Caller(fmt.Errorf("vaultfs: edit %s: old_string not found", relPath))
	}
	if count > 1 && !replaceAll {
		return EditResult{}, apperr.Caller(fmt.Errorf("vaultfs: edit %s: old_string occurs %d times; pass replace_all=true to replace all", relPath, count))
	}

	var updated string
	var replacements int
	if replaceAll {
		updated = strings.ReplaceAll(string(existing), oldString, newString)
		replacements = count
	} else {
		updated = strings.Replace(string(existing), oldString, newString, 1)
		replacements = 1
	}
	data := []byte(updated)
	if err := atomicfile.Write(vaultPath, abs, data); err != nil {
		return EditResult{}, fmt.Errorf("vaultfs: atomic write %s: %w", relPath, err)
	}
	sum := sha256.Sum256(data)
	return EditResult{
		VaultBinding: bind(vaultPath),
		Bytes:        int64(len(data)),
		Sha256:       hex.EncodeToString(sum[:]),
		Replacements: replacements,
	}, nil
}

// Delete removes the file at relPath under vaultPath.
//
// File-only: if the target is a directory, returns an informative error;
// recursive directory delete is out of scope for v1.
//
// expectedSha256 is the compare-and-set guard.
func Delete(vaultPath, relPath, expectedSha256 string) (DeleteResult, error) {
	if IsRefusedWritePath(relPath) {
		return DeleteResult{}, fmt.Errorf("%w: %s", ErrRefusedPath, relPath)
	}
	abs, err := ResolveSafePath(vaultPath, relPath)
	if err != nil {
		return DeleteResult{}, err
	}
	// Hold an exclusive advisory lock across the stat/pre-read→remove so the
	// delete-vs-recreate race (and the sha compare-and-set) is serialized.
	release, lerr := vaultlock.Acquire(vaultPath, abs)
	if lerr != nil {
		return DeleteResult{}, fmt.Errorf("vaultfs: lock %s: %w", relPath, lerr)
	}
	defer release()
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return DeleteResult{}, fmt.Errorf("%w: %s", ErrFileNotFound, relPath)
		}
		return DeleteResult{}, fmt.Errorf("vaultfs: stat %s: %w", relPath, err)
	}
	if info.IsDir() {
		return DeleteResult{}, fmt.Errorf("vaultfs: %s is a directory; recursive directory delete is not supported in v1 (file-only)", relPath)
	}
	if expectedSha256 != "" {
		existing, rerr := os.ReadFile(abs)
		if rerr != nil {
			return DeleteResult{}, fmt.Errorf("vaultfs: pre-read %s: %w", relPath, rerr)
		}
		sum := sha256.Sum256(existing)
		current := hex.EncodeToString(sum[:])
		if current != expectedSha256 {
			return DeleteResult{}, fmt.Errorf("%w: have %s, expected %s", ErrShaConflict, current, expectedSha256)
		}
	}
	// Delete uses os.Remove directly: it removes content rather than writing
	// new content, so it does not route through atomicfile (and thus does not
	// trigger surface stamping). Stamping tracks content-write semantics.
	if err := os.Remove(abs); err != nil {
		return DeleteResult{}, fmt.Errorf("vaultfs: remove %s: %w", relPath, err)
	}
	return DeleteResult{VaultBinding: bind(vaultPath), Removed: true}, nil
}

// Move renames a file from fromPath to toPath under vaultPath.
//
// Both endpoints are subject to .git-segment refusal. fromPath == toPath
// returns an error (caller bug, fail loud). Refuses to overwrite an existing
// destination.
//
// Implementation note: parent directories on the destination side are
// created implicitly via os.MkdirAll on the destination's parent so callers
// can move into not-yet-existing folders consistent with Write's behaviour.
func Move(vaultPath, fromPath, toPath string) (MoveResult, error) {
	if IsRefusedWritePath(fromPath) {
		return MoveResult{}, fmt.Errorf("%w: %s", ErrRefusedPath, fromPath)
	}
	if IsRefusedWritePath(toPath) {
		return MoveResult{}, fmt.Errorf("%w: %s", ErrRefusedPath, toPath)
	}
	if filepath.Clean(fromPath) == filepath.Clean(toPath) {
		return MoveResult{}, fmt.Errorf("vaultfs: move source and destination are the same path: %s", fromPath)
	}
	srcAbs, err := ResolveSafePath(vaultPath, fromPath)
	if err != nil {
		return MoveResult{}, err
	}
	dstAbs, err := ResolveSafePath(vaultPath, toPath)
	if err != nil {
		return MoveResult{}, err
	}
	// src is only being renamed away; dst is the at-risk created path. Locking
	// the single dst path (rather than both endpoints) avoids a two-lock
	// ordering deadlock while still guarding the file being created.
	release, lerr := vaultlock.Acquire(vaultPath, dstAbs)
	if lerr != nil {
		return MoveResult{}, fmt.Errorf("vaultfs: lock %s: %w", toPath, lerr)
	}
	defer release()
	if _, statErr := os.Stat(srcAbs); statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return MoveResult{}, fmt.Errorf("%w: %s", ErrFileNotFound, fromPath)
		}
		return MoveResult{}, fmt.Errorf("vaultfs: stat %s: %w", fromPath, statErr)
	}
	if _, statErr := os.Stat(dstAbs); statErr == nil {
		return MoveResult{}, fmt.Errorf("vaultfs: move destination %s already exists", toPath)
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return MoveResult{}, fmt.Errorf("vaultfs: stat %s: %w", toPath, statErr)
	}
	if err := os.MkdirAll(filepath.Dir(dstAbs), 0o755); err != nil {
		return MoveResult{}, fmt.Errorf("vaultfs: mkdir %s parent: %w", toPath, err)
	}
	// Move uses os.Rename directly: it does not write new content, so it
	// does not route through atomicfile (and thus does not trigger surface
	// stamping). Stamping tracks content-write semantics.
	if err := os.Rename(srcAbs, dstAbs); err != nil {
		return MoveResult{}, fmt.Errorf("vaultfs: rename %s -> %s: %w", fromPath, toPath, err)
	}
	return MoveResult{VaultBinding: bind(vaultPath), Moved: true}, nil
}
