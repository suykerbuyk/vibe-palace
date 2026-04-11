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

	"github.com/suykerbuyk/vibe-palace/internal/slug"
)

//go:embed templates
var defaultTemplates embed.FS

// ResourceInfo describes a named resource and which precedence level provided it.
type ResourceInfo struct {
	Name   string `json:"name"`
	Source string `json:"source"` // "room", "wing", "project", "vault", or "embedded"
}

// Resolver resolves resources using up to 5-tier precedence:
// Room > Wing > Project > Vault > Embedded.
// When wing and room are empty, the 3-tier subset (Project > Vault > Embedded) is used.
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

// Resolve returns (content, source, error) for a given resource identifier
// using 3-tier precedence (Project > Vault > Embedded).
// Source is one of: "project", "vault", "embedded".
//
// Resource identifiers:
//
//	"workflow"        → workflow.md
//	"resume"          → resume.md
//	"command:{name}"  → commands/{name}.md
//	"skill:{name}"    → skills/{name}.md
func (r *Resolver) Resolve(resource, project string) (string, string, error) {
	return r.ResolveScoped(resource, project, "", "")
}

// ResolveScoped returns (content, source, error) using 5-tier precedence:
// Room > Wing > Project > Vault > Embedded.
// When wing and room are empty, only the 3-tier subset is checked.
func (r *Resolver) ResolveScoped(resource, project, wing, room string) (string, string, error) {
	if err := validateScope(wing, room); err != nil {
		return "", "", err
	}

	resType, name, dir, err := parseResource(resource)
	if err != nil {
		return "", "", err
	}

	// For non-command/skill resources (workflow, resume), fall through to path-based resolution.
	if dir == "" {
		return r.resolveByPath(resType+".md", project, wing, room)
	}

	filename := name + ".md"

	// Tier 1 — Room (only if both wing and room are set).
	if wing != "" && room != "" && project != "" {
		roomPath := filepath.Join(r.vaultRoot, "Projects", project, dir, wing, room, filename)
		if data, err := os.ReadFile(roomPath); err == nil {
			return r.expandScoped(string(data), project, wing, room), "room", nil
		}
	}

	// Tier 2 — Wing (only if wing is set).
	if wing != "" && project != "" {
		wingPath := filepath.Join(r.vaultRoot, "Projects", project, dir, wing, ".wing", filename)
		if data, err := os.ReadFile(wingPath); err == nil {
			return r.expandScoped(string(data), project, wing, room), "wing", nil
		}
	}

	// Tier 3 — Project.
	if project != "" {
		projPath := filepath.Join(r.vaultRoot, "Projects", project, dir, filename)
		if data, err := os.ReadFile(projPath); err == nil {
			return r.expandScoped(string(data), project, wing, room), "project", nil
		}
	}

	// Tier 4 — Vault template.
	vaultPath := filepath.Join(r.vaultRoot, "Templates", dir, filename)
	if data, err := os.ReadFile(vaultPath); err == nil {
		return r.expandScoped(string(data), project, wing, room), "vault", nil
	}

	// Tier 5 — Embedded default.
	embedPath := path.Join("templates", dir, filename)
	if data, err := fs.ReadFile(r.defaults, embedPath); err == nil {
		return r.expandScoped(string(data), project, wing, room), "embedded", nil
	}

	return "", "", fmt.Errorf("resource %q not found at any precedence level", resource)
}

// resolveByPath handles non-command/skill resources (workflow, resume) which
// don't participate in wing/room scoping.
func (r *Resolver) resolveByPath(relPath, project, wing, room string) (string, string, error) {
	if project != "" {
		projPath := filepath.Join(r.vaultRoot, "Projects", project, relPath)
		if data, err := os.ReadFile(projPath); err == nil {
			return r.expandScoped(string(data), project, wing, room), "project", nil
		}
	}
	vaultPath := filepath.Join(r.vaultRoot, "Templates", relPath)
	if data, err := os.ReadFile(vaultPath); err == nil {
		return r.expandScoped(string(data), project, wing, room), "vault", nil
	}
	embedPath := path.Join("templates", relPath)
	if data, err := fs.ReadFile(r.defaults, embedPath); err == nil {
		return r.expandScoped(string(data), project, wing, room), "embedded", nil
	}
	return "", "", fmt.Errorf("resource %q not found at any precedence level", relPath)
}

