// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package project

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	slugpkg "github.com/suykerbuyk/vibe-palace/internal/slug"
)

// ConfigFileName is the name of the per-project configuration file.
const ConfigFileName = ".vibe-palace.toml"

// manifestFiles are ecosystem manifest files that mark a directory as a
// project even without a `.git` directory. Order is stable/alphabetical;
// detection picks the first one found.
var manifestFiles = []string{
	"Cargo.toml",
	"go.mod",
	"package.json",
	"pom.xml",
	"pyproject.toml",
}

// ProjectSignal identifies which evidence marked a directory as a project.
// Empty string means "no signal"; forceSkip callers should test that first.
type ProjectSignal string

const (
	SignalNone          ProjectSignal = ""
	SignalVibeConfig    ProjectSignal = ".vibe-palace.toml"
	SignalGit           ProjectSignal = ".git"
	SignalCargoToml     ProjectSignal = "Cargo.toml"
	SignalGoMod         ProjectSignal = "go.mod"
	SignalPackageJSON   ProjectSignal = "package.json"
	SignalPomXML        ProjectSignal = "pom.xml"
	SignalPyprojectToml ProjectSignal = "pyproject.toml"
)

// DetectSignal returns the first project signal found in dir (non-recursive
// for manifest files; walking upward for .vibe-palace.toml). If dir resolves
// to $HOME or the filesystem root, returns SignalNone regardless — those
// directories are force-skipped because running project-init there would
// pollute unrelated state.
//
// Precedence (first match wins):
//  1. .vibe-palace.toml (cwd or any parent — re-init case)
//  2. .git/ in cwd
//  3. Manifest files in cwd, in the order given by manifestFiles
func DetectSignal(dir string) ProjectSignal {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return SignalNone
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		resolved = abs
	}
	resolved = filepath.Clean(resolved)

	if isForceSkipDir(resolved) {
		return SignalNone
	}

	homeBoundary, _ := resolvedHome()
	if _, err := findMarkerUpward(resolved, homeBoundary); err == nil {
		return SignalVibeConfig
	}

	if _, err := os.Stat(filepath.Join(resolved, ".git")); err == nil {
		return SignalGit
	}

	for _, name := range manifestFiles {
		if _, err := os.Stat(filepath.Join(resolved, name)); err == nil {
			return ProjectSignal(name)
		}
	}

	return SignalNone
}

// resolvedHome returns the symlink-resolved, cleaned absolute path of the
// user's home directory, and whether it could be determined. It is both the
// force-skip target (isForceSkipDir) and the boundary at which the
// .vibe-palace.toml marker walk stops (findMarkerUpward): a marker located
// exactly at $HOME is ignored and the walk never climbs above it. Resolving
// symlinks here keeps the boundary comparison exact even when $HOME itself is
// a symlink (as it was for the dotfile-manager case that motivated this).
func resolvedHome() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		resolved = home
	}
	return filepath.Clean(resolved), true
}

// isForceSkipDir reports whether dir is a directory where project-init
// must never run regardless of signals present (user's $HOME or fs root).
func isForceSkipDir(dir string) bool {
	if dir == "/" {
		return true
	}
	if home, ok := resolvedHome(); ok && home == dir {
		return true
	}
	return false
}

// ProjectConfig holds project identity parsed from a .vibe-palace.toml file.
type ProjectConfig struct {
	Name   string   `toml:"name"`
	Domain string   `toml:"domain"`
	Tags   []string `toml:"tags"`
}

// ProjectFile is the top-level TOML structure for .vibe-palace.toml.
// It carries the [project] block and an optional top-level vault_path
// override. Other top-level keys are ignored.
type ProjectFile struct {
	Project   ProjectConfig `toml:"project"`
	VaultPath string        `toml:"vault_path"`
}

// DetectProject determines the project name for the given working directory.
// Detection priority:
//  1. .vibe-palace.toml in cwd or any parent directory
//  2. Git remote URL heuristics (origin remote)
//  3. Directory basename of cwd
//
// The returned name is validated as a slug.
func DetectProject(cwd string) (string, error) {
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve cwd: %w", err)
	}

	// Strategy 1: config file walk, bounded at $HOME so a stray
	// ~/.vibe-palace.toml does not name every directory under the home tree
	// (see the home-marker hardening). The walk is symlink-resolved to match
	// the resolved home boundary; the git/basename strategies below keep using
	// the unresolved cwd, preserving their existing behavior.
	markerStart := cwd
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		markerStart = filepath.Clean(resolved)
	}
	homeBoundary, _ := resolvedHome()
	if configPath, err := findMarkerUpward(markerStart, homeBoundary); err == nil {
		cfg, err := ParseProjectConfig(configPath)
		if err == nil && cfg.Name != "" {
			if err := slugpkg.Validate(cfg.Name); err != nil {
				return "", fmt.Errorf("project name from config: %w", err)
			}
			return cfg.Name, nil
		}
		// Config exists but has no name — fall through to git/basename.
	}

	// Strategy 2: git remote heuristics.
	if name, err := gitRemoteName(cwd); err == nil {
		slug := slugify(name)
		if slug != "" && slugpkg.Validate(slug) == nil {
			return slug, nil
		}
	}

	// Strategy 3: directory basename.
	slug := slugify(filepath.Base(cwd))
	if slug == "" {
		return "", fmt.Errorf("cannot detect project name from directory %q", cwd)
	}
	if err := slugpkg.Validate(slug); err != nil {
		return "", fmt.Errorf("directory basename: %w", err)
	}
	return slug, nil
}

