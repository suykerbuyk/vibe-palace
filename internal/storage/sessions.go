// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"gopkg.in/yaml.v3"
)

// FrictionBreakdown holds the four capped friction sub-scores that sum to the
// composite FrictionScore. It is stored as a pointer on SessionMeta with
// presence semantics: a nil pointer means friction was never broken down (old
// sessions), while a non-nil pointer with all-zero fields means a real,
// measured frictionless session. Each field is the already-capped sub-score
// (each 0-25), matching the composite computation. Total() reproduces the
// composite (sum, capped at 100).
type FrictionBreakdown struct {
	Corrections  int `yaml:"corrections"`
	Retries      int `yaml:"retries"`
	ErrorDensity int `yaml:"error_density"`
	Rework       int `yaml:"rework"`
}

// Total returns the composite friction score: the sum of the four sub-scores,
// capped at 100. It is the authoritative reconstruction of FrictionScore.
func (b FrictionBreakdown) Total() int {
	return min(b.Corrections+b.Retries+b.ErrorDensity+b.Rework, 100)
}

// SessionMeta is the lightweight metadata parsed from YAML frontmatter.
type SessionMeta struct {
	ID            string             `yaml:"session_id"`
	Project       string             `yaml:"project"`
	Date          string             `yaml:"date"`
	Iteration     int                `yaml:"iteration"`
	Title         string             `yaml:"title,omitempty"`
	Summary       string             `yaml:"summary,omitempty"`
	Tag           string             `yaml:"tag,omitempty"`
	Model         string             `yaml:"model,omitempty"`
	Branch        string             `yaml:"branch,omitempty"`
	Domain        string             `yaml:"domain,omitempty"`
	DurationMin   int                `yaml:"duration_minutes,omitempty"`
	Messages      int                `yaml:"messages,omitempty"`
	TokensIn      int                `yaml:"tokens_in,omitempty"`
	TokensOut     int                `yaml:"tokens_out,omitempty"`
	ToolUses      int                `yaml:"tool_uses,omitempty"`
	FrictionScore int                `yaml:"friction_score,omitempty"`
	Breakdown     *FrictionBreakdown `yaml:"friction_breakdown,omitempty"`
	Decisions     []string           `yaml:"decisions,omitempty"`
	FilesChanged  []string           `yaml:"files_changed,omitempty"`
	OpenThreads   []string           `yaml:"open_threads,omitempty"`
	NotePath      string             `yaml:"note_path,omitempty"`
	// Archive is the vault-relative path to the transcript manifest
	// associated with this session, if one was archived. Written by
	// vp_capture_session when session_id + adapter resolve to an
	// existing archive. See doc/adr/001-transcript-archive.md.
	Archive string `yaml:"archive,omitempty"`
	// NeedsIndexing marks hook-captured sessions for deferred transcript
	// indexing. Omitted from YAML when false so existing sessions are unaffected.
	NeedsIndexing bool `yaml:"needs_indexing,omitempty"`
	// EnrichedBy and EnrichedAt mark a session whose summary/decisions/threads
	// were synthesized by an LLM enrichment pass (set by the enrichment pass or
	// the Step-7 drain). EnrichedBy is the enriching model; EnrichedAt is an
	// RFC3339 timestamp. Both are omitted for plain heuristic captures.
	EnrichedBy string `yaml:"enriched_by,omitempty"`
	EnrichedAt string `yaml:"enriched_at,omitempty"`
}

// SessionRef identifies a session note that was just written: its ID plus the
// coordinates that resolve it again. It exists so a caller never has to
// re-derive where the note went by parsing the ID back apart — the writer that
// placed the file is the only thing entitled to say where it landed, and it now
// simply says so. NotePath is vault-relative (see SessionRelPath).
type SessionRef struct {
	ID          string
	NotePath    string
	Date        string
	Fingerprint string
	Iteration   int
}

// WriteSession writes a session markdown file with YAML frontmatter.
// It auto-increments the iteration number and returns the session ID.
// It is a thin wrapper over WriteSessionRef, which additionally reports where
// the note landed; prefer that one when you need the note's path.
func (v *Vault) WriteSession(project string, meta SessionMeta, body string) (string, error) {
	ref, err := v.WriteSessionRef(project, meta, body)
	if err != nil {
		return "", err
	}
	return ref.ID, nil
}

// WriteSessionRef writes a session markdown file with YAML frontmatter,
// auto-incrementing the iteration number, and returns the full identity of the
// note it wrote — ID, vault-relative path, and the (date, fp, iteration)
// coordinates that resolve it.
func (v *Vault) WriteSessionRef(project string, meta SessionMeta, body string) (SessionRef, error) {
	if err := slug.Validate(project); err != nil {
		return SessionRef{}, fmt.Errorf("project: %w", err)
	}
	if !datePattern.MatchString(meta.Date) {
		return SessionRef{}, fmt.Errorf("date %q must be in YYYY-MM-DD format", meta.Date)
	}

	// Fingerprint this writer (host+vault) so two offline hosts minting the
	// same (date, iteration) produce distinct filenames AND distinct meta.IDs,
	// avoiding git add/add conflicts and ambiguous session identity on sync.
	fp := surface.WriterFingerprint(v.Root)

	iteration, err := v.NextIteration(project, meta.Date, fp)
	if err != nil {
		return SessionRef{}, err
	}

	meta.Iteration = iteration
	meta.Project = project
	meta.ID = SessionStem(meta.Date, fp, iteration)

	// note_path is an identity coordinate, so the writer that decides where the
	// note goes is the only thing entitled to state where it went. It was yaml-
	// tagged and never assigned, so omitempty dropped the key from every note
	// ever written and every read-back unmarshalled a field that was not there:
	// vp_capture_session had reported note_path: "" for the entire life of the
	// project while returning status ok.
	rel, err := v.SessionRelPath(project, meta.Date, fp, iteration)
	if err != nil {
		return SessionRef{}, err
	}
	meta.NotePath = rel

	path, err := v.SessionFile(project, meta.Date, fp, iteration)
	if err != nil {
		return SessionRef{}, err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return SessionRef{}, fmt.Errorf("ensure sessions dir: %w", err)
	}

	data, err := marshalSessionFile(meta, body)
	if err != nil {
		return SessionRef{}, err
	}

	if err := v.lockedWrite(path, data); err != nil {
		return SessionRef{}, fmt.Errorf("write session file: %w", err)
	}
	return SessionRef{
		ID:          meta.ID,
		NotePath:    rel,
		Date:        meta.Date,
		Fingerprint: fp,
		Iteration:   iteration,
	}, nil
}

