// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package taskgraph_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/taskgraph"
)

// These tests run against the REAL vault, not a fixture, and skip when it is not
// reachable. That is not a shortcut — it is the whole point.
//
// A test that SEEDS "**Depends:** a, b" into a fixture and asserts it round-trips
// cannot catch the bug that actually matters here, which is READING A RELATION
// NOBODY WROTE. Only real task bodies contain the fenced snippets, the prose
// discussing the schema, and the accumulated markdown weirdness of a hundred
// files written over months. This project has been bitten by exactly that gap
// twice: note_path was empty on every session for six months with a green suite,
// and eight write-only SessionMeta fields survived because the tests seeded the
// fields themselves.
func liveVault(t *testing.T) (*storage.Vault, string) {
	t.Helper()

	// Resolve, never recall. OpenVaultGlobal reads the configured vault_path —
	// the vault lives at a different absolute path on every machine, so a
	// hardcoded one is a fact about the host that wrote it and a lie everywhere
	// else. (It also means this test does not reimplement config resolution,
	// which is the other half of the same lesson.)
	v, err := storage.OpenVaultGlobal()
	if err != nil {
		t.Skipf("no vault configured on this host: %v", err)
	}
	if _, err := os.Stat(v.Root); err != nil {
		t.Skipf("configured vault is not present on this host: %v", err)
	}
	return v, "vibe-palace"
}

// TestLiveVaultHasNoPhantomRelations is the canary.
//
// Every task file written before relations existed carries neither a **Parent:**
// nor a **Depends:** line. If the parser ever regresses to a whole-file scan,
// the task files that DISCUSS the schema in their bodies — and several do, in
// prose and inside fences — will start reporting relations nobody wrote. This
// test reads the real corpus and asserts that has not happened.
func TestLiveVaultHasNoPhantomRelations(t *testing.T) {
	v, project := liveVault(t)

	tasks, err := v.ListTasks(project, true)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) < 50 {
		t.Skipf("live vault has only %d tasks; this canary needs the real corpus", len(tasks))
	}

	tasksDir, err := v.TasksDir(project)
	if err != nil {
		t.Fatal(err)
	}

	for _, task := range tasks {
		if task.Parent == "" && len(task.Depends) == 0 {
			continue
		}
		// The task claims a relation. Prove the line is really in its HEADER,
		// not somewhere in its body.
		path := findTaskFile(tasksDir, task.Slug)
		if path == "" {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !headerDeclaresRelation(string(body)) {
			t.Errorf("task %q reports parent=%q depends=%v, but its HEADER declares no relation — "+
				"the parser is reading the body as metadata",
				task.Slug, task.Parent, task.Depends)
		}
	}
}

// TestLiveVaultGraphIsCleanAndTerminates asserts the real backlog has no
// structural lies in it — and, just as importantly, that building the graph over
// it RETURNS. A cycle that got walked instead of detected would hang here, and a
// hang must fail as a bug rather than as a CI timeout.
func TestLiveVaultGraphIsCleanAndTerminates(t *testing.T) {
	v, project := liveVault(t)

	type result struct {
		g   *taskgraph.Graph
		err error
	}
	ch := make(chan result, 1)
	go func() {
		g, err := taskgraph.BuildFromVault(v, project)
		ch <- result{g, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("build from live vault: %v", r.err)
		}
		if r.g.HasProblems() {
			t.Errorf("the live backlog has structural problems:\n"+
				"  cycles:        %+v\n"+
				"  dangling:      %+v\n"+
				"  stale parents: %+v\n"+
				"Run `vp tasks` to see them.",
				r.g.Cycles, r.g.Dangling, r.g.StaleParents)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("BuildFromVault did not terminate on the live vault")
	}
}

func findTaskFile(tasksDir, slug string) string {
	for _, dir := range []string{tasksDir, filepath.Join(tasksDir, "done"), filepath.Join(tasksDir, "cancelled")} {
		p := filepath.Join(dir, slug+".md")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// headerDeclaresRelation looks for a relation line in the contiguous metadata
// block at the top of the file — deliberately a SECOND, independent
// implementation of the rule. If it agreed with the parser by sharing its code,
// it could not catch the parser being wrong.
func headerDeclaresRelation(content string) bool {
	inHeader := false
	for line := range splitLines(content) {
		switch {
		case len(line) > 2 && line[:2] == "# ":
			inHeader = true
		case line == "":
			continue
		case hasPrefix(line, "**Status:**"), hasPrefix(line, "**Priority:**"):
			inHeader = true
		case hasPrefix(line, "**Parent:**"), hasPrefix(line, "**Depends:**"):
			return inHeader
		default:
			if inHeader {
				return false // the header block ended without a relation line
			}
		}
	}
	return false
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

func splitLines(s string) func(func(string) bool) {
	return func(yield func(string) bool) {
		start := 0
		for i := range len(s) {
			if s[i] == '\n' {
				if !yield(s[start:i]) {
					return
				}
				start = i + 1
			}
		}
		if start < len(s) {
			yield(s[start:])
		}
	}
}
