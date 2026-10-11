// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"sort"
	"strings"

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
	return CanonicalKeysFrom(defaultsToml)
}

// CanonicalKeysFrom extracts dot-notation keys from an arbitrary TOML
// source. Only active (uncommented) keys are returned — pedagogical
// commented examples in a template are not treated as canonical schema.
// Callers pass the appropriate schema source (defaults.toml for global,
// the cwd-project template for its upgrade path).
func CanonicalKeysFrom(text string) (map[string]bool, error) {
	var raw map[string]any
	if _, err := toml.Decode(text, &raw); err != nil {
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

		var block strings.Builder
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
				block.WriteString(snippet)
				block.WriteString("\n")
			} else {
				block.WriteString("# ")
				block.WriteString(shortKey)
				block.WriteString(" = \"\"\n")
			}
		}
		if block.String() != "" {
			insertions = append(insertions, insertion{afterLine: insertAt, content: block.String()})
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

	var result strings.Builder
	result.WriteString(joinLines(lines))

	// Append EOF sections.
	for _, block := range eofAppend {
		result.WriteString("\n")
		result.WriteString(block)
	}

	return result.String()
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
	var result strings.Builder
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
				result.WriteString("\n[")
				result.WriteString(parent)
				result.WriteString("]")
			}
		}
	}
	return result.String()
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
	var result strings.Builder
	result.WriteString(parts[0])
	for _, p := range parts[1:] {
		result.WriteString(".")
		result.WriteString(p)
	}
	return result.String()
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var result strings.Builder
	result.WriteString(lines[0])
	for _, l := range lines[1:] {
		result.WriteString("\n")
		result.WriteString(l)
	}
	return result.String()
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

// ---------------------------------------------------------------------------
// Optional / documented config-key tier.
//
// A third tier beside canonical (active-in-the-schema) and absent: keys shipped
// COMMENTED in the template. On `vp config upgrade` they propagate into existing
// configs as inert, commented documentation — visible, benign (a commented key
// resolves to its default), and NOT counted by the `vp check` staleness nag
// (CheckConfigStaleness is driven by CanonicalKeys, which never sees them).
//
// Everything below is ADDITIVE. The canonical-tier primitives above
// (UpgradeConfig, MissingKeys, CanonicalKeys[From], DetectMissingKeys,
// PresentKeys, ParseTemplateBlocks, ExtractKeySnippet) are unchanged and are only
// READ from here, never modified — the optional pass is orchestrated above the
// storage layer (internal/reconcile), and this file merely supplies the pieces.
// ---------------------------------------------------------------------------

// OptionalDocKeys returns the optional/documented tier: dot-notation keys that
// appear COMMENTED in templateText (so PresentKeys sees them) but are NOT active
// in canonicalText (so CanonicalKeysFrom does not). This is the SINGLE chokepoint
// that decides tier membership — an exclusion filter, if one is ever wanted,
// belongs here and nowhere else.
//
// A malformed canonicalText (which cannot happen for the embedded, build-pinned
// sources) yields an empty set rather than a panic, so the optional pass degrades
// to a no-op instead of mis-classifying every present key as optional.
func OptionalDocKeys(templateText, canonicalText string) map[string]bool {
	canonical, err := CanonicalKeysFrom(canonicalText)
	if err != nil {
		return map[string]bool{}
	}
	optional := make(map[string]bool)
	for k := range PresentKeys(templateText) {
		if !canonical[k] {
			optional[k] = true
		}
	}
	return optional
}