// ListResources returns deduplicated resource names using 3-tier precedence.
func (r *Resolver) ListResources(resourceType, project string) ([]ResourceInfo, error) {
	return r.ListResourcesScoped(resourceType, project, "", "")
}

// ListResourcesScoped returns deduplicated resource names merged across up to
// 5 precedence tiers: Room > Wing > Project > Vault > Embedded.
// Higher-precedence sources shadow lower when names collide.
func (r *Resolver) ListResourcesScoped(resourceType, project, wing, room string) ([]ResourceInfo, error) {
	if err := validateScope(wing, room); err != nil {
		return nil, err
	}

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

	addTier := func(names []string, source string) {
		for _, name := range names {
			if !seen[name] {
				seen[name] = true
				result = append(result, ResourceInfo{Name: name, Source: source})
			}
		}
	}

	// Tier 1 — Room.
	if wing != "" && room != "" && project != "" {
		roomDir := filepath.Join(r.vaultRoot, "Projects", project, dir, wing, room)
		addTier(listMDFiles(roomDir), "room")
	}

	// Tier 2 — Wing.
	if wing != "" && project != "" {
		wingDir := filepath.Join(r.vaultRoot, "Projects", project, dir, wing, ".wing")
		addTier(listMDFiles(wingDir), "wing")
	}

	// Tier 3 — Project.
	if project != "" {
		projDir := filepath.Join(r.vaultRoot, "Projects", project, dir)
		addTier(listMDFiles(projDir), "project")
	}

	// Tier 4 — Vault templates.
	vaultDir := filepath.Join(r.vaultRoot, "Templates", dir)
	addTier(listMDFiles(vaultDir), "vault")

	// Tier 5 — Embedded defaults.
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

// expand replaces template placeholders (3-tier compat).
func (r *Resolver) expand(content, project string) string {
	return r.expandScoped(content, project, "", "")
}

// expandScoped replaces template placeholders including wing/room.
func (r *Resolver) expandScoped(content, project, wing, room string) string {
	s := strings.ReplaceAll(content, "{{PROJECT}}", project)
	s = strings.ReplaceAll(s, "{{WING}}", wing)
	s = strings.ReplaceAll(s, "{{ROOM}}", room)
	s = strings.ReplaceAll(s, "{{DATE}}", time.Now().Format("2006-01-02"))
	return s
}

// parseResource extracts resource type, name, and directory from a resource identifier.
// For "command:foo" → ("command", "foo", "commands", nil).
// For "workflow" → ("workflow", "", "", nil) — dir is empty for non-scoped resources.
func parseResource(resource string) (resType, name, dir string, err error) {
	switch {
	case resource == "workflow":
		return "workflow", "", "", nil
	case resource == "resume":
		return "resume", "", "", nil
	case strings.HasPrefix(resource, "command:"):
		name = strings.TrimPrefix(resource, "command:")
		if err := validateResourceName(name); err != nil {
			return "", "", "", err
		}
		return "command", name, "commands", nil
	case strings.HasPrefix(resource, "skill:"):
		name = strings.TrimPrefix(resource, "skill:")
		if err := validateResourceName(name); err != nil {
			return "", "", "", err
		}
		return "skill", name, "skills", nil
	default:
		return "", "", "", fmt.Errorf("unknown resource type: %q", resource)
	}
}

// resourceToPath converts a resource identifier to a relative file path.
func resourceToPath(resource string) (string, error) {
	resType, name, dir, err := parseResource(resource)
	if err != nil {
		return "", err
	}
	if dir == "" {
		return resType + ".md", nil
	}
	return filepath.Join(dir, name+".md"), nil
}

// validateScope checks that wing/room parameters are valid.
func validateScope(wing, room string) error {
	if room != "" && wing == "" {
		return fmt.Errorf("room %q requires a wing to be specified", room)
	}
	if wing != "" {
		if err := slug.Validate(wing); err != nil {
			return fmt.Errorf("invalid wing: %w", err)
		}
	}
	if room != "" {
		if err := slug.Validate(room); err != nil {
			return fmt.Errorf("invalid room: %w", err)
		}
	}
	return nil
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
