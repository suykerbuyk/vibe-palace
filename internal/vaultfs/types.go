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

	// ErrFileNotFound is returned when a target file does not exist. It
	// wraps fs.ErrNotExist via errors.Join in the constructing call site.
	ErrFileNotFound = errors.New("vaultfs: file not found")

	// ErrShaConflict is returned by compare-and-set write/edit/delete when
	// the file's current SHA-256 does not match the caller-supplied
	// expected_sha256.
	ErrShaConflict = errors.New("vaultfs: sha256 conflict (compare-and-set failed)")
)

// Content is the result of a successful Read.
type Content struct {
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
	Exists bool   `json:"exists"`
	Type   string `json:"type"` // "file", "dir", or "" when not exists
}

// WriteResult is returned by Write.
type WriteResult struct {
	Bytes          int64  `json:"bytes"`
	Sha256         string `json:"sha256"`
	ReplacedSha256 string `json:"replaced_sha256,omitempty"`
}

// EditResult is returned by Edit.
type EditResult struct {
	Bytes        int64  `json:"bytes"`
	Sha256       string `json:"sha256"`
	Replacements int    `json:"replacements"`
}

// DeleteResult is returned by Delete.
type DeleteResult struct {
	Removed bool `json:"removed"`
}

// MoveResult is returned by Move.
type MoveResult struct {
	Moved bool `json:"moved"`
}

// Sha256Result is returned by Sha256.
type Sha256Result struct {
	Sha256 string    `json:"sha256"`
	Bytes  int64     `json:"bytes"`
	Mtime  time.Time `json:"mtime"`
}
