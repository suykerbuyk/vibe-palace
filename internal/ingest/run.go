// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
)

// The automatic run's defaults. They are UNMEASURED: Scope 6's measurement
// (an operator-provided stripped copy of the live vault, never a quantum
// project) has not been made, so these are small, stated placeholders (plan
// revisions D13 and R5) to be replaced from the measured numbers.
const (
	// DefaultRunArchives is how many archives one automatic run ingests or
	// attempts (UNMEASURED).
	DefaultRunArchives = 3
	// DefaultRunWallClock caps one automatic run, checked between archives
	// (UNMEASURED).
	DefaultRunWallClock = 10 * time.Minute
	// DefaultFailureLimit is how many failures make automatic runs skip an
	// archive until an explicit rebuild (UNMEASURED).
	DefaultFailureLimit = 3
	// DefaultMaxSourceBytes is the largest archive (manifest source_bytes)
	// an automatic run ingests; a larger one is left to `vp index rebuild`
	// (UNMEASURED).
	DefaultMaxSourceBytes int64 = 32 << 20
)

// RunBudget bounds one automatic run (ADR-014 decision 7, "The per-run
// budget"): archives per run, with a wall-clock cap as a backstop.
type RunBudget struct {
	Archives  int
	WallClock time.Duration
}

// RunOptions are a run's arguments (Scope 4, "Arguments"). The ingester
// resolves nothing: VaultRoot and Project come from the trigger.
type RunOptions struct {
	VaultRoot string
	Project   string
	// First is the source_sha256 of the archive the trigger just created, or
	// empty (a pull, a clone, `vp mcp` startup).
	First string
	// Explicit is true only for the rebuild driver: every pending archive,
	// no baseline set, no failure limit, no size cap, no budget.
	Explicit bool
	// Only, when set, limits the pass to this single project (the rebuild
	// driver's per-project explicit pass, so other projects are served only by
	// its later automatic-scope rescan). Empty means the triggering project
	// first, then the rest in slug order.
	Only string
	// Skip names projects left out of the pass entirely: not visited, not
	// ingested, not rescanned (`vp index rebuild --skip`). A skipped project's
	// ledger, baseline set and chunks are untouched.
	Skip []string
	// MaxArchives, when > 0, stops the pass after it has attempted that many
	// archives, even in Explicit mode where there is otherwise no budget
	// (`vp index rebuild --max-archives N`). The run then does not complete.
	MaxArchives int
	// NoEmbed makes every IngestArchive write chunks, local KG and the ledger
	// but embed nothing, and skips the repair pass's vector re-embed
	// (`vp index rebuild --no-embed`). The automatic ingester never sets it.
	NoEmbed bool
	// Checkpoint is passed to every IngestArchive; an error stops the run.
	Checkpoint func(n int) error
	// SkipHeal skips the graph heal for Project (the rebuild heals it once
	// itself, after its final engine rebuild).
	SkipHeal bool

	// Zero values mean the defaults.
	Budget         RunBudget
	FailureLimit   int
	MaxSourceBytes int64
}

// skipped reports whether project is named by Skip.
func (o RunOptions) skipped(project string) bool {
	return slices.Contains(o.Skip, project)
}

func (o RunOptions) budget() RunBudget {
	b := o.Budget
	if b.Archives <= 0 {
		b.Archives = DefaultRunArchives
	}
	if b.WallClock <= 0 {
		b.WallClock = DefaultRunWallClock
	}
	return b
}

func (o RunOptions) failureLimit() int {
	if o.FailureLimit > 0 {
		return o.FailureLimit
	}
	return DefaultFailureLimit
}

func (o RunOptions) maxSourceBytes() int64 {
	if o.MaxSourceBytes > 0 {
		return o.MaxSourceBytes
	}
	return DefaultMaxSourceBytes
}

