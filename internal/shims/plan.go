// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
)

// ChangeKind is the plan-level classification of a proposed action against
// one shim file. Mirrors ResultKind but adds Stale (a file we own for a
// name no longer in the command set).
type ChangeKind int

const (
	// New: no file at the target path; one will be created.
	New ChangeKind = iota
	// Modified: file exists with our marker but a stale hash; needs rewrite.
	Modified
	// UnchangedChange: file exists with our marker and the current hash.
	// Kept in the plan so callers can report counts accurately; Apply does
	// nothing for this kind.
	UnchangedChange
	// Stale: file exists with our marker for a command name that is no
	// longer in commands.List(). Candidate for removal pending user consent.
	Stale
	// CustomChange: file exists at a vpc-*.md path without our marker.
	// Untouched by Apply, surfaced for visibility.
	CustomChange
)

// String gives each kind a stable short name for status rows and tests.
func (k ChangeKind) String() string {
	switch k {
	case New:
		return "new"
	case Modified:
		return "modified"
	case UnchangedChange:
		return "unchanged"
	case Stale:
		return "stale"
	case CustomChange:
		return "custom"
	default:
		return fmt.Sprintf("kind(%d)", int(k))
	}
}

// Change is one entry in a shim plan: the action we propose for a single
// file. Name is the command name (empty for CustomChange files we did not
// put there). Path is always the absolute, cleaned filesystem path.
type Change struct {
	Kind    ChangeKind
	Name    string
	Path    string
	Brief   string // desired brief for New/Modified; empty otherwise
	Project string // owning project slug for project-scoped commands; empty for global
	ArgHint string // argument-hint passthrough for New/Modified; empty otherwise
	PrevSha string // sha currently on disk for Modified/Stale; empty otherwise
}

// Plan computes the set of shim changes required to bring projectRoot's
// .claude/commands/ into alignment with summaries. The function is pure
// relative to the filesystem scan — no writes — so callers can present it
// as a dry-run before invoking Apply.
//
// Summaries carry the authoritative command names and briefs (from
// commands.List). Files under .claude/commands/ with the "vpc-" prefix are
// scanned; files without that prefix are ignored (other tools' territory).
// The returned slice is sorted: New first, then Modified, Unchanged, Stale,
// Custom; stable by path within each kind.
func Plan(summaries []commands.Summary, projectRoot string) ([]Change, error) {
	shimRoot := filepath.Join(projectRoot, ShimDir)

	// Build the desired set keyed by command name.
	desired := make(map[string]commands.Summary, len(summaries))
	for _, s := range summaries {
		desired[s.Name] = s
	}

	// Scan the on-disk set. A missing directory is not an error — it just
	// means every desired shim is New.
	onDisk := map[string]string{}    // name -> absolute path (marker-bearing)
	customs := []string{}            // vpc-*.md files lacking our marker
	customNames := map[string]bool{} // command names occupied by a custom file
	entries, err := os.ReadDir(shimRoot)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", shimRoot, err)
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		fname := ent.Name()
		if !strings.HasPrefix(fname, FilePrefix) || !strings.HasSuffix(fname, ".md") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(fname, FilePrefix), ".md")
		path := filepath.Join(shimRoot, fname)
		scan, err := ScanShim(path)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", path, err)
		}
		if !scan.HasMarker {
			customs = append(customs, path)
			customNames[name] = true
			continue
		}
		onDisk[name] = path
	}

	var changes []Change

	// New / Modified / Unchanged for every desired command.
	for _, s := range summaries {
		path := filepath.Join(shimRoot, Filename(s.Name))
		existing, ok := onDisk[s.Name]
		if !ok {
			// A markerless custom file already occupies this command's path
			// (e.g. a hand-written shim). Wire would refuse to clobber it; the
			// CustomChange entry below already reports it, so skip the New to
			// avoid a confusing duplicate. The user removes the custom file to
			// adopt a managed shim.
			if customNames[s.Name] {
				continue
			}
			changes = append(changes, Change{
				Kind: New, Name: s.Name, Path: path,
				Brief: s.Brief, Project: s.Project, ArgHint: s.ArgHint,
			})
			continue
		}
		scan, err := ScanShim(existing)
		if err != nil {
			return nil, fmt.Errorf("rescan %s: %w", existing, err)
		}
		expected := ExpectedSha(s.Name, s.Brief, s.Project, s.ArgHint)
		if scan.Sha == expected && scan.Version == shimVersion {
			changes = append(changes, Change{
				Kind: UnchangedChange, Name: s.Name, Path: existing,
				Brief: s.Brief, Project: s.Project, ArgHint: s.ArgHint, PrevSha: scan.Sha,
			})
		} else {
			changes = append(changes, Change{
				Kind: Modified, Name: s.Name, Path: existing,
				Brief: s.Brief, Project: s.Project, ArgHint: s.ArgHint, PrevSha: scan.Sha,
			})
		}
	}

	// Stale: on-disk shim for a name that is no longer desired.
	for name, path := range onDisk {
		if _, ok := desired[name]; ok {
			continue
		}
		scan, _ := ScanShim(path)
		changes = append(changes, Change{
			Kind: Stale, Name: name, Path: path, PrevSha: scan.Sha,
		})
	}

	// Custom files: we did not write them, but we do want to report them so
	// the user sees what's in the directory.
	for _, path := range customs {
		changes = append(changes, Change{Kind: CustomChange, Path: path})
	}

	sortChanges(changes)
	return changes, nil
}

// sortChanges produces a deterministic order for CLI output and tests:
// New → Modified → Unchanged → Stale → Custom; alphabetical by path within
// each kind.
func sortChanges(changes []Change) {
	order := map[ChangeKind]int{
		New: 0, Modified: 1, UnchangedChange: 2, Stale: 3, CustomChange: 4,
	}
	sort.SliceStable(changes, func(i, j int) bool {
		if order[changes[i].Kind] != order[changes[j].Kind] {
			return order[changes[i].Kind] < order[changes[j].Kind]
		}
		return changes[i].Path < changes[j].Path
	})
}
