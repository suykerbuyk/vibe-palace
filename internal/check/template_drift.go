// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// Template drift — the vault's Templates/ tree against the binary's embedded
// corpus, reported host-agnostically.
//
// This classification used to live only in reconcile.TemplateTreeReconciler,
// which cmd/vp calls directly. That put it out of reach of check.Producers and
// therefore out of reach of the vp_check MCP tool, so a shell-less host (Grok,
// Zed, any HTTP client) could not verify template drift at all — the rollout
// checklist had to keep prescribing a CLI `vp check` alongside the MCP call.
//
// The recorded blocker for moving it was an import cycle: reconcile imports
// check, so check cannot import reconcile. That is true and is not the
// obstacle it was taken for — the drift computation needs internal/templates,
// not internal/reconcile, and templates imports neither. The classification
// therefore lives HERE, and the reconciler delegates to it. One definition of
// drift, two shapes: per-resource rows for the CLI table, one aggregate row for
// the registry.
//
// Advisory: Info, never Fail. Reconciling a template is `vp config sync`, a
// human move — and under the override-only model the healthy state is that no
// vault mirror exists at all.

// driftKind is what one Templates/ row is, beyond its Status: the aggregate
// splits its count by it, because "an override you wrote is kept" and "a
// mirror is waiting to be pruned" are different news for the reader.
type driftKind int

const (
	driftNone     driftKind = iota // no vault file; the embedded floor serves it
	driftMirror                    // vp-shipped bytes (current or earlier) pending a prune
	driftOverride                  // an operator's override of a built-in, kept
	driftFailed                    // unreadable
)

type driftRow struct {
	Result
	kind driftKind
}

// TemplateDriftRows classifies every embedded resource against the vault copy
// under relSubpath and returns one Result per resource, named
// "<namePrefix>:<vault-relative key>".
//
// It is the shared primitive behind both the CLI's per-template table and the
// aggregate producer below. relSubpath is vault-relative and slash-separated
// ("Templates", "Projects/<slug>"); namePrefix is the caller's row-name prefix.
func TemplateDriftRows(vaultRoot, relSubpath, namePrefix string) []Result {
	rows := templateDriftRows(vaultRoot, relSubpath, namePrefix)
	out := make([]Result, len(rows))
	for i, r := range rows {
		out[i] = r.Result
	}
	return out
}

// templateDriftRows classifies each copy by provenance — the binary alone,
// no host-local state (templates.ClassifyVaultCopy) — exactly as `vp config
// sync`'s reconcile does: a copy of the current embedded copy or of an earlier
// shipped version is drift pending a prune; anything else is an operator's
// override, kept; a path not reached directly is kept and never followed.
func templateDriftRows(vaultRoot, relSubpath, namePrefix string) []driftRow {
	resources, err := templates.WalkEmbedded()
	if err != nil {
		return []driftRow{{kind: driftFailed, Result: Result{
			Name:   namePrefix,
			Status: Fail,
			Err:    fmt.Errorf("walk embedded: %w", err),
		}}}
	}

	out := make([]driftRow, 0, len(resources))
	for _, res := range resources {
		key := relSubpath + "/" + res.RelPath
		name := namePrefix + ":" + key
		fail := func(err error) {
			out = append(out, driftRow{kind: driftFailed, Result: Result{Name: name, Status: Fail, Err: err}})
		}
		if err := vaultfs.CheckDirectPath(vaultRoot, key); err != nil {
			if !errors.Is(err, vaultfs.ErrIndirectPath) {
				fail(err)
				continue
			}
			out = append(out, driftRow{kind: driftOverride, Result: Result{
				Name:    name,
				Status:  Info,
				Summary: NotReachedDirectly + " (" + err.Error() + ")",
			}})
			continue
		}
		data, err := os.ReadFile(filepath.Join(vaultRoot, filepath.FromSlash(key)))
		if errors.Is(err, os.ErrNotExist) {
			out = append(out, driftRow{kind: driftNone, Result: Result{
				Name:    name,
				Status:  Pass,
				Summary: "served from embedded floor",
			}})
			continue
		}
		if err != nil {
			fail(err)
			continue
		}

		// Override-only model: no vault copy is the healthy state (the
		// embedded floor serves it). vp-shipped bytes are drift pending a
		// prune. An operator's override is kept — and reported as Info, never
		// Pass, so an override that shadows a built-in stays visible to every
		// restart and wrap that runs this check.
		kind := driftOverride
		var summary string
		switch templates.ClassifyVaultCopy(res.RelPath, data) {
		case templates.ProvenanceCurrent:
			kind = driftMirror
			summary = "drift (byte-identical to the current embedded copy; pending prune)"
			if string(data) != string(res.Bytes) {
				summary = "drift (identical, line endings aside, to the current embedded copy; pending prune)"
			}
		case templates.ProvenanceEarlier:
			kind = driftMirror
			summary = "drift (an earlier shipped version of " + res.RelPath +
				"; pending prune — it shadows the current built-in until then)"
		default:
			emb, ok := templates.EmbeddedSHA(res.RelPath)
			if !ok {
				emb = res.SHA256
			}
			summary = "operator override of a built-in (kept; shadows embedded " + shortSHA(emb) + ")"
		}
		out = append(out, driftRow{kind: kind, Result: Result{
			Name:    name,
			Status:  Info,
			Summary: summary,
		}})
	}
	return out
}

// NotReachedDirectly is the row a vault Templates/ path gets when a symlink
// sits in its path, it is a special file, or (on Windows) its spelling differs
// from the disk's: vp keeps it and never follows it — neither the check nor the
// prune reads through it. Shared with the reconciler so both say it one way.
const NotReachedDirectly = "not reached directly (a symlink in its path, a special file, or on Windows a " +
	"letter-case or short-name difference); kept, never followed"

