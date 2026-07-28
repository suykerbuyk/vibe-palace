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
const blockVersion = 2

// CommandToolName and SkillToolName are the canonical MCP tool names the
// managed block and bootstrap directive point users at. Exporting them from
// this package gives one source of truth that both internal/agentfile's
// block copy and internal/tools's CommandInvocation directive reference,
// preventing the drift that the block sha-mismatch path alone cannot catch.
const (
	CommandToolName = "vp_cmd"
	SkillToolName   = "vp_skill"
)

// blockBody is the canonical content between the open and close delimiters.
// Phrased as a BEFORE/call imperative rather than a bullet list — the
// previous passive form was treated as reference material and deferred
// instead of executed on session start. Changes propagate to existing
// files on the next `vp init` via the content-hash mismatch → update path.
var blockBody = "## Vibe-Palace Integration\n" +
	"\n" +
	"BEFORE responding to the user's first message in a new session, call\n" +
	"`vp_bootstrap_context` with the project slug to load project context,\n" +
	"resume, active tasks, recent sessions, and the command and skill\n" +
	"manifests — for example `{\"project\":\"<slug>\"}`. Prefer always naming\n" +
	"`project`; on stdio MCP the server may derive it from a high-confidence\n" +
	"cwd marker when omitted. Do this even if the first message seems trivial\n" +
	"— the returned payload shapes every subsequent response.\n" +
	"\n" +
	"When the user types `vpc-<name>` (for example `vpc-wrap`, `vpc-restart`),\n" +
	"call `" + CommandToolName + "` with `name=<name>`, follow the returned\n" +
	"instructions, and return to your normal posture when done. Commands are\n" +
	"one-shot.\n" +
	"\n" +
	"When the user types `vps-<name>` (for example `vps-startup-analyst`), call\n" +
	"`" + SkillToolName + "` with `name=<name>` and adopt the returned persona and\n" +
	"objectives as STANDING instruction for the rest of this session. Stay in\n" +
	"that posture until the user types `vps-clear`, or types\n" +
	"`vps-replace:<other>` (a model-parsed prefix parsed by you locally — strip\n" +
	"`replace:` and call `" + SkillToolName + "` with `name=<other>` after dropping all\n" +
	"prior personas), or a new session starts. Multiple `vps-*` invocations\n" +
	"stack additively unless replaced. `vps-clear` drops all active personas.\n" +
	"`vps-replace:` is a model-parsed prefix, not a tool parameter — you strip\n" +
	"the prefix yourself before calling `" + SkillToolName + "`."

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

// ExpectedSha returns the current content-hash the wire layer would embed.
// Callers comparing existing agent files against the embedded source of truth
// should compare their sha=... token to this value.
func ExpectedSha() string { return blockContentHash() }
