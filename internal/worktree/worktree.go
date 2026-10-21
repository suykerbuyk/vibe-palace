// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package worktree manages git worktrees for the plan-execution workflow.
//
// Each plan runs in an isolated tree under the project root (<root>/.wt/<slug>)
// on a branch whose name equals the worktree directory name, cut from a base
// branch (main by default), so multiple /vpc-execute-plan runs proceed
// concurrently without contending for the main checkout. The human then
// fast-forward-merges each finished branch to main and coordinates the ordering
// — this package never merges and never touches main.
//
// Epics use two tiers under <root>/.wt/: an integration worktree
// <root>/.wt/<epic> on branch <epic>, and task worktrees
// <root>/.wt/<epic>__<task> on branch <epic>__<task>. Branch name equals
// directory name at every tier, so the refs stay flat (no git directory/file ref
// conflict) and the owning task of any worktree is derivable from its path alone
// (ADR-017).
//
// It shells out to the project repo's git (not the vault): a plan worktree is
// project source-control, not vault content, so these operations carry no vault
// surface gate. The <root>/.wt/ directory is gitignored, so a live worktree
// never dirties the main checkout.
package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
	slugpkg "github.com/suykerbuyk/vibe-palace/internal/slug"
)

const (
	// DefaultBase is the branch a new plan worktree is cut from when the
	// caller does not override it. Local main — this is a locally-coordinated
	// fast-forward flow, not a fetch-first origin/main one.
	DefaultBase = "main"

	// wtDir is the directory, under the project root, that holds plan
	// worktrees: <root>/.wt/<name>. It is gitignored, so a live worktree never
	// dirties the main checkout (ADR-017).
	wtDir = ".wt"

	// epicSep joins an epic and its task into a single flat name used for BOTH
	// the worktree directory and its branch: <epic>__<task>. slug.Validate
	// rejects "_" in a component, so "__" is an unambiguous boundary that splits
	// back cleanly.
	epicSep = "__"
)

// runGit runs a git subcommand in dir and returns its combined output. It is a
// package-level var so tests can pin an isolated environment; it pins
// GIT_TERMINAL_PROMPT=0 and GIT_EDITOR=true so an operation can never hang on a
// credential prompt or an interactive editor.
var runGit = func(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitenv.SafeGitEnv("GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return string(out), fmt.Errorf("git %s: %w: %s", args[0], err, msg)
		}
		return string(out), fmt.Errorf("git %s: %w", args[0], err)
	}
	return string(out), nil
}

// Manager runs worktree operations against a single project repository root.
type Manager struct {
	// Root is the project repository root (the directory containing .git).
	Root string
}

// New returns a Manager rooted at the given project repository root. Resolve
// root with wrapstate.ResolveProjectRoot before calling.
func New(root string) *Manager { return &Manager{Root: root} }

