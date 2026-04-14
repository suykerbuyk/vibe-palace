// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package commands

import (
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
)

// BlockChangeKind classifies how an agent file's managed block compares to
// the embedded (source-of-truth) block.
type BlockChangeKind string

const (
	// BlockMissing means the agent file has no managed block yet.
	BlockMissing BlockChangeKind = "missing"
	// BlockStale means the block is present but its sha differs from embedded.
	BlockStale BlockChangeKind = "stale"
	// BlockCurrent means the block matches the embedded sha.
	BlockCurrent BlockChangeKind = "current"
)

// BlockChange describes a single agent file's managed-block status.
type BlockChange struct {
	// Target is the detected agent file (CLAUDE.md, AGENTS.md, …).
	Target agentfile.Target
	// Kind is the comparison result.
	Kind BlockChangeKind
	// PresentSha is the sha currently in the agent file; empty when Missing.
	PresentSha string
	// ExpectedSha is the sha the embedded block would install.
	ExpectedSha string
}

// ScanAgentBlocks walks the detected agent files under projectRoot and
// returns one BlockChange per file. Files without a managed block are
// returned as BlockMissing so upgrade can offer to add them.
func ScanAgentBlocks(projectRoot string) ([]BlockChange, error) {
	targets, _ := agentfile.Detect(projectRoot)
	expected := agentfile.ExpectedSha()

	out := make([]BlockChange, 0, len(targets))
	for _, t := range targets {
		sha, present, err := agentfile.ScanBlock(t.Path)
		if err != nil {
			return nil, err
		}
		bc := BlockChange{
			Target:      t,
			PresentSha:  sha,
			ExpectedSha: expected,
		}
		switch {
		case !present:
			bc.Kind = BlockMissing
		case sha == expected:
			bc.Kind = BlockCurrent
		default:
			bc.Kind = BlockStale
		}
		out = append(out, bc)
	}
	return out, nil
}

// ApplyAgentBlocks re-wires each target using the agentfile atomic-write path.
// Callers pass only the BlockChanges they want to apply (stale or missing);
// any BlockCurrent entry is skipped here too so callers can safely pass the
// raw ScanAgentBlocks result.
//
// Thin wrapper over agentfile.WireAll with WithTargets — the orchestrator
// owns the Wire loop so all three wiring call sites share one code path.
func ApplyAgentBlocks(changes []BlockChange) error {
	var targets []agentfile.Target
	var projectRoot string
	for _, c := range changes {
		if c.Kind == BlockCurrent {
			continue
		}
		targets = append(targets, c.Target)
		if projectRoot == "" {
			projectRoot = filepath.Dir(c.Target.Path)
		}
	}
	if len(targets) == 0 {
		return nil
	}
	outcomes, _, err := agentfile.WireAll(projectRoot, agentfile.WithTargets(targets...))
	if err != nil {
		return err
	}
	for _, oc := range outcomes {
		if oc.Err != nil {
			return oc.Err
		}
	}
	return nil
}
