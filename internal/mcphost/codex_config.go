// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcphost

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

const codexServerName = "vibe-palace"

type codexServerConfig struct {
	Command string
	Args    []string
	Enabled *bool
}

// patchCodexServerTable changes only the values of the three Vibe-Palace
// assignments. It never serializes the TOML document. Unrecognized syntax in
// the owned table is rejected rather than risking an edit to the wrong table.
func patchCodexServerTable(raw []byte, desired codexServerConfig) ([]byte, error) {
	if desired.Command == "" || len(desired.Args) == 0 {
		return nil, fmt.Errorf("Codex command and args are required")
	}
	if err := validateCodexTOML(raw); err != nil {
		return nil, fmt.Errorf("validate Codex TOML: %w", err)
	}

	lines := splitLines(raw)
	baseHeader := "[mcp_servers." + codexServerName + "]"
	childPrefix := "[mcp_servers." + codexServerName + ".tools."
	baseStart, baseEnd, firstChild := -1, len(lines), -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(stripTOMLComment(line.text))
		if trimmed == baseHeader {
			if baseStart >= 0 {
				return nil, fmt.Errorf("duplicate %s table", baseHeader)
			}
			baseStart = i
			continue
		}
		if strings.HasPrefix(trimmed, childPrefix) && strings.HasSuffix(trimmed, "]") && firstChild < 0 {
			firstChild = i
		}
		if baseStart >= 0 && strings.HasPrefix(trimmed, "[") {
			baseEnd = i
			break
		}
	}

	if baseStart < 0 && firstChild >= 0 {
		baseStart, baseEnd = firstChild, firstChild+1
		lines = append(lines[:firstChild], append([]sourceLine{{text: baseHeader, ending: "\n"}}, lines[firstChild:]...)...)
		// Preserve the source-line offsets after inserting the new table header.
		for i := range lines {
			lines[i].start = 0
		}
	} else if baseStart < 0 {
		baseStart, baseEnd = len(lines), len(lines)+1
		if len(lines) > 0 && lines[len(lines)-1].ending == "" {
			lines[len(lines)-1].ending = "\n"
		}
		lines = append(lines, sourceLine{text: baseHeader, ending: "\n"})
	}

	values := map[string]string{
		"command": strconv.Quote(desired.Command),
		"args":    encodeTOMLStringArray(desired.Args),
	}
	if desired.Enabled != nil {
		values["enabled"] = strconv.FormatBool(*desired.Enabled)
	}

	seen := map[string]bool{}
	for i := baseStart + 1; i < baseEnd; i++ {
		key, valueStart, valueEnd, ok := assignmentValueSpan(lines[i].text)
		if !ok {
			continue
		}
		want, owned := values[key]
		if !owned {
			continue
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate %s assignment in %s", key, baseHeader)
		}
		seen[key] = true
		lines[i].text = lines[i].text[:valueStart] + want + lines[i].text[valueEnd:]
	}

	var missing []string
	for _, key := range []string{"command", "args", "enabled"} {
		value, owned := values[key]
		if owned && !seen[key] {
			missing = append(missing, key+" = "+value)
		}
	}
	if len(missing) > 0 {
		insertAt := baseEnd
		insert := make([]sourceLine, 0, len(missing))
		for _, assignment := range missing {
			insert = append(insert, sourceLine{text: assignment, ending: "\n"})
		}
		lines = append(lines[:insertAt], append(insert, lines[insertAt:]...)...)
	}

	var out bytes.Buffer
	for _, line := range lines {
		out.WriteString(line.text)
		out.WriteString(line.ending)
	}
	patched := out.Bytes()
	if err := validateCodexTOML(patched); err != nil {
		return nil, fmt.Errorf("patched Codex TOML is invalid: %w", err)
	}
	return patched, nil
}

type sourceLine struct {
	text   string
	ending string
	start  int
}

func splitLines(raw []byte) []sourceLine {
	if len(raw) == 0 {
		return nil
	}
	parts := bytes.SplitAfter(raw, []byte("\n"))
	lines := make([]sourceLine, 0, len(parts))
	offset := 0
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		ending := ""
		text := part
		if part[len(part)-1] == '\n' {
			ending = "\n"
			text = part[:len(part)-1]
			if len(text) > 0 && text[len(text)-1] == '\r' {
				text = text[:len(text)-1]
				ending = "\r\n"
			}
		}
		lines = append(lines, sourceLine{text: string(text), ending: ending, start: offset})
		offset += len(part)
	}
	return lines
}

func validateCodexTOML(raw []byte) error {
	var doc map[string]any
	if _, err := toml.Decode(string(raw), &doc); err != nil {
		return err
	}
	return nil
}

func stripTOMLComment(line string) string {
	quoted, escaped := false, false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if quoted && r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			continue
		}
		if r == '#' && !quoted {
			return line[:i]
		}
	}
	return line
}

func assignmentValueSpan(line string) (key string, start, end int, ok bool) {
	code := stripTOMLComment(line)
	eq := strings.IndexByte(code, '=')
	if eq < 0 {
		return "", 0, 0, false
	}
	key = strings.TrimSpace(code[:eq])
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", 0, 0, false
	}
	start = eq + 1
	for start < len(code) && (code[start] == ' ' || code[start] == '\t') {
		start++
	}
	end = len(code)
	for end > start && (code[end-1] == ' ' || code[end-1] == '\t') {
		end--
	}
	if start == end {
		return "", 0, 0, false
	}
	// The owned Codex settings are scalar/one-line array values. Fail closed
	// rather than attempting to replace a multiline TOML value.
	if strings.ContainsAny(code[start:end], "\r\n") {
		return "", 0, 0, false
	}
	return key, start, end, true
}

func encodeTOMLStringArray(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = strconv.Quote(value)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func codexConfigRead(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return raw, err
}