// marshalSessionFile assembles the on-disk session file bytes: the YAML
// frontmatter (marshaled from meta) framed by "---\n…---\n", followed by the
// body with exactly one trailing newline. WriteSession and RewriteSession
// share this helper so both writers produce byte-identical framing for the
// same (meta, body) pair.
func marshalSessionFile(meta SessionMeta, body string) ([]byte, error) {
	yamlBytes, err := yaml.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshal session meta: %w", err)
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
	return buf.Bytes(), nil
}

// RewriteSession overwrites an EXISTING session file in place at the fixed
// (project, date, iteration) path. Unlike WriteSession it does NOT allocate a
// new iteration — the iteration is the caller's responsibility — so it is
// idempotent: re-running it with the same (meta, body) yields byte-identical
// bytes. It is used by the Step-7 enrichment drain to rewrite a previously
// captured note with synthesized summary/decisions/threads. It shares only the
// marshalSessionFile framing helper with WriteSession and serializes against a
// concurrent WriteSession via the same per-path advisory lock (lockedWrite).
func (v *Vault) RewriteSession(project, date, fp string, iteration int, meta SessionMeta, body string) error {
	if err := slug.Validate(project); err != nil {
		return fmt.Errorf("project: %w", err)
	}
	if !datePattern.MatchString(date) {
		return fmt.Errorf("date %q must be in YYYY-MM-DD format", date)
	}

	path, err := v.SessionFile(project, date, fp, iteration)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure sessions dir: %w", err)
	}

	// Defensively pin the identity fields so a rewrite cannot drift the
	// note's own coordinates away from its path. The ID carries the
	// fingerprint when present so it matches the host-scoped filename.
	// note_path is pinned here too — it is a coordinate like the rest, and
	// pinning it backfills every note written before WriteSession set it
	// (the drain rewrites from a ReadSession whose note_path is empty for
	// those, and an empty field would otherwise be written straight back).
	meta.Project = project
	meta.Date = date
	meta.Iteration = iteration
	meta.ID = SessionStem(date, fp, iteration)

	rel, err := v.SessionRelPath(project, date, fp, iteration)
	if err != nil {
		return err
	}
	meta.NotePath = rel

	data, err := marshalSessionFile(meta, body)
	if err != nil {
		return err
	}

	if err := v.lockedWrite(path, data); err != nil {
		return fmt.Errorf("rewrite session file: %w", err)
	}
	return nil
}

// ReadSession reads a session file and returns its metadata and body. fp is
// the writer fingerprint that scoped the file at write time (empty for legacy
// host-agnostic notes). Because this resolves arbitrary — possibly other-host
// — sessions (e.g. via the MCP session resource), fp must be supplied by the
// caller, not recomputed for the local host.
func (v *Vault) ReadSession(project, date, fp string, iteration int) (SessionMeta, string, error) {
	path, err := v.SessionFile(project, date, fp, iteration)
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
// project+date combination (1-based). It is max(NN)+1 over the matching files
// — not count+1 — so a gap in the iteration set (e.g. 01 and 03 present, 02
// deleted) never re-mints an existing iteration and overwrites its note. The
// scan is scoped to the writer fingerprint fp so two offline hosts each
// allocate iterations independently (both may legitimately start at 01 for the
// same date). When fp is empty the legacy host-agnostic glob is used.
func (v *Vault) NextIteration(project, date, fp string) (int, error) {
	dir, err := v.SessionDir(project)
	if err != nil {
		return 0, err
	}

	pattern := filepath.Join(dir, SessionGlobPrefix(date, fp)+"*.md")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return 0, fmt.Errorf("glob iterations: %w", err)
	}
	next := 0
	for _, m := range matches {
		if it := iterationFromSessionFilename(m); it > next {
			next = it
		}
	}
	return next + 1, nil
}

// iterationFromSessionFilename extracts the trailing NN iteration from a session
// filename's base (the last hyphen segment of the stem). It returns 0 for names
// with too few segments to carry an iteration, so a malformed match cannot
// inflate NextIteration. Kept storage-local — storage must not import capture.
func iterationFromSessionFilename(name string) int {
	base := strings.TrimSuffix(filepath.Base(name), ".md")
	parts := strings.Split(base, "-")
	if len(parts) < 4 {
		return 0
	}
	n, _ := strconv.Atoi(parts[len(parts)-1])
	return n
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
