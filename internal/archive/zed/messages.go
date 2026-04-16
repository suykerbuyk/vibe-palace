// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package zed

import (
	"encoding/json"
	"fmt"
)

// UnmarshalMessages parses Zed's Rust-style enum message envelopes
// ({"User": {...}} / {"Agent": {...}}) from the raw JSON array stored
// in the thread payload. Unknown envelopes (e.g., "Resume" string
// markers or future Zed variants) are skipped silently; a future
// schema-version bump will surface them at the version gate rather
// than corrupting synthesis here.
func UnmarshalMessages(rawMessages []json.RawMessage) ([]Message, error) {
	out := make([]Message, 0, len(rawMessages))
	for i, raw := range rawMessages {
		msg, err := unmarshalMessage(raw)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", i, err)
		}
		if msg != nil {
			out = append(out, *msg)
		}
	}
	return out, nil
}

func unmarshalMessage(raw json.RawMessage) (*Message, error) {
	// String markers like "Resume" are allowed and skipped.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return nil, nil
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}

	if data, ok := envelope["User"]; ok {
		return unmarshalUserMessage(data)
	}
	if data, ok := envelope["Agent"]; ok {
		return unmarshalAgentMessage(data)
	}
	// Unknown variant — skip rather than fail; schema-version gate
	// in parser.go catches genuine drift.
	return nil, nil
}

func unmarshalUserMessage(data json.RawMessage) (*Message, error) {
	var u struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, fmt.Errorf("unmarshal user: %w", err)
	}
	content, err := unmarshalContentBlocks(u.Content)
	if err != nil {
		return nil, err
	}
	return &Message{Role: "user", Content: content}, nil
}

func unmarshalAgentMessage(data json.RawMessage) (*Message, error) {
	var a struct {
		Content     []json.RawMessage          `json:"content"`
		ToolResults map[string]json.RawMessage `json:"tool_results"`
	}
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("unmarshal agent: %w", err)
	}
	content, err := unmarshalContentBlocks(a.Content)
	if err != nil {
		return nil, err
	}

	// Zed attaches tool_results to the agent message that initiated
	// them. Claude's convention is to emit tool_result blocks in the
	// *next* user-role message. We flatten both here: append
	// tool_result content blocks back onto the assistant message and
	// let the synthesizer repackage them into user turns later.
	for id, raw := range a.ToolResults {
		tr := decodeToolResult(id, raw)
		if tr != nil {
			content = append(content, *tr)
		}
	}

	return &Message{Role: "assistant", Content: content}, nil
}

func decodeToolResult(id string, raw json.RawMessage) *Content {
	var tr struct {
		ToolUseID string          `json:"tool_use_id"`
		ToolName  string          `json:"tool_name"`
		IsError   bool            `json:"is_error"`
		Content   json.RawMessage `json:"content"`
		Output    json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil
	}
	text := extractTextFromEnum(tr.Content)
	if text == "" {
		text = extractOutputText(tr.Output)
	}
	useID := tr.ToolUseID
	if useID == "" {
		useID = id
	}
	return &Content{
		Type:           "tool_result",
		ToolUseID:      useID,
		ToolResultText: text,
		ToolIsError:    tr.IsError,
	}
}

func unmarshalContentBlocks(raws []json.RawMessage) ([]Content, error) {
	out := make([]Content, 0, len(raws))
	for _, raw := range raws {
		block := unmarshalContentBlock(raw)
		if block != nil {
			out = append(out, *block)
		}
	}
	return out, nil
}

func unmarshalContentBlock(raw json.RawMessage) *Content {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil
	}

	if textRaw, ok := envelope["Text"]; ok {
		var text string
		if err := json.Unmarshal(textRaw, &text); err != nil {
			return nil
		}
		return &Content{Type: "text", Text: text}
	}

	if thinkRaw, ok := envelope["Thinking"]; ok {
		var think struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(thinkRaw, &think); err != nil {
			return nil
		}
		return &Content{Type: "thinking", Text: think.Text}
	}

	if tuRaw, ok := envelope["ToolUse"]; ok {
		var tu struct {
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(tuRaw, &tu); err != nil {
			return nil
		}
		return &Content{
			Type:      "tool_use",
			ToolID:    tu.ID,
			ToolName:  tu.Name,
			ToolInput: tu.Input,
		}
	}

	if mentionRaw, ok := envelope["Mention"]; ok {
		var mention struct {
			URI     json.RawMessage `json:"uri"`
			Content string          `json:"content"`
		}
		if err := json.Unmarshal(mentionRaw, &mention); err != nil {
			return nil
		}
		return &Content{
			Type:        "mention",
			MentionURI:  flattenMentionURI(mention.URI),
			MentionText: mention.Content,
		}
	}

	if _, ok := envelope["Image"]; ok {
		return &Content{Type: "image"}
	}

	return nil
}

func flattenMentionURI(raw json.RawMessage) string {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ""
	}
	if fileRaw, ok := envelope["File"]; ok {
		var f struct {
			AbsPath string `json:"abs_path"`
		}
		if err := json.Unmarshal(fileRaw, &f); err == nil {
			return "file://" + f.AbsPath
		}
	}
	if selRaw, ok := envelope["Selection"]; ok {
		var s struct {
			AbsPath   string `json:"abs_path"`
			LineRange struct {
				Start int `json:"start"`
				End   int `json:"end"`
			} `json:"line_range"`
		}
		if err := json.Unmarshal(selRaw, &s); err == nil {
			return fmt.Sprintf("file://%s#L%d-L%d", s.AbsPath, s.LineRange.Start, s.LineRange.End)
		}
	}
	if threadRaw, ok := envelope["Thread"]; ok {
		var t struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(threadRaw, &t); err == nil {
			return "thread://" + t.ID
		}
	}
	return ""
}

func extractTextFromEnum(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var envelope map[string]string
	if err := json.Unmarshal(raw, &envelope); err == nil {
		if text, ok := envelope["Text"]; ok {
			return text
		}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

func extractOutputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err == nil {
		if textRaw, ok := envelope["Text"]; ok {
			var text string
			if err := json.Unmarshal(textRaw, &text); err == nil {
				return text
			}
		}
	}
	return string(raw)
}
