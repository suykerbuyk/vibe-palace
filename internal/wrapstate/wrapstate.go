// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package wrapstate ports the wrap-state + anchor machinery that backs the
// /wrap and /restart command surface. It computes every mechanically-derivable
// fact the wrap orchestrator needs (next iteration number, branch, commits and
// files since the last anchor, task deltas via filesystem snapshot diff, test
// counts parsed from doc/TESTING.md, and dirty-state flags) plus a wrap-shape
// classification, and it reads/writes the project-side anchors under
// .vibe-palace/ (last-iter + last-tasks-snapshot.json).
//
// The package takes resolved filesystem paths rather than a vault handle so it
// stays decoupled from internal/storage; the internal/tools layer resolves
// vault-relative paths via the (*storage.Vault) helpers and hands them in.
package wrapstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// AnchorDir is the project-root directory holding the wrap anchors. Unlike
// vibe-vault's .vibe-vault/, vibe-palace stamps anchors under .vibe-palace/.
const AnchorDir = ".vibe-palace"

// AnchorFile is the canonical project-side iteration anchor.
const AnchorFile = "last-iter"

// SnapshotFile is the task-snapshot anchor against which Collect computes
// task deltas at the next wrap.
const SnapshotFile = "last-tasks-snapshot.json"

// WrapShape names a work-unit shape, surfaced as the `shape` field of
// vp_collect_wrap_state's response.
type WrapShape string

const (
	ShapeFreshFeature WrapShape = "fresh-feature"
	ShapePlanning     WrapShape = "planning"
	ShapeBookkeeping  WrapShape = "bookkeeping"
)

// CommitInfo summarizes a commit between the last-iter anchor and HEAD.
type CommitInfo struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// TaskDeltas reports task-folder transitions since the last wrap. Deltas are
// computed via project-repo snapshot comparison (.vibe-palace/last-tasks-snapshot.json),
// not git-history walking.
type TaskDeltas struct {
	Added     []string `json:"added"`
	Retired   []string `json:"retired"`
	Cancelled []string `json:"cancelled"`
}

// TestCounts captures the headline counts parsed out of doc/TESTING.md.
// Warning carries any non-fatal parse-failure detail (empty on success).
type TestCounts struct {
	Unit        int    `json:"unit"`
	Integration int    `json:"integration"`
	Lint        int    `json:"lint"`
	Warning     string `json:"warning,omitempty"`
}

// Result is the JSON shape returned by vp_collect_wrap_state.
type Result struct {
	IterN                       int          `json:"iter_n"`
	Branch                      string       `json:"branch"`
	LastIterAnchorSha           string       `json:"last_iter_anchor_sha,omitempty"`
	CommitsSinceLastIter        []CommitInfo `json:"commits_since_last_iter"`
	FilesChanged                []string     `json:"files_changed"`
	TaskDeltas                  TaskDeltas   `json:"task_deltas"`
	TestCounts                  TestCounts   `json:"test_counts"`
	VaultHasUncommittedWrites   bool         `json:"vault_has_uncommitted_writes"`
	MemoryHasUncommittedWrites  bool         `json:"memory_has_uncommitted_writes"`
	ProjectHasUncommittedWrites bool         `json:"project_has_uncommitted_writes"`
	Shape                       WrapShape    `json:"shape"`
}

// Snapshot is the on-disk format of <projectRoot>/.vibe-palace/last-tasks-snapshot.json.
// Each successful wrap rewrites the snapshot via StampIter so the next /wrap
// can compute task deltas as a set-difference between the snapshot and the
// live filesystem.
type Snapshot struct {
	IterN     int      `json:"iter_n"`
	AnchorSHA string   `json:"anchor_sha"`
	Active    []string `json:"active"`
	Done      []string `json:"done"`
	Cancelled []string `json:"cancelled"`
}

// iterNarrativeRe matches the H3 narrative header used in iterations.md
// (e.g., "### Iteration 168 — title (date)"). The capture group is the
// project-wide iteration number. The H3 (###) level is canonical and must
// not be relaxed to H2 (##).
var iterNarrativeRe = regexp.MustCompile(`(?m)^### Iteration (\d+)\b`)

