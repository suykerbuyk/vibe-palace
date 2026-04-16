// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	zedreader "github.com/suykerbuyk/vibe-palace/internal/archive/zed"
)

// ZedAdapterName is the manifest.adapter value for Zed.
const ZedAdapterName = "zed"

// ZedAdapterVersion is the adapter implementation version, independent
// of vp's release. Bump when the on-disk contract with Zed changes
// (e.g., threads schema migration, message-format bump).
const ZedAdapterVersion = "1.0.0"

// zedAdapter implements Adapter for Zed's threads.db SQLite store.
//
// ResolveSource reads a single thread, decompresses it, and
// synthesizes a deterministic Claude-shape JSONL to a temp file. The
// resulting file is what Create hashes and compresses; cleanup
// removes the temp file after archive write.
//
// The synthesized JSONL preserves content, not bytes: Zed's
// Rust-enum envelopes are flattened to Claude's tool_use / tool_result
// shape so downstream consumers can treat all archives uniformly.
type zedAdapter struct{}

func (zedAdapter) Name() string    { return ZedAdapterName }
func (zedAdapter) Version() string { return ZedAdapterVersion }

func (zedAdapter) ResolveSource(opts CreateOptions) (string, func(), error) {
	dbPath := opts.SourcePath
	if dbPath == "" {
		dbPath = zedreader.DefaultDBPath()
	}
	if dbPath == "" {
		return "", noopCleanup, fmt.Errorf("zed: threads db path could not be resolved (set --source-path or ZED_THREADS_DB)")
	}
	if _, err := os.Stat(dbPath); err != nil {
		return "", noopCleanup, fmt.Errorf("zed threads db not found: %w", err)
	}

	th, err := zedreader.QueryThread(dbPath, opts.SessionID)
	if err != nil {
		return "", noopCleanup, err
	}

	buf, err := synthesizeJSONL(th)
	if err != nil {
		return "", noopCleanup, fmt.Errorf("zed: synthesize jsonl: %w", err)
	}

	f, err := os.CreateTemp("", "vp-archive-zed-*.jsonl")
	if err != nil {
		return "", noopCleanup, fmt.Errorf("zed: temp file: %w", err)
	}
	tmpPath := f.Name()
	cleanup := func() { os.Remove(tmpPath) }

	if _, err := f.Write(buf); err != nil {
		f.Close()
		cleanup()
		return "", noopCleanup, fmt.Errorf("zed: write temp: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", noopCleanup, fmt.Errorf("zed: close temp: %w", err)
	}
	return tmpPath, cleanup, nil
}

func (zedAdapter) Inspect(sourcePath string) (int, string, error) {
	// The synthesized JSONL has the same shape as Claude's, so we can
	// reuse the Claude inspector verbatim.
	return InspectClaudeJSONL(sourcePath)
}

func init() {
	RegisterAdapter(zedAdapter{})
}

// --- JSONL synthesis ---

// jsonlLine / jsonlMessage / jsonlContent mirror a stable subset of
// Claude Code's line-delimited record shape. Struct types (not
// map[string]any) are deliberate: encoding/json emits struct fields
// in declaration order, guaranteeing deterministic bytes across runs
// with the same Thread input.
type jsonlLine struct {
	Type    string       `json:"type"`
	Message jsonlMessage `json:"message"`
}

type jsonlMessage struct {
	Role    string         `json:"role"`
	Model   string         `json:"model,omitempty"`
	Content []jsonlContent `json:"content"`
}

type jsonlContent struct {
	Type string `json:"type"`

	// text / thinking
	Text string `json:"text,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// synthesizeJSONL serializes a Zed Thread as Claude-shape JSONL.
//
// Layout rules:
//   - Each Zed user Message becomes one user-role line.
//   - Each Zed agent Message becomes one assistant-role line carrying
//     text + thinking + tool_use content blocks, in source order.
//   - tool_result content blocks attached to that agent Message are
//     split out into a following user-role line, in the order of the
//     tool_use blocks that produced them (stable, deterministic).
//   - Empty content arrays are elided: we emit an empty "content": []
//     rather than dropping the line, so line count matches turn count.
func synthesizeJSONL(th *zedreader.Thread) ([]byte, error) {
	model := th.ModelString()

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)

	emit := func(line jsonlLine) error {
		if line.Message.Content == nil {
			line.Message.Content = []jsonlContent{}
		}
		return enc.Encode(line)
	}

	for _, msg := range th.Messages {
		switch msg.Role {
		case "user":
			content := convertUserContent(msg.Content)
			if err := emit(jsonlLine{
				Type:    "user",
				Message: jsonlMessage{Role: "user", Content: content},
			}); err != nil {
				return nil, err
			}
		case "assistant":
			asstContent, toolResults := splitAssistantContent(msg.Content)
			if err := emit(jsonlLine{
				Type: "assistant",
				Message: jsonlMessage{
					Role:    "assistant",
					Model:   model,
					Content: asstContent,
				},
			}); err != nil {
				return nil, err
			}
			if len(toolResults) > 0 {
				if err := emit(jsonlLine{
					Type:    "user",
					Message: jsonlMessage{Role: "user", Content: toolResults},
				}); err != nil {
					return nil, err
				}
			}
		default:
			// Unknown role — skip. Parser has already gated on
			// thread.Version; anything landing here is a post-version
			// oddity worth ignoring rather than aborting.
		}
	}

	return out.Bytes(), nil
}

func convertUserContent(blocks []zedreader.Content) []jsonlContent {
	out := make([]jsonlContent, 0, len(blocks))
	for _, c := range blocks {
		switch c.Type {
		case "text":
			out = append(out, jsonlContent{Type: "text", Text: c.Text})
		case "mention":
			// Inline mentions as text; a URI prefix makes the
			// reference recoverable without a new content type.
			text := c.MentionText
			if c.MentionURI != "" {
				if text == "" {
					text = c.MentionURI
				} else {
					text = "[" + c.MentionURI + "] " + text
				}
			}
			if text != "" {
				out = append(out, jsonlContent{Type: "text", Text: text})
			}
		case "image":
			out = append(out, jsonlContent{Type: "image"})
		}
	}
	return out
}

// splitAssistantContent walks the agent's content blocks, keeping
// text/thinking/tool_use on the assistant line and peeling
// tool_result blocks out into a parallel slice for the following
// user line. Ordering within each slice follows the source.
func splitAssistantContent(blocks []zedreader.Content) (asst []jsonlContent, results []jsonlContent) {
	asst = make([]jsonlContent, 0, len(blocks))
	for _, c := range blocks {
		switch c.Type {
		case "text":
			asst = append(asst, jsonlContent{Type: "text", Text: c.Text})
		case "thinking":
			asst = append(asst, jsonlContent{Type: "thinking", Text: c.Text})
		case "tool_use":
			input := c.ToolInput
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			asst = append(asst, jsonlContent{
				Type:  "tool_use",
				ID:    c.ToolID,
				Name:  c.ToolName,
				Input: input,
			})
		case "tool_result":
			results = append(results, jsonlContent{
				Type:      "tool_result",
				ToolUseID: c.ToolUseID,
				Text:      c.ToolResultText,
				IsError:   c.ToolIsError,
			})
		case "image":
			asst = append(asst, jsonlContent{Type: "image"})
		}
	}
	return asst, results
}
