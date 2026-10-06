// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"strings"
	"time"
)

// ProjectHasTrackedContent reports whether slug has any git-TRACKED file under
// its footprint trees (Projects/<slug>, palace/<slug>). Unlike raw directory
// existence, departure RESIDUE — the ignored/empty leftover directories a delete
// or an earlier run can leave — is not tracked content, so a departed-then-
// residue slug reads ABSENT. It is the presence signal `vp vault copy --as`'s
// resume gate uses, so a stale residue dir cannot mis-route a fresh copy into a
// rename it has nothing to rename.
func (v *Vault) ProjectHasTrackedContent(slug string) (bool, error) {
	args := append([]string{"-c", "core.quotepath=off", "ls-files", "-z", "--"}, ProjectTrees(slug)...)
	out, err := gitCmd(v.Root, 30*time.Second, args...)
	if err != nil {
		return false, fmt.Errorf("list tracked files under %s: %w", slug, err)
	}
	return strings.Trim(out, "\x00 \n\t\r") != "", nil
}
