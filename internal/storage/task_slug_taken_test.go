// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
)

// These tests pin refuseTakenSlug, the one rule every task-placing path shares.
// Fixtures are laid down raw, bypassing every writer, because the shapes under
// test (an archived twin of an active slug) are exactly the ones the writers
// refuse to create. Each copy carries a distinct body marker, and the assertions
// read BYTES and FILE PRESENCE: a mis-resolved write produces a file whose
// header parses perfectly and whose body came from somewhere else.

func seedTaskRaw(t *testing.T, v *Vault, project, holder, slug, marker string) string {
	t.Helper()
	dir := filepath.Join(v.Root, "Projects", project, "tasks", holder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	status := map[string]string{"": "planning", "done": "done", "cancelled": "cancelled"}[holder]
	p := filepath.Join(dir, slug+".md")
	body := "# " + slug + "\n\n**Status:** " + status + "\n**Priority:** medium\n\n## Context\n\n" + marker + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readTaskBytes(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func taskFileAbsent(v *Vault, rel string) bool {
	_, err := os.Stat(filepath.Join(v.Root, rel))
	return os.IsNotExist(err)
}

// assertSlugTaken checks the refusal is the typed one, carries the expected
// operation and holder, is classified as the caller's fault, and names the
// twin's directory vault-relative.
func assertSlugTaken(t *testing.T, err error, op, holder string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected a refusal, got nil", op)
	}
	var te *taskSlugTakenError
	if !errors.As(err, &te) {
		t.Fatalf("%s: refusal is not a *taskSlugTakenError: %v", op, err)
	}
	if te.Op != op || te.Holder != holder {
		t.Errorf("%s: typed fields Op=%q Holder=%q, want Op=%q Holder=%q", op, te.Op, te.Holder, op, holder)
	}
	if !apperr.IsCaller(err) {
		t.Errorf("%s: refusal is not classified as a caller error", op)
	}
	if op == "create" && holder == "" {
		return // create's active wording is pinned verbatim elsewhere and names no path
	}
	want := "tasks/" + holder + "/"
	if holder == "" {
		want = "/tasks/"
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("%s: message does not name the twin's directory %q: %v", op, want, err)
	}
}

// A retire while cancelled/ holds the slug used to succeed and leave the
// done/+cancelled/ pair.
func TestRetireRefusesWhenCancelledHoldsSlug(t *testing.T) {
	v := testVault(t)
	act := seedTaskRaw(t, v, "p", "", "x", "ACTIVE")
	canc := seedTaskRaw(t, v, "p", "cancelled", "x", "CANCELLED")
	a0, c0 := readTaskBytes(t, act), readTaskBytes(t, canc)
	assertSlugTaken(t, v.RetireTask("p", "x"), "retire", "cancelled")
	if readTaskBytes(t, act) != a0 || readTaskBytes(t, canc) != c0 {
		t.Fatal("a refused retire changed a file")
	}
	if !taskFileAbsent(v, "Projects/p/tasks/done/x.md") {
		t.Fatal("a refused retire created done/x.md: the done+cancelled pair")
	}
	// "Nothing was changed" covers the tree too: the refusal comes before the
	// archive directory is created, not after.
	if !taskFileAbsent(v, "Projects/p/tasks/done") {
		t.Fatal("a refused retire created the done/ directory")
	}
}

// The mirror: a cancel while done/ holds the slug.
func TestCancelRefusesWhenDoneHoldsSlug(t *testing.T) {
	v := testVault(t)
	act := seedTaskRaw(t, v, "p", "", "x", "ACTIVE")
	done := seedTaskRaw(t, v, "p", "done", "x", "DONE")
	a0, d0 := readTaskBytes(t, act), readTaskBytes(t, done)
	assertSlugTaken(t, v.CancelTask("p", "x", ""), "cancel", "done")
	if readTaskBytes(t, act) != a0 || readTaskBytes(t, done) != d0 {
		t.Fatal("a refused cancel changed a file")
	}
	if !taskFileAbsent(v, "Projects/p/tasks/cancelled/x.md") {
		t.Fatal("a refused cancel created cancelled/x.md: the done+cancelled pair")
	}
	// "Nothing was changed" covers the tree too: the refusal comes before the
	// archive directory is created, not after.
	if !taskFileAbsent(v, "Projects/p/tasks/cancelled") {
		t.Fatal("a refused cancel created the cancelled/ directory")
	}
}

// A move into a project holding the slug archived used to land an active file
// beside the archived one.
func TestMoveRefusesArchivedTwinInDestination(t *testing.T) {
	for _, holder := range []string{"done", "cancelled"} {
		t.Run(holder, func(t *testing.T) {
			v := testVault(t)
			src := seedTaskRaw(t, v, "src", "", "x", "SOURCE")
			twin := seedTaskRaw(t, v, "dst", holder, "x", "TWIN")
			s0, w0 := readTaskBytes(t, src), readTaskBytes(t, twin)
			assertSlugTaken(t, v.MoveTaskToProject("src", "x", "dst"), "move", holder)
			if readTaskBytes(t, src) != s0 || readTaskBytes(t, twin) != w0 {
				t.Fatal("a refused move changed a file")
			}
			if !taskFileAbsent(v, "Projects/dst/tasks/x.md") {
				t.Fatal("a refused move landed an active file beside the archived twin")
			}
		})
	}
}

// The chain the probe found: a move created active+done, retire then refused,
// and cancel succeeded, leaving done+cancelled. Now it stops at step 1, and
// even with the twin planted as a merge would, steps 2 and 3 both refuse.
func TestMoveRetireCancelChainNeverReachesDoneCancelledPair(t *testing.T) {
	v := testVault(t)
	seedTaskRaw(t, v, "src", "", "x", "SOURCE")
	seedTaskRaw(t, v, "dst", "done", "x", "TWIN")
	if err := v.MoveTaskToProject("src", "x", "dst"); err == nil {
		t.Fatal("step 1: move into a project holding done/x succeeded: active+done created")
	}
	seedTaskRaw(t, v, "dst", "", "x", "MERGED-IN")
	assertSlugTaken(t, v.RetireTask("dst", "x"), "retire", "done")
	assertSlugTaken(t, v.CancelTask("dst", "x", ""), "cancel", "done")
	if !taskFileAbsent(v, "Projects/dst/tasks/cancelled/x.md") {
		t.Fatal("step 3: cancel created cancelled/x.md: the pair task-status once destroyed a body on")
	}
}

// Over-refusal control: the ordinary paths, with nothing in the way, still work.
func TestOrdinaryRetireCancelMoveStillWork(t *testing.T) {
	v := testVault(t)
	seedTaskRaw(t, v, "p", "", "r", "R")
	seedTaskRaw(t, v, "p", "", "c", "C")
	seedTaskRaw(t, v, "src", "", "m", "M")
	if err := v.RetireTask("p", "r"); err != nil {
		t.Fatalf("plain retire refused: %v", err)
	}
	if err := v.CancelTask("p", "c", ""); err != nil {
		t.Fatalf("plain cancel refused: %v", err)
	}
	if err := v.MoveTaskToProject("src", "m", "dst"); err != nil {
		t.Fatalf("plain move refused: %v", err)
	}
	for _, rel := range []string{"Projects/p/tasks/done/r.md", "Projects/p/tasks/cancelled/c.md", "Projects/dst/tasks/m.md"} {
		if taskFileAbsent(v, rel) {
			t.Errorf("%s missing after a successful operation", rel)
		}
	}
}

// CreateTask's existing refusal holds through the shared rule, for every holder.
func TestCreateStillRefusesEveryHolder(t *testing.T) {
	for _, holder := range []string{"", "done", "cancelled"} {
		t.Run("holder="+holder, func(t *testing.T) {
			v := testVault(t)
			seedTaskRaw(t, v, "p", holder, "x", "EXISTING")
			assertSlugTaken(t, v.CreateTask("p", TaskSpec{Slug: "x", Title: "New", Priority: "low"}), "create", holder)
		})
	}
}

// Fail-closed without depending on permissions, so it runs as root too: done/
// is a regular FILE, so stat of done/<slug>.md is ENOTDIR. The operation must
// refuse, must not blame a slug that is not taken, and must not hand the host's
// absolute path to the caller.
func TestArchiveDirIsAFileFailsClosed(t *testing.T) {
	for _, op := range []string{"create", "cancel"} {
		t.Run(op, func(t *testing.T) {
			v := testVault(t)
			seedTaskRaw(t, v, "p", "", "keep", "K")
			if err := os.WriteFile(filepath.Join(v.Root, "Projects/p/tasks/done"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			var err error
			slug := "n"
			if op == "create" {
				err = v.CreateTask("p", TaskSpec{Slug: slug, Title: "N", Priority: "low"})
			} else {
				slug = "keep"
				err = v.CancelTask("p", slug, "")
			}
			if err == nil {
				t.Fatalf("%s proceeded although done/ could not be inspected (ENOTDIR): fail-open", op)
			}
			var te *taskSlugTakenError
			if errors.As(err, &te) {
				t.Fatalf("ENOTDIR reported as slug-taken, which is a false cause: %v", err)
			}
			if strings.Contains(err.Error(), v.Root) {
				t.Errorf("the error carries the host's absolute vault path: %v", err)
			}
			if want := "Projects/p/tasks/done/" + slug + ".md"; !strings.Contains(err.Error(), want) {
				t.Errorf("the error does not name %s vault-relative: %v", want, err)
			}
		})
	}
}

// Fail-closed on an unreadable directory. Root ignores directory permissions,
// so this one self-skips there; TestArchiveDirIsAFileFailsClosed is the pin
// that still runs.
func TestUnreadableArchiveDirFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions; TestArchiveDirIsAFileFailsClosed covers this as root")
	}
	v := testVault(t)
	seedTaskRaw(t, v, "p", "", "x", "ACTIVE")
	cd := filepath.Join(v.Root, "Projects/p/tasks/cancelled")
	if err := os.MkdirAll(cd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cd, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(cd, 0o755) })
	if err := v.RetireTask("p", "x"); err == nil {
		t.Fatal("retire proceeded although cancelled/ could not be checked")
	}
	if taskFileAbsent(v, "Projects/p/tasks/x.md") {
		t.Fatal("a refused retire moved the source")
	}
}

// The most ordinary route to the pair, through typed calls only: a task moved
// out of p leaves a tombstone at p/cancelled/x (the vp_manage_task move arm
// files it with CreateTask then CancelTask), so moving the task BACK used to
// land an active x beside it, and retiring it there then created done/x
// beside cancelled/x. The move back is now refused and changes nothing.
func TestMoveBackOntoOwnTombstoneIsRefused(t *testing.T) {
	v := testVault(t)
	seedTaskRaw(t, v, "p", "", "x", "TASK")
	if err := os.MkdirAll(filepath.Join(v.Root, "Projects/q/tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := v.MoveTaskToProject("p", "x", "q"); err != nil {
		t.Fatalf("move out: %v", err)
	}
	if err := v.CreateTask("p", TaskSpec{Slug: "x", Title: "Tombstone", Priority: "low", Content: "moved to q"}); err != nil {
		t.Fatalf("tombstone create: %v", err)
	}
	if err := v.CancelTask("p", "x", ""); err != nil {
		t.Fatalf("tombstone cancel: %v", err)
	}
	moved := filepath.Join(v.Root, "Projects/q/tasks/x.md")
	tomb := filepath.Join(v.Root, "Projects/p/tasks/cancelled/x.md")
	m0, t0 := readTaskBytes(t, moved), readTaskBytes(t, tomb)
	assertSlugTaken(t, v.MoveTaskToProject("q", "x", "p"), "move", "cancelled")
	if readTaskBytes(t, moved) != m0 || readTaskBytes(t, tomb) != t0 {
		t.Fatal("a refused move back changed a file")
	}
	if !taskFileAbsent(v, "Projects/p/tasks/x.md") {
		t.Fatal("a refused move back landed an active x beside its own tombstone")
	}
}
