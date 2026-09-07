// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/mdfence"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// TaskMeta is the lightweight metadata for task listing.
//
// Parent and Depends are the relationship fields. Both are OPTIONAL and both
// carry omitempty for a reason that is load-bearing, not cosmetic: every task
// file written before they existed has neither, and TaskMeta is marshalled into
// vp_list_tasks, vp_get_task and vp tasks --json. Without omitempty, every
// legacy task would grow a "parent":"" and a "depends":null in every one of
// those payloads. With it, they stay byte-identical and absence means exactly
// what it says: standalone.
//
// There is deliberately NO Children field and NO IsEpic flag. TaskMeta is
// per-file truth, and whether a task has children is a fact about the whole set.
// An "epic" is DERIVED in internal/taskgraph as "a task something points at".
type TaskMeta struct {
	Slug     string   `json:"slug"`
	Title    string   `json:"title"`
	Status   string   `json:"status"`
	Priority string   `json:"priority"`
	Done     bool     `json:"done"`
	Parent   string   `json:"parent,omitempty"`
	Depends  []string `json:"depends,omitempty"`
}

// ConventionalFirstHeading is the H2 heading CreateTask emits between the
// header block and the body, pinned here as ONE constant so the writer and any
// future check cannot drift apart about what "the conventional first heading"
// is.
//
// # Why it exists
//
// Everything between the header block and the first H2 is the PREAMBLE, and no
// typed action could revise it: create writes it once at birth, amend is keyed
// on an H2 heading and cannot reach above the first one, and set_meta /
// update_status / set_relations each own a single header field. A task whose
// body had been fully corrected still opened with its original framing, and an
// agent reading top-down met the superseded claim first.
//
// Emitting this heading unconditionally makes the first prose in a task file
// amend-addressable, which is the mechanism half of the 2026-07-27 ruling. The
// discipline half is the rule it encodes:
//
//	The preamble is PROVENANCE ONLY — filing date, source, the commit or task
//	it came out of. Anything asserting a state of the world is a CLAIM and
//	belongs under this heading or a later one, where amend can revise it.
//	Immutability is fine for provenance and wrong for premises.
//
// Do not put a task's thesis above this heading. There is no writer that can
// take it back.
//
// A whole-file revision — a preamble repair, an H2 rename, a migration — goes
// through vp_manage_task action=overwrite (Vault.OverwriteTaskFile), which is
// the only sanctioned typed path to text amend cannot address.
const ConventionalFirstHeading = "Context"

// validStatuses is the WRITE set for UpdateTaskStatus — and ONLY the write set.
//
// The terminal values ("completed", "retired", "cancelled") are deliberately
// absent. A task leaves the active set by MOVING (RetireTask → done/,
// CancelTask → cancelled/), and moveTask writes "retired"/"cancelled" straight
// through replaceStatusLine without ever consulting this map. Letting
// UpdateTaskStatus stamp a terminal status in place produced a task file that
// claimed to be finished while still sitting in the active directory — visible
// to nobody, moved by nothing. Removing them from the write set is friction on
// the shortest didn't-notice path to that state; it is not a claim that a task
// cannot be marked done by other means (vp_vault_edit and a text editor both
// still can).
//
// This is NOT a read whitelist and must never become one. Every archived file
// on disk carries "**Status:** retired" or "**Status:** cancelled" — 96 of them
// today. A read-side check built on this map would declare the entire archive
// invalid. parseTaskMeta reads whatever the file says, on purpose.
//
// Honest scope: 0 of the 114 task files on disk use "completed". This closes a
// door almost nobody walks through. It is worth closing, and it is not coverage.
//
// "icebox" is a non-terminal status and belongs here: it means KNOWN, NOT DOING
// SOON. It is not "done" and it is not "cancelled" — the work is still real and
// the file stays in tasks/ with its whole history, one call away from coming
// back. It exists because a backlog that holds everything the project KNOWS is a
// knowledge base, not a backlog: 15 of 28 open tasks were found-in-passing items
// nobody intended to schedule, sitting in the same list as the critical ones,
// and that is what made the list unreadable. Readers hide it by default (vp
// tasks, vp_bootstrap_context) and must say how many they hid — an icebox that
// is silently invisible is just a deletion with extra steps.
var validStatuses = map[string]bool{
	"pending":     true,
	"in_progress": true,
	"blocked":     true,
	"icebox":      true,
}

// StatusIcebox marks a task as known but not scheduled. Readers that show
// intent (rather than knowledge) filter it out by default.
const StatusIcebox = "icebox"

// StatusInProgress marks work already under way. Readers that order by what to
// do NEXT put it ahead of everything not yet started.
const StatusInProgress = "in_progress"

// StatusRetired and StatusCancelled are the two TERMINAL status values. They are
// deliberately absent from validStatuses — UpdateTaskStatus must not stamp them
// in place, because a task reaches a terminal state by MOVING (see moveTask).
//
// They exist as constants so terminality has exactly ONE definition. Before this
// they were bare literals inside RetireTask and CancelTask, which meant any
// reader wanting to ask "is this status terminal?" had to hardcode a second copy
// — and a detector whose copy drifts from the writer's stops seeing the very
// disagreement it exists to report.
const (
	StatusRetired   = "retired"
	StatusCancelled = "cancelled"
)

// IsTerminalStatus reports whether a Status VALUE names an archived state.
//
// 🔴 CASE-INSENSITIVE ON THE VALUE, DELIBERATELY, and that is not the same
// looseness iteration 347 ruled against. There the case-folded thing was a
// shout-TOKEN scanned across free prose, so folding it turned BLOCKED into the
// English word "blocked" and fired on ordinary sentences. Here the input is a
// single header FIELD VALUE that has already been isolated by a case-SENSITIVE
// key match, and the real corpus spells its values inconsistently — "In Progress"
// and "in_progress" both appear on disk. Folding the value reads legacy files
// correctly; folding the key would be the 347 defect.
func IsTerminalStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case StatusRetired, StatusCancelled:
		return true
	}
	return false
}

// TaskStatusValue returns the value of a task **Status:** line, and ok=false for
// any other line.
//
// It is the narrow export of headerFieldValue — narrow on purpose. A caller
// outside this package needs to ask "is this line the status, and what does it
// say?" without owning a second definition of what a metadata line looks like:
// headerFieldValue is THE definition for the parser, the writers and the
// validator, and a detector matching a near-copy is how a reader and a writer
// come to disagree about which lines are metadata. Exporting the whole
// field-agnostic helper would invite exactly that drift for the other three
// fields, so this exposes the one field with an outside reader.
//
// The key match is case-SENSITIVE and anchored, like every other use.
func TaskStatusValue(line string) (string, bool) {
	return headerFieldValue(line, fieldStatus)
}

// DropIcebox removes tasks that are known but deliberately unscheduled.
//
// ONE definition, reached by every reader that shows INTENT — vp tasks,
// vp_list_tasks, and vp_bootstrap_context's active_task_count and head-of-queue
// derivation. Callers each writing their own two-line filter is how they end up
// disagreeing about what "active" means.
func DropIcebox(tasks []TaskMeta) []TaskMeta {
	out := make([]TaskMeta, 0, len(tasks))
	for _, t := range tasks {
		if t.Status != StatusIcebox {
			out = append(out, t)
		}
	}
	return out
}

// TaskSpec is the input to CreateTask. It is a struct rather than a positional
// argument list because the list was already five strings long, and Parent and
// Depends would have made it seven — the point at which a caller silently
// transposing two of them stops being hypothetical.
type TaskSpec struct {
	Slug     string
	Title    string
	Content  string
	Priority string
	Parent   string
	Depends  []string
}

// TaskRelations is the input to SetTaskRelations. Each field is TRI-STATE:
// nil leaves the relation unchanged, and a non-nil empty value CLEARS it. A
// plain string/slice could not express "leave alone" and "clear" as different
// requests, so a caller updating only Depends would have silently unparented
// the task. Same idiom as getTaskParams.IncludeContent.
type TaskRelations struct {
	Parent  *string
	Depends *[]string
}

// normalizeRelations validates a task's relations LEXICALLY and canonicalizes
// them. It deliberately does NOT check that the referenced tasks exist.
//
// Existence is a cross-file truth and belongs to internal/taskgraph, not to a
// per-file writer holding a per-path lock. Enforcing it here would also force a
// creation ORDER — a child could never be written before its epic — and would
// turn a typo, which the graph reports as dangling and keeps working around,
// into a hard write failure.
func normalizeRelations(self, parent string, depends []string) (string, []string, error) {
	if parent != "" {
		if err := slug.Validate(parent); err != nil {
			return "", nil, fmt.Errorf("invalid parent: %w", err)
		}
		if parent == self {
			return "", nil, fmt.Errorf("task %q cannot be its own parent", self)
		}
	}

	var out []string
	seen := make(map[string]bool)
	for _, d := range depends {
		d = strings.TrimSpace(d)
		if d == "" || seen[d] {
			continue
		}
		if err := slug.Validate(d); err != nil {
			return "", nil, fmt.Errorf("invalid depends entry: %w", err)
		}
		if d == self {
			return "", nil, fmt.Errorf("task %q cannot depend on itself", self)
		}
		seen[d] = true
		out = append(out, d)
	}
	return parent, out, nil
}

// upsertHeaderField sets a metadata field's value, replacing the first such line
// inside the header block or appending one at the end of the block if absent.
//
// Like replaceStatusLine it splices into the line slice rather than
// concatenating strings, so the file's trailing-newline shape survives exactly.
func upsertHeaderField(content, field, value string) string {
	lines := strings.Split(content, "\n")
	start, end := headerBlock(lines)
	rendered := "**" + field + ":** " + value

	for i := start; i < end; i++ {
		if _, ok := headerFieldValue(lines[i], field); ok {
			lines[i] = rendered
			return strings.Join(lines, "\n")
		}
	}
	lines = slices.Insert(lines, end, rendered)
	return strings.Join(lines, "\n")
}

// removeHeaderField deletes the first occurrence of a metadata field from the
// header block. Absent is not an error — clearing an unset relation is a no-op.
func removeHeaderField(content, field string) string {
	lines := strings.Split(content, "\n")
	start, end := headerBlock(lines)
	for i := start; i < end; i++ {
		if _, ok := headerFieldValue(lines[i], field); ok {
			lines = slices.Delete(lines, i, i+1)
			return strings.Join(lines, "\n")
		}
	}
	return content
}

// SetTaskRelations sets or clears the Parent and Depends relations of an ACTIVE
// task, in place.
//
// Same shape as UpdateTaskStatus: the per-path lock is held across the
// read→rewrite so a concurrent relation edit cannot clobber this one, and the
// write goes through atomicfile.Write directly — NEVER lockedWrite, which
// re-acquires this same lock, and vaultlock.Acquire is a blocking LOCK_EX with
// no LOCK_NB and no timeout, so re-entry is a permanent self-deadlock rather
// than an error.
func (v *Vault) SetTaskRelations(project, taskSlug string, rel TaskRelations) error {
	if rel.Parent == nil && rel.Depends == nil {
		return fmt.Errorf("set relations for %q: nothing to set (both parent and depends are unspecified)", taskSlug)
	}

	path, err := v.TaskFile(project, taskSlug)
	if err != nil {
		return err
	}

	var parent string
	var depends []string
	if rel.Parent != nil {
		parent = *rel.Parent
	}
	if rel.Depends != nil {
		depends = *rel.Depends
	}
	parent, depends, err = normalizeRelations(taskSlug, parent, depends)
	if err != nil {
		return err
	}

	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock task: %w", err)
	}
	defer release()

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read task: %w", err)
	}

	updated := string(data)
	if rel.Parent != nil {
		if parent == "" {
			updated = removeHeaderField(updated, fieldParent)
		} else {
			updated = upsertHeaderField(updated, fieldParent, parent)
		}
	}
	if rel.Depends != nil {
		if len(depends) == 0 {
			updated = removeHeaderField(updated, fieldDepends)
		} else {
			updated = upsertHeaderField(updated, fieldDepends, formatDependsList(depends))
		}
	}

	return atomicfile.Write(v.Root, path, []byte(updated))
}

// TaskMetaEdit is the input to SetTaskMeta. Both fields are TRI-STATE for the
// same reason TaskRelations is: nil means "leave it alone". Unlike relations,
// neither can be CLEARED — a task with no title or no priority is not a state
// worth being able to reach — so an empty string is rejected rather than treated
// as a clear.
type TaskMetaEdit struct {
	Title    *string
	Priority *string
}

// SetTaskMeta changes an ACTIVE task's TITLE and/or PRIORITY in place.
//
// Why this exists: Title and Priority were the two header fields with NO WRITER.
// CreateTask stamped them once and nothing could ever change them —
// UpdateTaskStatus owns Status, SetTaskRelations owns Parent/Depends, AmendTask
// owns the body sections, and the remaining two fields belonged to nobody.
//
// That is not academic. A task's TITLE is what vp_list_tasks and
// vp_bootstrap_context hand to every agent at session start, so a title that
// states a premise later disproved keeps reaching every future session as the
// headline while the correction sits in a body section the agent may never read.
// (Live specimen, 205: search-index-and-embed-cache-have-no-eviction-path was
// titled "...a full Rebuild does NOT fix a stale vector, because Rebuild trusts
// the same by-ID cache" — a claim the review disproved from source.) And a
// backlog whose priorities can never be revised is a backlog that records what
// someone guessed at creation time, not what the project currently believes.
//
// THREE WRITERS, DISJOINT FIELDS. Status stays with UpdateTaskStatus, which owns
// real terminal-state semantics; edges stay with SetTaskRelations. Nothing here
// can set a status or an edge, so there is never a second way to write a field —
// which is what keeps the reader and the writer from disagreeing about which
// value is real.
//
// Locking is the same shape as its siblings: the per-path lock spans the
// read→rewrite, and the write goes through atomicfile.Write(v.Root, ...) directly
// — never lockedWrite, which re-acquires this same lock and would self-deadlock.
func (v *Vault) SetTaskMeta(project, taskSlug string, edit TaskMetaEdit) error {
	if edit.Title == nil && edit.Priority == nil {
		return fmt.Errorf("set meta for %q: nothing to set (both title and priority are unspecified)", taskSlug)
	}

	var title, priority string
	if edit.Title != nil {
		title = strings.TrimSpace(*edit.Title)
		if title == "" {
			return fmt.Errorf("set meta for %q: title cannot be empty — a task with no title is not a reachable state", taskSlug)
		}
		if strings.ContainsAny(title, "\r\n") {
			return fmt.Errorf("set meta for %q: title must be a single line", taskSlug)
		}
	}
	if edit.Priority != nil {
		priority = strings.TrimSpace(*edit.Priority)
		if !validPriorities[priority] {
			return fmt.Errorf(
				"set meta for %q: invalid priority %q — must be low, medium, high, or critical",
				taskSlug, priority)
		}
	}

	path, err := v.TaskFile(project, taskSlug)
	if err != nil {
		return err
	}

	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock task: %w", err)
	}
	defer release()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("task %q not found in project %q (set_meta works on ACTIVE tasks only)", taskSlug, project)
		}
		return fmt.Errorf("read task: %w", err)
	}

	updated := string(data)
	if edit.Title != nil {
		updated = replaceTitleLine(updated, title)
	}
	if edit.Priority != nil {
		updated = upsertHeaderField(updated, fieldPriority, priority)
	}

	return atomicfile.Write(v.Root, path, []byte(updated))
}

