// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

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
// root, never inspecting or climbing above $HOME (the home-marker hardening).
// It returns the marker's path, or "" when there is none.
//
// It stops at $HOME in BOTH spellings, symlink-resolved and as given. The
// resolved one is what a resolved walk meets; the unresolved one is what the
// walk meets when the cwd cannot be resolved (it was deleted), so the walk
// climbs the logical path and would otherwise read $HOME's own marker.
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
	homeAsGiven := ""
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if a, err := filepath.Abs(home); err == nil {
			homeAsGiven = filepath.Clean(a)
		}
	}
	path, err := findMarkerUpward(start, homeBoundary, homeAsGiven)
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

// FindLocalOverride is the walk for the gitignored tier-1 override
// (LocalOverrideName, .vibe-palace/local.toml), bounded at $HOME EXACTLY like
// FindMarker (both spellings of home, symlink-resolved start). It returns the
// override's path, or "" when there is none (a walk that finds nothing is "",
// nil — only an unresolvable cwd is an error, matching FindMarker).
//
// 🔴 USE THIS ONLY WHEN THERE IS NO IDENTITY MARKER (a throwaway tree whose only
// vibe-palace file is an override). When an identity marker IS found, the resolver
// consults the override ONLY in the marker's own directory (ReadLocalOverrideInDir),
// never via this independent upward walk: an independent walk can climb PAST the
// marker's directory and bind a child checkout to a parent tree's throwaway vault,
// which is exactly the file-disagreement ADR-012 rule 4a forbids.
func FindLocalOverride(cwd string) (string, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve cwd: %w", err)
	}
	start := abs
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		start = filepath.Clean(resolved)
	}
	homeBoundary, _ := resolvedHome()
	homeAsGiven := ""
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if a, err := filepath.Abs(home); err == nil {
			homeAsGiven = filepath.Clean(a)
		}
	}
	path, err := findLocalOverrideUpward(start, homeBoundary, homeAsGiven)
	if err != nil {
		return "", nil
	}
	return path, nil
}

// LocateLocalOverride is FindLocalOverride followed by ReadMarker. A cwd with no
// override returns a zero Marker and no error. It reuses ReadMarker, so a
// vault_path swallowed by a table is reported in SwallowedTable exactly as for
// the identity marker; the override file's [project].name, if any, is read but
// unused (the identity marker is the one keyed by the resolver). It is the
// NO-identity-marker path only (see FindLocalOverride's warning).
func LocateLocalOverride(cwd string) (Marker, error) {
	path, err := FindLocalOverride(cwd)
	if err != nil || path == "" {
		return Marker{}, err
	}
	return ReadMarker(path)
}

// ReadLocalOverrideInDir reads dir/LocalOverrideName (dir/.vibe-palace/local.toml)
// WITHOUT walking. It is how the override is consulted once an identity marker has
// fixed the directory: co-located, so the vault and the project label come from
// the SAME tree (ADR-012 rule 4a).
//
// "No override" (zero Marker, nil) covers every way the override cannot exist:
//   - fs.ErrNotExist — no .vibe-palace directory, or no local.toml inside it;
//   - syscall.ENOTDIR — a non-directory component on the path, i.e. `.vibe-palace`
//     is a plain FILE (a stray sentinel), so it can hold no local.toml. A file that
//     cannot be a directory must not brick resolution.
//
// Only an override that EXISTS but cannot be TRUSTED (EACCES, a directory at the
// local.toml path, bad TOML) keeps its Path set and returns the error, so the
// caller refuses rather than silently fall through. The error never surfaces
// fs.ErrNotExist.
func ReadLocalOverrideInDir(dir string) (Marker, error) {
	path := filepath.Join(dir, LocalOverrideName)
	m, err := ReadMarker(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return Marker{}, nil
		}
		return m, err
	}
	return m, nil
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
