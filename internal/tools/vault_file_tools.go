// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Generic vault-relative file accessor MCP tools (vp_vault_*).
//
// All eight tools (read/list/exists/sha256/write/edit/delete/move) operate on
// vault-relative paths and resolve the absolute vault root from the injected
// *storage.Vault (.Root). Tools deliberately do NOT accept a runtime
// vault_path parameter; that would defeat the MCP-as-gatekeeper property.
//
// Path validation, symlink-escape protection (via filepath.EvalSymlinks),
// .git-segment refusal (case-insensitive), atomic writes, and compare-and-set
// semantics are implemented in package internal/vaultfs and reused here. This
// file is a thin JSON-Schema + (de)serialisation wrapper, mirroring the
// vibe-palace tool pattern (mcp.Tool{Name, Description, Schema, Handler}).

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// ---------------------------------------------------------------------------
// vp_vault_read
// ---------------------------------------------------------------------------

var vaultReadSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Vault-relative file path. Required."},
		"max_bytes": {"type": "integer", "description": "Optional read cap, max 10485760 (10 MiB). Default 1048576 (1 MiB)."}
	},
	"required": ["path"]
}`)

// VaultReadTool reads a file at a vault-relative path.
func VaultReadTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_vault_read",
		Description: "Read a file at a vault-relative path. Returns " +
			"{content, bytes, sha256, mtime}. Default cap 1 MiB; pass max_bytes " +
			"(up to 10 MiB) for larger files. Path is vault-relative (e.g. " +
			"'Projects/foo/agentctx/notes/x.md'); absolute paths, '..' segments, " +
			"control characters, and symlinks escaping the vault are rejected.",
		Schema: vaultReadSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Path     string `json:"path"`
				MaxBytes int64  `json:"max_bytes"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.Path == "" {
				return nil, fmt.Errorf("path is required")
			}
			return vaultfs.Read(vault.Root, args.Path, args.MaxBytes)
		},
	}
}

// ---------------------------------------------------------------------------
// vp_vault_list
// ---------------------------------------------------------------------------

var vaultListSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Vault-relative directory path. Required."},
		"include_sha256": {"type": "boolean", "description": "If true, compute SHA-256 for each file entry. Default false."}
	},
	"required": ["path"]
}`)

// Measured 2026-08-12 on the live vault: 2,898,291 bytes for one triples
// directory with include_sha256 — 145x a host's 19,968-byte flat inline cap,
// and there is no `limit` or paging parameter at all. Paging it is a design and
// is filed separately; the sentinel is what it can have today.
type vaultListResult struct {
	Entries []vaultfs.Entry `json:"entries"`

	// 🔴 THE TERMINAL SENTINEL — last field, no omitempty, always true on a
	// successful return. Its ABSENCE is the signal: absent ⇒ the host cut this
	// document and `entries` is a PREFIX. A truncated directory listing is
	// read as a directory that does not contain the file you were looking for,
	// which is how a caller concludes "not there" and writes a duplicate. No
	// omitempty because a false bool would vanish and make "cut" and "whole"
	// serialize identically. Anything declared after this field re-opens the
	// hole.
	Complete bool `json:"complete"`
}

// VaultListTool lists entries one level under a vault-relative directory.
func VaultListTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_vault_list",
		Description: "List entries one level under a vault-relative directory. " +
			"Returns {entries: [{name, type, bytes, sha256?}], complete}. .git entries are " +
			"hidden (case-insensitive). Pass include_sha256=true to compute " +
			"per-file hashes (default off for cheap large-dir listings). The result ENDS with " +
			"`complete`: if you do not see `complete: true`, your host truncated it and `entries` is a " +
			"PREFIX — do not conclude a file is absent, list a narrower path instead.",
		Schema: vaultListSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Path          string `json:"path"`
				IncludeSha256 bool   `json:"include_sha256"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.Path == "" {
				return nil, fmt.Errorf("path is required")
			}
			entries, err := vaultfs.List(vault.Root, args.Path, args.IncludeSha256)
			if err != nil {
				return nil, err
			}
			if entries == nil {
				entries = []vaultfs.Entry{}
			}
			return vaultListResult{Entries: entries, Complete: true}, nil
		},
	}
}

// ---------------------------------------------------------------------------
// vp_vault_exists
// ---------------------------------------------------------------------------

var vaultExistsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Vault-relative path. Required."}
	},
	"required": ["path"]
}`)

// VaultExistsTool reports whether a vault-relative path exists.
func VaultExistsTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_vault_exists",
		Description: "Check whether a vault-relative path exists. Returns " +
			"{exists, type} where type is 'file', 'dir', or '' when not present. " +
			"Dangling symlinks and links whose realpath escapes the vault report " +
			"exists=false.",
		Schema: vaultExistsSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Path string `json:"path"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.Path == "" {
				return nil, fmt.Errorf("path is required")
			}
			return vaultfs.Exists(vault.Root, args.Path)
		},
	}
}

// ---------------------------------------------------------------------------
// vp_vault_sha256
// ---------------------------------------------------------------------------

var vaultSha256Schema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Vault-relative file path. Required."}
	},
	"required": ["path"]
}`)

// VaultSha256Tool computes the SHA-256 of a vault-relative file.
func VaultSha256Tool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_vault_sha256",
		Description: "Compute SHA-256 of a vault-relative file without " +
			"transferring content over MCP. Returns {sha256, bytes, mtime}. " +
			"Bandwidth-friendly for large-file compare-and-set flows.",
		Schema: vaultSha256Schema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Path string `json:"path"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.Path == "" {
				return nil, fmt.Errorf("path is required")
			}
			return vaultfs.Sha256(vault.Root, args.Path)
		},
	}
}

