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
	Projects  []string // the projects visited, in order
	Stopped   string   // why the run stopped early: "budget", "wall clock", "checkpoint"
	Passes    int      // RunHeld passes: 1, plus one per re-acquire after the release
}

// ErrNoTarget: a run was not given a vault root and a project.
var ErrNoTarget = errors.New("ingest: a run needs a vault root and a project")

// noteFirstTimeout bounds a losing trigger's wait for the commit lock to
// record its First in the inbox.
var noteFirstTimeout = 5 * time.Second

// beforeReleaseFn, when a test sets it, runs after the last rescan and before
// the run lock is released.
var beforeReleaseFn func()

// Run is the pending-archive ingester (Scope 4). It try-locks the index run
// lock; when another run holds it, it records First in the project's inbox
// (plan revision R3) and returns at once. Otherwise it runs RunHeld, then
// releases the lock through ReleaseAndRecheck with the in-scope pending set
// its last rescan saw, and runs again when an archive arrived that the rescan
// did not see (ADR-014 decision 7, "No lost trigger").
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
	for {
		r, err := RunHeld(ctx, d, held, o)
		res.add(r)
		res.Passes++
		if err != nil {
			_ = held.Release()
			return res, err
		}
		if r.Stopped == "checkpoint" {
			return res, held.Release()
		}
		pending := func() ([]string, error) { return inScopePending(d, o) }
		seenList, err := pending()
		if err != nil {
			_ = held.Release()
			return res, err
		}
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
	r.Projects = append(r.Projects, o.Projects...)
	if o.Stopped != "" {
		r.Stopped = o.Stopped
	}
}

// runState is one RunHeld's bookkeeping.
type runState struct {
	start     time.Time
	budget    RunBudget
	admitted  int             // archives committed or attempted: the budget's count
	attempted map[string]bool // never retried in the same run
	skipped   map[string]bool // projects skipped for a fingerprint reason: warned once
	done      int
	total     int
	stopped   string
}

