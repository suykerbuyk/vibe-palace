// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package enrichment

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// jsonl joins record lines into a JSONL byte stream.
func jsonl(lines ...string) []byte {
	return []byte(strings.Join(lines, "\n"))
}

func TestExtractPromptInput(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want PromptInput
	}{
		{
			name: "empty input",
			in:   []byte(""),
			want: PromptInput{},
		},
		{
			name: "string-content user message",
			in: jsonl(
				`{"type":"user","message":{"content":"hello world"}}`,
			),
			want: PromptInput{
				UserText:     "hello world",
				UserMessages: 1,
			},
		},
		{
			name: "array-content assistant with text and tool_use",
			in: jsonl(
				`{"type":"assistant","message":{"content":[{"type":"text","text":"working on it"},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`,
			),
			want: PromptInput{
				AssistantText: "working on it",
				AsstMessages:  1,
				ToolCounts:    map[string]int{"Bash": 1},
			},
		},
		{
			name: "tool counting across records",
			in: jsonl(
				`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{}},{"type":"tool_use","name":"Read","input":{}}]}}`,
				`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{}}]}}`,
			),
			want: PromptInput{
				AsstMessages: 2,
				ToolCounts:   map[string]int{"Bash": 2, "Read": 1},
			},
		},
		{
			name: "user/assistant message counts and tool_result ignored",
			in: jsonl(
				`{"type":"user","message":{"content":"first"}}`,
				`{"type":"assistant","message":{"content":[{"type":"text","text":"reply"}]}}`,
				`{"type":"user","message":{"content":[{"type":"tool_result","content":"noise"},{"type":"text","text":"second"}]}}`,
				`{"type":"summary","message":{"content":"ignored"}}`,
			),
			want: PromptInput{
				UserText:      "first\nsecond",
				AssistantText: "reply",
				UserMessages:  2,
				AsstMessages:  1,
			},
		},
		{
			name: "files changed sorted and deduped",
			in: jsonl(
				`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/z/last.go"}},{"type":"tool_use","name":"Edit","input":{"file_path":"/a/first.go"}}]}}`,
				`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/a/first.go"}},{"type":"tool_use","name":"NotebookEdit","input":{"notebook_path":"/m/mid.ipynb"}},{"type":"tool_use","name":"MultiEdit","input":{"path":"/m/multi.go"}}]}}`,
				`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo hi"}}]}}`,
			),
			want: PromptInput{
				AsstMessages: 3,
				ToolCounts: map[string]int{
					"Write": 1, "Edit": 2, "NotebookEdit": 1,
					"MultiEdit": 1, "Bash": 1,
				},
				FilesChanged: []string{
					"/a/first.go", "/m/mid.ipynb",
					"/m/multi.go", "/z/last.go",
				},
			},
		},
		{
			name: "malformed lines skipped",
			in: jsonl(
				`{"type":"user","message":{"content":"keep me"}}`,
				`this is not json`,
				`{"type":"assistant","message":{"content":[{"type":"text","text":"also keep"}]}}`,
				``,
			),
			want: PromptInput{
				UserText:      "keep me",
				AssistantText: "also keep",
				UserMessages:  1,
				AsstMessages:  1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractPromptInput(tt.in)
			if err != nil {
				t.Fatalf("ExtractPromptInput() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExtractPromptInput() mismatch\n got: %+v\nwant: %+v", got, tt.want)
			}
		})
	}
}

func TestExtractPromptInputTruncation(t *testing.T) {
	// Build user text well over the cap, with newline boundaries so the
	// truncate helper has a break point past the half-way mark.
	var b strings.Builder
	for range 4000 {
		b.WriteString("aaaa\n") // 5 chars each -> 20000 chars
	}
	long := b.String()
	if len(long) <= maxUserChars {
		t.Fatalf("test setup: long text %d not over cap %d", len(long), maxUserChars)
	}

	line := `{"type":"user","message":{"content":` + jsonQuote(long) + `}}`
	got, err := ExtractPromptInput([]byte(line))
	if err != nil {
		t.Fatalf("ExtractPromptInput() error = %v", err)
	}

	if !strings.HasSuffix(got.UserText, "[...truncated]") {
		t.Errorf("expected truncation marker suffix, got tail %q", tail(got.UserText, 30))
	}
	// Content (excluding the appended marker) must not exceed the cap and
	// must be cut at/after the half-way point.
	body := strings.TrimSuffix(got.UserText, "\n[...truncated]")
	if len(body) > maxUserChars {
		t.Errorf("truncated body len %d exceeds cap %d", len(body), maxUserChars)
	}
	if len(body) < maxUserChars/2 {
		t.Errorf("truncated body len %d cut before half-way %d", len(body), maxUserChars/2)
	}
}

// TestExtractPromptInputToolResultUserNotCounted verifies that a user-type
// record whose content array carries only tool_result block(s) does NOT
// increment UserMessages and contributes no text to UserText. Claude Code
// delivers tool results as "user" records; those must not inflate the
// user-message count fed to the model.
func TestExtractPromptInputToolResultUserNotCounted(t *testing.T) {
	in := jsonl(
		`{"type":"user","message":{"content":"a real question"}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"command output noise"}]}}`,
	)
	got, err := ExtractPromptInput(in)
	if err != nil {
		t.Fatalf("ExtractPromptInput() error = %v", err)
	}
	if got.UserMessages != 1 {
		t.Errorf("UserMessages = %d, want 1 (tool_result-only record must not count)", got.UserMessages)
	}
	if got.UserText != "a real question" {
		t.Errorf("UserText = %q, want only the real user message", got.UserText)
	}
	if strings.Contains(got.UserText, "command output noise") {
		t.Errorf("UserText leaked tool_result content: %q", got.UserText)
	}
}

// TestTruncateDropsPartialRune verifies the rune-safe cut: when truncate must
// break on a byte boundary with no newline to break on, it drops the partial
// trailing UTF-8 rune so the result stays valid UTF-8 (json.Marshal would
// otherwise silently rewrite a split rune to U+FFFD).
func TestTruncateDropsPartialRune(t *testing.T) {
	// "世" is 3 bytes. Choose a maxChars that is NOT a multiple of 3 so the cut
	// lands mid-rune, and a string longer than the cap with no newlines.
	const maxChars = 100            // 100 % 3 == 1 → byte slice splits a rune
	text := strings.Repeat("世", 50) // 150 bytes, no newlines

	// Sanity: the naive byte slice the fix guards against IS invalid UTF-8.
	if utf8.ValidString(text[:maxChars]) {
		t.Fatalf("test setup: text[:%d] is unexpectedly valid UTF-8", maxChars)
	}

	got := truncate(text, maxChars)
	body := strings.TrimSuffix(got, "\n[...truncated]")

	if !utf8.ValidString(body) {
		t.Errorf("truncated body is not valid UTF-8: %q", body)
	}
	// The dropped partial rune means the body ends on a clean rune boundary and
	// does not contain the replacement character.
	if strings.ContainsRune(body, utf8.RuneError) {
		t.Errorf("truncated body contains U+FFFD replacement rune: %q", body)
	}
	if !strings.HasSuffix(got, "[...truncated]") {
		t.Errorf("expected truncation marker, got tail %q", tail(got, 20))
	}
}

// jsonQuote returns a JSON string literal for s.
func jsonQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`, "\r", `\r`)
	return `"` + r.Replace(s) + `"`
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
