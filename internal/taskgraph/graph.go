// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package taskgraph derives structure from flat task metadata: which tasks are
// grouped under an epic, which are blocked by which, and in what order the open
// work can actually be cleared.
//
// The structure is DERIVED, never stored. A task file carries only its own two
// relations — a parent and a list of dependencies — and everything a reader
// wants (epics, groups, blockers, ordering, orphans) is computed from the set.
// That is deliberate: the hand-maintained ordering table this package replaces
// rotted every single time it was edited, because a cross-reference maintained
// by hand is a cross-reference that drifts. An "epic" here is not a kind of task
// and there is no epic field — it is simply a task that something points at.
//
// # This package never returns an error for bad data
//
// A vault that is already inconsistent must still render. A dangling reference,
// a dependency cycle, a retired parent with live children — these are FINDINGS,
// carried in the result, not failures that abort it. The one thing that is not
// allowed is to hang: every traversal here is iterative and every cycle is
// detected rather than walked.
//
// # internal/storage must NEVER import this package
//
// The dependency runs one way. Storage owns per-file truth under a per-path
// lock; this package owns whole-set truth and holds no lock at all. If storage
// could reach the graph, CreateTask would start validating a write against the
// entire vault — which is precisely the write-time existence check that would
// make it impossible to write a child before its epic.
package taskgraph

