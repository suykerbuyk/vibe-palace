// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// CollectInput carries the resolved filesystem paths Collect needs. The
// tools layer resolves vault-relative paths via (*storage.Vault) helpers and
// hands them in so this package stays decoupled from internal/storage.
type CollectInput struct {
	VaultRoot      string // vault root (for the scoped vault-dirty probe)
	Project        string // project slug (scopes the vault-dirty probe)
	IterationsPath string // absolute path to the vault iterations.md
	TasksDir       string // absolute path to the vault tasks/ dir
	ProjectRoot    string // local project repo root (anchors, git, doc/TESTING.md)
}

// testCountsUnitRe matches the unit-count token in doc/TESTING.md's headline.
// Tolerant of an optional `**` markdown bold wrapper, an optional `~`
// approximation prefix, and the singular/plural `test|tests` suffix. Capture
// group 1 is the integer count.
var testCountsUnitRe = regexp.MustCompile(`\*{0,2}~?(\d+)\s+tests?\*{0,2}`)

// testCountsIntegrationRe matches the integration-test token, anchored on the
// literal word `integration` to avoid confusion with the unit count. Capture
// group 1 is the integer count.
var testCountsIntegrationRe = regexp.MustCompile(`\*{0,2}~?(\d+)\s+integration\s+tests?\*{0,2}`)

// testCountsFromTestingMD parses doc/TESTING.md's headline counts. On a
// missing file or unparseable content it returns zeros and a non-empty
// warning string — never a Go error, since stale counts are advisory and must
// not fail the wrap. `lint` is not in the headline and stays at 0.
func testCountsFromTestingMD(testingMDPath string) (unit, integration, lint int, warning string) {
	if testingMDPath == "" {
		return 0, 0, 0, "doc/TESTING.md path empty"
	}
	data, err := os.ReadFile(testingMDPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, 0, 0, "doc/TESTING.md missing"
		}
		return 0, 0, 0, fmt.Sprintf("read doc/TESTING.md: %v", err)
	}

	body := string(data)
	mu := testCountsUnitRe.FindStringSubmatch(body)
	if len(mu) < 2 {
		return 0, 0, 0, "doc/TESTING.md headline unit-count not found"
	}
	u, err := strconv.Atoi(mu[1])
	if err != nil {
		return 0, 0, 0, fmt.Sprintf("parse unit count: %v", err)
	}

	var i int
	mi := testCountsIntegrationRe.FindStringSubmatch(body)
	if len(mi) >= 2 {
		i, err = strconv.Atoi(mi[1])
		if err != nil {
			return u, 0, 0, fmt.Sprintf("parse integration count: %v", err)
		}
	}

	return u, i, 0, ""
}

// Collect assembles the full wrap-state record. Pure orchestration: every
// input is computed by a helper, and the resulting struct is the JSON shape
// returned by vp_collect_wrap_state.
func Collect(ctx context.Context, in CollectInput) (Result, error) {
	res := Result{}

	n, err := NextIterFromIterationsMD(in.IterationsPath)
	if err != nil {
		return res, fmt.Errorf("next iter from iterations.md: %w", err)
	}
	res.IterN = n

	res.Branch = DetectBranch(in.ProjectRoot)

	anchorSHA, err := LastIterAnchorSha(in.ProjectRoot)
	if err != nil {
		return res, fmt.Errorf("last iter anchor sha: %w", err)
	}
	res.LastIterAnchorSha = anchorSHA

	// commits/files since anchor — fall back to oldest root commit when no
	// anchor exists (first wrap, project never stamped).
	scanFrom := anchorSHA
	if scanFrom == "" {
		root, rerr := OldestRootCommit(ctx, in.ProjectRoot)
		if rerr != nil {
			return res, fmt.Errorf("oldest root commit: %w", rerr)
		}
		scanFrom = root
	}
	commits, err := CommitsSinceAnchor(ctx, in.ProjectRoot, scanFrom)
	if err != nil {
		return res, fmt.Errorf("commits since anchor: %w", err)
	}
	if commits == nil {
		commits = []CommitInfo{}
	}
	res.CommitsSinceLastIter = commits

	files, err := FilesChangedSinceAnchor(ctx, in.ProjectRoot, scanFrom)
	if err != nil {
		return res, fmt.Errorf("files changed since anchor: %w", err)
	}
	if files == nil {
		files = []string{}
	}
	res.FilesChanged = files

	// task_deltas — set-difference of last-tasks-snapshot.json against the
	// live tasks/ tree. Bootstraps gracefully when the snapshot is absent.
	snapshot, err := ReadSnapshot(in.ProjectRoot)
	if err != nil {
		return res, fmt.Errorf("read last tasks snapshot: %w", err)
	}
	active, done, cancelled, err := EnumerateLiveTasksFS(in.TasksDir)
	if err != nil {
		return res, fmt.Errorf("enumerate live tasks: %w", err)
	}
	res.TaskDeltas = ComputeTaskDeltas(snapshot, active, done, cancelled)

	// test counts — best-effort parse of doc/TESTING.md headline.
	u, i, l, warn := testCountsFromTestingMD(filepath.Join(in.ProjectRoot, "doc", "TESTING.md"))
	res.TestCounts = TestCounts{Unit: u, Integration: i, Lint: l, Warning: warn}

	// vault/project dirty flags. The vault probe is scoped to
	// Projects/<project>/ so a sibling project's uncommitted writes do not
	// falsely trip the vault-dirty flag for this project. Dirt is split by
	// category: VaultHasUncommittedWrites carries only NON-memory writes (the
	// nag-worthy signal), while memory writes — committed automatically by the
	// SessionEnd harvest and /wrap's vault sync — are surfaced separately and
	// are not a blocker.
	nonMemoryDirty, memoryDirty, err := VaultDirtyByCategory(in.VaultRoot, in.Project)
	if err != nil {
		return res, fmt.Errorf("vault git status: %w", err)
	}
	res.VaultHasUncommittedWrites = nonMemoryDirty
	res.MemoryHasUncommittedWrites = memoryDirty

	projectDirty, err := ProjectHasUncommittedWrites(in.ProjectRoot)
	if err != nil {
		return res, fmt.Errorf("project git status: %w", err)
	}
	res.ProjectHasUncommittedWrites = projectDirty

	// shape: classifier runs on the fully-populated state.
	res.Shape = ClassifyWrapShape(res)

	return res, nil
}
