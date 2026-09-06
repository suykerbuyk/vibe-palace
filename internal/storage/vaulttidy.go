// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// TidyResult reports the outcome of a TidyVault sweep.
//
// Reported vs ReportedUserContent semantics: Reported is the FULL catch-all of
// everything tidy declined to sweep (it needs human eyes; never staged) and is
// preserved exactly as before so existing consumers are unaffected.
// ReportedUserContent is an ADDITIVE classified SUBSET of Reported holding the
// paths that are user-persistent memory (Projects/<slug>/memory/...). These are
// deliberately un-swept user data (committed later by wrap/SessionEnd, not by
// tidy) — expected pending content, NOT genuinely-unexpected dirt. Every path in
// ReportedUserContent is also present in Reported; nothing here is ever Swept.
type TidyResult struct {
	Swept               []string         // vault-relative paths staged+committed
	Reported            []string         // dirt that needs human eyes; never staged (full catch-all)
	ReportedUserContent []string         // subset of Reported that is user memory (Projects/<slug>/memory/...); expected, not dirt
	Deferred            []string         // transcript .jsonl.zst whose sibling .manifest.json is not yet on disk; left untracked, not committed, not blocking
	Committed           bool             // true if a commit was created
	CommitSHA           string           // short SHA of the tidy commit (empty if no-op)
	RemoteResults       map[string]error // per-remote push result (nil = success)
	PushDowngraded      bool             // push requested but no remotes → local commit only
	Stranded            bool             // commit created + push attempted but reached NO remote (local-only strand)
	PopConflict         bool             // commit landed+pushed, but autostash re-apply conflicted (edits saved in stash)
	PopConflictPaths    []string         // files left with conflict markers after the autostash re-apply
}

// SweepRule classifies a vault-relative path as a sweepable capture artifact.
//
// Matching strategy (decision M1 = option B, no new dependency): the real vault
// layout requires deep (`**`) matching — drawers nest as
// palace/<p>/drawers/<p>/<room>/drawers.jsonl and triples nest arbitrarily under
// a source-derived subpath. Go's stdlib filepath.Match has no `**`, and
// doublestar is not a dependency, so each rule carries an explicit
// segment-matcher func over parts = strings.Split(vaultRelPath, "/"). Pattern is
// documentation only (the human-readable shape Match implements) and is kept for
// the test table and audit trail.
type SweepRule struct {
	Category string
	Pattern  string                    // documentation only; e.g. "palace/*/drawers/**/*.jsonl"
	Match    func(parts []string) bool // parts = strings.Split(vaultRelPath, "/")
}

// surfaceCategory is the Category of the status-gated .surface rule. classifyDirty
// applies the H2 status gate (sweep " M", report "??") only to this rule.
const surfaceCategory = "Surface stamps (tracked, modified)"

// suffix reports whether the final path segment ends with s.
func suffix(parts []string, s string) bool { return strings.HasSuffix(parts[len(parts)-1], s) }

