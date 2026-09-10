// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"slices"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// palaceLocalOnlyHeader is the first Details line of a non-Pass row. It states
// facts and stops: what the enumerators do with these directories, and that the
// answer is this host's alone. Whether git tracks anything in them is the second
// line, and it is claimed only as far as it was checked (palaceLocalOnlyTracking).
const palaceLocalOnlyHeader = "not counted as a palace store by any enumerator; this row describes THIS host only"

// CheckPalaceLocalOnly reports palace/<slug>/ directories that hold no regular
// file outside their machine-local .local/ — the complement of the presence rule
// every project enumerator applies (storage.Vault.PalaceNonStores, one
// predicate for both answers).
//
// Such a directory is normally host-local. The operator's gitignore keeps
// palace/*/.local/ out of the repository and git cannot carry an empty
// directory, so the canonical way to get one is a pull that deleted a project's
// tracked files and could not remove the ignored cache beside them. Another host
// looking at the same commit may have none. But the canonical gitignore ignores
// only palace/.local/, so a vault can have committed a .local/ file — and then
// it IS synced. The row therefore asks git (one read-only `git ls-files`) and
// states "tracked" or "not tracked" only as far as that answer goes.
//
// Report-only, like vault-filesystem: Info or Pass, never Fail, and it never
// writes. It prescribes NO disposition — no removal command, no verdict on the
// contents. A .local/ can hold imported-sessions.jsonl, the dedupe ledger `vp
// migrate` reads on its next run, and deciding what to do with a host's local
// state is the operator's call, not an instrument's. A test pins the absence of
// disposition words.
//
// 🔴 THIS ROW IS DELIBERATELY ABSENT FROM THE DELIVERY CHECK LISTS in the
// restart and wrap commands and the epic-orchestrator skill. `vp config sync`
// materializes every embedded template into the vault's Templates/ tier, which
// the resolver serves ahead of the embedded copy, so naming the selector there
// hands it to every host that reads the vault — including one still running a
// binary that predates it, where vp_check refuses an unknown check name. The row
// stays reachable through the full `vp check` suite and an explicit
// `vp_check {checks:["palace-local-only"]}`.
func CheckPalaceLocalOnly(v *storage.Vault) Result {
	r := Result{Name: "Palace local-only"}
	dirs, err := v.PalaceNonStores()
	if err != nil {
		r.Status = Info
		r.Summary = fmt.Sprintf("scan palace/: %v", err)
		return r
	}
	if len(dirs) == 0 {
		r.Status = Pass
		r.Summary = "none"
		return r
	}

	tracked, trackingLine := palaceLocalOnlyTracking(v, dirs)
	r.Status = Info
	r.Summary = fmt.Sprintf("%d palace/ director(ies) hold no file outside machine-local .local/", len(dirs))
	r.Details = []string{palaceLocalOnlyHeader, trackingLine}
	for _, d := range dirs {
		line := describePalaceNonStore(d)
		if n := tracked[d.Slug]; n > 0 {
			line += fmt.Sprintf(" — git tracks %d file(s) under .local/", n)
		}
		r.Details = append(r.Details, line)
	}
	return r
}

// palaceLocalOnlyTracking asks git which of these directories hold tracked
// .local/ files and phrases the answer without claiming more than was checked.
// Directories with no .local/ hold no file at all, which git cannot carry, so
// git is asked only when some directory has a .local/.
func palaceLocalOnlyTracking(v *storage.Vault, dirs []storage.PalaceNonStore) (map[string]int, string) {
	if !slices.ContainsFunc(dirs, func(d storage.PalaceNonStore) bool { return d.HasLocal }) {
		return nil, "none of them holds a file, and git cannot carry an empty directory"
	}
	tracked, err := v.TrackedPalaceLocalFiles()
	if err != nil {
		return nil, fmt.Sprintf("whether git tracks any file in them could not be checked: %v", err)
	}
	for _, d := range dirs {
		if tracked[d.Slug] > 0 {
			return tracked, "git tracks the .local/ files counted below, so those are synced to every host " +
				"that pulls; the embed-cache sweep leaves such a directory in place"
		}
	}
	return tracked, "git tracks no file in any of them, so none of this is synced to another host"
}

// describePalaceNonStore renders one directory as a single line of facts, e.g.
//
//	palace/obs/ — .local/: embed-cache/ (1 file), imported-sessions.jsonl — Projects/obs/: absent
func describePalaceNonStore(d storage.PalaceNonStore) string {
	parts := []string{"palace/" + d.Slug + "/"}
	switch {
	case d.HasLocal && d.EmptySubtree:
		parts = append(parts, ".local/: "+describeLocalEntries(d.Local), "empty subtree outside .local/")
	case d.HasLocal:
		parts = append(parts, ".local/: "+describeLocalEntries(d.Local))
	case d.EmptySubtree:
		parts = append(parts, "(empty subtree only)")
	default:
		parts = append(parts, "(empty)")
	}
	projects := "absent"
	if d.InProjects {
		projects = "present"
	}
	parts = append(parts, "Projects/"+d.Slug+"/: "+projects)
	return strings.Join(parts, " — ")
}

func describeLocalEntries(entries []storage.LocalEntry) string {
	if len(entries) == 0 {
		return "(empty)"
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir {
			out = append(out, e.Name)
			continue
		}
		switch e.Files {
		case -1:
			out = append(out, e.Name+"/ (unreadable)")
		case 1:
			out = append(out, e.Name+"/ (1 file)")
		default:
			out = append(out, fmt.Sprintf("%s/ (%d files)", e.Name, e.Files))
		}
	}
	return strings.Join(out, ", ")
}
