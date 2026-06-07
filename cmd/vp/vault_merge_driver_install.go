// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// gitattributesEntry is the single line appended to the vault's
// .gitattributes to bind *.surface files to the vp-surface merge driver.
const gitattributesEntry = "*.surface merge=vp-surface"

// gitconfigSectionHeader is the section header we look for in ~/.gitconfig. If
// found, we assume the section is intact (we don't validate the driver= line —
// install is best-effort and a half-installed gitconfig is the operator's
// problem).
const gitconfigSectionHeader = `[merge "vp-surface"]`

// gitconfigSectionBody is the literal block appended to ~/.gitconfig when the
// section is missing. Ends with a newline so subsequent sections format
// correctly. The driver command relies on `vp` being on PATH (matching the
// install convention used by the sibling vibe-vault driver).
const gitconfigSectionBody = `[merge "vp-surface"]
	name = vibe-palace surface stamp merge driver
	driver = vp vault merge-driver %O %A %B
`

// EnsureMergeDriverInstalled idempotently installs the vp-surface merge driver
// into:
//   - <vaultPath>/.gitattributes  (one line: `*.surface merge=vp-surface`)
//   - $HOME/.gitconfig            (a `[merge "vp-surface"]` section)
//
// Returns (installed, error) where installed=true means at least one of the
// two files needed work — useful for a caller-side "newly installed" notice.
// Re-running after a successful install is a no-op (returns (false, nil)), so
// the auto-invoke on every `vp vault pull/sync` against the live remote stays
// idempotent across repeated real pulls.
//
// The function reads ~/.gitconfig directly rather than shelling out to
// `git config --global` so the call is hermetic and testable via
// t.Setenv("HOME", t.TempDir()).
func EnsureMergeDriverInstalled(vaultPath string) (bool, error) {
	installed := false

	gaInstalled, err := ensureGitattributesEntry(vaultPath)
	if err != nil {
		return installed, err
	}
	installed = installed || gaInstalled

	gcInstalled, err := ensureGitconfigSection()
	if err != nil {
		return installed, err
	}
	installed = installed || gcInstalled

	return installed, nil
}

// ensureGitattributesEntry adds the *.surface merge=vp-surface line to
// <vaultPath>/.gitattributes if missing. Creates the file when absent. An
// empty vaultPath is a graceful no-op (defensive: the no-remote / unresolved
// vault path).
func ensureGitattributesEntry(vaultPath string) (bool, error) {
	if vaultPath == "" {
		return false, nil
	}
	path := filepath.Join(vaultPath, ".gitattributes")
	existing, err := readFileOrEmpty(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if containsLine(existing, gitattributesEntry) {
		return false, nil
	}

	// Append (with a leading newline if the file is non-empty and doesn't
	// already end with one) so we don't fuse our line onto a previous one.
	var buf bytes.Buffer
	buf.Write(existing)
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString(gitattributesEntry)
	buf.WriteByte('\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Fprintf(os.Stderr, "vp: registering vibe-palace surface merge driver in %s\n", path)
	return true, nil
}

// ensureGitconfigSection adds a [merge "vp-surface"] section to ~/.gitconfig
// if missing. Honors $HOME (set via t.Setenv in tests) and creates the file
// when absent.
func ensureGitconfigSection() (bool, error) {
	home, err := userHome()
	if err != nil {
		return false, err
	}
	path := filepath.Join(home, ".gitconfig")
	existing, err := readFileOrEmpty(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if bytes.Contains(existing, []byte(gitconfigSectionHeader)) {
		return false, nil
	}

	var buf bytes.Buffer
	buf.Write(existing)
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString(gitconfigSectionBody)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Fprintf(os.Stderr, "vp: registering vibe-palace surface merge driver in %s\n", path)
	return true, nil
}

// userHome returns $HOME if set (preferred — honors t.Setenv), else falls back
// to os.UserHomeDir.
func userHome() (string, error) {
	if h := os.Getenv("HOME"); h != "" {
		return h, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return h, nil
}

// readFileOrEmpty returns the file contents, or (nil, nil) when the file does
// not exist. Any other read error is returned.
func readFileOrEmpty(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// containsLine reports whether `line` appears as an exact, full-line match
// anywhere in `data` (split on '\n'). Tolerates leading/trailing whitespace on
// each line so a hand-edited file with tabs/spaces still hits.
func containsLine(data []byte, line string) bool {
	for _, raw := range bytes.Split(data, []byte("\n")) {
		trimmed := bytes.TrimSpace(raw)
		if string(trimmed) == line {
			return true
		}
	}
	return false
}
