// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// MoveTaskToProject is PHASE 1 of the cross-project move: a correct relocation,
// and a refusal for every case it cannot perform honestly. It writes no
// provenance and no header field, so the whole of its behaviour is "the bytes
// arrive unchanged somewhere else, or nothing happens at all" — which is what
// these tests measure, against a vault they build rather than against fixtures.
//
// The bodies below carry NO heading of their own: CreateTask emits the
// conventional first H2 itself and refuses a body that repeats it.

// moveTestVault builds a vault with `slug` active in `from`, plus whatever
// counterpart tasks the caller wants standing in `to`.
func moveTestVault(t *testing.T, from, to string, spec TaskSpec, inDest ...TaskSpec) *Vault {
	t.Helper()
	v := testVault(t)
	if err := v.CreateTask(from, spec); err != nil {
		t.Fatalf("CreateTask %s/%s: %v", from, spec.Slug, err)
	}
	for _, s := range inDest {
		if err := v.CreateTask(to, s); err != nil {
			t.Fatalf("CreateTask %s/%s: %v", to, s.Slug, err)
		}
	}
	return v
}

func plainTask(slug string) TaskSpec {
	return TaskSpec{Slug: slug, Title: strings.ToUpper(slug), Content: "Body text for " + slug + ".\n", Priority: "medium"}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestMoveTaskToProjectRelocatesTheBytesUnchanged is the happy path. It compares
// the destination bytes against the pre-move source bytes EXACTLY: phase 1
// writes no provenance, so any difference at all is a defect — a stamped header,
// a re-rendered body, a lost trailing newline.
func TestMoveTaskToProjectRelocatesTheBytesUnchanged(t *testing.T) {
	v := moveTestVault(t, "src-proj", "dst-proj", plainTask("wandering-task"), plainTask("anchor"))

	srcPath, err := v.TaskFile("src-proj", "wandering-task")
	if err != nil {
		t.Fatalf("TaskFile: %v", err)
	}
	before := mustReadFile(t, srcPath)

	if err := v.MoveTaskToProject("src-proj", "wandering-task", "dst-proj"); err != nil {
		t.Fatalf("MoveTaskToProject: %v", err)
	}

	if exists(srcPath) {
		t.Errorf("source copy still present at %s — a move leaves exactly one copy", srcPath)
	}
	destPath, err := v.TaskFile("dst-proj", "wandering-task")
	if err != nil {
		t.Fatalf("TaskFile(dest): %v", err)
	}
	after := mustReadFile(t, destPath)
	if string(after) != string(before) {
		t.Errorf("body changed by the move.\n before: %q\n  after: %q", before, after)
	}

	// And it reads back as a task of the destination project, active.
	meta, _, err := v.GetTask("dst-proj", "wandering-task")
	if err != nil {
		t.Fatalf("GetTask(dest): %v", err)
	}
	if meta.Done {
		t.Error("moved task reads as archived; it should still be active")
	}
	if _, _, err := v.GetTask("src-proj", "wandering-task"); err == nil {
		t.Error("task still resolves in the source project after the move")
	}
}

// TestMoveTaskSourceIsTheACTIVEPathOnly pins the mechanism the archived refusal
// rests on: Vault.TaskFile computes the active path unconditionally and never
// consults done/ or cancelled/. If that ever changed, an archived task would
// become movable without any rule being edited.
func TestMoveTaskSourceIsTheACTIVEPathOnly(t *testing.T) {
	v := testVault(t)
	tasksDir, err := v.TasksDir("p")
	if err != nil {
		t.Fatalf("TasksDir: %v", err)
	}
	got, err := v.TaskFile("p", "t")
	if err != nil {
		t.Fatalf("TaskFile: %v", err)
	}
	if want := filepath.Join(tasksDir, "t.md"); got != want {
		t.Fatalf("TaskFile = %q, want the ACTIVE path %q", got, want)
	}
}

// TestMoveTaskToProjectRefusesArchivedSource covers both archives. An archived
// body is the record of what happened in the project it happened in.
func TestMoveTaskToProjectRefusesArchivedSource(t *testing.T) {
	for _, tc := range []struct {
		name    string
		archive func(v *Vault, project, slug string) error
	}{
		{"done", (*Vault).RetireTask},
		{"cancelled", (*Vault).CancelTask},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := moveTestVault(t, "src-proj", "dst-proj", plainTask("finished-task"), plainTask("anchor"))
			if err := tc.archive(v, "src-proj", "finished-task"); err != nil {
				t.Fatalf("archive: %v", err)
			}

			err := v.MoveTaskToProject("src-proj", "finished-task", "dst-proj")
			if err == nil {
				t.Fatal("expected a refusal for an archived source task")
			}
			if !strings.Contains(err.Error(), "archived") {
				t.Errorf("refusal does not say the task is archived: %v", err)
			}

			// The archived record is untouched and nothing landed in the
			// destination.
			if _, _, gerr := v.GetTask("src-proj", "finished-task"); gerr != nil {
				t.Errorf("archived source task disappeared: %v", gerr)
			}
			destPath, _ := v.TaskFile("dst-proj", "finished-task")
			if exists(destPath) {
				t.Errorf("a refused move still created %s", destPath)
			}
		})
	}
}

