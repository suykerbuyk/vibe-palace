// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
)

// A vault records a project in TWO independent trees, and nothing keeps them in
// step:
//
//   - palace/<slug>/   — the drawer + knowledge-graph store (what capture indexes)
//   - Projects/<slug>/ — sessions, tasks, resume (the project's history)
//
// A project can exist in either without the other, and in the live vault many
// do: a project whose sessions were captured before it was ever drawer-indexed
// has no palace/ dir, and a palace/ dir survives a project whose history was
// never written. Neither tree is a superset of the other, so NEITHER ALONE
// ENUMERATES THE VAULT.
//
// ListProjects (drawers.go) reads only palace/. That is correct for search,
// which indexes drawers — but any caller asking "what is in this vault?" and
// reaching for it gets a subset and cannot tell. That is how a vault-global
// audit reports a clean bill of health for a corpus it never looked at.

// ProjectPresence is one project and the trees it actually appears in. The
// asymmetry is not an error to be resolved in favour of one side — it is a
// finding: a project in one tree and not the other is real vault drift, and the
// union enumerator is the only thing positioned to notice it.
type ProjectPresence struct {
	Slug       string
	InPalace   bool // palace/<slug>/ exists — the drawer + KG store
	InProjects bool // Projects/<slug>/ exists — sessions, tasks, resume
}

// Complete reports whether the project appears in BOTH trees. A false result is
// the drift; the caller decides whether to report or repair it.
func (p ProjectPresence) Complete() bool { return p.InPalace && p.InProjects }

// ListAllProjects enumerates the UNION of palace/ and Projects/, sorted by slug,
// recording which trees each project appears in.
//
// This is the enumerator a vault-global caller wants. A missing tree is not an
// error — a vault with no palace/ at all is simply one where nothing has been
// indexed yet — so an absent directory contributes nothing rather than failing
// the sweep. An unreadable one that EXISTS is a real error and is reported: "I
// could not look" is not "there was nothing there."
func (v *Vault) ListAllProjects() ([]ProjectPresence, error) {
	palace, err := listProjectDirs(filepath.Join(v.Root, "palace"))
	if err != nil {
		return nil, fmt.Errorf("list palace projects: %w", err)
	}
	projects, err := listProjectDirs(filepath.Join(v.Root, "Projects"))
	if err != nil {
		return nil, fmt.Errorf("list Projects entries: %w", err)
	}

	byslug := make(map[string]ProjectPresence, len(palace)+len(projects))
	for _, s := range palace {
		p := byslug[s]
		p.Slug, p.InPalace = s, true
		byslug[s] = p
	}
	for _, s := range projects {
		p := byslug[s]
		p.Slug, p.InProjects = s, true
		byslug[s] = p
	}

	out := make([]ProjectPresence, 0, len(byslug))
	for _, p := range byslug {
		out = append(out, p)
	}
	// Sorted so the enumeration is deterministic: an audit report that reorders
	// its own rows between runs is not diffable, and week-over-week drift is the
	// point of writing it down.
	slices.SortFunc(out, func(a, b ProjectPresence) int {
		switch {
		case a.Slug < b.Slug:
			return -1
		case a.Slug > b.Slug:
			return 1
		}
		return 0
	})
	return out, nil
}

// listProjectDirs returns the valid project slugs directly under dir. An absent
// dir yields nothing; an existing-but-unreadable one is an error.
//
// The filter mirrors ListProjects exactly — directories only, .local excluded
// (machine-local state, not a project), and the name must be a valid slug — so
// the two enumerators cannot disagree about what counts as a project. If they
// could, the union would report drift that is really just a naming rule applied
// unevenly.
func listProjectDirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var slugs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == ".local" {
			continue
		}
		if slug.Validate(name) != nil {
			continue
		}
		slugs = append(slugs, name)
	}
	return slugs, nil
}
