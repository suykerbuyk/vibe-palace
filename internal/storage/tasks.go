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
func (v *Vault) AmendTask(project, taskSlug, section, body string) error {
	section = strings.TrimSpace(section)
	if section == "" {
		return fmt.Errorf("amend %q: section is required — it names the H2 heading to replace or append", taskSlug)
	}
	if strings.ContainsAny(section, "\r\n") {
		return fmt.Errorf("amend %q: section must be a single line, got %q", taskSlug, section)
	}
	if strings.HasPrefix(section, "#") {
		return fmt.Errorf(
			"amend %q: section is the heading TEXT, not the markup — pass %q, not %q",
			taskSlug, strings.TrimLeft(strings.TrimSpace(section), "# "), section)
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("amend %q: body is required — an empty section records nothing", taskSlug)
	}
	if err := validateTaskBody(body); err != nil {
		return err
	}
	if err := validateAmendBody(body); err != nil {
		return err
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
			return fmt.Errorf("task %q not found in project %q (amend works on ACTIVE tasks only)", taskSlug, project)
		}
		return fmt.Errorf("read task: %w", err)
	}

	return atomicfile.Write(v.Root, path, []byte(upsertSection(string(data), section, body)))
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
func upsertSection(content, section, body string) string {
	rendered := "## " + section + "\n\n" + strings.TrimRight(body, "\n")

	start, end, found := sectionBounds(content, section)
	if !found {
		base := strings.TrimRight(content, "\n")
		if base == "" {
			return rendered + "\n"
		}
		return base + "\n\n" + rendered + "\n"
	}

	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	out = append(out, lines[:start]...)
	out = append(out, strings.Split(rendered, "\n")...)
	if end < len(lines) {
		out = append(out, "") // blank line before the section that follows
		out = append(out, lines[end:]...)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
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

	// Never overwrite an existing destination. lockedWrite is an unconditional
	// atomic overwrite, so a re-retire of a duplicate slug would clobber the
	// historical done/ (or cancelled/) record in place. That is the actual
	// data-loss mechanism; surface it as a bug state rather than destroy the
	// prior record.
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("cannot move task %q to %q: a task of that slug already exists there — refusing to overwrite the existing record", slug, destPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat dest: %w", err)
	}

	if err := v.lockedWrite(destPath, []byte(updated)); err != nil {
		return fmt.Errorf("write to dest: %w", err)
	}

	// 🔴 THIS STAYS SPLIT — write-dest-then-unlink-source — and is deliberately
	// NOT routed through vaultfs.Move.
	//
	// Move is os.Rename, and a rename cannot rewrite content. This operation
	// DOES rewrite content: replaceStatusLine above stamps "retired" or
	// "cancelled" into the body, and that new body is what lands in done/. So
	// the only way to express this as a real move is rename-then-rewrite, which
	// is strictly worse: a crash between the two steps leaves a file sitting in
	// done/ still declaring itself In Progress. That is not hypothetical — it is
	// the open bug `retired-task-files-keep-a-live-status-line`, and reordering
	// here would widen exactly the window that produced it. The current order
	// fails safe: the worst outcome is both copies existing, with the
	// destination already correct.
	//
	// Only the removal half is F2. The destination half is F1 (lockedWrite),
	// and the refuse-existing-destination rule stays the stat above — the same
	// policy vaultfs.Move enforces, kept here because this does not go through
	// vaultfs.Move. Retire/cancel semantics are unchanged.

	// 🔴 SEQUENTIAL LOCKS, NEVER NESTED (ADR-003). The source lock is acquired
	// only AFTER lockedWrite above has taken, used and RELEASED the
	// destination's lock. Do not hoist this acquire to the top of the function
	// to cover the read as well: that would hold the source lock across
	// lockedWrite's acquire of the destination, creating the first two-lock
	// nesting in the package and with it a lock ORDER that a future caller
	// could invert. vaultlock.Acquire is a blocking LOCK_EX with no timeout, so
	// an inversion is a permanent hang, not a detectable error.
	//
	// What this closes: the unlink is now serialized against UpdateTaskStatus,
	// which holds this same per-path lock across its whole read→rewrite
	// (see the Acquire in UpdateTaskStatus). Before this, a status update could
	// land its atomicfile.Write on a path this function was about to unlink and
	// the write was lost with no error on either side.
	//
	// What it does NOT close, deliberately: the os.ReadFile above still runs
	// unlocked, so a status update that lands between that read and this unlink
	// is still lost — the body copied to the destination is the pre-update one.
	// Closing that would require holding the source lock across lockedWrite,
	// i.e. the nesting this comment forbids. The window narrows from
	// read→write→unlink to read→write, and the operation was never atomic
	// across a crash anyway (ADR-003: "Locking never claimed to fix that").
	//
	// vaultfs.RemoveNoLock is correct here and must stay: it is the F2 sink and
	// deliberately never acquires, precisely so the lock can live at the call
	// site — the same shape as storage.DeleteMemory.
	release, err := vaultlock.Acquire(v.Root, srcPath)
	if err != nil {
		return fmt.Errorf("move task: lock %s: %w", srcPath, err)
	}
	defer release()
	return vaultfs.RemoveNoLock(srcPath)
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
	var h1, status, priority int
	for i, l := range outside {
		lines[i] = l.Text
		if isH1Line(l.Text) {
			h1++
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
func (v *Vault) OverwriteTaskFile(project, slug, content string) error {
	path, _, err := v.resolveTaskFile(project, slug)
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

	return atomicfile.Write(v.Root, path, []byte(content))
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
