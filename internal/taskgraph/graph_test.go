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
		Done:     status == "done" || status == "cancelled",
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
		task("epic", "planning", "high", ""),
		task("child-a", "planning", "high", "epic"),
		task("child-b", "planning", "low", "epic"),
		task("loner", "planning", "medium", ""),
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
		task("done-dep", "done", "high", ""),
		task("gone-dep", "cancelled", "high", ""),
		task("work", "planning", "high", "", "done-dep", "gone-dep"),
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
		task("work", "planning", "high", "", "no-such-task"),
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
		task("orphan", "planning", "high", "no-such-epic"),
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
		task("epic", "done", "high", ""),
		task("child", "planning", "high", "epic"),
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
			task("a", "planning", "high", "", "c"),
			task("b", "planning", "high", "", "a"),
			task("c", "planning", "high", "", "b"),
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
			task("a", "planning", "high", "", "b"),
			task("b", "planning", "high", "", "a"),
			task("downstream", "planning", "high", "", "a"),
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
		g := Build([]storage.TaskMeta{task("a", "planning", "high", "", "a")})
		if len(g.Cycles) != 1 || !slices.Equal(g.Cycles[0].Slugs, []string{"a"}) {
			t.Fatalf("cycles = %+v, want a self-cycle on [a]", g.Cycles)
		}
	})
}

func TestParentCycleDoesNotSpinDepth(t *testing.T) {
	withinTimeout(t, func() {
		g := Build([]storage.TaskMeta{
			task("a", "planning", "high", "b"),
			task("b", "planning", "high", "a"),
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
		task("late", "planning", "critical", "", "early"),
		task("early", "planning", "low", ""),
	})

	i, j := slices.Index(g.Order, "early"), slices.Index(g.Order, "late")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("Order = %v, want early before late even though late is critical", g.Order)
	}
}

func TestIceboxIsHiddenByDefaultAndShownOnRequest(t *testing.T) {
	tasks := []storage.TaskMeta{
		task("hot", "planning", "high", ""),
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
		task("old", "done", "high", ""),
		task("live", "planning", "high", ""),
	})
	for _, grp := range g.Grouped(true) {
		if slices.Contains(grp.Members, "old") {
			t.Fatal("a retired task must not appear in the open-work view")
		}
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	tasks := []storage.TaskMeta{
		task("epic", "planning", "high", ""),
		task("z", "planning", "high", "epic", "y"),
		task("y", "planning", "high", "epic"),
		task("cycle-a", "planning", "low", "", "cycle-b"),
		task("cycle-b", "planning", "low", "", "cycle-a"),
		task("orphan", "planning", "medium", "ghost"),
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
	active := []storage.TaskMeta{task("work", "planning", "high", "", "finished-thing")}
	g := Build(active)

	if len(g.Dangling) != 1 {
		t.Fatal("precondition: with the archive withheld, a satisfied dep looks dangling")
	}
	// ...which is exactly why BuildFromVault passes includeDone=true, and why no
	// production caller may call Build directly.
	full := append(active, task("finished-thing", "done", "high", ""))
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
		task("epic", "planning", "high", ""),
		task("child-a", "planning", "high", "epic"),
		task("child-b", "planning", "medium", "epic"),
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
		task("top", "planning", "high", ""),
		task("mid", "planning", "high", "top"),
		task("leaf", "planning", "high", "mid"),
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

// memberSet flattens every group's members into one sorted set, dropping the
// epic labels — enough to assert presence/absence regardless of grouping.
func memberSet(groups []Group) []string {
	var out []string
	for _, grp := range groups {
		out = append(out, grp.Members...)
	}
	slices.Sort(out)
	return out
}

// includeArchived=false hides Done work (reproducing Grouped); includeArchived=true
// surfaces it. The wrapper and the archived form must agree on the false case.
func TestGroupedArchivedIncludesDoneOnlyWhenAsked(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("epic", "planning", "high", ""),
		task("live", "planning", "high", "epic"),
		task("finished", "done", "high", "epic"),
	})

	open := memberSet(g.GroupedArchived(false, false))
	if slices.Contains(open, "finished") {
		t.Fatalf("Done member leaked into the open view: %v", open)
	}
	if !slices.Contains(open, "live") {
		t.Fatalf("active member missing from the open view: %v", open)
	}

	withDone := memberSet(g.GroupedArchived(false, true))
	if !slices.Contains(withDone, "finished") {
		t.Fatalf("Done member absent with includeArchived=true: %v", withDone)
	}

	// The exported wrapper is byte-for-byte the archived form with the flag off.
	if !reflect.DeepEqual(g.Grouped(false), g.GroupedArchived(false, false)) {
		t.Fatal("Grouped(x) must equal GroupedArchived(x, false)")
	}
	if !reflect.DeepEqual(g.Grouped(true), g.GroupedArchived(true, false)) {
		t.Fatal("Grouped(x) must equal GroupedArchived(x, false) for includeIcebox=true too")
	}
}

// Subtree collects the root plus ALL transitive descendants — Node.Children is
// direct-only, so every level below must be reached by walking, not by one hop.
func TestSubtreeReturnsFullTransitiveSet(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("epic", "planning", "high", ""),
		task("story", "planning", "high", "epic"),
		task("leaf", "planning", "high", "story"),
		task("elsewhere", "planning", "high", ""),
	})

	grp, ok := g.Subtree("epic", false, false)
	if !ok {
		t.Fatal("Subtree(epic) must report ok=true — the root exists")
	}
	if grp.Epic != "epic" {
		t.Fatalf("Group.Epic = %q, want the requested root %q", grp.Epic, "epic")
	}
	if !slices.Equal(grp.Members, []string{"epic", "story", "leaf"}) {
		t.Fatalf("Subtree(epic) members = %v, want the full chain including the deepest leaf", grp.Members)
	}
	if slices.Contains(grp.Members, "elsewhere") {
		t.Fatalf("Subtree(epic) leaked an unrelated task: %v", grp.Members)
	}

	// A nested root returns only its own subtree.
	sub, ok := g.Subtree("story", false, false)
	if !ok {
		t.Fatal("Subtree(story) must report ok=true")
	}
	if !slices.Equal(sub.Members, []string{"story", "leaf"}) {
		t.Fatalf("Subtree(story) members = %v, want [story leaf]", sub.Members)
	}

	// A leaf's subtree is simply itself — not an error.
	leaf, ok := g.Subtree("leaf", false, false)
	if !ok {
		t.Fatal("Subtree(leaf) must report ok=true")
	}
	if !slices.Equal(leaf.Members, []string{"leaf"}) {
		t.Fatalf("Subtree(leaf) members = %v, want just [leaf]", leaf.Members)
	}

	// A nonexistent root is the only ok=false case.
	if _, ok := g.Subtree("no-such-root", false, false); ok {
		t.Fatal("Subtree(nonexistent) must report ok=false")
	}
}

