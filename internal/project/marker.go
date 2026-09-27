// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	slugpkg "github.com/suykerbuyk/vibe-palace/internal/slug"
)

// Marker is ONE read of a checkout's .vibe-palace.toml: the file the walk
// found, its top-level vault_path, and its [project].name.
//
// 🔴 THERE IS ONE WALK AND ONE READER, AND BOTH CALLERS USE THEM. The vault
// resolver (storage.ResolveVaultBinding) keys the host's [project_vaults]
// binding on Name, and project detection (DetectProjectHighConfidence) labels
// the session with it. If the two found different files, or read the same
// name differently, a session could be written into one project's vault under
// another project's label (ADR-012).
type Marker struct {
	// Path is the marker file, symlink-resolved; "" when the walk found none.
	Path string
	// VaultPath is the top-level vault_path, "" when unset.
	VaultPath string
	// Name is [project].name with surrounding whitespace trimmed; "" when the
	// key is absent, the [project] table is not a table, or name is not a
	// string. NameErr says whether a non-empty Name is a valid slug.
	Name    string
	NameErr error
	// SwallowedTable names the table that captured a vault_path key written
	// below a [table] header (so it overrides nothing); "" when none did. The
	// caller decides what that means — the resolver refuses it.
	SwallowedTable string
}

// FindMarker is the one marker walk: from the symlink-resolved cwd toward the
// root, never inspecting or climbing above the symlink-resolved $HOME (the
// home-marker hardening). It returns the marker's path, or "" when there is
// none.
func FindMarker(cwd string) (string, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve cwd: %w", err)
	}
	start := abs
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		start = filepath.Clean(resolved)
	}
	homeBoundary, _ := resolvedHome()
	path, err := findMarkerUpward(start, homeBoundary)
	if err != nil {
		return "", nil
	}
	return path, nil
}

// LocateMarker is FindMarker followed by ReadMarker. A cwd with no marker
// returns a zero Marker and no error.
func LocateMarker(cwd string) (Marker, error) {
	path, err := FindMarker(cwd)
	if err != nil || path == "" {
		return Marker{}, err
	}
	return ReadMarker(path)
}

// ReadMarker reads one marker file ONCE.
//
// An error means the file could not be read or does not parse, or its
// vault_path is not a string. The returned Marker still carries Path, so a
// caller can name the file it could not read.
//
// The name is read TOLERANTLY from a generic decode of the same bytes: an
// absent, non-table [project] or a non-string name is "no name", never an
// error. That keeps detection's historical leniency (it never failed on what
// [project] held beyond the name) in the one reader both callers share.
func ReadMarker(path string) (Marker, error) {
	m := Marker{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		return m, fmt.Errorf("read config: %w", err)
	}
	var top struct {
		VaultPath string `toml:"vault_path"`
	}
	md, err := toml.Decode(string(data), &top)
	if err != nil {
		return m, fmt.Errorf("parse config: %w", err)
	}
	m.VaultPath = top.VaultPath
	if m.VaultPath == "" {
		m.SwallowedTable = swallowedVaultPathTable(md)
	}
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err == nil {
		if p, ok := raw["project"].(map[string]any); ok {
			if name, ok := p["name"].(string); ok {
				m.Name = strings.TrimSpace(name)
			}
		}
	}
	if m.Name != "" {
		m.NameErr = slugpkg.Validate(m.Name)
	}
	return m, nil
}

// swallowedVaultPathTable reports the table that captured a vault_path key,
// or "" when none did. TOML scopes every key after a [table] header into that
// table, so vault_path written below [project] binds project.vault_path and
// leaves the top-level key unset while the decode succeeds (iteration 210).
// MetaData.Keys reports every key the document defined, so the case is
// directly observable; the decode alone cannot see it.
func swallowedVaultPathTable(md toml.MetaData) string {
	for _, key := range md.Keys() {
		if len(key) < 2 || key[len(key)-1] != "vault_path" {
			continue
		}
		return strings.Join(key[:len(key)-1], ".")
	}
	return ""
}
