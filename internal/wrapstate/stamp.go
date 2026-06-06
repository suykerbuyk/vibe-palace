// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
)

// StampInput carries the resolved paths StampIter needs.
type StampInput struct {
	Project     string // project slug (recorded in the snapshot)
	ProjectRoot string // absolute local project repo root (anchor target)
	TasksDir    string // absolute path to the vault tasks/ dir (snapshot source)
	Iter        int    // iteration number to stamp (>= 1)
}

// StampResult is the JSON shape returned by vp_stamp_iter.
type StampResult struct {
	AnchorPath    string `json:"anchor_path"`
	Iter          int    `json:"iter"`
	BytesWritten  int    `json:"bytes_written"`
	SnapshotPath  string `json:"snapshot_path"`
	SnapshotBytes int    `json:"snapshot_bytes"`
}

// StampIter atomically writes the iteration anchor to
// <projectRoot>/.vibe-palace/last-iter AND a snapshot of the vault-side tasks/
// tree to <projectRoot>/.vibe-palace/last-tasks-snapshot.json. Both are
// project-root files (not vault artifacts), so they are written with surface
// stamping disabled. The first is the canonical project-side anchor for the
// wrap pipeline; the second anchors Collect's task-deltas computation. Every
// wrap MUST stamp.
func StampIter(in StampInput) (StampResult, error) {
	if in.ProjectRoot == "" {
		return StampResult{}, fmt.Errorf("project_path is required")
	}
	if !filepath.IsAbs(in.ProjectRoot) {
		return StampResult{}, fmt.Errorf("project_path must be absolute, got %q", in.ProjectRoot)
	}
	if in.Iter < 1 {
		return StampResult{}, fmt.Errorf("iter must be >= 1, got %d", in.Iter)
	}

	stateDir := filepath.Join(in.ProjectRoot, AnchorDir)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return StampResult{}, fmt.Errorf("create parent directory: %w", err)
	}

	dest := filepath.Join(stateDir, AnchorFile)
	data := []byte(strconv.Itoa(in.Iter) + "\n")
	// vaultRoot == "" skips surface stamping: these are project-root files,
	// not vault writes.
	if err := atomicfile.Write("", dest, data); err != nil {
		return StampResult{}, fmt.Errorf("write last-iter: %w", err)
	}

	snapshotPath := filepath.Join(stateDir, SnapshotFile)
	snapshotBytes, err := writeTasksSnapshot(in, snapshotPath)
	if err != nil {
		return StampResult{}, fmt.Errorf("write last-tasks-snapshot: %w", err)
	}

	return StampResult{
		AnchorPath:    dest,
		Iter:          in.Iter,
		BytesWritten:  len(data),
		SnapshotPath:  snapshotPath,
		SnapshotBytes: snapshotBytes,
	}, nil
}

// writeTasksSnapshot enumerates the live state of the vault-side tasks/ tree
// and writes the resulting slug sets to snapshotPath as JSON. The snapshot's
// anchor_sha records the prior wrap's anchor (best-effort, debugging aid). A
// missing vault tasks directory degrades to an empty-snapshot write rather
// than erroring. Returns the byte count written.
func writeTasksSnapshot(in StampInput, snapshotPath string) (int, error) {
	active, done, cancelled, err := EnumerateLiveTasksFS(in.TasksDir)
	if err != nil {
		return 0, fmt.Errorf("enumerate live tasks: %w", err)
	}
	if active == nil {
		active = []string{}
	}
	if done == nil {
		done = []string{}
	}
	if cancelled == nil {
		cancelled = []string{}
	}

	anchorSHA, _ := LastIterAnchorSha(in.ProjectRoot)

	snap := Snapshot{
		IterN:     in.Iter,
		AnchorSHA: anchorSHA,
		Active:    active,
		Done:      done,
		Cancelled: cancelled,
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("marshal snapshot: %w", err)
	}
	data = append(data, '\n')
	if err := atomicfile.Write("", snapshotPath, data); err != nil {
		return 0, fmt.Errorf("write snapshot: %w", err)
	}
	return len(data), nil
}
