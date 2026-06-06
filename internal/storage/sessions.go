// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"gopkg.in/yaml.v3"
)

// SessionMeta is the lightweight metadata parsed from YAML frontmatter.
type SessionMeta struct {
	ID            string   `yaml:"session_id"`
	Project       string   `yaml:"project"`
	Date          string   `yaml:"date"`
	Iteration     int      `yaml:"iteration"`
	Title         string   `yaml:"title,omitempty"`
	Summary       string   `yaml:"summary,omitempty"`
	Tag           string   `yaml:"tag,omitempty"`
	Model         string   `yaml:"model,omitempty"`
	Branch        string   `yaml:"branch,omitempty"`
	Domain        string   `yaml:"domain,omitempty"`
	DurationMin   int      `yaml:"duration_minutes,omitempty"`
	Messages      int      `yaml:"messages,omitempty"`
	TokensIn      int      `yaml:"tokens_in,omitempty"`
	TokensOut     int      `yaml:"tokens_out,omitempty"`
	ToolUses      int      `yaml:"tool_uses,omitempty"`
	FrictionScore int      `yaml:"friction_score,omitempty"`
	Decisions     []string `yaml:"decisions,omitempty"`
	FilesChanged  []string `yaml:"files_changed,omitempty"`
	OpenThreads   []string `yaml:"open_threads,omitempty"`
	NotePath      string   `yaml:"note_path,omitempty"`
	// Archive is the vault-relative path to the transcript manifest
	// associated with this session, if one was archived. Written by
	// vp_capture_session when session_id + adapter resolve to an
	// existing archive. See doc/adr/001-transcript-archive.md.
	Archive string `yaml:"archive,omitempty"`
	// NeedsIndexing marks hook-captured sessions for deferred transcript
	// indexing. Omitted from YAML when false so existing sessions are unaffected.
	NeedsIndexing bool `yaml:"needs_indexing,omitempty"`
}

// WriteSession writes a session markdown file with YAML frontmatter.
// It auto-increments the iteration number and returns the session ID.
func (v *Vault) WriteSession(project string, meta SessionMeta, body string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	if !datePattern.MatchString(meta.Date) {
		return "", fmt.Errorf("date %q must be in YYYY-MM-DD format", meta.Date)
	}

	iteration, err := v.NextIteration(project, meta.Date)
	if err != nil {
		return "", err
	}

	meta.Iteration = iteration
	meta.Project = project
	meta.ID = fmt.Sprintf("%s-%02d", meta.Date, iteration)

	path, err := v.SessionFile(project, meta.Date, iteration)
	if err != nil {
		return "", err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return "", fmt.Errorf("ensure sessions dir: %w", err)
	}

	yamlBytes, err := yaml.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("marshal session meta: %w", err)
	}

	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(yamlBytes)
	buf.WriteString("---\n")
	if body != "" {
		buf.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			buf.WriteByte('\n')
		}
	}

	if err := atomicfile.Write(v.Root, path, buf.Bytes()); err != nil {
		return "", fmt.Errorf("write session file: %w", err)
	}
	return meta.ID, nil
}

// ReadSession reads a session file and returns its metadata and body.
func (v *Vault) ReadSession(project, date string, iteration int) (SessionMeta, string, error) {
	path, err := v.SessionFile(project, date, iteration)
	if err != nil {
		return SessionMeta{}, "", err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return SessionMeta{}, "", fmt.Errorf("read session file: %w", err)
	}

	return ParseFrontmatter(data)
}

// ListSessions returns session metadata filtered by date range and limited
// to the specified count. Pass empty strings for dateFrom/dateTo to skip
// date filtering. Pass 0 for limit to return all matches.
func (v *Vault) ListSessions(project, dateFrom, dateTo string, limit int) ([]SessionMeta, error) {
	dir, err := v.SessionDir(project)
	if err != nil {
		return nil, err
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("glob sessions: %w", err)
	}
	sort.Strings(matches)

	var result []SessionMeta
	for _, m := range matches {
		base := filepath.Base(m)
		// Filename format: YYYY-MM-DD-NN.md — date is first 10 chars.
		if len(base) < 10 {
			continue
		}
		date := base[:10]
		if dateFrom != "" && date < dateFrom {
			continue
		}
		if dateTo != "" && date > dateTo {
			continue
		}

		data, err := os.ReadFile(m)
		if err != nil {
			return nil, fmt.Errorf("read session %s: %w", m, err)
		}
		meta, _, err := ParseFrontmatter(data)
		if err != nil {
			return nil, fmt.Errorf("parse session %s: %w", m, err)
		}
		result = append(result, meta)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

// SearchSessions returns sessions matching a text query and/or minimum
// friction score. The query matches against title, summary, and decisions
// (case-insensitive). Pass empty query and 0 minFriction to match all.
func (v *Vault) SearchSessions(query, project string, minFriction, limit int) ([]SessionMeta, error) {
	all, err := v.ListSessions(project, "", "", 0)
	if err != nil {
		return nil, err
	}

	lowerQuery := strings.ToLower(query)
	var result []SessionMeta
	for _, meta := range all {
		if minFriction > 0 && meta.FrictionScore < minFriction {
			continue
		}
		if query != "" && !sessionMatchesQuery(meta, lowerQuery) {
			continue
		}
		result = append(result, meta)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

// NextIteration returns the next available iteration number for a
// project+date combination (1-based).
func (v *Vault) NextIteration(project, date string) (int, error) {
	dir, err := v.SessionDir(project)
	if err != nil {
		return 0, err
	}

	pattern := filepath.Join(dir, date+"-*.md")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return 0, fmt.Errorf("glob iterations: %w", err)
	}
	return len(matches) + 1, nil
}

// ParseFrontmatter splits a markdown file with YAML frontmatter into
// metadata and body. The frontmatter is delimited by "---" on its own lines.
func ParseFrontmatter(data []byte) (SessionMeta, string, error) {
	content := string(data)

	if !strings.HasPrefix(content, "---\n") {
		return SessionMeta{}, "", fmt.Errorf("missing opening frontmatter delimiter")
	}

	// Find the closing "---" after the opening one.
	rest := content[4:] // skip opening "---\n"
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		// Try ending with "---" at EOF (no trailing newline after delimiter).
		if strings.HasSuffix(rest, "\n---") {
			idx = len(rest) - 4
		} else {
			return SessionMeta{}, "", fmt.Errorf("missing closing frontmatter delimiter")
		}
	}

	yamlStr := rest[:idx]
	body := ""
	closingEnd := idx + 5 // len("\n---\n")
	if closingEnd <= len(rest) {
		body = rest[closingEnd:]
	}

	var meta SessionMeta
	if err := yaml.Unmarshal([]byte(yamlStr), &meta); err != nil {
		return SessionMeta{}, "", fmt.Errorf("parse frontmatter YAML: %w", err)
	}
	return meta, body, nil
}

// sessionMatchesQuery checks if any searchable field contains the query.
func sessionMatchesQuery(meta SessionMeta, lowerQuery string) bool {
	if strings.Contains(strings.ToLower(meta.Title), lowerQuery) {
		return true
	}
	if strings.Contains(strings.ToLower(meta.Summary), lowerQuery) {
		return true
	}
	for _, d := range meta.Decisions {
		if strings.Contains(strings.ToLower(d), lowerQuery) {
			return true
		}
	}
	return false
}