// validPriorities is the write set for SetTaskMeta.
//
// CreateTask deliberately does NOT consult this — it accepts whatever it is
// given, and the corpus proves why: real task files on disk carry "P0"/"P1"
// priorities from an older convention. A read-side whitelist would declare them
// invalid; a create-side one would be a behaviour change nobody asked for. This
// constrains only the NEW writer, where there is no legacy to honour.
var validPriorities = map[string]bool{
	"low":      true,
	"medium":   true,
	"high":     true,
	"critical": true,
}

// replaceTitleLine rewrites the first H1 line. If the file has none, one is
// inserted at the top — the same not-silently-dropping-the-update posture as
// replaceStatusLine, and for the same reason: a caller that explicitly set a
// title and got no title is a caller that was lied to.
//
// It splices into the line slice rather than concatenating strings, so the
// file's trailing-newline shape survives exactly.
//
// FENCE-UNAWARE, AND SAFE — but only because of an invariant, so state it: this
// is a whole-file first-wins scan, and a fenced shell comment ("# rebuild the
// index") is H1-shaped. It cannot win the race because CreateTask ALWAYS writes
// "# Title" as line 1 and validateTaskBody REFUSES an unfenced H1 in any body, so
// the real title is always the first H1 in the file. This is the same reasoning
// that makes parseTaskMeta's whole-file Status/Priority scan safe (204) — and the
// same reasoning that made it UNSAFE for the OPTIONAL Parent/Depends fields,
// which have no such guaranteed first match. Do not copy this pattern to a field
// that CreateTask does not always write.
func replaceTitleLine(content, title string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if isH1Line(line) {
			lines[i] = "# " + title
			return strings.Join(lines, "\n")
		}
	}
	lines = slices.Insert(lines, 0, "# "+title, "")
	return strings.Join(lines, "\n")
}

// AmendTask replaces (or appends) one H2 SECTION of an ACTIVE task's body, in
// place. It is the only writer that can change a task's PLAN.
//
// Why this exists: for most of this project's life vp_manage_task could change a
// task's STATUS and its EDGES but not its BODY. So a decision, a review finding,
// or a reversal — the outputs of /vpc-review-plan, and of every architecture
// conversation — had nowhere to land. workflow.md said task files are "mutated
// ONLY via vp_manage_task" while the API made obeying that impossible, so an
// agent with an amendment to record either hand-edited the file (breaking the
// rule) or dropped the amendment on the floor when the session ended. A rule the
// API cannot satisfy is not a rule, it is prose.
//
// SECTION-KEYED, NOT APPEND-ONLY, and that is the whole design. An append-only
// amend is not idempotent: re-running it — after a crash, a retry, a re-read of
// the same instruction — silently duplicates the section, and the task then
// carries two "## Decision" blocks that disagree. Keying on the heading makes a
// repeated amend converge instead of accumulate. (Capture learned this at 199 and
// the reasoning transfers exactly.)
//
// The header block is unreachable from here BY CONSTRUCTION, not by checking:
// sections start at an H2, and headerBlock() ends before the first one. Title,
// Status, Priority, Parent and Depends stay owned by CreateTask,
// UpdateTaskStatus and SetTaskRelations respectively. validateTaskBody is still
// applied to the incoming body as defense in depth, so a caller cannot smuggle a
// "**Status:**" line into a section and produce a file whose reader and writer
// disagree about which status is real.
//
// Locking is the same shape as UpdateTaskStatus and SetTaskRelations: the
// per-path lock is held across the read→rewrite so a concurrent amend of a
// DIFFERENT section cannot clobber this one, and the write goes through
// atomicfile.Write(v.Root, ...) directly — NEVER lockedWrite, which re-acquires
// this same lock, and vaultlock.Acquire is a blocking LOCK_EX with no LOCK_NB
// and no timeout, so re-entry is a permanent self-deadlock rather than an error.
// Passing v.Root is what stamps the surface; a bare atomicfile.Write would
// silently skip it.
//
// Active tasks only. A retired task's plan is history, and history is append-only
// (iterations.md), not amendable.
func (v *Vault) AmendTask(project, taskSlug, section, body string) (op string, err error) {
	section = strings.TrimSpace(section)
	if section == "" {
		return "", fmt.Errorf("amend %q: section is required — it names the H2 heading to replace or append", taskSlug)
	}
	if strings.ContainsAny(section, "\r\n") {
		return "", fmt.Errorf("amend %q: section must be a single line, got %q", taskSlug, section)
	}
	if strings.HasPrefix(section, "#") {
		return "", fmt.Errorf(
			"amend %q: section is the heading TEXT, not the markup — pass %q, not %q",
			taskSlug, strings.TrimLeft(strings.TrimSpace(section), "# "), section)
	}
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("amend %q: body is required — an empty section records nothing", taskSlug)
	}
	if err := validateTaskBody(body); err != nil {
		return "", err
	}
	if err := validateAmendBody(body); err != nil {
		return "", err
	}

	path, err := v.TaskFile(project, taskSlug)
	if err != nil {
		return "", err
	}

	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return "", fmt.Errorf("lock task: %w", err)
	}
	defer release()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("task %q not found in project %q (amend works on ACTIVE tasks only)", taskSlug, project)
		}
		return "", fmt.Errorf("read task: %w", err)
	}

	next, op := upsertSection(string(data), section, body)
	if err := atomicfile.Write(v.Root, path, []byte(next)); err != nil {
		return "", err
	}
	return op, nil
}

// validateAmendBody refuses an H2 inside an amend body.
//
// The section heading is supplied by the `section` parameter and rendered here.
// A second H2 inside the body would START A NEW SECTION, and the next amend of
// the same section would then replace only up to that inner H2 — silently
// orphaning the remainder as a section nothing owns. Rejecting it is what keeps
// the replace idempotent. Sub-headings are H3+.
func validateAmendBody(body string) error {
	for _, l := range mdfence.OutsideFences(body) {
		if trimmed := strings.TrimSpace(l.Text); isH2Line(trimmed) {
			return fmt.Errorf(
				"amend body line %d is an H2 heading (%q): the section heading is supplied by the "+
					"`section` parameter, and a second H2 inside the body would split the section so a "+
					"later amend could not replace it whole — use H3 (###) for sub-headings",
				l.Num, trimmed)
		}
	}
	return nil
}

// sectionBounds locates an H2 section by heading text, FENCE-AWARE.
//
// Fence-awareness is not decoration. Task bodies quote markdown at each other
// constantly — this very project's task files contain fenced examples of task
// headers — so a naive scan would match a "## Decision" that is sample text
// inside a code block and splice a replacement into the middle of a fence. The
// 191 fence bug and the 204 header-parser bug were both this mistake; mdfence
// exists so it is made once.
//
// Returns the half-open line range [start, end) covering the heading and its
// body, where end is the next H1 or H2 outside a fence, or EOF. An H3 does not
// terminate a section.
func sectionBounds(content, section string) (start, end int, found bool) {
	lines := strings.Split(content, "\n")
	outside := make([]bool, len(lines))
	for _, l := range mdfence.OutsideFences(content) {
		if i := l.Num - 1; i >= 0 && i < len(outside) {
			outside[i] = true
		}
	}

	want := "## " + section
	start = -1
	for i, line := range lines {
		if outside[i] && strings.TrimSpace(line) == want {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, 0, false
	}

	end = len(lines)
	for i := start + 1; i < len(lines); i++ {
		if !outside[i] {
			continue
		}
		if trimmed := strings.TrimSpace(lines[i]); isH1Line(trimmed) || isH2Line(trimmed) {
			end = i
			break
		}
	}
	return start, end, true
}

// upsertSection replaces the named H2 section, or appends it if absent.
//
// Unlike upsertHeaderField, this normalizes the file to exactly one trailing
// newline. A body edit already rewrites whole lines, and CreateTask writes that
// shape anyway, so converging on it is predictable rather than lossy.
const (
	// AmendAppended is the op when amend added a new H2 because the heading
	// was absent. AmendReplaced is the op when it overwrote an existing one.
	// Convergence is still the design — reporting which branch ran is what
	// makes a section-name collision visible instead of silent.
	AmendAppended = "appended"
	AmendReplaced = "replaced"
)

func upsertSection(content, section, body string) (result, op string) {
	rendered := "## " + section + "\n\n" + strings.TrimRight(body, "\n")

	start, end, found := sectionBounds(content, section)
	if !found {
		base := strings.TrimRight(content, "\n")
		if base == "" {
			return rendered + "\n", AmendAppended
		}
		return base + "\n\n" + rendered + "\n", AmendAppended
	}

	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	out = append(out, lines[:start]...)
	out = append(out, strings.Split(rendered, "\n")...)
	if end < len(lines) {
		out = append(out, "") // blank line before the section that follows
		out = append(out, lines[end:]...)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n", AmendReplaced
}

// CreateTask creates a new task markdown file in the project's tasks directory.
// It is a hard error if the task already exists.
//
// The existence check runs INSIDE the per-path vaultlock: checking outside it is
// a TOCTOU — two concurrent creates of the same slug would both pass the stat and
// one would silently overwrite the other, breaking the already-exists contract.
//
// The write therefore goes through atomicfile.Write directly rather than
// lockedWrite: lockedWrite re-acquires this same lock, and vaultlock.Acquire is a
// blocking LOCK_EX with no LOCK_NB and no timeout, so the re-entry would be a
// permanent self-deadlock rather than an error. Same shape as
// (*Vault).WriteResume and UpdateTaskStatus.
func (v *Vault) CreateTask(project string, spec TaskSpec) error {
	slug, title, content, priority := spec.Slug, spec.Title, spec.Content, spec.Priority

	path, err := v.TaskFile(project, slug)
	if err != nil {
		return err
	}

	parent, depends, err := normalizeRelations(slug, spec.Parent, spec.Depends)
	if err != nil {
		return err
	}

	// Validate the caller-supplied body BEFORE any directory or file is
	// touched. This lives here, not in the MCP handler, as defense in depth:
	// the storage layer is the chokepoint every caller must pass through, so a
	// future second caller cannot reintroduce an unvalidated body by forgetting
	// to repeat the check. (vp_manage_task create is currently the only
	// production caller; vp_carried_promote_to_task was the second until it was
	// deleted.)
	if err := validateTaskBody(content); err != nil {
		return err
	}

	tasksDir, err := v.TasksDir(project)
	if err != nil {
		return err
	}
	if err := EnsureDir(tasksDir); err != nil {
		return fmt.Errorf("ensure tasks dir: %w", err)
	}

	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock task: %w", err)
	}
	defer release()

	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("task %q already exists in project %q", slug, project)
	}

	// A retired task is still a task. Guard the done/ and cancelled/ directories
	// too, mirroring GetTask's three-dir search idiom: creating a slug that was
	// already retired or cancelled would otherwise silently write a duplicate
	// active file, and the subsequent retire/cancel would clobber the historical
	// completion record. Refuse loudly and name the state so the operator can
	// choose a new slug (reopen is a deliberate, separate action — never folded
	// into create).
	for _, loc := range []struct {
		dir   func(string) (string, error)
		state string
	}{
		{v.TaskDoneDir, "done"},
		{v.TaskCancelledDir, "cancelled"},
	} {
		dir, err := loc.dir(project)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(dir, slug+".md")); err == nil {
			return fmt.Errorf("task %q already exists in tasks/%s/ (a retired task is still a task; choose a new slug, or reopen the existing one)", slug, loc.state)
		}
	}

	var buf strings.Builder
	fmt.Fprintf(&buf, "# %s\n\n", title)
	fmt.Fprintf(&buf, "**Status:** pending\n")
	fmt.Fprintf(&buf, "**Priority:** %s\n", priority)
	if parent != "" {
		fmt.Fprintf(&buf, "**Parent:** %s\n", parent)
	}
	if len(depends) > 0 {
		fmt.Fprintf(&buf, "**Depends:** %s\n", formatDependsList(depends))
	}
	// The conventional first H2, emitted UNCONDITIONALLY — including when
	// content already opens with its own H2. See ConventionalFirstHeading: the
	// point is that the region above the first heading is provenance-only and
	// addressable-by-amend prose starts here, and a rule that applies only when
	// the author forgot a heading is not a rule the reader can rely on. An
	// author whose content opens with its own H2 gets an empty Context section,
	// which is the cost of the guarantee and is deliberate.
	//
	// 🔴 THAT COST HAS A BOUND NOW, AND THE SENTENCE ABOVE NO LONGER STATES THE
	// WHOLE RULE. An empty section is still what a DIFFERENTLY named first H2
	// buys, and it is still deliberate. But a body carrying an H2 named
	// ConventionalFirstHeading itself bought something else entirely: two
	// sections under ONE amend key, with sectionBounds resolving the first — so
	// the author's prose was unreachable by that name for the life of the file,
	// and an amend on it would overwrite the emitted empty section while
	// reporting success. That shape is no longer a cost paid here; it is refused
	// at the door by validateTaskBody's conventional-heading arm, which runs
	// before any of this. The emit below stays unconditional, which is what
	// keeps the guarantee a guarantee.
	fmt.Fprintf(&buf, "\n## %s\n", ConventionalFirstHeading)
	if content != "" {
		buf.WriteString("\n")
		buf.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			buf.WriteByte('\n')
		}
	}

	return atomicfile.Write(v.Root, path, []byte(buf.String()))
}

// resolveTaskFile searches the active, done, and cancelled directories for a
// task's markdown file and returns the first match: its absolute path and
// whether it was found in an archive dir (done/ or cancelled/). It is the shared
// three-directory search behind GetTask and TaskFilePath — the ONE place that
// knows the search order, so the readers and the whole-file writer cannot drift
// apart about where a task lives.
//
// Unlike TaskFile (paths.go), which computes the ACTIVE path unconditionally
// without touching the filesystem, this resolves against what is actually on
// disk and errors if the slug is nowhere.
func (v *Vault) resolveTaskFile(project, slug string) (path string, done bool, err error) {
	if err := validateSlugs(project, slug); err != nil {
		return "", false, err
	}

	type location struct {
		dir  func(string) (string, error)
		done bool
	}
	locations := []location{
		{v.TasksDir, false},
		{v.TaskDoneDir, true},
		{v.TaskCancelledDir, true},
	}

	for _, loc := range locations {
		dir, err := loc.dir(project)
		if err != nil {
			return "", false, err
		}
		p := filepath.Join(dir, slug+".md")
		info, err := os.Stat(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", false, fmt.Errorf("stat task: %w", err)
		}
		if info.IsDir() {
			continue
		}
		return p, loc.done, nil
	}

	return "", false, fmt.Errorf("task %q not found in project %q", slug, project)
}

