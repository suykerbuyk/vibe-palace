// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package agentfile

import (
	"fmt"
	"log/slog"
	"os"
)

// Outcome is a per-target report from WireAll. Err, when non-nil, is the
// first error Wire returned for this target; WireAll keeps wiring other
// targets regardless so a single malformed file cannot starve the rest.
type Outcome struct {
	Target Target
	Result Result
	Err    error
}

// wireOpts holds the resolved option set. All fields have sensible
// zero-value defaults so callers passing no options get "detect all
// targets, wire each unconditionally" — the historical cmd_init behavior.
type wireOpts struct {
	// override, when non-nil, bypasses Detect entirely. Used by callers
	// that already know which Targets they want wired (commands upgrade,
	// absorb post-rewrite).
	override []Target
}

// WireOption configures WireAll.
type WireOption func(*wireOpts)

// WithTargets bypasses Detect and wires exactly the given targets. Used by
// callers that already hold a canonical Target (for example, after a file
// rewrite where re-running Detect would be redundant work).
func WithTargets(targets ...Target) WireOption {
	return func(o *wireOpts) {
		o.override = append(o.override[:0:0], targets...)
	}
}

// WireAll is the single orchestrator that drives agentfile.Wire across a
// project root. Behavior with no options: detect every agent file under
// projectRoot and wire each one (the cmd_init flow). See WithTargets for
// the flow used by commands upgrade and absorb, which both hand WireAll
// the exact targets they want wired rather than re-running Detect —
// commands upgrade having already dropped the targets whose block sha is
// current, so WireAll never needs to make that judgment itself.
//
// Returns one Outcome per wired (or attempted) target plus the Skip list
// from Detect (empty when WithTargets is used). The error return is
// non-nil only when projectRoot is unusable — individual Wire failures
// surface in the Outcome.Err field so callers can continue rendering
// status rows for the rest.
func WireAll(projectRoot string, opts ...WireOption) ([]Outcome, []Skip, error) {
	cfg := wireOpts{}
	for _, o := range opts {
		o(&cfg)
	}

	if projectRoot == "" {
		return nil, nil, fmt.Errorf("WireAll: empty projectRoot")
	}
	if info, err := os.Stat(projectRoot); err != nil {
		return nil, nil, fmt.Errorf("WireAll: stat %s: %w", projectRoot, err)
	} else if !info.IsDir() {
		return nil, nil, fmt.Errorf("WireAll: %s is not a directory", projectRoot)
	}

	var (
		targets []Target
		skips   []Skip
	)
	if cfg.override != nil {
		targets = cfg.override
	} else {
		targets, skips = Detect(projectRoot)
	}

	outcomes := make([]Outcome, 0, len(targets))
	for _, t := range targets {
		res, err := Wire(t)
		if err != nil {
			slog.Error("agentfile.WireAll: Wire failed",
				"op", "agentfile.WireAll", "path", t.Path, "display", t.DisplayName, "err", err)
		}
		outcomes = append(outcomes, Outcome{Target: t, Result: res, Err: err})
	}
	return outcomes, skips, nil
}
