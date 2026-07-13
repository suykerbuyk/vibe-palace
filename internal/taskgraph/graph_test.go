// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package taskgraph

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func task(slug, status, priority, parent string, depends ...string) storage.TaskMeta {
	return storage.TaskMeta{
		Slug:     slug,
		Title:    slug,
		Status:   status,
		Priority: priority,
		Done:     status == "retired" || status == "cancelled",
		Parent:   parent,
		Depends:  depends,
	}
}

// withinTimeout runs fn and fails if it has not returned in time. The package's
// contract is that it NEVER hangs on malformed data; a test that merely deadlocks
// the whole suite would report that as an infrastructure timeout, not as this
// bug. See the cycle cases below.
func withinTimeout(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Build did not terminate: a cycle is being walked instead of detected")
	}
}

func TestEpicIsDerivedFromChildren(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("epic", "pending", "high", ""),
		task("child-a", "pending", "high", "epic"),
		task("child-b", "pending", "low", "epic"),
		task("loner", "pending", "medium", ""),
	})

	if !g.Nodes["epic"].IsEpic() {
		t.Fatal("a task with children must be an epic")
	}
	if g.Nodes["loner"].IsEpic() {
		t.Fatal("a task with no children is not an epic")
	}
	if !slices.Equal(g.Epics, []string{"epic"}) {
		t.Fatalf("Epics = %v, want [epic]", g.Epics)
	}
	if !slices.Equal(g.Nodes["epic"].Children, []string{"child-a", "child-b"}) {
		t.Fatalf("children = %v, want sorted [child-a child-b]", g.Nodes["epic"].Children)
	}
}

func TestDependencyOnRetiredTaskIsSatisfiedNotDangling(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("done-dep", "retired", "high", ""),
		task("gone-dep", "cancelled", "high", ""),
		task("work", "pending", "high", "", "done-dep", "gone-dep"),
	})

	if len(g.Dangling) != 0 {
		t.Fatalf("a dep on an ARCHIVED task is satisfied, not dangling: %+v", g.Dangling)
	}
	if len(g.Nodes["work"].Blockers) != 0 {
		t.Fatalf("a satisfied dep must not block: %v", g.Nodes["work"].Blockers)
	}

	deps := g.Nodes["work"].Depends
	if deps[0].State != DepSatisfiedDone {
		t.Fatalf("retired dep state = %v, want satisfied", deps[0].State)
	}
	// Retired and cancelled are BOTH satisfied but must not be conflated: a
	// reader must never be told work was completed when it was abandoned.
	if deps[1].State != DepSatisfiedCancelled {
		t.Fatalf("cancelled dep state = %v, want satisfied (cancelled)", deps[1].State)
	}
	if deps[0].State.String() == deps[1].State.String() {
		t.Fatal("retired and cancelled deps must render differently")
	}
}

func TestDanglingDependencyIsReportedButDoesNotBlock(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("work", "pending", "high", "", "no-such-task"),
	})

	if len(g.Dangling) != 1 || g.Dangling[0].To != "no-such-task" {
		t.Fatalf("dangling dep not reported: %+v", g.Dangling)
	}
	// Failing CLOSED on a typo would silently freeze the task forever.
	if len(g.Nodes["work"].Blockers) != 0 {
		t.Fatalf("a dangling dep must not block: %v", g.Nodes["work"].Blockers)
	}
	if !slices.Contains(g.Order, "work") {
		t.Fatal("a task with a typo'd dep must still be schedulable")
	}
}

func TestDanglingParentMakesTaskItsOwnRootAndStillRenders(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("orphan", "pending", "high", "no-such-epic"),
	})

	if len(g.Dangling) != 1 || g.Dangling[0].Kind != "parent" {
		t.Fatalf("dangling parent not reported: %+v", g.Dangling)
	}
	if g.Nodes["orphan"].Depth != 0 {
		t.Fatalf("depth = %d, want 0 for a dangling parent", g.Nodes["orphan"].Depth)
	}
	groups := g.Grouped(false)
	var found bool
	for _, grp := range groups {
		if slices.Contains(grp.Members, "orphan") {
			found = true
		}
	}
	if !found {
		t.Fatal("a task with a dangling parent must still appear in the grouped view")
	}
}

