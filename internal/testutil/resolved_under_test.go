// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testutil

import (
	"errors"
	"path/filepath"
	"testing"
)

// recordingTB records a failure instead of stopping the test, and records a
// skip too: a helper that skipped where it must fail would otherwise report as
// success, and asserting failed-and-not-skipped is what catches that.
type recordingTB struct {
	testing.TB
	failed, skipped bool
}

func (r *recordingTB) Helper()               {}
func (r *recordingTB) Fatalf(string, ...any) { r.failed = true }
func (r *recordingTB) Fatal(...any)          { r.failed = true }
func (r *recordingTB) Skip(...any)           { r.skipped = true }
func (r *recordingTB) Skipf(string, ...any)  { r.skipped = true }
func (r *recordingTB) SkipNow()              { r.skipped = true }

func TestRequireResolvedUnder(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name    string
		resolve func() (string, error)
		fail    bool
	}{
		{"inside", func() (string, error) { return filepath.Join(root, "vibe-palace", "config.toml"), nil }, false},
		{"outside", func() (string, error) { return filepath.Join(t.TempDir(), "vibe-palace", "config.toml"), nil }, true},
		{"sibling with a shared prefix", func() (string, error) { return root + "-other/config.toml", nil }, true},
		{"unresolvable fails, never skips", func() (string, error) { return "", errors.New("neither $XDG_CONFIG_HOME nor $HOME is defined") }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &recordingTB{TB: t}
			RequireResolvedUnder(r, root, tc.resolve)
			if r.failed != tc.fail || r.skipped {
				t.Errorf("failed = %v skipped = %v, want failed = %v and never skipped", r.failed, r.skipped, tc.fail)
			}
		})
	}
}
