// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

const CodexRawAdapterName = "codex-raw"
const CodexRawAdapterVersion = "1.0.0"

// codexRawAdapter archives the exact file Codex supplied without interpreting
// its private rollout format as another host's transcript schema.
type codexRawAdapter struct{}

func (codexRawAdapter) Name() string    { return CodexRawAdapterName }
func (codexRawAdapter) Version() string { return CodexRawAdapterVersion }

func (codexRawAdapter) ResolveSource(opts CreateOptions) (string, func(), error) {
	if opts.SourcePath == "" {
		return "", noopCleanup, fmt.Errorf("codex-raw: transcript source path is required")
	}
	info, err := os.Stat(opts.SourcePath)
	if err != nil {
		return "", noopCleanup, fmt.Errorf("codex-raw: stat transcript: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return "", noopCleanup, fmt.Errorf("codex-raw: transcript must be a non-empty regular file")
	}
	return opts.SourcePath, noopCleanup, nil
}

func (codexRawAdapter) Inspect(sourcePath string) (int, string, error) {
	return inspectCodexRollout(sourcePath)
}

// codexRolloutRecord is the part of a Codex rollout record inspectCodexRollout
// reads. turn_context carries no payload.type, so Model is read from any record
// that has one.
type codexRolloutRecord struct {
	Type    string `json:"type"`
	Payload struct {
		Type  string `json:"type"`
		Role  string `json:"role"`
		Model string `json:"model"`
	} `json:"payload"`
}

// inspectCodexRollout counts a Codex rollout's conversational turns and reads
// its model, by Codex's own record rules rather than Claude's.
//
// A turn is a response_item whose payload.type is "message" and whose role is
// user or assistant. developer messages are injected instruction text; tool
// traffic (custom_tool_call, custom_tool_call_output, function_call,
// function_call_output, reasoning) and agent_message (sub-agent relay) are
// different payload types and are not turns.
//
// 🔴 This count is deliberately NOT comparable with InspectClaudeJSONL's, which
// counts top-level type == "user"/"assistant" and so includes Claude's
// tool-result records. turn_count is a per-adapter convenience field; the two
// numbers describe different things and nothing in the tree compares them.
//
// The model is the LAST non-empty payload.model seen, matching
// InspectClaudeJSONL. Codex also repeats it under
// event_msg/thread_settings_applied as payload.thread_settings.model; across
// the live corpus the two never disagree, so the second source is not read.
//
// Counting is best-effort: a line that does not decode is skipped, because the
// manifest's source hash already pins the exact bytes. On any returned error
// both results are the zero values — a count beside an error would be a number
// nothing could distinguish from a true one. A rollout with no turns and no
// model returns (0, "", nil); four of the 32 rollouts in the live corpus are
// exactly that, so it is the common case, not an edge.
func inspectCodexRollout(path string) (turnCount int, model string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", fmt.Errorf("codex-raw: open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// The Inspect-side ceiling, matching InspectClaudeJSONL rather than
	// SessionDay's 64 MiB. The longest line in the live corpus is 188,804 bytes.
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)

	for sc.Scan() {
		var r codexRolloutRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		if r.Type == "response_item" && r.Payload.Type == "message" &&
			(r.Payload.Role == "user" || r.Payload.Role == "assistant") {
			turnCount++
		}
		if r.Payload.Model != "" {
			model = r.Payload.Model
		}
	}
	if err := sc.Err(); err != nil {
		return 0, "", fmt.Errorf("codex-raw: scan %s: %w", path, err)
	}
	return turnCount, model, nil
}

func init() { RegisterAdapter(codexRawAdapter{}) }