// Subtree honors the same filters as grouped, applied to the root as well.
func TestSubtreeFiltersDoneAndIcebox(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("epic", "planning", "high", ""),
		task("live", "planning", "high", "epic"),
		task("finished", "done", "high", "epic"),
		task("cold", storage.StatusIcebox, "low", "epic"),
	})

	def, _ := g.Subtree("epic", false, false)
	if !slices.Equal(def.Members, []string{"epic", "live"}) {
		t.Fatalf("default Subtree members = %v, want Done and icebox hidden", def.Members)
	}

	all, _ := g.Subtree("epic", true, true)
	got := append([]string(nil), all.Members...)
	slices.Sort(got)
	if !slices.Equal(got, []string{"cold", "epic", "finished", "live"}) {
		t.Fatalf("Subtree(all) members = %v, want everything under the epic", all.Members)
	}

	// A root that exists but is itself filtered out yields ok=true, empty members.
	gd := Build([]storage.TaskMeta{
		task("done-epic", "done", "high", ""),
		task("done-child", "done", "high", "done-epic"),
	})
	grp, ok := gd.Subtree("done-epic", false, false)
	if !ok {
		t.Fatal("a Done root still EXISTS — ok must be true")
	}
	if len(grp.Members) != 0 {
		t.Fatalf("a Done root without includeArchived must yield empty members: %v", grp.Members)
	}
}

// Subtree must terminate even when Children form a parent cycle.
func TestSubtreeTerminatesOnParentCycle(t *testing.T) {
	withinTimeout(t, func() {
		g := Build([]storage.TaskMeta{
			task("a", "planning", "high", "b"),
			task("b", "planning", "high", "a"),
		})
		grp, ok := g.Subtree("a", false, false)
		if !ok {
			t.Fatal("Subtree(a) must report ok=true")
		}
		if !slices.Contains(grp.Members, "a") {
			t.Fatalf("Subtree(a) must contain its own root: %v", grp.Members)
		}
	})
}

// Role is DERIVED from edges: children make an epic; a resolvable parent demotes
// it to a story; no children makes a task.
func TestRoleDerivesEpicStoryTask(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("epic", "planning", "high", ""),
		task("story", "planning", "high", "epic"),
		task("leaf", "planning", "high", "story"),
	})

	if got := g.Role("epic"); got != "epic" {
		t.Fatalf("Role(epic) = %q, want epic — children, no parent", got)
	}
	if got := g.Role("story"); got != "story" {
		t.Fatalf("Role(story) = %q, want story — children AND a resolvable parent", got)
	}
	if got := g.Role("leaf"); got != "task" {
		t.Fatalf("Role(leaf) = %q, want task — no children", got)
	}

	if !g.IsRootEpic("epic") || g.IsRootEpic("story") || g.IsRootEpic("leaf") {
		t.Fatal("IsRootEpic must be true only for the root epic")
	}
	if !g.IsStory("story") || g.IsStory("epic") || g.IsStory("leaf") {
		t.Fatal("IsStory must be true only for the nested epic")
	}
}

// A node with children whose named parent is MISSING is a root "epic" — its own
// root — matching root(), not a "story". hasResolvableParent is the graph-level
// fact that tells the two apart.
func TestRoleDanglingParentWithChildrenIsEpic(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("head", "planning", "high", "ghost"), // parent does not exist
		task("child", "planning", "high", "head"), // has a child (leaf) AND a resolvable parent
		task("leaf", "planning", "high", "child"),
	})

	if !g.hasResolvableParent("child") {
		t.Fatal("child's parent (head) exists — it must resolve")
	}
	if g.hasResolvableParent("head") {
		t.Fatal("head's parent (ghost) is missing — it must NOT resolve")
	}
	if got := g.Role("head"); got != "epic" {
		t.Fatalf("Role(head) = %q, want epic — a dangling parent makes it its own root", got)
	}
	if got := g.Role("child"); got != "story" {
		t.Fatalf("Role(child) = %q, want story — it has a child AND a resolvable parent", got)
	}
}