// RunResult reports a run.
type RunResult struct {
	LockHeld  bool     // another run held the run lock; this one did nothing
	Committed int      // archives committed or superseded
	Failed    int      // archives that failed (a failure record each)
	Changed   int      // archives rewritten mid-run, left for the next run
	Repaired  int      // archives re-ingested by the repair pass
	Projects  []string // the projects the run visited (had archives or a ledger), in order
	Stopped   string   // why the run stopped early: "budget", "wall clock", "max archives", "checkpoint", "cancelled"
	Passes    int      // RunHeld passes, including re-runs before the release and after a re-acquire
	// FailedSHAs is the source_sha256 of every archive that got a failure
	// record in this run, so the rebuild driver can prove a completed rebuild
	// (RunLock.CompletedRebuild) that keeps those failures named.
	FailedSHAs []string
}

// ErrNoTarget: a run was not given a vault root and a project.
var ErrNoTarget = errors.New("ingest: a run needs a vault root and a project")

// noteFirstTimeout bounds a losing trigger's wait for the commit lock to
// record its First in the inbox.
var noteFirstTimeout = 5 * time.Second

// beforeReleaseFn, when a test sets it, runs after the last rescan and before
// the run lock is released.
var beforeReleaseFn func()

// runMemo is what one Run remembers across its passes and lock acquisitions:
// the archives it attempted (never retried in the same run) and the Warns it
// wrote (each once per archive per run).
type runMemo struct {
	attempted map[string]bool
	warned    map[string]bool
}

func newMemo() *runMemo { return &runMemo{attempted: map[string]bool{}, warned: map[string]bool{}} }

// attemptKey is how a run remembers an archive: its source_sha256, or, for
// an archive with none, its path, so a failing hashless archive is attempted
// once (code review fix 2).
func attemptKey(e *archive.Entry, sha string) string {
	if sha != "" {
		return sha
	}
	return "path:" + e.ArchivePath
}

// warnOnce logs a Warn the first time key is seen in this run.
func (m *runMemo) warnOnce(key, msg string, args ...any) {
	if m == nil || m.warned[key] {
		return
	}
	m.warned[key] = true
	slog.Warn(msg, args...)
}

// Run is the pending-archive ingester (Scope 4). It try-locks the index run
// lock; when another run holds it, it records First in the project's inbox
// (plan revision R3) and returns at once. Otherwise it runs the pass, then
// rescans: while the rescan finds an in-scope archive this run has not
// attempted and the budget is not spent, it runs the pass again, so an
// archive that arrived during the pass (or during its heal) is served now
// (code review fix 1). Then it releases the lock through ReleaseAndRecheck,
// with the set that last rescan saw, and runs again, with a fresh budget,
// when an archive arrived after it (ADR-014 decision 7, "No lost trigger").
func Run(ctx context.Context, d Deps, o RunOptions) (RunResult, error) {
	var res RunResult
	if o.VaultRoot == "" || o.Project == "" || d.Vault == nil {
		return res, ErrNoTarget
	}
	held, ok, err := indexstore.TryRunLock(d.Vault, indexstore.KindIngest, o.Project)
	if err != nil {
		return res, err
	}
	if !ok {
		res.LockHeld = true
		if o.First != "" && !o.Explicit {
			if err := indexstore.NoteFirst(ctx, d.Vault, o.Project, o.First, noteFirstTimeout); err != nil {
				slog.Warn("ingest: the run lock is held, and the trigger's archive could not be recorded for the holder",
					"project", o.Project, "source_sha256", o.First, "error", err)
			}
		}
		return res, nil
	}
	memo := newMemo()
	pending := func() ([]string, error) { return inScopePending(d, o) }
	for {
		st := newState(d, o, memo)
		var seenList []string
		for {
			before := st.progress()
			r, err := runHeld(ctx, d, held, o, st)
			res.add(r)
			res.Passes++
			if err != nil {
				_ = held.Release()
				return res, err
			}
			if st.stopped == "checkpoint" || st.stopped == "cancelled" {
				return res, held.Release()
			}
			seenList, err = pending()
			if err != nil {
				_ = held.Release()
				return res, err
			}
			unattempted := slices.ContainsFunc(seenList, func(s string) bool { return !memo.attempted[s] })
			if !unattempted || st.stopped != "" || st.progress() == before {
				break
			}
		}
		res.Stopped = st.stopped
		seen := map[string]struct{}{}
		for _, s := range seenList {
			seen[s] = struct{}{}
		}
		if beforeReleaseFn != nil {
			beforeReleaseFn()
		}
		again, err := held.ReleaseAndRecheck(seen, pending)
		if err != nil || !again {
			return res, err
		}
		o.First = ""
	}
}

