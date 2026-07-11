// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"errors"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// PreflightCheckItem is one entry in the warnings or errors list of a
// PreflightResult. `check` is a stable identifier (surface, vault_dirty,
// project_dirty); `detail` is the human-readable message.
type PreflightCheckItem struct {
	Check  string `json:"check"`
	Detail string `json:"detail"`
}

// PreflightResult is the JSON shape returned by vp_preflight_wrap. `ok`
// mirrors len(errors) == 0; warnings never flip ok off. `notes` carries
// purely-informational, non-alarming items (e.g. memory changes that commit
// automatically) and, like warnings, never affects `ok`.
type PreflightResult struct {
	OK       bool                 `json:"ok"`
	Warnings []PreflightCheckItem `json:"warnings"`
	Errors   []PreflightCheckItem `json:"errors"`
	Notes    []PreflightCheckItem `json:"notes"`
}

// surfaceCheckCompatible is the seam wrapping surface.CheckCompatible so tests
// can inject incompat conditions without staging an entire vault stamp tree.
var surfaceCheckCompatible = surface.CheckCompatible

// Preflight runs /wrap's readiness probe: surface compatibility (gating,
// errors), vault dirty (advisory warning, scoped to Projects/<project>/), and
// project dirty (advisory warning). Pure orchestration over the three helpers.
//
// project, when non-empty, scopes the vault-dirty probe to Projects/<project>/
// so a sibling project's uncommitted writes do not falsely trip the warning.
func Preflight(vaultRoot, projectRoot, project string) PreflightResult {
	res := PreflightResult{
		Warnings: []PreflightCheckItem{},
		Errors:   []PreflightCheckItem{},
		Notes:    []PreflightCheckItem{},
	}

	// 1. Surface check. Gate on surface.IncompatibleError; any other non-nil
	// error is also an error item. Note CheckCompatible now reports an empty
	// vault path (ErrNoVault) and an unreachable root (*VaultUnreachableError)
	// rather than returning nil for them — both are blocking for a wrap, which
	// exists to write to that vault.
	if err := surfaceCheckCompatible(vaultRoot); err != nil {
		var ie *surface.IncompatibleError
		if errors.As(err, &ie) {
			res.Errors = append(res.Errors, PreflightCheckItem{
				Check: "surface",
				Detail: fmt.Sprintf("binary v%d < vault v%d at %s",
					ie.BinarySurface, ie.VaultSurface, ie.StampDir),
			})
		} else {
			res.Errors = append(res.Errors, PreflightCheckItem{
				Check:  "surface",
				Detail: err.Error(),
			})
		}
	}

	// 2. Vault dirty, split by category. Non-memory dirt is the nag-worthy
	// signal (warning); memory dirt commits automatically (SessionEnd harvest
	// / wrap vault sync) and is surfaced as a non-alarming note. Neither
	// affects ok.
	nonMemoryDirty, memoryDirty, err := VaultDirtyByCategory(vaultRoot, project)
	if err != nil {
		res.Warnings = append(res.Warnings, PreflightCheckItem{
			Check:  "vault_dirty",
			Detail: fmt.Sprintf("vault git status probe failed: %v", err),
		})
	} else {
		if nonMemoryDirty {
			res.Warnings = append(res.Warnings, PreflightCheckItem{
				Check:  "vault_dirty",
				Detail: "vault has uncommitted writes — review before committing the wrap iter",
			})
		}
		if memoryDirty {
			res.Notes = append(res.Notes, PreflightCheckItem{
				Check:  "memory_dirty",
				Detail: "memory changes present — committed automatically by wrap/SessionEnd, not a blocker",
			})
		}
	}

	// 3. Project dirty (warning, never error).
	projectDirty, err := ProjectHasUncommittedWrites(projectRoot)
	if err != nil {
		res.Warnings = append(res.Warnings, PreflightCheckItem{
			Check:  "project_dirty",
			Detail: fmt.Sprintf("project git status probe failed: %v", err),
		})
	} else if projectDirty {
		res.Warnings = append(res.Warnings, PreflightCheckItem{
			Check:  "project_dirty",
			Detail: "project has uncommitted writes — wrap will likely include them in the next commit",
		})
	}

	res.OK = len(res.Errors) == 0
	return res
}