// TestMoveTaskToProjectRefusesDanglingEdges is the core refusal. The four cases
// are parent-only, one-dependency-only, both, and the PASSING case where the
// counterparts also live in the destination.
//
// 🔴 The refusal must NAME set_relations. This action never writes parent or
// depends_on — a second writer for either is how a reader and a writer come to
// disagree about which value is real — so the refusal has to hand the caller the
// action that does own the fix, or it is a "no" that gets worked around.
func TestMoveTaskToProjectRefusesDanglingEdges(t *testing.T) {
	for _, tc := range []struct {
		name       string
		parent     string
		depends    []string
		inDest     []TaskSpec
		wantRefuse bool
		wantNamed  []string
	}{
		{
			name:       "parent only",
			parent:     "an-epic",
			inDest:     []TaskSpec{plainTask("anchor")},
			wantRefuse: true,
			wantNamed:  []string{"an-epic"},
		},
		{
			name:       "one dependency only",
			depends:    []string{"other-work"},
			inDest:     []TaskSpec{plainTask("anchor")},
			wantRefuse: true,
			wantNamed:  []string{"other-work"},
		},
		{
			name:       "both",
			parent:     "an-epic",
			depends:    []string{"other-work"},
			inDest:     []TaskSpec{plainTask("anchor")},
			wantRefuse: true,
			wantNamed:  []string{"an-epic", "other-work"},
		},
		{
			name:    "counterparts live in the destination",
			parent:  "an-epic",
			depends: []string{"other-work"},
			inDest: []TaskSpec{
				plainTask("an-epic"),
				plainTask("other-work"),
			},
			wantRefuse: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := plainTask("edged-task")
			spec.Parent = tc.parent
			spec.Depends = tc.depends
			// The counterparts exist in the SOURCE project too, so the only
			// thing under test is whether they resolve in the DESTINATION.
			v := testVault(t)
			for _, s := range []string{tc.parent} {
				if s != "" {
					if err := v.CreateTask("src-proj", plainTask(s)); err != nil {
						t.Fatalf("CreateTask source counterpart: %v", err)
					}
				}
			}
			for _, d := range tc.depends {
				if err := v.CreateTask("src-proj", plainTask(d)); err != nil {
					t.Fatalf("CreateTask source counterpart: %v", err)
				}
			}
			if err := v.CreateTask("src-proj", spec); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
			for _, s := range tc.inDest {
				if err := v.CreateTask("dst-proj", s); err != nil {
					t.Fatalf("CreateTask dest: %v", err)
				}
			}

			srcPath, _ := v.TaskFile("src-proj", "edged-task")
			before := mustReadFile(t, srcPath)

			err := v.MoveTaskToProject("src-proj", "edged-task", "dst-proj")

			if !tc.wantRefuse {
				if err != nil {
					t.Fatalf("move refused a task whose edges DO resolve in the destination: %v", err)
				}
				destPath, _ := v.TaskFile("dst-proj", "edged-task")
				if !exists(destPath) {
					t.Errorf("task did not land at %s", destPath)
				}
				return
			}

			if err == nil {
				t.Fatal("expected a refusal for an edge that would dangle in the destination")
			}
			if !strings.Contains(err.Error(), "set_relations") {
				t.Errorf("refusal does not name set_relations as the owner of the fix: %v", err)
			}
			for _, name := range tc.wantNamed {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("refusal does not name the unresolved edge %q: %v", name, err)
				}
			}

			// A refusal writes nothing: the source is byte-identical and its
			// edges are untouched (this action is never a second writer for
			// parent or depends_on).
			if got := mustReadFile(t, srcPath); string(got) != string(before) {
				t.Errorf("refused move rewrote the source.\n before: %q\n  after: %q", before, got)
			}
			destPath, _ := v.TaskFile("dst-proj", "edged-task")
			if exists(destPath) {
				t.Errorf("refused move still created %s", destPath)
			}
		})
	}
}

