// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CanonicalGitignorePatterns is the growing set of .gitignore lines that
// vibe-palace considers canonical for a vault. The reconciler treats
// every entry here as an exact-line presence requirement: missing lines
// are appended at EOF in declaration order; already-present lines are
// left alone (including their position). Comment lines participate in
// the same exact-match rule so a user may override a header by moving
// or editing it — the reconciler will not re-inject it.
var CanonicalGitignorePatterns = []string{
	"# Machine-local data — not synced between machines.",
	"palace/.local/",
	"# Template reconcile sidecars",
	"*.bak",
	"*.new",
	"# Per-path advisory write locks (vaultlock) — host-local, never synced",
	".vp-locks/",
	"# Wrap commit-message scratch (Projects/<slug>/commit.msg) — host-local handoff",
	"# regenerated each /wrap, never synced; matches at any depth",
	"commit.msg",
}

// CanonicalProjectGitignorePatterns is the set of .gitignore lines that
// vibe-palace owns in a *consuming project's* repository root. These are
// the host-local AI artifacts vp writes into the project tree (CLAUDE.md,
// AGENTS.md, commit.msg, .claude/, .grok/, .vibe-palace/) and must never
// be committed. The reconciler treats every entry as an exact-line presence
// requirement: missing lines are appended at EOF in declaration order;
// already-present lines are left alone (including their position).
//
// This set is deliberately narrow: it covers ONLY vp-owned artifacts.
// Build, coverage, or binary entries (/vp, /build/, /coverage.*) are the
// consuming project's concern and are intentionally NOT managed here.
var CanonicalProjectGitignorePatterns = []string{
	"/CLAUDE.md",
	"/AGENTS.md",
	"/commit.msg",
	"/.claude/",
	"/.grok/",
	"/.vibe-palace/",
}

// ReconcileVaultGitignore ensures every pattern in
// CanonicalGitignorePatterns is present in <vaultRoot>/.gitignore as an
// exact line. Existing content — comments, blank lines, custom
// patterns, ordering — is preserved verbatim. Missing canonical lines
// append at EOF in declaration order. The file is written atomically
// via a sibling tmp-file + rename, with 0o644 permissions and exactly
// one trailing newline. Calling twice on the same vault produces
// byte-identical files.
func ReconcileVaultGitignore(vaultRoot string) error {
	// skipWhenComplete=false: the vault path always (re)writes so the
	// trailing newline is normalized even on a no-additions run.
	return reconcileGitignore(filepath.Join(vaultRoot, ".gitignore"),
		CanonicalGitignorePatterns, false)
}

// ReconcileProjectGitignore ensures every pattern in
// CanonicalProjectGitignorePatterns is present in
// <projectRoot>/.gitignore as an exact line. It mirrors
// ReconcileVaultGitignore — existing content is preserved verbatim and
// missing canonical lines append at EOF in declaration order — but is
// strictly append-only and idempotent: when every canonical line is
// already present the file is NOT touched at all (no write, no mtime
// change). Callers treat any error as non-fatal and log it.
func ReconcileProjectGitignore(projectRoot string) error {
	// skipWhenComplete=true: a no-op run must not churn the file's mtime
	// or rewrite user content that already satisfies the canonical set.
	return reconcileGitignore(filepath.Join(projectRoot, ".gitignore"),
		CanonicalProjectGitignorePatterns, true)
}

// MissingProjectGitignorePatterns returns the canonical project-root
// patterns that are absent from <projectRoot>/.gitignore, in declaration
// order. A missing file yields the full canonical set. It is read-only
// and underpins the advisory `vp check` row.
func MissingProjectGitignorePatterns(projectRoot string) ([]string, error) {
	path := filepath.Join(projectRoot, ".gitignore")
	present, err := gitignorePresentLines(path)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, p := range CanonicalProjectGitignorePatterns {
		if _, ok := present[p]; !ok {
			missing = append(missing, p)
		}
	}
	return missing, nil
}

// gitignorePresentLines reads path and returns the set of exact lines it
// contains. A nonexistent file yields an empty set with no error.
func gitignorePresentLines(path string) (map[string]struct{}, error) {
	var existing []byte
	if data, err := os.ReadFile(path); err == nil {
		existing = data
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read gitignore %s: %w", path, err)
	}
	// Strip the final newline (if any) before splitting so an empty file
	// becomes an empty set rather than a single empty-string line.
	trimmed := bytes.TrimRight(existing, "\n")
	present := make(map[string]struct{})
	if len(trimmed) > 0 {
		for _, l := range strings.Split(string(trimmed), "\n") {
			present[l] = struct{}{}
		}
	}
	return present, nil
}

// reconcileGitignore is the shared mechanism behind the vault and
// project-root reconcilers. It ensures every pattern in canonical is
// present in path as an exact line, preserving existing content verbatim
// and appending missing canonical lines at EOF in declaration order. The
// file is written atomically (sibling tmp-file + rename, 0o644, exactly
// one trailing newline).
//
// When skipWhenComplete is true and no canonical line was missing, the
// function returns without touching the file at all — no write, no mtime
// change. When false, the file is always (re)written, normalizing the
// trailing newline even on a no-additions run.
func reconcileGitignore(path string, canonical []string, skipWhenComplete bool) error {
	var existing []byte
	if data, err := os.ReadFile(path); err == nil {
		existing = data
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read gitignore %s: %w", path, err)
	}

	// Split into lines without losing empty trailing lines. We strip the
	// final newline (if any) before splitting so an empty file becomes
	// []string{} rather than []string{""}.
	trimmed := bytes.TrimRight(existing, "\n")
	var lines []string
	if len(trimmed) > 0 {
		lines = strings.Split(string(trimmed), "\n")
	}

	present := make(map[string]struct{}, len(lines))
	for _, l := range lines {
		present[l] = struct{}{}
	}

	added := 0
	for _, p := range canonical {
		if _, ok := present[p]; ok {
			continue
		}
		lines = append(lines, p)
		present[p] = struct{}{}
		added++
	}

	if skipWhenComplete && added == 0 {
		return nil
	}

	out := strings.Join(lines, "\n") + "\n"
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".gitignore.*.tmp")
	if err != nil {
		return fmt.Errorf("create tmp gitignore in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.WriteString(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write tmp gitignore: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tmp gitignore: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod tmp gitignore: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename tmp gitignore to %s: %w", path, err)
	}
	return nil
}

// GitAvailable returns true if git is found in PATH.
func GitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// GitIsRepo returns true if dir contains a .git directory.
func GitIsRepo(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && info.IsDir()
}

// GitInit runs git init in the given directory.
func GitInit(dir string) error {
	cmd := exec.Command("git", "init", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init %s: %s: %w", dir, out, err)
	}
	return nil
}
