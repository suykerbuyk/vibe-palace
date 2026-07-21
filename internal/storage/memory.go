// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"gopkg.in/yaml.v3"
)

// ErrMemoryNameRejected classifies a memory write that failed because of the
// TARGET NAME — an unportable/illegal/traversing filename, or a case-insensitive
// collision with an existing file — rather than an I/O failure. The harvest keys
// on it (errors.Is) to skip+log a single offending native memory file instead of
// aborting the whole SessionEnd hook.
var ErrMemoryNameRejected = errors.New("storage: memory filename rejected")

// memoryIndexFile is the name of the Claude native memory index. It is an
// index of memories, not a memory itself, so List operations skip it.
const memoryIndexFile = "MEMORY.md"

// memoryTypes is the set of valid MemoryMeta.Type values.
var memoryTypes = map[string]bool{
	"user":      true,
	"feedback":  true,
	"project":   true,
	"reference": true,
}

// MemoryMeta is the lightweight metadata for a stored memory. On disk the
// type is nested under a "metadata:" key to match Claude's native auto-memory
// frontmatter format (metadata.type); name and description live at the top
// level. Rel is populated by ListMemories/ReadMemory with the file's path
// relative to the memory directory; it is not a frontmatter field.
type MemoryMeta struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	Type        string `yaml:"-" json:"type"`
	Rel         string `yaml:"-" json:"rel"`
}

// memoryFrontmatter is the on-disk frontmatter shape. It captures both a
// top-level "type:" and the native nested "metadata.type:" so parsing is
// lenient across hand-written and Claude-generated memory files. Unknown
// fields (e.g. originSessionId) are ignored by yaml.Unmarshal.
type memoryFrontmatter struct {
	Name        string `yaml:"name,omitempty"`
	Description string `yaml:"description,omitempty"`
	Type        string `yaml:"type,omitempty"`
	Metadata    struct {
		Type string `yaml:"type,omitempty"`
	} `yaml:"metadata,omitempty"`
}

// resolvedType prefers a top-level type then falls back to metadata.type.
func (fm memoryFrontmatter) resolvedType() string {
	if fm.Type != "" {
		return fm.Type
	}
	return fm.Metadata.Type
}

// memoryDiskForm is the canonical on-disk frontmatter shape written by
// WriteMemory: name + description at the top level and type nested under
// metadata, matching the Claude native auto-memory format.
type memoryDiskForm struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Metadata    struct {
		Type string `yaml:"type"`
	} `yaml:"metadata"`
}

// parseMemoryFrontmatter splits a memory file with YAML frontmatter into
// metadata and body. The frontmatter is delimited by "---" on its own lines.
// It mirrors ParseFrontmatter's fence handling but decodes into MemoryMeta.
func parseMemoryFrontmatter(data []byte) (MemoryMeta, string, error) {
	content := string(data)

	if !strings.HasPrefix(content, "---\n") {
		return MemoryMeta{}, "", fmt.Errorf("missing opening frontmatter delimiter")
	}

	// Find the closing "---" after the opening one.
	rest := content[4:] // skip opening "---\n"
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		// Try ending with "---" at EOF (no trailing newline after delimiter).
		if strings.HasSuffix(rest, "\n---") {
			idx = len(rest) - 4
		} else {
			return MemoryMeta{}, "", fmt.Errorf("missing closing frontmatter delimiter")
		}
	}

	yamlStr := rest[:idx]
	body := ""
	closingEnd := idx + 5 // len("\n---\n")
	if closingEnd <= len(rest) {
		body = rest[closingEnd:]
	}
	// Consume the single blank-line separator that renderMemory writes between
	// the frontmatter and the body (also present in Claude native files), so a
	// WriteMemory/ReadMemory round-trip preserves the body byte-for-byte.
	body = strings.TrimPrefix(body, "\n")

	var fm memoryFrontmatter
	if err := yaml.Unmarshal([]byte(yamlStr), &fm); err != nil {
		return MemoryMeta{}, "", fmt.Errorf("parse memory frontmatter YAML: %w", err)
	}
	meta := MemoryMeta{
		Name:        fm.Name,
		Description: fm.Description,
		Type:        fm.resolvedType(),
	}
	return meta, body, nil
}

// ParseMemoryFile parses raw Claude native memory file bytes (YAML frontmatter
// + body) into a MemoryMeta and body, applying the same lenient frontmatter
// rules WriteMemory/ReadMemory use (top-level type or nested metadata.type).
// Rel is NOT populated — callers that know the destination filename set it.
//
// It is the exported wrapper over parseMemoryFrontmatter so packages outside
// storage (e.g. internal/memory's harvest engine) can read native memory files
// through the one canonical parser rather than re-implementing fence handling.
func ParseMemoryFile(data []byte) (MemoryMeta, string, error) {
	return parseMemoryFrontmatter(data)
}

