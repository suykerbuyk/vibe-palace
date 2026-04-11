// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package slug

import (
	"fmt"
	"regexp"
	"strings"
)

// slugPattern matches valid slugs: lowercase alphanumeric segments separated
// by single hyphens. No leading, trailing, or consecutive hyphens.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const maxSlugLength = 64

// Validate checks whether s is a valid slug.
// Valid slugs are non-empty, contain only lowercase alphanumeric characters
// and hyphens, and are at most 64 characters long.
func Validate(s string) error {
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

// Slugify produces a URL-safe slug from an arbitrary string.
// It lowercases, replaces separator characters with hyphens, collapses
// runs of hyphens, trims leading/trailing hyphens, and truncates at
// 60 characters on a hyphen boundary.
//
// NOTE: project/detect.go and tools/kg_tools.go have their own slug
// variants for different purposes (project detection slugs, KG entity
// IDs) and are intentionally NOT unified with this function.
func Slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == ' ' || r == '_' || r == '/' || r == '.' || r == ':':
			b.WriteByte('-')
		}
	}
	// Collapse consecutive hyphens.
	result := b.String()
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	result = strings.Trim(result, "-")
	if len(result) > 60 {
		// Truncate at hyphen boundary.
		if idx := strings.LastIndex(result[:60], "-"); idx > 0 {
			result = result[:idx]
		} else {
			result = result[:60]
		}
	}
	return result
}