// retiredLockDetail is the template-drift detail for a
// .vibe-palace/templates.lock still on disk: `vp config sync` removes an
// untracked, un-ignored one on a git vault of its own, and leaves a tracked
// or ignored one, and any on a non-git vault, where lagging hosts may share it.
const retiredLockDetail = "  .vibe-palace/templates.lock: a retired templates.lock is present; no vp from this release reads it — delete it when every host runs this release"

// shortSHA is the first 12 hex digits of a SHA, for a row a human reads.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// CheckTemplateDrift is the registry-shaped aggregate: one row for the whole
// Templates/ tree, with Details naming only the resources that are not simply
// served from the embedded floor.
//
// The per-resource shape the CLI prints is deliberately NOT what the registry
// serves. This tree carries ~38 embedded resources, and 38 rows is not a check
// result an agent reads — it is a payload an agent skims past, which is the
// same failure mode as an alert that fires on a healthy vault.
func CheckTemplateDrift(vaultRoot string) Result {
	r := Result{Name: "Template drift"}
	if vaultRoot == "" {
		r.Status = Skip
		r.Summary = "no vault configured"
		return r
	}

	rows := templateDriftRows(vaultRoot, "Templates", "Templates")
	_, lerr := os.Lstat(filepath.Join(vaultRoot, ".vibe-palace", "templates.lock"))
	retiredLock := lerr == nil

	var noted, failed []string
	var overrides, mirrors int
	for _, row := range rows {
		switch row.Status {
		case Fail:
			failed = append(failed, fmt.Sprintf("  %s: %v", row.Name, row.Err))
		case Info:
			noted = append(noted, fmt.Sprintf("  %s: %s", row.Name, row.Summary))
		}
		switch row.kind {
		case driftOverride:
			overrides++
		case driftMirror:
			mirrors++
		}
	}
	if retiredLock {
		noted = append(noted, retiredLockDetail)
	}

	if len(failed) > 0 {
		r.Status = Fail
		r.Summary = fmt.Sprintf("%d of %d template(s) unreadable", len(failed), len(rows))
		r.Details = append(failed, templateDriftRemedy...)
		return r
	}
	if len(noted) == 0 {
		r.Status = Pass
		r.Summary = fmt.Sprintf("%d templates in sync", len(rows))
		return r
	}
	var halves []string
	if overrides > 0 {
		halves = append(halves, fmt.Sprintf("%d override(s) of built-ins kept", overrides))
	}
	if mirrors > 0 {
		halves = append(halves, fmt.Sprintf("%d mirror(s) pending a prune", mirrors))
	}
	switch {
	case len(halves) > 0:
		r.Summary = fmt.Sprintf("%s (of %d)", strings.Join(halves, ", "), len(rows))
		if retiredLock {
			r.Summary += "; a retired templates.lock is present"
		}
	default:
		r.Summary = fmt.Sprintf("%d templates in sync; a retired templates.lock is present", len(rows))
	}
	r.Status = Info
	r.Details = append(noted, templateDriftRemedy...)
	return r
}

// templateDriftRemedy is the rollout-ordering rule this check replaced. It used
// to be a paragraph in a project's workflow.md, shipped in every bootstrap
// payload and enforced by nothing.
//
// It speaks to TWO audiences, and says which is which, because vp_check serves
// the same row to both. Addressed only to a vibe-palace contributor, "never edit
// the vault mirror" read to an operator as "you cannot customise a template";
// addressed only to an operator, it would drop the ordering rule a contributor
// needs. The customisation half steers to the project tier and says what vp
// does to a vault Templates/ override of a built-in: nothing — `vp config sync`
// judges it by the binary alone (the frozen shipped-version manifest, no
// host-local lock), never overwrites it, never prompts about it and never
// commits its removal; the upgrade commands never reset one. It names the reset
// verbs as the one way to remove an override, keeping a backup.
var templateDriftRemedy = []string{
	"CHANGING VIBE-PALACE ITSELF: the binary and the vault-served templates ship",
	"TOGETHER — a template supplies arguments the binary requires (commands/wrap.md",
	"supplies the expected_sha256 vp_update_resume demands), so a NEW binary served",
	"a stale vault copy breaks that command outright. The reverse is harmless. Edit",
	"ONLY the Go-embedded copy under internal/templates/templates/ (doctrine.md and",
	"commands/ included), then `make install`, then `vp config sync` — in that order.",
	"internal/templates/shipped.txt is frozen: a template edit needs nothing else.",
	"CUSTOMISING A BUILT-IN command or skill: put your copy under",
	"Projects/<slug>/commands/ or Projects/<slug>/skills/, which no reconciler and",
	"no upgrade command touches. An override of a built-in under vault Templates/",
	"shadows the built-in for every project. `vp config sync` keeps it — it never",
	"overwrites one, never prompts about one, and never commits its removal (a",
	"committed override is restored from HEAD) — and the upgrade commands never",
	"reset one, in any mode. A copy identical, line endings aside, to the current or",
	"an earlier shipped version of the built-in is vp's, not an override, and is",
	"pruned: edit a copy before syncing.",
	"To remove an override on purpose: `vp commands reset NAME` / `vp skills reset",
	"NAME` — it removes the file so the built-in serves it, and keeps a backup.",
	"A NEW vault-wide command or skill under Templates/ is safe — nothing touches it.",
	"A mirror is drift pending a prune, not an error. An override of a built-in is",
	"reported here so it stays visible, not because it is wrong.",
}
