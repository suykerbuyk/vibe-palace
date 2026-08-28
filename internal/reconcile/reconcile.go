// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package reconcile provides Check → Plan → Apply reconcilers for the
// config-file tiers that vibe-palace manages. Each reconciler wraps
// storage and check helpers so init, check, and config sync can share the
// same per-artifact logic.
package reconcile

import (
	"context"

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
	// ActionPrompt: ambiguous reconcile — the orchestrator must collect a
	// user choice and REWRITE this action into a concrete Create / Update /
	// Skip (possibly with a different Target, e.g. "<path>.new") BEFORE
	// calling Apply. Reconciler Apply implementations MUST return an error
	// if they observe an ActionPrompt — it is defense-in-depth against a
	// misbehaving orchestrator.
	//
	// For the TemplateTree reconciler, Action.Details carries the three
	// SHAs and the embedded resource identity needed to render a
	// meaningful menu, write a `.new` sidecar, and produce a
	// `vp check --dry-run` row. The Details slice contains, in order:
	//
	//   "embedded_sha=<hex>"
	//   "vault_sha=<hex>"
	//   "lock_sha=<hex>"
	//   "embedded_relpath=<embedded-relative-path>"
	//
	// Any SHA may be the empty string after the `=` (e.g. lock_sha=
	// when no lock entry exists). embedded_relpath is always non-empty
	// for TemplateTree Prompts and is how the orchestrator recovers the
	// embedded byte source for the `.new` sidecar branch without
	// reverse-engineering from Target. Orchestrator parsers should split
	// on the first '=' and treat missing keys as the empty value.
	ActionPrompt ActionKind = "Prompt"
	// ActionDelete: the artifact is a reconciler-owned vault mirror that
	// is byte-identical to the canonical (embedded) source and therefore
	// redundant — the embedded floor serves it directly. The fix is to
	// prune the vault file (after backing it up to a sibling .bak,
	// mirroring the update backup discipline) and drop its lock entry so
	// the persisted lock lists only genuine user overrides. Emitted by the
	// TemplateTree reconciler under the override-only materialization model
	// (ADR-008 Phase 3). Never prompted: pruning a byte-identical mirror is
	// safe because resolution falls through to the embedded tier.
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
