// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Archiving a task is REWRITE-THEN-RENAME: the terminal status is stamped in
// place while the file is still active, and the rename into done/ or cancelled/
// is the atomic commit point. Adopted 2026-09-01.
//
// These tests exist because the ordering is only observable when something goes
// wrong. In the success case the reorder is invisible, which is itself an
// assertion worth making — see the first test.
//
// Every fixture below passes a body with NO heading of its own. CreateTask emits
// the conventional first H2 itself and now REFUSES a body that repeats it
// (validateTaskBody's conventional-heading arm), so the "## Context\n\n…" bodies
// these fixtures used to carry would be rejected — and, before the refusal
// existed, they were quietly building the two-sections-one-key file that arm was
// added to prevent. The archive ordering these tests measure does not depend on
// the body's shape.

// archiveSlugCopies counts the file copies of a slug across all three task
// directories. The whole point of the ordering is that this is never 2.
func archiveSlugCopies(t *testing.T, v *Vault, project, slug string) (active, done, cancelled bool) {
	t.Helper()
	activePath, err := v.TaskFile(project, slug)
	if err != nil {
		t.Fatalf("TaskFile: %v", err)
	}
	doneDir, err := v.TaskDoneDir(project)
	if err != nil {
		t.Fatalf("TaskDoneDir: %v", err)
	}
	cancelledDir, err := v.TaskCancelledDir(project)
	if err != nil {
		t.Fatalf("TaskCancelledDir: %v", err)
	}
	exists := func(p string) bool {
		_, statErr := os.Stat(p)
		return statErr == nil
	}
	return exists(activePath),
		exists(filepath.Join(doneDir, slug+".md")),
		exists(filepath.Join(cancelledDir, slug+".md"))
}

// TestArchiveSuccessStateIsUnchangedByTheReorder is the invisibility assertion:
// on the happy path the new ordering must produce exactly what the old one did.
// It compares the archived bytes against the pre-archive bytes with only the
// Status line substituted, so a reorder that quietly dropped or reformatted body
// content would fail here rather than in some later reader.
func TestArchiveSuccessStateIsUnchangedByTheReorder(t *testing.T) {
	for _, tc := range []struct {
		name       string
		archive    func(v *Vault, project, slug string) error
		wantStatus string
		dirFn      func(v *Vault, project string) (string, error)
	}{
		{"retire", (*Vault).RetireTask, "retired", (*Vault).TaskDoneDir},
		{"cancel", (*Vault).CancelTask, "cancelled", (*Vault).TaskCancelledDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := testVault(t)
			if err := v.CreateTask("proj", TaskSpec{
				Slug: "my-task", Title: "My Task", Content: "Body text.\n", Priority: "medium",
			}); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}

			activePath, _ := v.TaskFile("proj", "my-task")
			before, err := os.ReadFile(activePath)
			if err != nil {
				t.Fatalf("read pre-archive body: %v", err)
			}
			want := replaceStatusLine(string(before), tc.wantStatus)

			if err := tc.archive(v, "proj", "my-task"); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			dir, _ := tc.dirFn(v, "proj")
			got, err := os.ReadFile(filepath.Join(dir, "my-task.md"))
			if err != nil {
				t.Fatalf("read archived body: %v", err)
			}
			if string(got) != want {
				t.Errorf("archived body differs from pre-archive body with the status substituted:\ngot:  %q\nwant: %q", got, want)
			}

			active, done, cancelled := archiveSlugCopies(t, v, "proj", "my-task")
			if active {
				t.Error("archived task must not remain in the active directory")
			}
			if tc.wantStatus == "retired" && (!done || cancelled) {
				t.Errorf("retired task placement wrong: done=%v cancelled=%v", done, cancelled)
			}
			if tc.wantStatus == "cancelled" && (!cancelled || done) {
				t.Errorf("cancelled task placement wrong: done=%v cancelled=%v", done, cancelled)
			}
		})
	}
}

