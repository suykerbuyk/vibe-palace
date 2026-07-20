// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package palace implements the palace architecture: structural metadata,
// graph navigation, and AAAK compression.
//
// AAAK (Autonomous Adaptive Associative Knowledge) is a lossy compression
// format that produces token-efficient structured summaries, intended to give
// compact digests for context loading.
//
// PARKED — implemented but not wired into any production path. AAAK is built
// and unit tested, but no production code calls Compress or CompressBatch:
// nothing in the tree produces or consumes an AAAK digest today, and no
// context-loading path uses one. The code is kept deliberately (it may yet
// earn a caller) but must not be read as a live capability. See the AAAK
// sections of doc/ARCHITECTURE.md and doc/PRD-vibe-palace.md.
//
// Format: ZID:ENTITIES|topics|"key_quote"|WEIGHT|EMOTIONS|FLAGS
package palace

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// DrawerMetadata provides context for AAAK compression.
type DrawerMetadata struct {
	Wing       string
	Room       string
	Hall       string
	SourceType string
	SourceRef  string
	FiledAt    string
}

// AAKResult holds a single AAAK compression result.
type AAKResult struct {
	ZID      string   `json:"zid"`
	Entities []string `json:"entities"`
	Topics   []string `json:"topics"`
	KeyQuote string   `json:"key_quote"`
	Weight   int      `json:"weight"`
	Emotions []string `json:"emotions"`
	Flags    []string `json:"flags"`
	Raw      string   `json:"raw"`
}

// EntityRegistry tracks entity-to-code mappings for uniqueness within a batch.
type EntityRegistry struct {
	codes map[string]string // entity name → 3-letter code
	used  map[string]bool   // codes already assigned
}

// NewEntityRegistry creates an empty entity registry.
func NewEntityRegistry() *EntityRegistry {
	return &EntityRegistry{
		codes: make(map[string]string),
		used:  make(map[string]bool),
	}
}

// Code returns or generates a unique 3-letter uppercase code for the entity.
func (r *EntityRegistry) Code(entity string) string {
	lower := strings.ToLower(entity)
	if code, ok := r.codes[lower]; ok {
		return code
	}
	code := generateCode(entity)
	// Ensure uniqueness by appending digits if needed.
	base := code
	for i := 0; r.used[code]; i++ {
		code = base[:2] + string(rune('0'+i%10))
	}
	r.codes[lower] = code
	r.used[code] = true
	return code
}

// generateCode produces a 3-letter uppercase code from an entity name.
// Prefers first 3 consonants; falls back to first 3 alphanumeric chars.
func generateCode(name string) string {
	upper := strings.ToUpper(name)
	vowels := "AEIOU"

	var consonants []byte
	for i := range len(upper) {
		c := upper[i]
		if c >= 'A' && c <= 'Z' && !strings.ContainsRune(vowels, rune(c)) {
			consonants = append(consonants, c)
			if len(consonants) == 3 {
				return string(consonants)
			}
		}
	}

	// Fallback: first 3 alphanumeric chars.
	var chars []byte
	for i := range len(upper) {
		c := upper[i]
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			chars = append(chars, c)
			if len(chars) == 3 {
				return string(chars)
			}
		}
	}

	// Pad with X if very short.
	for len(chars) < 3 {
		chars = append(chars, 'X')
	}
	return string(chars)
}

// Compress produces an AAAK summary of drawer content.
func Compress(text string, meta DrawerMetadata, reg *EntityRegistry) AAKResult {
	if reg == nil {
		reg = NewEntityRegistry()
	}

	entities := detectEntities(text, reg)
	topics := extractTopics(text, 3)
	keyQuote := extractKeyQuote(text, entities)
	flags := detectFlags(text)
	emotions := detectEmotions(text)
	weight := computeWeight(text, meta, entities, flags)

	raw := formatAAK(meta.SourceRef, entities, topics, keyQuote, weight, emotions, flags)

	return AAKResult{
		ZID:      meta.SourceRef,
		Entities: entities,
		Topics:   topics,
		KeyQuote: keyQuote,
		Weight:   weight,
		Emotions: emotions,
		Flags:    flags,
		Raw:      raw,
	}
}

// CompressBatch applies Compress to each drawer+metadata pair, sharing the
// entity registry across the batch for consistent codes.
func CompressBatch(drawers []storage.Drawer, metas []DrawerMetadata) []AAKResult {
	reg := NewEntityRegistry()
	results := make([]AAKResult, len(drawers))
	for i := range drawers {
		var m DrawerMetadata
		if i < len(metas) {
			m = metas[i]
		}
		if m.SourceRef == "" {
			m.SourceRef = drawers[i].ID
		}
		results[i] = Compress(drawers[i].Content, m, reg)
	}
	return results
}

// --- Entity detection ---

