// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"os"
	"os/exec"
	"path/filepath"
)

// GrokPresent reports whether this host/project looks like it uses xAI's
// Grok Build CLI, for the purpose of emitting Grok skill shims into
// .grok/skills/.
//
// Detection (not always-emit) mirrors CursorPresent's philosophy: a fresh
// `vp init` in a repo that has nothing to do with Grok must not scatter a
// `.grok/` tree. We emit only when there is a positive signal that Grok is
// in play.
//
// Returns true when ANY of:
//   - projectRoot/.grok/ exists and is a directory (the repo already has a
//     Grok surface — the strongest signal).
//   - ~/.grok/ exists and is a directory (the user runs Grok globally).
//   - `grok` is resolvable on PATH (the CLI is installed).
//
// Unlike CursorPresent, the PATH and home-dir branches make this host-wide:
// once a developer has Grok installed, every `vp init` they run wires the
// Grok shims, which is the intended ergonomics for a Grok-first user.
func GrokPresent(projectRoot string) bool {
	if info, err := os.Stat(filepath.Join(projectRoot, ".grok")); err == nil && info.IsDir() {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil {
		if info, err := os.Stat(filepath.Join(home, ".grok")); err == nil && info.IsDir() {
			return true
		}
	}
	if _, err := exec.LookPath("grok"); err == nil {
		return true
	}
	return false
}