// NextIterFromIterationsMD parses the iterations.md at the given path and
// returns max(### Iteration N) + 1. Returns 1 when the path is empty, the
// file is missing, or it contains no matching headers — the canonical
// "fresh project" signal.
func NextIterFromIterationsMD(iterationsPath string) (int, error) {
	if iterationsPath == "" {
		return 1, nil
	}
	data, err := os.ReadFile(iterationsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 1, nil
		}
		return 0, err
	}
	maxN := 0
	for _, m := range iterNarrativeRe.FindAllStringSubmatch(string(data), -1) {
		if len(m) < 2 {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err == nil && n > maxN {
			maxN = n
		}
	}
	return maxN + 1, nil
}

// ClassifyWrapShape returns the work-unit shape. Pure function: no I/O.
//
// Precedence: fresh-feature (commits since anchor) beats planning (tasks
// added) beats bookkeeping (no signals).
func ClassifyWrapShape(state Result) WrapShape {
	if len(state.CommitsSinceLastIter) > 0 {
		return ShapeFreshFeature
	}
	if len(state.TaskDeltas.Added) > 0 {
		return ShapePlanning
	}
	return ShapeBookkeeping
}

// ReadSnapshot reads <projectRoot>/.vibe-palace/last-tasks-snapshot.json. An
// absent file is treated as the empty snapshot (first-wrap condition); only
// true I/O / parse errors propagate.
func ReadSnapshot(projectRoot string) (Snapshot, error) {
	if projectRoot == "" {
		return Snapshot{}, nil
	}
	path := filepath.Join(projectRoot, AnchorDir, SnapshotFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Snapshot{}, nil
		}
		return Snapshot{}, fmt.Errorf("read tasks snapshot: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return Snapshot{}, nil
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("parse tasks snapshot: %w", err)
	}
	return snap, nil
}

// EnumerateLiveTasksFS walks tasksDir, tasksDir/done, and tasksDir/cancelled,
// returning the slug sets (filenames without `.md`) for each partition.
// Hidden files and non-`.md` entries are skipped. Missing partitions yield
// empty slices, not errors.
func EnumerateLiveTasksFS(tasksDir string) (active, done, cancelled []string, err error) {
	active, err = listSlugsIn(tasksDir)
	if err != nil {
		return nil, nil, nil, err
	}
	done, err = listSlugsIn(filepath.Join(tasksDir, "done"))
	if err != nil {
		return nil, nil, nil, err
	}
	cancelled, err = listSlugsIn(filepath.Join(tasksDir, "cancelled"))
	if err != nil {
		return nil, nil, nil, err
	}
	return active, done, cancelled, nil
}

// listSlugsIn returns the .md slugs (basename minus extension) directly inside
// dir. Sub-directories are not descended. Missing dir → empty slice + nil error.
func listSlugsIn(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		out = append(out, strings.TrimSuffix(name, ".md"))
	}
	return out, nil
}

// ComputeTaskDeltas implements the set-difference rules:
//
//   - added: live `tasks/<slug>.md` entries the snapshot does not mention in
//     any partition.
//   - retired: snapshot.Active slugs now sitting under live `tasks/done/`.
//   - cancelled: snapshot.Active slugs now sitting under live `tasks/cancelled/`.
//
// The snapshot is the prior wrap's filesystem state; the live arrays are this
// wrap's filesystem state.
func ComputeTaskDeltas(snapshot Snapshot, liveActive, liveDone, liveCancelled []string) TaskDeltas {
	known := make(map[string]struct{}, len(snapshot.Active)+len(snapshot.Done)+len(snapshot.Cancelled))
	for _, slug := range snapshot.Active {
		known[slug] = struct{}{}
	}
	for _, slug := range snapshot.Done {
		known[slug] = struct{}{}
	}
	for _, slug := range snapshot.Cancelled {
		known[slug] = struct{}{}
	}

	liveDoneSet := make(map[string]struct{}, len(liveDone))
	for _, slug := range liveDone {
		liveDoneSet[slug] = struct{}{}
	}
	liveCancelledSet := make(map[string]struct{}, len(liveCancelled))
	for _, slug := range liveCancelled {
		liveCancelledSet[slug] = struct{}{}
	}

	added := []string{}
	for _, slug := range liveActive {
		if _, ok := known[slug]; !ok {
			added = append(added, slug)
		}
	}
	retired := []string{}
	cancelled := []string{}
	for _, slug := range snapshot.Active {
		if _, ok := liveDoneSet[slug]; ok {
			retired = append(retired, slug)
			continue
		}
		if _, ok := liveCancelledSet[slug]; ok {
			cancelled = append(cancelled, slug)
		}
	}

	return TaskDeltas{Added: added, Retired: retired, Cancelled: cancelled}
}
