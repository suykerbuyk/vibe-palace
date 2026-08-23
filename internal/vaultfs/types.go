// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"errors"
	"time"
)

// Sentinel errors returned by vaultfs operations. Callers should compare with
// errors.Is to avoid coupling to wrapped error chains.
var (
	// ErrPathTraversal is returned when a relative path fails validation,
	// e.g. it contains "..", an absolute prefix, null bytes, control chars,
	// is empty, or cleans to ".".
	ErrPathTraversal = errors.New("vaultfs: path traversal rejected")

	// ErrRefusedPath is returned when a path's segments include a refused
	// segment (e.g. ".git" case-insensitively).
	ErrRefusedPath = errors.New("vaultfs: refused path")

	// ErrSymlinkEscape is returned when a path resolves (via EvalSymlinks)
	// outside the vault root.
	ErrSymlinkEscape = errors.New("vaultfs: symlink escape rejected")

	// ErrDestinationInsideVault is returned by RefuseDestinationInsideVault
	// when an export destination resolves INSIDE the vault. It is the inverse
	// of ErrSymlinkEscape: that one guards a vault-relative path leaving the
	// vault, this one guards an operator-typed host path entering it.
	ErrDestinationInsideVault = errors.New("vaultfs: destination resolves inside the vault")

	// ErrUnportableName is returned when a path segment cannot be represented on
	// NTFS/exFAT — a reserved character (< > : " \ | ? *), a Windows reserved
	// device name (CON, PRN, AUX, NUL, COM1-9, LPT1-9), a trailing dot or space,
	// or an over-length segment. Windows and darwin are shipped release targets,
	// so a name that is fine on the Linux host but illegal on those filesystems
	// makes the synced vault un-checkout-able there.
	ErrUnportableName = errors.New("vaultfs: unportable filename rejected")

	// ErrFileNotFound is returned when a target file does not exist. It
	// wraps fs.ErrNotExist via errors.Join in the constructing call site.
	ErrFileNotFound = errors.New("vaultfs: file not found")

	// ErrShaConflict is returned by compare-and-set write/edit/delete when
	// the file's current SHA-256 does not match the caller-supplied
	// expected_sha256.
	ErrShaConflict = errors.New("vaultfs: sha256 conflict (compare-and-set failed)")
)

// SourceUnknown is what a result reports when nobody told it where its vault
// binding came from. ADR-006: absence is not a value — a result that cannot
// name its source says so, rather than omitting the field (indistinguishable
// from "not applicable") or guessing (a silent lie wearing the face of a
// measurement).
const SourceUnknown = "unknown"

// VaultBinding names the vault a file operation acted on. It is embedded in
// every file-CRUD result, so the answer to "which vault did this touch?" ships
// with the bytes instead of requiring a `find` or a cross-check against another
// command.
//
// VaultPath is set by vaultfs itself from the root it was handed, so it is the
// vault the operation ACTUALLY used, by construction — it cannot drift from the
// bytes it describes. VaultPathSource is a fact about how the CALLER resolved
// that root, which vaultfs cannot know, so the caller stamps it; unstamped, it
// reads SourceUnknown.
//
// Embedded, so the JSON stays flat: {"bytes":…,"vault_path":…}.
type VaultBinding struct {
	VaultPath       string `json:"vault_path"`
	VaultPathSource string `json:"vault_path_source"`
}

// bind builds the binding for a result produced against vaultPath.
func bind(vaultPath string) VaultBinding {
	return VaultBinding{VaultPath: vaultPath, VaultPathSource: SourceUnknown}
}

// Content is the result of a successful Read.
type Content struct {
	VaultBinding
	Content string    `json:"content"`
	Bytes   int64     `json:"bytes"`
	Sha256  string    `json:"sha256"`
	Mtime   time.Time `json:"mtime"`
}

// Entry is a single result row from List.
type Entry struct {
	Name   string `json:"name"`
	Type   string `json:"type"` // "file" or "dir"
	Bytes  int64  `json:"bytes"`
	Sha256 string `json:"sha256,omitempty"`
}

// Existence is the result of an Exists query.
type Existence struct {
	VaultBinding
	Exists bool   `json:"exists"`
	Type   string `json:"type"` // "file", "dir", or "" when not exists
}

// WriteResult is returned by Write.
type WriteResult struct {
	VaultBinding
	Bytes          int64  `json:"bytes"`
	Sha256         string `json:"sha256"`
	ReplacedSha256 string `json:"replaced_sha256,omitempty"`
}

// EditResult is returned by Edit.
type EditResult struct {
	VaultBinding
	Bytes        int64  `json:"bytes"`
	Sha256       string `json:"sha256"`
	Replacements int    `json:"replacements"`
}

// DeleteResult is returned by Delete.
type DeleteResult struct {
	VaultBinding
	Removed bool `json:"removed"`
}

// MoveResult is returned by Move.
type MoveResult struct {
	VaultBinding
	Moved bool `json:"moved"`
}

// Sha256Result is returned by Sha256.
type Sha256Result struct {
	VaultBinding
	Sha256 string    `json:"sha256"`
	Bytes  int64     `json:"bytes"`
	Mtime  time.Time `json:"mtime"`
}