// GetTask reads a task file and returns its metadata and full content.
// It searches active, done, and cancelled directories.
func (v *Vault) GetTask(project, slug string) (TaskMeta, string, error) {
	path, done, err := v.resolveTaskFile(project, slug)
	if err != nil {
		return TaskMeta{}, "", err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return TaskMeta{}, "", fmt.Errorf("read task: %w", err)
	}

	meta := parseTaskMeta(slug, string(data), done)
	return meta, string(data), nil
}

// ListTasks returns metadata for all tasks in a project. If includeDone is
// true, also includes tasks from the done/ and cancelled/ subdirectories.
func (v *Vault) ListTasks(project string, includeDone bool) ([]TaskMeta, error) {
	tasksDir, err := v.TasksDir(project)
	if err != nil {
		return nil, err
	}

	var result []TaskMeta

	// Active tasks.
	active, err := globTaskMeta(tasksDir, false)
	if err != nil {
		return nil, err
	}
	result = append(result, active...)

	if includeDone {
		doneDir, err := v.TaskDoneDir(project)
		if err != nil {
			return nil, err
		}
		done, err := globTaskMeta(doneDir, true)
		if err != nil {
			return nil, err
		}
		result = append(result, done...)

		cancelDir, err := v.TaskCancelledDir(project)
		if err != nil {
			return nil, err
		}
		cancelled, err := globTaskMeta(cancelDir, true)
		if err != nil {
			return nil, err
		}
		result = append(result, cancelled...)
	}

	return result, nil
}

// UpdateTaskStatus updates the status line of an ACTIVE task file in place.
//
// It accepts only the non-terminal statuses (see validStatuses). A task reaches
// a terminal state by being MOVED — RetireTask or CancelTask — never by being
// stamped in place, because a "completed" file left in tasks/ is a task that
// reads as finished and behaves as active.
func (v *Vault) UpdateTaskStatus(project, slug, status string) error {
	if !validStatuses[status] {
		return fmt.Errorf(
			"invalid status %q: UpdateTaskStatus writes only pending, in_progress, or blocked — "+
				"a task reaches a terminal state by being moved (RetireTask/CancelTask), not stamped in place",
			status)
	}

	path, err := v.TaskFile(project, slug)
	if err != nil {
		return err
	}

	// Hold the per-path lock across the read→rewrite (RMW): a concurrent
	// status update of the same task must not clobber this one.
	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock task: %w", err)
	}
	defer release()

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read task: %w", err)
	}

	updated := replaceStatusLine(string(data), status)
	return atomicfile.Write(v.Root, path, []byte(updated))
}

// RetireTask moves a task to the done/ directory with status "retired".
func (v *Vault) RetireTask(project, slug string) error {
	return v.moveTask(project, slug, v.TaskDoneDir, StatusRetired)
}

// CancelTask moves a task to the cancelled/ directory with status "cancelled".
func (v *Vault) CancelTask(project, slug string) error {
	return v.moveTask(project, slug, v.TaskCancelledDir, StatusCancelled)
}

// moveTask updates a task's status and moves it to a destination directory.
func (v *Vault) moveTask(project, slug string, destFn func(string) (string, error), status string) error {
	srcPath, err := v.TaskFile(project, slug)
	if err != nil {
		return err
	}

	if _, err := os.Stat(srcPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("task %q not found (may already be %s)", slug, status)
		}
		return err
	}

	destDir, err := destFn(project)
	if err != nil {
		return err
	}
	if err := EnsureDir(destDir); err != nil {
		return fmt.Errorf("ensure dest dir: %w", err)
	}

	destPath := filepath.Join(destDir, slug+".md")

	// 🔴 REWRITE-THEN-RENAME. The status line is stamped IN PLACE at the active
	// path, and only then is the file renamed into the archive directory. The
	// rename is the operation's atomic commit point, because the content is
	// already final before the file moves. Adopted 2026-09-01; this replaced a
	// write-destination-then-unlink-source ordering.
	//
	// 🔴 RENAME-THEN-REWRITE REMAINS REJECTED, and that is a different ordering
	// from this one. Renaming first would put the file in done/ and only then
	// stamp it, so a crash between the steps leaves an archived file still
	// declaring itself in progress — which is the defect
	// `retired-task-files-keep-a-live-status-line` is about, manufactured by
	// design. The earlier comment here rejected rename-then-rewrite for exactly
	// that reason and was right to; it simply never considered the opposite
	// order. Do not "simplify" this into vaultfs.Move: Move is a bare rename and
	// cannot stamp the status line at all.
	//
	// The three orderings, by what a crash leaves behind:
	//
	//   write-dest, unlink-source  two copies; resolveTaskFile searches active
	//   (the previous order)       first, so the task reads as NOT retired even
	//                              though a correct done/ copy exists — the
	//                              retire silently appears not to have happened.
	//   rename, then rewrite       one copy in done/ declaring itself active.
	//   (rejected, stays so)       Undetectable from inside the file.
	//   rewrite, then rename       one copy in tasks/ carrying a terminal
	//   (adopted)                  status — an obviously mid-retire state, and
	//                              repairable by completing the rename.
	//
	// Never two copies, and the crash signature is now something a rule can
	// state: an ACTIVE task whose body says retired/cancelled cannot have come
	// from a sanctioned writer, because UpdateTaskStatus refuses terminal values.
	//
	// 🔴 THE REFUSE-EXISTING-DESTINATION CHECK MUST STAY AHEAD OF THE REWRITE.
	// Under the previous order the stat merely had to precede the destination
	// write. Here it has to precede the stamp, because the stamp mutates the
	// SOURCE: refusing after it would leave an active task carrying a terminal
	// status — the same broken state a crash produces, reached through an
	// ordinary refusal. TestRetireRefusalLeavesSourceBodyUnstamped pins this.
	//
	// 🔴 ONE LOCK, SO THERE IS NO ORDER TO INVERT (ADR-003, "sequential locks,
	// never nested"). The source path's lock is held across read → stamp →
	// rename, and no second lock is taken: the destination is created by the
	// rename itself, which is atomic and has no partial-content window for a
	// lock to protect. The previous order needed two locks — the destination's
	// inside lockedWrite, then the source's around the unlink — and carried a
	// comment forbidding anyone from hoisting the second across the first. That
	// hazard is gone rather than managed: a function that takes one lock cannot
	// invert an order.
	//
	// This also CLOSES the race the previous comment recorded as deliberately
	// left open: the read now happens under the same lock as the write, so a
	// concurrent UpdateTaskStatus can no longer land between them and be lost.
	// It could not be closed before without creating the forbidden nesting.
	//
	// Inside the lock, write with atomicfile.Write and NEVER lockedWrite:
	// lockedWrite re-acquires the same per-path lock and vaultlock.Acquire is a
	// blocking LOCK_EX with no timeout, so re-entry is a permanent self-deadlock
	// rather than an error (ADR-003, "never lockedWrite under a held lock").
	// vaultfs.RenameNoLock is the F2 sink and likewise never acquires, which is
	// what lets the lock live here at the call site.
	release, err := vaultlock.Acquire(v.Root, srcPath)
	if err != nil {
		return fmt.Errorf("move task: lock %s: %w", srcPath, err)
	}
	defer release()

	// Never overwrite an existing destination. A re-retire of a duplicate slug
	// would otherwise clobber the historical done/ (or cancelled/) record —
	// os.Rename replaces its destination silently, so this stat is the only
	// thing standing between a duplicate slug and a destroyed record. Surface it
	// as a bug state rather than lose the prior record.
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("cannot move task %q to %q: a task of that slug already exists there — refusing to overwrite the existing record", slug, destPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat dest: %w", err)
	}

	data, err := os.ReadFile(srcPath)
	if err != nil {
		// The pre-lock stat found the file; losing it here means a concurrent
		// archive of the same slug won the lock. Report it as the same
		// already-archived state that stat reports, not as a read fault.
		if os.IsNotExist(err) {
			return fmt.Errorf("task %q not found (may already be %s)", slug, status)
		}
		return fmt.Errorf("read task: %w", err)
	}
	updated := replaceStatusLine(string(data), status)

	// Step 1 — stamp the terminal status while the file is still active.
	// atomicfile.Write is atomic per file, so this either lands whole or leaves
	// the source untouched; there is no half-stamped body.
	if err := atomicfile.Write(v.Root, srcPath, []byte(updated)); err != nil {
		return fmt.Errorf("stamp %s status on %q: %w", status, slug, err)
	}

	// Step 2 — the commit. A crash before this leaves an active task carrying a
	// terminal status, which is recoverable by completing this rename.
	if err := vaultfs.RenameNoLock(srcPath, destPath); err != nil {
		return fmt.Errorf(
			"archive task %q: status was stamped %s but the move to %s failed, so the task is still in the active directory: %w",
			slug, status, destPath, err)
	}
	return nil
}

// MoveTaskToProject relocates an ACTIVE task file out of one project's tasks/
// directory and into another's, changing nothing inside the file.
//
// 🔴 IT IS A BARE RENAME AND STAYS ONE. It either performs a correct relocation
// or it refuses. It writes NO provenance, NO tombstone and NO header field;
// every case it cannot perform honestly is a refusal that names what to do
// instead. The provenance a completed move records at both ends is composed by
// MoveProvenance and SEQUENCED by the vp_manage_task `move` arm, after this
// returns — see the ordering note there. Do not fold those writes in here: a
// content write inside this function would give the rename something to order
// against, and the only orders available are the two `moveTask`'s own 🔴 comment
// rejects.
//
// 🔴 IT NEVER WRITES parent OR depends_on. SetTaskRelations is the sole writer
// for both, and a second writer for one field is how a reader and a writer come
// to disagree about which value is real. So an edge that would DANGLE in the
// destination is REFUSED rather than rewritten — which is also what keeps this
// operation a single rename with no content write to order against it.
//
// The source is Vault.TaskFile's path, which is the ACTIVE path unconditionally
// and never consults the archive. A done/ or cancelled/ task is therefore
// refused BY CONSTRUCTION rather than by a separate rule: an archived body is
// the record of what happened, and relocating it would move that record out
// from under the project it happened in.
//
// 🔴 ONE LOCK, ON THE SOURCE, SO THERE IS NO ORDER TO INVERT (ADR-003,
// "sequential locks, never nested"). It is held across read → check → rename,
// and NO destination lock is taken: the destination is created by the rename
// itself, which is atomic and has no partial-content window for a lock to
// protect. vaultlock.Acquire is a blocking LOCK_EX with no LOCK_NB and no
// timeout, so an inverted order would be a PERMANENT HANG rather than a
// detectable error — and a function that takes one lock cannot invert one. This
// is moveTask's discipline, copied deliberately; see the long block there.
// Inside the lock, never call lockedWrite — it re-acquires this same per-path
// lock and self-deadlocks — and nothing here writes content anyway.
//
// 🔴 THE REFUSE-EXISTING-DESTINATION STAT IS POLICY HERE, AND STAYS AHEAD OF THE
// RENAME. vaultfs.RenameNoLock is a bare os.Rename, which replaces its
// destination SILENTLY (raw.go says so, and says the rule belongs to each
// caller); the stat is the only thing standing between a duplicate slug and a
// destroyed task in the destination project.
//
// It does not go through vaultfs.Move, and that is not an oversight: Move
// REFUSES both endpoints via IsTaskFilePath, because a task's location is a
// field with typed writers. This IS one of those typed writers, so it works in
// absolute paths against the F2 sink directly — the same shape moveTask uses
// for its own rename. A second rename-policy implementation is one too many.
//
// Nothing here stamps .surface. A rename writes no content, which is the F2
// sink's documented behaviour and the same choice moveTask makes.
func (v *Vault) MoveTaskToProject(fromProject, slug, toProject string) error {
	if err := validateSlugs(fromProject, slug, toProject); err != nil {
		return err
	}
	if fromProject == toProject {
		return fmt.Errorf(
			"move task %q: source and destination project are both %q — there is nothing to move",
			slug, fromProject)
	}

	// ACTIVE path only. See the archived note above.
	srcPath, err := v.TaskFile(fromProject, slug)
	if err != nil {
		return err
	}
	if _, err := os.Stat(srcPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat task: %w", err)
		}
		// Say WHY it is not there when the answer is knowable: an archived slug
		// resolves in done/ or cancelled/, and "not found" would send the caller
		// hunting for a file that is sitting right there.
		if archived, _, rerr := v.resolveTaskFile(fromProject, slug); rerr == nil {
			return fmt.Errorf(
				"cannot move task %q out of project %q: it is archived at %s, and an archived body is the "+
					"record of what happened in THAT project — moving it would relocate the record. "+
					"Only ACTIVE tasks move between projects",
				slug, fromProject, archived)
		}
		return fmt.Errorf("task %q not found in the active tasks of project %q", slug, fromProject)
	}

	destDir, err := v.TasksDir(toProject)
	if err != nil {
		return err
	}
	destPath := filepath.Join(destDir, slug+".md")

	release, err := vaultlock.Acquire(v.Root, srcPath)
	if err != nil {
		return fmt.Errorf("move task: lock %s: %w", srcPath, err)
	}
	defer release()

	data, err := os.ReadFile(srcPath)
	if err != nil {
		// The pre-lock stat found the file; losing it here means a concurrent
		// archive or move of the same slug won the lock.
		if os.IsNotExist(err) {
			return fmt.Errorf("task %q not found in the active tasks of project %q", slug, fromProject)
		}
		return fmt.Errorf("read task: %w", err)
	}

	// The edge check reads the source under the same lock that holds the rename,
	// so a concurrent SetTaskRelations cannot land between the check and the
	// move. It only ever REFUSES; nothing is written back.
	if missing := v.danglingTaskEdges(toProject, parseTaskMeta(slug, string(data), false)); len(missing) > 0 {
		return fmt.Errorf(
			"cannot move task %q from project %q to %q: %s, and project %q has no task of that slug — "+
				"the edge would dangle the moment the file landed.\n"+
				"This action NEVER writes parent or depends_on: set_relations is the sole writer for both, "+
				"and a second writer for one field is how a reader and a writer come to disagree about which "+
				"value is real.\n"+
				"Fix the edge first with vp_manage_task action=set_relations — clear it, or re-point it at a "+
				"task that does live in %q — or move the counterpart task across as well, then re-run the move",
			slug, fromProject, toProject, strings.Join(missing, "; "), toProject, toProject)
	}

	// Only now may a directory be created: a refusal above must leave the
	// destination project's tree exactly as it found it.
	if err := EnsureDir(destDir); err != nil {
		return fmt.Errorf("ensure dest dir: %w", err)
	}

	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf(
			"cannot move task %q into project %q: %s already exists — refusing to overwrite the task that is "+
				"already there. Rename or archive one of the two, then re-run the move",
			slug, toProject, destPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat dest: %w", err)
	}

	// The commit point. It is the only mutation this function performs, and it
	// is atomic, so there is no half-moved state for a crash to leave behind.
	if err := vaultfs.RenameNoLock(srcPath, destPath); err != nil {
		return fmt.Errorf(
			"move task %q from project %q to %q failed, so the task is still in %s: %w",
			slug, fromProject, toProject, fromProject, err)
	}
	return nil
}

