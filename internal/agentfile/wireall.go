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
	// filter, when non-nil, restricts detected targets to those whose
	// DisplayName is present in the set. Ignored when override is set.
	filter map[string]struct{}
	// staleOnly skips targets whose existing block sha already matches
	// the embedded block — the ApplyAgentBlocks semantics.
	staleOnly bool
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

// WithFilter restricts wiring to detected targets whose DisplayName is in
// the given set. Has no effect when combined with WithTargets.
func WithFilter(displayNames ...string) WireOption {
	return func(o *wireOpts) {
		if o.filter == nil {
			o.filter = make(map[string]struct{}, len(displayNames))
		}
		for _, n := range displayNames {
			o.filter[n] = struct{}{}
		}
	}
}

// WithStaleOnly wires only targets whose current block sha differs from
// the embedded sha (or is absent). Mirrors the commands.ApplyAgentBlocks
// filter applied prior to extraction.
func WithStaleOnly() WireOption {
	return func(o *wireOpts) { o.staleOnly = true }
}

// WireAll is the single orchestrator that drives agentfile.Wire across a
// project root. Behavior with no options: detect every agent file under
// projectRoot and wire each one (the cmd_init flow). See WithTargets,
// WithFilter, and WithStaleOnly for the flows used by commands upgrade
// and absorb.
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
		if cfg.filter != nil {
			kept := targets[:0]
			for _, t := range targets {
				if _, ok := cfg.filter[t.DisplayName]; ok {
					kept = append(kept, t)
				}
			}
			targets = kept
		}
	}

	var expectedSha string
	if cfg.staleOnly {
		expectedSha = ExpectedSha()
	}

	outcomes := make([]Outcome, 0, len(targets))
	for _, t := range targets {
		if cfg.staleOnly {
			sha, _, err := ScanBlock(t.Path)
			if err != nil {
				slog.Warn("agentfile.WireAll: ScanBlock failed; wiring anyway",
					"op", "agentfile.WireAll", "path", t.Path, "err", err)
			} else if sha == expectedSha {
				continue
			}
		}
		res, err := Wire(t)
		if err != nil {
			slog.Error("agentfile.WireAll: Wire failed",
				"op", "agentfile.WireAll", "path", t.Path, "display", t.DisplayName, "err", err)
		}
		outcomes = append(outcomes, Outcome{Target: t, Result: res, Err: err})
	}
	return outcomes, skips, nil
}