func (r *RunResult) add(o RunResult) {
	r.Committed += o.Committed
	r.Failed += o.Failed
	r.Changed += o.Changed
	r.Repaired += o.Repaired
	for _, p := range o.Projects {
		if !slices.Contains(r.Projects, p) {
			r.Projects = append(r.Projects, p)
		}
	}
	for _, s := range o.FailedSHAs {
		if !slices.Contains(r.FailedSHAs, s) {
			r.FailedSHAs = append(r.FailedSHAs, s)
		}
	}
	if o.Stopped != "" {
		r.Stopped = o.Stopped
	}
}

// runState is the bookkeeping of one lock acquisition: its budget, and what
// its passes did.
type runState struct {
	memo        *runMemo
	start       time.Time
	budget      RunBudget
	maxArchives int             // MaxArchives cap (0 = none), honoured even in Explicit mode
	admitted    int             // archives and repair units committed or attempted: the budget's count
	skipped     map[string]bool // projects skipped (stale, or an error reading them): warned once
	repaired    map[string]bool // projects whose repair pass ran
	commits     map[string]int  // commits per project, for the heal
	healedAt    map[string]int  // commits per project when it was last healed
	firsts      map[string]map[string]bool
	visited     []string
	done        int
	total       int
	committed   int
	stopped     string
}

func newState(d Deps, o RunOptions, memo *runMemo) *runState {
	return &runState{
		memo: memo, start: d.now(), budget: o.budget(), maxArchives: o.MaxArchives,
		skipped: map[string]bool{}, repaired: map[string]bool{}, commits: map[string]int{}, healedAt: map[string]int{},
		firsts: map[string]map[string]bool{},
	}
}

// progress is what a pass changed: a pass that changes nothing ends the
// re-runs, so a run never loops on an archive it cannot attempt.
func (st *runState) progress() int { return st.admitted + st.committed }

func (st *runState) visit(p string) {
	if !slices.Contains(st.visited, p) {
		st.visited = append(st.visited, p)
	}
}

// admit reports whether another archive or repair unit may start: the first
// of an acquisition always may; after that, the MaxArchives cap (honoured even
// in Explicit mode), then, for an automatic run, the archive count and the
// wall-clock cap, checked between archives through the injected clock.
func (st *runState) admit(d Deps, explicit bool) bool {
	if st.admitted == 0 {
		return true
	}
	if st.maxArchives > 0 && st.admitted >= st.maxArchives {
		st.stopped = "max archives"
		return false
	}
	if explicit {
		return true
	}
	if st.admitted >= st.budget.Archives {
		st.stopped = "budget"
		return false
	}
	if d.now().Sub(st.start) >= st.budget.WallClock {
		st.stopped = "wall clock"
		return false
	}
	return true
}

// RunHeld is the pass for a caller that already holds the run lock (the
// rebuild driver, 7-S4): it never takes, releases or re-acquires it.
func RunHeld(ctx context.Context, d Deps, held *indexstore.RunLock, o RunOptions) (RunResult, error) {
	st := newState(d, o, newMemo())
	r, err := runHeld(ctx, d, held, o, st)
	r.Stopped = st.stopped
	return r, err
}

