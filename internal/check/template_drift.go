// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"os"
	"path/filepath"

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

// TemplateDriftRows classifies every embedded resource against the vault copy
// under relSubpath and returns one Result per resource, named
// "<namePrefix>:<vault-relative key>".
//
// It is the shared primitive behind both the CLI's per-template table and the
// aggregate producer below. relSubpath is vault-relative and slash-separated
// ("Templates", "Projects/<slug>"); namePrefix is the caller's row-name prefix.
func TemplateDriftRows(vaultRoot, relSubpath, namePrefix string) []Result {
	resources, err := templates.WalkEmbedded()
	if err != nil {
		return []Result{{
			Name:   namePrefix,
			Status: Fail,
			Err:    fmt.Errorf("walk embedded: %w", err),
		}}
	}
	lock, err := templates.ReadLock(vaultRoot)
	if err != nil {
		return []Result{{
			Name:   namePrefix,
			Status: Fail,
			Err:    fmt.Errorf("read lock: %w", err),
		}}
	}

	out := make([]Result, 0, len(resources))
	for _, res := range resources {
		key := relSubpath + "/" + res.RelPath
		target := filepath.Join(vaultRoot, filepath.FromSlash(key))
		vaultSHA, herr := templates.HashFile(target)
		if herr != nil && !os.IsNotExist(herr) {
			out = append(out, Result{
				Name:   namePrefix + ":" + key,
				Status: Fail,
				Err:    herr,
			})
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
		// pending a prune; a genuine override is in sync.
		status := Info
		summary := "drift"
		switch {
		case !vaultExists && !haveLock:
			status = Pass
			summary = "served from embedded floor"
		case !vaultExists && haveLock:
			// Dangling lock entry: sync will drop it. Drift, not fatal.
			summary = "drift (dangling lock entry; embedded floor serves it)"
		case haveLock && vaultSHA == entry.EmbeddedSHA:
			summary = "drift (reconciler-owned mirror pending prune)"
		case haveLock && vaultSHA == embSHA:
			summary = "drift (byte-identical to current embedded; pending prune)"
		case haveLock && embSHA == entry.EmbeddedSHA:
			status = Pass
			summary = "user override"
			// default: diverged override (haveLock) or no-lock file — drift
			// pending a prompt.
		}
		out = append(out, Result{
			Name:    namePrefix + ":" + key,
			Status:  status,
			Summary: summary,
		})
	}
	return out
}

// CheckTemplateDrift is the registry-shaped aggregate: one row for the whole
// Templates/ tree, with Details naming only the resources that actually drift.
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

	rows := TemplateDriftRows(vaultRoot, "Templates", "Templates")

	var drifted, failed []string
	for _, row := range rows {
		switch row.Status {
		case Fail:
			failed = append(failed, fmt.Sprintf("  %s: %v", row.Name, row.Err))
		case Info:
			drifted = append(drifted, fmt.Sprintf("  %s: %s", row.Name, row.Summary))
		}
	}

	if len(failed) > 0 {
		r.Status = Fail
		r.Summary = fmt.Sprintf("%d of %d template(s) unreadable", len(failed), len(rows))
		r.Details = append(failed, templateDriftRemedy...)
		return r
	}
	if len(drifted) == 0 {
		r.Status = Pass
		r.Summary = fmt.Sprintf("%d templates in sync", len(rows))
		return r
	}
	r.Status = Info
	r.Summary = fmt.Sprintf("%d of %d template(s) drift from the embedded corpus", len(drifted), len(rows))
	r.Details = append(drifted, templateDriftRemedy...)
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
// needs. The customisation half names what is actually unsafe today — a vault
// Templates/ file that shadows a built-in, which `vp config sync --yes` and the
// upgrade commands' reset both discard — and what is not: a new vault-wide
// name, which no reconciler or upgrade command iterates.
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
	"currently unsafe: `vp config sync --yes` (or its o/O answer) replaces it with the",
	"embedded copy and the next sync prunes it, and the upgrade commands' reset",
	"replaces it too (task vault-template-override-is-discarded-by-config-sync).",
	"A NEW vault-wide command or skill under Templates/ is safe — nothing touches it.",
	"A byte-identical mirror is drift pending a prune, not an error.",
}