// TestArchiveCrashBetweenStampAndRename is the ordering test proper: it forces a
// failure in the window BETWEEN the two halves and asserts what is left behind.
//
// The failure is injected without a production seam, by making the destination
// directory unwritable — the stamp targets the active directory and still
// succeeds, while the rename into the archive directory cannot. That is the
// exact interleaving a crash would produce.
//
// The state to prove is the whole reason for the ordering:
//   - ONE copy, in tasks/, carrying a TERMINAL status — an obviously mid-archive
//     state, and repairable by completing the rename.
//   - NEVER two copies (the previous write-dest-then-unlink ordering's signature,
//     where the active copy wins resolveTaskFile and the retire looks undone).
//   - NEVER a file in done/ claiming to be active (the rename-then-rewrite
//     signature, which is the defect this task is named for).
func TestArchiveCrashBetweenStampAndRename(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{
		Slug: "my-task", Title: "My Task", Content: "Body.\n", Priority: "medium",
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	doneDir, _ := v.TaskDoneDir("proj")
	if err := EnsureDir(doneDir); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	// r-x: stat of the (absent) destination still succeeds, the rename into it
	// does not.
	if err := os.Chmod(doneDir, 0o555); err != nil {
		t.Fatalf("chmod done dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(doneDir, 0o755) })

	// Permission bits do not constrain root, so the injection would silently
	// not inject and the test would assert nothing.
	if probe, err := os.CreateTemp(doneDir, "probe"); err == nil {
		probe.Close()
		os.Remove(probe.Name())
		t.Skip("cannot make a directory unwritable for this user (running as root?); the failure injection would not fire")
	}

	err := v.RetireTask("proj", "my-task")
	if err == nil {
		t.Fatal("RetireTask must fail when the archive directory cannot be written")
	}
	// The error has to say the task is still active, or an operator reading it
	// will look in the wrong directory for the wreckage.
	if !strings.Contains(err.Error(), "active directory") {
		t.Errorf("error should say the task is still in the active directory, got: %v", err)
	}

	active, done, cancelled := archiveSlugCopies(t, v, "proj", "my-task")
	if !active {
		t.Fatal("the interrupted archive must leave the task in the active directory")
	}
	if done || cancelled {
		t.Errorf("the interrupted archive must leave exactly one copy; done=%v cancelled=%v", done, cancelled)
	}

	// And that single copy carries the terminal status — the mid-archive
	// signature a detector can state a rule about, since UpdateTaskStatus
	// refuses terminal values and so cannot have produced it.
	activePath, _ := v.TaskFile("proj", "my-task")
	body, readErr := os.ReadFile(activePath)
	if readErr != nil {
		t.Fatalf("read active body: %v", readErr)
	}
	meta := parseTaskMeta("my-task", string(body), false)
	if meta.Status != "retired" {
		t.Errorf("interrupted archive should leave the ACTIVE file stamped retired, got status %q", meta.Status)
	}
}

// TestRetireRefusalLeavesSourceBodyUnstamped pins the ordering requirement that
// the refuse-existing-destination check runs BEFORE the stamp.
//
// Under the previous ordering the check only had to precede the destination
// write. Under rewrite-then-rename the stamp mutates the SOURCE, so a refusal
// that fired after it would leave an active task carrying a terminal status —
// manufacturing the interrupted-archive state through an ordinary refusal, with
// no crash involved. TestRetireDoesNotClobberExistingDoneRecord already proves
// the historical record survives; this proves the source body does too.
func TestRetireRefusalLeavesSourceBodyUnstamped(t *testing.T) {
	v := testVault(t)

	// Order matters: CreateTask refuses a slug that already lives in done/, so
	// the active file is created first and the historical record planted after.
	// The result is the duplicate-slug state a re-retire has to refuse.
	if err := v.CreateTask("proj", TaskSpec{
		Slug: "my-task", Title: "My Task", Content: "Active body.\n", Priority: "medium",
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	doneDir, _ := v.TaskDoneDir("proj")
	if err := EnsureDir(doneDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doneDir, "my-task.md"),
		[]byte("# My Task\n\n**Status:** retired\n\nHISTORICAL\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	activePath, _ := v.TaskFile("proj", "my-task")
	before, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}

	if err := v.RetireTask("proj", "my-task"); err == nil {
		t.Fatal("re-retire over an existing done/ record must be refused")
	}

	after, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("source must survive a refused archive: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("a refused archive must not have stamped the source body:\ngot:  %q\nwant: %q", after, before)
	}
	meta := parseTaskMeta("my-task", string(after), false)
	if meta.Status == "retired" {
		t.Error("refused archive stamped the source terminal — the destination check must precede the stamp")
	}
}