// sweepRules is the single source of truth for what tidy commits. Order matters
// only in that classifyDirty uses the first match; the rules are mutually
// exclusive in practice.
var sweepRules = []SweepRule{
	// Project-scoped capture output.
	{Category: "Session summaries", Pattern: "Projects/*/sessions/*.md",
		Match: func(p []string) bool {
			return len(p) == 4 && p[0] == "Projects" && p[2] == "sessions" && suffix(p, ".md")
		}},
	{Category: "Transcript archives", Pattern: "Projects/*/transcripts/*.{manifest.json,jsonl.zst}",
		Match: func(p []string) bool {
			return len(p) == 4 && p[0] == "Projects" && p[2] == "transcripts" &&
				(suffix(p, ".manifest.json") || suffix(p, ".jsonl.zst"))
		}},

	// Palace machine-generated knowledge/capture store (MUST be swept).
	{Category: "Knowledge graph entities", Pattern: "palace/*/kg/entities.jsonl",
		Match: func(p []string) bool {
			return len(p) == 4 && p[0] == "palace" && p[2] == "kg" && p[3] == "entities.jsonl"
		}},
	// H1 FIX: triples nest arbitrarily deep under a source-derived subpath — MUST
	// match deep. A single-level palace/*/kg/triples/*.json would match ZERO real
	// triples. Triple paths legitimately contain .claude/ segments (L2) — never
	// add a .claude exclusion here.
	{Category: "Knowledge graph triples", Pattern: "palace/*/kg/triples/**/*.json",
		Match: func(p []string) bool {
			return len(p) >= 5 && p[0] == "palace" && p[2] == "kg" && p[3] == "triples" && suffix(p, ".json")
		}},
	{Category: "Drawers (capture artifacts)", Pattern: "palace/*/drawers/**/*.jsonl",
		Match: func(p []string) bool {
			return len(p) >= 4 && p[0] == "palace" && p[2] == "drawers" && suffix(p, ".jsonl")
		}},

	// Vault-global audit output. Reports are machine-generated and dated; the
	// baseline is the audit's watermark. Both are FLAT under Audits/ — a nested
	// path is an unknown shape and gets human eyes (default-deny), which is why
	// these match on an exact length rather than a prefix.
	{Category: "Audit reports", Pattern: "Audits/*.md",
		Match: func(p []string) bool {
			return len(p) == 2 && p[0] == "Audits" && suffix(p, ".md")
		}},
	{Category: "Audit baseline", Pattern: "Audits/baseline.json",
		Match: func(p []string) bool {
			return len(p) == 2 && p[0] == "Audits" && p[1] == "baseline.json"
		}},

	// Surface provenance stamps — STATUS-GATED, not path-only (see H2). Match is
	// path-only here; the " M" (sweep) vs "??" (report) gate is applied by
	// classifyDirty.
	{Category: surfaceCategory, Pattern: "{Projects/*,palace/*,Templates,Audits}/.surface",
		Match: func(p []string) bool {
			if p[len(p)-1] != ".surface" {
				return false
			}
			switch {
			case len(p) == 3 && p[0] == "Projects": // Projects/<p>/.surface
				return true
			case len(p) == 3 && p[0] == "palace": // palace/<p>/.surface
				return true
			case len(p) == 2 && p[0] == "Templates": // Templates/.surface
				return true
			case len(p) == 2 && p[0] == "Audits": // Audits/.surface
				return true
			}
			return false
		}},
}

// PorcelainEntry is one record from `git status --porcelain -z`.
type PorcelainEntry struct {
	Status string // the 2-char XY code (e.g. " M", "M ", "MM", "??", "A ", "D ", "R ")
	Path   string // vault-relative path (the NEW path for renames/copies)
}

// parsePorcelainZ parses NUL-delimited `git status --porcelain -z` output into
// entries.
//
// Each ordinary record is `XY<space>PATH` terminated by NUL. A rename/copy
// record (R or C in the XY code) is followed by a SECOND NUL-separated field
// holding the OLD path; that extra field MUST be consumed or every subsequent
// record misaligns (review M2). The new (current) path is reported; rename/copy
// entries are routed to Reported by classifyDirty.
func parsePorcelainZ(out string) []PorcelainEntry {
	var entries []PorcelainEntry
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if f == "" {
			continue // trailing NUL / empty field
		}
		if len(f) < 3 {
			continue // malformed; nothing parseable
		}
		status := f[:2]
		path := f[3:] // skip the single space at index 2
		entries = append(entries, PorcelainEntry{Status: status, Path: path})

		// Rename/copy consumes the following field (the old path). Detect R/C in
		// either column and skip the extra field to keep records aligned.
		if strings.ContainsAny(status, "RC") {
			i++
		}
	}
	return entries
}