// IsEpic() semantics are UNCHANGED: it is len(Children) > 0, nothing more. A
// childless node is not an epic; a nested epic with children still is.
func TestIsEpicSemanticsUnchanged(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("top", "planning", "high", ""),
		task("mid", "planning", "high", "top"), // nested epic: has a child AND a parent
		task("leaf", "planning", "high", "mid"),
	})

	if !g.Nodes["top"].IsEpic() {
		t.Fatal("top has children — IsEpic must be true")
	}
	if !g.Nodes["mid"].IsEpic() {
		t.Fatal("a nested epic still has children — IsEpic must remain true regardless of its parent")
	}
	if g.Nodes["leaf"].IsEpic() {
		t.Fatal("a childless node is not an epic")
	}
}

// ---------------------------------------------------------------------------
// SupersededBy (board-reporting-supersession-link): RefKind.String()'s
// three-way switch, the derived Supersedes reverse view (fan-in, direct-edge
// only), the SupersededByRef cycle walk, dangling links, and HistoryLabel.
// ---------------------------------------------------------------------------

// TestRefKindString is the direct regression test for finding 1 of the
// plan's review: RefKind.String() used to be `if k == ParentRef { "parent" }
// else { "depends" }`, which would have silently mislabeled SupersededByRef
// as "depends" the moment it was added to the enum. Asserted directly
// against the string, not only indirectly through a Cycle/Dangling fixture,
// and asserts the three values are pairwise DISTINCT — the exact shape of
// the bug this guards against.
func TestRefKindString(t *testing.T) {
	cases := []struct {
		kind RefKind
		want string
	}{
		{ParentRef, "parent"},
		{DependsRef, "depends"},
		{SupersededByRef, "superseded_by"},
	}
	seen := make(map[string]RefKind)
	for _, tc := range cases {
		got := tc.kind.String()
		if got != tc.want {
			t.Errorf("RefKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
		if other, dup := seen[got]; dup {
			t.Fatalf("RefKind(%d) and RefKind(%d) both stringify to %q — exactly the finding-1 regression", tc.kind, other, got)
		}
		seen[got] = tc.kind
	}
}

func TestSupersededByFieldParsesIntoNode(t *testing.T) {
	a := task("a", "cancelled", "high", "")
	a.SupersededBy = "b"
	g := Build([]storage.TaskMeta{a, task("b", "pending", "high", "")})

	if g.Nodes["a"].Meta.SupersededBy != "b" {
		t.Fatalf("Meta.SupersededBy = %q, want %q", g.Nodes["a"].Meta.SupersededBy, "b")
	}
}

// TestSupersededByFanIn is the Acceptance-named fan-in case: two abandoned
// tasks pointing at the same successor. The derived reverse view is a list,
// not a scalar, sorted for a stable order across runs.
func TestSupersededByFanIn(t *testing.T) {
	a := task("a", "cancelled", "high", "")
	a.SupersededBy = "c"
	b := task("b", "cancelled", "high", "")
	b.SupersededBy = "c"
	c := task("c", "pending", "high", "")

	g := Build([]storage.TaskMeta{a, b, c})

	if !slices.Equal(g.Nodes["c"].Supersedes, []string{"a", "b"}) {
		t.Fatalf("Supersedes(c) = %v, want sorted fan-in [a b]", g.Nodes["c"].Supersedes)
	}
	if len(g.Dangling) != 0 {
		t.Fatalf("no dangling SupersededBy expected in a fan-in with a real successor: %+v", g.Dangling)
	}
}

// TestSupersededByChainReverseViewIsDirectEdgeOnly pins the chain decision:
// A→B→C is not a cycle, and C's derived Supersedes contains only its DIRECT
// predecessor B — never A, transitively. Matches how Children/IsEpic are
// also non-transitive.
func TestSupersededByChainReverseViewIsDirectEdgeOnly(t *testing.T) {
	withinTimeout(t, func() {
		a := task("a", "cancelled", "high", "")
		a.SupersededBy = "b"
		b := task("b", "cancelled", "high", "")
		b.SupersededBy = "c"
		c := task("c", "pending", "high", "")

		g := Build([]storage.TaskMeta{a, b, c})

		if len(g.Cycles) != 0 {
			t.Fatalf("a chain is not a cycle: %+v", g.Cycles)
		}
		if !slices.Equal(g.Nodes["b"].Supersedes, []string{"a"}) {
			t.Fatalf("Supersedes(b) = %v, want [a]", g.Nodes["b"].Supersedes)
		}
		if !slices.Equal(g.Nodes["c"].Supersedes, []string{"b"}) {
			t.Fatalf("Supersedes(c) = %v, want [b] — direct-edge only, NOT transitively including a", g.Nodes["c"].Supersedes)
		}
	})
}

// TestSupersededByCycleIsDetectedNotWalked is the Acceptance-named cycle
// case: A.SupersededBy=B, B.SupersededBy=A. Asserted against the Kind
// STRING, not the RefKind constant — a stringer regression (finding 1) is
// exactly the failure mode this guards against, mirroring
// TestDependencyCycleIsDetectedNotWalked's own shape for Depends.
func TestSupersededByCycleIsDetectedNotWalked(t *testing.T) {
	withinTimeout(t, func() {
		a := task("a", "cancelled", "high", "")
		a.SupersededBy = "b"
		b := task("b", "cancelled", "high", "")
		b.SupersededBy = "a"

		g := Build([]storage.TaskMeta{a, b})

		var found *Cycle
		for i := range g.Cycles {
			if g.Cycles[i].Kind == "superseded_by" {
				found = &g.Cycles[i]
			}
		}
		if found == nil {
			t.Fatalf("no superseded_by cycle reported: %+v", g.Cycles)
		}
		if !slices.Equal(found.Slugs, []string{"a", "b"}) {
			t.Fatalf("cycle slugs = %v, want rotated [a b]", found.Slugs)
		}
		if !g.HasProblems() {
			t.Fatal("a SupersededBy cycle is a problem")
		}
	})
}

// TestSelfSupersessionIsALengthOneCycle mirrors
// TestSelfDependencyIsALengthOneCycle: internal/storage refuses this at
// write time (normalizeSupersededBy), but this package's contract is that it
// NEVER hangs on malformed data regardless of how it got there, so Build
// must still detect rather than loop on a directly-constructed A→A edge.
func TestSelfSupersessionIsALengthOneCycle(t *testing.T) {
	withinTimeout(t, func() {
		a := task("a", "cancelled", "high", "")
		a.SupersededBy = "a"
		g := Build([]storage.TaskMeta{a})

		var found *Cycle
		for i := range g.Cycles {
			if g.Cycles[i].Kind == "superseded_by" {
				found = &g.Cycles[i]
			}
		}
		if found == nil || !slices.Equal(found.Slugs, []string{"a"}) {
			t.Fatalf("cycles = %+v, want a self-cycle on [a] for superseded_by", g.Cycles)
		}
	})
}

func TestSupersededByDanglingIsReportedButIsNotACycle(t *testing.T) {
	a := task("a", "cancelled", "high", "")
	a.SupersededBy = "no-such-task"

	g := Build([]storage.TaskMeta{a})

	var found bool
	for _, d := range g.Dangling {
		if d.Kind == "superseded_by" && d.From == "a" && d.To == "no-such-task" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dangling SupersededBy not reported: %+v", g.Dangling)
	}
	if len(g.Cycles) != 0 {
		t.Fatalf("a dangling link is not a cycle: %+v", g.Cycles)
	}
}

// TestHistoryLabel confirms the board/History render helper is a pure
// function of a node's own metadata: the supersession suffix appears ONLY
// for a cancelled task that actually carries a link, and every other task
// (cancelled without a link, or not cancelled at all) renders its bare
// status.
func TestHistoryLabel(t *testing.T) {
	cancelledWithLink := task("a", "cancelled", "high", "")
	cancelledWithLink.SupersededBy = "b"
	cancelledNoLink := task("c", "cancelled", "high", "")
	activeTask := task("d", "pending", "high", "")
	successor := task("b", "pending", "high", "")

	g := Build([]storage.TaskMeta{cancelledWithLink, cancelledNoLink, activeTask, successor})

	if got, want := g.Nodes["a"].HistoryLabel(), "cancelled → superseded by b"; got != want {
		t.Errorf("HistoryLabel(a) = %q, want %q", got, want)
	}
	if got, want := g.Nodes["c"].HistoryLabel(), "cancelled"; got != want {
		t.Errorf("HistoryLabel(c) = %q, want bare status %q", got, want)
	}
	if got, want := g.Nodes["d"].HistoryLabel(), "pending"; got != want {
		t.Errorf("HistoryLabel(d) = %q, want bare status %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Board (board-reporting-three-bucket-grouping): StatusBucket, BoardView and
// Graph.Board() — the three-bucket board report.
// ---------------------------------------------------------------------------

// findGroup returns the single group in groups whose Epic matches, if any.
func findGroup(groups []Group, epic string) (Group, bool) {
	for _, g := range groups {
		if g.Epic == epic {
			return g, true
		}
	}
	return Group{}, false
}

// countAppearances counts how many times slug shows up across all three
// buckets of view, either as a group's Epic or inside a group's Members. A
// root epic or a standalone task must appear exactly once, total.
func countAppearances(view BoardView, slug string) int {
	var all []Group
	all = append(all, view.Active...)
	all = append(all, view.Icebox...)
	all = append(all, view.History...)

	count := 0
	for _, grp := range all {
		if grp.Epic == slug {
			count++
		}
		if slices.Contains(grp.Members, slug) {
			count++
		}
	}
	return count
}

// bucketContaining reports which bucket holds the group named by epic, if
// any is found.
func bucketContaining(view BoardView, epic string) (Bucket, bool) {
	if _, ok := findGroup(view.Active, epic); ok {
		return BucketActive, true
	}
	if _, ok := findGroup(view.Icebox, epic); ok {
		return BucketIcebox, true
	}
	if _, ok := findGroup(view.History, epic); ok {
		return BucketHistory, true
	}
	return 0, false
}

func TestStatusBucket(t *testing.T) {
	cases := []struct {
		name   string
		status string
		done   bool
		want   Bucket
	}{
		{"planning", "planning", false, BucketActive},
		{"reviewed", "reviewed", false, BucketActive},
		{"in_progress", "in_progress", false, BucketActive},
		{"blocked", "blocked", false, BucketActive},
		{"icebox", storage.StatusIcebox, false, BucketIcebox},
		{"done via Done flag", storage.StatusDone, true, BucketHistory},
		{"cancelled via Done flag", storage.StatusCancelled, true, BucketHistory},
		// Legacy strings: the directory-derived Done flag decides it, not the
		// string on disk.
		{"legacy pending, Done false", "pending", false, BucketActive},
		{"legacy retired, Done true", "retired", true, BucketHistory},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := storage.TaskMeta{Slug: "x", Status: tc.status, Done: tc.done}
			if got := StatusBucket(meta); got != tc.want {
				t.Fatalf("StatusBucket(%+v) = %v, want %v", meta, got, tc.want)
			}
		})
	}
}

func TestBucketString(t *testing.T) {
	cases := []struct {
		b    Bucket
		want string
	}{
		{BucketActive, "active"},
		{BucketIcebox, "icebox"},
		{BucketHistory, "history"},
	}
	for _, tc := range cases {
		if got := tc.b.String(); got != tc.want {
			t.Fatalf("Bucket(%d).String() = %q, want %q", tc.b, got, tc.want)
		}
	}
}

// TestBoardPOLAEpicBucketIgnoresChildMutation is a MUTATION regression, not a
// static fixture: it builds the graph, asserts the epic's bucket, mutates
// ONLY the child's status, rebuilds, and asserts the epic's bucket is
// unchanged. An epic's own status decides its bucket — never a child's.
func TestBoardPOLAEpicBucketIgnoresChildMutation(t *testing.T) {
	epic := task("pola-epic", "in_progress", "high", "")
	child := task("pola-child", "planning", "high", "pola-epic")

	g := Build([]storage.TaskMeta{epic, child})
	view := g.Board()
	b, ok := bucketContaining(view, "pola-epic")
	if !ok || b != BucketActive {
		t.Fatalf("epic bucket = %v (found=%v), want Active", b, ok)
	}

	// Mutate ONLY the child's status and re-run Board() — the epic's own
	// Status/Done never changed, so its bucket must not either.
	child.Status = storage.StatusIcebox
	g2 := Build([]storage.TaskMeta{epic, child})
	view2 := g2.Board()
	b2, ok2 := bucketContaining(view2, "pola-epic")
	if !ok2 || b2 != BucketActive {
		t.Fatalf("after mutating only the child's status, epic bucket = %v (found=%v), want STILL Active (POLA)", b2, ok2)
	}
}

// TestBoardPartitionCompleteness builds a representative mix — active,
// iceboxed, and done epics, a stale-parented child, standalone tasks in all
// three states, and a SupersededBy link — and asserts every root
// epic/standalone slug appears in EXACTLY ONE bucket, with t.Fatal on zero or
// two appearances.
func TestBoardPartitionCompleteness(t *testing.T) {
	tasks := []storage.TaskMeta{
		task("epic-active", "in_progress", "high", ""),
		task("child-of-active", "planning", "high", "epic-active"),
		task("epic-icebox", storage.StatusIcebox, "medium", ""),
		task("child-of-icebox", "planning", "medium", "epic-icebox"),
		task("epic-done", "done", "high", ""),
		task("child-of-done", "done", "high", "epic-done"),
		task("epic-done-stale", "done", "high", ""),
		task("stale-child", "planning", "high", "epic-done-stale"),
		task("standalone-active", "planning", "low", ""),
		task("standalone-icebox", storage.StatusIcebox, "low", ""),
		task("standalone-done", "done", "low", ""),
		task("successor-task", "planning", "low", ""),
	}
	linked := task("cancelled-with-link", "cancelled", "low", "")
	linked.SupersededBy = "successor-task"
	tasks = append(tasks, linked)

	g := Build(tasks)
	view := g.Board()

	roots := []string{
		"epic-active", "epic-icebox", "epic-done", "epic-done-stale",
		"standalone-active", "standalone-icebox", "standalone-done",
		"cancelled-with-link", "successor-task",
	}
	for _, slug := range roots {
		if n := countAppearances(view, slug); n != 1 {
			t.Fatalf("%q appears %d times across the three buckets, want exactly 1", slug, n)
		}
	}
}

// TestBoardStaleParentChildStaysInHistoryGroupAndFlagged: an active child of
// a done parent is NOT hidden or relocated — it stays a Member of the done
// parent's (History) group, AND is separately present in g.StaleParents.
func TestBoardStaleParentChildStaysInHistoryGroupAndFlagged(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("done-parent", "done", "high", ""),
		task("stale-child", "planning", "high", "done-parent"),
	})
	view := g.Board()

	grp, ok := findGroup(view.History, "done-parent")
	if !ok {
		t.Fatal("done-parent group missing from History")
	}
	if !slices.Contains(grp.Members, "stale-child") {
		t.Fatalf("stale-child missing from done-parent's History members: %v", grp.Members)
	}
	if len(g.StaleParents) != 1 || g.StaleParents[0].From != "stale-child" {
		t.Fatalf("StaleParents = %+v, want stale-child flagged", g.StaleParents)
	}
}

// TestBoardStandaloneSplitsByStatus: three standalone tasks in three
// different statuses must produce three SEPARATE one-member Group{Epic: ""}
// entries, one per bucket — not one merged group misfiled into a single
// bucket.
func TestBoardStandaloneSplitsByStatus(t *testing.T) {
	g := Build([]storage.TaskMeta{
		task("standalone-active", "planning", "high", ""),
		task("standalone-icebox", storage.StatusIcebox, "high", ""),
		task("standalone-done", "done", "high", ""),
	})
	view := g.Board()

	checkOne := func(groups []Group, want string) {
		t.Helper()
		grp, ok := findGroup(groups, "")
		if !ok {
			t.Fatalf("no standalone group found, want one containing %q", want)
		}
		if !slices.Equal(grp.Members, []string{want}) {
			t.Fatalf("standalone group members = %v, want [%s]", grp.Members, want)
		}
	}
	checkOne(view.Active, "standalone-active")
	checkOne(view.Icebox, "standalone-icebox")
	checkOne(view.History, "standalone-done")

	for _, groups := range [][]Group{view.Active, view.Icebox, view.History} {
		var standaloneCount int
		for _, grp := range groups {
			if grp.Epic == "" {
				standaloneCount++
			}
		}
		if standaloneCount > 1 {
			t.Fatalf("more than one standalone group in a single bucket: %v", groups)
		}
	}
}

// TestBoardHistoryGroupsSortDescendingByModTime uses 3 epics so a stable-sort
// accident can't mask an ordering bug.
func TestBoardHistoryGroupsSortDescendingByModTime(t *testing.T) {
	e1 := task("hgroup-epic-1", "done", "high", "")
	e1.ModTime = "2026-01-01"
	c1 := task("hgroup-child-1", "done", "high", "hgroup-epic-1")
	e2 := task("hgroup-epic-2", "done", "high", "")
	e2.ModTime = "2026-03-01"
	c2 := task("hgroup-child-2", "done", "high", "hgroup-epic-2")
	e3 := task("hgroup-epic-3", "done", "high", "")
	e3.ModTime = "2026-02-01"
	c3 := task("hgroup-child-3", "done", "high", "hgroup-epic-3")

	g := Build([]storage.TaskMeta{e1, c1, e2, c2, e3, c3})
	view := g.Board()

	var order []string
	for _, grp := range view.History {
		order = append(order, grp.Epic)
	}
	want := []string{"hgroup-epic-2", "hgroup-epic-3", "hgroup-epic-1"}
	if !slices.Equal(order, want) {
		t.Fatalf("History group order = %v, want %v (ModTime descending)", order, want)
	}
}

// TestBoardActiveAndIceboxGroupsSortAscendingByCreateTime uses 3 epics per
// bucket so a stable-sort accident can't mask an ordering bug.
func TestBoardActiveAndIceboxGroupsSortAscendingByCreateTime(t *testing.T) {
	a1 := task("agroup-epic-a", "in_progress", "high", "")
	a1.CreateTime = "2026-02-01"
	ac1 := task("agroup-child-a", "planning", "high", "agroup-epic-a")
	a2 := task("agroup-epic-b", "in_progress", "high", "")
	a2.CreateTime = "2026-01-01"
	ac2 := task("agroup-child-b", "planning", "high", "agroup-epic-b")
	a3 := task("agroup-epic-c", "in_progress", "high", "")
	a3.CreateTime = "2026-03-01"
	ac3 := task("agroup-child-c", "planning", "high", "agroup-epic-c")

	i1 := task("igroup-epic-a", storage.StatusIcebox, "high", "")
	i1.CreateTime = "2026-02-01"
	ic1 := task("igroup-child-a", "planning", "high", "igroup-epic-a")
	i2 := task("igroup-epic-b", storage.StatusIcebox, "high", "")
	i2.CreateTime = "2026-01-01"
	ic2 := task("igroup-child-b", "planning", "high", "igroup-epic-b")
	i3 := task("igroup-epic-c", storage.StatusIcebox, "high", "")
	i3.CreateTime = "2026-03-01"
	ic3 := task("igroup-child-c", "planning", "high", "igroup-epic-c")

	g := Build([]storage.TaskMeta{a1, ac1, a2, ac2, a3, ac3, i1, ic1, i2, ic2, i3, ic3})
	view := g.Board()

	var activeOrder []string
	for _, grp := range view.Active {
		activeOrder = append(activeOrder, grp.Epic)
	}
	wantActive := []string{"agroup-epic-b", "agroup-epic-a", "agroup-epic-c"}
	if !slices.Equal(activeOrder, wantActive) {
		t.Fatalf("Active group order = %v, want %v (CreateTime ascending)", activeOrder, wantActive)
	}

	var iceboxOrder []string
	for _, grp := range view.Icebox {
		iceboxOrder = append(iceboxOrder, grp.Epic)
	}
	wantIcebox := []string{"igroup-epic-b", "igroup-epic-a", "igroup-epic-c"}
	if !slices.Equal(iceboxOrder, wantIcebox) {
		t.Fatalf("Icebox group order = %v, want %v (CreateTime ascending)", iceboxOrder, wantIcebox)
	}
}

// TestBoardHistoryMembersSortByModTimeNotCreateTime is the specific
// regression test for a blanket-CreateTime rule: CreateTime order and
// ModTime order are DELIBERATELY different here, and History-bucket members
// must follow ModTime.
func TestBoardHistoryMembersSortByModTimeNotCreateTime(t *testing.T) {
	epic := task("hmember-epic", "done", "high", "")
	c1 := task("hmember-child-1", "done", "high", "hmember-epic")
	c1.CreateTime = "2026-01-01" // oldest created
	c1.ModTime = "2026-03-01"    // most recently completed
	c2 := task("hmember-child-2", "done", "high", "hmember-epic")
	c2.CreateTime = "2026-02-01"
	c2.ModTime = "2026-01-01" // completed first
	c3 := task("hmember-child-3", "done", "high", "hmember-epic")
	c3.CreateTime = "2026-03-01" // newest created
	c3.ModTime = "2026-02-01"

	g := Build([]storage.TaskMeta{epic, c1, c2, c3})
	view := g.Board()

	grp, ok := findGroup(view.History, "hmember-epic")
	if !ok {
		t.Fatal("hmember-epic group missing")
	}
	want := []string{"hmember-child-1", "hmember-child-3", "hmember-child-2"}
	if !slices.Equal(grp.Members, want) {
		t.Fatalf("History members = %v, want ModTime-descending %v (NOT the CreateTime order)", grp.Members, want)
	}
}

// TestBoardActiveAndIceboxMembersSortByCreateTimeDescending.
func TestBoardActiveAndIceboxMembersSortByCreateTimeDescending(t *testing.T) {
	epic := task("amember-epic", "in_progress", "high", "")
	c1 := task("amember-child-1", "planning", "high", "amember-epic")
	c1.CreateTime = "2026-01-01"
	c2 := task("amember-child-2", "planning", "high", "amember-epic")
	c2.CreateTime = "2026-03-01"
	c3 := task("amember-child-3", "planning", "high", "amember-epic")
	c3.CreateTime = "2026-02-01"

	g := Build([]storage.TaskMeta{epic, c1, c2, c3})
	view := g.Board()

	grp, ok := findGroup(view.Active, "amember-epic")
	if !ok {
		t.Fatal("amember-epic group missing")
	}
	want := []string{"amember-child-2", "amember-child-3", "amember-child-1"}
	if !slices.Equal(grp.Members, want) {
		t.Fatalf("Active members = %v, want CreateTime-descending %v", grp.Members, want)
	}
}

// TestBoardDoneChildInActiveEpicSortsByCreateTimeLikeSiblings: a done child
// inside a still-Active epic must sort on the SAME axis (CreateTime) as its
// open siblings, not by its own ModTime.
func TestBoardDoneChildInActiveEpicSortsByCreateTimeLikeSiblings(t *testing.T) {
	epic := task("mixed-active-epic", "in_progress", "high", "")
	openChild := task("open-child", "planning", "high", "mixed-active-epic")
	openChild.CreateTime = "2026-01-01"
	doneChild := task("done-child", "done", "high", "mixed-active-epic")
	doneChild.CreateTime = "2026-02-01"
	doneChild.ModTime = "2026-06-01" // if ModTime were used, this would sort first regardless

	g := Build([]storage.TaskMeta{epic, openChild, doneChild})
	view := g.Board()

	grp, ok := findGroup(view.Active, "mixed-active-epic")
	if !ok {
		t.Fatal("mixed-active-epic group missing")
	}
	want := []string{"done-child", "open-child"}
	if !slices.Equal(grp.Members, want) {
		t.Fatalf("members = %v, want CreateTime-descending %v regardless of done-child's own ModTime", grp.Members, want)
	}
}

// TestBoardGroupTieBreaksOnEpicSlug: two epics share an identical CreateTime
// and must sort by Epic slug, asserted against the specific expected order
// and re-run to rule out stable-sort luck.
func TestBoardGroupTieBreaksOnEpicSlug(t *testing.T) {
	e1 := task("zzz-tie-epic", "in_progress", "high", "")
	e1.CreateTime = "2026-01-01"
	c1 := task("zzz-tie-child", "planning", "high", "zzz-tie-epic")
	e2 := task("aaa-tie-epic", "in_progress", "high", "")
	e2.CreateTime = "2026-01-01"
	c2 := task("aaa-tie-child", "planning", "high", "aaa-tie-epic")

	for i := 0; i < 5; i++ {
		g := Build([]storage.TaskMeta{e1, c1, e2, c2})
		view := g.Board()
		var order []string
		for _, grp := range view.Active {
			order = append(order, grp.Epic)
		}
		want := []string{"aaa-tie-epic", "zzz-tie-epic"}
		if !slices.Equal(order, want) {
			t.Fatalf("run %d: Active group order = %v, want %v (slug tiebreaker on equal CreateTime)", i, order, want)
		}
	}
}

// TestBoardMemberTieBreaksOnSlug: two members share an identical ModTime and
// must sort by slug, asserted against the specific expected order and re-run
// to rule out stable-sort luck.
func TestBoardMemberTieBreaksOnSlug(t *testing.T) {
	epic := task("tie-member-epic", "done", "high", "")
	c1 := task("zzz-tie-member", "done", "high", "tie-member-epic")
	c1.ModTime = "2026-01-01"
	c2 := task("aaa-tie-member", "done", "high", "tie-member-epic")
	c2.ModTime = "2026-01-01"

	for i := 0; i < 5; i++ {
		g := Build([]storage.TaskMeta{epic, c1, c2})
		view := g.Board()
		grp, ok := findGroup(view.History, "tie-member-epic")
		if !ok {
			t.Fatal("tie-member-epic group missing")
		}
		want := []string{"aaa-tie-member", "zzz-tie-member"}
		if !slices.Equal(grp.Members, want) {
			t.Fatalf("run %d: members = %v, want %v on equal ModTime", i, grp.Members, want)
		}
	}
}

// TestBoardGroupsWithMissingCreateTimeSortLast: a legacy epic with no
// CreateTime, mixed into the Active bucket with dated siblings, must not
// crash the sort and must land at the END (ascending convention).
func TestBoardGroupsWithMissingCreateTimeSortLast(t *testing.T) {
	dated1 := task("gmiss-epic-1", "in_progress", "high", "")
	dated1.CreateTime = "2026-01-01"
	c1 := task("gmiss-child-1", "planning", "high", "gmiss-epic-1")
	dated2 := task("gmiss-epic-2", "in_progress", "high", "")
	dated2.CreateTime = "2026-02-01"
	c2 := task("gmiss-child-2", "planning", "high", "gmiss-epic-2")
	legacy := task("gmiss-epic-legacy", "in_progress", "high", "") // no CreateTime
	c3 := task("gmiss-child-legacy", "planning", "high", "gmiss-epic-legacy")

	g := Build([]storage.TaskMeta{dated1, c1, dated2, c2, legacy, c3})
	view := g.Board()

	var order []string
	for _, grp := range view.Active {
		order = append(order, grp.Epic)
	}
	want := []string{"gmiss-epic-1", "gmiss-epic-2", "gmiss-epic-legacy"}
	if !slices.Equal(order, want) {
		t.Fatalf("Active group order = %v, want dated groups first, legacy (no CreateTime) last: %v", order, want)
	}
}

// TestBoardGroupsWithMissingModTimeSortLast: a legacy done epic with no
// ModTime, mixed into the History bucket with dated siblings, must not
// crash the sort and must land at the END (descending convention).
func TestBoardGroupsWithMissingModTimeSortLast(t *testing.T) {
	dated1 := task("hmiss-epic-1", "done", "high", "")
	dated1.ModTime = "2026-02-01"
	c1 := task("hmiss-child-1", "done", "high", "hmiss-epic-1")
	dated2 := task("hmiss-epic-2", "done", "high", "")
	dated2.ModTime = "2026-01-01"
	c2 := task("hmiss-child-2", "done", "high", "hmiss-epic-2")
	legacy := task("hmiss-epic-legacy", "done", "high", "") // no ModTime
	c3 := task("hmiss-child-legacy", "done", "high", "hmiss-epic-legacy")

	g := Build([]storage.TaskMeta{dated1, c1, dated2, c2, legacy, c3})
	view := g.Board()

	var order []string
	for _, grp := range view.History {
		order = append(order, grp.Epic)
	}
	want := []string{"hmiss-epic-1", "hmiss-epic-2", "hmiss-epic-legacy"}
	if !slices.Equal(order, want) {
		t.Fatalf("History group order = %v, want dated groups first (descending), legacy (no ModTime) last: %v", order, want)
	}
}

// TestBoardActiveMembersWithMissingCreateTimeSortLast: a legacy member with
// no CreateTime, mixed among dated siblings, must not crash the sort and
// must land last.
func TestBoardActiveMembersWithMissingCreateTimeSortLast(t *testing.T) {
	epic := task("mmiss-epic", "in_progress", "high", "")
	dated1 := task("mmiss-child-1", "planning", "high", "mmiss-epic")
	dated1.CreateTime = "2026-02-01"
	dated2 := task("mmiss-child-2", "planning", "high", "mmiss-epic")
	dated2.CreateTime = "2026-01-01"
	legacy := task("mmiss-child-legacy", "planning", "high", "mmiss-epic") // no CreateTime

	g := Build([]storage.TaskMeta{epic, dated1, dated2, legacy})
	view := g.Board()

	grp, ok := findGroup(view.Active, "mmiss-epic")
	if !ok {
		t.Fatal("mmiss-epic group missing")
	}
	want := []string{"mmiss-child-1", "mmiss-child-2", "mmiss-child-legacy"}
	if !slices.Equal(grp.Members, want) {
		t.Fatalf("members = %v, want dated descending then legacy (no CreateTime) last", grp.Members)
	}
}

// TestBoardHistoryMembersWithMissingModTimeSortLast mirrors the above for the
// History bucket's descending-ModTime convention.
func TestBoardHistoryMembersWithMissingModTimeSortLast(t *testing.T) {
	epic := task("hmmiss-epic", "done", "high", "")
	dated1 := task("hmmiss-child-1", "done", "high", "hmmiss-epic")
	dated1.ModTime = "2026-02-01"
	dated2 := task("hmmiss-child-2", "done", "high", "hmmiss-epic")
	dated2.ModTime = "2026-01-01"
	legacy := task("hmmiss-child-legacy", "done", "high", "hmmiss-epic") // no ModTime

	g := Build([]storage.TaskMeta{epic, dated1, dated2, legacy})
	view := g.Board()

	grp, ok := findGroup(view.History, "hmmiss-epic")
	if !ok {
		t.Fatal("hmmiss-epic group missing")
	}
	want := []string{"hmmiss-child-1", "hmmiss-child-2", "hmmiss-child-legacy"}
	if !slices.Equal(grp.Members, want) {
		t.Fatalf("members = %v, want dated descending then legacy (no ModTime) last", grp.Members)
	}
}

// TestBoardSupersessionDoesNotAffectBucketing: a cancelled task with a
// SupersededBy link buckets into History identically to one without — a
// fixture comparison, not just an absence-of-crash check.
func TestBoardSupersessionDoesNotAffectBucketing(t *testing.T) {
	plain := task("supersede-plain", "cancelled", "high", "")
	linked := task("supersede-linked", "cancelled", "high", "")
	linked.SupersededBy = "supersede-successor"
	successor := task("supersede-successor", "planning", "high", "")

	g := Build([]storage.TaskMeta{plain, linked, successor})
	view := g.Board()

	if n := countAppearances(view, "supersede-plain"); n != 1 {
		t.Fatalf("supersede-plain appears %d times, want 1", n)
	}
	if n := countAppearances(view, "supersede-linked"); n != 1 {
		t.Fatalf("supersede-linked appears %d times, want 1", n)
	}

	histGrp, ok := findGroup(view.History, "")
	if !ok {
		t.Fatal("standalone History group missing")
	}
	if !slices.Contains(histGrp.Members, "supersede-plain") || !slices.Contains(histGrp.Members, "supersede-linked") {
		t.Fatalf("both cancelled tasks must land in the same standalone History group: %v", histGrp.Members)
	}
	if slices.Contains(histGrp.Members, "supersede-successor") {
		t.Fatal("supersede-successor is still open (planning) and must not be in History")
	}

	activeGrp, ok := findGroup(view.Active, "")
	if !ok {
		t.Fatal("standalone Active group missing")
	}
	if !slices.Equal(activeGrp.Members, []string{"supersede-successor"}) {
		t.Fatalf("Active standalone members = %v, want [supersede-successor]", activeGrp.Members)
	}
}
