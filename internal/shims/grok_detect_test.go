// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGrokPresentProjectDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".grok"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !GrokPresent(root) {
		t.Error(".grok/ present but GrokPresent = false")
	}
}

func TestGrokPresentRejectsFileNamedGrok(t *testing.T) {
	// A regular file named .grok is not a Grok surface.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".grok"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Only the projectRoot branch is asserted here: it must not fire on a
	// plain file. The home-dir / PATH branches are environment-dependent so
	// the result is only deterministic when grok is absent from both.
	if _, err := exec.LookPath("grok"); err != nil {
		if home, herr := os.UserHomeDir(); herr != nil || !dirExists(filepath.Join(home, ".grok")) {
			if GrokPresent(root) {
				t.Error(".grok file (not dir) triggered GrokPresent — must not")
			}
		}
	}
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func TestGrokPresentHomeDir(t *testing.T) {
	// os.UserHomeDir() honors $HOME on unix; point it at a temp home that
	// has a ~/.grok dir, with a project root that has none, so only the
	// home-dir branch can fire. Neutralize PATH so grok-on-PATH cannot.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".grok"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	if !GrokPresent(t.TempDir()) {
		t.Error("~/.grok present but GrokPresent = false")
	}
}

func TestGrokPresentPATH(t *testing.T) {
	// Stage an executable named "grok" on PATH; with a Grok-free home and
	// project root, only the LookPath branch can fire.
	bin := t.TempDir()
	grok := filepath.Join(bin, "grok")
	if err := os.WriteFile(grok, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	if !GrokPresent(t.TempDir()) {
		t.Error("grok on PATH but GrokPresent = false")
	}
}

func TestGrokAbsent(t *testing.T) {
	// Grok-free project, home, and PATH → false.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if GrokPresent(t.TempDir()) {
		t.Error("Grok-free environment reported as present")
	}
}