// runHeld is one pass:
//
//  1. every in-scope project's ledger is created up front, so a budget stop
//     cannot let archives that arrive meanwhile join a baseline created later;
//  2. the pending archives, the triggering project first, then the others in
//     slug order, repeated while a pass makes progress and the budget lasts;
//  3. the repair pass, only after every project's pending archives, so a
//     repair that keeps failing cannot starve another project (fix 4);
//  4. the graph heal of each visited project, after its last commit.
//
// An error reading one project is logged and that project skipped; it never
// stops the run for the others.
func runHeld(ctx context.Context, d Deps, held *indexstore.RunLock, o RunOptions, st *runState) (RunResult, error) {
	var res RunResult
	projects, err := projectOrder(d, o)
	if err != nil {
		return res, err
	}
	for _, p := range projects {
		if err := ctx.Err(); err != nil {
			st.stopped = "cancelled"
			res.Stopped = st.stopped
			return res, nil
		}
		if st.skipped[p] || st.firsts[p] != nil {
			continue
		}
		// An explicit rebuild processes a fingerprint-stale project (it has
		// discarded and reset the fingerprint first); only the automatic pass
		// skips one and leaves it to `vp index rebuild` (ruling B, round 7).
		if !o.Explicit {
			if skip := staleSkip(d, p, st); skip {
				continue
			}
		}
		entries, err := archive.ListEntries(d.Vault.Root, p)
		if err != nil {
			st.skip(p, "ingest: cannot list the project's archives; skipped this run", err)
			continue
		}
		if len(entries) == 0 {
			continue
		}
		first := ""
		if p == o.Project {
			first = o.First
		}
		firsts, err := ensureLedger(ctx, d, p, first)
		if errors.Is(err, indexstore.ErrProjectGone) {
			continue
		}
		if err != nil {
			st.skip(p, "ingest: cannot create the project's ledger; skipped this run", err)
			continue
		}
		st.firsts[p] = firsts
	}
	for {
		before := st.progress()
		for _, p := range projects {
			if st.stopped != "" {
				break
			}
			if err := held.SetProject(p); err != nil {
				return res, err
			}
			if err := runPending(ctx, d, held, o, p, st, &res); err != nil {
				return res, err
			}
		}
		if st.stopped != "" || st.progress() == before {
			break
		}
	}
	for _, p := range projects {
		if st.stopped != "" {
			break
		}
		if st.skipped[p] || st.repaired[p] {
			continue
		}
		st.repaired[p] = true
		if err := repairProject(ctx, d, o, p, st, &res); err != nil {
			st.skip(p, "ingest: the repair pass failed; skipped this run", err)
		}
	}
	// The graph heal, once per visited project after its last commit, with
	// the run lock held; never for a project skipped as stale, nor for the
	// project whose rebuild heals it itself (SkipHeal).
	for _, p := range st.visited {
		if st.skipped[p] || (o.SkipHeal && p == o.Project) || st.stopped == "checkpoint" || st.stopped == "cancelled" {
			continue
		}
		if h, ok := st.healedAt[p]; ok && h == st.commits[p] {
			continue
		}
		if !o.Explicit && d.now().Sub(st.start) >= st.budget.WallClock {
			break
		}
		st.healedAt[p] = st.commits[p]
		healGraph(ctx, d, p)
	}
	res.Projects = slices.Clone(st.visited)
	res.Stopped = st.stopped
	return res, nil
}

// skip marks a project skipped for the rest of this acquisition, with one
// Warn per run.
func (st *runState) skip(p, msg string, err error) {
	st.skipped[p] = true
	st.memo.warnOnce("skip:"+p+":"+msg, msg, "project", p, "error", err)
}

// projectOrder is the order a pass visits projects: with Only set, that one
// project alone; otherwise the triggering project, then every other project in
// slug order. Projects named by Skip are left out of either form.
func projectOrder(d Deps, o RunOptions) ([]string, error) {
	if o.Only != "" {
		if o.skipped(o.Only) {
			return nil, nil
		}
		return []string{o.Only}, nil
	}
	all, err := d.Vault.ListAllProjects()
	if err != nil {
		return nil, err
	}
	first := o.Project
	out := []string{}
	if first != "" && !o.skipped(first) {
		out = append(out, first)
	}
	var rest []string
	for _, p := range all {
		if p.Slug != first && !o.skipped(p.Slug) {
			rest = append(rest, p.Slug)
		}
	}
	sort.Strings(rest)
	return append(out, rest...), nil
}

