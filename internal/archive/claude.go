// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ClaudeCodeAdapterName is the manifest.adapter value for Claude Code.
const ClaudeCodeAdapterName = "claude-code"

// ClaudeCodeAdapterVersion is the adapter implementation version,
// independent of vp's release version. Bump when the adapter's
// on-disk contract with Claude Code changes (e.g., path layout moves).
const ClaudeCodeAdapterVersion = "1.0.0"

// claudeAdapter implements Adapter for Claude Code's ~/.claude/projects
// JSONL session log.
type claudeAdapter struct{}

func (claudeAdapter) Name() string    { return ClaudeCodeAdapterName }
func (claudeAdapter) Version() string { return ClaudeCodeAdapterVersion }

func (claudeAdapter) ResolveSource(opts CreateOptions) (string, func(), error) {
	if opts.SourcePath != "" {
		return opts.SourcePath, noopCleanup, nil
	}
	cwd := opts.SourceCWD
	if cwd == "" {
		c, err := os.Getwd()
		if err != nil {
			return "", noopCleanup, fmt.Errorf("resolve cwd: %w", err)
		}
		cwd = c
	}
	p, err := ClaudeSessionPath(cwd, opts.SessionID)
	if err != nil {
		return "", noopCleanup, err
	}
	if _, err := os.Stat(p); err != nil {
		return "", noopCleanup, fmt.Errorf("claude session jsonl not found: %w", err)
	}
	return p, noopCleanup, nil
}

func (claudeAdapter) Inspect(sourcePath string) (int, string, error) {
	return InspectClaudeJSONL(sourcePath)
}

func init() {
	RegisterAdapter(claudeAdapter{})
}

// ClaudeHome returns the root Claude Code state directory, honoring
// the CLAUDE_HOME env var and falling back to ~/.claude.
func ClaudeHome() (string, error) {
	if v := os.Getenv("CLAUDE_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".claude"), nil
}

// EncodeProjectDir converts an absolute working-directory path into
// the encoded segment Claude Code uses under ~/.claude/projects/.
// The encoding replaces path separators with a dash and drops the
// leading dash if the input is absolute (which is the common case).
//
// Example: "/home/alice/code/proj" -> "-home-alice-code-proj"
func EncodeProjectDir(cwd string) string {
	// Normalize separators for portability.
	s := filepath.ToSlash(cwd)
	return strings.ReplaceAll(s, "/", "-")
}

// ClaudeSessionPath returns the absolute path to the JSONL file for
// a given working directory and session ID. The file may or may not
// exist; callers should Stat it if existence matters.
func ClaudeSessionPath(cwd, sessionID string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("session id is required")
	}
	home, err := ClaudeHome()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("absolute path: %w", err)
	}
	encoded := EncodeProjectDir(abs)
	return filepath.Join(home, "projects", encoded, sessionID+".jsonl"), nil
}

// claudeLine is the minimal shape we need out of each JSONL line for
// manifest population. Extra fields are ignored.
type claudeLine struct {
	Type    string `json:"type"`
	Message struct {
		Role  string `json:"role"`
		Model string `json:"model"`
	} `json:"message"`
}

// InspectClaudeJSONL reads the JSONL file and returns:
//   - turnCount: the number of user+assistant message records
//   - model: the most recent non-empty assistant model string
//
// This is a read-only pass; the file is not modified and the entire
// stream is consumed via a buffered scanner.
func InspectClaudeJSONL(path string) (turnCount int, model string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Claude JSONL lines can be large (tool outputs, file reads).
	// Generous buffer ceiling — 16 MiB handles realistic worst cases.
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)

	for sc.Scan() {
		var l claudeLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			// Skip malformed lines — counting is best-effort. The
			// source hash in the manifest still pins the exact bytes.
			continue
		}
		if l.Type == "user" || l.Type == "assistant" {
			turnCount++
		}
		if l.Message.Model != "" {
			model = l.Message.Model
		}
	}
	if err := sc.Err(); err != nil {
		return 0, "", fmt.Errorf("scan %s: %w", path, err)
	}
	return turnCount, model, nil
}
