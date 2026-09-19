// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
)

// ProjectDirState is what a vault's Projects/<slug>/ directory holds, as
// ClassifyProjectDir judges it.
type ProjectDirState int

const (
	// ProjectAbsent: there is no Projects/<slug>/ entry.
	ProjectAbsent ProjectDirState = iota
	// ProjectPhantom: a directory that is neither initialised nor carrying
	// history — e.g. one holding only .surface, memory/, transcripts/ or empty
	// subdirectories.
	ProjectPhantom
	// ProjectScaffoldOnly: an init-scaffold marker and no content.
	ProjectScaffoldOnly
	// ProjectWithContent: real history.
	ProjectWithContent
)

// String renders the state as the word used in operator-facing messages.
func (s ProjectDirState) String() string {
	switch s {
	case ProjectAbsent:
		return "absent"
	case ProjectPhantom:
		return "phantom"
	case ProjectScaffoldOnly:
		return "scaffold-only"
	case ProjectWithContent:
		return "with-content"
	}
	return fmt.Sprintf("ProjectDirState(%d)", int(s))
}

// Initialised reports whether the directory is a project `vp init` set up or
// one that already carries history: ScaffoldOnly or WithContent.
func (s ProjectDirState) Initialised() bool {
	return s == ProjectScaffoldOnly || s == ProjectWithContent
}

// projectScaffoldMarkers are the files `vp init` lays down that mark a
// directory as initialised even before it holds any history.
//
// 🔴 config.toml IS A MARKER ONLY WHILE `vp init` STILL WRITES IT FIRST. A
// directory holding only config.toml is an interrupted init, and every caller
// treated that as initialised before this classifier existed. It leaves this
// list in the same change that retires its writer (task
// move-per-project-config-out-of-the-shared-vault, release R4).
var projectScaffoldMarkers = []string{"commands/README.md", "skills/README.md", "config.toml"}

// ClassifyProjectDir classifies <vaultRoot>/Projects/<project>/. It is the ONE
// predicate for "is this an initialised project" — shared by absorb's
// refusal, the stray-scaffolds check and config sync's project enumeration,
// which used to carry three different rules. It answers a different question
// from ListAllProjects, deliberately: MEMBERSHIP (listing, search) counts any
// real directory under Projects/, while INITIALISED (may vp write project
// history or scaffolding here?) needs a scaffold marker or real content.
//
// Rules, in this order; the first that decides wins:
//
//  1. project must pass slug.Validate, or its error is returned.
//  2. Projects/<project> is Lstat'ed. Not-exist is ProjectAbsent. An entry that
//     is not a directory is an error — INCLUDING A SYMLINK, deliberately:
//     ListAllProjects already excludes symlinked projects, and a writer must
//     not follow a link out of the vault's own tree on this predicate's say-so.
//  3. ProjectWithContent, checked in this order and stopping at the first hit:
//     resume.md, iterations.md (present and not a directory, via os.Stat), then
//     any non-directory entry at any depth under sessions/, then under tasks/.
//  4. ProjectScaffoldOnly when any of projectScaffoldMarkers is present and not
//     a directory. The markers are an OR: the first one present decides, and
//     a marker whose check errors does not hide a later one that is present.
//  5. Otherwise ProjectPhantom.
//
// 🔴 FAIL-CLOSED. A missing file or a missing sessions/ or tasks/ is simply
// absent. Any other stat or walk error is returned rather than read as "no
// content": a tree that cannot be inspected may hold history. Because rule 3
// stops at the first hit, a walk error surfaces only when nothing earlier
// already decided ProjectWithContent. The markers are consulted only after
// content is ruled out, so an error there cannot hide history: it is returned
// only when NO marker is present, never when a later marker decides
// ProjectScaffoldOnly. Every error names vault-relative paths.
//
// When err != nil the returned state is meaningless (it is the zero value,
// ProjectAbsent); callers must check err first.
func ClassifyProjectDir(vaultRoot, project string) (ProjectDirState, error) {
	if err := slug.Validate(project); err != nil {
		return ProjectAbsent, err
	}
	rel := "Projects/" + project
	dir := filepath.Join(vaultRoot, "Projects", project)

	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return ProjectAbsent, nil
		}
		return ProjectAbsent, fmt.Errorf("inspect %s: %w", rel, pathErrCause(err))
	}
	if !info.IsDir() {
		return ProjectAbsent, fmt.Errorf("%s is not a directory", rel)
	}

	for _, name := range []string{"resume.md", "iterations.md"} {
		ok, err := nonDirPresent(dir, rel, name)
		if err != nil {
			return ProjectAbsent, err
		}
		if ok {
			return ProjectWithContent, nil
		}
	}
	for _, sub := range []string{"sessions", "tasks"} {
		ok, err := treeHoldsNonDir(dir, rel, sub)
		if err != nil {
			return ProjectAbsent, err
		}
		if ok {
			return ProjectWithContent, nil
		}
	}

	var markerErr error
	for _, name := range projectScaffoldMarkers {
		ok, err := nonDirPresent(dir, rel, name)
		if err != nil {
			if markerErr == nil {
				markerErr = err
			}
			continue
		}
		if ok {
			return ProjectScaffoldOnly, nil
		}
	}
	if markerErr != nil {
		return ProjectAbsent, markerErr
	}
	return ProjectPhantom, nil
}

// nonDirPresent reports whether dir/name exists and is not a directory. It
// follows symlinks (os.Stat), as the predicate it replaced did. Not-exist is
// false; any other error is returned, naming rel/name.
func nonDirPresent(dir, rel, name string) (bool, error) {
	info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect %s/%s: %w", rel, name, pathErrCause(err))
	}
	return !info.IsDir(), nil
}

// treeHoldsNonDir reports whether dir/sub holds any non-directory entry at any
// depth. A missing dir/sub is false. Any other walk error is returned, naming
// the vault-relative path it occurred at.
func treeHoldsNonDir(dir, rel, sub string) (bool, error) {
	root := filepath.Join(dir, sub)
	found := false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			where := rel + "/" + sub
			if r, rerr := filepath.Rel(dir, path); rerr == nil {
				where = rel + "/" + filepath.ToSlash(r)
			}
			return fmt.Errorf("inspect %s: %w", where, pathErrCause(err))
		}
		if !d.IsDir() {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

// pathErrCause strips a *fs.PathError down to its cause, so an error that
// already names a vault-relative path does not also carry the host's absolute
// one.
func pathErrCause(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