// DetectOptionalDocKeys returns the sorted optional/documented keys that are
// absent (in any form — active or commented) from the config at configPath.
// Parameterized by template+canonical text so BOTH the `vp check` row
// (check.CheckConfigDocs, embedded globals) and the `vp config upgrade` dry-run
// (reconcile Plan, per-tier texts) call this one function — a single chokepoint,
// so neither helper is a duplicate that sourceaudit would flag uninvoked.
func DetectOptionalDocKeys(configPath, templateText, canonicalText string) ([]string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	optional := OptionalDocKeys(templateText, canonicalText)
	present := PresentKeys(string(data))
	var missing []string
	for k := range optional {
		if !present[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	return missing, nil
}

// PropagateOptionalBlocks inserts the optional/documented tier (OptionalDocKeys)
// into userText as inert commented documentation, idempotently, and returns the
// upgraded text plus the sorted list of keys it propagated. It never activates a
// key, never synthesizes an ACTIVE section header for a commented template
// section, and never duplicates content already present (active or commented).
//
// Per missing section S, the home is read from the USER's config:
//   - S == "" (top-level), or S has an ACTIVE [S] header, or S has only a
//     COMMENTED "# [S]" header  -> insert ONLY the missing keys under that
//     existing header (insertKeysUnderHeader).
//   - S absent entirely         -> append the verbatim commented block at EOF
//     (orderEOFSections keeps project_vaults last, per the template's documented
//     intent).
//
// It deliberately does NOT route group-(a) (active-header) keys through
// UpgradeConfig/ExtractKeySnippet. ExtractKeySnippet accumulates a comment buffer
// that only resets on a non-comment, non-key line, so against any run of
// consecutive commented keys (e.g. the cwd template's `# domain` then `# tags`,
// or a top-level `# vault_path` under a prose preamble) it drags the earlier keys
// into the later key's snippet — duplicating them on insert. extractCommentedKeySnippet
// (below) is drag-free and used uniformly instead.
func PropagateOptionalBlocks(userText, templateText, canonicalText string) (string, []string) {
	optional := OptionalDocKeys(templateText, canonicalText)
	present := PresentKeys(userText)
	missing := MissingKeys(optional, present)
	if len(missing) == 0 {
		return userText, nil
	}

	blocks := ParseTemplateBlocks(templateText)

	activeHeaders := make(map[string]bool)
	for _, sr := range FindSectionRanges(userText) {
		if sr.Name != "" {
			activeHeaders[sr.Name] = true
		}
	}

	// Deterministic processing order.
	sections := make([]string, 0, len(missing))
	for s := range missing {
		sections = append(sections, s)
	}
	sort.Strings(sections)

	out := userText
	var eofSections []string
	for _, s := range sections {
		if s == "" || activeHeaders[s] || hasCommentedHeader(out, s) {
			out = insertKeysUnderHeader(out, s, missing[s], blocks[s])
		} else {
			eofSections = append(eofSections, s)
		}
	}
	for _, s := range orderEOFSections(eofSections) {
		out = appendBlockAtEOF(out, optionalSectionBlock(s, missing[s], blocks[s]))
	}

	var propagated []string
	for _, keys := range missing {
		propagated = append(propagated, keys...)
	}
	sort.Strings(propagated)
	return out, propagated
}

// hasCommentedHeader reports whether text carries a commented "# [section]"
// header for section (and no active one — callers check active first).
func hasCommentedHeader(text, section string) bool {
	for _, line := range splitLines(text) {
		if sec, ok := parseSectionHeader(trimLeftSpace(line), true); ok && sec == section {
			return true
		}
	}
	return false
}

// insertKeysUnderHeader inserts the missing keys (as commented snippets drawn
// from block) under section's existing home in text, after the section's last
// non-blank line. It handles three homes uniformly: the implicit top-level
// section (section == ""), an active [section] header, and a commented
// "# [section]" header. A section's scope runs to the next header of EITHER kind
// (FindSectionRanges is active-only and cannot scope a commented section, which is
// why this is done by hand). Returns text unchanged if no home is found.
func insertKeysUnderHeader(text, section string, keys []string, block string) string {
	lines := splitLines(text)

	// Resolve the section's line range [bodyStart, end): the lines that belong
	// to it, after its header (if any).
	bodyStart, end := 0, len(lines)
	if section != "" {
		hdr := -1
		for i, line := range lines {
			t := trimLeftSpace(line)
			if sec, ok := parseSectionHeader(t, false); ok && sec == section {
				hdr = i
				break
			}
			if sec, ok := parseSectionHeader(t, true); ok && sec == section {
				hdr = i
				break
			}
		}
		if hdr < 0 {
			return text
		}
		bodyStart = hdr + 1
	}
	for i := bodyStart; i < len(lines); i++ {
		t := trimLeftSpace(lines[i])
		if _, ok := parseSectionHeader(t, false); ok {
			end = i
			break
		}
		if _, ok := parseSectionHeader(t, true); ok {
			end = i
			break
		}
	}

	// Insertion point: after the last non-blank line in the section body; if the
	// body is empty, insert at the very start of the body (bodyStart).
	after := bodyStart
	for i := bodyStart; i < end; i++ {
		if len(trimLeftSpace(lines[i])) > 0 {
			after = i + 1
		}
	}

	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	var snippet []string
	for _, k := range sorted {
		short := k
		if i := lastDot(k); i >= 0 {
			short = k[i+1:]
		}
		s := extractCommentedKeySnippet(block, short)
		if s == "" {
			s = "# " + short + ` = ""`
		}
		snippet = append(snippet, splitLines(s)...)
	}
	if len(snippet) == 0 {
		return text
	}

	out := make([]string, 0, len(lines)+len(snippet))
	out = append(out, lines[:after]...)
	out = append(out, snippet...)
	out = append(out, lines[after:]...)
	return joinLines(out)
}

// extractCommentedKeySnippet returns a commented key's own documentation (its
// contiguous leading comment prose) together with its "# key = val" line, from a
// template section block. Unlike ExtractKeySnippet it does NOT accumulate across
// sibling commented keys, blank lines, or the section header: the backward walk
// from the key line stops at the first blank line, non-comment (active) line,
// commented *key* line, or commented section header. Returns "" if key is absent.
// This is what makes the optional tier drag-free regardless of how many commented
// keys sit together in a block, while still carrying each key's doc comment.
func extractCommentedKeySnippet(sectionBlock, key string) string {
	lines := splitLines(sectionBlock)
	idx := -1
	for i, line := range lines {
		if k, ok := parseKeyAssignment(trimLeftSpace(line), true); ok && k == key {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ""
	}
	start := idx
	for i := idx - 1; i >= 0; i-- {
		t := trimLeftSpace(lines[i])
		if len(t) == 0 || t[0] != '#' {
			break // blank or active line
		}
		if _, ok := parseSectionHeader(t, true); ok {
			break // commented section header
		}
		if _, ok := parseKeyAssignment(t, true); ok {
			break // commented sibling key
		}
		start = i // a prose comment line belonging to this key
	}
	return joinLines(lines[start : idx+1])
}

// orderEOFSections sorts the fully-absent sections to append, but keeps
// project_vaults (and any project_vaults.* subtable) LAST, honoring the
// template's "Kept LAST in this file: every key below a [table] header belongs to
// it" intent so a later uncommented binding is not stranded above another table.
func orderEOFSections(sections []string) []string {
	var normal, last []string
	for _, s := range sections {
		if s == "project_vaults" || strings.HasPrefix(s, "project_vaults.") {
			last = append(last, s)
		} else {
			normal = append(normal, s)
		}
	}
	sort.Strings(normal)
	sort.Strings(last)
	return append(normal, last...)
}

// optionalSectionBlock renders the EOF block for a fully-absent section. When the
// template block is wholly commented (every non-blank line is a comment) it is
// used verbatim, preserving the header, key order and prose a fresh `vp init`
// would carry. Otherwise — a section whose template block carries ACTIVE
// (canonical) keys, which in the real flow cannot reach EOF-append because the
// canonical pass creates that section first — it synthesizes a commented
// "# [section]" header plus ONLY the optional keys' commented snippets, so the
// optional pass never injects an active canonical key.
func optionalSectionBlock(section string, keys []string, block string) string {
	if block != "" && blockIsWhollyCommented(block) {
		return block
	}
	var b []string
	if section != "" {
		b = append(b, "# ["+section+"]")
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	for _, k := range sorted {
		short := k
		if i := lastDot(k); i >= 0 {
			short = k[i+1:]
		}
		s := extractCommentedKeySnippet(block, short)
		if s == "" {
			s = "# " + short + ` = ""`
		}
		b = append(b, splitLines(s)...)
	}
	return joinLines(b)
}

// blockIsWhollyCommented reports whether every non-blank line of block is a
// comment (no active key or active section header).
func blockIsWhollyCommented(block string) bool {
	for _, line := range splitLines(block) {
		t := trimLeftSpace(line)
		if len(t) == 0 || t[0] == '#' {
			continue
		}
		return false
	}
	return true
}

// appendBlockAtEOF appends a verbatim (already fully commented) template block to
// text, separated by a blank line. A no-op for an empty block.
func appendBlockAtEOF(text, block string) string {
	if block == "" {
		return text
	}
	if text == "" {
		return block
	}
	return text + "\n\n" + block
}
