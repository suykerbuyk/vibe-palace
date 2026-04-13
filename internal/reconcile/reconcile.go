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
	Errors    []error
}

// Reconciler is the Check → Plan → Apply contract each config-tier adapter
// implements. Check returns []check.Result so vp check can consume the same
// row shape it always has. Plan reads current state; Apply writes.
type Reconciler interface {
	Name() string
	Tier() Tier
	Requires() []string
	Check(ctx context.Context) []check.Result
	Plan(ctx context.Context) (Plan, error)
	Apply(ctx context.Context, p Plan) (Report, error)
}