// entityPattern matches capitalized multi-word names (simple heuristic).
var entityPattern = regexp.MustCompile(`\b[A-Z][a-z]+(?:\s+[A-Z][a-z]+)+\b`)

func detectEntities(text string, reg *EntityRegistry) []string {
	matches := entityPattern.FindAllString(text, -1)
	seen := make(map[string]bool)
	var codes []string
	for _, m := range matches {
		lower := strings.ToLower(m)
		if seen[lower] {
			continue
		}
		seen[lower] = true
		codes = append(codes, reg.Code(m))
	}
	sort.Strings(codes)
	return codes
}

// --- Topic extraction ---

func extractTopics(text string, n int) []string {
	words := tokenize(text)
	freq := make(map[string]int)
	for _, w := range words {
		lower := strings.ToLower(w)
		if len(lower) < 3 || stopWords[lower] {
			continue
		}
		freq[lower]++
	}

	type kv struct {
		word  string
		count int
	}
	var ranked []kv
	for w, c := range freq {
		ranked = append(ranked, kv{w, c})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].count != ranked[j].count {
			return ranked[i].count > ranked[j].count
		}
		return ranked[i].word < ranked[j].word
	})

	var topics []string
	for i := range ranked {
		if i >= n {
			break
		}
		topics = append(topics, ranked[i].word)
	}
	return topics
}

func tokenize(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_'
	})
}

// --- Key quote extraction ---

var decisionKeywords = []string{
	"decided", "chose", "will use", "agreed", "committed to",
	"decision", "selected", "picked", "opted",
}

func extractKeyQuote(text string, entityCodes []string) string {
	sentences := splitSentences(text)
	if len(sentences) == 0 {
		return ""
	}

	bestScore := -1
	bestIdx := 0

	for i, s := range sentences {
		score := scoreSentence(s, entityCodes)
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	quote := strings.TrimSpace(sentences[bestIdx])
	if len(quote) > 120 {
		// Truncate at word boundary.
		if idx := strings.LastIndex(quote[:117], " "); idx > 0 {
			quote = quote[:idx] + "..."
		} else {
			quote = quote[:117] + "..."
		}
	}
	return quote
}

func scoreSentence(s string, entityCodes []string) int {
	lower := strings.ToLower(s)
	score := 0

	for _, kw := range decisionKeywords {
		if strings.Contains(lower, kw) {
			score += 3
			break
		}
	}

	if len(entityCodes) > 0 {
		// Check for capitalized names as a proxy for entity mentions.
		if entityPattern.MatchString(s) {
			score += 2
		}
	}

	wordCount := len(strings.Fields(s))
	lengthBonus := wordCount / 10
	lengthBonus = min(lengthBonus, 3)
	score += lengthBonus

	return score
}

func splitSentences(text string) []string {
	// Split on sentence-ending punctuation followed by whitespace or end.
	re := regexp.MustCompile(`[.!?]+\s+`)
	parts := re.Split(text, -1)
	var sentences []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			sentences = append(sentences, trimmed)
		}
	}
	return sentences
}

// --- Emotion detection ---

type emotionDef struct {
	name     string
	keywords []string
}

var emotionDefs = []emotionDef{
	{"joy", []string{"happy", "pleased", "delighted", "glad", "thrilled"}},
	{"trust", []string{"trust", "reliable", "dependable", "confident in", "faith"}},
	{"fear", []string{"afraid", "scared", "worried about", "terrified", "dreading"}},
	{"surprise", []string{"surprised", "unexpected", "astonished", "shocked", "wow"}},
	{"sadness", []string{"sad", "disappointed", "unhappy", "unfortunate", "regret"}},
	{"disgust", []string{"disgusting", "terrible", "awful", "horrible", "repulsive"}},
	{"anger", []string{"angry", "furious", "outraged", "infuriated", "livid"}},
	{"anticipation", []string{"looking forward", "anticipate", "expecting", "upcoming", "excited about"}},
	{"frustration", []string{"frustrated", "annoying", "stuck", "struggling", "painful"}},
	{"confusion", []string{"confused", "unclear", "puzzling", "baffling", "makes no sense"}},
	{"satisfaction", []string{"satisfied", "accomplished", "pleased with", "works well", "nailed it"}},
	{"curiosity", []string{"curious", "interesting", "wonder", "intriguing", "fascinating"}},
	{"relief", []string{"relieved", "finally", "at last", "thankfully", "phew"}},
	{"pride", []string{"proud", "achievement", "accomplished", "impressive", "well done"}},
	{"doubt", []string{"doubt", "skeptical", "uncertain", "questionable", "not sure"}},
	{"urgency", []string{"urgent", "immediately", "asap", "critical", "deadline"}},
	{"excitement", []string{"exciting", "amazing", "awesome", "fantastic", "brilliant"}},
	{"concern", []string{"concerned", "troubling", "worrisome", "alarming", "risky"}},
	{"optimism", []string{"optimistic", "hopeful", "promising", "positive", "bright"}},
	{"pessimism", []string{"pessimistic", "hopeless", "bleak", "doomed", "unlikely"}},
	{"determination", []string{"determined", "committed", "persistent", "resolute", "will not give up"}},
	{"resignation", []string{"resigned", "accepted", "inevitable", "nothing we can do", "give up"}},
	{"confidence", []string{"confident", "certain", "sure", "no doubt", "absolutely"}},
	{"anxiety", []string{"anxious", "nervous", "uneasy", "tense", "on edge"}},
}

