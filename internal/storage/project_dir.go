// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import "github.com/suykerbuyk/vibe-palace/internal/projectdir"

// The project-directory predicate lives in internal/projectdir, a leaf the
// vault's write primitives can import; these forwards keep storage's callers
// unchanged.

// ProjectDirState is projectdir.ProjectDirState.
type ProjectDirState = projectdir.ProjectDirState

// The ProjectDirState values; see projectdir.
const (
	ProjectAbsent       = projectdir.ProjectAbsent
	ProjectPhantom      = projectdir.ProjectPhantom
	ProjectScaffoldOnly = projectdir.ProjectScaffoldOnly
	ProjectWithContent  = projectdir.ProjectWithContent
)

// projectScaffoldMarkers are projectdir.ScaffoldMarkers.
var projectScaffoldMarkers = projectdir.ScaffoldMarkers

// ClassifyProjectDir is projectdir.ClassifyProjectDir.
func ClassifyProjectDir(vaultRoot, project string) (ProjectDirState, error) {
	return projectdir.ClassifyProjectDir(vaultRoot, project)
}
