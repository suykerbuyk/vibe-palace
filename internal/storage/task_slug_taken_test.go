// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
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
			err := v.MoveTaskToProject("src", "x", "dst")
			assertSlugTaken(t, err, "move", holder)
			// A plain archived twin is not a move-out tombstone, and keeps the
			// generic wording.
			if strings.Contains(err.Error(), "tombstone") {
				t.Errorf("an ordinary %s/ twin was described as a tombstone: %v", holder, err)
			}
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
// files TombstoneSpec with CreateTask then CancelTask), so moving the task BACK used to
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
	prov := MoveProvenance{FromProject: "p", ToProject: "q", Slug: "x", Day: "2026-09-19"}
	if err := v.CreateTask("p", prov.TombstoneSpec()); err != nil {
		t.Fatalf("tombstone create: %v", err)
	}
	if err := v.CancelTask("p", "x", ""); err != nil {
		t.Fatalf("tombstone cancel: %v", err)
	}
	moved := filepath.Join(v.Root, "Projects/q/tasks/x.md")
	tomb := filepath.Join(v.Root, "Projects/p/tasks/cancelled/x.md")
	m0, t0 := readTaskBytes(t, moved), readTaskBytes(t, tomb)
	err := v.MoveTaskToProject("q", "x", "p")
	assertSlugTaken(t, err, "move", "cancelled")
	// The holder is the move's own tombstone, not a stray twin, so the message
	// names it as such and points at a new slug, never at a hand rename.
	msg := err.Error()
	for _, want := range []string{"is the tombstone", `moved out of it to project "q"`, "Choose a new slug"} {
		if !strings.Contains(msg, want) {
			t.Errorf("move-back refusal does not say %q: %v", want, err)
		}
	}
	if strings.Contains(msg, "rename") || strings.Contains(msg, "by hand") {
		t.Errorf("move-back refusal suggests a hand rename of a correct tombstone: %v", err)
	}
	if readTaskBytes(t, moved) != m0 || readTaskBytes(t, tomb) != t0 {
		t.Fatal("a refused move back changed a file")
	}
	if !taskFileAbsent(v, "Projects/p/tasks/x.md") {
		t.Fatal("a refused move back landed an active x beside its own tombstone")
	}
}

// A retire or cancel that loses the lock to a concurrent archive of the SAME
// task must report "not found", never the slug-taken refusal: there is one task,
// already archived, and a hand-rename remedy would be false.
//
// The timing is forced, not hoped for. The test holds the task's lock, so the
// losing call passes its unlocked source stat and blocks in vaultlock.Acquire;
// the test then archives the file exactly as the winning call would, under that
// lock, and releases it. This cannot flake RED: with the under-lock re-check
// every interleaving ends in "not found" — a call that is slow to reach its first
// stat simply fails there instead. Timing only decides whether a BROKEN re-check
// is caught, so each case runs three times with a 30 ms head start; a single
// catch reds the test.
func TestLostArchiveRaceIsNotReportedAsSharedSlug(t *testing.T) {
	for _, c := range []struct {
		name, winnerDir string
		loser           func(v *Vault) error
	}{
		{"cancel-loses-to-retire", "done", func(v *Vault) error { return v.CancelTask("p", "x", "") }},
		{"retire-loses-to-cancel", "cancelled", func(v *Vault) error { return v.RetireTask("p", "x") }},
		{"retire-loses-to-retire", "done", func(v *Vault) error { return v.RetireTask("p", "x") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			for rep := 0; rep < 3; rep++ {
				v := testVault(t)
				src := seedTaskRaw(t, v, "p", "", "x", "TASK")
				release, err := vaultlock.Acquire(v.Root, src)
				if err != nil {
					t.Fatal(err)
				}
				got := make(chan error, 1)
				go func() { got <- c.loser(v) }()
				time.Sleep(30 * time.Millisecond)
				// The winner's effect, under the lock this test holds.
				winner := filepath.Join(v.Root, "Projects/p/tasks", c.winnerDir, "x.md")
				if err := os.MkdirAll(filepath.Dir(winner), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(src, winner); err != nil {
					t.Fatal(err)
				}
				if err := release(); err != nil {
					t.Fatal(err)
				}
				err = <-got
				var te *taskSlugTakenError
				if errors.As(err, &te) {
					t.Fatalf("rep %d: the loser of an archive race was told two tasks share the slug: %v", rep, err)
				}
				if err == nil || !strings.Contains(err.Error(), "not found") {
					t.Fatalf("rep %d: want a not-found error from the losing call, got %v", rep, err)
				}
			}
		})
	}
}

// The same property under real concurrency, and the one-winner invariant: a
// retire and a cancel of one task, started together, archive it exactly once
// and never produce a slug-taken refusal. Measured before the fix: 168 of 200
// losing calls were misdiagnosed.
func TestConcurrentRetireAndCancelArchiveOnceWithoutMisdiagnosis(t *testing.T) {
	const iterations = 100
	for i := 0; i < iterations; i++ {
		v := testVault(t)
		seedTaskRaw(t, v, "p", "", "x", "TASK")
		start := make(chan struct{})
		errs := make(chan error, 2)
		go func() { <-start; errs <- v.RetireTask("p", "x") }()
		go func() { <-start; errs <- v.CancelTask("p", "x", "") }()
		close(start)
		succeeded := 0
		for j := 0; j < 2; j++ {
			err := <-errs
			var te *taskSlugTakenError
			switch {
			case err == nil:
				succeeded++
			case errors.As(err, &te):
				t.Fatalf("iteration %d: a race loser was told two tasks share the slug: %v", i, err)
			case !strings.Contains(err.Error(), "not found"):
				t.Fatalf("iteration %d: unexpected error from the losing call: %v", i, err)
			}
		}
		done := !taskFileAbsent(v, "Projects/p/tasks/done/x.md")
		cancelled := !taskFileAbsent(v, "Projects/p/tasks/cancelled/x.md")
		if succeeded != 1 || done == cancelled {
			t.Fatalf("iteration %d: %d calls succeeded, done=%v cancelled=%v; want exactly one archive", i, succeeded, done, cancelled)
		}
	}
}

// A real task that happens to be titled "Moved to <word>" and was later
// cancelled is not a move-out tombstone: the title alone would have described it
// as one, naming a project called "redis". Only a file carrying tombstoneMarker,
// the sentence TombstoneSpec writes, gets the tombstone wording.
func TestCancelledTaskTitledMovedToIsNotCalledATombstone(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("p", TaskSpec{Slug: "cache-layer", Title: "Moved to redis", Priority: "low",
		Content: "Plan: move the cache layer to redis, measure, then retire memcached."}); err != nil {
		t.Fatal(err)
	}
	if err := v.CancelTask("p", "cache-layer", ""); err != nil {
		t.Fatal(err)
	}
	seedTaskRaw(t, v, "q", "", "cache-layer", "FROM-Q")
	err := v.MoveTaskToProject("q", "cache-layer", "p")
	assertSlugTaken(t, err, "move", "cancelled")
	if strings.Contains(err.Error(), "is the tombstone") || strings.Contains(err.Error(), "redis") {
		t.Fatalf("a real cancelled task titled \"Moved to redis\" was described as a move-out tombstone: %v", err)
	}
	if !strings.Contains(err.Error(), "so the slug is taken there") {
		t.Errorf("want the ordinary taken-slug wording: %v", err)
	}
}
