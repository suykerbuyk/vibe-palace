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

	"github.com/suykerbuyk/vibe-palace/internal/mdfence"
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

// CommitInfo summarizes a commit between an anchor and HEAD. Subject is the
// first line only (filled by CommitsSinceAnchor); Body is the FULL raw message
// including the subject (filled by CommitBodiesSinceAnchor). The two probes
// populate different fields deliberately: the subject-only line-split walk
// cannot carry a multi-line body, so the commit-log archive uses the
// record-separated body walk instead.
type CommitInfo struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Body    string `json:"body,omitempty"`
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

// IterationHeadingPrefix is the canonical markdown prefix for an iteration
// narrative header in iterations.md: H2, e.g. "## Iteration 191 — title".
//
// iterations.md is titled with a single H1 ("# <project> — Iteration
// Narratives"), so H2 is the natural level for a narrative and H3 is left free
// for the sub-sections narratives routinely carry ("### Phase 1 — ...",
// "### Results"). The file previously used H3 for BOTH, which put an iteration
// header at the same level as its own subsections.
const IterationHeadingPrefix = "## "

// iterHeadingRe matches an iteration narrative header at H2 (canonical) or H3
// (legacy). The capture groups are the hashes and the project-wide iteration
// number.
//
// The READER is deliberately tolerant of both levels, and that asymmetry is the
// whole point. NextIterFromIterationsMD's only output is a number: it has no
// channel on which to report "your heading is at the wrong level", so a strict
// matcher cannot fail loudly — it can only silently UNDER-COUNT, which is
// exactly the defect this tolerance exists to prevent. A strict-H3 matcher was
// blind to 110 H2 headings in vibe-palace (reporting iteration 188 at 190) and
// blind to every heading in rusty-can, whose narratives are entirely H2 — for
// which it reported 1, the "fresh project" signal, on a project with 18
// iterations of history. A wrap trusting that would have renumbered from
// scratch on top of real history.
//
// Strictness belongs in the WRITER, where a caller can actually see the error:
// AppendIterationOwned COMPOSES the canonical H2 header itself (via
// FormatIterationHeader) from the server-derived number and the caller's title,
// so nothing new can enter the file at the wrong level, while the reader stays
// unable to go blind on what is already there. ContainsIterationHeader guards
// the door so a narrative body cannot smuggle in a second header.
var iterHeadingRe = regexp.MustCompile(`^(#{2,3})[ \t]+Iteration (\d+)\b`)

// NextIterFromIterationsMD parses the iterations.md at the given path and
// returns max(Iteration N) + 1 over every iteration header, at either heading
// level. Returns 1 when the path is empty, the file is missing, or it contains
// no headers at all — the canonical "fresh project" signal.
//
// Fence-aware: a heading-shaped line inside a fenced code block is a comment or
// sample text, not a header, and must not move the iteration counter.
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
	for _, h := range scanIterHeadings(string(data)) {
		if h.n > maxN {
			maxN = h.n
		}
	}
	return maxN + 1, nil
}

// iterHeading is one iteration header found outside a code fence.
type iterHeading struct {
	line   int    // 1-indexed line number
	hashes string // "##" or "###"
	n      int    // the iteration number
	text   string // the trimmed header line
}

// scanIterHeadings returns every iteration header in content that lies outside a
// fenced code block: a heading-shaped line inside a code block is sample text,
// not a header, and must not move the iteration counter.
//
// Fence detection lives in internal/mdfence, which is the ONE definition of a
// markdown fence in this codebase. It is not "the line starts with ```" — that
// rule misreads an inline code run as an opening fence and swallows the rest of
// the document. See the mdfence package doc; iterations.md carries the line that
// proves it.
func scanIterHeadings(content string) []iterHeading {
	var out []iterHeading
	for _, l := range mdfence.OutsideFences(content) {
		// CommonMark: 4+ leading spaces is an indented code block, not a heading.
		// mdfence tracks backtick/tilde fences only; without this check a
		// "### Iteration N" quoted inside an indented sample becomes a false
		// positive (auditor inventing findings — worse than missing them).
		indent := 0
		for indent < len(l.Text) && l.Text[indent] == ' ' {
			indent++
		}
		if indent >= 4 {
			continue
		}
		trimmed := strings.TrimSpace(l.Text)
		m := iterHeadingRe.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		out = append(out, iterHeading{line: l.Num, hashes: m[1], n: n, text: trimmed})
	}
	return out
}

// FormatIterationHeader composes the canonical H2 iteration header line —
// "## Iteration N — title" — from a server-derived number and a caller title.
// It is the WRITER half of the contract iterHeadingRe reads: the server owns the
// header, so the file can never accumulate the wrong heading level again. Before
// this, vp_append_iteration accepted arbitrary markdown and the wrap template
// named no level, so the file grew both — 110 H2 against 81 H3 in vibe-palace
// alone, the same shape as the status-line defect fixed in iteration 184: a
// reader with a strict private definition, a writer with none.
// It also STRIPS a leading "Iteration N —" already present in the caller's
// title, via the one stripper in heading_contract.go. That is not defensive
// tidying — it closes the hole that WROTE the corruption. rezbldrvault carries
// five headings of the form "## Iteration 40 — Iteration 40 — …", and they got
// there because this function faithfully prefixed a title that already carried
// the prefix. The check and the one-shot migration cleaned those five up, but a
// repair whose source is still open regenerates its own work: the next caller
// that passes an already-prefixed title writes the sixth.
//
// Stripping here makes the round trip stop being a fixed point over that
// corruption — IsCanonicalHeader now returns false for an unmigrated doubled
// heading rather than reporting it as perfectly canonical. That is the intended
// consequence, not a regression: the oracle gained the sight it lacked, and
// leftover archive damage is now visible to the reader half as well as to the
// doubled-prefix rule that was written precisely because the oracle was blind.
func FormatIterationHeader(n int, title string) string {
	return fmt.Sprintf("%sIteration %d — %s", IterationHeadingPrefix, n, StripDoubledPrefix(title))
}

// ContainsIterationHeader reports whether content carries any iteration header
// (at H2 or H3, outside a code fence). AppendIterationOwned composes the header
// itself, so a narrative body that also carries one would double it; the tool
// handler rejects such a body at the door.
func ContainsIterationHeader(content string) bool {
	return len(scanIterHeadings(content)) > 0
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
