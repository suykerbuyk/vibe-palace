// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package shims emits one Claude Code slash-command shim per vibe-palace
// command into the project's .claude/commands/ directory. Each shim is a
// minimal markdown file that delegates to the MCP command tool named by
// agentfile.CommandToolName, so typing /vpc-<name> in the REPL surfaces
// the full vibe-palace command set through Claude Code's fuzzy-match menu
// without duplicating command bodies.
package shims

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
)

// shimVersion bumps when the rendered shim schema changes in a way we want to
// force-refresh existing files. Changing the template text does not require a
// bump because the content-hash catches body drift; bump only for structural
// changes that need a hash miss on every prior shim.
const shimVersion = 1

// FilePrefix is the filename prefix emitted into .claude/commands/. One
// constant drives both the commands.Alias trigger and the shim filename so
// the two surfaces cannot drift.
const FilePrefix = "vpc-"

// ShimDir is the project-relative directory shims land in.
const ShimDir = ".claude/commands"

// GrokCommandsPluginDir is the directory inside a project-scoped Grok
// plugin where we emit the same vpc-*.md slash-command shims. This gives
// Grok Build first-class native /vpc-<name> slash commands (discovered from
// .grok/plugins/) with the identical naming convention used for Claude.
const GrokCommandsPluginDir = ".grok/plugins/vibe-palace/commands"

// shim marker delimiters. The opening marker carries a 7-hex content hash
// derived from the render inputs and the template version; the closing
// marker is static so the regex can find the marker pair regardless of
// sha contents.
const (
	shimOpenFmt    = "<!-- vibe-palace:shim v=%d sha=%s -->"
	shimCloseDelim = "<!-- vibe-palace:shim-end -->"
)

// markerRegexp locates the opening shim marker and captures its sha.
// Anchored to the literal "vibe-palace:shim " (with trailing space) so it
// never matches the "vibe-palace:shim-end" close marker.
var markerRegexp = regexp.MustCompile(`<!-- vibe-palace:shim v=(\d+) sha=([0-9a-f]+) -->`)

// Filename returns the shim filename (without directory) for the given
// command name — e.g. Filename("restart") == "vpc-restart.md".
func Filename(name string) string { return FilePrefix + name + ".md" }

// contentHash returns the first 7 hex chars of sha256 over the render
// inputs plus the template version. Keying on inputs (rather than on the
// rendered bytes) avoids the self-referential problem where the sha token
// would need to be in the hashed content.
//
// project and argHint are appended to the hashed input ONLY when non-empty,
// so an existing global shim (project=="" && argHint=="") keeps its prior
// hash byte-for-byte — no mass rewrite on upgrade. A project-scoped or
// hinted shim folds those values in so a slug/hint change trips drift
// detection.
func contentHash(name, brief, project, argHint string) string {
	h := sha256.New()
	fmt.Fprintf(h, "v=%d\x00name=%s\x00brief=%s\x00tool=%s",
		shimVersion, name, brief, agentfile.CommandToolName)
	if project != "" {
		fmt.Fprintf(h, "\x00project=%s", project)
	}
	if argHint != "" {
		fmt.Fprintf(h, "\x00arghint=%s", argHint)
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)[:7]
}

// ExpectedSha returns the sha a freshly rendered shim would carry for the
// given name, brief, project, and argHint. Callers comparing an on-disk
// shim's extracted sha to the current expected value use this.
func ExpectedSha(name, brief, project, argHint string) string {
	return contentHash(name, brief, project, argHint)
}

// Render returns the full shim file body for the given command. project is the
// owning project slug for project-scoped commands (empty for global ones); when
// set, the body passes project="<slug>" so the MCP server resolves the project
// tier without cwd context. argHint, when non-empty, becomes the frontmatter
// argument-hint (else a generic fallback). Output uses LF line endings and is
// deterministic for a given (name, brief, project, argHint, template version)
// tuple.
func Render(name, brief, project, argHint string) string {
	sha := contentHash(name, brief, project, argHint)
	desc := "Vibe-palace command"
	if brief != "" {
		desc += " — " + sanitizeFrontmatter(brief)
	}
	hint := "[args forwarded to the vibe-palace command]"
	if argHint != "" {
		hint = sanitizeFrontmatter(argHint)
	}
	alias := commands.Alias(name)
	openMarker := fmt.Sprintf(shimOpenFmt, shimVersion, sha)
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("description: ")
	sb.WriteString(desc)
	sb.WriteString("\n")
	sb.WriteString("argument-hint: ")
	sb.WriteString(hint)
	sb.WriteString("\n")
	sb.WriteString("---\n\n")
	sb.WriteString(openMarker)
	sb.WriteString("\n")
	sb.WriteString("Call `")
	sb.WriteString(agentfile.CommandToolName)
	sb.WriteString("` with `name=\"")
	sb.WriteString(name)
	sb.WriteString("\"`")
	if project != "" {
		sb.WriteString(" and `project=\"")
		sb.WriteString(project)
		sb.WriteString("\"`, then follow the returned instructions verbatim. Forward any\n")
	} else {
		sb.WriteString(" and follow the returned instructions verbatim. Forward any\n")
	}
	sb.WriteString("arguments the user supplied after `/")
	sb.WriteString(alias)
	sb.WriteString("` as `$ARGUMENTS`.\n")
	sb.WriteString(shimCloseDelim)
	sb.WriteString("\n")
	return sb.String()
}

// sanitizeFrontmatter strips characters that would break single-line YAML
// frontmatter (CR, LF). Briefs come from user-authored command files so we
// cannot assume they are safe; we collapse whitespace rather than quoting
// because Claude Code's parser prefers bare scalars.
func sanitizeFrontmatter(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// ScanResult describes what ScanShim found at a path.
type ScanResult struct {
	// Exists is true when the file exists (even if we do not own it).
	Exists bool
	// HasMarker is true when the opening shim marker is present in the file.
	// A file that exists without the marker is a user-owned "custom" file.
	HasMarker bool
	// Sha is the content hash carried by the opening marker, empty when
	// HasMarker is false or the marker is malformed.
	Sha string
	// Version is the template version declared in the marker, 0 when
	// HasMarker is false.
	Version int
}

// ScanShim reads path and classifies it. Returns a zero ScanResult with no
// error when the file does not exist. IO errors other than NotExist are
// surfaced so callers can distinguish permission issues from missing files.
func ScanShim(path string) (ScanResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ScanResult{}, nil
		}
		return ScanResult{}, err
	}
	res := ScanResult{Exists: true}
	m := markerRegexp.FindSubmatch(data)
	if m == nil {
		return res, nil
	}
	res.HasMarker = true
	res.Sha = string(m[2])
	// Version parse errors fall through as 0 — we treat a malformed marker
	// as "drift" so the updater rewrites the file.
	fmt.Sscanf(string(m[1]), "%d", &res.Version)
	return res, nil
}
