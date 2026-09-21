// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// vaultProjectConfigHeader is the first Details line of an Info row: what the
// files are, that nothing reads them, and where per-project settings live now.
const vaultProjectConfigHeader = "retired per-project vault config: nothing reads it as input, and per-project " +
	"scoring lives in each host's own <config dir>/vibe-palace/projects/<slug>.toml"

// CheckVaultProjectConfig reports every Projects/<slug>/config.toml present in
// the vault — the per-project config file that no binary reads or writes any
// longer.
//
// A survivor is inert, and a COMMITTED one is also invisible everywhere else:
// git status shows nothing and vault tidy says nothing, so without this row a
// survivor that a late host or a hand edit put back would never be mentioned
// again. That is why the row enumerates the FILESYSTEM and never git: an
// untracked survivor (a hand edit, a restored backup) must be listed exactly as
// a committed one is.
//
// Report-only: Info or Pass, never Fail. Each survivor's line carries the
// removal command. vp vault delete is deliberately left ungated for this path
// (vaultfs refuses only writes that would create or change it), so the command
// the row prints is one that works.
func CheckVaultProjectConfig(vaultRoot string) Result {
	r := Result{Name: "Vault project config"}
	projectsDir := filepath.Join(vaultRoot, "Projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			r.Status = Pass
			r.Summary = "no Projects/ directory"
			return r
		}
		r.Status = Info
		r.Summary = fmt.Sprintf("scan Projects/: %v", err)
		return r
	}

	var survivors, unchecked []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel := "Projects/" + e.Name() + "/config.toml"
		info, lerr := os.Lstat(filepath.Join(projectsDir, e.Name(), "config.toml"))
		switch {
		case lerr == nil:
			line := rel + " — remove with: vp vault delete " + rel + ", then commit the removal"
			if !info.Mode().IsRegular() {
				line = rel + " (not a regular file: " + info.Mode().Type().String() + ") — remove it by hand"
			}
			survivors = append(survivors, line)
		case errors.Is(lerr, fs.ErrNotExist):
		default:
			// Neither present nor absent: say so rather than read it as absent.
			unchecked = append(unchecked, fmt.Sprintf("%s: could not be checked: %v", rel, lerr))
		}
	}

	if len(survivors) == 0 && len(unchecked) == 0 {
		r.Status = Pass
		r.Summary = "none"
		return r
	}
	sort.Strings(survivors)
	sort.Strings(unchecked)
	r.Status = Info
	switch {
	case len(survivors) > 0 && len(unchecked) > 0:
		r.Summary = fmt.Sprintf("%d retired Projects/<slug>/config.toml file(s) present; %d could not be checked",
			len(survivors), len(unchecked))
	case len(survivors) > 0:
		r.Summary = fmt.Sprintf("%d retired Projects/<slug>/config.toml file(s) present", len(survivors))
	default:
		r.Summary = fmt.Sprintf("%d Projects/<slug>/config.toml path(s) could not be checked", len(unchecked))
	}
	if len(survivors) > 0 {
		r.Details = append([]string{vaultProjectConfigHeader}, survivors...)
	}
	r.Details = append(r.Details, unchecked...)
	return r
}
