// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/mdfence"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// TaskMeta is the lightweight metadata for task listing.
//
// Parent and Depends are the relationship fields. Both are OPTIONAL and both
// carry omitempty for a reason that is load-bearing, not cosmetic: every task
// file written before they existed has neither, and TaskMeta is marshalled into
// vp_list_tasks, vp_get_task, vp tasks --json and vp_bootstrap_context's
// active_tasks — the last of which is already fighting a token budget it
// exceeds. Without omitempty, every legacy task would grow a "parent":"" and a
// "depends":null in every one of those payloads. With it, they stay
// byte-identical and absence means exactly what it says: standalone.
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

// DropIcebox removes tasks that are known but deliberately unscheduled.
//
// ONE definition, reached by every reader that shows INTENT — vp tasks,
// vp_list_tasks, and vp_bootstrap_context's active_tasks. Three callers each
// writing their own two-line filter is how the three of them end up disagreeing
// about what "active" means.
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
	if content != "" {
		buf.WriteString("\n")
		buf.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			buf.WriteByte('\n')
		}
	}

	return atomicfile.Write(v.Root, path, []byte(buf.String()))
}

// GetTask reads a task file and returns its metadata and full content.
// It searches active, done, and cancelled directories.
func (v *Vault) GetTask(project, slug string) (TaskMeta, string, error) {
	if err := validateSlugs(project, slug); err != nil {
		return TaskMeta{}, "", err
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
			return TaskMeta{}, "", err
		}
		path := filepath.Join(dir, slug+".md")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return TaskMeta{}, "", fmt.Errorf("read task: %w", err)
		}

		meta := parseTaskMeta(slug, string(data), loc.done)
		return meta, string(data), nil
	}

	return TaskMeta{}, "", fmt.Errorf("task %q not found in project %q", slug, project)
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
	return v.moveTask(project, slug, v.TaskDoneDir, "retired")
}

// CancelTask moves a task to the cancelled/ directory with status "cancelled".
func (v *Vault) CancelTask(project, slug string) error {
	return v.moveTask(project, slug, v.TaskCancelledDir, "cancelled")
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

	// Update status in file before moving.
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read task: %w", err)
	}
	updated := replaceStatusLine(string(data), status)

	destDir, err := destFn(project)
	if err != nil {
		return err
	}
	if err := EnsureDir(destDir); err != nil {
		return fmt.Errorf("ensure dest dir: %w", err)
	}

	destPath := filepath.Join(destDir, slug+".md")
	if err := v.lockedWrite(destPath, []byte(updated)); err != nil {
		return fmt.Errorf("write to dest: %w", err)
	}

	return os.Remove(srcPath)
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
// metadata header.
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
func validateTaskBody(content string) error {
	const remedy = "strip the leading metadata block from content: create supplies the \"# Title\" heading and the \"**Status:**\", \"**Priority:**\", \"**Parent:**\" and \"**Depends:**\" lines itself"
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
		}
	}
	return nil
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
