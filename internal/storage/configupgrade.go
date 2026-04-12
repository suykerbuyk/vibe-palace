// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"sort"

	"github.com/BurntSushi/toml"
)

// DetectMissingKeys returns a sorted list of dot-notation keys present in
// the canonical defaults but absent from the user's config file. Returns
// nil when the config is up to date.
func DetectMissingKeys(configPath string) ([]string, error) {
	canonical, err := CanonicalKeys()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	present := PresentKeys(string(data))

	var missing []string
	for k := range canonical {
		if !present[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	return missing, nil
}

// CanonicalKeys extracts dot-notation keys from the embedded defaults.toml.
func CanonicalKeys() (map[string]bool, error) {
	var raw map[string]any
	if _, err := toml.Decode(defaultsToml, &raw); err != nil {
		return nil, err
	}
	keys := make(map[string]bool)
	flattenToKeys("", raw, keys)
	return keys, nil
}

// flattenToKeys recursively walks a parsed TOML map and emits dot-notation keys.
func flattenToKeys(prefix string, m map[string]any, out map[string]bool) {
	for k, v := range m {
		full := k
		if prefix != "" {
			full = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			flattenToKeys(full, sub, out)
		} else {
			out[full] = true
		}
	}
}

// PresentKeys scans raw TOML text for keys, respecting section scope.
// Detects both active keys (key = val) and commented-out keys (# key = val).
// Tracks section headers (both active and commented) to correctly resolve
// nested key paths like palace.llm.endpoint.
func PresentKeys(text string) map[string]bool {
	keys := make(map[string]bool)
	currentSection := ""

	for _, line := range splitLines(text) {
		trimmed := trimLeftSpace(line)

		// Active section header: [foo.bar]
		if sec, ok := parseSectionHeader(trimmed, false); ok {
			currentSection = sec
			continue
		}

		// Commented section header: # [foo.bar]
		if sec, ok := parseSectionHeader(trimmed, true); ok {
			currentSection = sec
			continue
		}

		// Active key: key = val
		if key, ok := parseKeyAssignment(trimmed, false); ok {
			full := key
			if currentSection != "" {
				full = currentSection + "." + key
			}
			keys[full] = true
			continue
		}

		// Commented key: # key = val
		if key, ok := parseKeyAssignment(trimmed, true); ok {
			full := key
			if currentSection != "" {
				full = currentSection + "." + key
			}
			keys[full] = true
			continue
		}
	}

	return keys
}

// parseSectionHeader extracts the section name from a line like [foo.bar]
// or # [foo.bar] (if commented is true).
func parseSectionHeader(line string, commented bool) (string, bool) {
	s := line
	if commented {
		if len(s) == 0 || s[0] != '#' {
			return "", false
		}
		s = trimLeftSpace(s[1:])
	}
	if len(s) < 3 || s[0] != '[' || s[len(s)-1] != ']' {
		return "", false
	}
	// Skip table arrays [[...]]
	if s[1] == '[' {
		return "", false
	}
	section := trimSpace(s[1 : len(s)-1])
	if section == "" {
		return "", false
	}
	return section, true
}

// parseKeyAssignment extracts the key name from a line like key = val
// or # key = val (if commented is true).
func parseKeyAssignment(line string, commented bool) (string, bool) {
	s := line
	if commented {
		if len(s) == 0 || s[0] != '#' {
			return "", false
		}
		s = trimLeftSpace(s[1:])
	}
	// Must start with a letter or underscore (TOML bare key).
	if len(s) == 0 || !(isLetter(s[0]) || s[0] == '_') {
		return "", false
	}
	// Find the key name (bare key characters: letters, digits, -, _).
	i := 0
	for i < len(s) && (isLetter(s[i]) || isDigit(s[i]) || s[i] == '-' || s[i] == '_') {
		i++
	}
	if i == 0 {
		return "", false
	}
	key := s[:i]
	rest := trimLeftSpace(s[i:])
	if len(rest) == 0 || rest[0] != '=' {
		return "", false
	}
	return key, true
}

// MissingKeys returns canonical keys absent from the present set, grouped
// by TOML section ("" = top-level).
func MissingKeys(canonical, present map[string]bool) map[string][]string {
	grouped := make(map[string][]string)
	for k := range canonical {
		if present[k] {
			continue
		}
		section := ""
		if i := lastDot(k); i >= 0 {
			section = k[:i]
		}
		grouped[section] = append(grouped[section], k)
	}
	// Sort keys within each section for deterministic output.
	for s := range grouped {
		sort.Strings(grouped[s])
	}
	return grouped
}

// SectionRange tracks the line range of a TOML section in text.
type SectionRange struct {
	Name      string // dot-notation section name, "" for top-level
	StartLine int    // line number of [section] header (0-indexed)
	EndLine   int    // line number of last line in section (exclusive)
}

// FindSectionRanges returns line ranges for each TOML section in the text.
// Only considers uncommented section headers.
func FindSectionRanges(text string) []SectionRange {
	lines := splitLines(text)
	var ranges []SectionRange

	currentSection := ""
	currentStart := 0

	for i, line := range lines {
		trimmed := trimLeftSpace(line)
		if sec, ok := parseSectionHeader(trimmed, false); ok {
			// Close previous section.
			ranges = append(ranges, SectionRange{
				Name:      currentSection,
				StartLine: currentStart,
				EndLine:   i,
			})
			currentSection = sec
			currentStart = i
		}
	}
	// Close final section.
	ranges = append(ranges, SectionRange{
		Name:      currentSection,
		StartLine: currentStart,
		EndLine:   len(lines),
	})

	return ranges
}

// ParseTemplateBlocks splits the template.toml into per-section blocks.
// Key "" holds top-level content. Each block includes the [section] header,
// preceding comments, and all key lines through to the next section.
func ParseTemplateBlocks(templateText string) map[string]string {
	lines := splitLines(templateText)
	blocks := make(map[string]string)

	currentSection := ""
	var currentLines []string

	for _, line := range lines {
		trimmed := trimLeftSpace(line)

		// Check for active or commented section header.
		if sec, ok := parseSectionHeader(trimmed, false); ok {
			// Save previous block.
			if len(currentLines) > 0 || currentSection == "" {
				blocks[currentSection] = joinLines(currentLines)
			}
			currentSection = sec
			currentLines = []string{line}
			continue
		}
		if sec, ok := parseSectionHeader(trimmed, true); ok {
			// Commented section header — start a new block.
			if len(currentLines) > 0 || currentSection == "" {
				blocks[currentSection] = joinLines(currentLines)
			}
			currentSection = sec
			currentLines = []string{line}
			continue
		}

		currentLines = append(currentLines, line)
	}

	// Save final block.
	if len(currentLines) > 0 || currentSection == "" {
		blocks[currentSection] = joinLines(currentLines)
	}

	return blocks
}

// ExtractKeySnippet returns the comment lines + key line for a specific key
// from a template section block, with the key line commented out if it isn't
// already. Returns empty string if the key is not found.
func ExtractKeySnippet(sectionBlock string, key string) string {
	return extractKeySnippet(sectionBlock, key, false)
}

// ExtractKeySnippetActive is like ExtractKeySnippet but preserves active keys
// as active (unmodified) rather than commenting them out. Used for sections
// where the template intends its active values to be adopted verbatim, such
// as the [meta] schema-version block.
func ExtractKeySnippetActive(sectionBlock string, key string) string {
	return extractKeySnippet(sectionBlock, key, true)
}

func extractKeySnippet(sectionBlock string, key string, keepActive bool) string {
	lines := splitLines(sectionBlock)
	var snippet []string
	var commentBuffer []string

	for _, line := range lines {
		trimmed := trimLeftSpace(line)

		// Accumulate comment lines as potential prefix for the next key.
		if len(trimmed) > 0 && trimmed[0] == '#' {
			// Check if this comment IS the key.
			if k, ok := parseKeyAssignment(trimmed, true); ok && k == key {
				snippet = append(snippet, commentBuffer...)
				snippet = append(snippet, line)
				return joinLines(snippet)
			}
			commentBuffer = append(commentBuffer, line)
			continue
		}

		// Active key line.
		if k, ok := parseKeyAssignment(trimmed, false); ok {
			if k == key {
				snippet = append(snippet, commentBuffer...)
				if keepActive {
					snippet = append(snippet, line)
				} else {
					// Comment it out for insertion.
					snippet = append(snippet, "# "+line)
				}
				return joinLines(snippet)
			}
		}

		// Non-comment, non-key line — reset comment buffer.
		commentBuffer = nil
	}

	return ""
}

// sectionKeepsActive reports whether a TOML section's keys should be injected
// as active values rather than commented-out defaults. Only the [meta] block
// (schema version markers) qualifies today.
func sectionKeepsActive(section string) bool {
	return section == "meta"
}

// UpgradeConfig inserts missing key snippets into the user's config text.
// Returns the original text unchanged if nothing is missing.
func UpgradeConfig(userText string, missing map[string][]string, templateBlocks map[string]string) string {
	if len(missing) == 0 {
		return userText
	}

	lines := splitLines(userText)
	sections := FindSectionRanges(userText)

	// Build a map of section name → SectionRange for quick lookup.
	sectionMap := make(map[string]*SectionRange)
	for i := range sections {
		sectionMap[sections[i].Name] = &sections[i]
	}

	// Process insertions in reverse line order so line numbers stay valid.
	type insertion struct {
		afterLine int // insert after this line index
		content   string
	}
	var insertions []insertion

	// Track sections that need to be appended at EOF.
	var eofAppend []string

	// Sort section names for deterministic processing.
	var sectionNames []string
	for sec := range missing {
		sectionNames = append(sectionNames, sec)
	}
	sort.Strings(sectionNames)

	for _, section := range sectionNames {
		keys := missing[section]
		sr, exists := sectionMap[section]

		if !exists {
			// Section doesn't exist — will append at EOF.
			var block string
			for _, key := range keys {
				// Extract just the key name (last segment after dot).
				shortKey := key
				if i := lastDot(key); i >= 0 {
					shortKey = key[i+1:]
				}
				var snippet string
			if sectionKeepsActive(section) {
				snippet = ExtractKeySnippetActive(templateBlocks[section], shortKey)
			} else {
				snippet = ExtractKeySnippet(templateBlocks[section], shortKey)
			}
				if snippet != "" {
					block += snippet + "\n"
				} else {
					block += "# " + shortKey + " = \"\"\n"
				}
			}
			if block != "" {
				// Add section header if not top-level.
				if section != "" {
					// Ensure parent sections exist.
					block = ensureParentSections(section, sectionMap, lines) + "\n[" + section + "]\n" + block
				}
				eofAppend = append(eofAppend, block)
			}
			continue
		}

		// Section exists — find insertion point.
		// Insert after the last key line, or after section header + comments.
		insertAt := findInsertionPoint(lines, sr)

		var block string
		for _, key := range keys {
			shortKey := key
			if i := lastDot(key); i >= 0 {
				shortKey = key[i+1:]
			}
			var snippet string
			if sectionKeepsActive(section) {
				snippet = ExtractKeySnippetActive(templateBlocks[section], shortKey)
			} else {
				snippet = ExtractKeySnippet(templateBlocks[section], shortKey)
			}
			if snippet != "" {
				block += snippet + "\n"
			} else {
				block += "# " + shortKey + " = \"\"\n"
			}
		}
		if block != "" {
			insertions = append(insertions, insertion{afterLine: insertAt, content: block})
		}
	}

	// Sort insertions in reverse order by line number.
	sort.Slice(insertions, func(i, j int) bool {
		return insertions[i].afterLine > insertions[j].afterLine
	})

	// Apply insertions.
	for _, ins := range insertions {
		snippetLines := splitLines(ins.content)
		// Insert after the target line.
		after := ins.afterLine + 1
		newLines := make([]string, 0, len(lines)+len(snippetLines))
		newLines = append(newLines, lines[:after]...)
		newLines = append(newLines, snippetLines...)
		newLines = append(newLines, lines[after:]...)
		lines = newLines
	}

	result := joinLines(lines)

	// Append EOF sections.
	for _, block := range eofAppend {
		result += "\n" + block
	}

	return result
}

// findInsertionPoint finds the line index after which new keys should be
// inserted within a section. Returns the last key/comment line before
// the next section or end of range.
func findInsertionPoint(lines []string, sr *SectionRange) int {
	lastContent := sr.StartLine // default: after section header
	for i := sr.StartLine; i < sr.EndLine && i < len(lines); i++ {
		trimmed := trimLeftSpace(lines[i])
		if len(trimmed) > 0 {
			lastContent = i
		}
	}
	return lastContent
}

// ensureParentSections checks if parent sections of a nested section
// already exist and returns any necessary parent section headers.
func ensureParentSections(section string, sectionMap map[string]*SectionRange, lines []string) string {
	parts := splitDot(section)
	var result string
	for i := 1; i < len(parts); i++ {
		parent := joinDot(parts[:i])
		if _, exists := sectionMap[parent]; !exists {
			// Check if parent appears as commented section.
			found := false
			for _, line := range lines {
				if sec, ok := parseSectionHeader(trimLeftSpace(line), true); ok && sec == parent {
					found = true
					break
				}
				if sec, ok := parseSectionHeader(trimLeftSpace(line), false); ok && sec == parent {
					found = true
					break
				}
			}
			if !found {
				result += "\n[" + parent + "]"
			}
		}
	}
	return result
}

func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

func joinDot(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += "." + p
	}
	return result
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	result := lines[0]
	for _, l := range lines[1:] {
		result += "\n" + l
	}
	return result
}

// Helper functions to avoid regexp dependency.

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trimLeftSpace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[i:]
}

func trimSpace(s string) string {
	s = trimLeftSpace(s)
	i := len(s)
	for i > 0 && (s[i-1] == ' ' || s[i-1] == '\t') {
		i--
	}
	return s[:i]
}

func isLetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
func isDigit(b byte) bool  { return b >= '0' && b <= '9' }
