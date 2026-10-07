// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// ProjectExists reports whether project is a member of the vault, by
// storage.Vault.ProjectExists: the union of Projects/<slug>/ directories and
// palace/<slug>/ stores that cross-project search already enumerates by. Both
// MCP vp_search and CLI `vp search` call this — or, for search, get it called
// for them by Engine.Search — before any embedder work runs, so an unknown
// project is refused identically on both surfaces instead of costing a model
// load (or a cold host's ONNX download) merely to answer "no results".
//
// A notes-only project (Projects/<slug>/ with no palace/ store) is in; a
// .local-only palace husk is out; so is a symlinked Projects/<slug> OR a
// symlinked palace/<slug> — both trees are enumerated through the same
// listProjectDirs, whose os.ReadDir entries report IsDir() false for a
// symlink regardless of what it points at, so neither tree ever follows one
// into a project name.
//
// It returns an error only when ListAllProjects itself could not look — an
// existing-but-unreadable tree. A caller must not collapse that into "the
// project doesn't exist": "I could not look" is not "absent", and conflating
// the two would turn a real I/O failure into a silent, wrong "unknown project"
// refusal.
func ProjectExists(vault *storage.Vault, project string) (bool, error) {
	return vault.ProjectExists(project)
}

// UnknownProjectError reports that Project names no member of ListAllProjects.
// Answering a search of an absent project with empty results is the
// silent-skip-as-success shape this project deletes on sight — searching a
// project that does not exist is always an error, whichever surface asked and
// however the project name was resolved, never zero hits dressed as success.
type UnknownProjectError struct {
	Project string
}

func (e *UnknownProjectError) Error() string {
	return fmt.Sprintf("unknown project %q: no such project in the vault", e.Project)
}

// NothingIndexableError reports that Project exists but has nothing any search
// tier could ever index — no session notes, no iterations, no tracked
// transcript archives, no host-local chunks, and (while the vault carries no
// migration marker) no tracked drawers: (*Engine).TrulyEmpty is true. A search
// of such a project is an error that names `vp index rebuild`, never zero hits
// dressed as success — the same silent-skip-as-success shape UnknownProjectError
// refuses (ADR-014 decision 8). A project with tracked archives and nothing else
// is NOT truly empty: it answers [] and is not skipped by cross-project search.
//
// An empty Project means a cross-project search found no project in the vault
// with anything indexable; a non-empty Project names the single project asked
// for.
type NothingIndexableError struct {
	Project string
}

func (e *NothingIndexableError) Error() string {
	if e.Project == "" {
		return "no project in the vault has anything to search yet; run `vp index rebuild <project>` once a project's content is ingested"
	}
	return fmt.Sprintf("project %q has nothing to search yet; run `vp index rebuild %s`", e.Project, e.Project)
}
