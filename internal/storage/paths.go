// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// slugPattern matches valid slugs: lowercase alphanumeric segments separated
// by single hyphens. No leading, trailing, or consecutive hyphens.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const maxSlugLength = 64

// ValidateSlug checks that s is a valid slug for use in vault paths.
// Valid slugs are non-empty, contain only lowercase alphanumeric characters
// and hyphens, and are at most 64 characters long.
func ValidateSlug(s string) error {
	if s == "" {
		return fmt.Errorf("slug must not be empty")
	}
	if len(s) > maxSlugLength {
		return fmt.Errorf("slug %q exceeds maximum length of %d characters", s, maxSlugLength)
	}
	if strings.Contains(s, "..") {
		return fmt.Errorf("slug %q contains path traversal", s)
	}
	if strings.Contains(s, "/") || strings.Contains(s, "\\") {
		return fmt.Errorf("slug %q contains path separator", s)
	}
	if !slugPattern.MatchString(s) {
		return fmt.Errorf("slug %q must contain only lowercase alphanumeric characters and hyphens", s)
	}
	return nil
}

// validateSlugs validates all provided slugs, returning the first error found.
func validateSlugs(slugs ...string) error {
	for _, s := range slugs {
		if err := ValidateSlug(s); err != nil {
			return err
		}
	}
	return nil
}

// PalacePath returns the path to a project's palace directory:
// {vault}/palace/{project}
func (v *Vault) PalacePath(project string) (string, error) {
	if err := ValidateSlug(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "palace", project), nil
}

// DrawerDir returns the path to a drawer directory:
// {vault}/palace/{project}/drawers/{wing}/{room}
func (v *Vault) DrawerDir(project, wing, room string) (string, error) {
	if err := validateSlugs(project, wing, room); err != nil {
		return "", err
	}
	return filepath.Join(v.Root, "palace", project, "drawers", wing, room), nil
}

// DrawerFile returns the path to a drawer JSONL file:
// {vault}/palace/{project}/drawers/{wing}/{room}/drawers.jsonl
func (v *Vault) DrawerFile(project, wing, room string) (string, error) {
	dir, err := v.DrawerDir(project, wing, room)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "drawers.jsonl"), nil
}

// KGEntitiesFile returns the path to the knowledge graph entities file:
// {vault}/palace/{project}/kg/entities.jsonl
func (v *Vault) KGEntitiesFile(project string) (string, error) {
	if err := ValidateSlug(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "palace", project, "kg", "entities.jsonl"), nil
}

// KGTriplePath returns the path to a knowledge graph triple file:
// {vault}/palace/{project}/kg/triples/{subj}--{pred}--{obj}.json
//
// Subject, predicate, and object are lowercased with spaces replaced by
// underscores. They must not contain the "--" delimiter sequence.
func (v *Vault) KGTriplePath(project, subject, predicate, object string) (string, error) {
	if err := ValidateSlug(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	subj := encodeTripleComponent(subject)
	pred := encodeTripleComponent(predicate)
	obj := encodeTripleComponent(object)
	for _, c := range []struct{ name, val string }{
		{"subject", subj}, {"predicate", pred}, {"object", obj},
	} {
		if c.val == "" {
			return "", fmt.Errorf("%s must not be empty", c.name)
		}
		if strings.Contains(c.val, "--") {
			return "", fmt.Errorf("%s %q must not contain \"--\" delimiter", c.name, c.val)
		}
	}
	filename := subj + "--" + pred + "--" + obj + ".json"
	return filepath.Join(v.Root, "palace", project, "kg", "triples", filename), nil
}

// LocalDir returns the path to a project's machine-local directory:
// {vault}/palace/{project}/.local
func (v *Vault) LocalDir(project string) (string, error) {
	if err := ValidateSlug(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "palace", project, ".local"), nil
}

// VaultLocalDir returns the path to the vault-wide machine-local directory:
// {vault}/palace/.local
func (v *Vault) VaultLocalDir() string {
	return filepath.Join(v.Root, "palace", ".local")
}

// EnsureDir creates the directory tree at path if it does not exist.
// Uses os.MkdirAll with 0755 permissions. Idempotent.
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0755)
}

// KGTriplesDir returns the path to the knowledge graph triples directory:
// {vault}/palace/{project}/kg/triples
func (v *Vault) KGTriplesDir(project string) (string, error) {
	if err := ValidateSlug(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "palace", project, "kg", "triples"), nil
}

// SessionDir returns the path to a project's sessions directory:
// {vault}/Projects/{project}/sessions
func (v *Vault) SessionDir(project string) (string, error) {
	if err := ValidateSlug(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "sessions"), nil
}

// datePattern matches YYYY-MM-DD date strings.
var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// SessionFile returns the path to a session markdown file:
// {vault}/Projects/{project}/sessions/YYYY-MM-DD-NN.md
func (v *Vault) SessionFile(project, date string, iteration int) (string, error) {
	if err := ValidateSlug(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	if !datePattern.MatchString(date) {
		return "", fmt.Errorf("date %q must be in YYYY-MM-DD format", date)
	}
	if iteration < 1 {
		return "", fmt.Errorf("iteration must be >= 1, got %d", iteration)
	}
	filename := fmt.Sprintf("%s-%02d.md", date, iteration)
	return filepath.Join(v.Root, "Projects", project, "sessions", filename), nil
}

// TasksDir returns the path to a project's tasks directory:
// {vault}/Projects/{project}/agentctx/tasks
func (v *Vault) TasksDir(project string) (string, error) {
	if err := ValidateSlug(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "agentctx", "tasks"), nil
}

// TaskFile returns the path to a task markdown file:
// {vault}/Projects/{project}/agentctx/tasks/{slug}.md
func (v *Vault) TaskFile(project, slug string) (string, error) {
	if err := validateSlugs(project, slug); err != nil {
		return "", err
	}
	return filepath.Join(v.Root, "Projects", project, "agentctx", "tasks", slug+".md"), nil
}

// TaskDoneDir returns the path to a project's completed tasks directory:
// {vault}/Projects/{project}/agentctx/tasks/done
func (v *Vault) TaskDoneDir(project string) (string, error) {
	if err := ValidateSlug(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "agentctx", "tasks", "done"), nil
}

// TaskCancelledDir returns the path to a project's cancelled tasks directory:
// {vault}/Projects/{project}/agentctx/tasks/cancelled
func (v *Vault) TaskCancelledDir(project string) (string, error) {
	if err := ValidateSlug(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "agentctx", "tasks", "cancelled"), nil
}

// ProjectConfigFile returns the path to a project's config file:
// {vault}/Projects/{project}/agentctx/config.toml
func (v *Vault) ProjectConfigFile(project string) (string, error) {
	if err := ValidateSlug(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "agentctx", "config.toml"), nil
}

// encodeTripleComponent lowercases the input and replaces spaces with underscores.
func encodeTripleComponent(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), " ", "_")
}
