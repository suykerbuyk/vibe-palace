// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package project

import (
	"os"
	"path/filepath"
)

// HasRootedSignal reports whether dir ITSELF is a directory a caller may be
// onboarded in place — the gate a non-interactive surface (the MCP tool) uses
// before it is allowed to write into a working tree it did not choose.
//
// It is deliberately NOT DetectSignal, and the three differences are the whole
// point:
//
//  1. NO UPWARD WALK. DetectSignal returns SignalVibeConfig the moment
//     findMarkerUpward succeeds, and that walk climbs parents until it hits the
//     exact home boundary — outside $HOME it is effectively unbounded. A remote
//     caller naming /tmp/scratch would be onboarded because some ancestor
//     carries a marker. Here every probe is against dir itself.
//
//  2. THE FORCE-SKIP STILL APPLIES. isForceSkipDir (the filesystem root, and
//     exactly $HOME) is unexported and reachable only through DetectSignal, so
//     a hand-rolled predicate would silently lose it and happily scaffold a
//     project into the operator's home directory.
//
//  3. IT ORS OVER ALL THREE SIGNALS. DetectSignal's precedence makes
//     ".vibe-palace.toml wins" observable at the return value, so
//     "DetectSignal(dir) != SignalNone && the marker lives in dir" wrongly
//     rejects a directory that has its own .git but sits under a marked
//     ancestor. Each signal is probed independently here.
//
// DetectSignal's own semantics are untouched: its upward walk is what the
// re-init and vault-resolution paths rely on.
func HasRootedSignal(dir string) bool {
	if dir == "" {
		return false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		resolved = abs
	}
	resolved = filepath.Clean(resolved)

	if isForceSkipDir(resolved) {
		return false
	}
	if !IsDir(resolved) {
		return false
	}

	// .vibe-palace.toml in dir ITSELF — never an ancestor's.
	if _, err := os.Stat(filepath.Join(resolved, ConfigFileName)); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(resolved, ".git")); err == nil {
		return true
	}
	// Same list DetectSignal consults, so the two predicates can never drift
	// about what counts as an ecosystem manifest.
	for _, name := range manifestFiles {
		if _, err := os.Stat(filepath.Join(resolved, name)); err == nil {
			return true
		}
	}
	return false
}

// IsDir reports whether p exists and is a directory. A caller that hands us a
// FILE path must not pass the gate: every artifact the working-tree steps write
// is created relative to a directory.
//
// It is exported because HasRootedSignal collapses "absent" and "present but
// unmarked" into one false, and a caller that has to EXPLAIN the false — the
// omission reason in internal/onboard — needs the same existence predicate
// rather than a second derivation of it.
func IsDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
