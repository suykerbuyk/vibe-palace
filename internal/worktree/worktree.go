// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package worktree manages git worktrees for the plan-execution workflow.
//
// Each plan runs in an isolated tree at a sibling path (../wt/<slug>) on a
// plan/<slug> branch cut from a base branch (main by default), so multiple
// /vpc-execute-plan runs proceed concurrently without contending for the main
// checkout. The human then fast-forward-merges each finished branch to main and
// coordinates the ordering — this package never merges and never touches main.
//
// It shells out to the project repo's git (not the vault): a plan worktree is
// project source-control, not vault content, so these operations carry no vault
// surface gate. The convention mirrors the epic-orchestrator's ../wt/<epic> +
// merge --ff-only pattern one level down, at single-plan granularity.
package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	slugpkg "github.com/suykerbuyk/vibe-palace/internal/slug"
)

const (
	// DefaultBase is the branch a new plan worktree is cut from when the
	// caller does not override it. Local main — this is a locally-coordinated
	// fast-forward flow, not a fetch-first origin/main one.
	DefaultBase = "main"

	// BranchPrefix namespaces plan-execution branches so `list` can tell a
	// plan worktree apart from the main checkout or any other worktree.
	BranchPrefix = "plan/"

	// siblingDir is the directory, a sibling of the repo root, that holds plan
	// worktrees: <repo-parent>/wt/<slug>.
	siblingDir = "wt"
)

// runGit runs a git subcommand in dir and returns its combined output. It is a
// package-level var so tests can pin an isolated environment; it pins
// GIT_TERMINAL_PROMPT=0 and GIT_EDITOR=true so an operation can never hang on a
// credential prompt or an interactive editor.
var runGit = func(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
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
	// DeleteBranch additionally deletes the plan/<slug> branch with a SAFE
	// delete (git branch -d), which refuses when the branch carries commits not
	// yet merged — protecting an un-fast-forwarded plan from being discarded.
	DeleteBranch bool
}

// branchName returns the default branch name for a slug: plan/<slug>.
func branchName(slug string) string { return BranchPrefix + slug }

// worktreePath returns the sibling path a slug's worktree lives at:
// <repo-parent>/wt/<slug>.
func (m *Manager) worktreePath(slug string) string {
	return filepath.Join(filepath.Dir(m.Root), siblingDir, slug)
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

// Create establishes a new plan worktree for slug at ../wt/<slug> on a
// plan/<slug> branch cut from base. An empty base defaults to DefaultBase; an
// empty branch defaults to plan/<slug>. It refuses when the worktree path or the
// branch already exists, so a re-run reports the collision instead of clobbering
// in-flight work.
func (m *Manager) Create(slug, base, branch string) (Result, error) {
	if err := slugpkg.Validate(slug); err != nil {
		return Result{}, err
	}
	if err := m.ensureRepo(); err != nil {
		return Result{}, err
	}
	if base == "" {
		base = DefaultBase
	}
	if branch == "" {
		branch = branchName(slug)
	}
	path := m.worktreePath(slug)

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
	return Result{Slug: slug, Path: path, Branch: branch, Base: base}, nil
}

// Remove tears down slug's worktree. With DeleteBranch it then safe-deletes the
// plan/<slug> branch (which git refuses if the branch is unmerged). The worktree
// is removed first because git will not delete a branch that is still checked
// out in a worktree.
func (m *Manager) Remove(slug string, opts RemoveOptions) error {
	if err := slugpkg.Validate(slug); err != nil {
		return err
	}
	if err := m.ensureRepo(); err != nil {
		return err
	}
	args := []string{"worktree", "remove"}
	if opts.Force {
		args = append(args, "--force")
	}
	args = append(args, m.worktreePath(slug))
	if _, err := runGit(m.Root, args...); err != nil {
		return err
	}
	if opts.DeleteBranch {
		branch := branchName(slug)
		if _, err := runGit(m.Root, "branch", "-d", branch); err != nil {
			return fmt.Errorf("worktree removed, but branch %s not deleted (unmerged? use git branch -D to force): %w", branch, err)
		}
	}
	return nil
}

// List returns the repo's worktrees. By default only plan/* worktrees are
// returned — the coordination surface for this workflow; all=true returns every
// worktree, including the main checkout.
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
	plans := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Branch, BranchPrefix) {
			plans = append(plans, e)
		}
	}
	return plans, nil
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
