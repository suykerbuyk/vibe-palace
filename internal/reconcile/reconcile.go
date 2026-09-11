// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package reconcile provides Check → Plan → Apply reconcilers for the
// config-file tiers that vibe-palace manages. Each reconciler wraps
// storage and check helpers so init, check, and config sync can share the
// same per-artifact logic.
package reconcile

import (
	"context"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/check"
)

// Tier identifies which of the three config-file tiers a reconciler owns.
type Tier string

const (
	TierGlobal  Tier = "global"
	TierVault   Tier = "vault"
	TierProject Tier = "project"
)

// ActionKind classifies a single Plan action.
type ActionKind string

const (
	// ActionCreate: the artifact is missing and will be created.
	ActionCreate ActionKind = "Create"
	// ActionUpdate: the artifact exists but drifted from canonical; will be
	// patched (e.g. missing keys added).
	ActionUpdate ActionKind = "Update"
	// ActionUnchanged: the artifact is already in sync.
	ActionUnchanged ActionKind = "Unchanged"
	// ActionSkip: cannot act because a required input is missing (e.g. sync
	// mode with no seed and nothing to fix).
	ActionSkip ActionKind = "Skip"
	// ActionDelete: the artifact is a vault Templates/ copy the binary can
	// prove is vp's — the current embedded copy, or an earlier shipped version
	// from the frozen shipped.txt manifest, line endings aside
	// (templates.ClassifyVaultCopy) — and therefore redundant: the embedded
	// floor serves the resource directly. The fix is to prune the vault file.
	// Emitted by the TemplateTree reconciler under the override-only model
	// (ADR-008 Phase 3), and never prompted: nothing it removes is operator
	// content.
	//
	// Details carry "embedded_relpath=" (the built-in it prunes),
	// "provenance=" (current or earlier), "vault_sha=" (the raw sha256 of the
	// worktree bytes; empty when the file is already absent), "key=" (the
	// ProvenanceKey a shipped.txt row matches) and, for a removal already
	// pending in the worktree, "pending=true". Apply re-reads the file and
	// re-checks it with PruneAccepts immediately before removing it, and
	// `vp config sync` checks the committed copy and every remote tip with
	// the same rule before it commits the removal.
	//
	// (A reconciler prompt, ActionPrompt, existed until the shipped-version
	// manifest retired templates.lock: with provenance decided by the binary
	// alone, an operator's copy is always kept and there is nothing to ask.)
	ActionDelete ActionKind = "Delete"
)

// Action describes a single proposed change to one artifact (typically
// one file or directory).
type Action struct {
	Kind    ActionKind
	Target  string   // absolute path of the artifact, when applicable
	Summary string   // short human-readable description
	Details []string // optional — e.g. list of missing keys for an Update
}

// Detail returns the value of the first "<key>=<value>" entry in Details, or
// "" when the key is absent. The TemplateTree reconciler's Delete rows carry
// their provenance this way (see ActionDelete).
func (a Action) Detail(key string) string {
	prefix := key + "="
	for _, d := range a.Details {
		if after, ok := strings.CutPrefix(d, prefix); ok {
			return after
		}
	}
	return ""
}

// Plan is the full set of proposed actions for one reconciler.
type Plan struct {
	Actions []Action
}

// Report summarises what Apply did.
type Report struct {
	Created   int
	Updated   int
	Unchanged int
	Skipped   int
	Pruned    int
	Errors    []error
	// Notes are human-readable outcome lines a caller may surface verbatim,
	// written only for work that actually happened (e.g. how many lines a
	// write really added, as opposed to what the Plan estimated).
	Notes []string
}

// Reconciler is the Check → Plan → Apply contract each config-tier adapter
// implements. Check returns []check.Result so vp check can consume the same
// row shape it always has. Plan reads current state; Apply writes.
type Reconciler interface {
	Name() string
	Tier() Tier
	Check(ctx context.Context) []check.Result
	Plan(ctx context.Context) (Plan, error)
	Apply(ctx context.Context, p Plan) (Report, error)
}