// ---------------------------------------------------------------------------
// vp_vault_write
// ---------------------------------------------------------------------------

var vaultWriteSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Vault-relative file path. Required."},
		"content": {"type": "string", "description": "File content. Required (use empty string to truncate)."},
		"expected_sha256": {"type": "string", "description": "Optional compare-and-set guard: file's current SHA-256 must match or write is refused."}
	},
	"required": ["path", "content"]
}`)

// VaultWriteTool atomically writes content to a vault-relative file.
func VaultWriteTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_vault_write",
		Mutating: true,
		Description: "Atomically write content to a vault-relative file. Returns " +
			"{bytes, sha256, replaced_sha256?}. Refuses any path whose segments " +
			"include '.git' (case-insensitive). Parent directories are created as " +
			"needed. Pass expected_sha256 for compare-and-set: if the file's " +
			"current SHA differs, the write is refused and the file is unchanged.",
		Schema: vaultWriteSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Path           string `json:"path"`
				Content        string `json:"content"`
				ExpectedSha256 string `json:"expected_sha256"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.Path == "" {
				return nil, fmt.Errorf("path is required")
			}
			return vaultfs.Write(vault.Root, args.Path, args.Content, args.ExpectedSha256)
		},
	}
}

// ---------------------------------------------------------------------------
// vp_vault_edit
// ---------------------------------------------------------------------------

var vaultEditSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Vault-relative file path. Required."},
		"old_string": {"type": "string", "description": "String to replace. Required, non-empty."},
		"new_string": {"type": "string", "description": "Replacement. May be empty (deletes occurrences)."},
		"replace_all": {"type": "boolean", "description": "Replace all occurrences. Default false (multi-occurrence is rejected)."},
		"expected_sha256": {"type": "string", "description": "Optional compare-and-set guard."}
	},
	"required": ["path", "old_string"]
}`)

// VaultEditTool replaces old_string with new_string in a vault-relative file.
func VaultEditTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_vault_edit",
		Mutating: true,
		Description: "Replace old_string with new_string in a vault-relative " +
			"file. Returns {bytes, sha256, replacements}. If old_string occurs " +
			"more than once, the call fails unless replace_all=true (mirrors " +
			"Claude Code Edit semantics). Refuses '.git' paths. Pass " +
			"expected_sha256 for compare-and-set.",
		Schema: vaultEditSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Path           string `json:"path"`
				OldString      string `json:"old_string"`
				NewString      string `json:"new_string"`
				ReplaceAll     bool   `json:"replace_all"`
				ExpectedSha256 string `json:"expected_sha256"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.Path == "" {
				return nil, fmt.Errorf("path is required")
			}
			if args.OldString == "" {
				return nil, fmt.Errorf("old_string is required")
			}
			return vaultfs.Edit(vault.Root, args.Path, args.OldString, args.NewString, args.ReplaceAll, args.ExpectedSha256)
		},
	}
}

// ---------------------------------------------------------------------------
// vp_vault_delete
// ---------------------------------------------------------------------------

var vaultDeleteSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Vault-relative file path. Required."},
		"expected_sha256": {"type": "string", "description": "Optional compare-and-set guard."}
	},
	"required": ["path"]
}`)

// VaultDeleteTool deletes a file at a vault-relative path.
func VaultDeleteTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_vault_delete",
		Mutating: true,
		Description: "Delete a file at a vault-relative path. Returns {removed}. " +
			"Refuses directories (file-only in v1) and any path whose segments " +
			"include '.git' (case-insensitive). Pass expected_sha256 for " +
			"compare-and-set.",
		Schema: vaultDeleteSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Path           string `json:"path"`
				ExpectedSha256 string `json:"expected_sha256"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.Path == "" {
				return nil, fmt.Errorf("path is required")
			}
			return vaultfs.Delete(vault.Root, args.Path, args.ExpectedSha256)
		},
	}
}

// ---------------------------------------------------------------------------
// vp_vault_move
// ---------------------------------------------------------------------------

var vaultMoveSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"from_path": {"type": "string", "description": "Source vault-relative path. Required."},
		"to_path": {"type": "string", "description": "Destination vault-relative path. Required."}
	},
	"required": ["from_path", "to_path"]
}`)

// VaultMoveTool renames a vault-relative file.
func VaultMoveTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_vault_move",
		Mutating: true,
		Description: "Rename a vault-relative file from from_path to to_path. " +
			"Returns {moved}. Refuses to overwrite an existing destination. " +
			"Refuses '.git' paths on either endpoint. Refuses same-source-and-" +
			"destination (caller bug). Parent directories on the destination " +
			"side are created implicitly.",
		Schema: vaultMoveSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				FromPath string `json:"from_path"`
				ToPath   string `json:"to_path"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.FromPath == "" {
				return nil, fmt.Errorf("from_path is required")
			}
			if args.ToPath == "" {
				return nil, fmt.Errorf("to_path is required")
			}
			return vaultfs.Move(vault.Root, args.FromPath, args.ToPath)
		},
	}
}

// unmarshalParams decodes JSON params into dst, tolerating empty input.
func unmarshalParams(params json.RawMessage, dst any) error {
	if len(params) == 0 {
		return nil
	}
	if err := json.Unmarshal(params, dst); err != nil {
		return fmt.Errorf("parse params: %w", err)
	}
	return nil
}
