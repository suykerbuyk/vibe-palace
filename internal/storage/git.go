// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

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

// WriteVaultGitignore writes a .gitignore to the vault root, excluding
// machine-local data (models, caches) from version control.
func WriteVaultGitignore(vaultRoot string) error {
	content := `# Machine-local data — not synced between machines.
palace/.local/
`
	path := filepath.Join(vaultRoot, ".gitignore")
	return os.WriteFile(path, []byte(content), 0o644)
}