// TestMoveTaskToProjectEdgeResolvesAgainstTheARCHIVEToo pins the resolution set.
// A counterpart that is retired or cancelled in the destination COUNTS as
// resolving: taskgraph builds from ListTasks(project, true) and vp_manage_task's
// schema states that a dependency on a retired or cancelled task is SATISFIED.
// An active-only rule here would be a second, stricter definition of "resolves".
func TestMoveTaskToProjectEdgeResolvesAgainstTheARCHIVEToo(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("dst-proj", plainTask("an-epic")); err != nil {
		t.Fatalf("CreateTask dest epic: %v", err)
	}
	if err := v.RetireTask("dst-proj", "an-epic"); err != nil {
		t.Fatalf("RetireTask: %v", err)
	}
	spec := plainTask("child-task")
	spec.Parent = "an-epic"
	if err := v.CreateTask("src-proj", spec); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if err := v.MoveTaskToProject("src-proj", "child-task", "dst-proj"); err != nil {
		t.Fatalf("move refused an edge whose counterpart is archived in the destination: %v", err)
	}
}

// TestMoveTaskToProjectRefusesOccupiedDestination is the stat that stands
// between a duplicate slug and a destroyed task: vaultfs.RenameNoLock is a bare
// os.Rename and replaces its destination SILENTLY. Asserting the refusal alone
// would pass on an implementation that renamed first and complained after, so
// the destination bytes are compared.
func TestMoveTaskToProjectRefusesOccupiedDestination(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("src-proj", plainTask("collide")); err != nil {
		t.Fatalf("CreateTask source: %v", err)
	}
	occupant := plainTask("collide")
	occupant.Title = "The task already living there"
	occupant.Content = "A DIFFERENT body that must survive the refusal.\n"
	if err := v.CreateTask("dst-proj", occupant); err != nil {
		t.Fatalf("CreateTask dest: %v", err)
	}

	destPath, _ := v.TaskFile("dst-proj", "collide")
	before := mustReadFile(t, destPath)
	srcPath, _ := v.TaskFile("src-proj", "collide")
	srcBefore := mustReadFile(t, srcPath)

	err := v.MoveTaskToProject("src-proj", "collide", "dst-proj")
	if err == nil {
		t.Fatal("expected a refusal for an occupied destination")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("refusal does not say the destination is occupied: %v", err)
	}

	if got := mustReadFile(t, destPath); string(got) != string(before) {
		t.Errorf("the pre-existing destination task was overwritten.\n before: %q\n  after: %q", before, got)
	}
	if got := mustReadFile(t, srcPath); string(got) != string(srcBefore) {
		t.Errorf("refused move disturbed the source.\n before: %q\n  after: %q", srcBefore, got)
	}
}