func TestRetiredParentWithActiveChildIsReportedStale(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("epic", "retired", "high", ""),
		task("child", "pending", "high", "epic"),
	})

	if len(g.StaleParents) != 1 || g.StaleParents[0].From != "child" {
		t.Fatalf("stale parent not reported: %+v", g.StaleParents)
	}
	// The child must remain schedulable — this is how finished-looking work
	// hides unfinished work, so it is surfaced, not quietly dropped.
	if !slices.Contains(g.Order, "child") {
		t.Fatal("an active child of a retired parent must stay in Order")
	}
}

func TestDependencyCycleIsDetectedNotWalked(t *testing.T) {
	withinTimeout(t, func() {
		g := Build([]storage.TaskMeta{
			task("a", "pending", "high", "", "c"),
			task("b", "pending", "high", "", "a"),
			task("c", "pending", "high", "", "b"),
		})

		if len(g.Cycles) != 1 {
			t.Fatalf("cycles = %+v, want exactly 1", g.Cycles)
		}
		// The SCC is a set, not a path — reported sorted, so it is stable no
		// matter which node the traversal happened to enter from.
		if !slices.Equal(g.Cycles[0].Slugs, []string{"a", "b", "c"}) {
			t.Fatalf("cycle = %v, want sorted membership [a b c]", g.Cycles[0].Slugs)
		}
		for _, s := range []string{"a", "b", "c"} {
			if slices.Contains(g.Order, s) {
				t.Fatalf("%q is in a cycle and has no defensible position in Order", s)
			}
		}
		if !g.HasProblems() {
			t.Fatal("a cycle is a problem")
		}
	})
}

// A node merely DOWNSTREAM of a cycle also never drains in Kahn. Reporting
// Kahn's leftovers as "the cycle" would name an innocent task; the SCC pass
// exists precisely to avoid that.
func TestDownstreamOfCycleIsNotNamedAsPartOfIt(t *testing.T) {
	withinTimeout(t, func() {
		g := Build([]storage.TaskMeta{
			task("a", "pending", "high", "", "b"),
			task("b", "pending", "high", "", "a"),
			task("downstream", "pending", "high", "", "a"),
		})

		if len(g.Cycles) != 1 {
			t.Fatalf("cycles = %+v, want exactly 1", g.Cycles)
		}
		if slices.Contains(g.Cycles[0].Slugs, "downstream") {
			t.Fatalf("downstream task named as a cycle member: %v", g.Cycles[0].Slugs)
		}
		if !slices.Equal(g.Cycles[0].Slugs, []string{"a", "b"}) {
			t.Fatalf("cycle = %v, want [a b]", g.Cycles[0].Slugs)
		}
	})
}

func TestSelfDependencyIsALengthOneCycle(t *testing.T) {
	withinTimeout(t, func() {
		g := Build([]storage.TaskMeta{task("a", "pending", "high", "", "a")})
		if len(g.Cycles) != 1 || !slices.Equal(g.Cycles[0].Slugs, []string{"a"}) {
			t.Fatalf("cycles = %+v, want a self-cycle on [a]", g.Cycles)
		}
	})
}

func TestParentCycleDoesNotSpinDepth(t *testing.T) {
	withinTimeout(t, func() {
		g := Build([]storage.TaskMeta{
			task("a", "pending", "high", "b"),
			task("b", "pending", "high", "a"),
		})

		var found bool
		for _, c := range g.Cycles {
			if c.Kind == "parent" && slices.Equal(c.Slugs, []string{"a", "b"}) {
				found = true
			}
		}
		if !found {
			t.Fatalf("parent cycle not reported: %+v", g.Cycles)
		}
		// Depth is capped, not looped. The value is unimportant; termination is.
		if g.Nodes["a"].Depth > 2 {
			t.Fatalf("depth = %d, want capped", g.Nodes["a"].Depth)
		}
		g.Grouped(false) // must also terminate
	})
}

func TestOrderPutsDependencyBeforeDependent(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("late", "pending", "critical", "", "early"),
		task("early", "pending", "low", ""),
	})

	i, j := slices.Index(g.Order, "early"), slices.Index(g.Order, "late")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("Order = %v, want early before late even though late is critical", g.Order)
	}
}