// danglingTaskEdges describes the parent and depends_on entries of meta that do
// NOT resolve to a task in project. An empty result means every edge the task
// carries would still point at something.
//
// 🔴 RESOLUTION IS ACTIVE + ARCHIVED — resolveTaskFile's three directories —
// because that is the set every other reader of an edge already uses:
// taskgraph.BuildFromVault builds from ListTasks(project, true), and
// vp_manage_task's own schema states that "a dependency on a retired or
// cancelled task counts as SATISFIED". An active-only rule here would refuse a
// move whose counterpart is merely finished, which is not a dangling edge by
// anyone else's definition — and a second definition of "resolves" is the same
// two-readers-disagreeing defect as a second writer.
func (v *Vault) danglingTaskEdges(project string, meta TaskMeta) []string {
	var missing []string
	if meta.Parent != "" {
		if _, _, err := v.resolveTaskFile(project, meta.Parent); err != nil {
			missing = append(missing, fmt.Sprintf("its parent is %q", meta.Parent))
		}
	}
	for _, dep := range meta.Depends {
		if _, _, err := v.resolveTaskFile(project, dep); err != nil {
			missing = append(missing, fmt.Sprintf("it depends on %q", dep))
		}
	}
	return missing
}

// MoveProvenance is the record a completed cross-project move leaves at BOTH
// ends: a section appended to the moved task in the DESTINATION project, and a
// tombstone task filed in the SOURCE project's cancelled/ directory.
//
// 🔴 IT IS A COMPOSER, NOT A WRITER. Nothing here touches the filesystem, and
// MoveTaskToProject does not call it. The move stays a bare rename with no
// content write to order against it, and the SEQUENCE — rename, then
// destination provenance, then source tombstone — is assembled by the
// vp_manage_task `move` arm out of ordinary typed calls (AmendTask, CreateTask,
// CancelTask), each of which takes and releases its OWN lock, one at a time.
// That is what keeps this operation inside ADR-003's sequential-locks rule: no
// lock here is ever held while another is acquired, and vaultlock.Acquire is a
// blocking LOCK_EX with no timeout, so an inverted order would be a permanent
// hang rather than a detectable error.
//
// Putting the PROSE here rather than in the handler is deliberate: what a move
// records is a property of the operation, and a second copy of these words
// living beside a second caller is how two callers come to write two different
// histories for one event.
type MoveProvenance struct {
	// FromProject is where the task WAS. ToProject is where it now is.
	FromProject string
	ToProject   string
	// Slug is the task's slug, which the move does not change: it is free in
	// the source project precisely because the rename removed the file, which
	// is what lets the tombstone take it.
	Slug string
	// Day is the calendar day the writer stamped, via CalendarDay. The WRITER
	// owns the clock (ADR-006); no caller supplies a date.
	Day string
	// Commit is the vault commit the move was made against — the vault's HEAD
	// as it stood when the provenance was composed. It is EMPTY when the vault
	// is not a git repository or git could not answer, and the rendered prose
	// then says so rather than inventing a value. A provenance note that
	// fabricates a commit is worse than one that admits it has none.
	Commit string
}

// NewMoveProvenance composes the record for one move. `now` is a parameter so a
// test can pin an instant and so one logical operation cannot disagree with
// itself by reading the clock twice.
//
// The HEAD read is best-effort and never fails the move: by the time this runs
// the rename has already happened, so an unavailable git is a missing field in a
// note, not a reason to refuse work that is already done.
func (v *Vault) NewMoveProvenance(fromProject, toProject, taskSlug string, now time.Time) MoveProvenance {
	head, err := gitCmd(v.Root, 10*time.Second, "rev-parse", "HEAD")
	if err != nil {
		head = ""
	}
	return MoveProvenance{
		FromProject: fromProject,
		ToProject:   toProject,
		Slug:        taskSlug,
		Day:         v.CalendarDay(now),
		Commit:      head,
	}
}

// DestinationHeading is the H2 heading text of the section appended to the moved
// task in the destination project.
//
// 🔴 IT NAMES AN ORIGIN, NEVER A STATUS, AND THAT IS FORCED BY HOW AMEND WORKS.
// AmendTask is KEYED on the heading text and cannot revise it, so a heading is
// written once and is effectively permanent. "Moved from <source-project>" is a
// fact about an event that happened on a date: nothing that occurs later can
// make it untrue. A heading that asserted a STATE instead — "Relocated",
// "Awaiting re-triage", "Now owned by X" — would be equally permanent and would
// go stale the moment the state changed, leaving a heading the file's own body
// contradicts and no typed action able to fix it (overwrite is the only reach,
// and reaching for a whole-file rewrite to correct a heading is the cost this
// avoids).
//
// A task moved A → B → C therefore accumulates TWO sections, "Moved from A" and
// "Moved from B". That is a history, not a duplicate, and is the intended
// behaviour.
//
// The one case that CONVERGES rather than accumulates is a return trip: A → B →
// A → B re-amends "Moved from A" and replaces the earlier one. That is amend's
// keyed idempotence doing exactly its job, and the surviving section still
// describes the move that put the file where it is — so the file never asserts
// anything false; it simply does not keep the date of a superseded move from the
// same origin. The source-side tombstones keep that trail.
func (p MoveProvenance) DestinationHeading() string {
	return "Moved from " + p.FromProject
}

// DestinationBody is the section body appended to the moved task. It records the
// source project, the day, and the commit the move was made against.
//
// It carries no H2 of its own — the heading comes from DestinationHeading via
// amend's `section` parameter, and a second H2 inside the body would split the
// section so a later amend could not replace it whole (validateAmendBody refuses
// one). It carries no header field lines either, so it cannot smuggle a second
// writer for Status, Parent or Depends past validateTaskBody.
func (p MoveProvenance) DestinationBody() string {
	var b strings.Builder
	fmt.Fprintf(&b, "This task was moved out of project `%s` and into project `%s` on %s%s.\n",
		p.FromProject, p.ToProject, p.Day, p.commitClause())
	b.WriteString("\n")
	fmt.Fprintf(&b, "The move relocated the file and changed nothing inside it; this section is the only "+
		"thing the move added. A tombstone recording the same relocation was filed in the source project at "+
		"`Projects/%s/tasks/cancelled/%s.md`, so a reader who follows a stale reference into `%s` is told "+
		"where the work went instead of finding nothing.\n",
		p.FromProject, p.Slug, p.FromProject)
	return b.String()
}

// TombstoneSpec is the task filed in the SOURCE project to record where the task
// went. The arm creates it and then cancels it, which is what lands it at
// Projects/<source>/tasks/cancelled/<slug>.md — the directory this project
// already treats as the home of "why something is not here".
//
// The title is PROVENANCE for the same reason the destination heading is: "Moved
// to <destination>" is an event, and set_meta is the only thing that could ever
// revise it. It deliberately does NOT copy the live task's title, which belongs
// to the live task and can be changed there without this record becoming wrong.
//
// The spec carries NO parent and NO depends. A tombstone is a record, not a node
// of the plan, and giving it edges would make the source project's graph assert
// structure that moved away. Its VALUE to the graph is the opposite and is
// passive: because resolveTaskFile searches cancelled/ too, a task still left
// behind in the source project naming this slug as a parent or a dependency
// resolves to this record rather than dangling.
//
// The body is prose a reader can act on, not padding to clear CreateTask's
// content floor — it names the destination project, the destination path, the
// day, the commit, and what a reader should do instead of touching this file.
func (p MoveProvenance) TombstoneSpec() TaskSpec {
	var b strings.Builder
	fmt.Fprintf(&b, "This task is no longer in `%s`. On %s it was moved to project `%s`%s, and its file "+
		"now lives at `Projects/%s/tasks/%s.md`, which is where its plan, its status and its edges are "+
		"maintained from now on.\n",
		p.FromProject, p.Day, p.ToProject, p.commitClause(), p.ToProject, p.Slug)
	b.WriteString("\n")
	fmt.Fprintf(&b, "This file is a tombstone, not the task. It exists so that a reader who follows a slug, "+
		"a link or a stale reference into `%s` finds out WHERE the work went instead of finding nothing, and "+
		"so that anything still naming `%s` as a parent or a dependency resolves to a record rather than "+
		"dangling. Do not amend it and do not reopen it: amend, retire and cancel belong to the live task in "+
		"`%s`.\n",
		p.FromProject, p.Slug, p.ToProject)
	return TaskSpec{
		Slug:     p.Slug,
		Title:    "Moved to " + p.ToProject,
		Content:  b.String(),
		Priority: "medium",
	}
}

// commitClause renders the against-which-commit half of a provenance sentence,
// or says plainly that there is none. It never invents a value.
func (p MoveProvenance) commitClause() string {
	if p.Commit == "" {
		return " (the vault is not a git repository, so there is no commit to record it against)"
	}
	return ", against vault commit " + p.Commit
}

// The four header field names. A task's metadata header is a contiguous run of
// "**Field:** value" lines, and these are the only fields in it.
const (
	fieldStatus   = "Status"
	fieldPriority = "Priority"
	fieldParent   = "Parent"
	fieldDepends  = "Depends"
)

// headerFieldValue is THE definition of a metadata line for the whole package:
// the parser (parseTaskMeta), the writers (replaceStatusLine, upsertHeaderField)
// and the validator (validateTaskBody) all reach it, directly or through the
// predicates below, so they cannot drift apart and start disagreeing about which
// lines are metadata.
//
// Deliberately line-oriented and leading-whitespace tolerant — the same shape
// the markdown these files carry has always used. A "**Status:**" mid-sentence
// is not a status line to anyone here, by construction.
func headerFieldValue(line, field string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	prefix := "**" + field + ":**"
	if !strings.HasPrefix(trimmed, prefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, prefix)), true
}

// isHeaderFieldLine reports whether a line is any of the four metadata lines.
// It is what bounds the header block; see headerBlock.
func isHeaderFieldLine(line string) bool {
	for _, f := range []string{fieldStatus, fieldPriority, fieldParent, fieldDepends} {
		if _, ok := headerFieldValue(line, f); ok {
			return true
		}
	}
	return false
}

// isStatusLine reports whether a line is a task **Status:** line.
func isStatusLine(line string) bool {
	_, ok := headerFieldValue(line, fieldStatus)
	return ok
}

// isPriorityLine reports whether a line is a task **Priority:** line.
func isPriorityLine(line string) bool {
	_, ok := headerFieldValue(line, fieldPriority)
	return ok
}

// isParentLine reports whether a line is a task **Parent:** line.
func isParentLine(line string) bool {
	_, ok := headerFieldValue(line, fieldParent)
	return ok
}

// isDependsLine reports whether a line is a task **Depends:** line.
func isDependsLine(line string) bool {
	_, ok := headerFieldValue(line, fieldDepends)
	return ok
}

// isH1Line reports whether a line is a markdown H1 ("# " at line start).
func isH1Line(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "# ")
}

// isH2Line reports whether a line opens an H2 section. The trailing space in the
// prefix is load-bearing: it makes "### sub" an H3 rather than an H2 with a
// stray hash, which is what lets a section carry sub-headings without any of
// them terminating it.
func isH2Line(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "## ")
}

// headerBlock returns the [start, end) line range of the contiguous metadata
// block: the run of "**Field:**" lines following the H1 (or, with no H1, the run
// at the top of the file). An empty block yields start == end.
//
// 🔴 THIS EXISTS BECAUSE PARENT AND DEPENDS ARE *OPTIONAL*, AND THAT BREAKS THE
// INVARIANT THE WHOLE-FILE PARSE RESTED ON.
//
// parseTaskMeta scans the entire file, first-wins, fence-UNAWARE. That is safe
// for Status and Priority only because CreateTask always writes them in the
// header — so the header match is always reached before any body line that looks
// like one. An OPTIONAL field has no such guaranteed header match to win the
// race, so a whole-file scan would happily read a task whose BODY discusses
// "**Depends:** a, b" — including inside a fenced code block — as a task that
// really has those dependencies. The task file that introduced this feature does
// exactly that, which is how the trap was found.
//
// Fence-awareness is NOT the fix and would be weaker: an un-fenced "## Schema"
// section containing a depends line is still prose, not metadata, and mdfence
// would happily hand it over. Block-scoping is strictly stronger, and it costs
// no fence state machine on the ListTasks hot path.
func headerBlock(lines []string) (start, end int) {
	at := 0
	for i, line := range lines {
		if isH1Line(line) {
			at = i + 1
			break
		}
	}
	for at < len(lines) && strings.TrimSpace(lines[at]) == "" {
		at++
	}
	start = at
	for at < len(lines) && isHeaderFieldLine(lines[at]) {
		at++
	}
	return start, at
}

// validateTaskBody rejects a caller-supplied task body that carries its own
// metadata header, or the one H2 heading CreateTask writes for itself.
//
// CreateTask writes "# Title", "**Status:**" and "**Priority:**" itself. A body
// that repeats them yields a file with two H1s and two status lines — and since
// the writer (replaceStatusLine) rewrites the FIRST status line, the file then
// reads back with a status nobody ever wrote. Rejecting the duplicate at the
// door is the only way the two can stay in agreement.
//
// This is the ONE place that must be fence-aware. Task plan bodies routinely
// carry shell/TOML/Python snippets, where "# Usage: ..." is a comment, not a
// heading — rejecting those would hard-fail create on a very common and
// entirely legitimate body shape. Inside a fence, nothing is metadata. Outside
// one, an H1 or a status line can only be a duplicated header block.
//
// Fence detection lives in internal/mdfence and MUST NOT be reimplemented here.
// It was, once, as "the trimmed line starts with ``` or ~~~" — and that rule
// FAILED OPEN. A body line whose first non-space characters are an inline code
// run (```bash tutorial``` ...) was misread as an opening fence, so everything
// after it was treated as fenced and skipped, and a duplicate "**Status:**"
// walked straight past the check this function exists to perform — silently
// reinstating the corruption iteration 184 closed. Proven by test before the
// fix, not argued. See the mdfence package doc.
//
// An unterminated fence simply means the rest of the body is treated as fenced.
// A task body is not required to be well-formed markdown, and over-rejecting is
// precisely the failure mode being avoided here.
//
// parseTaskMeta and replaceStatusLine stay deliberately fence-UNAWARE: CreateTask
// writes the real header at the top of the file, the body follows it, and both
// are first-wins — so the genuine header is always found before any fence in the
// body can be reached.
// Parent and Depends are rejected here too, and that DIVERGES from Priority,
// which is not — deliberately. A stray "**Priority:**" in a body is harmless:
// the real one is in the header and first-wins finds it first. A stray relation
// line is not harmless. Relations are optional, so a body-borne one is the only
// candidate the parser could find — a SILENT RELATIONSHIP nobody wrote. The
// header block (see headerBlock) already prevents the parser from reading it;
// this closes the shortest door as well, so the two defences are independent.
//
// The FIFTH arm extends the same rule from the header block to the one HEADING
// the writer owns — see isConventionalFirstHeadingLine for why it is here, why
// it refuses rather than adapts, and why it matches exactly.
func validateTaskBody(content string) error {
	const remedy = "strip the leading metadata block from content: create supplies the \"# Title\" heading and the \"**Status:**\", \"**Priority:**\", \"**Parent:**\" and \"**Depends:**\" lines itself"
	const headingRemedy = "create writes that heading itself, immediately above your content — rename this one, move its prose beneath the emitted heading, or use \"###\" if it was meant as a sub-heading"
	for _, l := range mdfence.OutsideFences(content) {
		trimmed := strings.TrimSpace(l.Text)
		switch {
		case isStatusLine(trimmed):
			return fmt.Errorf("task content line %d is a status line (%q): %s", l.Num, trimmed, remedy)
		case isParentLine(trimmed):
			return fmt.Errorf("task content line %d is a parent line (%q): %s", l.Num, trimmed, remedy)
		case isDependsLine(trimmed):
			return fmt.Errorf("task content line %d is a depends line (%q): %s", l.Num, trimmed, remedy)
		case isH1Line(trimmed):
			return fmt.Errorf("task content line %d is an H1 heading (%q): %s", l.Num, trimmed, remedy)
		case isConventionalFirstHeadingLine(trimmed):
			return fmt.Errorf("task content line %d repeats the conventional first heading (%q), and a "+
				"file with two %q sections strands the second from amend, which resolves the first: %s",
				l.Num, trimmed, ConventionalFirstHeading, headingRemedy)
		}
	}
	return nil
}

