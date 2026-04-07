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
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// ConfigFileName is the name of the per-project configuration file.
const ConfigFileName = ".vibe-palace.toml"

// ProjectConfig holds project identity parsed from a .vibe-palace.toml file.
type ProjectConfig struct {
	Name   string   `toml:"name"`
	Domain string   `toml:"domain"`
	Tags   []string `toml:"tags"`
}

// projectFile is the top-level TOML structure for .vibe-palace.toml.
type projectFile struct {
	Project ProjectConfig `toml:"project"`
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

	// Strategy 1: config file walk.
	if configPath, err := findFileUpward(cwd, ConfigFileName); err == nil {
		cfg, err := ParseProjectConfig(configPath)
		if err == nil && cfg.Name != "" {
			if err := storage.ValidateSlug(cfg.Name); err != nil {
				return "", fmt.Errorf("project name from config: %w", err)
			}
			return cfg.Name, nil
		}
		// Config exists but has no name — fall through to git/basename.
	}

	// Strategy 2: git remote heuristics.
	if name, err := gitRemoteName(cwd); err == nil {
		slug := slugify(name)
		if slug != "" && storage.ValidateSlug(slug) == nil {
			return slug, nil
		}
	}

	// Strategy 3: directory basename.
	slug := slugify(filepath.Base(cwd))
	if slug == "" {
		return "", fmt.Errorf("cannot detect project name from directory %q", cwd)
	}
	if err := storage.ValidateSlug(slug); err != nil {
		return "", fmt.Errorf("directory basename: %w", err)
	}
	return slug, nil
}

// ParseProjectConfig parses a .vibe-palace.toml file and returns the project
// configuration. Returns an error if the file cannot be read or is invalid TOML.
func ParseProjectConfig(path string) (ProjectConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProjectConfig{}, fmt.Errorf("read config: %w", err)
	}

	var pf projectFile
	if err := toml.Unmarshal(data, &pf); err != nil {
		return ProjectConfig{}, fmt.Errorf("parse config: %w", err)
	}
	return pf.Project, nil
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
