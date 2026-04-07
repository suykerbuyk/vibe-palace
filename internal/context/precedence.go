// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package context

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed templates
var defaultTemplates embed.FS

// ResourceInfo describes a named resource and which precedence level provided it.
type ResourceInfo struct {
	Name   string `json:"name"`
	Source string `json:"source"` // "project", "vault", or "embedded"
}

// Resolver resolves resources using 3-tier precedence:
// Project override > Vault template > Embedded default.
type Resolver struct {
	defaults  embed.FS
	vaultRoot string
}

// NewResolver creates a Resolver with the compiled-in defaults and vault root path.
func NewResolver(vaultRoot string) *Resolver {
	return &Resolver{
		defaults:  defaultTemplates,
		vaultRoot: vaultRoot,
	}
}

// Resolve returns (content, source, error) for a given resource identifier.
// Source is one of: "project", "vault", "embedded".
//
// Resource identifiers:
//
//	"workflow"        → workflow.md
//	"resume"          → resume.md
//	"command:{name}"  → commands/{name}.md
//	"skill:{name}"    → skills/{name}.md
func (r *Resolver) Resolve(resource, project string) (string, string, error) {
	relPath, err := resourceToPath(resource)
	if err != nil {
		return "", "", err
	}

	// Tier 1: Project override.
	if project != "" {
		projPath := filepath.Join(r.vaultRoot, "Projects", project, "agentctx", relPath)
		if data, err := os.ReadFile(projPath); err == nil {
			return r.expand(string(data), project), "project", nil
		}
	}

	// Tier 2: Vault template.
	vaultPath := filepath.Join(r.vaultRoot, "Templates", relPath)
	if data, err := os.ReadFile(vaultPath); err == nil {
		return r.expand(string(data), project), "vault", nil
	}

	// Tier 3: Embedded default.
	// embed.FS uses forward slashes regardless of OS.
	embedPath := path.Join("templates", relPath)
	if data, err := fs.ReadFile(r.defaults, embedPath); err == nil {
		return r.expand(string(data), project), "embedded", nil
	}

	return "", "", fmt.Errorf("resource %q not found at any precedence level", resource)
}

// ListResources returns deduplicated resource names for a given type
// ("command" or "skill"), merged across all three precedence tiers.
// Higher-precedence sources appear first when names collide.
func (r *Resolver) ListResources(resourceType, project string) ([]ResourceInfo, error) {
	var dir string
	switch resourceType {
	case "command":
		dir = "commands"
	case "skill":
		dir = "skills"
	default:
		return nil, fmt.Errorf("unsupported resource type for listing: %q", resourceType)
	}

	seen := make(map[string]bool)
	var result []ResourceInfo

	// Tier 1: Project overrides (highest precedence).
	if project != "" {
		projDir := filepath.Join(r.vaultRoot, "Projects", project, "agentctx", dir)
		for _, name := range listMDFiles(projDir) {
			if !seen[name] {
				seen[name] = true
				result = append(result, ResourceInfo{Name: name, Source: "project"})
			}
		}
	}

	// Tier 2: Vault templates.
	vaultDir := filepath.Join(r.vaultRoot, "Templates", dir)
	for _, name := range listMDFiles(vaultDir) {
		if !seen[name] {
			seen[name] = true
			result = append(result, ResourceInfo{Name: name, Source: "vault"})
		}
	}

	// Tier 3: Embedded defaults.
	embedDir := path.Join("templates", dir)
	if entries, err := fs.ReadDir(r.defaults, embedDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			if !seen[name] {
				seen[name] = true
				result = append(result, ResourceInfo{Name: name, Source: "embedded"})
			}
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// expand replaces template placeholders.
func (r *Resolver) expand(content, project string) string {
	s := strings.ReplaceAll(content, "{{PROJECT}}", project)
	s = strings.ReplaceAll(s, "{{DATE}}", time.Now().Format("2006-01-02"))
	return s
}

// resourceToPath converts a resource identifier to a relative file path.
func resourceToPath(resource string) (string, error) {
	switch {
	case resource == "workflow":
		return "workflow.md", nil
	case resource == "resume":
		return "resume.md", nil
	case strings.HasPrefix(resource, "command:"):
		name := strings.TrimPrefix(resource, "command:")
		if err := validateResourceName(name); err != nil {
			return "", err
		}
		return filepath.Join("commands", name+".md"), nil
	case strings.HasPrefix(resource, "skill:"):
		name := strings.TrimPrefix(resource, "skill:")
		if err := validateResourceName(name); err != nil {
			return "", err
		}
		return filepath.Join("skills", name+".md"), nil
	default:
		return "", fmt.Errorf("unknown resource type: %q", resource)
	}
}

// validateResourceName rejects names that could cause path traversal.
func validateResourceName(name string) error {
	if name == "" {
		return fmt.Errorf("resource name must not be empty")
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("resource name must not contain path separators: %q", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("resource name must not contain '..': %q", name)
	}
	return nil
}

// listMDFiles returns sorted basenames (without .md) of markdown files in a directory.
// Returns nil if the directory does not exist.
func listMDFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(names)
	return names
}