// renderMemory serializes a MemoryMeta and body into the canonical on-disk
// form: frontmatter with name, description, and metadata.type, then a blank
// line, then the body.
func renderMemory(meta MemoryMeta, body string) ([]byte, error) {
	var disk memoryDiskForm
	disk.Name = meta.Name
	disk.Description = meta.Description
	disk.Metadata.Type = meta.Type

	yamlBytes, err := yaml.Marshal(disk)
	if err != nil {
		return nil, fmt.Errorf("marshal memory meta: %w", err)
	}

	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(yamlBytes)
	buf.WriteString("---\n")
	buf.WriteString("\n")
	if body != "" {
		buf.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes(), nil
}

// WriteMemory writes a memory file under {vault}/Projects/{project}/memory/{rel}
// as a blind, full-file atomic overwrite. The atomic temp+rename auto-stamps
// the MCP surface, so no pre-scaffold or vault lock is needed. meta.Type must
// be one of user|feedback|project|reference and meta.Name must be non-empty.
func (v *Vault) WriteMemory(project, rel string, meta MemoryMeta, body string) error {
	if strings.TrimSpace(meta.Name) == "" {
		return fmt.Errorf("memory name must not be empty")
	}
	if !memoryTypes[meta.Type] {
		return fmt.Errorf("memory type %q must be one of user, feedback, project, reference", meta.Type)
	}

	abs, err := v.MemoryFile(project, rel)
	if err != nil {
		// A rejected NAME is classifiable so the harvest can skip+log one bad file
		// instead of failing the whole hook. MemoryFile already returns the specific
		// cause (unportable / traversal / .git / empty); tag it for the caller.
		return errors.Join(ErrMemoryNameRejected, err)
	}
	if err := v.checkMemoryCaseCollision(project, rel); err != nil {
		return err // already wraps ErrMemoryNameRejected
	}

	data, err := renderMemory(meta, body)
	if err != nil {
		return err
	}

	if err := atomicfile.Write(v.Root, abs, data); err != nil {
		return fmt.Errorf("write memory file: %w", err)
	}
	return nil
}

// checkMemoryCaseCollision refuses a write whose target filename collides
// case-insensitively with a DIFFERENTLY-cased file already in the same memory
// directory. On a case-insensitive filesystem (macOS/Windows default) "Foo.md"
// and "foo.md" are one file, so the second write silently clobbers the first —
// exactly the kind of silent cross-platform corruption this guards. An EXACT-name
// match is exempt: rewriting the same file is an update, not a collision.
func (v *Vault) checkMemoryCaseCollision(project, rel string) error {
	dir, err := v.MemoryDir(project)
	if err != nil {
		return err
	}
	cleaned := filepath.Clean(rel)
	targetDir := filepath.Join(dir, filepath.Dir(cleaned))
	targetBase := filepath.Base(cleaned)

	entries, err := os.ReadDir(targetDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no directory yet ⇒ no sibling to collide with
		}
		return fmt.Errorf("read memory dir: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == targetBase {
			continue // same file, exact case ⇒ a rewrite, allowed
		}
		if strings.EqualFold(name, targetBase) {
			return fmt.Errorf("%w: %q collides case-insensitively with existing %q "+
				"(the same file on macOS/Windows)", ErrMemoryNameRejected, targetBase, name)
		}
	}
	return nil
}

// ReadMemory reads a memory file and returns its metadata (with Rel set) and
// body.
func (v *Vault) ReadMemory(project, rel string) (MemoryMeta, string, error) {
	abs, err := v.MemoryFile(project, rel)
	if err != nil {
		return MemoryMeta{}, "", err
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return MemoryMeta{}, "", fmt.Errorf("read memory file: %w", err)
	}

	meta, body, err := parseMemoryFrontmatter(data)
	if err != nil {
		return MemoryMeta{}, "", fmt.Errorf("parse memory %s: %w", rel, err)
	}
	meta.Rel = rel
	return meta, body, nil
}

// ListMemories returns metadata for all memory files in a project's memory
// directory, recursing into subdirectories so memories written with a nested
// rel (e.g. "prefs/style.md") are surfaced. Bodies are NOT included — only
// name/description/type/rel. Rel is the slash-separated path relative to the
// memory dir, so it round-trips through ReadMemory/MemoryFile. The root
// MEMORY.md index file is skipped. Files that fail to parse are logged and
// skipped rather than failing the whole list. A missing memory directory
// yields an empty slice and nil error. limit <= 0 returns all matches;
// otherwise the result is capped. Results are sorted by Rel for determinism.
func (v *Vault) ListMemories(project string, limit int) ([]MemoryMeta, error) {
	dir, err := v.MemoryDir(project)
	if err != nil {
		return nil, err
	}

	var matches []string
	walkErr := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // missing memory dir → empty list
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		// Skip only the root-level index file, not a memory legitimately named
		// MEMORY.md inside a subdirectory.
		if strings.EqualFold(d.Name(), memoryIndexFile) && filepath.Dir(p) == dir {
			return nil
		}
		matches = append(matches, p)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk memories: %w", walkErr)
	}
	sort.Strings(matches)

	var result []MemoryMeta
	for _, m := range matches {
		rel, relErr := filepath.Rel(dir, m)
		if relErr != nil {
			slog.Warn("skip memory with unresolvable rel", "path", m, "err", relErr)
			continue
		}
		rel = filepath.ToSlash(rel)

		data, err := os.ReadFile(m)
		if err != nil {
			slog.Warn("skip unreadable memory", "path", m, "err", err)
			continue
		}
		meta, _, err := parseMemoryFrontmatter(data)
		if err != nil {
			slog.Warn("skip unparseable memory", "path", m, "err", err)
			continue
		}
		meta.Rel = rel
		result = append(result, meta)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

// DeleteMemory removes a memory file. A missing file is not an error: the
// operation is idempotent and returns nil on os.IsNotExist.
func (v *Vault) DeleteMemory(project, rel string) error {
	abs, err := v.MemoryFile(project, rel)
	if err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete memory file: %w", err)
	}
	return nil
}
