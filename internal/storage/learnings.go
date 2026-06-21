// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// learningTypes is the set of valid Learning.Type values. Learnings are
// deliberately cross-project, so the per-project "project" type is rejected.
var learningTypes = map[string]bool{
	"user":      true,
	"feedback":  true,
	"reference": true,
}

// LearningMetadata is the lightweight, body-free metadata for a stored
// learning. Slug is the filename stem (not a frontmatter field); name,
// description, and type come from the file's flat top-level frontmatter.
type LearningMetadata struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
}

// Learning is a full learning: its metadata plus the markdown body.
type Learning struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Content     string `json:"content"`
}

// learningFrontmatter is the on-disk frontmatter shape. Unlike memory files,
// the type lives at the flat top level (not nested under metadata:). Unknown
// fields (e.g. originSessionId) are ignored by yaml.Unmarshal.
type learningFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Type        string `yaml:"type"`
}

// LearningsDir returns the path to the vault-wide learnings directory:
// {vault}/Knowledge/learnings. Learnings are cross-project, so this is not
// parameterized by project.
func (v *Vault) LearningsDir() string {
	return filepath.Join(v.Root, "Knowledge", "learnings")
}

// parseLearningFrontmatter splits a learning file with YAML frontmatter into
// metadata and body. The frontmatter is delimited by "---" on its own lines.
// It mirrors parseMemoryFrontmatter's fence handling but decodes into the flat
// learning shape and enforces the required name/description/type fields and the
// allowed type set.
func parseLearningFrontmatter(data []byte) (LearningMetadata, string, error) {
	content := string(data)

	if !strings.HasPrefix(content, "---\n") {
		return LearningMetadata{}, "", fmt.Errorf("missing opening frontmatter delimiter")
	}

	// Find the closing "---" after the opening one.
	rest := content[4:] // skip opening "---\n"
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		// Try ending with "---" at EOF (no trailing newline after delimiter).
		if strings.HasSuffix(rest, "\n---") {
			idx = len(rest) - 4
		} else {
			return LearningMetadata{}, "", fmt.Errorf("missing closing frontmatter delimiter")
		}
	}

	yamlStr := rest[:idx]
	body := ""
	closingEnd := idx + 5 // len("\n---\n")
	if closingEnd <= len(rest) {
		body = rest[closingEnd:]
	}

	var fm learningFrontmatter
	if err := yaml.Unmarshal([]byte(yamlStr), &fm); err != nil {
		return LearningMetadata{}, "", fmt.Errorf("parse learning frontmatter YAML: %w", err)
	}
	if fm.Name == "" {
		return LearningMetadata{}, "", fmt.Errorf("learning frontmatter missing required field: name")
	}
	if fm.Description == "" {
		return LearningMetadata{}, "", fmt.Errorf("learning frontmatter missing required field: description")
	}
	if fm.Type == "" {
		return LearningMetadata{}, "", fmt.Errorf("learning frontmatter missing required field: type")
	}
	if !learningTypes[fm.Type] {
		return LearningMetadata{}, "", fmt.Errorf("learning type %q must be one of user, feedback, reference", fm.Type)
	}

	meta := LearningMetadata{
		Name:        fm.Name,
		Description: fm.Description,
		Type:        fm.Type,
	}
	return meta, body, nil
}

// collectLearnings walks the vault-wide learnings directory and returns the
// frontmatter metadata of every valid learning, unsorted. Only frontmatter is
// parsed; bodies are not retained. A missing directory yields a nil slice and
// nil error. Non-.md files and subdirectories are ignored, and files that fail
// to parse are logged and skipped rather than failing the whole walk. If
// filterType is non-empty, only learnings whose Type matches are returned.
//
// ListLearnings and CountLearnings share this walk so the two never diverge on
// what counts as a valid learning.
func (v *Vault) collectLearnings(filterType string) ([]LearningMetadata, error) {
	dir := v.LearningsDir()

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read learnings dir: %w", err)
	}

	var result []LearningMetadata
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			slog.Warn("skip unreadable learning", "path", p, "err", err)
			continue
		}
		meta, _, err := parseLearningFrontmatter(data)
		if err != nil {
			slog.Warn("skip unparseable learning", "path", p, "err", err)
			continue
		}
		meta.Slug = strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if filterType != "" && meta.Type != filterType {
			continue
		}
		result = append(result, meta)
	}
	return result, nil
}

// learningSlugs returns the filename stems of every .md file in the vault-wide
// learnings directory, without reading or parsing file contents. It backs the
// not-found error message, where only the available names are needed — a full
// parse of every learning just to typo-correct one slug would be wasteful.
// os.ReadDir returns entries in sorted order, so the result is deterministic.
func (v *Vault) learningSlugs() []string {
	entries, err := os.ReadDir(v.LearningsDir())
	if err != nil {
		return nil
	}
	var slugs []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		slugs = append(slugs, strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
	}
	return slugs
}

// ListLearnings returns body-free metadata for every valid learning in the
// vault-wide learnings directory, sorted by Slug. A missing directory (or one
// with no valid learnings) yields an empty, non-nil slice and nil error. If
// filterType is non-empty, only learnings whose Type matches are returned.
func (v *Vault) ListLearnings(filterType string) ([]LearningMetadata, error) {
	result, err := v.collectLearnings(filterType)
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Slug < result[j].Slug
	})
	if result == nil {
		result = []LearningMetadata{}
	}
	return result, nil
}

// GetLearning reads a single learning by slug, returning its metadata and
// body. The slug must be a bare filename stem: a slug containing a path
// separator or ".." (or an empty slug) is rejected to prevent traversal. A
// single leading blank line is trimmed from the body if present. An unknown
// slug yields an error whose message lists the available slugs.
func (v *Vault) GetLearning(slug string) (Learning, error) {
	if slug == "" {
		return Learning{}, fmt.Errorf("learning slug must not be empty")
	}
	if strings.Contains(slug, "..") || strings.ContainsRune(slug, '/') || strings.ContainsRune(slug, filepath.Separator) {
		return Learning{}, fmt.Errorf("learning slug %q must not contain path separators or \"..\"", slug)
	}

	dir := v.LearningsDir()
	p := filepath.Join(dir, slug+".md")
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			slugs := v.learningSlugs()
			if len(slugs) == 0 {
				return Learning{}, fmt.Errorf("learning %q not found: no learnings available", slug)
			}
			return Learning{}, fmt.Errorf("learning %q not found: available slugs: %s", slug, strings.Join(slugs, ", "))
		}
		return Learning{}, fmt.Errorf("read learning %s: %w", slug, err)
	}

	meta, body, err := parseLearningFrontmatter(data)
	if err != nil {
		return Learning{}, fmt.Errorf("parse learning %s: %w", slug, err)
	}
	body = strings.TrimPrefix(body, "\n")

	return Learning{
		Slug:        slug,
		Name:        meta.Name,
		Description: meta.Description,
		Type:        meta.Type,
		Content:     body,
	}, nil
}

// CountLearnings returns the number of valid learnings in the vault-wide
// learnings directory. A missing directory yields 0 and nil error. It shares
// collectLearnings with ListLearnings (so malformed files are excluded the
// same way) but skips the sort and slice copy that a bare count does not need.
func (v *Vault) CountLearnings() (int, error) {
	result, err := v.collectLearnings("")
	if err != nil {
		return 0, err
	}
	return len(result), nil
}