// TestMoveTaskToProjectRefusesSameProject: a move to where the task already is
// is not a no-op worth performing, it is a caller who meant something else.
func TestMoveTaskToProjectRefusesSameProject(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("src-proj", plainTask("stay")); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	srcPath, _ := v.TaskFile("src-proj", "stay")
	before := mustReadFile(t, srcPath)

	if err := v.MoveTaskToProject("src-proj", "stay", "src-proj"); err == nil {
		t.Fatal("expected a refusal when source and destination project are the same")
	}
	if got := mustReadFile(t, srcPath); string(got) != string(before) {
		t.Error("same-project refusal disturbed the file")
	}
}

// TestMoveTaskToProjectRefusesMissingSource keeps the not-found message distinct
// from the archived one: an unknown slug is not an archived slug, and telling a
// caller the wrong one sends them looking in the wrong place.
func TestMoveTaskToProjectRefusesMissingSource(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("dst-proj", plainTask("anchor")); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	err := v.MoveTaskToProject("src-proj", "never-existed", "dst-proj")
	if err == nil {
		t.Fatal("expected a refusal for a task that does not exist")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("message = %v, want a not-found refusal", err)
	}
}

// ---------------------------------------------------------------------------
// THE LOCK SITE. The three tests below exist because "dest exists ⇒ refuse and
// the destination bytes are unchanged" — TestMoveTaskToProjectRefusesOccupied-
// Destination, above — is NECESSARY BUT NOT SUFFICIENT: it passes with the lock
// on the WRONG path, because a single-threaded refusal never exercises the
// window. The defect this operation is exposed to is many sources, ONE shared
// destination, and only the destination lock closes it. So the lock SITE is
// what these pin: A that the destination is locked, B that the source is not,
// C that the collision the lock exists for is representable and survives.
// ---------------------------------------------------------------------------

// moveLockTestVault builds a source project holding `slug` and a destination
// project that ALREADY EXISTS on disk (via an unrelated anchor task).
//
// The destination directory must exist before the test takes its own lock:
// vaultlock's canonicalKey EvalSymlinks-resolves the PARENT of a path that does
// not exist yet and falls back to a lexical clean when the parent is missing
// too. Both spellings are stable, but they are not necessarily the SAME key, so
// a test that locked destPath before its parent directory existed could hash to
// a different lock file than MoveTaskToProject does after EnsureDir — and would
// then pass or fail for a reason that has nothing to do with the lock site.
func moveLockTestVault(t *testing.T, from, to, slug string) (v *Vault, srcPath, destPath string) {
	t.Helper()
	v = moveTestVault(t, from, to, plainTask(slug), plainTask("dest-anchor"))
	srcPath, err := v.TaskFile(from, slug)
	if err != nil {
		t.Fatalf("TaskFile(src): %v", err)
	}
	destPath, err = v.TaskFile(to, slug)
	if err != nil {
		t.Fatalf("TaskFile(dest): %v", err)
	}
	if !exists(filepath.Dir(destPath)) {
		t.Fatalf("destination tasks dir %s does not exist — the lock keys would not be comparable", filepath.Dir(destPath))
	}
	return v, srcPath, destPath
}