// classifyDirty routes each porcelain entry into swept (committable capture
// artifacts), reported (everything else; needs human eyes), or deferred (an
// in-flight transcript half; left for the next sweep). It is the data-driven
// heart of tidy: each path is matched against the first applicable sweepRule.
//
// Status gating (H2): for the .surface rule only, routing depends on the status
// code, not the path alone. Porcelain -z encodes status as two columns XY where
// X is the index (staged) state and Y is the worktree state:
//   - "??" → untracked (a brand-new project dir git has never seen, or a stray
//     scaffold). Untracked .surface is REPORTED — a genuinely new project gets
//     one round of human eyes; a stray Projects/p/ scaffold is caught, not
//     committed.
//   - any tracked modification (" M" worktree-modified — the hook stamp churn —
//     plus "M " staged and "MM" both-modified) → SWEPT. The gate is simply
//     "untracked ('??') reports; everything else sweeps".
//
// Transcript pair split: transcripts are written by a background hook as a PAIR
// — Projects/<slug>/transcripts/<name>.jsonl.zst (atomic temp+rename, written
// first) THEN <name>.manifest.json (written second, NOT atomic). A sweep that
// runs between the two writes would commit the .jsonl.zst without its manifest.
// So when a .jsonl.zst matches the transcript rule but its sibling manifest is
// not yet on disk (os.IsNotExist), the zst is DEFERRED — left untracked for the
// next sweep once the pair is complete. The manifest is ALWAYS swept as before:
// its zst was written first so it exists, and this also covers the steady state
// where the zst is already committed and only the manifest is now dirty. The
// probe stats the sibling on disk (NOT the dirty entry set) — gating on the
// dirty set would defer the follow-up manifest forever.
//
// All other rules sweep regardless of status, including "??" for newly created
// sessions/transcripts/drawers/triples and "D " deletes (git add stages
// deletions). Rename/copy (R/C in either column) never matches a sweep rule's
// intent — capture artifacts are append-only and timestamped, so a rename
// signals human activity — and is routed to Reported.
func classifyDirty(vaultPath string, entries []PorcelainEntry) (swept, reported, deferred []string) {
	for _, e := range entries {
		// .vp-locks/ holds transient advisory-lock sidecars — pure runtime state,
		// never content. Production vaults gitignore it so it never reaches
		// porcelain, but skip it defensively regardless: a lock file must never be
		// committed (not swept) nor block a push (not reported). This matters for
		// the repo-root commit lock, whose sidecar persists past release and would
		// otherwise be re-scanned as dirt by the post-merge sync gate.
		if strings.SplitN(e.Path, "/", 2)[0] == ".vp-locks" {
			continue
		}

		// Vault-root .vp-fs-probe-* files are the transient probes
		// check.CheckVaultFilesystem creates and deletes to test whether the
		// filesystem accepts ":" in a filename. Same class as .vp-locks above,
		// and skipped for the same reason plus one that bites sooner: a probe
		// is Reported dirt, and Reported dirt makes SyncFlow REFUSE TO SYNC.
		// The .gitignore pattern keeps it out of porcelain — but only on a
		// vault whose .gitignore has been reconciled since that pattern was
		// added, which is no existing vault until the operator deploys. Until
		// then this skip is the only thing standing between a crashed check
		// and a wedged sync.
		if !strings.Contains(e.Path, "/") && strings.HasPrefix(e.Path, ".vp-fs-probe-") {
			continue
		}

		// Rename/copy always needs human eyes regardless of path.
		if strings.ContainsAny(e.Status, "RC") {
			reported = append(reported, e.Path)
			continue
		}

		rule := matchRule(e.Path)
		if rule == nil {
			reported = append(reported, e.Path)
			continue
		}

		if rule.Category == surfaceCategory {
			// H2 status gate: untracked .surface is reported, tracked is swept.
			if e.Status == "??" {
				reported = append(reported, e.Path)
			} else {
				swept = append(swept, e.Path)
			}
			continue
		}

		// Transcript pair-split guard: a lone .jsonl.zst whose sibling manifest is
		// not yet on disk is deferred, not swept.
		if base, ok := strings.CutSuffix(e.Path, ".jsonl.zst"); ok {
			sibling := base + ".manifest.json"
			if _, err := os.Stat(filepath.Join(vaultPath, sibling)); os.IsNotExist(err) {
				deferred = append(deferred, e.Path)
				continue
			}
		}

		swept = append(swept, e.Path)
	}
	return swept, reported, deferred
}

// isUserMemoryPath reports whether a vault-relative path is user-persistent
// memory under Projects/<slug>/memory/. These files are deliberately NOT covered
// by any sweepRule (memory is user data, committed by wrap/SessionEnd, never by
// tidy); this segment check mirrors the sweepRules matchers (parts =
// strings.Split(vaultRelPath, "/")) so tidy can label them as expected user
// content rather than lumping them into the generic "needs human eyes" dirt.
func isUserMemoryPath(parts []string) bool {
	return len(parts) >= 4 && parts[0] == "Projects" && parts[2] == "memory"
}

// classifyReportedUserContent returns the subset of reported paths that are
// user-persistent memory. The returned slice is a classified view ONLY — every
// element is still present in reported (decision: Reported stays the full catch-
// all; ReportedUserContent is additive).
func classifyReportedUserContent(reported []string) []string {
	var userContent []string
	for _, p := range reported {
		if isUserMemoryPath(strings.Split(p, "/")) {
			userContent = append(userContent, p)
		}
	}
	return userContent
}

// matchRule returns the first sweepRule whose Match accepts vaultRelPath, or nil.
func matchRule(vaultRelPath string) *SweepRule {
	parts := strings.Split(vaultRelPath, "/")
	for i := range sweepRules {
		if sweepRules[i].Match(parts) {
			return &sweepRules[i]
		}
	}
	return nil
}