func detectEmotions(text string) []string {
	lower := strings.ToLower(text)
	var found []string
	for _, e := range emotionDefs {
		for _, kw := range e.keywords {
			if strings.Contains(lower, kw) {
				found = append(found, e.name)
				break
			}
		}
	}
	return found
}

// --- Flag detection ---

type flagDef struct {
	name     string
	keywords []string
}

var flagDefs = []flagDef{
	{"ORIGIN", []string{"started", "created", "initialized", "first"}},
	{"CORE", []string{"fundamental", "core", "essential", "critical"}},
	{"SENSITIVE", []string{"secret", "credential", "password", "token", "private key"}},
	{"PIVOT", []string{"changed direction", "pivoted", "reversed", "abandoned"}},
	{"GENESIS", []string{"new project", "greenfield", "from scratch"}},
	{"DECISION", []string{"decided", "chose", "agreed", "committed to"}},
	{"TECHNICAL", nil}, // detected via patterns, not keywords
}

var codeBlockPattern = regexp.MustCompile("(?s)```.*?```")
var filePathPattern = regexp.MustCompile(`(?:^|[\s(])(/[\w./-]+|[\w.-]+\.\w{1,5})(?:[\s),:]|$)`)
var funcNamePattern = regexp.MustCompile(`\b\w+\(`)

func detectFlags(text string) []string {
	lower := strings.ToLower(text)
	var flags []string

	for _, f := range flagDefs {
		if f.name == "TECHNICAL" {
			continue
		}
		for _, kw := range f.keywords {
			if strings.Contains(lower, kw) {
				flags = append(flags, f.name)
				break
			}
		}
	}

	// TECHNICAL: code blocks, file paths, or function calls.
	if codeBlockPattern.MatchString(text) ||
		filePathPattern.MatchString(text) ||
		funcNamePattern.MatchString(text) {
		flags = append(flags, "TECHNICAL")
	}

	return flags
}

// --- Weight computation ---

func computeWeight(text string, meta DrawerMetadata, entities, flags []string) int {
	weight := 1 // base

	lower := strings.ToLower(text)
	for _, kw := range decisionKeywords {
		if strings.Contains(lower, kw) {
			weight++
			break
		}
	}

	if len(entities) > 0 {
		weight++
	}

	if meta.Hall == HallDecisions || meta.Hall == HallDiscoveries {
		weight++
	}

	if len(flags) > 0 {
		weight++
	}

	if weight > 5 {
		weight = 5
	}
	return weight
}

// --- Formatting ---

func formatAAK(zid string, entities, topics []string, keyQuote string, weight int, emotions, flags []string) string {
	entStr := strings.Join(entities, ",")
	topicStr := strings.Join(topics, ",")
	emotionStr := strings.Join(emotions, ",")
	flagStr := strings.Join(flags, ",")

	return fmt.Sprintf(`%s:%s|%s|"%s"|%d|%s|%s`,
		zid, entStr, topicStr, keyQuote, weight, emotionStr, flagStr)
}

// --- Stop words ---

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "but": true,
	"not": true, "you": true, "all": true, "can": true, "had": true,
	"her": true, "was": true, "one": true, "our": true, "out": true,
	"has": true, "have": true, "been": true, "were": true, "they": true,
	"this": true, "that": true, "with": true, "from": true, "will": true,
	"what": true, "when": true, "which": true, "their": true, "said": true,
	"each": true, "about": true, "would": true, "there": true, "them": true,
	"then": true, "than": true, "could": true, "other": true, "into": true,
	"more": true, "some": true, "such": true, "time": true, "very": true,
	"just": true, "know": true, "take": true, "come": true, "make": true,
	"like": true, "long": true, "look": true, "many": true, "over": true,
	"also": true, "back": true, "after": true, "only": true, "most": true,
	"being": true, "where": true, "does": true, "here": true, "much": true,
	"both": true, "those": true, "well": true, "still": true, "these": true,
	"because": true, "before": true, "should": true, "between": true,
	"under": true, "never": true, "same": true, "another": true,
	"while": true, "last": true, "might": true, "great": true,
	"since": true, "through": true, "even": true, "down": true,
	"way": true, "who": true, "did": true, "get": true, "own": true,
	"too": true, "any": true, "its": true, "how": true, "use": true,
	"him": true, "his": true, "she": true, "may": true, "let": true,
}
