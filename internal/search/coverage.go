// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Index coverage (index-coverage-instrument; ADR-014 decision 8). Coverage
// answers, for one project, which of seven ordered states its host-local
// search index is in and why, counting SESSIONS (decision 7), never archive
// files. It is computed only from records search would itself answer from —
// TrulyEmpty and Stale (this package), the completeness record's built tiers,
// the migration marker and tracked-drawer presence, the transcript manifests
// and the ingest ledger — and the run lock's advisory holder record. It opens
// no archive, loads no embedder and takes no lock (Scope 10, Scope 5).
//
// The seven states, tested in this order, first match wins (Scope 1).

// CoverageState is one of the seven ordered index-coverage states.
type CoverageState string

const (
	// CoverageAbsent: the project is truly empty (TrulyEmpty) — nothing any
	// tier could index.
	CoverageAbsent CoverageState = "absent"
	// CoverageStale: Stale() holds — a fingerprint mismatch, or missing vectors
	// on ledgered archives.
	CoverageStale CoverageState = "stale"
	// CoverageLegacy: the vault carries no migration marker and tracked drawers
	// answer search (the glide path). It outranks unbuilt deliberately.
	CoverageLegacy CoverageState = "legacy"
	// CoverageUnbuilt: content exists but completeness.json records no built
	// tier on this host (a fresh clone; an ingest-only host).
	CoverageUnbuilt CoverageState = "unbuilt"
	// CoverageNotes: notes and iterations are built and the project has no
	// archive with a live session (m = 0).
	CoverageNotes CoverageState = "notes"
	// CoveragePartial: the project has live sessions and n < m (includes n = 0,
	// pending archives and a non-empty baseline set).
	CoveragePartial CoverageState = "partial"
	// CoverageCurrent: m > 0 and n = m.
	CoverageCurrent CoverageState = "current"
)

// Coverage is one project's index-coverage state, its reason, and the session
// counts behind it. It is the one shared struct `vp index status`,
// vp_index_status and the bootstrap instrument all return (Scope 6).
type Coverage struct {
	Project string        `json:"project"`
	State   CoverageState `json:"state"`
	Reason  string        `json:"reason"`
	// N is live sessions whose latest ledger record is live and names the live
	// archive's source_sha256; M is live sessions (a session with at least one
	// tracked archive). A session archived on two days counts once (decision 7).
	N int `json:"n"`
	M int `json:"m"`
	// Pending, Backlog and Failing split the m-n sessions not yet ingested:
	// backlog is the ledger's baseline set (only `vp index rebuild` clears it),
	// pending is the rest (the ingester drains them), and failing is the pending
	// at or over the ingester's failure limit (Scope 4).
	Pending int `json:"pending"`
	Backlog int `json:"backlog"`
	Failing int `json:"failing"`
	// Run is the index run in progress on this host, when the holder record
	// names a live pid; nil otherwise (Scope 5). It is vault-wide, read once.
	Run *CoverageRun `json:"run,omitempty"`
}

// CoverageRun is the advisory run-lock holder, reported only while its pid is
// alive. Reading it never takes the run lock (ADR-014 lines 609-621).
type CoverageRun struct {
	PID       int                  `json:"pid"`
	Kind      string               `json:"kind"`
	Project   string               `json:"project,omitempty"`
	StartTime time.Time            `json:"start_time"`
	Progress  *indexstore.Progress `json:"progress,omitempty"`
}

// Seams so a test can count file operations and control pid liveness without a
// real run (Scope 10 cost bound; Scope 5 run-in-progress). The store read uses
// the package's existing readStoreFn seam.
var (
	coverageListEntries      = archive.ListEntries
	coverageReadCompleteness = indexstore.ReadCompleteness
	coverageReadHolder       = indexstore.ReadHolder
	coverageRunAlive         = pidSignalAlive
)

// coverageFailureLimit mirrors ingest.DefaultFailureLimit (how many failures
// make the automatic ingester skip an archive until `vp index rebuild`). It is
// a local copy because internal/ingest imports this package, so this package
// cannot import it; TestCoverageFailureLimitMatchesIngest in cmd/vp (which
// imports both) guards the two against drift. It is a var so a test can set a
// smaller limit.
var coverageFailureLimit = 3

// IndexCoverage is the shared single-project derivation: state, reason, the
// session counts, and the run in progress. `vp index status` and
// vp_index_status both call it, so the two agree by construction. It loads no
// embedder and takes no lock.
func (e *Engine) IndexCoverage(project string) (Coverage, error) {
	cov, err := e.CoverageState(project)
	if err != nil {
		return Coverage{}, err
	}
	run, err := e.RunInProgress()
	if err != nil {
		return Coverage{}, err
	}
	cov.Run = run
	return cov, nil
}

