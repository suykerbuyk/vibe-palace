// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package kg

import (
	"regexp"
	"strings"
)

// EntityType categorizes detected entities.
type EntityType string

const (
	EntityPerson  EntityType = "person"
	EntityProject EntityType = "project"
	EntityConcept EntityType = "concept"
	EntityTool    EntityType = "tool"
)

// DetectedEntity represents an entity found in text.
type DetectedEntity struct {
	Name       string
	Type       EntityType
	Confidence float64
}

// Compiled regex patterns for entity detection.
var (
	// Person: "Name said/thinks/mentioned/suggested/decided/proposed/argued/noted"
	personVerbRe = regexp.MustCompile(`\b([A-Z][a-z]+(?:\s+[A-Z][a-z]+)*)\s+(?:said|thinks|mentioned|suggested|decided|proposed|argued|noted|believes|explained|recommended|asked|told|reported|confirmed|stated)\b`)

	// Person: capitalized multi-word name (2+ words, each capitalized).
	capitalNameRe = regexp.MustCompile(`\b([A-Z][a-z]{1,20}(?:\s+[A-Z][a-z]{1,20})+)\b`)

	// Project: hyphenated lowercase names near project signals.
	projectNameRe = regexp.MustCompile(`\b([a-z][a-z0-9]*(?:-[a-z0-9]+)+)\b`)
	projectCtxRe  = regexp.MustCompile(`(?i)\b(?:deploy|build|ship|launch|release|repo|repository|project|codebase|branch|merge|PR|pull request)\b`)
	versionRefRe  = regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9_-]*)\s+v\d+`)

	// Tool: "using/with/via X" pattern.
	toolPatternRe = regexp.MustCompile(`(?i)\b(?:using|with|via|in)\s+([A-Z][A-Za-z0-9]+)\b`)

	// Concept: capitalized terms near conceptual signals.
	conceptTermRe = regexp.MustCompile(`\b([A-Z][a-z]+(?:\s+[A-Z][a-z]+)*)\s+(?:architecture|pattern|strategy|principle|approach|paradigm|methodology|framework|design)\b`)
	conceptCtxRe  = regexp.MustCompile(`(?i)\b(?:architecture|pattern|strategy|principle|approach|paradigm|methodology|framework|design)\s+(?:called|named|known as)\s+([A-Z][A-Za-z\s]+)`)
)

// knownTools is a curated set of well-known developer tools.
var knownTools = map[string]bool{
	"Docker": true, "Git": true, "Postgres": true, "PostgreSQL": true,
	"Redis": true, "Kubernetes": true, "Terraform": true, "Ansible": true,
	"Jenkins": true, "GitHub": true, "GitLab": true, "Jira": true,
	"Linear": true, "Slack": true, "Grafana": true, "Prometheus": true,
	"Datadog": true, "Sentry": true, "Nginx": true, "Apache": true,
	"MySQL": true, "MongoDB": true, "Elasticsearch": true, "Kafka": true,
	"RabbitMQ": true, "React": true, "Vue": true, "Angular": true,
	"Next": true, "Vite": true, "Webpack": true, "Babel": true,
	"TypeScript": true, "Python": true, "Rust": true, "Go": true,
	"Node": true, "Deno": true, "Bun": true, "VSCode": true,
	"Neovim": true, "Vim": true, "Emacs": true, "Zed": true,
	"Claude": true, "GPT": true, "Copilot": true, "Cursor": true,
	"AWS": true, "GCP": true, "Azure": true, "Vercel": true,
	"Netlify": true, "Fly": true, "Railway": true, "Supabase": true,
	"Firebase": true, "Stripe": true, "Auth0": true, "Okta": true,
}

// commonFalsePositives are capitalized words that aren't entity names.
var commonFalsePositives = map[string]bool{
	"The": true, "This": true, "That": true, "These": true, "Those": true,
	"Then": true, "There": true, "Their": true, "They": true, "Here": true,
	"When": true, "Where": true, "Which": true, "While": true, "What": true,
	"Also": true, "After": true, "Before": true, "Since": true, "Until": true,
	"However": true, "Although": true, "Because": true, "Therefore": true,
	"Moreover": true, "Furthermore": true, "Additionally": true,
	"But": true, "And": true, "For": true, "Not": true, "Yet": true,
	"Still": true, "Just": true, "Some": true, "Most": true, "Many": true,
	"Each": true, "Every": true, "Both": true, "Either": true, "Neither": true,
	"Other": true, "Another": true, "Such": true, "Rather": true,
	"Perhaps": true, "Maybe": true, "Already": true, "Always": true,
	"Never": true, "Often": true, "Sometimes": true, "Usually": true,
	"Currently": true, "Recently": true, "Previously": true,
	"Now": true, "Very": true, "Quite": true, "Really": true,
	"All": true, "Any": true, "No": true, "Yes": true,
	"How": true, "Why": true, "Who": true, "Our": true,
	"My": true, "Your": true, "His": true, "Her": true, "Its": true,
	"We": true, "You": true, "He": true, "She": true, "It": true,
	"If": true, "So": true, "As": true, "Or": true, "Do": true,
	"Let": true, "May": true, "Can": true, "Will": true, "Did": true,
	"Was": true, "Were": true, "Has": true, "Had": true, "Would": true,
	"Could": true, "Should": true, "Might": true, "Must": true,
	"Being": true, "Having": true, "Doing": true, "Going": true,
	"First": true, "Second": true, "Third": true, "Last": true, "Next": true,
	"New": true, "Old": true, "One": true, "Two": true,
	"Right": true, "Well": true, "Much": true, "Long": true,
	"Part": true, "Sure": true, "Good": true, "Bad": true, "Great": true,
	"Like": true, "Same": true, "Even": true, "Only": true,
	"True": true, "False": true, "None": true, "Null": true,
}

// DetectEntities finds person, project, tool, and concept entities in text.
func DetectEntities(text string) []DetectedEntity {
	if strings.TrimSpace(text) == "" {
		return nil
	}

	candidates := make(map[string]*DetectedEntity)

	detectPersons(text, candidates)
	detectTools(text, candidates)
	detectProjects(text, candidates)
	detectConcepts(text, candidates)

	var result []DetectedEntity
	for _, e := range candidates {
		if e.Confidence >= 0.3 {
			result = append(result, *e)
		}
	}
	return result
}

func detectPersons(text string, candidates map[string]*DetectedEntity) {
	// Verb pattern matches (strong signal).
	for _, m := range personVerbRe.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if isFalsePositive(name) {
			continue
		}
		key := normalize(name)
		if e, ok := candidates[key]; ok {
			if e.Type == EntityPerson {
				e.Confidence = clamp(e.Confidence + 0.4)
			}
		} else {
			candidates[key] = &DetectedEntity{Name: name, Type: EntityPerson, Confidence: 0.7}
		}
	}

	// Capitalized multi-word names (weaker signal).
	for _, m := range capitalNameRe.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if isFalsePositive(name) || isKnownTool(name) {
			continue
		}
		key := normalize(name)
		if _, ok := candidates[key]; !ok {
			candidates[key] = &DetectedEntity{Name: name, Type: EntityPerson, Confidence: 0.3}
		}
	}
}

func detectTools(text string, candidates map[string]*DetectedEntity) {
	// Known tools mentioned anywhere.
	words := regexp.MustCompile(`\b([A-Z][A-Za-z0-9]+)\b`).FindAllStringSubmatch(text, -1)
	for _, m := range words {
		name := m[1]
		if knownTools[name] {
			key := normalize(name)
			candidates[key] = &DetectedEntity{Name: name, Type: EntityTool, Confidence: 0.8}
		}
	}

	// "using/with/via X" pattern.
	for _, m := range toolPatternRe.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if isFalsePositive(name) {
			continue
		}
		key := normalize(name)
		if _, ok := candidates[key]; !ok {
			candidates[key] = &DetectedEntity{Name: name, Type: EntityTool, Confidence: 0.5}
		}
	}
}

func detectProjects(text string, candidates map[string]*DetectedEntity) {
	hasProjectCtx := projectCtxRe.MatchString(text)

	// Hyphenated lowercase names.
	for _, m := range projectNameRe.FindAllStringSubmatch(text, -1) {
		name := m[1]
		key := normalize(name)
		if _, ok := candidates[key]; ok {
			continue
		}
		conf := 0.3
		if hasProjectCtx {
			conf = 0.6
		}
		candidates[key] = &DetectedEntity{Name: name, Type: EntityProject, Confidence: conf}
	}

	// Version references: "X v1.2"
	for _, m := range versionRefRe.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if isFalsePositive(name) {
			continue
		}
		key := normalize(name)
		if _, ok := candidates[key]; !ok {
			candidates[key] = &DetectedEntity{Name: name, Type: EntityProject, Confidence: 0.7}
		}
	}
}

func detectConcepts(text string, candidates map[string]*DetectedEntity) {
	// "X architecture/pattern/strategy"
	for _, m := range conceptTermRe.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if isFalsePositive(name) {
			continue
		}
		key := normalize(name)
		if _, ok := candidates[key]; !ok {
			candidates[key] = &DetectedEntity{Name: name, Type: EntityConcept, Confidence: 0.5}
		}
	}

	// "pattern called/named X"
	for _, m := range conceptCtxRe.FindAllStringSubmatch(text, -1) {
		name := strings.TrimSpace(m[1])
		if isFalsePositive(name) {
			continue
		}
		key := normalize(name)
		if _, ok := candidates[key]; !ok {
			candidates[key] = &DetectedEntity{Name: name, Type: EntityConcept, Confidence: 0.6}
		}
	}
}

func isFalsePositive(name string) bool {
	// Check each word in multi-word names.
	words := strings.Fields(name)
	if len(words) == 1 {
		return commonFalsePositives[name]
	}
	// Multi-word: only false positive if ALL words are false positives.
	allFP := true
	for _, w := range words {
		if !commonFalsePositives[w] {
			allFP = false
			break
		}
	}
	return allFP
}

func isKnownTool(name string) bool {
	for w := range strings.FieldsSeq(name) {
		if knownTools[w] {
			return true
		}
	}
	return false
}

func normalize(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func clamp(f float64) float64 {
	if f > 1.0 {
		return 1.0
	}
	return f
}
