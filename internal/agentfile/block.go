// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package agentfile detects well-known AI agent instruction files
// (CLAUDE.md, AGENTS.md, .cursorrules, .rules,
// .github/copilot-instructions.md) in a project root and wires in a
// delimited managed block that tells agents how to bootstrap the
// vibe-palace context.
package agentfile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// blockVersion bumps when the block's schema (not just the body text) changes
// in a way downstream tooling needs to distinguish.
const blockVersion = 1

// blockBody is the canonical content between the open and close delimiters.
// Changes to this constant propagate automatically to existing files on the
// next `vp init` via the content-hash mismatch → update path.
const blockBody = "## Vibe-Palace Integration\n" +
	"\n" +
	"- Call `vp_bootstrap_context` at session start to load project context,\n" +
	"  resume, active tasks, recent sessions, and the command manifest.\n" +
	"- When the user types `vpc-<name>` (for example `vpc-wrap`,\n" +
	"  `vpc-restart`, `vpc-review-plan`), call `vp_get_command(\"<name>\")`\n" +
	"  and follow the returned instructions.\n" +
	"- Use `vp_list_commands` to see all commands currently available for\n" +
	"  this project."

const blockCloseDelim = "<!-- vibe-palace:end -->"

// blockContentHash returns the first 7 hex chars of the SHA-256 of blockBody.
// Short enough to be unobtrusive in the delimiter, long enough to be unique
// in practice across block revisions.
func blockContentHash() string {
	sum := sha256.Sum256([]byte(blockBody))
	return hex.EncodeToString(sum[:])[:7]
}

// blockOpenDelim returns the opening HTML-comment delimiter, carrying the
// schema version and the content hash. Phase 3's `vp commands upgrade` uses
// the hash to detect staleness without a full diff.
func blockOpenDelim() string {
	return fmt.Sprintf("<!-- vibe-palace:begin v=%d sha=%s -->", blockVersion, blockContentHash())
}

// managedBlock returns the full block (delimiters + body) with LF line
// endings. The wirer translates to CRLF when the host file uses CRLF.
func managedBlock() string {
	return blockOpenDelim() + "\n" + blockBody + "\n" + blockCloseDelim
}