// isConventionalFirstHeadingLine reports whether a body line is an H2 whose text
// is exactly ConventionalFirstHeading — the heading CreateTask emits for itself.
//
// # Why refusing is the fix, and not skipping the emit
//
// The alternative that looks cheapest is to detect the collision and suppress
// the unconditional emit at CreateTask. It is not free: it reintroduces exactly
// the conditional the emit's own comment argues against. The guarantee's whole
// value is that it holds without the reader checking — "a rule that applies only
// when the author forgot a heading is not a rule the reader can rely on" — and
// making the emit depend on author input converts the guarantee into a
// convention. validateWholeTaskFile's zero-H2 refusal is built on that guarantee
// holding unconditionally.
//
// Splicing the author's same-named section under the emitted heading fails the
// same way from the other side: `create` would become a section-merging writer,
// which it is not, and it must detect the same-named section to do it. Both
// alternatives are also indistinguishable in the resulting FILE — for a
// colliding body each produces one heading with the author's prose beneath it —
// so no assertion about the artifact can tell them apart or pin them.
//
// Refusing is the only shape that leaves the emit statement unconditional, and
// it deletes the collision at the one moment it is free to fix: before the file
// exists. That matters because repair does not scale here. A collision is a
// two-line diff only while the file has no history; once amended it needs a
// whole-file rewrite, and once archived it is reachable only by a migrate-family
// caller built for it.
//
// # ANYWHERE, not just the first H2
//
// The collision does not require the body to OPEN with the heading. A body
// shaped `## Design … ## Context` produces the same two-sections-one-key file,
// and the live corpus carries a specimen whose occurrences sit at lines 7 and 11
// with a non-empty first section. A first-H2 predicate would miss it.
//
// # EXACT match, and this deliberately DIVERGES from refuseLegacySectionCollision
//
// That sibling (further down this file) folds case. This one must not, and the
// reason is that a refusal has to be keyed on the same equality as the resolver
// it protects. sectionBounds matches `"## " + section` with `==` — case
// SENSITIVE — so `## context` and `## Context` are two distinct amend keys and
// strand nothing. Folding case here would refuse a body that produces exactly
// the documented, accepted outcome (an empty Context section beside a
// differently-keyed one), and over-rejection at create is paid by the author.
//
// 🔴 THE COUPLING IS PINNED BY TEST, NOT BY THIS PARAGRAPH. Two definitions of
// "same heading" that agree only on today's data are still two definitions;
// TestConventionalHeadingRefusalMatchesSectionBoundsEquality drives both this
// predicate and sectionBounds over the same inputs so they cannot drift.
//
// # PREVENTION ONLY — the files that already collide are deliberately left
//
// This rule stops new collisions and repairs none of the existing ones. That is
// a decision, not an omission, and it is NOT symmetric with the sibling ruling on
// archived task files whose "**Status:**" line disagreed with their directory.
//
// The difference is what the stale artifact still DOES. There, the wrong value
// was actively re-exported — `vp tasks --json` and every status-keyed reader kept
// handing a "pending" archived task to callers — so leaving it was shipping a
// false answer on every read. Here the second heading is INERT: amend is refused
// on archived tasks outright, so the destroy-the-prose hazard cannot fire on
// them, and the duplicate section is fully readable to a human. Nothing reads it
// wrong; it is only unreachable to one writer that is itself refused.
//
// 🔴 THAT INERTNESS ARGUMENT COVERS THE ARCHIVED FILES ONLY, and the distinction
// is worth keeping straight. An ACTIVE instance is amendable, so the hazard is
// live for it — an amend keyed on the conventional name resolves the first of the
// two sections, and what that costs depends on whether the first one holds prose.
// When this shipped the class held exactly one active file, in ANOTHER project,
// outside this task's reach; it is enumerated as a known out-of-scope instance in
// internal/vaultaudit/dimensions.go's no-dimension ruling rather than left to look
// overlooked. Do not infer from this paragraph that every remaining instance is
// harmless — infer that the archived ones are, and re-derive the active set.
//
// So do not "finish this by symmetry" with a repair migration. If one is ever
// wanted, the case has to be made on its own facts.
//
// Re-derive the population fence-aware, never fence-blind and never cited: walk
// Projects/*/tasks{,/done,/cancelled}/*.md, collect outside-fence H2 texts via
// mdfence.OutsideFences, and report files where one text repeats. A fence-blind
// grep overstates it — the task that filed this defect quotes the colliding pair
// inside a fence and a blind scan reports the file as an instance of itself.
//
// # It is reached from AmendTask too, and that is harmless
//
// validateTaskBody guards both CreateTask and AmendTask. An amend body carrying
// any H2 at all is already refused by validateAmendBody, so this arm changes no
// amend outcome — only which message a body carrying this particular H2 gets,
// which is why the remedy text above also names the "###" fix.
func isConventionalFirstHeadingLine(trimmed string) bool {
	return trimmed == "## "+ConventionalFirstHeading
}

// validateWholeTaskFile validates a COMPLETE task file — header and body
// together — the shape OverwriteTaskFile is about to persist over an existing
// task. It is the exact INVERSE of validateTaskBody: that function rejects a
// header-less create/amend body that carries ANY metadata; this one REQUIRES a
// well-formed metadata header and rejects a file that is missing it or has it
// twice. Do not confuse the two, and do not call one from the other.
//
// It is fence-aware for the same reason validateTaskBody is: a whole task file
// routinely carries shell/TOML/Python snippets whose "# Usage" comment or sample
// "**Status:**" line is prose, not structure. Only lines OUTSIDE fenced code
// blocks count. Fence detection is mdfence's, never a local reimplementation.
//
// Enforced, counting only lines outside fences:
//   - balanced code fences (an unterminated ``` or ~~~ run is an error — checked
//     first, because an open fence swallows the trailing header and would
//     otherwise masquerade as a "missing field"),
//   - exactly one "# " H1 title line,
//   - at least one "## " H2 heading (no upper bound — many sections is normal;
//     only ZERO is the defect). amend is keyed on an exact "## " heading match,
//     so a body with no H2 has no addressable section and every word of its
//     prose is unreachable to every later write. CreateTask guarantees the
//     heading at birth (ConventionalFirstHeading, emitted unconditionally); this
//     check is what keeps a whole-file write from undoing that guarantee,
//   - exactly one "**Status:**" line and exactly one "**Priority:**" line,
//   - those Status and Priority lines lie inside the one contiguous "**Field:**"
//     run following the title (a well-formed header block — a stray field marooned
//     in the body is not a header).
func validateWholeTaskFile(content string) error {
	if unbalancedFence(content) {
		return errors.New("unterminated code fence: a ``` or ~~~ block is opened but never closed")
	}

	outside := mdfence.OutsideFences(content)
	lines := make([]string, len(outside))
	var h1, h2, status, priority int
	for i, l := range outside {
		lines[i] = l.Text
		if isH1Line(l.Text) {
			h1++
		}
		if isH2Line(l.Text) {
			h2++
		}
		if isStatusLine(l.Text) {
			status++
		}
		if isPriorityLine(l.Text) {
			priority++
		}
	}

	switch {
	case h1 == 0:
		return errors.New("missing title: no \"# \" H1 heading outside code fences")
	case h1 > 1:
		return fmt.Errorf("two title lines: found %d \"# \" H1 headings, want exactly one", h1)
	}
	// An `if` rather than a switch, unlike its neighbours: they each carry a
	// zero case AND a duplicate case, and this rule has no upper bound to pair
	// with — many sections is the normal shape of a task file.
	if h2 == 0 {
		return errors.New("missing section: no \"## \" H2 heading outside code fences, so no part of the body is addressable by amend")
	}
	switch {
	case status == 0:
		return errors.New("missing Status: no \"**Status:**\" line outside code fences")
	case status > 1:
		return fmt.Errorf("two Status lines: found %d \"**Status:**\" lines, want exactly one", status)
	}
	switch {
	case priority == 0:
		return errors.New("missing Priority: no \"**Priority:**\" line outside code fences")
	case priority > 1:
		return fmt.Errorf("two Priority lines: found %d \"**Priority:**\" lines, want exactly one", priority)
	}

	start, end := headerBlock(lines)
	if start == end {
		return errors.New("malformed header block: no \"**Field:**\" run follows the title")
	}
	if !blockHas(lines, start, end, isStatusLine) {
		return errors.New("malformed header block: the \"**Status:**\" line is not part of the contiguous header block after the title")
	}
	if !blockHas(lines, start, end, isPriorityLine) {
		return errors.New("malformed header block: the \"**Priority:**\" line is not part of the contiguous header block after the title")
	}

	return nil
}

// blockHas reports whether any line in the half-open range [start, end) of lines
// satisfies pred. It scopes the header-field predicates to the header block found
// by headerBlock.
func blockHas(lines []string, start, end int, pred func(string) bool) bool {
	for i := start; i < end && i < len(lines); i++ {
		if pred(lines[i]) {
			return true
		}
	}
	return false
}

// unbalancedFence reports whether content ends with an OPEN code fence — a ```
// or ~~~ block that is opened and never closed.
//
// mdfence exposes no imbalance detector (OutsideFences deliberately treats the
// tail of an unterminated fence as fenced and simply drops it), so this drives
// mdfence's own Delim/OpensFence primitives through the identical open/close
// state machine as mdfence.Scanner and reports whether it is still inside a fence
// at EOF. Reusing those primitives is what keeps "is this a fence" answered in
// exactly one place; only the terminal in-fence check is local.
func unbalancedFence(content string) bool {
	var openCh byte
	var openRun int
	in := false
	for line := range strings.SplitSeq(content, "\n") {
		ch, run, info, ok := mdfence.Delim(line)
		if !ok {
			continue
		}
		if !in {
			// A backtick delimiter whose info string carries a backtick is prose
			// (an inline code run), not an opening fence — the defect mdfence
			// exists to prevent.
			if !mdfence.OpensFence(ch, info) {
				continue
			}
			in, openCh, openRun = true, ch, run
			continue
		}
		// Inside a fence: a bare run of the same character, at least as long as
		// the opener, closes it.
		if ch == openCh && run >= openRun && info == "" {
			in = false
		}
	}
	return in
}

// OverwriteTaskFile replaces a task file's entire contents with content, after
// validating content as a well-formed whole task file (validateWholeTaskFile).
//
// The write is guarded and surface-stamped: it resolves the task across the
// active/done/cancelled dirs, holds the per-path advisory lock across the
// read→compare→write, and — because content already equals the header+body a
// task file carries — persists it verbatim via atomicfile.Write under v.Root so
// the .surface stamp is applied. An identical-content call is a no-op: the file
// is neither rewritten nor restamped. Invalid content is rejected WITHOUT
// touching the file on disk.
//
// Whether writing to an ARCHIVED task (done/ or cancelled/) is permitted is the
// CALLER's concern — the CLI refuses archived slugs. This writer honors whatever
// path resolveTaskFile returns.
//
// The HEADER is not the caller's concern, and that asymmetry is deliberate. A
// body that changes a header field is REFUSED here, for every caller, because
// the refusal was previously an MCP-handler-local rule while `vp tasks edit`
// reached this same writer with no header diff at all — so a hand-edited
// Status/Parent/Depends line saved cleanly through the CLI and was refused
// through MCP. One rule on the writer both surfaces already call is the whole
// point; a second copy beside the CLI would be the defect, not the fix.
//
// Migrations that exist to rewrite headers call
// OverwriteTaskFileRewritingHeader instead. That is an explicit, greppable
// opt-in rather than a boolean at the call site: a reader can see which callers
// are allowed to move a header field and which are not.
func (v *Vault) OverwriteTaskFile(project, slug, content string) error {
	return v.overwriteTaskFile(project, slug, content, headerMustMatch)
}

// OverwriteTaskFileRewritingHeader is OverwriteTaskFile with the header-change
// refusal lifted. It is for the `vp migrate task-*` commands, whose entire
// purpose is repairing a malformed or legacy header block — the one class of
// caller for which "the header must not move" is the wrong rule.
//
// It is deliberately a separate, awkwardly-named entry point rather than a
// parameter. Adding a migration is then a decision someone makes on purpose and
// a reviewer can find with one grep, which is the property a bare `true`
// argument at a call site does not have.
func (v *Vault) OverwriteTaskFileRewritingHeader(project, slug, content string) error {
	return v.overwriteTaskFile(project, slug, content, headerMayChange)
}

// headerPolicy selects whether a whole-file overwrite may move a header field.
type headerPolicy int

const (
	// headerMustMatch refuses any body whose header differs from disk.
	headerMustMatch headerPolicy = iota
	// headerMayChange permits it — migrations repairing a header block.
	headerMayChange
)

func (v *Vault) overwriteTaskFile(project, slug, content string, policy headerPolicy) error {
	path, done, err := v.resolveTaskFile(project, slug)
	if err != nil {
		return err
	}

	// Hold the per-path lock across the read→compare→write so a concurrent
	// writer of the same task cannot interleave. Acquire is a blocking LOCK_EX
	// and re-entrant acquisition deadlocks, so write with atomicfile.Write
	// directly under the held lock rather than via lockedWrite (which re-locks).
	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock task: %w", err)
	}
	defer release()

	current, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read task: %w", err)
	}
	if string(current) == content {
		// No-op: identical content. Do not rewrite and do not restamp.
		return nil
	}

	if err := validateWholeTaskFile(content); err != nil {
		return err
	}

	// ORDER IS DELIBERATE: shape first, then the header compare. The MCP handler
	// used to run its header check BEFORE validation, so a body that is both
	// shape-invalid and header-smuggling now reports the shape error where it
	// once reported the smuggle. That is the correct way round — refuseHeaderChange
	// reads the proposed header through parseTaskMeta, whose Parent/Depends bind
	// to headerBlock, and on a malformed file that block is not trustworthy. A
	// compare over an unvalidated parse can read body prose as metadata and
	// refuse (or clear) the wrong field.
	//
	// The compare then runs INSIDE the held lock, against the bytes just read
	// from disk. The MCP handler compared against a snapshot it took before the
	// lock was acquired, which left a window where a concurrent writer could
	// move a field between the compare and the write.
	if policy == headerMustMatch {
		onDisk := ParseTaskMetaFromContent(slug, string(current), done)
		proposed := ParseTaskMetaFromContent(slug, content, done)
		// Wrapped at its SOURCE, per apperr's contract: a guard correctly
		// rejecting bad input is the caller's fault, never a system-health
		// problem. errors.As survives the %w wrapping every surface applies,
		// so both the MCP handler and the CLI get the classification without
		// either of them knowing this rule exists.
		if err := refuseHeaderChange(onDisk, proposed); err != nil {
			return apperr.Caller(err)
		}
	}

	return atomicfile.Write(v.Root, path, []byte(content))
}

