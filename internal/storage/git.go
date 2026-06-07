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
	path := filepath.Join(vaultRoot, ".gitignore")
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
	for _, p := range CanonicalGitignorePatterns {
		if _, ok := present[p]; ok {
			continue
		}
		lines = append(lines, p)
		present[p] = struct{}{}
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