// admit reports whether another archive may start: the first archive of a
// run always may; after that, the archive count and the wall-clock cap,
// checked between archives through the injected clock.
func (st *runState) admit(d Deps, explicit bool) bool {
	if explicit || st.admitted == 0 {
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
// rebuild driver, 7-S4): it never takes, releases or re-acquires it. The
// triggering project goes first, then every other project with archives, in
// slug order. Each pass over the projects is repeated while it made progress
// and the budget lasts; an archive is never retried within one RunHeld.
func RunHeld(ctx context.Context, d Deps, held *indexstore.RunLock, o RunOptions) (RunResult, error) {
	var res RunResult
	st := &runState{start: d.now(), budget: o.budget(), attempted: map[string]bool{}, skipped: map[string]bool{}}
	projects, err := projectOrder(d, o.Project)
	if err != nil {
		return res, err
	}
	for {
		before := st.admitted
		for _, p := range projects {
			if st.stopped != "" {
				break
			}
			if err := held.SetProject(p); err != nil {
				return res, err
			}
			if !slices.Contains(res.Projects, p) {
				res.Projects = append(res.Projects, p)
			}
			first := ""
			if p == o.Project {
				first = o.First
			}
			if err := runProject(ctx, d, held, o, p, first, st, &res); err != nil {
				return res, err
			}
		}
		if st.stopped != "" || st.admitted == before {
			break
		}
	}
	res.Stopped = st.stopped
	return res, nil
}

// projectOrder is the triggering project, then every other project in slug
// order.
func projectOrder(d Deps, first string) ([]string, error) {
	all, err := d.Vault.ListAllProjects()
	if err != nil {
		return nil, err
	}
	out := []string{first}
	var rest []string
	for _, p := range all {
		if p.Slug != first {
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
}

// runProject is one project's pass: its pending archives in scope, newest
// first (First first), then its inbox upkeep and its older archives recorded
// superseded.
func runProject(ctx context.Context, d Deps, held *indexstore.RunLock, o RunOptions, p, first string, st *runState, res *RunResult) error {
	if st.skipped[p] {
		return nil
	}
	if skip, err := staleSkip(d, p); err != nil || skip {
		st.skipped[p] = skip
		return err
	}
	entries, err := archive.ListEntries(d.Vault.Root, p)
	if err != nil {
		slog.Warn("ingest: cannot list archives", "project", p, "error", err)
		return nil
	}
	if len(entries) == 0 {
		return nil
	}
	firsts, err := ensureLedger(ctx, d, p, first)
	if errors.Is(err, indexstore.ErrProjectGone) {
		return nil
	}
	if err != nil {
		return err
	}
	snap, err := indexstore.ReadStore(d.Vault, p)
	if err != nil {
		return err
	}
	items := plan(entries, snap.Ledger(), firsts, o, st.attempted, d.Vault.Root, true)
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
	return tidyProject(ctx, d, p, entries)
}

// staleSkip reports whether project is stale for a fingerprint reason: its
// chunk recipe or embedding regime is another's. It is skipped, never
// discarded, with one Warn naming `vp index rebuild` (Chair ruling 2: a Warn
// reaches the bootstrap health alert).
func staleSkip(d Deps, p string) (bool, error) {
	_, reasons, err := d.Engine.Stale(p)
	if err != nil {
		return false, err
	}
	for _, r := range reasons {
		if r.Kind == indexstore.StaleFingerprint {
			slog.Warn("ingest: project skipped: its index was built under another "+r.Fingerprint+" fingerprint; run `vp index rebuild`",
				"project", p, "fingerprint", r.Fingerprint)
			return true, nil
		}
	}
	return false, nil
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
// live archive is the newest listed one (latest captured_at) on disk. When
// warn is set, the skips that need the operator are logged.
func plan(entries []*archive.Entry, l *indexstore.Ledger, firsts map[string]bool, o RunOptions, attempted map[string]bool, root string, warn bool) []item {
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
		if it, ok := decide(sid, newest, list, l, firsts, o, attempted, root, warn); ok {
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
	o RunOptions, attempted map[string]bool, root string, warn bool) (item, bool) {
	sha := newest.Manifest.SourceSHA256
	logf := func(msg string, args ...any) {
		if warn {
			slog.Warn(msg, append([]any{"session", sid, "archive", newest.ArchivePath}, args...)...)
		}
	}
	if sha == "" {
		if !o.Explicit {
			// R4: an archive with no source_sha256 can be keyed only by
			// reading it, so an automatic run would re-read it every time;
			// it is left to `vp index rebuild`.
			logf("ingest: archive has no source_sha256; left to `vp index rebuild`")
			return item{}, false
		}
		return item{e: newest}, true
	}
	if attempted[sha] || l.Superseded(sha) {
		return item{}, false
	}
	rec, has := l.Session(sid)
	it := item{e: newest}
	switch {
	case !has:
	case rec.State == indexstore.StateLive && rec.SHA == sha:
		return item{}, false
	case rec.State == indexstore.StateLive:
		if !listedSHA(list, rec.SHA) && !newerThan(newest.Manifest.CapturedAt, rec.CapturedAt) &&
			rec.ArchivePath != vaultRel(root, newest.ArchivePath) {
			// The ledgered live archive is gone and nothing listed is
			// known to be newer: never roll the session back.
			logf("ingest: the session's ingested archive is no longer on disk and no newer one is; left to `vp index rebuild`",
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
	case l.InBaseline(sha) && !firsts[sha]:
		return item{}, false
	case l.FailureCount(sha) >= o.failureLimit() && !superseding:
		// R2: never for a session mid-supersede, which search would
		// otherwise lose until a rebuild.
		return item{}, false
	case newest.Manifest.SourceBytes > o.maxSourceBytes():
		logf("ingest: archive is larger than an automatic run takes; left to `vp index rebuild`",
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
// the project's pass must end (the project is gone or stale).
func ingestOne(ctx context.Context, d Deps, held *indexstore.RunLock, o RunOptions, p string, it item, st *runState, res *RunResult) (bool, error) {
	sha := it.e.Manifest.SourceSHA256
	r, err := IngestArchive(ctx, d, p, it.e, IngestOptions{Embed: true, Checkpoint: o.Checkpoint, Retarget: it.retarget})
	if r.SHA != "" {
		sha = r.SHA
	}
	switch {
	case err == nil && (r.Outcome == AlreadyLedgered || r.Outcome == RecordedOlder):
		// Another run did the work: no budget, no progress (R6).
		return false, nil
	case err == nil:
		res.Committed++
	case errors.Is(err, ErrCheckpoint):
		slog.Warn("ingest: run stopped at a checkpoint", "project", p, "archive", it.e.ArchivePath, "error", err)
		st.stopped = "checkpoint"
		return true, nil
	case errors.Is(err, indexstore.ErrProjectGone):
		// R6: the project went away; no failure, on to the next project.
		slog.Info("ingest: project removed during the run", "project", p)
		return true, nil
	case errors.Is(err, ErrFingerprintStale):
		slog.Warn("ingest: project skipped: its index was built under another chunk recipe or embedding regime; run `vp index rebuild`",
			"project", p, "error", err)
		return true, nil
	case errors.Is(err, archive.ErrArchiveChanged):
		// Adopted note: rewritten since the listing; retried next run.
		slog.Info("ingest: archive rewritten during the run; retried next run", "project", p, "archive", it.e.ArchivePath)
		res.Changed++
	case errors.Is(err, indexstore.ErrNoLedger):
		return true, nil
	default:
		res.Failed++
		slog.Warn("ingest: archive failed", "project", p, "archive", it.e.ArchivePath, "source_sha256", sha, "error", err)
		if ferr := recordFailure(ctx, d, p, it.e.Manifest.SessionID, sha, err); ferr != nil {
			slog.Warn("ingest: could not record the failure", "project", p, "source_sha256", sha, "error", ferr)
		}
	}
	st.admitted++
	st.attempted[sha] = true
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
	projects, err := projectOrder(d, o.Project)
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
			return nil, err
		}
		inbox, err := indexstore.ReadFirsts(d.Vault, p)
		if err != nil {
			return nil, err
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
		for _, it := range plan(entries, snap.Ledger(), firsts, o, nil, d.Vault.Root, false) {
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