// item is one archive a project's pass ingests.
type item struct {
	e        *archive.Entry
	retarget string
	revert   bool // the session reverts to its source archive (R1)
}

// runPending is one project's pending archives in scope, newest first (First
// and inbox entries first), then its older archives recorded superseded and
// its inbox upkeep.
func runPending(ctx context.Context, d Deps, held *indexstore.RunLock, o RunOptions, p string, st *runState, res *RunResult) error {
	if st.skipped[p] {
		return nil
	}
	entries, err := archive.ListEntries(d.Vault.Root, p)
	if err != nil {
		st.skip(p, "ingest: cannot list the project's archives; skipped this run", err)
		return nil
	}
	if len(entries) == 0 {
		return nil
	}
	firsts := st.firsts[p]
	if firsts == nil {
		// Archives that arrived after the up-front step, in a project that
		// had none then.
		first := ""
		if p == o.Project {
			first = o.First
		}
		if firsts, err = ensureLedger(ctx, d, p, first); err != nil {
			if !errors.Is(err, indexstore.ErrProjectGone) {
				st.skip(p, "ingest: cannot create the project's ledger; skipped this run", err)
			}
			return nil
		}
		st.firsts[p] = firsts
	}
	snap, err := indexstore.ReadStore(d.Vault, p)
	if err != nil {
		st.skip(p, "ingest: cannot read the project's index store; skipped this run", err)
		return nil
	}
	st.visit(p)
	items := plan(entries, snap.Ledger(), firsts, o, st.memo, d.Vault.Root)
	st.total += len(items)
	for _, it := range items {
		if !st.admit(d, o.Explicit) {
			return nil
		}
		stop, err := ingestOne(ctx, d, held, o, p, it, st, res)
		if err != nil {
			return err
		}
		if stop {
			break
		}
	}
	if st.skipped[p] || st.stopped == "cancelled" {
		return nil
	}
	if err := tidyProject(ctx, d, p, entries); err != nil && !errors.Is(err, indexstore.ErrProjectGone) {
		st.skip(p, "ingest: cannot tidy the project's ledger; skipped this run", err)
	}
	return nil
}

// staleSkip reports whether project is stale for a fingerprint reason: its
// chunk recipe or embedding regime is another's. It is skipped, never
// discarded, with one Warn naming `vp index rebuild` (Chair ruling 2: a Warn
// reaches the bootstrap health alert). An error reading its state skips it
// for this run, with a Warn, and never stops the run.
func staleSkip(d Deps, p string, st *runState) bool {
	_, reasons, err := d.Engine.Stale(p)
	if err != nil {
		st.skip(p, "ingest: cannot read the project's stale state; skipped this run", err)
		return true
	}
	for _, r := range reasons {
		if r.Kind == indexstore.StaleFingerprint {
			st.skipped[p] = true
			st.memo.warnOnce("stale:"+p, "ingest: project skipped: its index was built under another "+r.Fingerprint+" fingerprint; run `vp index rebuild`",
				"project", p, "fingerprint", r.Fingerprint)
			return true
		}
	}
	return false
}

// ensureLedger creates the project's ledger when it has none, leaving the
// trigger's First and every inbox entry out of the baseline set, and returns
// those (they are in scope even if already in the set, plan revision R3).
func ensureLedger(ctx context.Context, d Deps, p, first string) (map[string]bool, error) {
	tx, err := indexstore.Lock(ctx, d.Vault, p, indexstore.NoTimeout)
	if err != nil {
		return nil, err
	}
	defer tx.Release()
	inbox, err := tx.Firsts()
	if err != nil {
		return nil, err
	}
	exclude := slices.Clone(inbox)
	if first != "" {
		exclude = append(exclude, first)
	}
	if _, err := tx.EnsureLedger(exclude); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, s := range exclude {
		out[s] = true
	}
	return out, nil
}