// HeaderChangeError is the typed refusal a whole-file overwrite returns when the
// proposed body moves a header field. It is typed so a surface can classify it:
// this is a caller's malformed request, never an internal fault.
type HeaderChangeError struct {
	// Field is the header field as it appears in the file ("title",
	// "**Status:**", …) so the message names what the caller actually typed.
	Field string
	// Was and Now are the on-disk and proposed values.
	Was, Now string
	// Action is the vp_manage_task action that DOES own Field.
	Action string
}

func (e *HeaderChangeError) Error() string {
	return fmt.Sprintf(
		"overwrite refused: the body changes %s from %q to %q. "+
			"Header fields are not overwrite's to write — %s owns this one, and two writers for "+
			"one field is how a reader and a writer come to disagree about which value is real. "+
			"Re-send the body with %s unchanged, then call action=%s if you meant to change it",
		e.Field, e.Was, e.Now, e.Action, e.Field, e.Action)
}

// refuseHeaderChange reports a HeaderChangeError when a proposed whole-file
// overwrite body disagrees with the task's current header.
//
// It is the guard that keeps `overwrite` from becoming a second writer for
// fields that already have one. vp_manage_task's design is eight actions with
// DISJOINT write sets: title and priority belong to set_meta, status to
// update_status, parent and depends to set_relations. A whole-file writer
// trivially reaches all of them, so without this it would be a bypass for every
// one of those rules at once, including the terminal-status rule that keeps a
// "completed" task from sitting in the active directory.
//
// The answer is a rejected body rather than a silent revert: silently restoring
// the old header would write something the caller did not ask for, and a caller
// who genuinely wants a status change has an action for it.
//
// Depends is compared as an ordered list because that is how it is written and
// read back; a reorder is a change to the field and belongs to set_relations
// like any other.
func refuseHeaderChange(onDisk, proposed TaskMeta) error {
	for _, f := range []HeaderChangeError{
		{"title", onDisk.Title, proposed.Title, "set_meta"},
		{"**Status:**", onDisk.Status, proposed.Status, "update_status"},
		{"**Priority:**", onDisk.Priority, proposed.Priority, "set_meta"},
		{"**Parent:**", onDisk.Parent, proposed.Parent, "set_relations"},
		{"**Depends:**", formatDependsList(onDisk.Depends), formatDependsList(proposed.Depends), "set_relations"},
	} {
		if f.Was != f.Now {
			return &HeaderChangeError{Field: f.Field, Was: f.Was, Now: f.Now, Action: f.Action}
		}
	}
	return nil
}

// ParseTaskMetaFromContent extracts a task's header metadata from a whole task
// file's markdown WITHOUT touching the filesystem.
//
// It exists for one caller shape: comparing a PROPOSED whole-file body against
// the task currently on disk, so a writer can refuse a body that smuggles a
// change to a header field owned by another action. slug and done describe the
// task being compared against, not anything read out of content.
//
// It is the same parse the readers use, deliberately — a second, private
// re-implementation of "what does this header say" is how a reader and a writer
// come to disagree about which value is real.
func ParseTaskMetaFromContent(slug, content string, done bool) TaskMeta {
	return parseTaskMeta(slug, content, done)
}

// parseTaskMeta extracts metadata from task markdown content.
//
// Every field is FIRST-wins, matching what CreateTask writes and what
// replaceStatusLine rewrites. Last-wins here is what let a file with a
// duplicated header report line 8's status while the writer kept updating
// line 3's.
// Title, Status and Priority keep the original whole-file scan. Parent and
// Depends are OPTIONAL and are therefore bound to the header block — see
// headerBlock for why a whole-file scan would read body prose as metadata.
func parseTaskMeta(slug, content string, done bool) TaskMeta {
	meta := TaskMeta{Slug: slug, Done: done}
	var haveStatus, havePriority bool
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if isH1Line(trimmed) && meta.Title == "" {
			meta.Title = strings.TrimPrefix(trimmed, "# ")
		}
		if !haveStatus && isStatusLine(trimmed) {
			meta.Status, _ = headerFieldValue(trimmed, fieldStatus)
			haveStatus = true
		}
		if !havePriority && isPriorityLine(trimmed) {
			meta.Priority, _ = headerFieldValue(trimmed, fieldPriority)
			havePriority = true
		}
	}

	lines := strings.Split(content, "\n")
	start, end := headerBlock(lines)
	var haveParent, haveDepends bool
	for _, line := range lines[start:end] {
		if v, ok := headerFieldValue(line, fieldParent); ok && !haveParent {
			meta.Parent = v
			haveParent = true
		}
		if v, ok := headerFieldValue(line, fieldDepends); ok && !haveDepends {
			meta.Depends = parseDependsList(v)
			haveDepends = true
		}
	}
	return meta
}

// parseDependsList splits a "**Depends:**" value into slugs, preserving file
// order, dropping empties, and deduping. It returns nil (never an empty slice)
// when there is nothing, so TaskMeta.Depends stays omitempty-clean.
func parseDependsList(v string) []string {
	var out []string
	seen := make(map[string]bool)
	for part := range strings.SplitSeq(v, ",") {
		s := strings.TrimSpace(part)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// formatDependsList renders slugs as a "**Depends:**" value.
func formatDependsList(slugs []string) string {
	return strings.Join(slugs, ", ")
}

// replaceStatusLine replaces the value on the first **Status:** line in task
// markdown. If the content has NO status line, one is inserted — immediately
// after the H1 title if there is one, otherwise at the top of the file.
// Silently dropping the update (the old behaviour) left the task file claiming
// a status the caller had explicitly changed away from.
func replaceStatusLine(content, newStatus string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if isStatusLine(line) {
			lines[i] = "**Status:** " + newStatus
			return strings.Join(lines, "\n")
		}
	}

	// No status line: insert one. Splicing into the line slice (rather than
	// concatenating strings) preserves the file's trailing-newline shape
	// exactly — a trailing "" element stays the trailing "" element.
	at := 0
	for i, line := range lines {
		if isH1Line(line) {
			at = i + 1
			break
		}
	}
	lines = slices.Insert(lines, at, "**Status:** "+newStatus)
	return strings.Join(lines, "\n")
}

// globTaskMeta reads all .md files from a directory and returns their metadata.
func globTaskMeta(dir string, done bool) ([]TaskMeta, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("glob tasks: %w", err)
	}

	var result []TaskMeta
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			return nil, fmt.Errorf("read task %s: %w", m, err)
		}
		slug := strings.TrimSuffix(filepath.Base(m), ".md")
		meta := parseTaskMeta(slug, string(data), done)
		result = append(result, meta)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Legacy header classification.
//
// A task file written before the current header contract carries its status as a
// BARE, un-bolded "Status: value" line directly under the title, where the
// contract wants "**Status:** value" inside the contiguous field run. Every
// predicate in this package requires the bolded form — headerFieldValue is THE
// definition and it is prefix-matched on "**Status:**" — so the legacy line is
// invisible to the parser, to the writers, and to the detectors alike.
//
// It is not invisible to headerBlock. A bare line sitting between the title and
// the field run is not isHeaderFieldLine, so it ends the block before it starts
// and validateWholeTaskFile refuses the file at "no \"**Field:**\" run follows
// the title". That is why no whole-file writer can repair these files, including
// the migrations built to repair them.
//
// 🔴 This classifier sits IN FRONT of validateWholeTaskFile and never weakens it.
// A repair built on it must produce a file the validator ACCEPTS, and prove that
// by asking the validator rather than by comparing bytes.

// legacyStatusValue returns the value of a BARE "Status: value" line, and
// ok=false for any other line.
//
// The bolded form belongs to headerFieldValue and is not matched here. Neither
// is "Status::Skipped" — a Rust path expression that appears mid-sentence in a
// real archived task and that a naive "^Status:" match reads as a header line.
// The discriminator is that the key must be followed by whitespace or by end of
// line, which is the whole of the difference between a field and a code snippet.
//
// Leading whitespace is tolerated for the same reason headerFieldValue tolerates
// it: it is the shape this markdown has always used. Tolerating it is safe only
// because ScanLegacyHeader adds the structural guard below; this predicate alone
// is NOT sufficient to identify a header line.
func legacyStatusValue(line string) (string, bool) {
	return legacyFieldValue(line, fieldStatus)
}

// legacyFieldValue is the field-agnostic form of legacyStatusValue, and the ONE
// definition of "a bare legacy header line" for this package. The bare-only
// repair needs the same predicate for Priority that the classifier uses for
// Status, and a near-copy is how a classifier and a repair come to disagree
// about which lines are header fields.
//
// It is LINE-ANCHORED, which is load-bearing rather than tidy: real legacy
// status values carry colons mid-sentence ("Test case: ...", "DONE: ..."), and a
// colon scan that is not anchored splits a value in half at the first of them.
func legacyFieldValue(line, field string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), field+":")
	if !ok {
		return "", false
	}
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// LegacyHeaderClass names the shape of a task file's status declaration.
type LegacyHeaderClass int

const (
	// LegacyHeaderClean — no bare legacy status line outside fences. The file
	// may still be invalid for other reasons; this classifier does not judge.
	LegacyHeaderClean LegacyHeaderClass = iota

	// LegacyHeaderBoth — a bare legacy line AND a bolded field. The bare line
	// carries the surviving true value; the bolded field carries the stale one.
	// This is the only class with a provably lossless mechanical repair.
	LegacyHeaderBoth

	// LegacyHeaderBareOnly — a bare legacy line and NO bolded field, so the bare
	// line is the file's ONLY status declaration. Deleting it destroys the
	// status and leaves the file refused at validateWholeTaskFile's "missing
	// Status" arm rather than repaired.
	//
	// The repair is CONSTRUCTION, not promotion, and that distinction was
	// measured rather than assumed: every file in the class carries zero bolded
	// header fields of any kind, so promoting Status alone still leaves the file
	// refused — at the missing-PRIORITY arm instead. See
	// RepairLegacyBareOnlyHeader, which builds the whole header block.
	LegacyHeaderBareOnly

	// LegacyHeaderMultiTitle — more than one H1 outside fences. The second title
	// opens an intact legacy document whose own header disagrees with the first,
	// so choosing between them is a judgment call per file rather than a
	// migration. Separate task; reported here, never written.
	//
	// This class WINS over the other three, and that is load-bearing: at least
	// one real file carries a modern bolded field belonging to the prepended
	// header and a bare legacy line belonging to the document underneath it.
	// Classified as Both, its repair would carry the legacy document's status
	// onto the modern header — a destructive edit across two unrelated records.
	LegacyHeaderMultiTitle

	// LegacyHeaderInverted — a bare legacy line AND a bolded field, exactly like
	// Both, except that the BOLDED value is already a terminal status. That
	// inverts the premise the Both repair rests on.
	//
	// 🔴 The Both repair carries the bare value onto the bolded field because the
	// bare line is assumed authoritative and the bolded one stale. That direction
	// is an ASSUMPTION inferred from SHAPE, not a derivation — nothing measures
	// which value is true, and nothing can: which of two values is correct is not
	// a question a shape validator has an opinion about. The assumption held for
	// all 17 files repaired in vault 7a8393741, which is exactly the kind of run
	// that turns an assumption into an unexamined one.
	//
	// Where the bolded value is terminal, carrying the bare value forward would
	// overwrite a clean "retired"/"cancelled" with whatever the legacy line says —
	// on the real specimen, a sentence. The file would stop reading as terminal
	// while sitting in done/, manufacturing the exact finding
	// vaultaudit.DimTaskStatusDirectory rule 1 exists to report.
	//
	// So this class decides NOTHING about which value is true. It records that the
	// premise is unproven for this file and hands the judgment to a human, the way
	// BareOnly and MultiTitle already do. Separate task; reported here, never
	// written.
	LegacyHeaderInverted
)

// String renders the class for operator-facing reports.
func (c LegacyHeaderClass) String() string {
	switch c {
	case LegacyHeaderClean:
		return "clean"
	case LegacyHeaderBoth:
		return "both"
	case LegacyHeaderBareOnly:
		return "bare-only"
	case LegacyHeaderMultiTitle:
		return "multi-title"
	case LegacyHeaderInverted:
		return "inverted"
	}
	return "unknown"
}

// LegacyHeaderScan is one task file's classification plus the line numbers a
// repair needs. Line numbers are 1-indexed and count EVERY line of the file,
// fenced ones included, so they address the file the way an editor does; zero
// means absent.
type LegacyHeaderScan struct {
	Class     LegacyHeaderClass
	TitleLine int
	BareLine  int
	BareValue string
	BoldLine  int
	BoldValue string
}

// ScanLegacyHeader classifies a whole task file.
//
// Fence-awareness is load-bearing rather than defensive: the task files that
// DOCUMENT this defect quote its specimens inside code fences, so a fence-blind
// scan reports those tasks as instances of the bug and a fence-blind repair
// rewrites them. Fence detection is mdfence's, never a local reimplementation.
//
// The legacy line is identified STRUCTURALLY, never by line number: it is a bare
// status line, outside fences, whose nearest preceding non-blank unfenced line is
// the title. Position varies across the real corpus — directly under the title,
// one blank line below it, and further down again in a file that opens with a
// YAML block — so a rule keyed on a fixed line number misses most of the
// population, while a bare "^Status:" match with no structural guard picks up
// body prose and code. Both halves of that trade were measured before this
// predicate was written.
func ScanLegacyHeader(content string) LegacyHeaderScan {
	var scan LegacyHeaderScan

	outside := mdfence.OutsideFences(content)
	h1Count := 0
	prevNonBlank := -1

	for i, l := range outside {
		trimmed := strings.TrimSpace(l.Text)

		if isH1Line(trimmed) {
			h1Count++
			if scan.TitleLine == 0 {
				scan.TitleLine = l.Num
			}
		}
		if v, ok := headerFieldValue(l.Text, fieldStatus); ok && scan.BoldLine == 0 {
			scan.BoldLine = l.Num
			scan.BoldValue = v
		}
		// The structural guard. A bare status line is a header field only
		// directly under the title; anywhere else in the file it is prose.
		if v, ok := legacyStatusValue(l.Text); ok && scan.BareLine == 0 &&
			prevNonBlank >= 0 && isH1Line(strings.TrimSpace(outside[prevNonBlank].Text)) {
			scan.BareLine = l.Num
			scan.BareValue = v
		}

		if trimmed != "" {
			prevNonBlank = i
		}
	}

	switch {
	case h1Count > 1:
		scan.Class = LegacyHeaderMultiTitle
	case scan.BareLine == 0:
		scan.Class = LegacyHeaderClean
	case scan.BoldLine == 0:
		scan.Class = LegacyHeaderBareOnly
	case IsTerminalStatus(scan.BoldValue):
		// The bolded value is already terminal, so the Both repair's premise —
		// bare is true, bolded is stale — is unproven for this file and the
		// repair would overwrite a correct terminal status. Classified apart and
		// never written; see LegacyHeaderInverted.
		//
		// The discriminator is deliberately just this one predicate. A file whose
		// bare and bolded values are BOTH terminal would be trivially lossless to
		// repair, and there is no such file in the corpus — building the rule for
		// it would be building a rule for an empty set.
		scan.Class = LegacyHeaderInverted
	default:
		scan.Class = LegacyHeaderBoth
	}
	return scan
}