// RunInProgress reads the run lock's advisory holder record WITHOUT taking the
// lock, and reports the run only while the pid it names is alive. A missing
// record, an unparseable one, or one naming a dead pid reports no run (nil).
// This is read once per vault (Scope 5, Scope 10).
func (e *Engine) RunInProgress() (*CoverageRun, error) {
	h, err := coverageReadHolder(e.vault)
	if err != nil {
		// ErrHolderUnknown means no record, or an unparseable one: no run.
		if errors.Is(err, indexstore.ErrHolderUnknown) {
			return nil, nil
		}
		return nil, err
	}
	if !coverageRunAlive(h.PID) {
		return nil, nil
	}
	return &CoverageRun{
		PID:       h.PID,
		Kind:      string(h.Kind),
		Project:   h.Project,
		StartTime: h.StartTime,
		Progress:  h.Progress,
	}, nil
}

// CoverageState derives a project's state, reason and session counts without
// the run in progress (which bootstrap reads once per vault, not once per
// project). IndexCoverage attaches the run; bootstrap calls this directly.
func (e *Engine) CoverageState(project string) (Coverage, error) {
	cov := Coverage{Project: project}

	// 1. absent — the one truly-empty predicate (child 2).
	empty, err := e.TrulyEmpty(project)
	if err != nil {
		return Coverage{}, err
	}
	if empty {
		cov.State = CoverageAbsent
		cov.Reason = "truly empty: nothing to index"
		return cov, nil
	}

	// 2. stale — a fingerprint mismatch, or missing vectors on ledgered
	// archives. Stale() compares the fingerprint strings without loading the
	// model, so this reads stale after an upgrade before any search has run.
	stale, reasons, err := e.Stale(project)
	if err != nil {
		return Coverage{}, err
	}

	// The session counts feed legacy's progress and the notes/partial/current
	// split. Compute them once, before the state branches that need them.
	n, m, pending, backlog, failing, err := e.coverageCounts(project)
	if err != nil {
		return Coverage{}, err
	}
	cov.N, cov.M, cov.Pending, cov.Backlog, cov.Failing = n, m, pending, backlog, failing

	if stale {
		rec, err := coverageReadCompleteness(e.vault, project)
		if err != nil {
			return Coverage{}, err
		}
		cov.State = CoverageStale
		cov.Reason = staleReason(project, reasons, rec.LocalMisses)
		return cov, nil
	}

	// 3. legacy — no migration marker and tracked drawers answer search. It
	// outranks unbuilt deliberately: a fresh clone of an unmigrated vault reads
	// legacy, with "not built yet on this host" and the ingester's progress.
	migrated, err := storage.VaultMigrated(e.vault.Root)
	if err != nil {
		return Coverage{}, err
	}
	rec, err := coverageReadCompleteness(e.vault, project)
	if err != nil {
		return Coverage{}, err
	}
	built := len(rec.Tiers) > 0
	if !migrated {
		drawers, err := e.HasTrackedDrawers(project)
		if err != nil {
			return Coverage{}, err
		}
		if drawers {
			cov.State = CoverageLegacy
			cov.Reason = legacyReason(project, built, n, m, pending, backlog)
			return cov, nil
		}
	}

	// 4. unbuilt — content exists but no built tier on this host. A missing
	// fingerprint lands here, never in stale (child 2 handles that in Stale()).
	if !built {
		cov.State = CoverageUnbuilt
		cov.Reason = "not built yet on this host"
		return cov, nil
	}

	// 5. notes — built, and the project has no archive with a live session.
	if m == 0 {
		cov.State = CoverageNotes
		cov.Reason = "notes and iterations built; the project has no archives"
		return cov, nil
	}

	// 6. partial — live sessions and n < m (includes n = 0 and a backlog).
	if n < m {
		cov.State = CoveragePartial
		cov.Reason = partialReason(project, n, m, pending, backlog, failing)
		return cov, nil
	}

	// 7. current — m > 0 and n = m.
	cov.State = CoverageCurrent
	cov.Reason = fmt.Sprintf("all %d sessions ingested", m)
	return cov, nil
}