// plan returns the project's archives a run ingests, newest first, the
// trigger's First (or an inbox entry) ahead of the rest. For each session the
// live archive is the newest listed one (latest captured_at) on disk. With a
// memo, the skips that need the operator are logged once per archive per run
// and attempted archives are left out; with none (the rescan) nothing is
// logged.
func plan(entries []*archive.Entry, l *indexstore.Ledger, firsts map[string]bool, o RunOptions, memo *runMemo, root string) []item {
	bySession := map[string][]*archive.Entry{}
	var order []string
	for _, e := range entries {
		if e.Manifest == nil || e.Manifest.SessionID == "" {
			continue
		}
		sid := e.Manifest.SessionID
		if _, ok := bySession[sid]; !ok {
			order = append(order, sid)
		}
		bySession[sid] = append(bySession[sid], e)
	}
	var out []item
	for _, sid := range order {
		list := bySession[sid]
		newest := list[0]
		for _, e := range list[1:] {
			if newerThan(e.Manifest.CapturedAt, newest.Manifest.CapturedAt) {
				newest = e
			}
		}
		if it, ok := decide(sid, newest, list, l, firsts, o, memo, root); ok {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		fi, fj := firsts[out[i].e.Manifest.SourceSHA256], firsts[out[j].e.Manifest.SourceSHA256]
		if fi != fj {
			return fi
		}
		return newerThan(out[i].e.Manifest.CapturedAt, out[j].e.Manifest.CapturedAt)
	})
	return out
}

// decide is one session's choice: whether its newest listed archive is
// pending and in scope, and with which re-target.
func decide(sid string, newest *archive.Entry, list []*archive.Entry, l *indexstore.Ledger, firsts map[string]bool,
	o RunOptions, memo *runMemo, root string) (item, bool) {
	sha := newest.Manifest.SourceSHA256
	warn := func(kind, msg string, args ...any) {
		memo.warnOnce(kind+":"+newest.ArchivePath, msg, append([]any{"session", sid, "archive", newest.ArchivePath}, args...)...)
	}
	// An archive this run already attempted is never retried in it, hashless
	// ones included (fix 2).
	if memo != nil && memo.attempted[attemptKey(newest, sha)] {
		return item{}, false
	}
	if sha == "" {
		if !o.Explicit {
			// R4: an archive with no source_sha256 can be keyed only by
			// reading it, so an automatic run would re-read it every time;
			// it is left to `vp index rebuild`.
			warn("nohash", "ingest: archive has no source_sha256; left to `vp index rebuild`")
			return item{}, false
		}
		return item{e: newest}, true
	}
	rec, has := l.Session(sid)
	it := item{e: newest}
	switch {
	case has && rec.State == indexstore.StateSuperseding && sha == rec.SupersedingFrom && !listedSHA(list, rec.SHA):
		// R1: the supersede's target vanished and nothing newer is on
		// disk: the session reverts to its source archive.
		it.retarget, it.revert = rec.SHA, true
	case l.Superseded(sha):
		return item{}, false
	case !has:
	case rec.State == indexstore.StateLive && rec.SHA == sha:
		return item{}, false
	case rec.State == indexstore.StateLive:
		if !listedSHA(list, rec.SHA) && !newerThan(newest.Manifest.CapturedAt, rec.CapturedAt) &&
			rec.ArchivePath != vaultRel(root, newest.ArchivePath) {
			// The ledgered live archive is gone and nothing listed is
			// known to be newer: never roll the session back.
			warn("gone", "ingest: the session's ingested archive is no longer on disk and no newer one is; left to `vp index rebuild`",
				"ledgered", rec.SHA)
			return item{}, false
		}
	case rec.State == indexstore.StateSuperseding && rec.SHA != sha:
		it.retarget = rec.SHA
	}
	if o.Explicit {
		return it, true
	}
	superseding := has && rec.State == indexstore.StateSuperseding
	switch {
	case l.InBaseline(sha) && !firsts[sha] && !superseding:
		return item{}, false
	case l.FailureCount(sha) >= o.failureLimit() && !superseding:
		// R2: never for a session mid-supersede, which search would
		// otherwise lose until a rebuild.
		return item{}, false
	case newest.Manifest.SourceBytes > o.maxSourceBytes():
		warn("size", "ingest: archive is larger than an automatic run takes; left to `vp index rebuild`",
			"source_bytes", newest.Manifest.SourceBytes, "limit", o.maxSourceBytes())
		return item{}, false
	}
	return it, true
}