// RepairLegacyBothHeader rewrites a LegacyHeaderBoth file into the current
// contract: it DROPS the bare legacy line and carries that line's value onto the
// bolded field, which held the stale one.
//
// 🔴 Both edits are ONE write, deliberately. The bare line is the true value and
// the bolded field is the lie, so a two-step that dropped the bare line first
// would leave the file asserting only the falsehood, and a crash between the
// steps would make that permanent. This is the same reasoning that made archiving
// rewrite-then-rename with a single atomic commit point.
//
// The value is carried across VERBATIM and is not normalized to a terminal
// token. Making an archived file's status agree with its directory is
// `vp migrate task-status`, which already exists and is precisely what this
// repair unblocks — one concern per command, so the two cannot drift into two
// definitions of the same thing.
//
// Every other class is refused. BareOnly has no bolded field to carry a value
// onto and is repaired by RepairLegacyBareOnlyHeader, MultiTitle needs a
// per-file judgment call, and Inverted carries a terminal bolded value this
// repair would destroy; refusing here is what keeps each repair to one class.
func RepairLegacyBothHeader(content string) (string, error) {
	scan := ScanLegacyHeader(content)
	if scan.Class != LegacyHeaderBoth {
		return "", fmt.Errorf("legacy header repair handles %s files only, got %s", LegacyHeaderBoth, scan.Class)
	}

	lines := strings.Split(content, "\n")
	if scan.BareLine < 1 || scan.BareLine > len(lines) || scan.BoldLine < 1 || scan.BoldLine > len(lines) {
		return "", fmt.Errorf("legacy header repair: line out of range (bare %d, bold %d, file has %d lines)",
			scan.BareLine, scan.BoldLine, len(lines))
	}

	// Set the bolded field FIRST, by its pre-removal index, then drop the bare
	// line. Doing it in this order is correct whichever line comes first.
	lines[scan.BoldLine-1] = "**" + fieldStatus + ":** " + scan.BareValue
	repaired := strings.Join(slices.Delete(lines, scan.BareLine-1, scan.BareLine), "\n")

	// The oracle. This classifier sits in front of the validator and defers to
	// it; a repair whose output the validator would refuse is a bug in the
	// repair. Asking here is what stops this from becoming a second, weaker
	// definition of a well-formed task file.
	if err := validateWholeTaskFile(repaired); err != nil {
		return "", fmt.Errorf("legacy header repair produced an invalid task file: %w", err)
	}
	return repaired, nil
}

// ---------------------------------------------------------------------------
// The bare-only repair: header-block CONSTRUCTION.

const (
	// LegacyPriorityDefault is the priority supplied to a bare-only file that
	// carries none in any form. Exported so the operator-facing report NAMES the
	// value it is about to fabricate instead of restating a literal that could
	// drift from this one. It is an OPERATOR DECISION (2026-09-05), not a
	// derivation: 17 of the 34 files in the live class state no priority
	// anywhere, and the alternative — repairing only the files whose every field
	// is derivable — was put to the operator and declined in favour of full
	// coverage. Re-derive the split with `vp migrate task-header`; never quote a
	// count from a comment.
	LegacyPriorityDefault = "medium"

	// legacyHeaderSectionHeading is where a wrapped legacy value's remainder
	// lands. Named by TOPIC and not by a claim, because amend is keyed on
	// heading text and cannot revise it later.
	legacyHeaderSectionHeading = "## Legacy header"

	legacyHeaderSectionProvenance = "The pre-contract header run, relocated verbatim when the bolded header " +
		"fields above were constructed from it. Nothing here was edited."

	// legacyHeaderSectionFrontmatterNote is appended for the one file shape that
	// carries legacy status in TWO places. Not touching the YAML block is the
	// right call — it is a third header format with its own readers — but the
	// sentence above, unqualified, tells a reader that all pre-contract header
	// material is in this section, and for such a file it is not. The clause is
	// conditional rather than universal so it stays true per file instead of
	// being noise on the 33 that have no frontmatter.
	legacyHeaderSectionFrontmatterNote = " The YAML frontmatter block above the title is a separate " +
		"legacy format and was left where it is; it may carry its own status and priority keys."
)

// LegacyPrioritySource names where a constructed **Priority:** value came from.
// The report prints it because "medium" that was READ from the file and "medium"
// that was SUPPLIED for it are different facts, and only one of them is an
// operator decision the reviewer needs to see.
type LegacyPrioritySource string

const (
	// PriorityFromRun — a bare "Priority:" line inside the legacy header run.
	PriorityFromRun LegacyPrioritySource = "the legacy run"
	// PriorityFromFrontmatter — a "priority:" key in YAML frontmatter, which is
	// a THIRD header format the classifier does not read. Exactly one file in
	// the live class has it, and it has no bare Priority line.
	PriorityFromFrontmatter LegacyPrioritySource = "YAML frontmatter"
	// PriorityFromDefault — nothing in the file states one; LegacyPriorityDefault
	// was supplied.
	PriorityFromDefault LegacyPrioritySource = "supplied default"
)

// LegacyBareOnlyRepair is the outcome of constructing a header block, carried as
// a struct so the report can name WHAT was written and where each value came
// from without re-deriving any of it. A caller re-deriving the priority source
// from the bytes would be a second definition of the rule below.
type LegacyBareOnlyRepair struct {
	Content        string
	Status         string
	Priority       string
	PrioritySource LegacyPrioritySource
	Relocated      int // lines of the legacy run moved into the body (the whole run bar a consumed Priority)
}

// RepairLegacyBareOnlyHeader builds a valid modern header block for a
// LegacyHeaderBareOnly file, in ONE write, validated against
// validateWholeTaskFile as the oracle.
//
// # Why this is CONSTRUCTION and not promotion
//
// The obvious repair — bold the bare Status line — produces a file the validator
// still refuses. Every file in the live class carries ZERO bolded header fields
// of any kind, so a promoted Status leaves "missing Priority" behind it. The
// deliverable is therefore the whole block: a Status field, a Priority field,
// and a decision about where the rest of the legacy run goes.
//
// # The run, and the rule that is NOT applied to it
//
// The run is the contiguous non-blank stretch beginning at the bare Status line
// and ending at the first blank line — the same contiguous-run shape headerBlock
// enforces for the modern header. It is fence-aware: a fence opening inside the
// run ends it, because a fenced line is not structure.
//
// 🔴 THIS DELIBERATELY DOES NOT SPLIT THE RUN INTO FIELDS AT EVERY "Key:" LINE,
// and that is a correction to the plan it was built from. The corpus proves the
// boundary is undecidable: "Scope:" opening a real second legacy field and
// "Design decision:" opening a mid-sentence prose line are the SAME line shape,
// in two files, meaning opposite things. A value-boundary rule keyed on that
// shape truncates a 22-line status value at its 13th line.
//
// The boundary is made IRRELEVANT instead of guessed. Only two values are
// extracted — the Status value's first line, and a Priority line — and
// everything else in the run travels verbatim into the body. Whether a given
// line is "continuation" or "another legacy key" changes nothing about where it
// ends up, so the question never has to be answered. Created:, Scope:,
// "Design decision:" and "Depends on:" all ride along; no modern field is
// invented for them, because Parent and Depends have exactly one writer and it
// is set_relations.
//
// # The legacy run is RELOCATED, never flattened
//
// Joining a wrapped value onto one field line would be lossless for exactly one
// command: `vp migrate task-status` replaces the WHOLE Status value with a
// terminal token, so a flattened 22-line implementation history is destroyed on
// the next run. The run is relocated under an H2 instead, where it survives —
// and survives addressably, because amend reaches an H2 section and reaches
// nothing above the first one.
//
// The run is relocated ENTIRE, its Status line included. See the comment at the
// relocation itself for why the plan's overflow-only boundary loses the value's
// first line to the very command the relocation exists to protect against.
//
// # The consequence this repair CREATES, and the pairing it requires
//
// None of the legacy values is terminal, and the whole live class sits in done/.
// DimTaskStatusDirectory skips a file with no bolded Status line — absence is the
// older format, not a claim — so these files are invisible to it today and become
// rule-1 findings the moment the field exists. `vp migrate task-status` is the
// second half of the pair and must run immediately after; the caller says so in
// its own help, and a test drives both and asserts the findings return to
// baseline.
func RepairLegacyBareOnlyHeader(content string) (LegacyBareOnlyRepair, error) {
	var out LegacyBareOnlyRepair

	scan := ScanLegacyHeader(content)
	if scan.Class != LegacyHeaderBareOnly {
		return out, fmt.Errorf("legacy bare-only repair handles %s files only, got %s",
			LegacyHeaderBareOnly, scan.Class)
	}

	lines := strings.Split(content, "\n")
	if scan.BareLine < 1 || scan.BareLine > len(lines) {
		return out, fmt.Errorf("legacy bare-only repair: line out of range (bare %d, file has %d lines)",
			scan.BareLine, len(lines))
	}

	unfenced := make(map[int]bool, len(lines))
	for _, l := range mdfence.OutsideFences(content) {
		unfenced[l.Num] = true
	}

	runStart := scan.BareLine - 1
	runEnd := runStart
	for runEnd < len(lines) && strings.TrimSpace(lines[runEnd]) != "" && unfenced[runEnd+1] {
		runEnd++
	}

	// The Status value comes from the classifier, never from a second parse of
	// the same line.
	out.Status = scan.BareValue

	// 🔴 THE WHOLE RUN IS RELOCATED, INCLUDING THE STATUS LINE ITSELF, and that
	// is a correction to the plan this was built from rather than an extension
	// of it. That plan relocated only the OVERFLOW — the value's second line
	// onward — on the stated ground that `vp migrate task-status` replaces the
	// whole Status value and would destroy anything flattened onto the field.
	// The reasoning is right and it was applied one line short: the value's
	// FIRST line is handed to that same field, so the mandatory paired command
	// destroys it too.
	//
	// Measured on a copy of the live vault, running both halves in order:
	// hnsw-library-bug-fixes-and-hardening's 236-byte upstream-PR provenance
	// survived nowhere in the file afterwards, and friction-analytics-port's
	// relocated block opened mid-sentence with its first clause gone. Relocating
	// the run entire is what makes the legacy header survive the operation it is
	// half of, and it also stops the relocated block being a fragment.
	//
	// The Priority line is the ONE exception, and for the opposite reason:
	// task-status does not touch Priority, so a consumed Priority value is
	// preserved by the header field itself and duplicating it would be noise.
	//
	// First-wins on that key, and an EMPTY value does not count as one: falling
	// through to the supplied default beats writing a blank field. A second
	// Priority line, if a vault this measurement did not cover has one, stays in
	// the relocated block where a human can see it.
	relocated := make([]string, 0, runEnd-runStart)
	relocated = append(relocated, lines[runStart])
	for i := runStart + 1; i < runEnd; i++ {
		if v, ok := legacyFieldValue(lines[i], fieldPriority); ok && v != "" && out.Priority == "" {
			out.Priority, out.PrioritySource = v, PriorityFromRun
			continue
		}
		relocated = append(relocated, lines[i])
	}
	if out.Priority == "" {
		if v, ok := frontmatterValue(content, "priority"); ok && v != "" {
			out.Priority, out.PrioritySource = v, PriorityFromFrontmatter
		} else {
			out.Priority, out.PrioritySource = LegacyPriorityDefault, PriorityFromDefault
		}
	}
	out.Relocated = len(relocated)

	repaired := make([]string, 0, len(lines)+6)
	repaired = append(repaired, lines[:runStart]...)
	repaired = append(repaired,
		"**"+fieldStatus+":** "+out.Status,
		"**"+fieldPriority+":** "+out.Priority)
	// Unconditional: `relocated` is seeded with the run's Status line, so there is
	// always a section to write and always a collision to check for. An earlier
	// `if len(relocated) > 0` here was dead — it dated from the overflow-only
	// boundary, where a one-line run relocated nothing — and a guard suggesting a
	// path that cannot exist is worse than no guard.
	if err := refuseLegacySectionCollision(content); err != nil {
		return out, err
	}
	provenance := legacyHeaderSectionProvenance
	if _, hasFrontmatter := frontmatterBlock(content); hasFrontmatter {
		provenance += legacyHeaderSectionFrontmatterNote
	}
	repaired = append(repaired, "", legacyHeaderSectionHeading, "", provenance, "")
	repaired = append(repaired, relocated...)
	repaired = append(repaired, lines[runEnd:]...)
	out.Content = strings.Join(repaired, "\n")

	// The oracle. This repair sits IN FRONT of the validator and never weakens
	// it; a constructed file the validator would refuse is a bug here.
	if err := validateWholeTaskFile(out.Content); err != nil {
		return out, fmt.Errorf("legacy bare-only repair produced an invalid task file: %w", err)
	}
	return out, nil
}

// refuseLegacySectionCollision refuses a file that already carries an H2 by the
// relocation heading's name. Emitting a second one would leave the pre-existing
// section unreachable — amend is keyed on heading text and takes the FIRST match
// — which is the stranding defect this project already has filed against
// CreateTask's unconditional "## Context".
//
// No file in the live class collides today (derive: `vp migrate task-header`,
// then grep the reported files). This refuses rather than skips the check
// because the command runs against vaults that measurement did not cover, and
// the failure it prevents is silent.
func refuseLegacySectionCollision(content string) error {
	for _, l := range mdfence.OutsideFences(content) {
		if isH2Line(l.Text) && strings.EqualFold(strings.TrimSpace(l.Text), legacyHeaderSectionHeading) {
			return fmt.Errorf("legacy bare-only repair: line %d already carries the %q heading the "+
				"relocated prose would use, and a second one would strand it from amend", l.Num, legacyHeaderSectionHeading)
		}
	}
	return nil
}

