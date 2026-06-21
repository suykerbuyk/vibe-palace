// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package enrichment

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// transcriptLine is the per-record shape we decode from each Claude Code
// JSONL line. Only "user" and "assistant" records carry content we care
// about; everything else is ignored. The Content field is captured as
// raw JSON because it is polymorphic — either a plain string or an array
// of content blocks.
type transcriptLine struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// contentBlock is one element of an array-shaped message content. Only a
// subset of fields is decoded; unknown block types are skipped. Input is
// kept raw so file-path keys can be probed defensively per tool.
type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// fileEditTools are the tool_use names whose input carries a path we
// treat as a changed file.
var fileEditTools = map[string]bool{
	"Write":        true,
	"Edit":         true,
	"MultiEdit":    true,
	"NotebookEdit": true,
}

// ExtractPromptInput parses raw Claude Code JSONL transcript bytes into a
// populated, truncated PromptInput. It handles both content shapes (plain
// string and content-block array), counts tool_use invocations and
// user/assistant records, and collects distinct edited file paths.
// Malformed lines are skipped. Duration and the narrative fields are left
// as zero values for the caller to fill in.
func ExtractPromptInput(data []byte) (PromptInput, error) {
	var (
		userParts  []string
		asstParts  []string
		toolCounts = map[string]int{}
		fileSet    = map[string]struct{}{}
		out        PromptInput
	)

	sc := bufio.NewScanner(bytes.NewReader(data))
	// Claude JSONL lines can be large (tool outputs, file reads).
	// Generous buffer ceiling — 16 MiB handles realistic worst cases.
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)

	for sc.Scan() {
		var l transcriptLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			// Skip malformed lines — extraction is best-effort.
			continue
		}
		switch l.Type {
		case "user":
			// Count only records carrying real user text. Claude Code delivers
			// tool_result blocks as "user"-type records too; those have no text
			// and must not inflate the user-message count fed to the model.
			if text := userContentText(l.Message.Content); text != "" {
				out.UserMessages++
				userParts = append(userParts, text)
			}
		case "assistant":
			out.AsstMessages++
			text := assistantContent(l.Message.Content, toolCounts, fileSet)
			if text != "" {
				asstParts = append(asstParts, text)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return PromptInput{}, fmt.Errorf("scan transcript: %w", err)
	}

	out.UserText = truncate(strings.Join(userParts, "\n"), maxUserChars)
	out.AssistantText = truncate(strings.Join(asstParts, "\n"), maxAssistantChars)

	if len(toolCounts) > 0 {
		out.ToolCounts = toolCounts
	}
	if len(fileSet) > 0 {
		files := make([]string, 0, len(fileSet))
		for f := range fileSet {
			files = append(files, f)
		}
		sort.Strings(files)
		out.FilesChanged = files
	}

	return out, nil
}

// userContentText extracts user-visible text from a user record's
// content. A plain string is returned directly; an array yields the
// concatenation of its text blocks. tool_result blocks are ignored as
// noise.
func userContentText(raw json.RawMessage) string {
	if s, ok := decodeString(raw); ok {
		return s
	}
	blocks, ok := decodeBlocks(raw)
	if !ok {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// assistantContent extracts assistant text-block text and, as a side
// effect, tallies tool_use blocks into toolCounts and records edited file
// paths into fileSet.
func assistantContent(raw json.RawMessage, toolCounts map[string]int, fileSet map[string]struct{}) string {
	if s, ok := decodeString(raw); ok {
		return s
	}
	blocks, ok := decodeBlocks(raw)
	if !ok {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case "tool_use":
			if b.Name == "" {
				continue
			}
			toolCounts[b.Name]++
			if fileEditTools[b.Name] {
				collectFilePaths(b.Input, fileSet)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// collectFilePaths probes a tool_use input object for the known
// file-path keys and records any non-empty string values.
func collectFilePaths(raw json.RawMessage, fileSet map[string]struct{}) {
	if len(raw) == 0 {
		return
	}
	var input struct {
		FilePath     string `json:"file_path"`
		Path         string `json:"path"`
		NotebookPath string `json:"notebook_path"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return
	}
	for _, p := range []string{input.FilePath, input.Path, input.NotebookPath} {
		if p != "" {
			fileSet[p] = struct{}{}
		}
	}
}

// decodeString reports whether raw is a JSON string and returns its
// value.
func decodeString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// decodeBlocks reports whether raw is a JSON array and returns its
// content blocks.
func decodeBlocks(raw json.RawMessage) ([]contentBlock, bool) {
	if len(raw) == 0 || raw[0] != '[' {
		return nil, false
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, false
	}
	return blocks, true
}
