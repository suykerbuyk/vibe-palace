// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"log/slog"

	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// SkipReason names which capture gate refused a run. SkipNone means no gate
// fired and the caller should proceed.
type SkipReason string

const (
	SkipNone          SkipReason = ""
	SkipNoProject     SkipReason = "no-project"
	SkipDeparted      SkipReason = "departed"
	SkipUninitialised SkipReason = "uninitialised"
)

// GateCapture is the capture opt-in gate shared by every host that writes a
// session into a vault. It answers one question — may a capture into
// (vaultRoot, projectSlug) from cwd write anything at all? — and emits the
// operator-facing log line for whichever gate refused.
//
// It is expressed over primitives, not over a host's event type, which is why
// it can be shared where hook.Run cannot: Run welds nine steps over a
// Claude-shaped Payload, and five of them (memory harvest, claim, enrichment
// drain, archive link-back, friction backfill) are either host-specific or
// budget-incompatible on another host. The three gates are none of those.
//
// 🔴 THE TWO WARN MESSAGES ARE AN INTERFACE, NOT PROSE. vplog.categorize keeps
// the text before the first colon when that index falls in (0, 40)
// (internal/vplog/summary.go), and that category is what vp_bootstrap_context's
// health alert prints to the operator ("hook stale checkout xN"). Rewording
// either message before its colon silently renames a health category. The host
// rides as an ATTRIBUTE so one category still serves every caller.
//
// It calls project.Departed and storage.ClassifyProjectDir itself rather than
// taking their results, because the log lines carry what those calls return —
// the departure's redirect and source, the project dir's state and error — and
// a gate that only logged its own arguments could not reproduce them.
//
// Every arm returns rather than falling through: a skip must happen BEFORE any
// write, since archive, harvest and capture all lazily create Projects/<slug>/.
func GateCapture(host, vaultRoot, projectSlug, cwd, sessionID string) SkipReason {
	// Opt-in gate. Auto-capture only into directories carrying an explicit
	// .vibe-palace.toml signal (cwd or a parent). Without it the CWD is not a
	// vp-managed project — e.g. the vault root (itself a git repo) or an
	// un-init'd code repo — and capturing would scaffold a stray
	// Projects/<basename>/ tree and write a claim sentinel into a non-project dir.
	if project.DetectSignal(cwd) != project.SignalVibeConfig {
		slog.Info("hook: skipping capture — no .vibe-palace.toml project signal", "cwd", cwd, "host", host)
		return SkipNoProject
	}

	// Stale-checkout gate. The marker-presence gate above does not look at the
	// NAME, and everything downstream lazily creates Projects/<slug>/. A
	// checkout whose marker still names a renamed-away project would resurrect
	// it, unattended, at every SessionEnd. Skip BEFORE any of it: the transcript
	// stays in the host's own store and the native memory is neither read nor
	// deleted, so nothing is lost. Warn, not Info — this is a checkout the
	// operator has to fix. The redirect (renamed to X / moved to vault Y, from
	// the departure record) rides as an attribute, so the category stays stable.
	//
	// This arm does NOT exist to prevent a write the writers would allow — it
	// exists to produce the right OUTCOME. Measured by deleting it and running
	// the callers' tests: archive.Create refuses a departed slug too, via
	// departedpath.RecordExists (record present) or the uninitialised check
	// (record absent — project.Departed's git-history fallback only fires when
	// Projects/<slug>/ is already gone, so the writer refuses that case as
	// "not initialised"). What changes is that a refusal there is an ERROR the
	// host reports as a failed hook, carrying the wrong diagnosis, instead of a
	// skip the operator can read. Removing this arm turns every stale checkout
	// into an apparent hook crash and loses the "hook stale checkout" health
	// category with it.
	if d, departed := project.Departed(vaultRoot, projectSlug); departed {
		slog.Warn("hook stale checkout: skipping capture — the marker names a project that departed the vault; update .vibe-palace.toml",
			"project", projectSlug, "cwd", cwd, "redirect", d.Redirect(), "source", d.Source, "host", host)
		return SkipDeparted
	}

	// Uninitialised-project gate. Every vault writer refuses a project the vault
	// has not initialised (projectdir.RefuseUninitialisedAbs): a write there
	// would create Projects/<slug>/ and its .surface stamp, and the hook commits
	// neither. Skip the whole run BEFORE archive, harvest and capture rather
	// than let each fail: the transcript stays in the host's own store and the
	// native memory is neither read nor deleted, so nothing is lost and
	// `vp archive create` recovers the transcript once the project exists.
	//
	// The writers refuse this case too, so deleting this arm would still write
	// nothing — it would return an ERROR where the caller wants a skip, which is
	// the difference between a hook that reports a crash and one that reports
	// nothing to do. The callers' tests assert the nil/skip, not just the
	// absence of files.
	if vaultRoot != "" {
		state, err := storage.ClassifyProjectDir(vaultRoot, projectSlug)
		if err != nil || !state.Initialised() {
			slog.Warn("hook uninitialised project: skipping capture — the vault has not initialised the marker's project; run `vp init <checkout>`, then recover the transcript with `vp archive create --session-id <id>`",
				"project", projectSlug, "state", state.String(), "err", err, "cwd", cwd, "session_id", sessionID, "host", host)
			return SkipUninitialised
		}
	}

	return SkipNone
}