// coverageCounts counts sessions against the ledger, reading the manifests and
// the ledger only — it never opens or hashes an archive (Scope 2). m is live
// sessions; n is those whose latest ledger record is live and names the live
// archive's source_sha256 and is not superseding. The m-n remainder splits into
// pending, backlog and failing.
func (e *Engine) coverageCounts(project string) (n, m, pending, backlog, failing int, err error) {
	entries, err := coverageListEntries(e.vault.Root, project)
	if err != nil {
		return 0, 0, 0, 0, 0, err
	}
	// One live archive per session: the latest captured_at. ListEntries sorts
	// oldest first, so the last entry for a session_id wins.
	live := map[string]*archive.Entry{}
	order := make([]string, 0)
	for _, en := range entries {
		if en.Manifest == nil || en.Manifest.SessionID == "" {
			continue
		}
		sid := en.Manifest.SessionID
		if _, seen := live[sid]; !seen {
			order = append(order, sid)
		}
		live[sid] = en
	}
	m = len(live)
	if m == 0 {
		return 0, 0, 0, 0, 0, nil
	}

	st, err := readStoreFn(e.vault, project)
	if err != nil {
		return 0, 0, 0, 0, 0, err
	}
	ledger := st.Ledger()

	// With no ledger yet (no ingest run has created it on this host), every live
	// archive is backlog, which is what the ledger's creation will record
	// (ADR-014 decision 2).
	if ledger == nil || !ledger.Exists() {
		return 0, m, 0, m, 0, nil
	}

	for _, sid := range order {
		en := live[sid]
		sha := en.Manifest.SourceSHA256
		rec, ok := ledger.Session(sid)
		// Match on source_sha256, never the archive path (ADR-014 lines
		// 571-573). Only a manifest with no source_sha256 falls back to the
		// ledger's recorded path to recover the hash ING stored for it.
		if sha == "" && ok && samePath(rec.ArchivePath, en.ArchivePath, e.vault.Root) {
			sha = rec.SHA
		}
		ingested := ok && rec.State == indexstore.StateLive && sha != "" && rec.SHA == sha
		if ingested {
			n++
			continue
		}
		// Not ingested: pending or backlog, and failing when at the limit. A
		// failure record is never an ingest and never makes a session count in
		// n (ADR-014 lines 557-560): failures land here, as pending/failing.
		if sha != "" && ledger.InBaseline(sha) {
			backlog++
			continue
		}
		pending++
		if sha != "" && ledger.FailureCount(sha) >= coverageFailureLimit {
			failing++
		}
	}
	return n, m, pending, backlog, failing, nil
}

// samePath reports whether the ledger's recorded (vault-relative or absolute)
// archive path names the same file as entry's absolute archive path. Used only
// for the rare manifest with no source_sha256.
func samePath(recorded, entryAbs, vaultRoot string) bool {
	if recorded == "" {
		return false
	}
	if recorded == entryAbs {
		return true
	}
	return strings.TrimPrefix(strings.TrimPrefix(entryAbs, vaultRoot), "/") ==
		strings.TrimPrefix(strings.TrimPrefix(recorded, vaultRoot), "/")
}

// staleReason renders the stale reason: the chunk-recipe or embedder
// fingerprint mismatch (cleared by `vp index rebuild`), and/or the missing
// vectors on ledgered archives with their count (cleared by the ingester's
// repair pass or a rebuild). Both when both are set.
func staleReason(project string, reasons []indexstore.StaleReason, localMisses int) string {
	var parts []string
	for _, r := range reasons {
		switch {
		case r.Kind == indexstore.StaleFingerprint && r.Fingerprint == indexstore.FingerprintChunks:
			parts = append(parts, fmt.Sprintf("chunk-recipe fingerprint mismatch — run `vp index rebuild %s`", project))
		case r.Kind == indexstore.StaleFingerprint && r.Fingerprint == indexstore.FingerprintEmbed:
			parts = append(parts, fmt.Sprintf("embedder fingerprint mismatch — run `vp index rebuild %s`", project))
		case r.Kind == indexstore.StaleMissingVectors:
			parts = append(parts, fmt.Sprintf("%d missing vector(s) on ledgered archives — the ingester's repair pass or `vp index rebuild %s` clears it", localMisses, project))
		}
	}
	if len(parts) == 0 {
		// A persisted stale flag with an unrecognised reason still carries one.
		return fmt.Sprintf("stale — run `vp index rebuild %s`", project)
	}
	return strings.Join(parts, "; ")
}

// legacyReason renders the glide-path reason: the marker is absent and tracked
// drawers answer, plus "not built yet on this host" when no tier is built, plus
// the ingester's progress (n of m, pending and backlog).
func legacyReason(project string, built bool, n, m, pending, backlog int) string {
	parts := []string{"no migration marker: tracked drawers answer search"}
	if !built {
		parts = append(parts, "not built yet on this host")
	}
	parts = append(parts, ingesterProgress(project, n, m, pending, backlog, 0))
	return strings.Join(parts, "; ")
}

// partialReason names n of m, then the pending count (the ingester drains them)
// and the backlog count (only `vp index rebuild` clears it) SEPARATELY, and the
// failing count when any archive has stopped being retried.
func partialReason(project string, n, m, pending, backlog, failing int) string {
	return ingesterProgress(project, n, m, pending, backlog, failing)
}

// ingesterProgress is the shared "n of m … pending … backlog … failing" text,
// naming the ingester for pending and `vp index rebuild <p>` for backlog and
// failing (Scope 4).
func ingesterProgress(project string, n, m, pending, backlog, failing int) string {
	parts := []string{fmt.Sprintf("%d of %d sessions ingested", n, m)}
	if pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending (the ingester takes them on its next runs)", pending))
	}
	if backlog > 0 {
		parts = append(parts, fmt.Sprintf("%d backlog — run `vp index rebuild %s`", backlog, project))
	}
	if failing > 0 {
		parts = append(parts, fmt.Sprintf("%d failing — run `vp index rebuild %s`", failing, project))
	}
	return strings.Join(parts, "; ")
}

// pidSignalAlive reports whether pid names a live process (signal 0). A process
// alive but owned by another user (EPERM) counts as alive. It is the coverage
// path's copy of the holder-liveness check; the tools package has its own.
func pidSignalAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