import (
	"cmp"
	"slices"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// RefKind distinguishes the two edge types a task can carry.
type RefKind int

const (
	// ParentRef is a **Parent:** edge (child → parent).
	ParentRef RefKind = iota
	// DependsRef is a **Depends:** edge (dependent → dependency).
	DependsRef
)

func (k RefKind) String() string {
	if k == ParentRef {
		return "parent"
	}
	return "depends"
}

// DepState is the resolution of a single dependency.
type DepState int

const (
	// DepOpen names an existing, active task that is not finished. This is the
	// only state that blocks, and the only one that contributes a sort edge.
	DepOpen DepState = iota
	// DepSatisfiedDone names a retired task: the dependency is met.
	DepSatisfiedDone
	// DepSatisfiedCancelled names a cancelled task. Also met — but kept
	// distinct from retired so a reader is never told that work was COMPLETED
	// when it was actually abandoned. Those are different facts.
	DepSatisfiedCancelled
	// DepDangling names a slug that does not exist. Reported, never a blocker:
	// failing closed on a typo would silently freeze an entire epic.
	DepDangling
)

func (s DepState) String() string {
	switch s {
	case DepSatisfiedDone:
		return "satisfied"
	case DepSatisfiedCancelled:
		return "satisfied (cancelled)"
	case DepDangling:
		return "dangling"
	default:
		return "open"
	}
}

// Dep is one resolved entry of a task's **Depends:** list.
type Dep struct {
	Slug  string   `json:"slug"`
	State DepState `json:"-"`
	// StateName is the human/JSON-facing rendering of State.
	StateName string `json:"state"`
}

// Ref is a single edge, used to report anomalies.
type Ref struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

// Node is one task plus everything derived about its place in the set.
type Node struct {
	Meta storage.TaskMeta `json:"meta"`
	// Children is every task naming this one as its parent, sorted. A non-empty
	// Children is what MAKES this node an epic — there is no epic flag.
	Children []string `json:"children,omitempty"`
	// Depends is one entry per **Depends:** slug, in file order, resolved.
	Depends []Dep `json:"depends,omitempty"`
	// Blockers is the subset of Depends that is DepOpen — the work that must
	// land before this task can start.
	Blockers []string `json:"blockers,omitempty"`
	// Depth is the length of the parent chain above this node. Capped rather
	// than looped when the chain cycles.
	Depth int `json:"depth"`
}

// IsEpic reports whether anything points at this node. Derived, never stored.
func (n *Node) IsEpic() bool { return len(n.Children) > 0 }

// Cycle is a closed loop of references.
//
// For a parent cycle, Slugs IS the loop, in chain order, rotated so the
// lexicographically smallest member comes first — a parent chain is functional
// (one parent per node), so the loop is a genuine path.
//
// For a dependency cycle, Slugs is the strongly connected component, SORTED. An
// SCC can interleave several distinct loops, so there is no single path to
// print; naming an arbitrary walk would read as authoritative and be wrong.
// Either way the rendering is stable across runs.
type Cycle struct {
	Kind  string   `json:"kind"`
	Slugs []string `json:"slugs"`
}

// Group is an epic and the open work under it. Epic == "" is the standalone
// bucket: tasks that are nobody's child and nobody's parent.
type Group struct {
	Epic    string   `json:"epic,omitempty"`
	Members []string `json:"members"`
}

// Graph is the derived whole-set view.
type Graph struct {
	Nodes map[string]*Node `json:"nodes"`
	// Epics is every node with children, sorted.
	Epics []string `json:"epics,omitempty"`
	// Order is a topological order over ACTIVE tasks: a dependency always
	// precedes its dependent. Nodes caught in a dependency cycle are excluded
	// (they have no defensible position) and appear in Cycles instead.
	Order []string `json:"order"`
	// Cycles, Dangling and StaleParents are the findings.
	Cycles   []Cycle `json:"cycles,omitempty"`
	Dangling []Ref   `json:"dangling,omitempty"`
	// StaleParents names an ACTIVE task whose parent is retired or cancelled.
	// This is exactly how finished-looking work hides unfinished work, so it is
	// surfaced rather than quietly reparented.
	StaleParents []Ref `json:"stale_parents,omitempty"`
}

// HasProblems reports whether the set carries any structural finding.
func (g *Graph) HasProblems() bool {
	return len(g.Cycles) > 0 || len(g.Dangling) > 0 || len(g.StaleParents) > 0
}

// PriorityRank orders priorities for deterministic tie-breaking. Unknown and
// empty priorities sort last — they are not an error (nothing has ever validated
// the priority string), they are simply least urgent.
func PriorityRank(p string) int {
	switch p {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	default:
		return 4
	}
}

// Build derives the graph from task metadata.
//
// 🔴 Build must be given the ARCHIVED tasks too, or every completed dependency
// reads as dangling. Production callers must use BuildFromVault, which is the
// one place that decision is made; Build stays exported for tests and stays pure.
func Build(tasks []storage.TaskMeta) *Graph {
	g := &Graph{Nodes: make(map[string]*Node, len(tasks))}

	for _, t := range tasks {
		g.Nodes[t.Slug] = &Node{Meta: t}
	}

	slugs := make([]string, 0, len(g.Nodes))
	for s := range g.Nodes {
		slugs = append(slugs, s)
	}
	slices.Sort(slugs)

	// Children, and the parent-side findings.
	for _, s := range slugs {
		n := g.Nodes[s]
		p := n.Meta.Parent
		if p == "" {
			continue
		}
		parent, ok := g.Nodes[p]
		if !ok {
			g.Dangling = append(g.Dangling, Ref{From: s, To: p, Kind: ParentRef.String()})
			continue
		}
		parent.Children = append(parent.Children, s)
		if !n.Meta.Done && parent.Meta.Done {
			g.StaleParents = append(g.StaleParents, Ref{From: s, To: p, Kind: ParentRef.String()})
		}
	}
	for _, s := range slugs {
		if n := g.Nodes[s]; n.IsEpic() {
			slices.Sort(n.Children)
			g.Epics = append(g.Epics, s)
		}
	}

	// Dependencies. Only an OPEN dep — one that exists, is active, and is not
	// finished — blocks or contributes an edge. Satisfied deps contribute no
	// edge, which is also why a satisfied dep can never be part of a cycle.
	adj := make(map[string][]string, len(g.Nodes)) // dependency -> dependents
	indeg := make(map[string]int, len(g.Nodes))
	for _, s := range slugs {
		n := g.Nodes[s]
		for _, d := range n.Meta.Depends {
			dep := Dep{Slug: d, State: DepDangling}
			target, ok := g.Nodes[d]
			switch {
			case !ok:
				g.Dangling = append(g.Dangling, Ref{From: s, To: d, Kind: DependsRef.String()})
			case target.Meta.Status == "cancelled":
				dep.State = DepSatisfiedCancelled
			case target.Meta.Done:
				dep.State = DepSatisfiedDone
			default:
				dep.State = DepOpen
			}
			dep.StateName = dep.State.String()
			n.Depends = append(n.Depends, dep)

			if dep.State == DepOpen && !n.Meta.Done {
				n.Blockers = append(n.Blockers, d)
				adj[d] = append(adj[d], s)
				indeg[s]++
			}
		}
	}

	g.Order, g.Cycles = topoSort(g, slugs, adj, indeg)
	g.Cycles = append(g.Cycles, parentCycles(g, slugs)...)
	assignDepths(g, slugs)

	slices.SortFunc(g.Cycles, func(a, b Cycle) int {
		if c := cmp.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		return slices.Compare(a.Slugs, b.Slugs)
	})
	slices.SortFunc(g.Dangling, refLess)
	slices.SortFunc(g.StaleParents, refLess)
	return g
}

func refLess(a, b Ref) int {
	if c := cmp.Compare(a.From, b.From); c != 0 {
		return c
	}
	return cmp.Compare(a.To, b.To)
}

// topoSort runs Kahn over the ACTIVE tasks' open-dependency edges. Whatever
// fails to drain is caught in (or downstream of) a cycle; those nodes are handed
// to sccCycles for enumeration rather than being reported as an ordering.
func topoSort(g *Graph, slugs []string, adj map[string][]string, indeg map[string]int) ([]string, []Cycle) {
	var ready []string
	for _, s := range slugs {
		if !g.Nodes[s].Meta.Done && indeg[s] == 0 {
			ready = append(ready, s)
		}
	}

	less := func(a, b string) int {
		na, nb := g.Nodes[a], g.Nodes[b]
		if c := cmp.Compare(PriorityRank(na.Meta.Priority), PriorityRank(nb.Meta.Priority)); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	}

	var order []string
	placed := make(map[string]bool, len(slugs))
	for len(ready) > 0 {
		slices.SortFunc(ready, less)
		s := ready[0]
		ready = ready[1:]
		order = append(order, s)
		placed[s] = true
		for _, dependent := range adj[s] {
			indeg[dependent]--
			if indeg[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
	}

	var stuck []string
	for _, s := range slugs {
		if !g.Nodes[s].Meta.Done && !placed[s] {
			stuck = append(stuck, s)
		}
	}
	if len(stuck) == 0 {
		return order, nil
	}
	return order, sccCycles(stuck, adj)
}

// sccCycles finds the strongly connected components of the stuck subgraph and
// returns those that are genuine cycles.
//
// Kahn's leftovers are NOT all cycle members — a node merely DOWNSTREAM of a
// cycle never reaches in-degree zero either. Reporting the leftovers as "the
// cycle" would name innocent tasks, so the components are computed properly.
// Tarjan, iterated with an explicit stack: recursion here would be a stack
// overflow on a deep chain, and this package's contract is that it never hangs
// and never dies.
func sccCycles(stuck []string, adj map[string][]string) []Cycle {
	inScope := make(map[string]bool, len(stuck))
	for _, s := range stuck {
		inScope[s] = true
	}
	// Edges within scope, dependency -> dependent, deterministic order.
	edges := make(map[string][]string, len(stuck))
	for _, s := range stuck {
		var out []string
		for _, d := range adj[s] {
			if inScope[d] {
				out = append(out, d)
			}
		}
		slices.Sort(out)
		edges[s] = out
	}

	var (
		index   = make(map[string]int, len(stuck))
		low     = make(map[string]int, len(stuck))
		onStack = make(map[string]bool, len(stuck))
		stack   []string
		next    int
		cycles  []Cycle
		order   = append([]string(nil), stuck...)
	)
	slices.Sort(order)

	type frame struct {
		node string
		i    int
	}

	for _, root := range order {
		if _, seen := index[root]; seen {
			continue
		}
		work := []frame{{node: root}}
		index[root], low[root] = next, next
		next++
		stack = append(stack, root)
		onStack[root] = true

		for len(work) > 0 {
			f := &work[len(work)-1]
			if f.i < len(edges[f.node]) {
				w := edges[f.node][f.i]
				f.i++
				if _, seen := index[w]; !seen {
					index[w], low[w] = next, next
					next++
					stack = append(stack, w)
					onStack[w] = true
					work = append(work, frame{node: w})
				} else if onStack[w] {
					low[f.node] = min(low[f.node], index[w])
				}
				continue
			}

			v := f.node
			work = work[:len(work)-1]
			if len(work) > 0 {
				parent := work[len(work)-1].node
				low[parent] = min(low[parent], low[v])
			}
			if low[v] != index[v] {
				continue
			}

			var comp []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				comp = append(comp, w)
				if w == v {
					break
				}
			}
			selfLoop := len(comp) == 1 && slices.Contains(edges[comp[0]], comp[0])
			if len(comp) > 1 || selfLoop {
				// A strongly connected component is a SET, not a path: it may
				// interleave several distinct loops, so there is no single
				// traversal order to report. Sort it and say so, rather than
				// printing an arbitrary walk that reads like the one true cycle.
				slices.Sort(comp)
				cycles = append(cycles, Cycle{Kind: DependsRef.String(), Slugs: comp})
			}
		}
	}
	return cycles
}

// parentCycles walks each parent chain. Every node has at most one parent, so a
// chain either terminates or closes on itself — a colored walk finds the latter
// without ever revisiting a node twice across the whole sweep.
func parentCycles(g *Graph, slugs []string) []Cycle {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(slugs))
	var cycles []Cycle

	for _, s := range slugs {
		if color[s] != white {
			continue
		}
		var path []string
		cur := s
		for cur != "" && color[cur] == white {
			color[cur] = gray
			path = append(path, cur)
			n, ok := g.Nodes[cur]
			if !ok {
				cur = ""
				break
			}
			cur = n.Meta.Parent
		}
		if cur != "" && color[cur] == gray {
			// Closed on a node in the CURRENT path: that is the cycle.
			if i := slices.Index(path, cur); i >= 0 {
				cycles = append(cycles, Cycle{Kind: ParentRef.String(), Slugs: rotate(path[i:])})
			}
		}
		for _, p := range path {
			color[p] = black
		}
	}
	return cycles
}

// assignDepths walks each parent chain, capping rather than looping when the
// chain cycles.
func assignDepths(g *Graph, slugs []string) {
	for _, s := range slugs {
		n := g.Nodes[s]
		seen := map[string]bool{s: true}
		depth := 0
		cur := n.Meta.Parent
		for cur != "" && !seen[cur] {
			parent, ok := g.Nodes[cur]
			if !ok {
				break // dangling parent: the chain stops here, it does not fail
			}
			seen[cur] = true
			depth++
			cur = parent.Meta.Parent
		}
		n.Depth = depth
	}
}

// rotate returns the slice rotated so its smallest element is first, giving a
// cycle one canonical spelling no matter which node the traversal entered from.
func rotate(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	minIdx := 0
	for i, s := range out {
		if s < out[minIdx] {
			minIdx = i
		}
	}
	return append(out[minIdx:], out[:minIdx]...)
}

// root returns the top of a node's parent chain, stopping at a dangling parent
// and capping on a cycle.
func (g *Graph) root(slug string) string {
	seen := map[string]bool{}
	cur := slug
	for {
		if seen[cur] {
			return cur
		}
		seen[cur] = true
		n, ok := g.Nodes[cur]
		if !ok || n.Meta.Parent == "" {
			return cur
		}
		if _, ok := g.Nodes[n.Meta.Parent]; !ok {
			return cur // dangling parent: this node is its own root
		}
		cur = n.Meta.Parent
	}
}

// hasResolvableParent reports whether slug names a node whose parent is set AND
// present in the node set. Whether a parent RESOLVES is a whole-set fact — a
// node cannot know from its own file whether the slug it names exists — so it is
// derived here on the graph, never stored, mirroring root(), which treats a
// dangling parent as the node being its own root.
func (g *Graph) hasResolvableParent(slug string) bool {
	n, ok := g.Nodes[slug]
	if !ok || n.Meta.Parent == "" {
		return false
	}
	_, ok = g.Nodes[n.Meta.Parent]
	return ok
}

// Role classifies a node's place in the epic/story/task hierarchy, DERIVED from
// its edges rather than stored (there is no role field, just as there is no epic
// field): a node with no children is a "task"; a node WITH children whose parent
// resolves is a "story" — an epic nested under another epic; a node with children
// whose parent is empty or dangling is a root "epic", its own root, matching
// root(). Keeping this on the graph is what lets the dangling-parent case agree
// with root() — a Node alone cannot tell a resolvable parent from a phantom one.
func (g *Graph) Role(slug string) string {
	n, ok := g.Nodes[slug]
	if !ok || !n.IsEpic() {
		return "task"
	}
	if g.hasResolvableParent(slug) {
		return "story"
	}
	return "epic"
}

// IsRootEpic reports whether slug heads its own chain (Role == "epic"). A thin
// convenience wrapper so callers read intent instead of matching on the string.
func (g *Graph) IsRootEpic(slug string) bool { return g.Role(slug) == "epic" }

// IsStory reports whether slug is an epic nested under a resolvable parent
// (Role == "story"). A thin convenience wrapper over Role.
func (g *Graph) IsStory(slug string) bool { return g.Role(slug) == "story" }

// Grouped buckets the OPEN work by the epic at the top of its parent chain.
//
// Groups are ordered by their most urgent member so the critical work is at the
// top of the screen, and the standalone bucket — tasks that are nobody's child
// and nobody's parent — comes last. Members within a group follow Order, so a
// dependency is always listed above the task it blocks.
//
// This is the byte-for-byte historical behavior: a thin wrapper over grouped
// with archived work excluded.
func (g *Graph) Grouped(includeIcebox bool) []Group {
	return g.grouped(includeIcebox, false)
}

// GroupedArchived is Grouped with the archive-inclusion decision made explicit:
// pass includeArchived=true to keep Done tasks in the buckets (an epic-close
// review wants to SEE the finished work), false to reproduce Grouped exactly.
func (g *Graph) GroupedArchived(includeIcebox, includeArchived bool) []Group {
	return g.grouped(includeIcebox, includeArchived)
}

// grouped is the shared implementation. includeArchived governs whether Done
// tasks are kept; includeIcebox whether icebox tasks are. Both default off so
// the open-work view stays uncluttered.
func (g *Graph) grouped(includeIcebox, includeArchived bool) []Group {
	orderIdx := g.orderIndex()

	byRoot := make(map[string][]string)
	for slug, n := range g.Nodes {
		if !includeArchived && n.Meta.Done {
			continue
		}
		if !includeIcebox && n.Meta.Status == storage.StatusIcebox {
			continue
		}
		r := g.root(slug)
		if r == slug {
			// This node is the top of its own chain. If anything points at it,
			// it IS the epic — it names the group and is not a member of it.
			// Listing it among its own children sorts it into the middle of
			// them, which reads as though the epic were a sibling of its own
			// work. If nothing points at it, it stands alone.
			if !n.IsEpic() {
				byRoot[""] = append(byRoot[""], slug)
			} else if _, ok := byRoot[r]; !ok {
				byRoot[r] = nil // ensure the group exists even if empty
			}
			continue
		}
		byRoot[r] = append(byRoot[r], slug)
	}

	// An epic's own priority counts toward its group's rank — otherwise a
	// critical epic whose members are all medium would sort below them.
	rank := func(grp Group) (int, int) {
		bestPri, bestOrder := 99, 1<<30
		members := grp.Members
		if grp.Epic != "" {
			members = append(append([]string(nil), members...), grp.Epic)
		}
		for _, m := range members {
			n, ok := g.Nodes[m]
			if !ok {
				continue
			}
			bestPri = min(bestPri, PriorityRank(n.Meta.Priority))
			if i, ok := orderIdx[m]; ok {
				bestOrder = min(bestOrder, i)
			}
		}
		return bestPri, bestOrder
	}

	groups := make([]Group, 0, len(byRoot))
	for epic, members := range byRoot {
		g.sortMembers(members, orderIdx)
		groups = append(groups, Group{Epic: epic, Members: members})
	}

	slices.SortFunc(groups, func(a, b Group) int {
		// Standalone bucket always last.
		if (a.Epic == "") != (b.Epic == "") {
			if a.Epic == "" {
				return 1
			}
			return -1
		}
		apri, aord := rank(a)
		bpri, bord := rank(b)
		if c := cmp.Compare(apri, bpri); c != 0 {
			return c
		}
		if c := cmp.Compare(aord, bord); c != 0 {
			return c
		}
		return cmp.Compare(a.Epic, b.Epic)
	})
	return groups
}

// Subtree returns the group rooted at root: the root slug plus every transitive
// descendant reached through Node.Children (which is direct-children-only, so
// the walk here is what makes it transitive), filtered and ordered exactly as a
// Grouped group's members. The bool reports whether root exists in the node set;
// a leaf's subtree is simply itself, which is NOT an error — callers decide
// leaf-vs-grouping via Role, not via this bool.
//
// The same filter grouped applies is applied here, to the root as well as its
// descendants: a Done member is dropped unless includeArchived, an icebox member
// unless includeIcebox. Existence and visibility are distinct facts, so a root
// that exists but is itself filtered out (a Done root without includeArchived)
// yields ok=true with an empty Members rather than ok=false — the caller asked
// "does this exist and what is open under it", and both answers are honest.
func (g *Graph) Subtree(root string, includeIcebox, includeArchived bool) (Group, bool) {
	if _, ok := g.Nodes[root]; !ok {
		return Group{Epic: root}, false
	}

	visible := func(n *Node) bool {
		if !includeArchived && n.Meta.Done {
			return false
		}
		if !includeIcebox && n.Meta.Status == storage.StatusIcebox {
			return false
		}
		return true
	}

	// BFS over Children. The seen set makes the walk transitive without ever
	// revisiting a node, which also caps a parent cycle rather than spinning on
	// it — this package's contract is that it never hangs.
	var members []string
	seen := map[string]bool{}
	queue := []string{root}
	for len(queue) > 0 {
		slug := queue[0]
		queue = queue[1:]
		if seen[slug] {
			continue
		}
		seen[slug] = true
		n, ok := g.Nodes[slug]
		if !ok {
			continue
		}
		if visible(n) {
			members = append(members, slug)
		}
		queue = append(queue, n.Children...)
	}

	g.sortMembers(members, g.orderIndex())
	return Group{Epic: root, Members: members}, true
}

// orderIndex inverts Order into slug -> position for O(1) lookups.
func (g *Graph) orderIndex() map[string]int {
	orderIdx := make(map[string]int, len(g.Order))
	for i, s := range g.Order {
		orderIdx[s] = i
	}
	return orderIdx
}

// sortMembers orders slugs in place exactly the way a group's Members are
// ordered, factored out of grouped so Grouped and Subtree share ONE ordering and
// cannot drift. The listing is a PRE-ORDER walk of the parent forest — the only
// ordering in which a nested epic prints above its own child (a flat sort put
// "leaf" above "mid" alphabetically, and the indentation then read as a child
// parenting its parent). Within a level, dependency order wins (a dependency
// precedes what it blocks), then priority, then slug.
func (g *Graph) sortMembers(members []string, orderIdx map[string]int) {
	// nodeLess ranks two tasks at the same level: dependency order first, then
	// priority, then slug.
	nodeLess := func(a, b string) int {
		ia, oka := orderIdx[a]
		ib, okb := orderIdx[b]
		switch {
		case oka && okb && ia != ib:
			return cmp.Compare(ia, ib)
		case oka != okb:
			// A cycle member has no position in Order; list it after the tasks
			// that do, rather than pretending it has one.
			if oka {
				return -1
			}
			return 1
		}
		return cmp.Compare(a, b)
	}

	// ancestry returns the chain from the top of the parent chain down to m,
	// capped on a cycle.
	ancestry := func(m string) []string {
		var chain []string
		seen := map[string]bool{}
		for cur := m; cur != "" && !seen[cur]; {
			seen[cur] = true
			chain = append(chain, cur)
			n, ok := g.Nodes[cur]
			if !ok {
				break
			}
			cur = n.Meta.Parent
		}
		slices.Reverse(chain)
		return chain
	}

	paths := make(map[string][]string, len(members))
	for _, m := range members {
		paths[m] = ancestry(m)
	}
	slices.SortFunc(members, func(a, b string) int {
		pa, pb := paths[a], paths[b]
		for i := 0; i < len(pa) && i < len(pb); i++ {
			if pa[i] != pb[i] {
				return nodeLess(pa[i], pb[i])
			}
		}
		// One chain is a prefix of the other: the ancestor comes first.
		return cmp.Compare(len(pa), len(pb))
	})
}