func listedSHA(list []*archive.Entry, sha string) bool {
	for _, e := range list {
		if e.Manifest.SourceSHA256 == sha {
			return true
		}
	}
	return false
}

// ingestOne ingests one archive and accounts for it. It reports stop when
// the project's pass must end (the project is gone or stale, or the run is
// stopping).
func ingestOne(ctx context.Context, d Deps, held *indexstore.RunLock, o RunOptions, p string, it item, st *runState, res *RunResult) (bool, error) {
	sha := it.e.Manifest.SourceSHA256
	r, err := IngestArchive(ctx, d, p, it.e, IngestOptions{Embed: !o.NoEmbed, Checkpoint: o.Checkpoint, Retarget: it.retarget})
	if r.SHA != "" {
		sha = r.SHA
	}
	switch {
	case err == nil && (r.Outcome == AlreadyLedgered || r.Outcome == RecordedOlder):
		// Another run did the work: no budget, no progress (R6).
		return false, nil
	case err == nil:
		res.Committed++
		st.committed++
		st.commits[p]++
		if it.revert {
			// The rollback is never silent.
			slog.Info("ingest: a session mid-supersede reverted to its source archive: its target is no longer on disk and no newer archive is",
				"project", p, "session", it.e.Manifest.SessionID, "archive", it.e.ArchivePath, "source_sha256", sha, "vanished", it.retarget)
		}
	case errors.Is(err, ErrCheckpoint):
		slog.Warn("ingest: run stopped at a checkpoint", "project", p, "archive", it.e.ArchivePath, "error", err)
		st.stopped = "checkpoint"
		return true, nil
	case ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		// A cancelled run is not the archive's fault.
		slog.Info("ingest: run cancelled", "project", p, "archive", it.e.ArchivePath)
		st.stopped = "cancelled"
		return true, nil
	case errors.Is(err, indexstore.ErrProjectGone):
		// R6: the project went away; no failure, on to the next project.
		slog.Info("ingest: project removed during the run", "project", p)
		return true, nil
	case errors.Is(err, ErrFingerprintStale):
		st.skipped[p] = true
		st.memo.warnOnce("stale:"+p, "ingest: project skipped: its index was built under another chunk recipe or embedding regime; run `vp index rebuild`",
			"project", p, "error", err)
		return true, nil
	case errors.Is(err, indexstore.ErrNoLedger):
		slog.Warn("ingest: the project's ledger disappeared during the run (a discard?); its pass ends", "project", p)
		return true, nil
	case errors.Is(err, archive.ErrArchiveChanged):
		// Adopted note: rewritten since the listing; retried next run.
		slog.Info("ingest: archive rewritten during the run; retried next run", "project", p, "archive", it.e.ArchivePath)
		res.Changed++
	default:
		res.Failed++
		if sha != "" {
			res.FailedSHAs = append(res.FailedSHAs, sha)
		}
		slog.Warn("ingest: archive failed", "project", p, "archive", it.e.ArchivePath, "source_sha256", sha, "error", err)
		if ferr := recordFailure(ctx, d, p, it.e.Manifest.SessionID, sha, err); ferr != nil {
			slog.Warn("ingest: could not record the failure", "project", p, "source_sha256", sha, "error", ferr)
		}
	}
	st.admitted++
	st.memo.attempted[attemptKey(it.e, it.e.Manifest.SourceSHA256)] = true
	if sha != "" {
		st.memo.attempted[sha] = true
	}
	st.done++
	if err := held.SetProgress(st.done, st.total); err != nil {
		return false, err
	}
	return false, nil
}

