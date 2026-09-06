// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
// errors), vault dirty (advisory warning, scoped to Projects/<project>/),
// project dirty (advisory warning), and an unconsumed commit.msg (advisory
// warning, clean trees only). Pure orchestration over the helpers.
//
// projectRoot must come from the CALLER, never from the process working
// directory — vp mcp is long-lived and its cwd is the host's launch directory.
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
			// 🔴 THE ERROR'S OWN TEXT, NOT A RE-RENDER OF ITS FIELDS.
			//
			// This used to build the detail by hand from BinarySurface /
			// VaultSurface / StampDir, producing "binary v2 < vault v3 at
			// <dir>" — accurate, well-formed, and USELESS. PreflightCheckItem
			// carries only Check and Detail, so the error is retained nowhere
			// else; ok:false halts /vpc-wrap, and that one string is the whole
			// of what the agent can relay. A stranded host was told what was
			// wrong and never what to do, on a path whose own template calls
			// this "the same class the preflight at the top of this step gates
			// on" — where vp_surface_check DOES carry the remedy.
			//
			// ie.Error() is the pass-through: it renders the version gap AND
			// the remediation, from surface's single source of that prose.
			//
			// 🔴 DO NOT reach for check.CheckSurface's Details instead. It is
			// an import cycle — internal/check/iteration_headings.go already
			// imports this package — and it would re-introduce a second copy of
			// the prose, which is the defect, not the fix.
			res.Errors = append(res.Errors, PreflightCheckItem{
				Check:  "surface",
				Detail: ie.Error(),
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

	// 3. Project dirty (warning, never error) and, on a CLEAN tree only, an
	// unconsumed commit.msg. One git probe serves both: the two checks are
	// mutually exclusive branches of the same tree state, so calling
	// ProjectGitState once is both cheaper and impossible to leave inconsistent.
	//
	// GitNotARepo is silent for both, as it was before: a directory with no
	// commits cannot have an unconsumed message for one.
	state, err := ProjectGitState(projectRoot)
	switch {
	case err != nil:
		res.Warnings = append(res.Warnings, PreflightCheckItem{
			Check:  "project_dirty",
			Detail: fmt.Sprintf("project git status probe failed: %v", err),
		})
	case state == GitDirty:
		res.Warnings = append(res.Warnings, PreflightCheckItem{
			Check:  "project_dirty",
			Detail: "project has uncommitted writes — wrap will likely include them in the next commit",
		})
	case state == GitClean:
		// <project_root>/commit.msg is authored by wrap Step 7 or /stage Step 3
		// and removed by the commit that consumes it (`git commit -F commit.msg
		// && rm commit.msg`). So it exists only while the tree is dirty —
		// between authoring and that commit. Present on a CLEAN tree it is a
		// message nothing consumed, and the next `git commit -F` relands it on
		// unrelated work: silently, because a stale message is valid prose about
		// the same project and the file is gitignored, so `git status` never
		// shows it.
		//
		// The invariant is derived from state git already reports — no stamp
		// file, no mtime heuristic, no comparison against HEAD's message text.
		if _, serr := os.Stat(filepath.Join(projectRoot, "commit.msg")); serr == nil {
			res.Warnings = append(res.Warnings, PreflightCheckItem{
				Check: "commit_msg_unconsumed",
				Detail: fmt.Sprintf("%s exists but the project tree is CLEAN — this message was "+
					"authored and never consumed, and the next `git commit -F commit.msg` would "+
					"reland it on different work. Read it, then either use it for this session's "+
					"commit or delete it.", filepath.Join(projectRoot, "commit.msg")),
			})
		}
	}

	res.OK = len(res.Errors) == 0
	return res
}
