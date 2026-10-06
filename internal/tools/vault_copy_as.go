// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

// CopyProjectAs is `vp vault copy --as <newname>` (U11): copy one project into
// the served vault from a published remote, then rename it there to a new slug.
// It is pure composition — it reuses the landed cores storage.ApplyCopy (U5),
// storage.ApplyRename (U9) and the host-local index step
// indexstore.AdoptRenamedProject, and adds NO storage engine. It lives here (not
// in storage) because it drives the host-local index step, and storage cannot
// import indexstore. Both the CLI (`vp vault copy --as`) and the MCP tool
// (vp_vault_copy's `as` param) call it.
//
// Spec: task copy-a-project-under-a-new-name, `Plan revision 2026-10-05`
// (governs) + the original plan + `Plan re-review 2026-10-05 (reviewer, round
// 2)` SF-a. The crux is a MARKER-AWARE, GATE-FIRST state machine: it reads the
// vault's standing lifecycle marker first and routes a pending run to its
// owning core's redo (the per-vault marker is one O_EXCL slot, so a copy marker
// and a rename marker never coexist); only with no marker does it classify the
// stable state from the destination trees + the departure record, and the
// rename's fresh-target preflight runs AFTER that classification, never before
// (so a completed `--as` re-run is a no-op, not a `<newname>`-exists refusal).