// Result describes a worktree that Create established.
type Result struct {
	Slug   string `json:"slug"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
}

// Entry is one worktree as reported by List.
type Entry struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Head   string `json:"head"`
}

// RemoveOptions tunes Remove.
type RemoveOptions struct {
	// Force passes --force to `git worktree remove`, allowing removal of a
	// worktree with a dirty or locked tree.
	Force bool
	// DeleteBranch additionally deletes the worktree's branch (= its directory
	// name) with a SAFE delete (git branch -d), which refuses when the branch
	// carries commits not yet merged — protecting an un-fast-forwarded plan from
	// being discarded.
	DeleteBranch bool
}

// branchName returns the branch name for a worktree name. Branch name equals
// directory name at every tier (ADR-017), so this is the identity — kept as a
// single named definition of that invariant.
func branchName(name string) string { return name }

// worktreePath returns the under-root path a worktree name lives at:
// <root>/.wt/<name>.
func (m *Manager) worktreePath(name string) string {
	return filepath.Join(m.Root, wtDir, name)
}

// ensureRepo verifies Root is set and is a git repository.
func (m *Manager) ensureRepo() error {
	if m.Root == "" {
		return fmt.Errorf("no project repository root resolved")
	}
	if _, err := runGit(m.Root, "rev-parse", "--git-dir"); err != nil {
		return fmt.Errorf("%s is not a git repository", m.Root)
	}
	return nil
}

// branchExists reports whether a local branch of the given name exists. A
// missing ref is reported as (false, nil); ensureRepo has already confirmed the
// repo, so a non-zero exit here means the ref is absent, not that git is broken.
func (m *Manager) branchExists(branch string) bool {
	_, err := runGit(m.Root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// create establishes a worktree named name at <root>/.wt/<name> on a branch of
// the same name cut from base. An empty base defaults to DefaultBase. It refuses
// when the worktree path or the branch already exists, so a re-run reports the
// collision instead of clobbering in-flight work. Callers validate their
// components before composing name; create does not re-validate the composite
// (which, for an epic, is intentionally not a slug).
func (m *Manager) create(name, base string) (Result, error) {
	if err := m.ensureRepo(); err != nil {
		return Result{}, err
	}
	if base == "" {
		base = DefaultBase
	}
	branch := branchName(name)
	path := m.worktreePath(name)

	if _, err := os.Stat(path); err == nil {
		return Result{}, fmt.Errorf("worktree path already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return Result{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if m.branchExists(branch) {
		return Result{}, fmt.Errorf("branch already exists: %s", branch)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{}, fmt.Errorf("create worktree parent dir: %w", err)
	}
	if _, err := runGit(m.Root, "worktree", "add", path, "-b", branch, base); err != nil {
		return Result{}, err
	}
	return Result{Slug: name, Path: path, Branch: branch, Base: base}, nil
}

// Create establishes a single-task worktree at <root>/.wt/<slug> on branch
// <slug>, cut from base (DefaultBase when empty).
func (m *Manager) Create(slug, base string) (Result, error) {
	if err := slugpkg.Validate(slug); err != nil {
		return Result{}, err
	}
	return m.create(slug, base)
}

// CreateEpicIntegration establishes an epic's integration worktree at
// <root>/.wt/<epic> on branch <epic>. Task worktrees fast-forward into this
// branch.
func (m *Manager) CreateEpicIntegration(epic, base string) (Result, error) {
	if err := slugpkg.Validate(epic); err != nil {
		return Result{}, fmt.Errorf("epic: %w", err)
	}
	return m.create(epic, base)
}

// CreateEpicTask establishes an epic task worktree at <root>/.wt/<epic>__<task>
// on branch <epic>__<task>. Each of epic and task is validated as a bare slug;
// the composite is derived, never passed through slug.Validate.
func (m *Manager) CreateEpicTask(epic, task, base string) (Result, error) {
	if err := slugpkg.Validate(epic); err != nil {
		return Result{}, fmt.Errorf("epic: %w", err)
	}
	if err := slugpkg.Validate(task); err != nil {
		return Result{}, fmt.Errorf("task: %w", err)
	}
	return m.create(epic+epicSep+task, base)
}

// remove tears down the worktree named name. With DeleteBranch it then
// safe-deletes the branch of the same name (git refuses if unmerged). The
// worktree is removed first because git will not delete a branch that is still
// checked out in a worktree.
func (m *Manager) remove(name string, opts RemoveOptions) error {
	if err := m.ensureRepo(); err != nil {
		return err
	}
	args := []string{"worktree", "remove"}
	if opts.Force {
		args = append(args, "--force")
	}
	args = append(args, m.worktreePath(name))
	if _, err := runGit(m.Root, args...); err != nil {
		return err
	}
	if opts.DeleteBranch {
		branch := branchName(name)
		if _, err := runGit(m.Root, "branch", "-d", branch); err != nil {
			return fmt.Errorf("worktree removed, but branch %s not deleted (unmerged? use git branch -D to force): %w", branch, err)
		}
	}
	return nil
}

// Remove tears down a single-task worktree <root>/.wt/<slug> and (with
// DeleteBranch) its branch <slug>.
func (m *Manager) Remove(slug string, opts RemoveOptions) error {
	if err := slugpkg.Validate(slug); err != nil {
		return err
	}
	return m.remove(slug, opts)
}

// RemoveEpicTask tears down an epic task worktree <root>/.wt/<epic>__<task> and
// (with DeleteBranch) its branch <epic>__<task>.
func (m *Manager) RemoveEpicTask(epic, task string, opts RemoveOptions) error {
	if err := slugpkg.Validate(epic); err != nil {
		return fmt.Errorf("epic: %w", err)
	}
	if err := slugpkg.Validate(task); err != nil {
		return fmt.Errorf("task: %w", err)
	}
	return m.remove(epic+epicSep+task, opts)
}

// RemoveEpicIntegration removes an epic's integration worktree <root>/.wt/<epic>
// and (with DeleteBranch) its branch <epic>. It REFUSES while any task worktree
// or task branch for the epic still exists — a <root>/.wt/<epic>__* directory or
// a refs/heads/<epic>__* branch — so the integration tree the tasks fast-forward
// into is never torn down from under an in-flight task (ADR-017 decision 6).
func (m *Manager) RemoveEpicIntegration(epic string, opts RemoveOptions) error {
	if err := slugpkg.Validate(epic); err != nil {
		return fmt.Errorf("epic: %w", err)
	}
	if err := m.ensureRepo(); err != nil {
		return err
	}
	remaining, err := m.epicTaskLeftovers(epic)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return fmt.Errorf("refusing to remove epic integration %q: task worktree(s)/branch(es) still present: %s", epic, strings.Join(remaining, ", "))
	}
	return m.remove(epic, opts)
}

// epicTaskLeftovers reports any task worktree directories (<root>/.wt/<epic>__*)
// or task branches (refs/heads/<epic>__*) still present for the epic. Because
// slug.Validate rejects "_" in a component, the "<epic>__" prefix is an
// unambiguous boundary — an epic "foo" never matches "foobar__…".
func (m *Manager) epicTaskLeftovers(epic string) ([]string, error) {
	prefix := epic + epicSep
	var found []string

	wtRoot := filepath.Join(m.Root, wtDir)
	ents, err := os.ReadDir(wtRoot)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", wtRoot, err)
	}
	for _, e := range ents {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			found = append(found, filepath.Join(wtDir, e.Name()))
		}
	}

	out, err := runGit(m.Root, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && strings.HasPrefix(line, prefix) {
			found = append(found, "branch "+line)
		}
	}
	return found, nil
}

// List returns the repo's .wt/ worktrees. By default only worktrees whose parent
// directory is exactly <root>/.wt are returned — the coordination surface for
// this workflow; all=true returns every worktree, including the main checkout
// and any harness-owned trees (e.g. .claude/worktrees/*).
func (m *Manager) List(all bool) ([]Entry, error) {
	if err := m.ensureRepo(); err != nil {
		return nil, err
	}
	out, err := runGit(m.Root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	entries := parseWorktreeList(out)
	if all {
		return entries, nil
	}
	if len(entries) == 0 {
		return nil, nil
	}
	// Discriminate by PATH, not branch name: a vibe-palace worktree is one whose
	// parent directory is exactly <root>/.wt. `git worktree list` reports the
	// MAIN worktree first and in git's own absolute, symlink-resolved form, so
	// joining .wt to entries[0].Path yields the same form the children are
	// reported in — no need to reconcile m.Root's symlinks separately. Parent-dir
	// equality (not ancestry) keeps the main checkout, .claude/worktrees/*, and
	// any nested layout out, while including both epic tiers (.wt/<epic> and
	// .wt/<epic>__<task>), whose parent is .wt.
	wtRoot := filepath.Join(entries[0].Path, wtDir)
	ours := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if filepath.Dir(e.Path) == wtRoot {
			ours = append(ours, e)
		}
	}
	return ours, nil
}

// ReconcileOptions tunes Reconcile.
type ReconcileOptions struct {
	// CheckRemote enables the single live-remote probe of main that gates
	// "reclaimable", plus the per-branch live-PR probe for an unmerged tree. It is
	// the default (callers pass true). When false, no network is touched:
	// MainOnRemote reports "unverified" and nothing is reclaimable — a deliberate
	// opt-out that never silently promotes a tree to reclaimable.
	CheckRemote bool
}

// ReconcileEntry is one worktree, classified. Merged and Pushed are tri-state
// strings ("yes"/"no"/"unverified"); Pushed is populated only for an unmerged
// vibe-palace tree (to distinguish a possible live PR) and is "" otherwise.
type ReconcileEntry struct {
	Path         string `json:"path"`
	Branch       string `json:"branch"`
	Name         string `json:"name"`
	Role         string `json:"role"` // "main" | "harness" | "vibe-palace"
	OnConvention bool   `json:"on_convention"`
	Merged       string `json:"merged"` // "yes" | "no" | "unverified"
	Dirty        bool   `json:"dirty"`
	Pushed       string `json:"pushed,omitempty"` // "yes" | "no" | "unverified"
	Verdict      string `json:"verdict"`          // "reclaimable" | "keep" | "skip"
	Reason       string `json:"reason,omitempty"`
}

// ReconcileReport is the read-only reconcile result: the shared main-on-remote
// verdict (computed once) and one entry per worktree. Strictly report-only —
// Reconcile never removes, deletes, or writes anything (it mirrors planscan).
type ReconcileReport struct {
	MainOnRemote string           `json:"main_on_remote"` // "yes" | "no" | "unverified"
	Entries      []ReconcileEntry `json:"entries"`
}

// Reconcile enumerates every worktree (List(all=true)), classifies each, and
// reports whether it is safe to reclaim. It is strictly read-only: it shells git
// only for rev-parse / merge-base / status / ls-remote, and it NEVER fetches,
// NEVER reads a tracking ref (@{u}) or `git status` ahead-behind, and NEVER
// removes anything. The acceptance signal for a reclaim is whether local main is
// on the LIVE remote (one ls-remote), computed once and shared across entries.
func (m *Manager) Reconcile(opts ReconcileOptions) (ReconcileReport, error) {
	if err := m.ensureRepo(); err != nil {
		return ReconcileReport{}, err
	}
	entries, err := m.List(true)
	if err != nil {
		return ReconcileReport{}, err
	}
	report := ReconcileReport{MainOnRemote: "unverified", Entries: []ReconcileEntry{}}
	if len(entries) == 0 {
		return report, nil
	}
	root := entries[0].Path // canonical root, git's own form (the basis List uses)
	wtRoot := filepath.Join(root, wtDir)
	harnessRoot := filepath.Join(root, ".claude", "worktrees")

	// Resolve the base (main) tip once. Empty when main is absent — every tree is
	// then Merged=unverified / keep (never reclaimable).
	mainSHA := m.refSHA(DefaultBase)

	// The one acceptance probe: is local main on the live remote? Computed ONCE.
	if opts.CheckRemote && mainSHA != "" {
		report.MainOnRemote = m.remoteContains(DefaultBase, mainSHA)
	}

	for _, e := range entries {
		report.Entries = append(report.Entries, m.classifyWorktree(e, opts, root, wtRoot, harnessRoot, mainSHA, report.MainOnRemote))
	}
	return report, nil
}

// classifyWorktree applies the ADR-017 decision-6 classification to one worktree.
// Role and on-convention are pure path classification (no git call); git probes
// run only when the branch is a real ref, and a failed probe is a classification
// (unverified), never a hard error.
func (m *Manager) classifyWorktree(e Entry, opts ReconcileOptions, root, wtRoot, harnessRoot, mainSHA, mainOnRemote string) ReconcileEntry {
	ent := ReconcileEntry{Path: e.Path, Branch: e.Branch, Name: filepath.Base(e.Path)}

	switch {
	case e.Path == root:
		ent.Role, ent.Verdict, ent.Reason = "main", "skip", "main checkout"
		return ent
	case filepath.Dir(e.Path) == harnessRoot:
		ent.Role, ent.Verdict, ent.Reason = "harness", "skip", "harness-owned (.claude/worktrees)"
		return ent
	}
	ent.Role = "vibe-palace"

	realRef := e.Branch != "" && e.Branch != "(detached)"
	ent.OnConvention = filepath.Dir(e.Path) == wtRoot && realRef && e.Branch == ent.Name
	ent.Dirty = !m.worktreeClean(e.Path)

	switch {
	case !realRef || mainSHA == "":
		ent.Merged = "unverified"
	default:
		bSHA := m.refSHA(e.Branch)
		switch {
		case bSHA == "":
			ent.Merged = "unverified"
		case m.isAncestor(bSHA, mainSHA):
			ent.Merged = "yes"
		default:
			ent.Merged = "no"
		}
	}

	if !ent.OnConvention {
		ent.Verdict, ent.Reason = "keep", "off-convention (hand-rolled?) — vp worktree create owns path+branch"
		return ent
	}
	if ent.Dirty {
		ent.Verdict, ent.Reason = "keep", "uncommitted work"
		return ent
	}
	switch ent.Merged {
	case "unverified":
		ent.Verdict, ent.Reason = "keep", "could not verify against main — keep"
		return ent
	case "no":
		if opts.CheckRemote {
			ent.Pushed = m.remoteContains(e.Branch, m.refSHA(e.Branch))
		}
		if ent.Pushed == "yes" {
			ent.Verdict, ent.Reason = "keep", "unmerged; branch is on the remote (possible live PR) — do not delete"
		} else {
			ent.Verdict, ent.Reason = "keep", "unmerged commits not in main"
		}
		return ent
	}

	// Merged == "yes": the work is in local main. Reclaim only if local main is on
	// the live remote (ADR-017 decision 6 — remove after the operator's push).
	if mainOnRemote != "yes" {
		ent.Verdict, ent.Reason = "keep", "merged into local main; main is not on the remote — push before remove"
		return ent
	}

	// Epic-integration guard: a name without epicSep MAY be an epic integration
	// tree; it is reclaimable only when no task tree/branch remains — the SAME
	// predicate RemoveEpicIntegration enforces, so the report and
	// `vp worktree remove --epic` cannot disagree. (For a single-task name this
	// finds nothing and is a no-op.)
	if !strings.Contains(ent.Name, epicSep) {
		if leftovers, lerr := m.epicTaskLeftovers(ent.Name); lerr == nil && len(leftovers) > 0 {
			ent.Verdict, ent.Reason = "keep", "epic task worktrees/branches still present"
			return ent
		}
	}

	ent.Verdict = "reclaimable"
	return ent
}

// refSHA returns the commit a local branch points at, or "" if the ref is
// missing (rev-parse --verify --quiet exits non-zero, surfaced by runGit as an
// error — reported here as absence, not failure).
func (m *Manager) refSHA(branch string) string {
	out, err := runGit(m.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// isAncestor reports whether commit a is an ancestor of commit b. Both SHAs must
// already be verified-present; a non-zero exit then means "not an ancestor".
func (m *Manager) isAncestor(a, b string) bool {
	_, err := runGit(m.Root, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// worktreeClean reports whether the worktree at path has no uncommitted changes.
// An unreadable status returns false (not confirmed clean) so such a tree is
// never reclaimed.
func (m *Manager) worktreeClean(path string) bool {
	out, err := runGit(path, "status", "--porcelain")
	return err == nil && strings.TrimSpace(out) == ""
}

// remoteContains reports whether localSHA is on the live remote for ref: "yes"
// when a configured/known remote carries the ref at localSHA (or with localSHA a
// locally-decidable ancestor of the remote tip), "no" when a remote is reachable
// but does not, and "unverified" when no remote is configured or none could be
// reached. It uses `git ls-remote` ONLY — never a tracking ref, never a fetch.
// Worst-of across remotes: any "no" wins, else any "yes", else "unverified".
func (m *Manager) remoteContains(ref, localSHA string) string {
	if localSHA == "" {
		return "unverified"
	}
	remotes := m.remotesFor(ref)
	if len(remotes) == 0 {
		return "unverified"
	}
	anyNo, anyYes := false, false
	for _, r := range remotes {
		switch m.remoteRefVerdict(r, ref, localSHA) {
		case "no":
			anyNo = true
		case "yes":
			anyYes = true
		}
	}
	switch {
	case anyNo:
		return "no"
	case anyYes:
		return "yes"
	default:
		return "unverified"
	}
}

// remoteRefVerdict probes one remote for refs/heads/<ref> via ls-remote (live,
// no fetch). "yes" if the remote tip equals localSHA or localSHA is a
// locally-decidable ancestor of it; "no" if the remote lacks the ref or carries
// a tip local cannot prove it is behind; "unverified" if the remote is
// unreachable.
func (m *Manager) remoteRefVerdict(remote, ref, localSHA string) string {
	out, err := runGit(m.Root, "ls-remote", remote, "refs/heads/"+ref)
	if err != nil {
		return "unverified"
	}
	line := strings.TrimSpace(out)
	if line == "" {
		return "no" // remote has no such branch
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "unverified"
	}
	remoteSHA := fields[0]
	if remoteSHA == localSHA {
		return "yes"
	}
	// Differs: say "yes" only if local is provably behind (the remote tip is a
	// known local object with localSHA as an ancestor). Otherwise "no" — never
	// falsely reclaim, and never fetch to find out.
	if m.isAncestor(localSHA, remoteSHA) {
		return "yes"
	}
	return "no"
}

// remotesFor returns the remotes to probe for a branch: its configured upstream
// (branch.<b>.remote) if set, else every configured remote.
func (m *Manager) remotesFor(branch string) []string {
	if out, err := runGit(m.Root, "config", "--get", "branch."+branch+".remote"); err == nil {
		if r := strings.TrimSpace(out); r != "" {
			return []string{r}
		}
	}
	out, err := runGit(m.Root, "remote")
	if err != nil {
		return nil
	}
	var remotes []string
	for _, line := range strings.Split(out, "\n") {
		if r := strings.TrimSpace(line); r != "" {
			remotes = append(remotes, r)
		}
	}
	return remotes
}

// parseWorktreeList parses `git worktree list --porcelain` output. Records are
// separated by blank lines; each has a `worktree <path>` line, a `HEAD <sha>`
// line, and either `branch refs/heads/<name>` or a bare `detached` marker.
func parseWorktreeList(out string) []Entry {
	var entries []Entry
	var cur Entry
	started := false
	flush := func() {
		if started {
			entries = append(entries, cur)
		}
		cur = Entry{}
		started = false
	}
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			flush()
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			cur.Path = val
			started = true
		case "HEAD":
			cur.Head = val
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "detached":
			cur.Branch = "(detached)"
		}
	}
	flush()
	return entries
}