// TestMoveTaskDestinationIsTheLockedPath is the PRIMARY PIN on the lock site,
// and it is deterministic: no goroutine racing is needed to make it correct.
// The test itself takes the destination's lock and holds it, then asserts that
// MoveTaskToProject CANNOT FINISH while it is held, and DOES finish once it is
// released.
//
// 🔴 THIS IS THE TEST THAT DISCRIMINATES. If the implementation locked srcPath
// instead — which is what it did before, and what a later reader is most likely
// to "restore" — the move would sail straight past a lock held on destPath and
// complete inside the window, and the blocked-window assertion below fails
// immediately. An occupied-destination test cannot tell those two
// implementations apart; this one can only pass for the correct one.
func TestMoveTaskDestinationIsTheLockedPath(t *testing.T) {
	v, _, destPath := moveLockTestVault(t, "src-proj", "dst-proj", "wanderer")

	release, err := vaultlock.Acquire(v.Root, destPath)
	if err != nil {
		t.Fatalf("test could not take the destination lock: %v", err)
	}
	released := false
	defer func() {
		if !released {
			_ = release()
		}
	}()

	done := make(chan error, 1)
	go func() {
		done <- v.MoveTaskToProject("src-proj", "wanderer", "dst-proj")
	}()

	// The move must be BLOCKED on the destination lock for as long as the test
	// holds it. vaultlock.Acquire is a blocking LOCK_EX with no timeout, so a
	// correct implementation cannot get past it and nothing arrives here.
	select {
	case err := <-done:
		t.Fatalf("MoveTaskToProject COMPLETED (err=%v) while the test held the lock on the DESTINATION path %s. "+
			"The one lock is not on the destination — this is the many-sources-one-destination race: two moves "+
			"of the same slug out of two different source projects both stat an absent destination, both "+
			"proceed, and the second os.Rename destroys the task the first placed", err, destPath)
	case <-time.After(200 * time.Millisecond):
		// Correct: still blocked.
	}

	released = true
	if err := release(); err != nil {
		t.Fatalf("release destination lock: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("move failed after the destination lock was released: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("MoveTaskToProject never completed after the destination lock was released — it is waiting on " +
			"a lock nothing will free, which is what a nested or inverted acquisition looks like")
	}

	if !exists(destPath) {
		t.Errorf("move reported success but %s is not there", destPath)
	}
}

// TestMoveTaskSourceIsNotLocked is the OTHER half of the pin, and it exists to
// keep this operation SINGLE-LOCKED. Holding the source path's lock must not
// stop the move: if it ever does, someone has added a second Acquire (or
// restored the old one), and this operation now holds two locks with an order
// that can be inverted. vaultlock.Acquire is a blocking LOCK_EX with no timeout,
// so an inversion is a PERMANENT HANG rather than a detectable error — the
// cheapest defence is to have no second lock at all, which is what this test
// keeps true.
func TestMoveTaskSourceIsNotLocked(t *testing.T) {
	v, srcPath, destPath := moveLockTestVault(t, "src-proj", "dst-proj", "wanderer")

	release, err := vaultlock.Acquire(v.Root, srcPath)
	if err != nil {
		t.Fatalf("test could not take the source lock: %v", err)
	}
	defer func() { _ = release() }()

	done := make(chan error, 1)
	go func() {
		done <- v.MoveTaskToProject("src-proj", "wanderer", "dst-proj")
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("move failed while the source lock was held: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("MoveTaskToProject BLOCKED on a lock held over the SOURCE path %s. The operation is supposed "+
			"to take exactly ONE lock, on the destination; a second lock here means there is now a lock ORDER, "+
			"and vaultlock.Acquire has no timeout, so inverting it is a permanent hang", srcPath)
	}

	if exists(srcPath) {
		t.Errorf("source copy still present at %s", srcPath)
	}
	if !exists(destPath) {
		t.Errorf("destination %s is not there", destPath)
	}
}

// TestMoveTaskConcurrentSameSlugFromTwoProjects makes the collision the lock
// exists for REPRESENTABLE: project-a and project-b each hold a task with the
// SAME slug, and both are moved into project-c at once. Their source paths
// differ, so they hash to DIFFERENT lock files — which is exactly why a source
// lock cannot serialise them and why the destination is the locked path.
//
// The assertions are about survival, not about who wins: exactly one move
// succeeds, the destination holds the WINNER's bytes whole, and the LOSER's
// task is still sitting untouched in its own project. On the source-locked
// implementation the second rename replaces the first task's file and the
// loser's source is gone, so both of the last two assertions redden.
//
// 🔴 THIS ONE IS PROBABILISTIC, WHICH IS WHY IT IS NOT THE PRIMARY PIN. It is
// a real two-goroutine race, so on the source-locked implementation it fails
// only when the two threads actually interleave — measured at roughly 3% of
// runs on the machine this was written on, where BOTH moves returned nil and
// the second rename had silently destroyed the first task. A once-per-CI-run
// 3% detector is a reason to keep this test, not a reason to trust it: the
// DETERMINISTIC pin is TestMoveTaskDestinationIsTheLockedPath above.
//
// Runs under -race.
func TestMoveTaskConcurrentSameSlugFromTwoProjects(t *testing.T) {
	const slug = "contested"

	v := testVault(t)
	sources := [2]string{"project-a", "project-b"}
	var srcPaths [2]string
	var srcBytes [2][]byte

	for i, proj := range sources {
		spec := plainTask(slug)
		spec.Title = "Contested task as it stands in " + proj
		spec.Content = "This body belongs to " + proj + " and must never be half-written or lost.\n"
		if err := v.CreateTask(proj, spec); err != nil {
			t.Fatalf("CreateTask %s/%s: %v", proj, slug, err)
		}
		p, err := v.TaskFile(proj, slug)
		if err != nil {
			t.Fatalf("TaskFile(%s): %v", proj, err)
		}
		srcPaths[i] = p
		srcBytes[i] = mustReadFile(t, p)
	}
	if string(srcBytes[0]) == string(srcBytes[1]) {
		t.Fatal("the two source tasks are byte-identical, so the winner could not be identified — the test " +
			"would pass on an implementation that destroyed one of them")
	}

	// project-c must already exist, so the move is not also racing a first-ever
	// directory creation. An unrelated anchor task is how a project comes to
	// exist here.
	if err := v.CreateTask("project-c", plainTask("dest-anchor")); err != nil {
		t.Fatalf("CreateTask project-c/dest-anchor: %v", err)
	}
	destPath, err := v.TaskFile("project-c", slug)
	if err != nil {
		t.Fatalf("TaskFile(dest): %v", err)
	}

	var errs [2]error
	var wg sync.WaitGroup
	wg.Add(len(sources))
	for i, proj := range sources {
		go func(i int, proj string) {
			defer wg.Done()
			errs[i] = v.MoveTaskToProject(proj, slug, "project-c")
		}(i, proj)
	}
	wg.Wait()

	winner, loser := -1, -1
	failures := 0
	for i := range errs {
		if errs[i] == nil {
			winner = i
		} else {
			failures++
			loser = i
		}
	}
	if failures != 1 || winner < 0 {
		t.Fatalf("expected EXACTLY ONE move to succeed and one to be refused; got project-a=%v project-b=%v. "+
			"Two successes mean both renames ran and the second destroyed the first task", errs[0], errs[1])
	}
	if !strings.Contains(errs[loser].Error(), "already exists") {
		t.Errorf("the losing move was refused for the wrong reason: %v", errs[loser])
	}

	// The destination must hold the WINNER's bytes, whole. A torn or replaced
	// file is the destroyed-task outcome this lock exists to prevent.
	got := mustReadFile(t, destPath)
	if string(got) != string(srcBytes[winner]) {
		t.Errorf("destination does not hold %s's task intact.\n want: %q\n  got: %q",
			sources[winner], srcBytes[winner], got)
	}

	// The LOSER was refused, so its task never moved: it is still in its own
	// project, byte-identical. This is the half that catches a move that
	// half-ran — renamed away and then reported a failure.
	if !exists(srcPaths[loser]) {
		t.Fatalf("%s's task is GONE from %s: the refused move removed it anyway, which is a destroyed record",
			sources[loser], srcPaths[loser])
	}
	if got := mustReadFile(t, srcPaths[loser]); string(got) != string(srcBytes[loser]) {
		t.Errorf("%s's task was disturbed by the refused move.\n before: %q\n  after: %q",
			sources[loser], srcBytes[loser], got)
	}

	// And the winner really did leave its own project.
	if exists(srcPaths[winner]) {
		t.Errorf("%s's task is still at %s after a successful move", sources[winner], srcPaths[winner])
	}
}