import (
	"context"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// CopyAsOutcome names what CopyProjectAs did.
type CopyAsOutcome string

const (
	// CopyAsCopiedAndRenamed: a full run — copied then renamed.
	CopyAsCopiedAndRenamed CopyAsOutcome = "copied-and-renamed"
	// CopyAsResumed: the copy was already present; only the rename ran.
	CopyAsResumed CopyAsOutcome = "resumed-rename"
	// CopyAsAlreadyDone: nothing to do (idempotent no-op).
	CopyAsAlreadyDone CopyAsOutcome = "already-done"
	// CopyAsDryRun: planned only, wrote nothing.
	CopyAsDryRun CopyAsOutcome = "dry-run"
)

// CopyAsResult reports a `vp vault copy --as` run.
type CopyAsResult struct {
	Outcome   CopyAsOutcome                     `json:"outcome"`
	Project   string                            `json:"project"`
	NewName   string                            `json:"new_name"`
	Copy      *storage.CopyResult               `json:"copy,omitempty"`
	Rename    *storage.RenameResult             `json:"rename,omitempty"`
	HostLocal *indexstore.RenameHostLocalResult `json:"host_local,omitempty"`
	// BaselineWarnings names each archive that could not join this host's
	// baseline set under the new name. Run inside this shared core (not the
	// surfaces) so the CLI and MCP paths cannot diverge (SF2 parity).
	BaselineWarnings []string `json:"baseline_warnings,omitempty"`
}

// CopyProjectAs runs the --as composition. req is copy's own request with
// exactly one project in req.Projects (the arity is the caller's contract;
// enforced here too). newName is the slug to leave it under.
func CopyProjectAs(ctx context.Context, vault *storage.Vault, req storage.CopyRequest, newName string) (*CopyAsResult, error) {
	if vault == nil || vault.Root == "" {
		return nil, fmt.Errorf("no vault is bound: copy --as acts only on the vault this server serves")
	}
	if len(req.Projects) != 1 {
		return nil, fmt.Errorf("copy --as copies exactly one project under a new name, got %d", len(req.Projects))
	}
	project := req.Projects[0]
	if err := slug.Validate(newName); err != nil {
		return nil, fmt.Errorf("--as %q: %v", newName, err)
	}
	if newName == project {
		return nil, fmt.Errorf("--as %q is the same as the project being copied; leave it off to copy under the same name", newName)
	}
	req.Vault = vault.Root
	res := &CopyAsResult{Project: project, NewName: newName}

	// 1) Marker-aware gate: route any pending lifecycle run to its owning core.
	cmd, found, err := storage.PendingLifecycleCommand(vault.Root)
	if err != nil {
		return res, err
	}
	switch {
	case found && cmd == storage.RenameCommandName:
		// The copy already published; a rename run is pending — finish it.
		return res, finishRenameHalf(ctx, vault, project, newName, req.DryRun, res)
	case found && cmd == storage.CopyCommandName:
		// A copy run is pending — let ApplyCopy's redo finish it or roll it back
		// (a dry run, or a foreign copy, surfaces LifecyclePendingError here).
		cr, cerr := storage.ApplyCopy(req)
		res.Copy = cr
		if cerr != nil {
			return res, cerr
		}
		// The marker is cleared now; fall through to the stable-state gate.
	case found:
		// A foreign lifecycle run (e.g. a project delete) stands on the dest;
		// ApplyCopy surfaces its LifecyclePendingError. U11 cannot proceed.
		cr, cerr := storage.ApplyCopy(req)
		res.Copy = cr
		return res, cerr
	}

	// 2) No pending marker: classify the stable state from trees + the record.
	// TRACKED-content presence (git ls-files over the footprint), NOT raw dir
	// existence and NOT vault.ProjectExists. Raw os.Lstat over-matches departure
	// residue (empty/ignored leftover dirs) — a prior-departed <project> would
	// read present and mis-route a fresh copy into a rename with nothing to
	// rename. Tracked content excludes residue (so a departed-residue <project>
	// reads absent → FULL copy → the clear departure-record refusal) while still
	// seeing a re-created <project> with tracked content (SF-a ambiguous).
	pPresent, err := vault.ProjectHasTrackedContent(project)
	if err != nil {
		return res, err
	}
	nPresent, err := vault.ProjectHasTrackedContent(newName)
	if err != nil {
		return res, err
	}
	rec, recFound := departure.Read(vault.Root, project)
	// SF-a sub-note: the direct record of <project>, via Read (never Resolve,
	// which would follow a multi-hop chain and over-match).
	renamedToNew := recFound && rec.Kind == departure.Renamed && rec.To == newName

	switch {
	case !pPresent && nPresent && renamedToNew:
		// (a) DONE — <project> absent, <newname> present, its own record names it.
		// (SF-a: require <project> ABSENT; (P present, N present) is (d), not done.)
		res.Outcome = CopyAsAlreadyDone
		return res, nil
	case nPresent:
		// (d) AMBIGUOUS — <newname> present without the renamed record, or both
		// present. A human resolves it; U11 refuses rather than guess.
		return res, fmt.Errorf("refusing copy --as: %s already holds %q, but %q's departure record does not name it as the rename target — resolve by hand (the destination is in a half-migrated state)", vault.Root, newName, project)
	case pPresent:
		// (b) RESUME — the copy is present with no marker (fully published, or
		// just finished by the copy-pending redo above); rename only.
		return res, finishRenameHalf(ctx, vault, project, newName, req.DryRun, res)
	default:
		// (c) FULL RUN — copy, then rename.
		cr, cerr := storage.ApplyCopy(req)
		res.Copy = cr
		if cerr != nil {
			return res, cerr
		}
		if req.DryRun {
			// The rename cannot be previewed pre-copy (its digest depends on the
			// copied bytes). Report the copy plan and the follow-on rename.
			res.Outcome = CopyAsDryRun
			return res, nil
		}
		res.Outcome = CopyAsCopiedAndRenamed
		return res, finishRenameHalf(ctx, vault, project, newName, false, res)
	}
}

// finishRenameHalf runs (or dry-runs) the rename of <project> to <newName> in
// the served vault, then its host-local index step, reusing the landed cores.
// A dry run returns the rename plan and runs no host-local step. On a pending
// rename marker, ApplyRename's own redo finishes or rolls back.
func finishRenameHalf(ctx context.Context, vault *storage.Vault, project, newName string, dryRun bool, res *CopyAsResult) error {
	rr, rerr := storage.ApplyRename(storage.RenameRequest{Vault: vault.Root, From: project, To: newName, DryRun: dryRun})
	res.Rename = rr
	if rerr != nil {
		return rerr
	}
	if dryRun {
		if res.Outcome == "" {
			res.Outcome = CopyAsDryRun
		}
		return nil
	}
	// The rename published; drive the host-local index step exactly as
	// cmd_vault_rename does (copy built no index store for <project>, so
	// AdoptRenamedStore's source-absent branch skips the move and <newName>
	// re-embeds).
	hl, herr := indexstore.AdoptRenamedProject(ctx, vault, project, newName)
	if herr != nil {
		return fmt.Errorf("the rename committed and published, but the host-local index step failed (re-run copy --as, or run `vp index rebuild %s`): %w", newName, herr)
	}
	res.HostLocal = &hl
	// CLI/MCP parity (SF2): the incoming archives join this host's baseline set
	// under the NEW name here, in the shared core, so neither surface can skip
	// it. Never fails the run — a warning at worst, and under M0=rebuild the new
	// name re-embeds anyway.
	res.BaselineWarnings = AddIncomingArchivesToBaseline(ctx, vault, []string{newName})
	if res.Outcome == "" {
		res.Outcome = CopyAsResumed
	}
	return nil
}