func TestIceboxIsHiddenByDefaultAndShownOnRequest(t *testing.T) {
	tasks := []storage.TaskMeta{
		task("hot", "pending", "high", ""),
		task("cold", storage.StatusIcebox, "low", ""),
	}
	g := Build(tasks)

	members := func(groups []Group) []string {
		var out []string
		for _, grp := range groups {
			out = append(out, grp.Members...)
		}
		slices.Sort(out)
		return out
	}

	if got := members(g.Grouped(false)); !slices.Equal(got, []string{"hot"}) {
		t.Fatalf("default view = %v, want icebox hidden", got)
	}
	if got := members(g.Grouped(true)); !slices.Equal(got, []string{"cold", "hot"}) {
		t.Fatalf("--all view = %v, want icebox shown", got)
	}
}

func TestArchivedTasksAreNotGrouped(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("old", "retired", "high", ""),
		task("live", "pending", "high", ""),
	})
	for _, grp := range g.Grouped(true) {
		if slices.Contains(grp.Members, "old") {
			t.Fatal("a retired task must not appear in the open-work view")
		}
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	tasks := []storage.TaskMeta{
		task("epic", "pending", "high", ""),
		task("z", "pending", "high", "epic", "y"),
		task("y", "pending", "high", "epic"),
		task("cycle-a", "pending", "low", "", "cycle-b"),
		task("cycle-b", "pending", "low", "", "cycle-a"),
		task("orphan", "pending", "medium", "ghost"),
	}

	first := Build(tasks)
	for range 20 {
		if got := Build(tasks); !reflect.DeepEqual(first, got) {
			t.Fatal("Build is not deterministic: map iteration order is reaching the output")
		}
	}
	if !reflect.DeepEqual(first.Grouped(true), Build(tasks).Grouped(true)) {
		t.Fatal("Grouped is not deterministic")
	}
}

// The footgun BuildFromVault exists to prevent: hand Build only the ACTIVE
// tasks and every completed dependency reads as a typo.
func TestBuildWithoutArchivedTasksMisreadsSatisfiedDepsAsDangling(t *testing.T) {
	active := []storage.TaskMeta{task("work", "pending", "high", "", "finished-thing")}
	g := Build(active)

	if len(g.Dangling) != 1 {
		t.Fatal("precondition: with the archive withheld, a satisfied dep looks dangling")
	}
	// ...which is exactly why BuildFromVault passes includeDone=true, and why no
	// production caller may call Build directly.
	full := append(active, task("finished-thing", "retired", "high", ""))
	if g2 := Build(full); len(g2.Dangling) != 0 {
		t.Fatalf("with the archive present the dep must resolve: %+v", g2.Dangling)
	}
}

// An epic NAMES its group; it is not a row inside it. Listing it among its own
// children sorted it into the middle of them (write-path-correctness appeared
// second in its own group), which reads as though the epic were a sibling of its
// own work. Caught by running it against the real backlog, not by a fixture.
func TestEpicIsNotAMemberOfItsOwnGroup(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("epic", "pending", "high", ""),
		task("child-a", "pending", "high", "epic"),
		task("child-b", "pending", "medium", "epic"),
	})

	for _, grp := range g.Grouped(false) {
		if grp.Epic == "epic" {
			if slices.Contains(grp.Members, "epic") {
				t.Fatalf("epic is listed among its own members: %v", grp.Members)
			}
			if !slices.Equal(grp.Members, []string{"child-a", "child-b"}) {
				t.Fatalf("members = %v, want just the children", grp.Members)
			}
			return
		}
	}
	t.Fatal("epic group missing")
}

// A nested epic (an epic under an epic) IS a member of its parent's group — it
// is only excluded from the group it heads.
func TestNestedEpicIsAMemberOfItsParentsGroup(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("top", "pending", "high", ""),
		task("mid", "pending", "high", "top"),
		task("leaf", "pending", "high", "mid"),
	})

	for _, grp := range g.Grouped(false) {
		if grp.Epic == "top" {
			if !slices.Equal(grp.Members, []string{"mid", "leaf"}) {
				t.Fatalf("members = %v, want [mid leaf] — the nested epic and its child", grp.Members)
			}
			if g.Nodes["leaf"].Depth != 2 {
				t.Fatalf("leaf depth = %d, want 2 so the renderer can step it in", g.Nodes["leaf"].Depth)
			}
			return
		}
	}
	t.Fatal("top group missing")
}
