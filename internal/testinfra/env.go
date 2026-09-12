// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testinfra

import (
	"os"
	"path/filepath"
	"testing"
)

// Env holds one test's isolated HOME/XDG_CONFIG_HOME (and, if requested,
// CLAUDE_HOME) directories, plus enough to reconstruct a subprocess's
// environment that must observe the same isolation.
type Env struct {
	Home          string
	XDGConfigHome string
	ClaudeHome    string // empty unless WithClaudeHome() was passed
}

// EnvOption configures IsolateEnv beyond its HOME/XDG_CONFIG_HOME baseline.
type EnvOption func(*Env, *testing.T)

// WithClaudeHome also isolates CLAUDE_HOME to a fresh t.TempDir(), for tests
// that exercise Claude-host session-map or cwd-encoded native-dir resolution
// (internal/archive/claude.go, internal/memory's NativeDirFromCwd path).
func WithClaudeHome() EnvOption {
	return func(e *Env, t *testing.T) {
		t.Helper()
		e.ClaudeHome = t.TempDir()
		t.Setenv("CLAUDE_HOME", e.ClaudeHome)
	}
}

// IsolateEnv sets HOME to a fresh t.TempDir() and XDG_CONFIG_HOME to that
// SAME directory's <Home>/.config subdirectory (nested, matching
// internal/integration/template_reconcile_test.go's setupFreshEnv exemplar
// exactly) — NOT two independent temp roots. Both are set via t.Setenv, so
// t.Setenv panics if called after t.Parallel() on the same test: callers
// must call IsolateEnv (and any options) BEFORE t.Parallel().
//
// The XDG_CONFIG_HOME directory is created (MkdirAll) so callers that write
// directly under it — rather than through a package function that creates
// its own parents — do not need a redundant mkdir of their own.
func IsolateEnv(t *testing.T, opts ...EnvOption) *Env {
	t.Helper()

	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	if err := os.MkdirAll(xdg, 0o755); err != nil {
		t.Fatalf("IsolateEnv: mkdir XDG_CONFIG_HOME %s: %v", xdg, err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	e := &Env{Home: home, XDGConfigHome: xdg}
	for _, opt := range opts {
		opt(e, t)
	}
	return e
}

// Environ returns os.Environ() with this Env's isolated variables applied on
// top (last-wins per os/exec semantics), for building a subprocess's
// cmd.Env. extra is appended after the isolated vars, letting a caller layer
// on its own overrides (e.g. a fake-binary PATH prepend) without
// reconstructing the base set by hand.
func (e *Env) Environ(extra ...string) []string {
	env := append(os.Environ(),
		"HOME="+e.Home,
		"XDG_CONFIG_HOME="+e.XDGConfigHome,
	)
	if e.ClaudeHome != "" {
		env = append(env, "CLAUDE_HOME="+e.ClaudeHome)
	}
	return append(env, extra...)
}