// scanPorcelain runs the whole-vault status scan. -uall surfaces files inside
// untracked directories (not just the directory). Output is raw (NOT trimmed)
// because records are NUL-delimited. Mirrors gitCmd's prompt-suppressing env.
func scanPorcelain(vaultPath string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", vaultPath, "status", "--porcelain", "-z", "-uall")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	return string(out), nil
}

// TidyScan runs the scan→parse→classify half of tidy and returns a TidyResult
// with Swept/Reported populated and Committed=false. It NEVER commits and NEVER
// probes remotes — it is the read-only classification path shared with
// TidyVault (one classification code path) and backs `--dry-run`.
func TidyScan(vaultPath string) (*TidyResult, error) {
	return TidyScanWithTimeout(vaultPath, DefaultTidyScanTimeout)
}

// DefaultTidyScanTimeout is the budget TidyScan gives `git status`. It is
// generous because the callers that use it — `vp vault tidy`, vp_vault_tidy,
// SyncVault — are operator-initiated and would rather wait than mis-report a
// vault as clean.
//
// 🔴 A CALLER ON A LATENCY-CRITICAL PATH MUST NOT INHERIT IT. See
// TidyScanWithTimeout.
const DefaultTidyScanTimeout = 30 * time.Second

// TidyScanWithTimeout is TidyScan with a caller-chosen budget for the single
// `git status` it shells out to.
//
// It exists for the bootstrap dirt alert, which runs on the hottest path in the
// system (the session handshake, measured at ~0.012 s) and must be BEST-EFFORT:
// a scan that cannot finish inside its budget has to degrade to "no signal"
// rather than hold the handshake for the default half-minute. The classification
// is unchanged and shared — there is still exactly one classifier — so the
// bootstrap alert and SyncVault's refusal gate cannot come to different verdicts
// about the same worktree.
func TidyScanWithTimeout(vaultPath string, timeout time.Duration) (*TidyResult, error) {
	raw, err := scanPorcelain(vaultPath, timeout)
	if err != nil {
		return nil, err
	}
	swept, reported, deferred := classifyDirty(vaultPath, parsePorcelainZ(raw))
	return &TidyResult{
		Swept:               swept,
		Reported:            reported,
		ReportedUserContent: classifyReportedUserContent(reported),
		Deferred:            deferred,
	}, nil
}

// TidyVault scans the whole vault for uncommitted dirt, classifies it into
// host-generated capture artifacts (swept) versus everything else (reported),
// and commits ONLY the swept set via CommitAndPushPaths. It NEVER runs
// `git add -A`; non-artifact dirt is reported, never staged, preserving the
// deliberate never-add-A invariant.
//
// When push is true the function probes for configured remotes; if none exist it
// downgrades to a local-only commit and sets PushDowngraded (decision #2 —
// degrade gracefully; no-remotes != offline). When remotes exist the push policy
// is passed straight through; per-remote push failures are recorded in
// RemoteResults and do NOT become a returned error (offline tolerance inherited
// from CommitAndPushPaths). Only real failures (scan/commit) return a non-nil
// error.
//
// An empty swept set is a no-op: CommitAndPushPaths errors on zero paths, so it
// is never called; the returned TidyResult has Committed=false with Reported
// populated.
func TidyVault(vaultPath string, push bool) (*TidyResult, error) {
	result, err := TidyScan(vaultPath)
	if err != nil {
		return nil, err
	}
	swept := result.Swept

	// Empty sweep set → no-op. Do NOT call CommitAndPushPaths (it errors on
	// len(paths)==0).
	if len(swept) == 0 {
		return result, nil
	}

	// Remote downgrade: requesting push against a remote-less vault would error
	// in CommitAndPushPaths ("no git remotes configured"). The shared helper
	// downgrades to a local commit instead and reports it.
	msg := tidyCommitMessage(swept)

	pushRes, downgraded, err := CommitAndPushPathsWithDowngrade(vaultPath, msg, swept, push)
	if err != nil {
		return nil, err
	}

	result.Committed = true
	result.CommitSHA = pushRes.CommitSHA
	result.RemoteResults = pushRes.RemoteResults
	result.PushDowngraded = downgraded
	result.Stranded = pushRes.Stranded()
	result.PopConflict = pushRes.PopConflict
	result.PopConflictPaths = pushRes.PopConflictPaths
	return result, nil
}

// tidyCommitMessage builds a terse, self-describing commit subject summarizing
// the sweep counts. The hostname stamp is appended by CommitAndPushPaths.
func tidyCommitMessage(swept []string) string {
	n := len(swept)
	plural := "s"
	if n == 1 {
		plural = ""
	}
	return fmt.Sprintf("vault tidy: sweep %d capture artifact%s", n, plural)
}
