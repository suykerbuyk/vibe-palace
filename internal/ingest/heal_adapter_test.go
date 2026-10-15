// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/search"
)

// fakeHealEngine satisfies graphHealEngine without a real search engine, so the
// adapter's mapping can be tested in isolation.
type fakeHealEngine struct {
	out  search.HealOutcome
	err  error
	seen struct {
		vaultRoot, project string
	}
}

func (f *fakeHealEngine) HealGraph(_ context.Context, vaultRoot, project string) (search.HealOutcome, error) {
	f.seen.vaultRoot, f.seen.project = vaultRoot, project
	return f.out, f.err
}

// Test 9: NewEngineHealer returns a GraphHealer whose HealGraph forwards the
// (vault, project) args and maps search.HealOutcome field-for-field to
// HealResult. The compile-time assertion var _ GraphHealer = engineHealer{}
// lives in heal.go. Mutant: an adapter that drops a field or the error.
func TestEngineHealerMapsResult(t *testing.T) {
	fe := &fakeHealEngine{out: search.HealOutcome{Rebuilt: true, Deleted: false, Reason: "fingerprint mismatch"}}
	var h GraphHealer = NewEngineHealer(fe)
	r, err := h.HealGraph(context.Background(), "/vault/root", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if r != (HealResult{Rebuilt: true, Deleted: false, Reason: "fingerprint mismatch"}) {
		t.Fatalf("mapped result = %+v, want the search outcome's fields", r)
	}
	if fe.seen.vaultRoot != "/vault/root" || fe.seen.project != "proj" {
		t.Fatalf("adapter did not forward args: %+v", fe.seen)
	}

	sentinel := errors.New("boom")
	eh := NewEngineHealer(&fakeHealEngine{err: sentinel})
	if _, err := eh.HealGraph(context.Background(), "v", "p"); !errors.Is(err, sentinel) {
		t.Fatalf("adapter swallowed the error: %v", err)
	}
}

// TestDepsGraphHealerDriftGuard fails if a Deps literal across the production
// and test wiring does not set GraphHealer. It uses filepath.WalkDir (never
// `git ls-files`, which skips new untracked files) over the four packages that
// construct a Deps, and counts the literal against the GraphHealer assignments.
// A future literal that forgets the healer breaks this (S5 residual:
// fixture_test.go is a bare in-package Deps the ingest.Deps grep missed).
func TestDepsGraphHealerDriftGuard(t *testing.T) {
	root := repoRoot(t)
	// Needles built by concatenation so THIS test file never matches itself.
	depsNeedle := "Deps" + "{"
	healerNeedle := "GraphHealer" + ":"
	dirs := []string{
		filepath.Join("internal", "ingest"),
		filepath.Join("internal", "integration"),
		filepath.Join("cmd", "vp"),
		filepath.Join("internal", "tools"),
	}
	deps, healers := 0, 0
	for _, d := range dirs {
		abs := filepath.Join(root, d)
		err := filepath.WalkDir(abs, func(path string, de os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if de.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			s := string(b)
			deps += strings.Count(s, depsNeedle)
			healers += strings.Count(s, healerNeedle)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", abs, err)
		}
	}
	const want = 6 // cmd_drain_archives, cmd_index_rebuild, system_tools, session_pipeline_test, fixture_test, rebuild_marker_test (markerProbe)
	if deps != want {
		t.Fatalf("found %d Deps literals, want %d — a new one must wire GraphHealer (update this count deliberately)", deps, want)
	}
	if healers != deps {
		t.Fatalf("found %d Deps literals but %d set GraphHealer — every Deps literal must set it", deps, healers)
	}
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from the test directory")
		}
		dir = parent
	}
}