// ParseProjectConfig parses a .vibe-palace.toml file and returns the project
// configuration. Returns an error if the file cannot be read or is invalid TOML.
// Unknown top-level keys are tolerated.
func ParseProjectConfig(path string) (ProjectConfig, error) {
	pf, err := ParseProjectFile(path)
	if err != nil {
		return ProjectConfig{}, err
	}
	return pf.Project, nil
}

// ParseProjectFile parses a .vibe-palace.toml file and returns the full
// file contents, including any top-level vault_path override. Unknown
// top-level keys are tolerated.
func ParseProjectFile(path string) (ProjectFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProjectFile{}, fmt.Errorf("read config: %w", err)
	}

	var pf ProjectFile
	if err := toml.Unmarshal(data, &pf); err != nil {
		return ProjectFile{}, fmt.Errorf("parse config: %w", err)
	}
	return pf, nil
}

// findMarkerUpward walks from dir toward the filesystem root looking for a
// .vibe-palace.toml marker, but STOPS at the home boundary: it never inspects
// or climbs above $HOME. This honors the marker-file template's promise that a
// file at $HOME/.vibe-palace.toml is ignored — so a single stray marker in the
// home directory (e.g. linked there by a dotfile manager) does not enroll the
// entire home tree into auto-capture.
//
// dir must already be symlink-resolved by the caller so the boundary comparison
// is exact. A zero homeBoundary disables the stop (used only when the home
// directory cannot be determined), degrading to findFileUpward's walk-to-root.
// This bound applies to the marker walk ONLY; the .git walk in gitRemoteName
// still climbs to the filesystem root, since a project legitimately lives above
// $HOME on some hosts.
func findMarkerUpward(dir, homeBoundary string) (string, error) {
	for {
		if homeBoundary != "" && dir == homeBoundary {
			return "", fmt.Errorf("%s not found (stopped at home boundary)", ConfigFileName)
		}
		candidate := filepath.Join(dir, ConfigFileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root.
			return "", fmt.Errorf("%s not found", ConfigFileName)
		}
		dir = parent
	}
}

// findFileUpward walks from dir toward the filesystem root looking for a file
// with the given name. Returns the full path if found, or an error.
func findFileUpward(dir, filename string) (string, error) {
	for {
		candidate := filepath.Join(dir, filename)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root.
			return "", fmt.Errorf("%s not found", filename)
		}
		dir = parent
	}
}

// gitRemoteName extracts the repository name from the origin remote URL of
// the git repository containing dir. It walks upward to find .git first.
func gitRemoteName(dir string) (string, error) {
	// Find the git root by walking upward for .git.
	gitDir, err := findFileUpward(dir, ".git")
	if err != nil {
		return "", fmt.Errorf("not a git repository")
	}
	repoRoot := filepath.Dir(gitDir)

	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git remote: %w", err)
	}
	return extractRepoName(strings.TrimSpace(string(out))), nil
}

// extractRepoName extracts the repository name from a git remote URL.
// Handles SSH (git@host:user/repo.git) and HTTPS (https://host/user/repo.git).
func extractRepoName(url string) string {
	// Strip trailing .git suffix.
	url = strings.TrimSuffix(url, ".git")

	// SSH format: git@github.com:user/repo
	if idx := strings.LastIndex(url, ":"); idx != -1 && !strings.Contains(url, "://") {
		url = url[idx+1:]
	}

	// Take the last path segment.
	if idx := strings.LastIndex(url, "/"); idx != -1 {
		return url[idx+1:]
	}
	return url
}

// nonAlphanumeric matches sequences of characters that are not lowercase
// alphanumeric.
var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// slugify converts a name to a valid slug: lowercase, non-alphanumeric runs
// replaced with single hyphens, leading/trailing hyphens trimmed.
func slugify(name string) string {
	s := strings.ToLower(name)
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}