// frontmatterValue reads one key from a YAML frontmatter block, which is a THIRD
// header format some legacy task files carry above their title.
//
// It owns only the DELIMITER policy; the key match is frontmatterField's, shared
// with frontmatterFieldFromHead. An earlier version of this function hand-rolled
// the key match too, and justified it by claiming a YAML dependency would be a
// larger surface than the question. That was WRONG TWICE: gopkg.in/yaml.v3 is
// already a direct dependency (go.mod) and ParseFrontmatter in this same package
// already calls yaml.Unmarshal, so nothing was being avoided; and the copy was
// LOOSER than the definition it duplicated, matching on a TrimSpace'd key so a
// nested "  priority:" under a "meta:" block read as top-level. Recorded because
// the refuted reasoning is the useful part.
//
// The policy, and why it is stricter than its sibling's: the block must OPEN the
// file, so a "---" rule further down the body is not mistaken for one, and it
// must be CLOSED. An unterminated block is not treated as running to EOF —
// that would read the entire task body as metadata. frontmatterFieldFromHead
// tolerates the unterminated case because its window is bounded by a fixed read
// size and narrowing it would change a live session-reading path; this caller
// has the whole file and no such excuse.
func frontmatterValue(content, key string) (string, bool) {
	front, ok := frontmatterBlock(content)
	if !ok {
		return "", false
	}
	v := frontmatterField(front, key)
	return v, v != ""
}

// frontmatterBlock returns the text between a task file's frontmatter
// delimiters. It is the delimiter policy in one place, so "is there a
// frontmatter block" and "what does it say" cannot come to disagree — the
// relocation provenance note is emitted on the first question and the priority
// is read from the second.
func frontmatterBlock(content string) (string, bool) {
	front, ok := strings.CutPrefix(content, "---\n")
	if !ok {
		return "", false
	}
	end := strings.Index(front, "\n---")
	if end < 0 {
		return "", false
	}
	return front[:end], true
}

// ---------------------------------------------------------------------------
// The multi-title repair: demote the second title, relabel the fields under it.

const (
	// legacyStatusRelabel and legacyPriorityRelabel are what a demoted legacy
	// header block's own field lines become.
	//
	// 🔴 THIS LABEL IS INVENTED, AND IT IS INVENTED ONCE. Fencing the legacy
	// block, blockquoting it, or stripping its "**" markers would all satisfy the
	// validator equally well — the choice between them is a ONE-TIME CONVENTION,
	// not a per-file judgment, which is the distinction that decides whether this
	// class is mechanical at all. The rationale, so the next reader does not
	// re-litigate it:
	//
	//   - It preserves the value VERBATIM and legibly. Fencing would turn the
	//     whole legacy block into a code block and change how its prose renders;
	//     blockquoting changes rendering too, on text nobody asked to restyle.
	//   - It stays greppable as a field-that-was. Stripping the "**" would leave a
	//     bare "Status:" line indistinguishable from prose to a human — and one
	//     that would classify as a LEGACY header line again the moment anything
	//     promoted the demoted heading back to an H1.
	//   - It cannot be mistaken for a field by any reader here. headerFieldValue
	//     is prefix-matched on "**Status:**" and legacyStatusValue on "Status:",
	//     anchored after trimming; neither matches "**Legacy status:**".
	legacyStatusRelabel   = "**Legacy status:**"
	legacyPriorityRelabel = "**Legacy priority:**"
)

// LegacyRefusalKind names WHY a repair declined a file. The two reasons are not
// interchangeable and an operator-facing report must not blur them: a SHAPE
// refusal happens BEFORE the transform runs, so the validator never sees any
// bytes and there is nothing for it to have refused.
type LegacyRefusalKind int

const (
	// LegacyRefusedNone — the repair succeeded.
	LegacyRefusedNone LegacyRefusalKind = iota
	// LegacyRefusedShape — the file is not the shape this repair serves. Decided
	// from the file's own structure, before any transform is attempted.
	LegacyRefusedShape
	// LegacyRefusedValidator — the transform ran and validateWholeTaskFile
	// refused its output, so the defect is not the one being repaired.
	LegacyRefusedValidator
)

// String renders the refusal for operator-facing reports.
func (k LegacyRefusalKind) String() string {
	switch k {
	case LegacyRefusedShape:
		return "shape"
	case LegacyRefusedValidator:
		return "validator"
	}
	return "none"
}

// LegacyH1 is one unfenced H1 line: where it is and what it says. Reported for a
// file the repair refuses, so an operator can judge the shape without opening it.
type LegacyH1 struct {
	Line int
	Text string
}

// LegacyMultiTitleRepair is the outcome of a multi-title repair, carried as a
// struct so the report can describe both the write it plans AND the file it
// refuses. Titles and FieldLines are populated even when the repair fails —
// they are the sign-off detail.
type LegacyMultiTitleRepair struct {
	Content string // empty when the repair is refused
	Titles  []LegacyH1
	// FieldLines are every unfenced **Status:**/**Priority:** line in the ORIGINAL
	// file. A refusal is usually about where these sit relative to each other, and
	// line numbers are what make that judgeable from the report alone.
	FieldLines         []LegacyH1
	DemotedTitle       string
	RelabelledStatus   int
	RelabelledPriority int
	// Refusal says which kind of refusal produced the accompanying error, so the
	// report can state the reason it actually has rather than a blanket claim.
	Refusal LegacyRefusalKind
}

// RepairLegacyMultiTitleHeader rewrites a LegacyHeaderMultiTitle file whose shape
// is a modern header block PREPENDED above one intact legacy document: the second
// H1 is demoted to an H2, and the legacy field lines beneath it are relabelled.
//
// # Why this is mechanical, when the filed plan said it was a judgment call
//
// The filed reason for refusing automation was that the two headers disagree and
// choosing between them is a per-file judgment. The premise is true and the
// conclusion does not follow, because THIS TRANSFORM DOES NOT CHOOSE. Both values
// survive verbatim: the modern one stays a field, the legacy one becomes prose
// under a demoted heading that `amend` can address. Nothing is deleted and no
// value is preferred.
//
// The precedence it encodes is not inferred from shape — it is the status quo,
// verifiable today. Every reader already returns the MODERN header's values for
// these files, because parseTaskMeta is first-wins and the modern block is first.
// The legacy values are invisible to every reader now and stay invisible after.
// That is the line between this and the BOTH repair iteration 378 corrected: that
// one CHANGED the value every reader saw, on a premise inferred from shape.
//
// # TWO PRECONDITIONS, each with its own reason
//
// Both are refusals in the safe direction, and each exists for a different
// reason, so neither is the other's spare.
//
//  1. NOTHING OF THE FIRST DOCUMENT'S BODY MAY PRECEDE THE SECOND TITLE — no
//     unfenced H2 between them. This is what "PREPENDED" means, spelled as a
//     predicate. A rival document's title arrives before the first document has
//     any sections; a SECTION HEADING that happens to be written as an H1
//     arrives after them. The wedges that do occur between the two titles in the
//     real corpus are prose, blockquotes and stranded YAML — never a section.
//
//  2. EXACTLY TWO UNFENCED H1s. This one is a precondition of the TRANSFORM
//     rather than of the class: the relabel step is defined as "the fields below
//     the demoted title", and with a third title it would relabel a third
//     document's fields too. Nothing measured says that is right, so it is
//     refused rather than guessed.
//
// Neither rule names a file. Both happen to fire on the same single live
// specimen — one whose demoted headings would read "## Open Questions",
// "## Definition of done", "## Revised sequencing" — but a rule that named that
// file would be an exception wearing a rule's clothes, and would not survive the
// next vault.
//
// # 🔴 THE VALIDATOR DECIDES, NEVER THE CLASSIFIER
//
// ScanLegacyHeader returns `clean` for every file this transform touches,
// INCLUDING one whose output validateWholeTaskFile still refuses — the classifier
// asks "how many titles", and one title is one title whether or not the header
// block beneath it is well formed. A repair keyed on the classifier going clean
// would write nothing for that file AND drop it from the only report that names
// it, leaving a file no tool can write and no tool mentions. So the verdict here
// is the VALIDATOR's, per file, and a refusal is a reported row rather than a
// silence.
func RepairLegacyMultiTitleHeader(content string) (LegacyMultiTitleRepair, error) {
	var out LegacyMultiTitleRepair

	scan := ScanLegacyHeader(content)
	if scan.Class != LegacyHeaderMultiTitle {
		out.Refusal = LegacyRefusedShape
		return out, fmt.Errorf("legacy multi-title repair handles %s files only, got %s",
			LegacyHeaderMultiTitle, scan.Class)
	}

	unfenced := mdfence.OutsideFences(content)
	firstH2 := 0
	for _, l := range unfenced {
		trimmed := strings.TrimSpace(l.Text)
		switch {
		case isH1Line(trimmed):
			out.Titles = append(out.Titles, LegacyH1{Line: l.Num, Text: trimmed})
		case isH2Line(trimmed):
			if firstH2 == 0 {
				firstH2 = l.Num
			}
		case isStatusLine(trimmed), isPriorityLine(trimmed):
			out.FieldLines = append(out.FieldLines, LegacyH1{Line: l.Num, Text: trimmed})
		}
	}

	// Order matters: ask whether the file IS this shape before asking whether the
	// transform can express it. A document using H1 for its sections fails both
	// tests, and "this is not a prepended header" is the reason that describes it.
	if len(out.Titles) > 1 && !secondTitleOpensADocument(unfenced, out.Titles[1].Line, firstH2) {
		out.Refusal = LegacyRefusedShape
		return out, fmt.Errorf("the H1 at line %d cannot be shown to open a document of its own: it "+
			"carries no header material before the next heading (no bolded field line, no bare "+
			"\"Status:\" line), and %s. A task document begins with one or the other, so this H1 may be "+
			"a SECTION written at the wrong level and demoting it could restructure the document",
			out.Titles[1].Line, precedingSectionNote(unfenced, out.Titles[1].Line, firstH2))
	}
	if len(out.Titles) != 2 {
		out.Refusal = LegacyRefusedShape
		return out, fmt.Errorf("legacy multi-title repair handles a modern header prepended above ONE "+
			"legacy document, which is exactly two titles; this file has %d unfenced H1 lines, and the "+
			"relabel step is defined relative to ONE demoted title", len(out.Titles))
	}

	second := out.Titles[1]
	lines := strings.Split(content, "\n")
	if second.Line < 1 || second.Line > len(lines) {
		out.Refusal = LegacyRefusedShape
		return out, fmt.Errorf("legacy multi-title repair: title line out of range (%d, file has %d lines)",
			second.Line, len(lines))
	}

	// Demote by inserting a '#' AT the existing marker rather than at the start of
	// the line, so an indented H1 (which isH1Line accepts, because it trims) stays
	// indented instead of becoming "# # Title".
	lines[second.Line-1] = demoteH1(lines[second.Line-1])
	out.DemotedTitle = strings.TrimSpace(lines[second.Line-1])

	outsideFences := make(map[int]bool, len(lines))
	for _, l := range unfenced {
		outsideFences[l.Num] = true
	}
	// Relabel only BELOW the demoted title: everything above it belongs to the
	// modern header block, whose fields are the ones every reader already returns.
	for i := second.Line; i < len(lines); i++ {
		if !outsideFences[i+1] {
			continue
		}
		trimmed := strings.TrimSpace(lines[i])
		switch {
		case isStatusLine(trimmed):
			lines[i] = strings.Replace(lines[i], "**"+fieldStatus+":**", legacyStatusRelabel, 1)
			out.RelabelledStatus++
		case isPriorityLine(trimmed):
			lines[i] = strings.Replace(lines[i], "**"+fieldPriority+":**", legacyPriorityRelabel, 1)
			out.RelabelledPriority++
		}
	}

	repaired := strings.Join(lines, "\n")

	// The oracle, and the trap. A file can reach here classifying `clean` and
	// still be refused — its modern header may be malformed for reasons that have
	// nothing to do with a second title.
	if err := validateWholeTaskFile(repaired); err != nil {
		out.Refusal = LegacyRefusedValidator
		return out, fmt.Errorf("demoting the second title leaves a file validateWholeTaskFile still "+
			"refuses, so the defect is not the two titles: %w", err)
	}
	out.Content = repaired
	return out, nil
}

// demoteH1 turns an H1 line into an H2 by doubling its leading hash, preserving
// whatever indentation the line carries.
func demoteH1(line string) string {
	at := strings.Index(line, "#")
	if at < 0 {
		return line
	}
	return line[:at] + "#" + line[at:]
}

// headingTextAt returns the text of the unfenced line with the given 1-indexed
// number, for a refusal message that names the heading it is talking about.
func headingTextAt(unfenced []mdfence.Line, num int) string {
	for _, l := range unfenced {
		if l.Num == num {
			return strings.TrimSpace(l.Text)
		}
	}
	return ""
}

// secondTitleOpensADocument answers the ONE question the multi-title shape rule
// asks: can the H1 at titleLine be shown to begin a document of its own, rather
// than being a section of the document above it written at the wrong level?
//
// A task document begins one of two ways, and either is sufficient evidence:
//
//	A. IT OWNS HEADER MATERIAL. A bolded "**Field:**" line or a bare legacy
//	   "Status:" line appears under the title, before the next heading of any
//	   level. That is what the top of a task file looks like, in both the modern
//	   and the legacy dialect.
//	B. NOTHING OF THE DOCUMENT ABOVE PRECEDES IT. The file's FIRST H2 comes after
//	   this title, so the document above contributed only a header — which is what
//	   "PREPENDED" means. A file with no H2 at all fails this: it has no section
//	   anywhere, so this title is not shown to open one either.
//
// 🔴 BOTH DISJUNCTS ARE LOAD-BEARING, and the corpus proves each direction rather
// than the pair agreeing by luck. Measured over the live class of 33: A holds for
// 30, B for 32, A-or-B for 32, and NEITHER for exactly one — the document that
// uses H1 for its section headings. A alone wrongly refuses two live files whose
// legacy half carries no header block at all; B alone wrongly refuses a genuine
// prepended-over file whose modern half has a section of its own, and lets
// through a section-headed document that simply has no H2 to give it away.
//
// This is ONE question with two positive answers, not two refusal rules stacked.
// Anything that shows a document starting here is enough; nothing showing it is
// the refusal.
func secondTitleOpensADocument(unfenced []mdfence.Line, titleLine, firstH2 int) bool {
	for _, l := range unfenced {
		if l.Num <= titleLine {
			continue
		}
		trimmed := strings.TrimSpace(l.Text)
		if isH1Line(trimmed) || isH2Line(trimmed) {
			break
		}
		if isHeaderFieldLine(trimmed) {
			return true
		}
		if _, ok := legacyStatusValue(trimmed); ok {
			return true
		}
	}
	return firstH2 != 0 && firstH2 > titleLine
}

// precedingSectionNote renders the half of a shape refusal that says WHY evidence
// B is absent, which is different in the two cases and matters to whoever reads
// the row.
func precedingSectionNote(unfenced []mdfence.Line, titleLine, firstH2 int) string {
	if firstH2 == 0 {
		return "the file carries no \"## \" heading anywhere, so no section begins here either"
	}
	return fmt.Sprintf("section heading %q at line %d already opened this document's body",
		headingTextAt(unfenced, firstH2), firstH2)
}