func recordFailure(ctx context.Context, d Deps, p, session, sha string, cause error) error {
	if sha == "" {
		return nil
	}
	tx, err := indexstore.Lock(ctx, d.Vault, p, indexstore.NoTimeout)
	if err != nil {
		return err
	}
	defer tx.Release()
	if err := tx.RecordFailure(session, sha, cause); err != nil {
		return err
	}
	return tx.Commit()
}

// tidyProject records each session's older listed archives superseded once
// its newer archive is ledgered (they are never pending again, ADR-014 line
// 588), and drops served inbox entries: those now ledgered live or
// superseded, or no longer on disk.
func tidyProject(ctx context.Context, d Deps, p string, entries []*archive.Entry) error {
	tx, err := indexstore.Lock(ctx, d.Vault, p, indexstore.NoTimeout)
	if errors.Is(err, indexstore.ErrProjectGone) {
		return nil
	}
	if err != nil {
		return err
	}
	defer tx.Release()
	l, err := tx.Ledger()
	if err != nil {
		return err
	}
	if !l.Exists() {
		return nil
	}
	listed := map[string]bool{}
	for _, e := range entries {
		sha, sid := e.Manifest.SourceSHA256, e.Manifest.SessionID
		if sha == "" || sid == "" {
			continue
		}
		listed[sha] = true
		rec, has := l.Session(sid)
		if !has || l.Superseded(sha) || rec.SHA == sha || rec.SupersedingFrom == sha || rec.State != indexstore.StateLive {
			continue
		}
		if newerThan(e.Manifest.CapturedAt, rec.CapturedAt) || rec.CapturedAt == "" {
			continue // newer than the live one, or unknown: a pending supersede
		}
		if err := tx.RecordSuperseded(sid, sha, vaultRel(d.Vault.Root, e.ArchivePath)); err != nil {
			return err
		}
	}
	inbox, err := tx.Firsts()
	if err != nil {
		return err
	}
	var served []string
	for _, s := range inbox {
		live := false
		for _, e := range entries {
			if e.Manifest.SourceSHA256 == s {
				if rec, ok := l.Session(e.Manifest.SessionID); ok && rec.State == indexstore.StateLive && rec.SHA == s {
					live = true
				}
			}
		}
		if live || l.Superseded(s) || !listed[s] {
			served = append(served, s)
		}
	}
	if err := tx.DropFirsts(served); err != nil {
		return err
	}
	return tx.Commit()
}

// inScopePending is every in-scope pending source_sha256 across the vault's
// projects, read without a lock: the set a run's last rescan saw, and the
// predicate ReleaseAndRecheck calls after the release. A gone project has no
// pending archive, so it never makes a run re-acquire (R6).
func inScopePending(d Deps, o RunOptions) ([]string, error) {
	projects, err := projectOrder(d, o)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range projects {
		if ok, err := d.Vault.ProjectExists(p); err != nil || !ok {
			continue
		}
		if _, reasons, err := d.Engine.Stale(p); err == nil && hasFingerprintReason(reasons) {
			continue
		}
		entries, err := archive.ListEntries(d.Vault.Root, p)
		if err != nil || len(entries) == 0 {
			continue
		}
		snap, err := indexstore.ReadStore(d.Vault, p)
		if err != nil {
			continue // the pass logged it; one unreadable project never stops the rest
		}
		inbox, err := indexstore.ReadFirsts(d.Vault, p)
		if err != nil {
			continue
		}
		firsts := map[string]bool{}
		for _, s := range inbox {
			firsts[s] = true
		}
		if !snap.Ledger().Exists() {
			// No ledger yet: only the trigger's archives are in scope; the
			// rest becomes the baseline set when the ledger is created.
			for s := range firsts {
				out = append(out, s)
			}
			continue
		}
		for _, it := range plan(entries, snap.Ledger(), firsts, o, nil, d.Vault.Root) {
			out = append(out, it.e.Manifest.SourceSHA256)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func hasFingerprintReason(rs []indexstore.StaleReason) bool {
	for _, r := range rs {
		if r.Kind == indexstore.StaleFingerprint {
			return true
		}
	}
	return false
}

// now is the injected clock.
func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}
