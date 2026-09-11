// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
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
	driftMirror                    // a vp-written mirror pending a prune
	driftDangling                  // a lock entry whose file is gone
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

func templateDriftRows(vaultRoot, relSubpath, namePrefix string) []driftRow {
	resources, err := templates.WalkEmbedded()
	if err != nil {
		return []driftRow{{kind: driftFailed, Result: Result{
			Name:   namePrefix,
			Status: Fail,
			Err:    fmt.Errorf("walk embedded: %w", err),
		}}}
	}
	lock, err := templates.ReadLock(vaultRoot)
	if err != nil {
		return []driftRow{{kind: driftFailed, Result: Result{
			Name:   namePrefix,
			Status: Fail,
			Err:    fmt.Errorf("read lock: %w", err),
		}}}
	}

	out := make([]driftRow, 0, len(resources))
	for _, res := range resources {
		key := relSubpath + "/" + res.RelPath
		target := filepath.Join(vaultRoot, filepath.FromSlash(key))
		vaultSHA, herr := templates.HashFile(target)
		if herr != nil && !os.IsNotExist(herr) {
			out = append(out, driftRow{kind: driftFailed, Result: Result{
				Name:   namePrefix + ":" + key,
				Status: Fail,
				Err:    herr,
			}})
			continue
		}
		vaultExists := herr == nil
		embSHA, ok := templates.EmbeddedSHA(res.RelPath)
		if !ok {
			embSHA = res.SHA256
		}
		entry, haveLock := lock.Entries[key]

		// Override-only model: no vault mirror is the healthy state (the
		// embedded floor serves it). A byte-identical mirror is drift
		// pending a prune. An operator's override is kept — and reported as
		// Info, never Pass, so an override that shadows a built-in stays
		// visible to every restart and wrap that runs this check.
		status := Info
		kind := driftOverride
		var summary string
		switch {
		case !vaultExists && !haveLock:
			status = Pass
			kind = driftNone
			summary = "served from embedded floor"
		case !vaultExists && haveLock:
			// Dangling lock entry: sync will drop it. Drift, not fatal.
			kind = driftDangling
			summary = "drift (dangling lock entry; embedded floor serves it)"
		case haveLock && vaultSHA == entry.EmbeddedSHA:
			kind = driftMirror
			summary = "drift (reconciler-owned mirror pending prune)"
		case haveLock && vaultSHA == embSHA:
			kind = driftMirror
			summary = "drift (byte-identical to current embedded; pending prune)"
		case !haveLock && vaultSHA == embSHA:
			// The sync's silent-adopt pre-pass makes this a prune.
			kind = driftMirror
			summary = "drift (byte-identical to current embedded; pending prune)"
		case haveLock && embSHA == entry.EmbeddedSHA:
			summary = "operator override of a built-in (kept; shadows embedded " + shortSHA(embSHA) + ")"
		case haveLock:
			summary = "operator override of a built-in (kept; the embedded copy changed since this host's lock baseline)"
		default:
			summary = "override of a built-in with no lock entry on this host (kept)"
		}
		out = append(out, driftRow{kind: kind, Result: Result{
			Name:    namePrefix + ":" + key,
			Status:  status,
			Summary: summary,
		}})
	}
	return out
}

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

	var noted, failed []string
	var overrides, mirrors, dangling int
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
		case driftDangling:
			dangling++
		}
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
	if dangling > 0 {
		halves = append(halves, fmt.Sprintf("%d dangling lock entr(y/ies) pending a sync", dangling))
	}
	r.Status = Info
	r.Summary = fmt.Sprintf("%s (of %d)", strings.Join(halves, ", "), len(rows))
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
// needs. The customisation half steers to the project tier, and says exactly
// which risk to a vault Templates/ override of a built-in is closed (`vp config
// sync` no longer overwrites one or commits its removal) and which remains (the
// upgrade commands' --overwrite reset, and a lock-less host's prompt). It must
// not call that tier safe: reversing the steer is a recorded operator decision
// that belongs to template-provenance-manifest-retires-the-host-local-lock.
var templateDriftRemedy = []string{
	"CHANGING VIBE-PALACE ITSELF: the binary and the vault-served templates ship",
	"TOGETHER — a template supplies arguments the binary requires (commands/wrap.md",
	"supplies the expected_sha256 vp_update_resume demands), so a NEW binary served",
	"a stale vault copy breaks that command outright. The reverse is harmless. Edit",
	"ONLY the Go-embedded copy under internal/templates/templates/ (doctrine.md and",
	"commands/ included), then `make install`, then `vp config sync` — in that order.",
	"Never hand-edit templates.lock.",
	"CUSTOMISING A BUILT-IN command or skill: put your copy under",
	"Projects/<slug>/commands/ or Projects/<slug>/skills/, which no reconciler and",
	"no upgrade command touches. An override of a built-in under vault Templates/ is",
	"still not recommended. `vp config sync` no longer overwrites one, and never",
	"commits its removal (a committed override is restored from HEAD), but",
	"`vp commands upgrade --overwrite` / `vp skills upgrade --overwrite` reset it to",
	"the embedded copy (task upgrade-overwrite-resets-vault-template-overrides), and",
	"a host whose templates.lock does not record it prompts on every sync (task",
	"template-provenance-manifest-retires-the-host-local-lock).",
	"A NEW vault-wide command or skill under Templates/ is safe — nothing touches it.",
	"A byte-identical mirror is drift pending a prune, not an error. An override of",
	"a built-in is reported here so it stays visible, not because it is wrong.",
}
